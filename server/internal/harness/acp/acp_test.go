package acp

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

const prompt = "Create hello.txt containing hi, then reply with one short sentence."

// openReplay also returns the diag log, which records what the adapter sent.
func openReplay(t *testing.T, id, transcript, ws string) (*Harness, harness.Runtime, string) {
	t.Helper()
	root := t.TempDir()
	cwd := filepath.Join(root, ws)
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	exe, env := replaytest.Command(t, transcript, root)
	h := New(config.ACPAgent{ID: id, Command: exe, Args: []string{"acp"}, Env: env})
	diag := filepath.Join(t.TempDir(), "diag.ndjson")
	rt, err := h.Open(context.Background(), harness.OpenOptions{SessionID: "local", Cwd: cwd,
		Instructions: "Show images as Markdown.", DiagPath: diag})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return h, rt, diag
}

// allowOnce picks the first allow_once option (neither transcript asks, though).
func allowOnce(ev harness.ApprovalEvent) string {
	for _, o := range ev.Approval.Options {
		if o.Kind == model.OptionAllowOnce {
			return o.ID
		}
	}
	return ev.Approval.Options[0].ID
}

func texts(items []model.Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Text)
	}
	return out
}

// Every item must be final by the time the turn ends; otherwise a later turn
// would complete it and move it below the next prompt.
func checkFinal(t *testing.T, turn *replaytest.Turn) {
	t.Helper()
	for _, k := range turn.Keys {
		if it := turn.Items[k]; it.Status != model.ItemCompleted {
			t.Errorf("%s item %q is %s at turn end", it.Kind, it.ID, it.Status)
		}
	}
}

func TestReplayCursor(t *testing.T) {
	h, rt, diag := openReplay(t, "cursor", "testdata/cursor.ndjson", "acp-rec/ws")
	if rt.NativeID() != "814aab6f-57ff-49f2-a32c-492fb2ff0b98" {
		t.Errorf("NativeID() = %q", rt.NativeID())
	}

	turn := replaytest.RunTurn(t, rt, prompt, allowOnce)

	// ACP has no system prompt, so the instructions go before the first prompt.
	_, sent := replaytest.Diag(t, diag)
	var blocks []any
	for _, f := range sent {
		if f["method"] == "session/prompt" {
			blocks = f["params"].(map[string]any)["prompt"].([]any)
			break
		}
	}
	if len(blocks) != 2 || blocks[0].(map[string]any)["text"] != "Show images as Markdown." || blocks[1].(map[string]any)["text"] != prompt {
		t.Errorf("first prompt = %v, want the instructions, then the prompt", blocks)
	}

	if got := texts(turn.ItemsOf(model.ItemReasoning)); !slices.Equal(got, []string{
		`Creating hello.txt with "hi" and replying with one short sentence.`,
		`Created hello.txt with "hi". Preparing a short reply.`,
	}) {
		t.Errorf("reasoning = %q", got)
	}
	if got := texts(turn.ItemsOf(model.ItemAssistantMessage)); !slices.Equal(got, []string{
		"I'll create `hello.txt` with `hi` and then reply in one short sentence.",
		"Created `hello.txt` with the word hi.",
	}) {
		t.Errorf("assistant = %q", got)
	}
	// Transcript order: thought, message, tool, thought, message.
	var kinds []model.ItemKind
	for _, k := range turn.Keys {
		kinds = append(kinds, turn.Items[k].Kind)
	}
	if want := []model.ItemKind{model.ItemReasoning, model.ItemAssistantMessage, model.ItemToolCall, model.ItemReasoning, model.ItemAssistantMessage}; !slices.Equal(kinds, want) {
		t.Errorf("item kinds = %v, want %v", kinds, want)
	}

	tools := turn.ItemsOf(model.ItemToolCall)
	if len(tools) != 1 {
		t.Fatalf("tool calls = %+v", tools)
	}
	tool := tools[0].Tool
	if tools[0].Status != model.ItemCompleted || tool.Kind != model.ToolEdit || tool.Name != "Edit File" || tool.Title != "Edit hello.txt" {
		t.Errorf("tool = %s %+v", tools[0].Status, tool)
	}
	if !slices.Equal(tool.Paths, []string{"hello.txt"}) || tool.Output != "Edited hello.txt" {
		t.Errorf("tool paths = %v, output = %q", tool.Paths, tool.Output)
	}
	checkFinal(t, turn)

	if turn.End.Status != model.TurnCompleted || turn.End.Error != "" || turn.End.Usage != nil {
		t.Errorf("turn end = %+v", turn.End)
	}

	// Modes and models learned from session/new are offered by later probes.
	var info model.HarnessInfo
	h.Overlay(&info)
	if info.DefaultMode != "agent" || len(info.Modes) != 3 || len(info.Models) == 0 || !info.Caps.SetMode || !info.Caps.ModelSelect {
		t.Errorf("overlay = %+v", info)
	}

	// Cursor puts the effort in the model value.
	if got, want := turn.ModelInfos(), []harness.ModelInfo{{ID: "grok-4.6[effort=high,fast=true]", Name: "grok-4.6", Effort: "high"}}; !slices.Equal(got, want) {
		t.Errorf("model = %v, want %v", got, want)
	}
	replaytest.CloseClean(t, rt)
}

