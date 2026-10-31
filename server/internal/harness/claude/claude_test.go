package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"remote-agent/internal/config"
	"remote-agent/internal/harness"
	"remote-agent/internal/harness/replaytest"
	"remote-agent/internal/model"
)

func TestMain(m *testing.M) {
	replaytest.Main()
	os.Exit(m.Run())
}

// openReplay starts the adapter against a recorded transcript. It returns the
// runtime and the workspace dir standing in for the recorded one.
func openReplay(t *testing.T, transcript, ws string, o harness.OpenOptions) (harness.Runtime, string) {
	t.Helper()
	root := t.TempDir()
	cwd := filepath.Join(root, ws)
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	exe, env := replaytest.Command(t, transcript, root)
	h := New(config.Command{Command: exe, Env: env})
	o.Cwd = cwd
	rt, err := h.Open(context.Background(), o)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return rt, cwd
}

func TestReplayWriteWithApproval(t *testing.T) {
	diag := filepath.Join(t.TempDir(), "diag.ndjson")
	rt, _ := openReplay(t, "testdata/write.ndjson", "claude-rec/ws", harness.OpenOptions{SessionID: "local-session", Mode: "default",
		Instructions: "Show images as Markdown.", DiagPath: diag})
	if command, _ := replaytest.Diag(t, diag); !strings.Contains(command, `"--append-system-prompt" "Show images as Markdown."`) {
		t.Errorf("command = %s, want the instructions in the system prompt", command)
	}

	turn := replaytest.RunTurn(t, rt, "Create a file hello.txt containing the word hi. Then reply with one short sentence.",
		func(harness.ApprovalEvent) string { return "allow" })

	// Native id: first our --session-id, then the one Claude reports in system/init.
	var natives []string
	var modes []string
	for _, ev := range turn.Events {
		switch ev := ev.(type) {
		case harness.NativeID:
			natives = append(natives, ev.ID)
		case harness.ModeChanged:
			modes = append(modes, ev.Mode)
		}
	}
	if want := []string{"local-session", "470b4231-674a-4c66-adc6-09afc9506634"}; !slices.Equal(natives, want) {
		t.Errorf("native ids = %v, want %v", natives, want)
	}
	if rt.NativeID() != "470b4231-674a-4c66-adc6-09afc9506634" {
		t.Errorf("NativeID() = %q", rt.NativeID())
	}
	if len(modes) != 0 {
		t.Errorf("unexpected mode changes %v (mode was already default)", modes)
	}

	// The approval card for the Write call.
	if len(turn.Approvals) != 1 {
		t.Fatalf("approvals = %d, want 1", len(turn.Approvals))
	}
	ap := turn.Approvals[0]
	if ap.ID != "773cbea5-92d4-414d-9705-f3e13781c047" {
		t.Errorf("approval id = %q", ap.ID)
	}
	if ap.Approval.ToolName != "Write" || ap.Approval.Title != "Write hello.txt" || ap.Approval.ToolItemID != "toolu_01SPsRJPTkSthTUEnEbRB2US" {
		t.Errorf("approval = %+v", ap.Approval)
	}
	var opts []string
	for _, o := range ap.Approval.Options {
		opts = append(opts, o.ID+"="+o.Label)
	}
	if want := []string{"allow=Allow", "allow_session=Allow all edits this session", "deny=Deny"}; !slices.Equal(opts, want) {
		t.Errorf("options = %v, want %v", opts, want)
	}

	// The tool call: shown early from the stream, pending while approval is asked, then completed.
	tools := turn.ItemsOf(model.ItemToolCall)
	if len(tools) != 1 {
		t.Fatalf("tool calls = %+v", tools)
	}
	tool := tools[0]
	if tool.ID != "toolu_01SPsRJPTkSthTUEnEbRB2US" || tool.Status != model.ItemCompleted {
		t.Errorf("tool = %s %s", tool.ID, tool.Status)
	}
	if tool.Tool.Name != "Write" || tool.Tool.Kind != model.ToolEdit || tool.Tool.Title != "Write hello.txt" {
		t.Errorf("tool = %+v", tool.Tool)
	}
	if !slices.Equal(tool.Tool.Paths, []string{"hello.txt"}) {
		t.Errorf("paths = %v", tool.Tool.Paths)
	}
	if !strings.HasPrefix(tool.Tool.Output, "File created successfully at: ") {
		t.Errorf("output = %q", tool.Tool.Output)
	}
	var toolStates []model.ItemStatus
	for _, ev := range turn.Events {
		if ie, ok := ev.(harness.ItemEvent); ok && ie.Item.ID == tool.ID {
			toolStates = append(toolStates, ie.Item.Status)
		}
	}
	if want := []model.ItemStatus{model.ItemInProgress, model.ItemInProgress, model.ItemPending, model.ItemCompleted}; !slices.Equal(toolStates, want) {
		t.Errorf("tool states = %v, want %v", toolStates, want)
	}

	// The final answer streams in and is completed by the assistant frame.
	msgs := turn.ItemsOf(model.ItemAssistantMessage)
	if len(msgs) != 1 {
		t.Fatalf("assistant messages = %+v", msgs)
	}
	if want := "Created `hello.txt` containing \"hi\" in the working directory."; msgs[0].Text != want || msgs[0].Status != model.ItemCompleted {
		t.Errorf("assistant = %q (%s)", msgs[0].Text, msgs[0].Status)
	}
	// Empty thinking blocks are not shown.
	if r := turn.ItemsOf(model.ItemReasoning); len(r) != 0 {
		t.Errorf("reasoning = %+v", r)
	}

	end := turn.End
	if end.Status != model.TurnCompleted || end.Error != "" {
		t.Errorf("turn end = %+v", end)
	}
	// A fork after the turn cuts the conversation at its last message.
	if end.ForkPoint != "4cd29579-a17e-458a-b48c-9dda682c406c" {
		t.Errorf("fork point = %q", end.ForkPoint)
	}
	if end.Usage == nil || end.Usage.CostUSD <= 0 || end.Usage.InputTokens != 4 || end.Usage.OutputTokens != 312 ||
		end.Usage.CacheReadTokens != 33571 || end.Usage.CacheWriteTokens != 37647 {
		t.Errorf("usage = %+v", end.Usage)
	}
	// Context starts with Claude's own count, window included, before the
	// first result; then it is the main agent's last request.
	want := []harness.ContextUsage{{Used: 39512, Window: 1000000}, {Used: 33593, Window: 1000000}, {Used: 37652, Window: 1000000}}
	if got := turn.Contexts(); !slices.Equal(got, want) {
		t.Errorf("context = %v, want %v", got, want)
	}
	// The recording ran Haiku, which the CLI's model list doesn't name.
	if got, want := turn.ModelInfos(), []harness.ModelInfo{{ID: "claude-haiku-5-5", Effort: "high"}}; !slices.Equal(got, want) {
		t.Errorf("model = %v, want %v", got, want)
	}

	replaytest.CloseClean(t, rt)
}

