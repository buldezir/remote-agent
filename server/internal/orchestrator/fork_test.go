package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"remote-agent/internal/gitx"
	"remote-agent/internal/harness"
	"remote-agent/internal/harness/fake"
	"remote-agent/internal/model"
	"remote-agent/internal/store"
)

func (e *env) fork(p ForkParams) *model.Session {
	e.t.Helper()
	s, err := e.o.Fork(bg, p)
	if err != nil {
		e.t.Fatalf("fork: %v", err)
	}
	return s
}

func (e *env) lastReply(sessionID string) string {
	e.t.Helper()
	msgs := byKind(e.items(sessionID), model.ItemAssistantMessage)
	if len(msgs) == 0 {
		e.t.Fatal("no reply")
	}
	return msgs[len(msgs)-1].Text
}

// plainAgent is the fake agent under another name, which can't fork.
type plainAgent struct{}

func (plainAgent) ID() string { return "plain" }
func (plainAgent) Probe(ctx context.Context) model.HarnessInfo {
	info := fake.Harness{}.Probe(ctx)
	info.ID, info.Name = "plain", "Plain"
	return info
}
func (plainAgent) Open(ctx context.Context, o harness.OpenOptions) (harness.Runtime, error) {
	return fake.Harness{}.Open(ctx, o)
}

// brokenFork is the fake agent failing to fork, as a CLI that can't find
// the message to cut the conversation at does.
type brokenFork struct{ fake.Harness }

func (h brokenFork) Open(ctx context.Context, o harness.OpenOptions) (harness.Runtime, error) {
	if o.Fork {
		return nil, errors.New("no message found")
	}
	return h.Harness.Open(ctx, o)
}

func TestForkInWorktree(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	src := e.create(CreateSessionParams{Mode: "auto", Prompt: "write new.txt", Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree}})
	e.waitTurn(src.ID, 1)
	e.prompt(src.ID, "write a.txt", "")
	e.waitTurn(src.ID, 2)
	srcItems := e.items(src.ID)

	f := e.fork(ForkParams{CommandID: "f1", SessionID: src.ID, Turn: 1, Prompt: "continue"})
	if f.ForkedFrom == nil || *f.ForkedFrom != (model.ForkedFrom{SessionID: src.ID, Turn: 1}) {
		t.Errorf("forkedFrom = %+v", f.ForkedFrom)
	}
	if f.Title != "write new.txt (fork)" || f.Harness != "fake" || f.Mode != "auto" {
		t.Errorf("fork = %+v", f)
	}
	ws := f.Workspace
	if ws.Kind != model.WorkspaceWorktree || ws.Path == src.Workspace.Path || !isProvisional(ws.Branch, f.ID) {
		t.Fatalf("workspace = %+v", ws)
	}
	// The files are as they were after turn 1: new.txt, and a.txt as it was.
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(ws.Path, name))
		if err != nil {
			return "<missing>"
		}
		return string(b)
	}
	if read("new.txt") != "hello from fake\n" || read("a.txt") != "original\n" {
		t.Errorf("fork's files: new=%q a=%q", read("new.txt"), read("a.txt"))
	}
	if b := gitx.CurrentBranch(bg, ws.Path); b != ws.Branch {
		t.Errorf("fork's branch = %s", b)
	}

	// The fork's agent copied the source's conversation, cut after turn 1.
	e.waitTurn(f.ID, 2)
	if got, want := e.lastReply(f.ID), fmt.Sprintf("Forked fake-%s at fake-%s#1. Echo: continue", src.ID, src.ID); got != want {
		t.Errorf("reply = %q, want %q", got, want)
	}
	if s := e.session(f.ID); s.NativeID != "fake-"+f.ID {
		t.Errorf("fork's native id = %q", s.NativeID)
	}

	// Its transcript: turn 1's, a notice, then its own turn 2.
	items, turns := e.items(f.ID), e.turns(f.ID)
	if got := kinds(items); got != "user_message,reasoning,tool_call,assistant_message,notice,user_message,reasoning,assistant_message" {
		t.Fatalf("items = %s", got)
	}
	checkOrder(t, items, turns)
	if items[0].Text != "write new.txt" || items[0].ID == srcItems[0].ID || items[0].TurnID != turns[0].ID {
		t.Errorf("copied prompt = %+v", items[0])
	}
	if items[4].Text != "Forked from “write new.txt” after turn 1." {
		t.Errorf("notice = %q", items[4].Text)
	}
	if len(turns) != 2 || turns[0].N != 1 || turns[1].N != 2 || turns[0].ID == e.turns(src.ID)[0].ID {
		t.Fatalf("turns = %+v", turns)
	}
	// The copied turn keeps its changes, in checkpoints of the fork's own.
	if sha, err := gitx.Resolve(bg, e.dir, checkpointRef(f.ID, 1, "after")); err != nil || sha != turns[0].CheckpointAfter {
		t.Errorf("checkpoint ref = %s, %v", sha, err)
	}
	if d, err := e.o.TurnDiff(bg, turns[0].ID, nil); err != nil || fileStats(d.Files) != "added new.txt" {
		t.Errorf("copied turn's diff = %+v, %v", d, err)
	}

	// The source is as it was.
	if got := kinds(e.items(src.ID)); got != kinds(srcItems) {
		t.Errorf("source items = %s", got)
	}
	if b, _ := os.ReadFile(filepath.Join(src.Workspace.Path, "a.txt")); string(b) != "hello from fake\n" {
		t.Errorf("source a.txt = %q", b)
	}

	// A retry of the command returns the same fork.
	again, err := e.o.Fork(bg, ForkParams{CommandID: "f1", SessionID: src.ID, Turn: 1, Prompt: "continue"})
	if err != nil || again.ID != f.ID {
		t.Errorf("retried fork = %v, %v", again, err)
	}
}

