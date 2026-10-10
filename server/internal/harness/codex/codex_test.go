package codex

import (
	"context"
	"os"
	"path/filepath"
	"slices"
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

func TestReplayShellCommandWithApproval(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "codex-rec/ws")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	exe, env := replaytest.Command(t, "testdata/run.ndjson", root)
	h := New(config.Command{Command: exe, Env: env})
	diag := filepath.Join(t.TempDir(), "diag.ndjson")
	rt, err := h.Open(context.Background(), harness.OpenOptions{SessionID: "local", Cwd: cwd, Mode: "untrusted",
		Instructions: "Show images as Markdown.", DiagPath: diag})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, sent := replaytest.Diag(t, diag)
	if i := slices.IndexFunc(sent, func(f map[string]any) bool { return f["method"] == "thread/start" }); i < 0 ||
		sent[i]["params"].(map[string]any)["developerInstructions"] != "Show images as Markdown." {
		t.Errorf("thread/start lacks the instructions: %v", sent)
	}
	const threadID = "01a11df1-fdad-7021-bc6b-dfdb9956398c"
	if rt.NativeID() != threadID {
		t.Errorf("NativeID() = %q", rt.NativeID())
	}

	turn := replaytest.RunTurn(t, rt, "Use a shell command to create hello.txt containing hi, then reply with one short sentence.",
		func(harness.ApprovalEvent) string { return "allow" })

	if ev, ok := turn.Events[0].(harness.NativeID); !ok || ev.ID != threadID {
		t.Errorf("first event = %#v, want NativeID", turn.Events[0])
	}

	const command = `printf 'hi\n' > hello.txt` // parsed from `/bin/zsh -lc "..."`
	if len(turn.Approvals) != 1 {
		t.Fatalf("approvals = %d, want 1", len(turn.Approvals))
	}
	ap := turn.Approvals[0].Approval
	if ap.ToolName != "shell" || ap.Title != command || ap.ToolItemID != "exec-b9f3b878-8469-4a2f-985c-c60d6302c2e9" {
		t.Errorf("approval = %+v", ap)
	}
	var opts []string
	for _, o := range ap.Options {
		opts = append(opts, o.ID)
	}
	// availableDecisions has no acceptForSession, so there is no session-wide option.
	if !slices.Equal(opts, []string{"allow", "deny"}) {
		t.Errorf("options = %v", opts)
	}

	tools := turn.ItemsOf(model.ItemToolCall)
	if len(tools) != 1 {
		t.Fatalf("tool calls = %+v", tools)
	}
	tool := tools[0]
	if tool.Status != model.ItemCompleted || tool.Tool.Name != "shell" || tool.Tool.Kind != model.ToolExecute || tool.Tool.Title != command {
		t.Errorf("tool = %s %+v", tool.Status, tool.Tool)
	}
	if tool.Tool.ExitCode == nil || *tool.Tool.ExitCode != 0 {
		t.Errorf("exit code = %v", tool.Tool.ExitCode)
	}
	var states []model.ItemStatus
	for _, ev := range turn.Events {
		if ie, ok := ev.(harness.ItemEvent); ok && ie.Item.ID == tool.ID {
			states = append(states, ie.Item.Status)
		}
	}
	if want := []model.ItemStatus{model.ItemInProgress, model.ItemPending, model.ItemCompleted}; !slices.Equal(states, want) {
		t.Errorf("tool states = %v, want %v", states, want)
	}

	var texts []string
	for _, m := range turn.ItemsOf(model.ItemAssistantMessage) {
		if m.Status != model.ItemCompleted {
			t.Errorf("message %q is %s", m.Text, m.Status)
		}
		texts = append(texts, m.Text)
	}
	if want := []string{"I’ll create the requested file now.", "Created `hello.txt` containing `hi`."}; !slices.Equal(texts, want) {
		t.Errorf("assistant messages = %q, want %q", texts, want)
	}
	// The user's own message is not echoed back as an item.
	if n := len(turn.ItemsOf(model.ItemUserMessage)); n != 0 {
		t.Errorf("user messages = %d", n)
	}

	end := turn.End
	if end.Status != model.TurnCompleted || end.Error != "" {
		t.Errorf("turn end = %+v", end)
	}
	if u := end.Usage; u == nil || u.InputTokens != 29281 || u.OutputTokens != 129 || u.CacheReadTokens != 25600 {
		t.Errorf("usage = %+v", end.Usage)
	}
	want := []harness.ContextUsage{{Used: 14687, Window: 258400}, {Used: 14723, Window: 258400}}
	if got, want := turn.ModelInfos(), []harness.ModelInfo{{ID: "gpt-5.6-sol", Effort: "medium"}}; !slices.Equal(got, want) {
		t.Errorf("model = %v, want %v", got, want)
	}
	if got := turn.Contexts(); !slices.Equal(got, want) {
		t.Errorf("context = %v, want %v", got, want)
	}

	replaytest.CloseClean(t, rt)
}
