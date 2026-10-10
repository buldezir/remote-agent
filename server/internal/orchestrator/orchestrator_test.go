package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"remote-agent/internal/config"
	"remote-agent/internal/events"
	"remote-agent/internal/gitx"
	"remote-agent/internal/harness"
	"remote-agent/internal/harness/claude"
	"remote-agent/internal/harness/fake"
	"remote-agent/internal/harness/replaytest"
	"remote-agent/internal/images"
	"remote-agent/internal/model"
	"remote-agent/internal/store"
)

func TestMain(m *testing.M) {
	replaytest.Main() // when re-executed as a recorded agent CLI
	// Isolate git from the developer's config (global ignores, signing, hooks).
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	os.Setenv("GIT_AUTHOR_NAME", "test")
	os.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	os.Setenv("GIT_COMMITTER_NAME", "test")
	os.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
	os.Exit(m.Run())
}

var bg = context.Background()

type env struct {
	t    *testing.T
	st   *store.Store
	o    *Orchestrator
	proj *model.Project
	dir  string // project dir
	log  *logBuffer
	imgs *images.Store
}

// logBuffer collects the orchestrator's log; actors write it concurrently.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newEnv sets up a store, an orchestrator with the fake harness, and a project.
// With git, the project is a repo with one commit containing a.txt.
func newEnv(t *testing.T, git bool) *env {
	t.Helper()
	return newEnvAt(t, t.TempDir(), git, fake.Harness{})
}

func newEnvAt(t *testing.T, dir string, git bool, hs ...harness.Harness) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "rad.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hub := events.NewHub()
	st.OnCommit(hub.Publish)
	logs := &logBuffer{}
	imgs := images.New(t.TempDir())
	o := New(st, harness.NewRegistry(hs...), Options{
		WorktreesDir: t.TempDir(),
		Images:       imgs,
		Log:          slog.New(slog.NewTextHandler(logs, nil)),
	})
	t.Cleanup(o.Shutdown)

	if git {
		run(t, dir, "git", "init", "-q", "-b", "main")
		os.WriteFile(filepath.Join(dir, "a.txt"), []byte("original\n"), 0o644)
		run(t, dir, "git", "add", "-A")
		run(t, dir, "git", "commit", "-q", "-m", "init")
	}
	proj, err := o.AddProject(bg, dir)
	if err != nil {
		t.Fatal(err)
	}
	if proj.IsGitRepo != git {
		t.Fatalf("IsGitRepo = %v", proj.IsGitRepo)
	}
	return &env{t: t, st: st, o: o, proj: proj, dir: dir, log: logs, imgs: imgs}
}

