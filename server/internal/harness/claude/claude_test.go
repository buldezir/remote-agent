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
	if end.Usage == nil || end.Usage.CostUSD <= 0 || end.Usage.InputTokens != 4 || end.Usage.OutputTokens != 312 ||
		end.Usage.CacheReadTokens != 33571 || end.Usage.CacheWriteTokens != 37647 {
		t.Errorf("usage = %+v", end.Usage)
	}
	// Context is the main agent's last request; the window arrives with the result.
	want := []harness.ContextUsage{{Used: 33593}, {Used: 37652}, {Used: 37652, Window: 1000000}}
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