func TestModelName(t *testing.T) {
	var init initResponse
	json.Unmarshal([]byte(`{"models":[
		{"value":"default","resolvedModel":"claude-opus-5-5","displayName":"Default (recommended)"},
		{"value":"opus","resolvedModel":"claude-opus-5-5","displayName":"Opus 5.5"}]}`), &init)
	for id, want := range map[string]string{"claude-opus-5-5": "Opus 5.5", "claude-haiku-5-5": ""} {
		if got := init.modelName(id); got != want {
			t.Errorf("modelName(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestChoices(t *testing.T) {
	// The initialize response recorded in the transcript.
	data, err := os.ReadFile("testdata/write.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	var line struct {
		Frame struct {
			Response struct {
				Response initResponse `json:"response"`
			} `json:"response"`
		} `json:"frame"`
	}
	json.Unmarshal([]byte(strings.Split(string(data), "\n")[3]), &line)
	init := line.Frame.Response.Response
	models, efforts := init.choices()
	if len(models) != 3 || models[1].ID != "opus" || models[1].Name != "Opus 5.5" {
		t.Fatalf("models = %+v", models)
	}
	var ids []string
	for _, e := range efforts {
		ids = append(ids, e.ID)
	}
	if want := []string{"low", "medium", "high", "xhigh", "max"}; !slices.Equal(ids, want) {
		t.Errorf("efforts = %v, want %v", ids, want)
	}
	if efforts[3].Name != "Extra high" || !reflect.DeepEqual(models[1].Efforts, efforts) {
		t.Errorf("xhigh = %+v, opus efforts = %+v", efforts[3], models[1].Efforts)
	}
}

func TestDescribeToolSymlinkedCwd(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	real, _ = filepath.EvalSymlinks(real)
	input, _ := json.Marshal(map[string]string{"file_path": filepath.Join(real, "server.go")})
	title, paths := describeTool("Edit", input, link)
	if title != "Edit server.go" || len(paths) != 1 || paths[0] != "server.go" {
		t.Fatalf("got %q %v", title, paths)
	}
}

func TestReplayPlanApproval(t *testing.T) {
	for _, tc := range []struct {
		name, transcript, sessionID, option string
		wantMode                            string   // the mode the approval asks Claude for
		wantModes                           []string // the mode changes rad reports
		wantApprovals                       int
	}{
		{"auto", "testdata/plan.ndjson", "01a14b23-33f5-75cc-abc5-09c64e16449a", "allow",
			"auto", []string{"auto"}, 1},
		{"yolo", "testdata/plan-yolo.ndjson", "01a14b2f-4c65-7301-bb7d-7743ee0dde58", "yolo",
			"bypassPermissions", []string{"bypassPermissions"}, 1},
		// Recorded without --allow-dangerously-skip-permissions: Claude
		// refuses bypass mode, says so in a status frame, and asks again.
		{"refused", "testdata/mode-refused.ndjson", "01a14b2d-1c3a-7294-9dbf-d8e63a661ff1", "yolo",
			"bypassPermissions", []string{"bypassPermissions", "default"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diag := filepath.Join(t.TempDir(), "diag.ndjson")
			rt, _ := openReplay(t, tc.transcript, "claude-rec/ws",
				harness.OpenOptions{SessionID: tc.sessionID, Mode: "plan", DiagPath: diag})
			turn := replaytest.RunTurn(t, rt, "Plan how to create a file hello.txt containing the word hi, then build it.",
				func(ev harness.ApprovalEvent) string {
					if ev.Approval.Special == model.SpecialPlan {
						return tc.option
					}
					return "allow"
				})

			if len(turn.Approvals) != tc.wantApprovals {
				t.Fatalf("approvals = %d, want %d", len(turn.Approvals), tc.wantApprovals)
			}
			ap := turn.Approvals[0].Approval
			if ap.Special != model.SpecialPlan || !strings.Contains(ap.PlanText, "hello.txt") {
				t.Errorf("approval = %+v", ap)
			}
			var opts []string
			for _, o := range ap.Options {
				opts = append(opts, o.ID+"="+o.Label)
			}
			if want := []string{"allow=Build with auto mode", "yolo=Yolo", "deny=Continue planning"}; !slices.Equal(opts, want) {
				t.Errorf("options = %v, want %v", opts, want)
			}

			// The approval itself switches Claude's mode, so the build that
			// follows runs in it.
			command, sent := replaytest.Diag(t, diag)
			if !strings.Contains(command, `"--allow-dangerously-skip-permissions"`) {
				t.Errorf("command = %s, want bypass mode allowed", command)
			}
			var perms any
			for _, f := range sent {
				if r, _ := f["response"].(map[string]any); r != nil && r["request_id"] == turn.Approvals[0].ID {
					perms = r["response"].(map[string]any)["updatedPermissions"]
				}
			}
			if want := []any{map[string]any{"type": "setMode", "mode": tc.wantMode, "destination": "session"}}; !reflect.DeepEqual(perms, want) {
				t.Errorf("updatedPermissions = %v, want %v", perms, want)
			}
			var modes []string
			for _, ev := range turn.Events {
				if mc, ok := ev.(harness.ModeChanged); ok {
					modes = append(modes, mc.Mode)
				}
			}
			if !slices.Equal(modes, tc.wantModes) {
				t.Errorf("mode changes = %v, want %v", modes, tc.wantModes)
			}
			if tools := turn.ItemsOf(model.ItemToolCall); len(tools) == 0 || tools[len(tools)-1].Tool.Output != "hi" {
				t.Errorf("tool calls = %+v, want the build to end with cat", tools)
			}
			if turn.End.Status != model.TurnCompleted {
				t.Errorf("end = %+v", turn.End)
			}
			replaytest.CloseClean(t, rt)
		})
	}
}

func TestReplayCompact(t *testing.T) {
	rt, _ := openReplay(t, "testdata/compact.ndjson", "claude-rec/ws",
		harness.OpenOptions{SessionID: "01a1268a-c7f9-77b8-ad00-c550f1168608", Mode: "default"})
	replaytest.RunTurn(t, rt, "Reply with just OK.", nil)

	// Claude runs /compact itself. The summary it goes on from isn't shown.
	turn := replaytest.RunTurn(t, rt, "/compact", nil)
	if len(turn.Keys) != 1 || turn.Items[turn.Keys[0]].Kind != model.ItemNotice || turn.Items[turn.Keys[0]].Text != "Context compacted" {
		t.Errorf("items = %v, want the notice", turn.Items)
	}
	if c := turn.Contexts(); !slices.Equal(c, []harness.ContextUsage{{Used: 3255, Window: 1000000}}) {
		t.Errorf("context = %v, want the size after compacting", c)
	}
	if turn.End.Status != model.TurnCompleted {
		t.Errorf("end = %+v", turn.End)
	}
}

// TestReplayFork replays a fork's first turn: Claude copied the session it
// resumes into a new one, cut at a message, and goes on from there.
func TestReplayFork(t *testing.T) {
	diag := filepath.Join(t.TempDir(), "diag.ndjson")
	rt, _ := openReplay(t, "testdata/fork.ndjson", "claude-fork/ws", harness.OpenOptions{SessionID: "local-fork", Mode: "acceptEdits",
		ResumeID: "01a18f38-5a9b-77f7-8859-3ff3504beb48", Fork: true, ForkAt: "6726a61d-a552-4770-b474-ea88bad4296d", DiagPath: diag})
	native := rt.NativeID()
	if native == "local-fork" || native == "01a18f38-5a9b-77f7-8859-3ff3504beb48" || len(native) != 36 {
		t.Errorf("native id = %q, want a new one", native)
	}
	command, _ := replaytest.Diag(t, diag)
	want := `"--resume" "01a18f38-5a9b-77f7-8859-3ff3504beb48" "--fork-session" "--session-id" "` + native +
		`" "--resume-session-at" "6726a61d-a552-4770-b474-ea88bad4296d"`
	if !strings.Contains(command, want) {
		t.Errorf("command = %s, want %s", command, want)
	}

	turn := replaytest.RunTurn(t, rt, "Without using any tools: list every request I have made to you in this conversation, in one short sentence.", nil)
	msgs := turn.ItemsOf(model.ItemAssistantMessage)
	if len(msgs) != 1 || !strings.HasPrefix(msgs[0].Text, "You asked me to create `note.txt`") {
		t.Errorf("reply = %+v", msgs)
	}
	if turn.End.Status != model.TurnCompleted || turn.End.ForkPoint != "897687db-f76e-48ec-abcd-09002094fc26" {
		t.Errorf("end = %+v", turn.End)
	}
	// The recording was made with the id rad chose, which Claude reports back.
	if rt.NativeID() != "14b3bc8a-284e-42b4-a1ed-a49673064f8f" {
		t.Errorf("NativeID() = %q", rt.NativeID())
	}
	replaytest.CloseClean(t, rt)
}

func TestReplayBackgroundTask(t *testing.T) {
	rt, _ := openReplay(t, "testdata/background.ndjson", "claude-rec/ws",
		harness.OpenOptions{SessionID: "01a12bdb-5f09-7249-b557-4061a96f6959", Mode: "default"})
	turn := replaytest.RunTurn(t, rt, "Run the shell command `sleep 5 && echo done` with the Bash tool's run_in_background option, "+
		"then reply with just: started. When it finishes, reply with just: finished.", nil)
	task := model.BackgroundTask{ID: "b1bf88bbn", Kind: model.BackgroundCommand, Description: "Sleep 5 seconds then echo done"}
	if got := backgroundLists(turn.Events); !reflect.DeepEqual(got, [][]model.BackgroundTask{{task}}) {
		t.Errorf("background tasks = %v, want the sleep", got)
	}
	if msgs := turn.ItemsOf(model.ItemAssistantMessage); len(msgs) != 1 || msgs[0].Text != "started" {
		t.Errorf("messages = %v", msgs)
	}

	// The command ends after the turn, and Claude starts one of its own to say so.
	own := replaytest.AwaitTurn(t, rt, nil)
	if got := backgroundLists(own.Events); !reflect.DeepEqual(got, [][]model.BackgroundTask{{}}) {
		t.Errorf("background tasks = %v, want none left", got)
	}
	var started []harness.TurnStarted
	for _, ev := range own.Events {
		if s, ok := ev.(harness.TurnStarted); ok {
			started = append(started, s)
		}
	}
	if want := []harness.TurnStarted{{Notice: "Background task finished: Sleep 5 seconds then echo done"}}; !reflect.DeepEqual(started, want) {
		t.Errorf("turn started = %v, want %v", started, want)
	}
	if msgs := own.ItemsOf(model.ItemAssistantMessage); len(msgs) != 1 || msgs[0].Text != "finished" {
		t.Errorf("messages = %v", msgs)
	}
	if own.End.Status != model.TurnCompleted {
		t.Errorf("end = %+v", own.End)
	}
	replaytest.CloseClean(t, rt)
}

func TestReplayBackgroundAgent(t *testing.T) {
	rt, _ := openReplay(t, "testdata/background-agent.ndjson", "claude-rec/ws",
		harness.OpenOptions{SessionID: "01a12be9-81e8-7f23-86be-12abfcc758ee", Mode: "default"})
	turn := replaytest.RunTurn(t, rt, "Do exactly this, in order. 1) Use the Agent tool with run_in_background set to true and "+
		"subagent_type general-purpose, giving it this task: 'Run the shell command `sleep 10 && ls` with the Bash tool, then report "+
		"the output.' 2) Run the shell command `sleep 6` yourself with the Bash tool (not in the background). 3) Reply with just: "+
		"started. Later, when the agent reports back, reply with just the file names it listed.", nil)
	task := model.BackgroundTask{ID: "afcd61f3815bac2ba", Kind: model.BackgroundAgent, Description: "List files after short sleep"}
	if got := backgroundLists(turn.Events); !reflect.DeepEqual(got, [][]model.BackgroundTask{{task}}) {
		t.Errorf("background tasks = %v, want the agent", got)
	}
	// The turn ends while the subagent's command runs; it goes on.
	const sub = "toolu_01EJUfKZ7F5Y5kxbhBDCVpGA"
	if it := turn.Items[sub]; it.Status != model.ItemInProgress || it.ParentItemID != "toolu_01XYnc4cdjaY39GCMtLExTRu" {
		t.Errorf("subagent's command at the turn's end = %s under %q, want in progress", it.Status, it.ParentItemID)
	}

	own := replaytest.AwaitTurn(t, rt, nil)
	if it := own.Items[sub]; it.Status != model.ItemCompleted || it.Tool.Output != "alpha.txt" {
		t.Errorf("subagent's command = %s %q, want its output", it.Status, it.Tool.Output)
	}
	var notices []string
	for _, ev := range own.Events {
		if s, ok := ev.(harness.TurnStarted); ok {
			notices = append(notices, s.Notice)
		}
	}
	if want := []string{"Background task finished: List files after short sleep"}; !slices.Equal(notices, want) {
		t.Errorf("turn started = %q, want %q", notices, want)
	}
	if msgs := own.ItemsOf(model.ItemAssistantMessage); len(msgs) != 2 || msgs[0].ParentItemID == "" || msgs[1].Text != "alpha.txt" {
		t.Errorf("messages = %v, want the subagent's report, then the reply", msgs)
	}
	replaytest.CloseClean(t, rt)
}

// A background task that ends during a turn is Claude's to read in that
// turn: no notice, and no turn of its own after it.
func TestReplayBackgroundTaskEndsMidTurn(t *testing.T) {
	rt, _ := openReplay(t, "testdata/background-midturn.ndjson", "claude-rec/ws",
		harness.OpenOptions{SessionID: "01a12beb-06ae-7b85-8709-7aae34c74857", Mode: "default"})
	first := replaytest.RunTurn(t, rt, "Run the shell command `sleep 8 && echo first` with the Bash tool's run_in_background option, "+
		"then reply with just: started.", nil)
	second := replaytest.RunTurn(t, rt, "Run the shell command `sleep 15 && echo second` with the Bash tool in the foreground "+
		"(not in the background), then reply with just: done.", nil)
	task := model.BackgroundTask{ID: "bv2dc40k5", Kind: model.BackgroundCommand, Description: "Run delayed echo in background"}
	if got := backgroundLists(first.Events); !reflect.DeepEqual(got, [][]model.BackgroundTask{{task}}) {
		t.Errorf("background tasks in turn 1 = %v", got)
	}
	if got := backgroundLists(second.Events); !reflect.DeepEqual(got, [][]model.BackgroundTask{{}}) {
		t.Errorf("background tasks in turn 2 = %v, want none left", got)
	}
	rest := replaytest.CloseClean(t, rt)
	for _, ev := range slices.Concat(first.Events, second.Events, rest) {
		if s, ok := ev.(harness.TurnStarted); ok {
			t.Errorf("unexpected turn of Claude's own: %+v", s)
		}
	}
}

func backgroundLists(evs []harness.Event) [][]model.BackgroundTask {
	var out [][]model.BackgroundTask
	for _, ev := range evs {
		if b, ok := ev.(harness.BackgroundTasks); ok {
			out = append(out, b.Tasks)
		}
	}
	return out
}

func TestDescribeToolHomePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	plan := filepath.Join(home, ".claude", "plans", "cache.md")
	input, _ := json.Marshal(map[string]string{"file_path": plan})
	title, paths := describeTool("Write", input, t.TempDir())
	if title != "Write ~/.claude/plans/cache.md" || len(paths) != 1 || paths[0] != plan {
		t.Fatalf("got %q %v", title, paths)
	}
	input, _ = json.Marshal(map[string]string{"file_path": filepath.Join(home, "proj", "a.go")})
	if title, _ := describeTool("Read", input, filepath.Join(home, "proj")); title != "Read a.go" {
		t.Fatalf("a file in the cwd: got %q", title)
	}
}

func TestReplaySkills(t *testing.T) {
	diag := filepath.Join(t.TempDir(), "diag.ndjson")
	rt, _ := openReplay(t, "testdata/skills.ndjson", "claude-rec/ws", harness.OpenOptions{SessionID: "skills", DiagPath: diag})
	commands, err := rt.(harness.CommandLister).Commands(context.Background())
	if err != nil || len(commands) != 2 || commands[1].Name != "prototype" || commands[1].Description != "Prototype a design" {
		t.Fatalf("commands = %+v, %v", commands, err)
	}
	replaytest.RunTurn(t, rt, "/prototype keep arguments", nil)
	_, sent := replaytest.Diag(t, diag)
	found := false
	for _, f := range sent {
		if f["type"] == "user" {
			found = f["message"].(map[string]any)["content"] == "/prototype keep arguments"
		}
	}
	if !found {
		t.Errorf("skill invocation missing: %+v", sent)
	}
	replaytest.CloseClean(t, rt)
}

func TestReplayWorktree(t *testing.T) {
	rt, cwd := openReplay(t, "testdata/worktree.ndjson", "claude-rec/ws",
		harness.OpenOptions{SessionID: "01a1875d-9c34-7ae8-b758-3c1a595d7ecc", Mode: "acceptEdits"})
	moves := func(turn *replaytest.Turn) []harness.WorkdirChanged {
		var out []harness.WorkdirChanged
		for _, ev := range turn.Events {
			if m, ok := ev.(harness.WorkdirChanged); ok {
				out = append(out, m)
			}
		}
		return out
	}
	// Claude enters a worktree of its own, and works there.
	turn := replaytest.RunTurn(t, rt, "Use the EnterWorktree tool to enter a new worktree named fix. "+
		"Then create note.txt in it containing the word hi. Then reply with one short sentence.", nil)
	if got, want := moves(turn), []harness.WorkdirChanged{{Dir: filepath.Join(cwd, ".claude/worktrees/fix")}}; !slices.Equal(got, want) {
		t.Errorf("moves = %v, want %v", got, want)
	}
	if got := moves(replaytest.RunTurn(t, rt, "Reply with just: ok", nil)); len(got) != 0 {
		t.Errorf("moves = %v, want none", got)
	}
	// And leaves it.
	turn = replaytest.RunTurn(t, rt, "Use the ExitWorktree tool with action keep. Then reply with one short sentence.", nil)
	if got, want := moves(turn), []harness.WorkdirChanged{{Dir: cwd}}; !slices.Equal(got, want) {
		t.Errorf("moves = %v, want %v", got, want)
	}
	if got := moves(replaytest.RunTurn(t, rt, "Reply with just: ok", nil)); len(got) != 0 {
		t.Errorf("moves = %v, want none", got)
	}
	replaytest.CloseClean(t, rt)
}