func TestReplayOpenCode(t *testing.T) {
	_, rt, _ := openReplay(t, "opencode", "testdata/opencode.ndjson", "acp-rec/ws2")
	if rt.NativeID() != "ses_ee207f6aeffehxs1Ib9mB62GAR" {
		t.Errorf("NativeID() = %q", rt.NativeID())
	}

	turn := replaytest.RunTurn(t, rt, prompt, allowOnce)

	// The current mode comes from the "mode" config option.
	var modes []string
	for _, ev := range turn.Events {
		if m, ok := ev.(harness.ModeChanged); ok {
			modes = append(modes, m.Mode)
		}
	}
	if !slices.Equal(modes, []string{"build"}) {
		t.Errorf("modes = %v", modes)
	}

	if got := texts(turn.ItemsOf(model.ItemReasoning)); !slices.Equal(got, []string{"Let me create the file."}) {
		t.Errorf("reasoning = %q", got)
	}
	if got := texts(turn.ItemsOf(model.ItemAssistantMessage)); !slices.Equal(got, []string{"Created hello.txt containing hi."}) {
		t.Errorf("assistant = %q", got)
	}
	tools := turn.ItemsOf(model.ItemToolCall)
	if len(tools) != 1 {
		t.Fatalf("tool calls = %+v", tools)
	}
	tool := tools[0].Tool
	if tools[0].Status != model.ItemCompleted || tool.Kind != model.ToolEdit || tool.Name != "write" || tool.Title != "hello.txt" {
		t.Errorf("tool = %s %+v", tools[0].Status, tool)
	}
	if !slices.Equal(tool.Paths, []string{"hello.txt"}) || tool.Output != "Wrote file successfully." || len(tool.Input) == 0 {
		t.Errorf("tool = %+v", tool)
	}
	checkFinal(t, turn)

	end := turn.End
	if end.Status != model.TurnCompleted || end.Error != "" {
		t.Errorf("turn end = %+v", end)
	}
	if u := end.Usage; u == nil || u.InputTokens != 251 || u.OutputTokens != 7 || u.CacheReadTokens != 10880 {
		t.Errorf("usage = %+v", end.Usage)
	}
	if got, want := turn.Contexts(), []harness.ContextUsage{{Used: 11131, Window: 200000}}; !slices.Equal(got, want) {
		t.Errorf("context = %v, want %v", got, want)
	}

	if got, want := turn.ModelInfos(), []harness.ModelInfo{{ID: "opencode/big-pickle", Name: "OpenCode Zen/Big Pickle"}}; !slices.Equal(got, want) {
		t.Errorf("model = %v, want %v", got, want)
	}
	replaytest.CloseClean(t, rt)
}

func TestValueParam(t *testing.T) {
	for v, want := range map[string]string{
		"grok-4.6[effort=high,fast=true]":                    "high",
		"grok-4.7[context=256k,reasoning_effort=xhigh,fast]": "xhigh",
		"default[]":           "",
		"opencode/big-pickle": "",
	} {
		if got := valueParam(v, "effort", "reasoning_effort"); got != want {
			t.Errorf("valueParam(%q) = %q, want %q", v, got, want)
		}
	}
}