func run(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (e *env) create(p CreateSessionParams) *model.Session {
	e.t.Helper()
	if p.ProjectID == "" {
		p.ProjectID = e.proj.ID
	}
	if p.Harness == "" {
		p.Harness = "fake"
	}
	s, err := e.o.CreateSession(bg, p)
	if err != nil {
		e.t.Fatalf("create session: %v", err)
	}
	return s
}

func (e *env) prompt(sessionID, text, cmd string) *model.Item {
	e.t.Helper()
	it, err := e.o.Prompt(bg, sessionID, text, nil, cmd)
	if err != nil {
		e.t.Fatalf("prompt %q: %v", text, err)
	}
	return it
}

// waitFor polls cond until it holds.
func (e *env) waitFor(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (e *env) session(id string) *model.Session {
	e.t.Helper()
	s, err := e.st.Session(bg, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func (e *env) turns(id string) []*model.Turn {
	e.t.Helper()
	ts, err := e.st.Turns(bg, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return ts
}

func (e *env) items(id string) []*model.Item {
	e.t.Helper()
	its, err := e.st.Items(bg, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return its
}

// waitTurn waits until turn n has ended and the session has settled.
func (e *env) waitTurn(sessionID string, n int) *model.Turn {
	e.t.Helper()
	var turn *model.Turn
	e.waitFor("turn to end", func() bool {
		ts := e.turns(sessionID)
		if len(ts) < n || ts[n-1].Status == model.TurnRunning {
			return false
		}
		turn = ts[n-1]
		return e.session(sessionID).Status == model.SessionIdle
	})
	return turn
}

func (e *env) pendingApproval(sessionID string) *model.Item {
	e.t.Helper()
	var ap *model.Item
	e.waitFor("pending approval", func() bool {
		for _, it := range e.items(sessionID) {
			if it.Kind == model.ItemApproval && it.Status == model.ItemPending {
				ap = it
				return true
			}
		}
		return false
	})
	return ap
}

func byKind(items []*model.Item, kind model.ItemKind) []*model.Item {
	var out []*model.Item
	for _, it := range items {
		if it.Kind == kind {
			out = append(out, it)
		}
	}
	return out
}

func kinds(items []*model.Item) string {
	var out []string
	for _, it := range items {
		out = append(out, string(it.Kind))
	}
	return strings.Join(out, ",")
}

// checkOrder verifies item orders are unique and that items of later turns
// never sort before items of earlier turns.
func checkOrder(t *testing.T, items []*model.Item, turns []*model.Turn) {
	t.Helper()
	turnN := map[string]int{}
	for _, tr := range turns {
		turnN[tr.ID] = tr.N
	}
	last := 0
	for i, it := range items {
		if i > 0 && it.Order <= items[i-1].Order {
			t.Errorf("item %d (%s %q) has order %d after %d", i, it.Kind, it.Text, it.Order, items[i-1].Order)
		}
		if n := turnN[it.TurnID]; it.TurnID != "" {
			if n < last {
				t.Errorf("%s %q (turn %d) sorts after turn %d items", it.Kind, it.Text, n, last)
			}
			last = max(last, n)
		}
	}
}

func TestPromptPersistsTranscript(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	s := e.create(CreateSessionParams{CommandID: "c1", Prompt: "hello there\nsecond line"})
	if s.Title != "hello there" || s.Mode != "ask" || s.Workspace.Kind != model.WorkspaceRoot ||
		s.Workspace.Path != e.dir || s.Workspace.Branch != "main" {
		t.Errorf("session = %+v", s)
	}

	turn := e.waitTurn(s.ID, 1)
	if turn.Status != model.TurnCompleted || turn.N != 1 || turn.EndedAt == nil || turn.Usage == nil || turn.Usage.OutputTokens != 42 {
		t.Errorf("turn = %+v", turn)
	}
	if turn.CheckpointBefore == "" || turn.CheckpointAfter == "" {
		t.Errorf("git project turn has no checkpoints: %+v", turn)
	}

	sess := e.session(s.ID)
	if sess.NativeID != "fake-"+s.ID || sess.Error != "" {
		t.Errorf("session = %+v", sess)
	}
	items := e.items(s.ID)
	if got := kinds(items); got != "user_message,reasoning,assistant_message" {
		t.Fatalf("items = %s", got)
	}
	for _, it := range items {
		if it.Status != model.ItemCompleted || it.TurnID != turn.ID || it.SessionID != s.ID {
			t.Errorf("item %+v", it)
		}
	}
	if items[0].Text != "hello there\nsecond line" || items[2].Text != "Echo: hello there second line" {
		t.Errorf("texts = %q, %q", items[0].Text, items[2].Text)
	}
	checkOrder(t, items, e.turns(s.ID))
	if sess.ItemCount != int64(len(items)) {
		t.Errorf("item count = %d, want %d", sess.ItemCount, len(items))
	}

	// The session also shows up in the index stream.
	evs, _, err := e.st.Changes(bg, model.IndexStream, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range evs {
		if ev.Session != nil && ev.Session.ID == s.ID && ev.Session.Status == model.SessionIdle {
			found = true
		}
	}
	if !found {
		t.Error("index has no idle session")
	}

	if c := sess.Context; c == nil || *c != (model.ContextUsage{Used: 30000, Window: 200000}) {
		t.Errorf("context = %+v", c)
	}

	// A second prompt reuses the runtime and starts turn 2. The branch was
	// switched in between, and the session picks that up.
	run(t, e.dir, "git", "checkout", "-q", "-b", "feature")
	e.prompt(s.ID, "again", "c2")
	if t2 := e.waitTurn(s.ID, 2); t2.Status != model.TurnCompleted || t2.CheckpointBefore == "" {
		t.Errorf("turn 2 = %+v", t2)
	}
	checkOrder(t, e.items(s.ID), e.turns(s.ID))
	sess = e.session(s.ID)
	if sess.Workspace.Branch != "feature" {
		t.Errorf("branch = %q", sess.Workspace.Branch)
	}
	// Turn 2 reports no window; the known one is kept.
	if c := sess.Context; c == nil || *c != (model.ContextUsage{Used: 60000, Window: 200000}) {
		t.Errorf("context = %+v", c)
	}
	if m := sess.ModelInfo; m == nil || *m != (model.ModelInfo{ID: "echo", Effort: "medium"}) {
		t.Errorf("model info = %+v", m)
	}

	// The log describes each prompt and turn, but holds no prompt text (nor
	// the title made from it).
	logs := e.log.String()
	for _, want := range []string{"msg=prompt ", "chars=23 lines=2 queued=false", `msg="turn started"`, `msg="turn ended"`,
		"turn=2 status=completed", "model=echo effort=medium", "branch=feature"} {
		if !strings.Contains(logs, want) {
			t.Errorf("log lacks %q:\n%s", want, logs)
		}
	}
	for _, text := range []string{"hello", "second line", "again"} {
		if strings.Contains(logs, text) {
			t.Errorf("log contains prompt text %q:\n%s", text, logs)
		}
	}
}

func TestImages(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	var b bytes.Buffer
	png.Encode(&b, image.NewGray(image.Rect(0, 0, 8, 6)))
	ref, err := e.imgs.Put(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}

	// A prompt of only an image, made with the session.
	s := e.create(CreateSessionParams{CommandID: "c1", Images: []string{ref.ID}})
	e.waitTurn(s.ID, 1)
	items := e.items(s.ID)
	if got := kinds(items); got != "user_message,reasoning,assistant_message" {
		t.Fatalf("items = %s", got)
	}
	if len(items[0].Images) != 1 || items[0].Images[0] != ref || items[0].Text != "" {
		t.Errorf("user message = %+v", items[0])
	}
	if items[2].Text != "Echo (1 image(s)):" {
		t.Errorf("reply = %q (the fake counts the images it got)", items[2].Text)
	}
	if !strings.Contains(e.log.String(), "images=1") {
		t.Errorf("log does not count the images:\n%s", e.log.String())
	}

	// A tool that returns an image.
	e.prompt(s.ID, "take a screenshot", "")
	e.waitTurn(s.ID, 2)
	var shot *model.Item
	for _, it := range e.items(s.ID) {
		if it.Kind == model.ItemToolCall {
			shot = it
		}
	}
	if shot == nil || len(shot.Images) != 1 || shot.Images[0].Width != 640 || shot.Images[0].MimeType != "image/png" {
		t.Fatalf("screenshot item = %+v", shot)
	}
	if _, _, err := e.imgs.Get(shot.Images[0].ID); err != nil {
		t.Errorf("screenshot not stored: %v", err)
	}

	// A reply that shows an image file as a Markdown image: rad keeps the
	// file and points the link at its copy.
	e.prompt(s.ID, "show me a picture", "")
	e.waitTurn(s.ID, 3)
	items = e.items(s.ID)
	reply := items[len(items)-1]
	if reply.Kind != model.ItemAssistantMessage || len(reply.Images) != 1 || reply.Images[0].Width != 640 {
		t.Fatalf("reply = %+v", reply)
	}
	if want := "![Picture](rad-image:" + reply.Images[0].ID + ")"; !strings.HasSuffix(reply.Text, want) {
		t.Errorf("reply text = %q, want it to end with %q", reply.Text, want)
	}
	if _, _, err := e.imgs.Get(reply.Images[0].ID); err != nil {
		t.Errorf("picture not stored: %v", err)
	}

	if _, err := e.o.Prompt(bg, s.ID, "look", []string{"nope.png"}, ""); CodeOf(err) != CodeInvalid {
		t.Errorf("unknown image: err = %v", err)
	}
}

func TestCompact(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	s := e.create(CreateSessionParams{CommandID: "c1", Prompt: "hi"})
	e.waitTurn(s.ID, 1)
	if c := e.session(s.ID).Context; c == nil || c.Used != 30000 || c.Window != 200000 {
		t.Fatalf("context = %+v", c)
	}

	// The command is a prompt like any other; the harness runs it.
	e.prompt(s.ID, "/compact", "")
	if turn := e.waitTurn(s.ID, 2); turn.Status != model.TurnCompleted {
		t.Fatalf("turn = %+v", turn)
	}
	items := e.items(s.ID)
	if got := kinds(items[len(items)-2:]); got != "user_message,notice" || items[len(items)-2].Text != "/compact" {
		t.Errorf("items = %s, %q", got, items[len(items)-2].Text)
	}
	if c := e.session(s.ID).Context; c == nil || c.Used != 5000 || c.Window != 200000 {
		t.Errorf("context after compacting = %+v, want less used in the same window", c)
	}
}

func TestCreateWithEffort(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	s := e.create(CreateSessionParams{CommandID: "c1", Effort: "high", Prompt: "hi"})
	e.waitTurn(s.ID, 1)
	// The fake harness reports back the effort it was opened with.
	sess := e.session(s.ID)
	if sess.Effort != "high" || sess.ModelInfo == nil || sess.ModelInfo.Effort != "high" {
		t.Errorf("effort = %q, model info = %+v", sess.Effort, sess.ModelInfo)
	}
}

func TestInvalidRequests(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	cases := []struct {
		p    CreateSessionParams
		code string
	}{
		{CreateSessionParams{ProjectID: "nope", Harness: "fake"}, CodeNotFound},
		{CreateSessionParams{ProjectID: e.proj.ID, Harness: "nope"}, CodeInvalid},
		{CreateSessionParams{ProjectID: e.proj.ID, Harness: "fake", Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree}}, CodeInvalid},
	}
	for _, c := range cases {
		if _, err := e.o.CreateSession(bg, c.p); CodeOf(err) != c.code {
			t.Errorf("create %+v: %v, want %s", c.p, err, c.code)
		}
	}
	s := e.create(CreateSessionParams{})
	if s.Title != "New session" {
		t.Errorf("title = %q", s.Title)
	}
	if _, err := e.o.Prompt(bg, s.ID, "  \n", nil, ""); CodeOf(err) != CodeInvalid {
		t.Errorf("empty prompt: %v", err)
	}
	if _, err := e.o.Prompt(bg, "missing", "hi", nil, ""); CodeOf(err) != CodeNotFound {
		t.Errorf("unknown session: %v", err)
	}
	if _, err := e.o.TurnDiff(bg, "missing", nil); CodeOf(err) != CodeNotFound {
		t.Errorf("unknown turn: %v", err)
	}
	if _, err := e.o.SessionDiff(bg, s.ID, nil); CodeOf(err) != CodeInvalid {
		t.Errorf("diff in non-git project: %v", err)
	}
	if err := e.o.RemoveProject(bg, e.proj.ID); CodeOf(err) != CodeConflict {
		t.Errorf("remove project with sessions: %v", err)
	}
}

func TestApprovalAllow(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	s := e.create(CreateSessionParams{Mode: "ask", Prompt: "please write foo.txt now"})

	ap := e.pendingApproval(s.ID)
	if got := e.session(s.ID).Status; got != model.SessionAwaitingApproval {
		t.Errorf("status = %s", got)
	}
	if ap.Approval.Title != "Write foo.txt" || ap.Approval.ToolName != "Write" || len(ap.Approval.Options) != 3 {
		t.Errorf("approval = %+v", ap.Approval)
	}
	tools := byKind(e.items(s.ID), model.ItemToolCall)
	if len(tools) != 1 || ap.Approval.ToolItemID != tools[0].ID || tools[0].Status != model.ItemPending {
		t.Fatalf("approval does not point at the pending tool call: %+v / %+v", ap.Approval, tools)
	}

	if err := e.o.Respond(bg, s.ID, ap.ID, harness.Response{OptionID: "bogus"}, "r0"); CodeOf(err) != CodeInvalid {
		t.Errorf("unknown option: %v", err)
	}
	if err := e.o.Respond(bg, s.ID, ap.ID, harness.Response{OptionID: "allow"}, "r1"); err != nil {
		t.Fatal(err)
	}
	turn := e.waitTurn(s.ID, 1)
	if turn.Status != model.TurnCompleted {
		t.Errorf("turn = %+v", turn)
	}
	if b, err := os.ReadFile(filepath.Join(e.dir, "foo.txt")); err != nil || string(b) != "hello from fake\n" {
		t.Errorf("foo.txt = %q, %v", b, err)
	}

	items := e.items(s.ID)
	var approval, tool *model.Item
	for _, it := range items {
		switch it.Kind {
		case model.ItemApproval:
			approval = it
		case model.ItemToolCall:
			tool = it
		}
	}
	if approval.Status != model.ItemResolved || approval.Approval.Decision == nil || approval.Approval.Decision.OptionID != "allow" {
		t.Errorf("approval = %+v %+v", approval, approval.Approval)
	}
	if tool.Status != model.ItemCompleted || tool.Tool.Output != "wrote foo.txt" {
		t.Errorf("tool = %+v %+v", tool, tool.Tool)
	}
	checkOrder(t, items, e.turns(s.ID))

	// Retrying the same command is a no-op; a new answer is a conflict.
	if err := e.o.Respond(bg, s.ID, ap.ID, harness.Response{OptionID: "allow"}, "r1"); err != nil {
		t.Errorf("retry: %v", err)
	}
	if err := e.o.Respond(bg, s.ID, ap.ID, harness.Response{OptionID: "deny"}, "r2"); CodeOf(err) != CodeConflict {
		t.Errorf("late answer: %v", err)
	}
}

func TestApprovalDeny(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	s := e.create(CreateSessionParams{Prompt: "write bar.txt"})
	ap := e.pendingApproval(s.ID)
	if err := e.o.Respond(bg, s.ID, ap.ID, harness.Response{OptionID: "deny", Message: "not now"}, ""); err != nil {
		t.Fatal(err)
	}
	if turn := e.waitTurn(s.ID, 1); turn.Status != model.TurnCompleted {
		t.Errorf("turn = %+v", turn)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "bar.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("bar.txt exists after deny: %v", err)
	}
	items := e.items(s.ID)
	tool := byKind(items, model.ItemToolCall)[0]
	if tool.Status != model.ItemFailed || tool.Tool.Output != "denied: not now" {
		t.Errorf("tool = %s %q", tool.Status, tool.Tool.Output)
	}
	approval := byKind(items, model.ItemApproval)[0]
	if d := approval.Approval.Decision; approval.Status != model.ItemResolved || d == nil || d.OptionID != "deny" || d.Message != "not now" {
		t.Errorf("approval = %s %+v", approval.Status, d)
	}
	if msgs := byKind(items, model.ItemAssistantMessage); len(msgs) != 1 || msgs[0].Text != "OK, I won't." {
		t.Errorf("messages = %+v", msgs)
	}
}

func TestIdempotentCommands(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	p := CreateSessionParams{CommandID: "create-1", Prompt: "hi"}
	s1 := e.create(p)
	s2 := e.create(p)
	if s1.ID != s2.ID {
		t.Fatalf("retried create made a new session: %s vs %s", s1.ID, s2.ID)
	}
	e.waitTurn(s1.ID, 1)
	if ss, _ := e.st.Sessions(bg); len(ss) != 1 {
		t.Errorf("sessions = %d", len(ss))
	}

	// Concurrent retries of one prompt apply it once.
	var wg sync.WaitGroup
	ids := make([]string, 5)
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			it, err := e.o.Prompt(bg, s1.ID, "second", nil, "prompt-2")
			if err != nil {
				t.Error(err)
				return
			}
			ids[i] = it.ID
		}()
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("retries returned different items: %v", ids)
		}
	}
	e.waitTurn(s1.ID, 2)
	users := byKind(e.items(s1.ID), model.ItemUserMessage)
	if len(users) != 2 || users[0].Text != "hi" || users[1].Text != "second" {
		t.Errorf("user messages = %+v", users)
	}
	if n := len(e.turns(s1.ID)); n != 2 {
		t.Errorf("turns = %d", n)
	}
}

func TestQueuedPrompt(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	s := e.create(CreateSessionParams{Prompt: "slow first"})
	q := e.prompt(s.ID, "second", "")
	if q.Status != model.ItemQueued || q.TurnID != "" {
		t.Errorf("queued item = %+v", q)
	}
	e.waitTurn(s.ID, 2)
	turns := e.turns(s.ID)
	if len(turns) != 2 || turns[0].Status != model.TurnCompleted || turns[1].Status != model.TurnCompleted {
		t.Fatalf("turns = %+v", turns)
	}
	items := e.items(s.ID)
	if got := kinds(items); got != "user_message,reasoning,assistant_message,user_message,reasoning,assistant_message" {
		t.Errorf("items = %s", got)
	}
	for _, it := range items {
		if it.ID == q.ID && (it.Status != model.ItemCompleted || it.TurnID != turns[1].ID) {
			t.Errorf("queued prompt after its turn = %+v", it)
		}
	}
	checkOrder(t, items, turns)
}

func TestInterrupt(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	s := e.create(CreateSessionParams{Prompt: "slow reply with quite a few words in it"})
	e.waitFor("streaming", func() bool {
		msgs := byKind(e.items(s.ID), model.ItemAssistantMessage)
		return len(msgs) == 1 && msgs[0].Text != ""
	})
	queued := e.prompt(s.ID, "never runs", "")
	if err := e.o.Interrupt(bg, s.ID, false); err != nil {
		t.Fatal(err)
	}
	turn := e.waitTurn(s.ID, 1)
	if turn.Status != model.TurnInterrupted {
		t.Errorf("turn = %s", turn.Status)
	}
	items := e.items(s.ID)
	for _, it := range items {
		switch {
		case it.ID == queued.ID:
			if it.Status != model.ItemCancelled {
				t.Errorf("queued prompt = %s", it.Status)
			}
		case it.Status != model.ItemCompleted:
			t.Errorf("%s item left %s", it.Kind, it.Status)
		}
	}
	if msg := byKind(items, model.ItemAssistantMessage)[0]; strings.HasSuffix(msg.Text, "it") {
		t.Errorf("message was not cut short: %q", msg.Text)
	}
	if n := len(e.turns(s.ID)); n != 1 {
		t.Errorf("cancelled prompt started a turn (%d turns)", n)
	}

	// Interrupting an idle session is a no-op; the runtime keeps working.
	if err := e.o.Interrupt(bg, s.ID, false); err != nil {
		t.Error(err)
	}
	e.prompt(s.ID, "after", "")
	if turn := e.waitTurn(s.ID, 2); turn.Status != model.TurnCompleted {
		t.Errorf("turn 2 = %s", turn.Status)
	}
}

func TestForceInterruptRestartsRuntime(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	s := e.create(CreateSessionParams{Prompt: "slow reply with many words"})
	e.waitFor("streaming", func() bool { return len(byKind(e.items(s.ID), model.ItemAssistantMessage)) == 1 })
	if err := e.o.Interrupt(bg, s.ID, true); err != nil {
		t.Fatal(err)
	}
	if turn := e.waitTurn(s.ID, 1); turn.Status != model.TurnInterrupted {
		t.Errorf("turn = %s", turn.Status)
	}
	// The next prompt reopens the runtime, resuming the native session.
	e.prompt(s.ID, "hello again", "")
	if turn := e.waitTurn(s.ID, 2); turn.Status != model.TurnCompleted {
		t.Errorf("turn 2 = %s", turn.Status)
	}
	if sess := e.session(s.ID); sess.NativeID != "fake-"+s.ID || sess.Status != model.SessionIdle {
		t.Errorf("session = %+v", sess)
	}
	checkOrder(t, e.items(s.ID), e.turns(s.ID))
}

func TestFailedTurn(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	s := e.create(CreateSessionParams{Prompt: "please fail"})
	turn := e.waitTurn(s.ID, 1)
	if turn.Status != model.TurnFailed || turn.Error != "fake failure requested" {
		t.Errorf("turn = %+v", turn)
	}
	errs := byKind(e.items(s.ID), model.ItemError)
	if len(errs) != 1 || errs[0].Text != "fake failure requested" || errs[0].TurnID != turn.ID {
		t.Errorf("error items = %+v", errs)
	}
	if sess := e.session(s.ID); sess.Status != model.SessionIdle {
		t.Errorf("status = %s", sess.Status)
	}
}

func TestSetModeAndArchive(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	s := e.create(CreateSessionParams{Prompt: "hi", Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree}})
	e.waitTurn(s.ID, 1)
	if err := e.o.SetMode(bg, s.ID, "auto"); err != nil {
		t.Fatal(err)
	}
	if m := e.session(s.ID).Mode; m != "auto" {
		t.Errorf("mode = %s", m)
	}
	// In auto mode the fake writes without asking.
	e.prompt(s.ID, "write auto.txt", "")
	e.waitTurn(s.ID, 2)
	if len(byKind(e.items(s.ID), model.ItemApproval)) != 0 {
		t.Error("auto mode asked for approval")
	}
	if _, err := os.Stat(filepath.Join(s.Workspace.Path, "auto.txt")); err != nil {
		t.Error(err)
	}

	if err := e.o.Archive(bg, s.ID, true); err != nil {
		t.Fatal(err)
	}
	sess := e.session(s.ID)
	if !sess.Archived || sess.Status != model.SessionStopped {
		t.Errorf("session = %+v", sess)
	}
	if _, err := os.Stat(s.Workspace.Path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("worktree still exists: %v", err)
	}
	if refs := run(t, e.dir, "git", "for-each-ref", "refs/ra/cp/"+s.ID+"/"); refs != "" {
		t.Errorf("checkpoint refs left: %s", refs)
	}
	if _, err := e.o.Prompt(bg, s.ID, "more", nil, ""); CodeOf(err) != CodeConflict {
		t.Errorf("prompt archived session: %v", err)
	}
	if err := e.o.RemoveProject(bg, e.proj.ID); err != nil {
		t.Errorf("remove project: %v", err)
	}
}

func TestRecoverAfterCrash(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	now := time.Now().UTC()
	sess := &model.Session{ID: store.NewID(), ProjectID: e.proj.ID, Harness: "fake", Mode: "ask",
		Workspace: model.Workspace{Kind: model.WorkspaceRoot, Path: e.dir}, Status: model.SessionAwaitingApproval,
		NativeID: "fake-crashed", Title: "crashed", CreatedAt: now}
	turn := &model.Turn{ID: store.NewID(), SessionID: sess.ID, N: 1, Status: model.TurnRunning, StartedAt: now}
	mk := func(order int64, kind model.ItemKind, st model.ItemStatus) *model.Item {
		return &model.Item{ID: store.NewID(), SessionID: sess.ID, TurnID: turn.ID, Order: order, Kind: kind, Status: st}
	}
	user, msg := mk(1, model.ItemUserMessage, model.ItemCompleted), mk(2, model.ItemAssistantMessage, model.ItemInProgress)
	tool, ap := mk(3, model.ItemToolCall, model.ItemPending), mk(4, model.ItemApproval, model.ItemPending)
	queued := mk(5, model.ItemUserMessage, model.ItemQueued)
	queued.TurnID = ""
	tool.Tool = &model.ToolCall{Name: "Write", Kind: model.ToolEdit}
	ap.Approval = &model.Approval{ToolItemID: tool.ID, Title: "Write x", Options: []model.ApprovalOption{{ID: "allow", Label: "Allow", Kind: model.OptionAllowOnce}}}
	// An idle session must not get a restart notice.
	idle := &model.Session{ID: store.NewID(), ProjectID: e.proj.ID, Harness: "fake", Status: model.SessionIdle, CreatedAt: now}
	_, err := e.st.Write(bg, func(w *store.W) error {
		for _, s := range []*model.Session{sess, idle} {
			if err := w.PutSession(s); err != nil {
				return err
			}
		}
		if err := w.PutTurn(turn); err != nil {
			return err
		}
		for _, it := range []*model.Item{user, msg, tool, ap, queued} {
			if err := w.PutItem(it); err != nil {
				return err
			}
		}
		return w.SetItemCount(sess.ID, 5)
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := e.o.Recover(bg); err != nil {
		t.Fatal(err)
	}

	got := e.session(sess.ID)
	if got.Status != model.SessionIdle || got.ItemCount != 6 {
		t.Errorf("session = %s, %d items", got.Status, got.ItemCount)
	}
	if tr := e.turns(sess.ID)[0]; tr.Status != model.TurnInterrupted || tr.EndedAt == nil {
		t.Errorf("turn = %+v", tr)
	}
	want := map[string]model.ItemStatus{
		user.ID: model.ItemCompleted, msg.ID: model.ItemCompleted, tool.ID: model.ItemFailed, // never ran
		ap.ID: model.ItemExpired, queued.ID: model.ItemCancelled,
	}
	items := e.items(sess.ID)
	if len(items) != 6 {
		t.Fatalf("items = %s", kinds(items))
	}
	for _, it := range items[:5] {
		if it.Status != want[it.ID] {
			t.Errorf("%s item is %s, want %s", it.Kind, it.Status, want[it.ID])
		}
	}
	if n := items[5]; n.Kind != model.ItemNotice || n.Order != 6 || !strings.Contains(n.Text, "Server restarted") {
		t.Errorf("notice = %+v", n)
	}
	if e.session(idle.ID).Status != model.SessionIdle || len(e.items(idle.ID)) != 0 {
		t.Error("idle session was touched")
	}

	// The session works again: the next turn is 2, resuming the native session.
	e.prompt(sess.ID, "are you there", "")
	if tr := e.waitTurn(sess.ID, 2); tr.Status != model.TurnCompleted {
		t.Errorf("turn 2 = %+v", tr)
	}
	if got := e.session(sess.ID); got.NativeID != "fake-crashed" {
		t.Errorf("native id = %s", got.NativeID)
	}
	// Recovering again is harmless.
	if err := e.o.Recover(bg); err != nil {
		t.Fatal(err)
	}
	if n := len(byKind(e.items(sess.ID), model.ItemNotice)); n != 1 {
		t.Errorf("notices = %d", n)
	}
}

func fileStats(files []gitx.FileStat) string {
	var out []string
	for _, f := range files {
		out = append(out, string(f.Status)+" "+f.Path)
	}
	slices.Sort(out)
	return strings.Join(out, ", ")
}

func TestWorktreeDiffAndRevert(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	s := e.create(CreateSessionParams{Mode: "auto", Prompt: "write new.txt", Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree}})
	ws := s.Workspace.Path
	if s.Workspace.Kind != model.WorkspaceWorktree || !strings.HasPrefix(s.Workspace.Branch, "ra/write-new-txt-") || ws == e.dir {
		t.Fatalf("workspace = %+v", s.Workspace)
	}
	if b := gitx.CurrentBranch(bg, ws); b != s.Workspace.Branch {
		t.Errorf("worktree branch = %s", b)
	}
	t1 := e.waitTurn(s.ID, 1)
	e.prompt(s.ID, "write a.txt", "")
	t2 := e.waitTurn(s.ID, 2)

	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(ws, name))
		if err != nil {
			return "<missing>"
		}
		return string(b)
	}
	if read("new.txt") != "hello from fake\n" || read("a.txt") != "hello from fake\n" {
		t.Fatalf("turns did not write files: %q %q", read("new.txt"), read("a.txt"))
	}
	if _, err := os.Stat(filepath.Join(e.dir, "new.txt")); err == nil {
		t.Error("worktree session wrote into the main checkout")
	}

	d1, err := e.o.TurnDiff(bg, t1.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileStats(d1.Files); got != "added new.txt" || d1.From != t1.CheckpointBefore || d1.To != t1.CheckpointAfter ||
		!strings.Contains(d1.Patch, "+hello from fake") || d1.Truncated {
		t.Errorf("turn 1 diff = %s %+v", got, d1)
	}
	d2, err := e.o.TurnDiff(bg, t2.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileStats(d2.Files); got != "modified a.txt" || d2.Files[0].Additions != 1 || d2.Files[0].Deletions != 1 {
		t.Errorf("turn 2 diff = %+v", d2.Files)
	}
	sd, err := e.o.SessionDiff(bg, s.ID, []string{"a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if got := fileStats(sd.Files); got != "added new.txt, modified a.txt" || sd.From != t1.CheckpointBefore {
		t.Errorf("session diff = %s from %s", got, sd.From)
	}
	if strings.Contains(sd.Patch, "new.txt") || !strings.Contains(sd.Patch, "-original") {
		t.Errorf("path-filtered patch = %q", sd.Patch)
	}

	if _, err := e.o.Revert(bg, s.ID, 9); CodeOf(err) != CodeNotFound {
		t.Errorf("revert unknown turn: %v", err)
	}
	files, err := e.o.Revert(bg, s.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileStats(files); got != "modified a.txt" || read("a.txt") != "original\n" || read("new.txt") != "hello from fake\n" {
		t.Errorf("revert to turn 2 = %s; a=%q new=%q", got, read("a.txt"), read("new.txt"))
	}
	files, err = e.o.Revert(bg, s.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := fileStats(files); got != "added new.txt" || read("new.txt") != "<missing>" || read("a.txt") != "original\n" {
		t.Errorf("revert to turn 1 = %s; a=%q new=%q", got, read("a.txt"), read("new.txt"))
	}
	notices := byKind(e.items(s.ID), model.ItemNotice)
	if len(notices) != 2 || notices[1].Text != "Reverted 1 file(s) to their state before turn 1." {
		t.Errorf("notices = %+v", notices)
	}
	// The main checkout and its index are untouched.
	if b, _ := os.ReadFile(filepath.Join(e.dir, "a.txt")); string(b) != "original\n" {
		t.Errorf("main a.txt = %q", b)
	}
	if st := run(t, e.dir, "git", "status", "--porcelain"); st != "" {
		t.Errorf("main checkout dirty: %s", st)
	}

	// The agent is told about the revert on the next prompt.
	e.prompt(s.ID, "continue", "")
	e.waitTurn(s.ID, 3)
	msgs := byKind(e.items(s.ID), model.ItemAssistantMessage)
	if last := msgs[len(msgs)-1].Text; !strings.Contains(last, "reverted to their state before turn 1") || !strings.HasSuffix(last, "continue") {
		t.Errorf("turn 3 reply = %q", last)
	}
}

func TestRevertRejectedForRootWorkspace(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	s := e.create(CreateSessionParams{Mode: "auto", Prompt: "write new.txt"})
	e.waitTurn(s.ID, 1)
	if _, err := e.o.Revert(bg, s.ID, 1); CodeOf(err) != CodeInvalid {
		t.Errorf("revert root workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "new.txt")); err != nil {
		t.Errorf("file was reverted in the user's checkout: %v", err)
	}
	// Diffs still work for root sessions.
	d, err := e.o.SessionDiff(bg, s.ID, nil)
	if err != nil || fileStats(d.Files) != "added new.txt" {
		t.Errorf("session diff = %+v, %v", d, err)
	}
}

// installed skips probing (which would run the test binary with --version).
type installed struct{ harness.Harness }

func (h installed) Probe(context.Context) model.HarnessInfo {
	return model.HarnessInfo{ID: h.ID(), Name: h.ID(), Installed: true, AuthOK: true, DefaultMode: "default"}
}

// TestClaudeTranscript drives the orchestrator with a recorded Claude Code
// session (see internal/harness/claude/testdata).
func TestClaudeTranscript(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	ws := filepath.Join(root, "claude-rec/ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	exe, env := replaytest.Command(t, "../harness/claude/testdata/write.ndjson", root)
	e := newEnvAt(t, ws, false, installed{claude.New(config.Command{Command: exe, Env: env})})

	s := e.create(CreateSessionParams{Harness: "claude", Prompt: "Create a file hello.txt containing the word hi. Then reply with one short sentence."})
	ap := e.pendingApproval(s.ID)
	if ap.Approval.Title != "Write hello.txt" {
		t.Errorf("approval = %+v", ap.Approval)
	}
	if err := e.o.Respond(bg, s.ID, ap.ID, harness.Response{OptionID: "allow"}, "r1"); err != nil {
		t.Fatal(err)
	}
	turn := e.waitTurn(s.ID, 1)
	if turn.Status != model.TurnCompleted || turn.Usage == nil || turn.Usage.CostUSD <= 0 {
		t.Errorf("turn = %+v", turn)
	}

	items := e.items(s.ID)
	if got := kinds(items); got != "user_message,tool_call,approval,assistant_message" {
		t.Fatalf("items = %s", got)
	}
	tool, approval, msg := items[1], items[2], items[3]
	if tool.Status != model.ItemCompleted || tool.Tool.Title != "Write hello.txt" || !strings.HasPrefix(tool.Tool.Output, "File created") {
		t.Errorf("tool = %s %+v", tool.Status, tool.Tool)
	}
	if approval.Status != model.ItemResolved || approval.Approval.ToolItemID != tool.ID {
		t.Errorf("approval = %s %+v", approval.Status, approval.Approval)
	}
	if msg.Status != model.ItemCompleted || msg.Text != "Created `hello.txt` containing \"hi\" in the working directory." {
		t.Errorf("message = %s %q", msg.Status, msg.Text)
	}
	checkOrder(t, items, e.turns(s.ID))
	if sess := e.session(s.ID); sess.NativeID != "470b4231-674a-4c66-adc6-09afc9506634" || sess.Mode != "default" || sess.Status != model.SessionIdle {
		t.Errorf("session = %+v", sess)
	}
}
