package codex

import (
	"context"
	"fmt"
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
		Instructions: "Show images as Markdown.", DiagPath: diag, MCP: &harness.MCPServer{URL: "http://127.0.0.1:7421/v1/mcp", Token: "secret-token"}})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, sent := replaytest.Diag(t, diag)
	if i := slices.IndexFunc(sent, func(f map[string]any) bool { return f["method"] == "thread/start" }); i < 0 ||
		sent[i]["params"].(map[string]any)["developerInstructions"] != "Show images as Markdown." {
		t.Errorf("thread/start lacks the instructions: %v", sent)
	} else if cfg := fmt.Sprint(sent[i]["params"].(map[string]any)["config"]); cfg !=
		"map[mcp_servers.rad:map[bearer_token_env_var:RAD_MCP_TOKEN tool_timeout_sec:3700 url:http://127.0.0.1:7421/v1/mcp]]" {
		t.Errorf("config = %s, want rad's MCP server, with its token in the environment", cfg)
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
	// A fork after the turn goes through it.
	if end.ForkPoint != "01a11df1-ff10-75d3-baed-536d1128d96b" {
		t.Errorf("fork point = %q", end.ForkPoint)
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

func TestReplayCompact(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "codex-rec/ws")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	exe, env := replaytest.Command(t, "testdata/compact.ndjson", root)
	diag := filepath.Join(t.TempDir(), "diag.ndjson")
	rt, err := New(config.Command{Command: exe, Env: env}).Open(context.Background(),
		harness.OpenOptions{SessionID: "local", Cwd: cwd, Mode: "auto", Effort: "low", DiagPath: diag})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	replaytest.RunTurn(t, rt, "Reply with just OK.", nil)

	turn := replaytest.RunTurn(t, rt, "/compact", nil)
	_, sent := replaytest.Diag(t, diag)
	if last := sent[len(sent)-1]; last["method"] != "thread/compact/start" ||
		last["params"].(map[string]any)["threadId"] != "01a1268e-bdd8-7131-bc09-d641db40ad9e" {
		t.Errorf("sent %v, want thread/compact/start", last)
	}
	if len(turn.Keys) != 1 || turn.Items[turn.Keys[0]].Kind != model.ItemNotice || turn.Items[turn.Keys[0]].Text != "Context compacted" {
		t.Errorf("items = %v, want the notice", turn.Items)
	}
	if c := turn.Contexts(); !slices.Equal(c, []harness.ContextUsage{{Used: 4695, Window: 258400}}) {
		t.Errorf("context = %v, want the size after compacting", c)
	}
	if turn.End.Status != model.TurnCompleted {
		t.Errorf("end = %+v", turn.End)
	}
}

// TestReplayFork replays a fork's first turn: Codex copied the thread it
// was given into a new one, through the given turn, and goes on from there.
func TestReplayFork(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "codex-fork/ws")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	exe, env := replaytest.Command(t, "testdata/fork.ndjson", root)
	diag := filepath.Join(t.TempDir(), "diag.ndjson")
	rt, err := New(config.Command{Command: exe, Env: env}).Open(context.Background(), harness.OpenOptions{SessionID: "local", Cwd: cwd,
		Mode: "auto", Model: "gpt-6-luna", Effort: "low", ResumeID: "01a18f38-e752-7043-a080-f16a53d65767", Fork: true,
		ForkAt: "01a18f38-e784-7600-93cd-5055c9f7b559", DiagPath: diag})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, sent := replaytest.Diag(t, diag)
	i := slices.IndexFunc(sent, func(f map[string]any) bool { return f["method"] == "thread/fork" })
	if i < 0 {
		t.Fatalf("sent %v, want thread/fork", sent)
	}
	params := sent[i]["params"].(map[string]any)
	if params["threadId"] != "01a18f38-e752-7043-a080-f16a53d65767" || params["lastTurnId"] != "01a18f38-e784-7600-93cd-5055c9f7b559" ||
		params["cwd"] != cwd || params["model"] != "gpt-6-luna" {
		t.Errorf("thread/fork params = %v", params)
	}
	if rt.NativeID() != "01a18f39-1b91-74d1-9c80-56fe029f7d2a" {
		t.Errorf("NativeID() = %q, want the new thread's", rt.NativeID())
	}

	turn := replaytest.RunTurn(t, rt, "Without using any tools: list every request I have made to you in this conversation, in one short sentence.", nil)
	msgs := turn.ItemsOf(model.ItemAssistantMessage)
	if len(msgs) != 1 || msgs[0].Text != "You asked me to create `note.txt` containing `alpha`, then to list every request in this conversation without using tools." {
		t.Errorf("reply = %+v", msgs)
	}
	if turn.End.Status != model.TurnCompleted || turn.End.ForkPoint != "01a18f39-1bc6-7383-827b-2e95baa725d9" {
		t.Errorf("end = %+v", turn.End)
	}
	replaytest.CloseClean(t, rt)
}

func TestSplitAnswer(t *testing.T) {
	labels := []string{"Red", "Red, dark", "Blue"}
	for a, want := range map[string][]string{
		"Blue":                  {"Blue"},
		"Blue, Red":             {"Blue", "Red"},
		"Red, dark":             {"Red, dark"},
		"Red, dark, Blue":       {"Red, dark", "Blue"},
		"Blue, teal, not green": {"Blue", "teal, not green"},
		"teal, not green":       {"teal, not green"},
	} {
		if got := splitAnswer(a, labels); !slices.Equal(got, want) {
			t.Errorf("splitAnswer(%q) = %q, want %q", a, got, want)
		}
	}
}