func TestForkToAnotherAgent(t *testing.T) {
	t.Parallel()
	e := newEnvAt(t, t.TempDir(), true, fake.Harness{}, plainAgent{})
	src := e.create(CreateSessionParams{Prompt: "hello there"})
	e.waitTurn(src.ID, 1)
	e.prompt(src.ID, "and again", "")
	e.waitTurn(src.ID, 2)

	// In the project folder, with the plain agent, which can't fork.
	f := e.fork(ForkParams{SessionID: src.ID, Harness: "plain", Prompt: "now blue"})
	if f.Workspace.Kind != model.WorkspaceRoot || f.Workspace.Path != e.dir || f.Mode != "ask" || f.ForkedFrom.Turn != 2 {
		t.Errorf("fork = %+v", f)
	}
	e.waitTurn(f.ID, 3)
	// The fake echoes the words of the prompt.
	reply := strings.Join(strings.Fields(e.lastReply(f.ID)), " ")
	for _, want := range []string{
		"Echo: [Note from the user's tooling: this session is a fork of an earlier conversation, after its turn 2",
		"<transcript> User: hello there Assistant: Echo: hello there User: and again Assistant: Echo: and again",
		"[Forked from “hello there” after turn 2.] </transcript> now blue",
	} {
		if !strings.Contains(reply, want) {
			t.Errorf("reply lacks %q:\n%s", want, reply)
		}
	}
	if strings.Contains(reply, "Considering:") || strings.Contains(reply, "Forked fake-") {
		t.Errorf("reply has the agent's reasoning, or a native fork:\n%s", reply)
	}
	// Its turns are its own: the plain agent's points would be the fake's.
	for _, turn := range e.turns(f.ID)[:2] {
		if turn.ForkPoint != "" {
			t.Errorf("copied turn %d has fork point %q", turn.N, turn.ForkPoint)
		}
	}

	// Only the first prompt brings the conversation.
	e.prompt(f.ID, "and green", "")
	e.waitTurn(f.ID, 4)
	if got := e.lastReply(f.ID); got != "Echo: and green" {
		t.Errorf("second reply = %q", got)
	}
}

func TestForkFallsBackToText(t *testing.T) {
	t.Parallel()
	e := newEnvAt(t, t.TempDir(), false, brokenFork{})
	src := e.create(CreateSessionParams{Prompt: "hello"})
	e.waitTurn(src.ID, 1)
	f := e.fork(ForkParams{SessionID: src.ID})
	if f.Workspace.Kind != model.WorkspaceRoot {
		t.Errorf("workspace = %+v", f.Workspace)
	}
	e.prompt(f.ID, "go on", "")
	e.waitTurn(f.ID, 2)
	notices := byKind(e.items(f.ID), model.ItemNotice)
	if len(notices) != 2 || notices[1].Text != "Could not copy the conversation into the fork; the agent gets it as text with the next prompt." {
		t.Errorf("notices = %+v", notices)
	}
	if reply := strings.Join(strings.Fields(e.lastReply(f.ID)), " "); !strings.Contains(reply, "<transcript> User: hello Assistant: Echo: hello") || !strings.HasSuffix(reply, "</transcript> go on") {
		t.Errorf("reply = %q", reply)
	}
}

func TestHandoffLines(t *testing.T) {
	exit := 1
	for _, c := range []struct {
		item *model.Item
		want string
	}{
		{&model.Item{Kind: model.ItemUserMessage, TurnID: "t", Text: " Fix it ", Images: []model.ImageRef{{}, {}}}, "User:\nFix it [2 image(s)]"},
		{&model.Item{Kind: model.ItemUserMessage, Text: "never ran"}, ""},
		{&model.Item{Kind: model.ItemAssistantMessage, Text: "Done."}, "Assistant:\nDone."},
		{&model.Item{Kind: model.ItemAssistantMessage, ParentItemID: "x", Text: "a subagent's"}, ""},
		{&model.Item{Kind: model.ItemReasoning, Text: "hmm"}, ""},
		{&model.Item{Kind: model.ItemToolCall, Status: model.ItemCompleted, Tool: &model.ToolCall{Name: "Bash", Title: "go test ./...\nmore", ExitCode: &exit}},
			"Tool: go test ./... … (exit 1)"},
		{&model.Item{Kind: model.ItemToolCall, Status: model.ItemFailed, Tool: &model.ToolCall{Name: "Read"}}, "Tool: Read (failed)"},
		{&model.Item{Kind: model.ItemPlan, Plan: &model.Plan{Entries: []model.PlanEntry{{Content: "Test", Status: "completed"}}}}, "Plan:\n- [completed] Test"},
		{&model.Item{Kind: model.ItemApproval, Approval: &model.Approval{Title: "Pick a color",
			Questions: []model.Question{{Question: "Which color?"}},
			Options:   []model.ApprovalOption{{ID: "submit", Label: "Submit"}},
			Decision:  &model.Decision{OptionID: "submit", Answers: map[string]string{"Which color?": "Red"}}}}, "Asked: Pick a color\nWhich color? → Red — Submit"},
		{&model.Item{Kind: model.ItemApproval, Approval: &model.Approval{Title: "Write a.go",
			Options:  []model.ApprovalOption{{ID: "deny", Label: "Deny"}},
			Decision: &model.Decision{OptionID: "deny", Message: "not there"}}}, "Asked: Write a.go — Deny: not there"},
		{&model.Item{Kind: model.ItemApproval, Approval: &model.Approval{Title: "Unanswered"}}, ""},
		{&model.Item{Kind: model.ItemNotice, Text: "Context compacted"}, "[Context compacted]"},
	} {
		if got := handoffLine(c.item); got != c.want {
			t.Errorf("handoffLine(%s %q) = %q, want %q", c.item.Kind, c.item.Text, got, c.want)
		}
	}
}

func TestForkRules(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	empty := e.create(CreateSessionParams{})
	if _, err := e.o.Fork(bg, ForkParams{SessionID: empty.ID}); CodeOf(err) != CodeInvalid {
		t.Errorf("fork without turns: %v", err)
	}
	if _, err := e.o.Fork(bg, ForkParams{SessionID: "nope"}); CodeOf(err) != CodeNotFound {
		t.Errorf("fork unknown session: %v", err)
	}
	s := e.create(CreateSessionParams{Prompt: "one"})
	e.waitTurn(s.ID, 1)
	e.prompt(s.ID, "slow two", "")
	e.waitFor("turn 2", func() bool { return len(e.turns(s.ID)) == 2 })
	if _, err := e.o.Fork(bg, ForkParams{SessionID: s.ID, Turn: 2}); CodeOf(err) != CodeConflict {
		t.Errorf("fork a running turn: %v", err)
	}
	if _, err := e.o.Fork(bg, ForkParams{SessionID: s.ID, Turn: 9}); CodeOf(err) != CodeNotFound {
		t.Errorf("fork turn 9: %v", err)
	}
	if _, err := e.o.Fork(bg, ForkParams{SessionID: s.ID, Workspace: &WorkspaceParams{Kind: model.WorkspaceWorktree}}); CodeOf(err) != CodeInvalid {
		t.Errorf("worktree fork outside git: %v", err)
	}
	if _, err := e.o.Fork(bg, ForkParams{SessionID: s.ID, Harness: "nope"}); CodeOf(err) != CodeInvalid {
		t.Errorf("fork to an unknown agent: %v", err)
	}
	// While turn 2 runs, a fork goes from turn 1; a fork's fork keeps one suffix.
	f := e.fork(ForkParams{SessionID: s.ID, Title: ""})
	if f.ForkedFrom.Turn != 1 || f.Title != "one (fork)" {
		t.Errorf("fork = %+v", f)
	}
	e.waitTurn(s.ID, 2)
	ff := e.fork(ForkParams{SessionID: f.ID, Title: "Mine", Draft: true, Prompt: "later"})
	if ff.Title != "Mine" || ff.Draft == nil || ff.Draft.Text != "later" || len(e.turns(ff.ID)) != 1 {
		t.Errorf("fork of a fork = %+v", ff)
	}
	if got := kinds(e.items(ff.ID)); got != "user_message,reasoning,assistant_message,notice,notice" {
		t.Errorf("fork of a fork's items = %s", got)
	}
	if fff := e.fork(ForkParams{SessionID: f.ID}); fff.Title != "one (fork)" {
		t.Errorf("title = %q", fff.Title)
	}
}

func TestCopyTranscript(t *testing.T) {
	t1, t2 := &model.Turn{ID: "t1", N: 1, ForkPoint: "p1"}, &model.Turn{ID: "t2", N: 2}
	it := func(id string, order int64, turn string, kind model.ItemKind, st model.ItemStatus) *model.Item {
		return &model.Item{ID: id, SessionID: "src", TurnID: turn, Order: order, Kind: kind, Status: st}
	}
	tool := it("tool", 2, "t1", model.ItemToolCall, model.ItemCompleted)
	sub := it("sub", 7, "t1", model.ItemAssistantMessage, model.ItemInProgress) // a background subagent, still going
	sub.ParentItemID = "tool"
	ap := it("ap", 3, "t1", model.ItemApproval, model.ItemResolved)
	ap.Approval = &model.Approval{ToolItemID: "tool"}
	items := []*model.Item{
		it("u1", 1, "t1", model.ItemUserMessage, model.ItemCompleted), tool, ap,
		it("cancelled", 4, "", model.ItemUserMessage, model.ItemCancelled),
		it("revert", 5, "", model.ItemNotice, model.ItemCompleted),
		it("u2", 6, "t2", model.ItemUserMessage, model.ItemCompleted), sub,
		it("later", 8, "", model.ItemNotice, model.ItemCompleted),
	}
	out, turns := copyTranscript("dst", items, []*model.Turn{t1, t2}, 1, true)
	var got []string
	ids := map[string]string{}
	for i, c := range out {
		got = append(got, fmt.Sprintf("%d:%s:%s", c.Order, c.Kind, c.Status))
		ids[c.ID] = []string{"u1", "tool", "ap", "revert", "sub"}[i]
		if c.SessionID != "dst" {
			t.Errorf("item %d session = %s", i, c.SessionID)
		}
	}
	if want := "1:user_message:completed 2:tool_call:completed 3:approval:resolved 4:notice:completed 5:assistant_message:completed"; strings.Join(got, " ") != want {
		t.Fatalf("items = %s, want %s", strings.Join(got, " "), want)
	}
	if len(turns) != 1 || turns[0].ID == "t1" || turns[0].ForkPoint != "p1" || out[0].TurnID != turns[0].ID || out[3].TurnID != "" {
		t.Errorf("turns = %+v, items = %+v", turns, out)
	}
	if ids[out[4].ParentItemID] != "tool" || ids[out[2].Approval.ToolItemID] != "tool" || ap.Approval.ToolItemID != "tool" {
		t.Errorf("parent = %q, tool item = %q", out[4].ParentItemID, out[2].Approval.ToolItemID)
	}
	if _, turns := copyTranscript("dst", items, []*model.Turn{t1, t2}, 1, false); turns[0].ForkPoint != "" {
		t.Errorf("another agent's copy keeps fork point %q", turns[0].ForkPoint)
	}
}

func TestHandoffBudget(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	a := newActor(e.o, &model.Session{ID: "f", ForkedFrom: &model.ForkedFrom{SessionID: "s", Turn: 40}}, nil)
	long := strings.Repeat("x", handoffEntry+100)
	_, err := e.st.Write(bg, func(w *store.W) error {
		for i := 1; i <= 40; i++ {
			turn := fmt.Sprintf("t%d", i)
			if err := w.PutItem(&model.Item{ID: fmt.Sprintf("u%d", i), SessionID: "f", TurnID: turn, Order: int64(2*i - 1),
				Kind: model.ItemUserMessage, Status: model.ItemCompleted, Text: fmt.Sprintf("request %d", i)}); err != nil {
				return err
			}
			if err := w.PutItem(&model.Item{ID: fmt.Sprintf("a%d", i), SessionID: "f", TurnID: turn, Order: int64(2 * i),
				Kind: model.ItemAssistantMessage, Status: model.ItemCompleted, Text: long}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	h := a.handoff(bg, "")
	if !strings.Contains(h, "User:\nrequest 1\n\n[… ") || !strings.Contains(h, "User:\nrequest 40") || strings.Contains(h, "request 2\n") {
		t.Errorf("handoff keeps the wrong entries:\n%.400s", h)
	}
	if !strings.Contains(h, " […]") || len(h) > handoffBudget+2000 {
		t.Errorf("handoff is %d long", len(h))
	}
}
