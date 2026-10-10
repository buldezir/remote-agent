package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"remote-agent/internal/config"
	"remote-agent/internal/harness"
	"remote-agent/internal/harness/replaytest"
	"remote-agent/internal/model"
)

func TestMain(m *testing.M) {
	replaytest.Main()
	os.Exit(m.Run())
}

// open starts a runtime that replays transcript in a workspace like the recorded one.
func open(t *testing.T, transcript, sessionID string) (harness.Runtime, string) {
	t.Helper()
	root := t.TempDir()
	cwd := filepath.Join(root, "pi-rec/ws")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	exe, env := replaytest.Command(t, transcript, root)
	diag := filepath.Join(t.TempDir(), "diag.ndjson")
	rt, err := New(config.Command{Command: exe, Env: env}).Open(context.Background(),
		harness.OpenOptions{SessionID: sessionID, Cwd: cwd, Model: "cursor/composer-2",
			Instructions: "Show images as Markdown.", DiagPath: diag})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if command, _ := replaytest.Diag(t, diag); !strings.Contains(command, `"--append-system-prompt" "Show images as Markdown."`) {
		t.Errorf("command = %s, want the instructions in the system prompt", command)
	}
	if rt.NativeID() != sessionID {
		t.Errorf("NativeID() = %q, want the session id", rt.NativeID())
	}
	return rt, cwd
}

func texts(items []model.Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Text)
	}
	return out
}

func TestReplayToolsAndGatedCommand(t *testing.T) {
	const sid = "01a12103-a87d-7e9a-bb13-cbd7b134718c"
	rt, _ := open(t, "testdata/write.ndjson", sid)

	turn := replaytest.RunTurn(t, rt, "Create hello.txt containing the word hi using the write tool, then show it with `cat hello.txt` using bash. Then reply with one short sentence.",
		func(harness.ApprovalEvent) string { t.Error("unexpected approval"); return "" })
	if ev, ok := turn.Events[0].(harness.NativeID); !ok || ev.ID != sid {
		t.Errorf("first event = %#v, want NativeID", turn.Events[0])
	}
	// --model cursor/composer-2 resolved to another model inside pi.
	if got, want := turn.ModelInfos(), []harness.ModelInfo{{ID: "cursor/composer-2.5-max-mode-fast"}}; !slices.Equal(got, want) {
		t.Errorf("model = %v, want %v", got, want)
	}
	tools := turn.ItemsOf(model.ItemToolCall)
	if len(tools) != 2 {
		t.Fatalf("tool calls = %+v", tools)
	}
	write, bash := tools[0], tools[1]
	if write.Status != model.ItemCompleted || write.Tool.Kind != model.ToolEdit || write.Tool.Title != "Write hello.txt" ||
		!slices.Equal(write.Tool.Paths, []string{"hello.txt"}) || write.Tool.Output != "Successfully wrote 2 bytes to hello.txt" {
		t.Errorf("write = %s %+v", write.Status, write.Tool)
	}
	if bash.Status != model.ItemCompleted || bash.Tool.Kind != model.ToolExecute || bash.Tool.Title != "cat hello.txt" || bash.Tool.Output != "hi" {
		t.Errorf("bash = %s %+v", bash.Status, bash.Tool)
	}
	if got := texts(turn.ItemsOf(model.ItemReasoning)); len(got) != 3 || got[1] != "Now displaying hello.txt with bash." {
		t.Errorf("reasoning = %q", got)
	}
	if got, want := texts(turn.ItemsOf(model.ItemAssistantMessage)), []string{"Created `hello.txt` with “hi” and `cat` confirmed it prints `hi`."}; !slices.Equal(got, want) {
		t.Errorf("assistant = %q, want %q", got, want)
	}
	for _, it := range turn.Items {
		if it.Status == model.ItemInProgress {
			t.Errorf("item %s left in progress: %+v", it.ID, it)
		}
	}
	if n := len(turn.ItemsOf(model.ItemUserMessage)); n != 0 {
		t.Errorf("user messages = %d; the orchestrator adds the prompt itself", n)
	}
	if want := []harness.ContextUsage{{Used: 5364, Window: 200000}, {Used: 5666, Window: 200000}, {Used: 5789, Window: 200000}}; !slices.Equal(turn.Contexts(), want) {
		t.Errorf("context = %v, want %v", turn.Contexts(), want)
	}
	if end := turn.End; end.Status != model.TurnCompleted || end.Usage == nil || end.Usage.InputTokens != 16125 || end.Usage.OutputTokens != 694 || end.Usage.CostUSD <= 0 {
		t.Errorf("end = %+v usage %+v", end, end.Usage)
	}

	// pi's example permission-gate extension asks before rm -rf.
	turn = replaytest.RunTurn(t, rt, "Now delete hello.txt by running exactly `rm -rf hello.txt` with bash, then reply with one short sentence.",
		func(harness.ApprovalEvent) string { return "opt:0" })
	if len(turn.Approvals) != 1 {
		t.Fatalf("approvals = %+v", turn.Approvals)
	}
	ap := turn.Approvals[0].Approval
	wantOpts := []model.ApprovalOption{{ID: "opt:0", Label: "Yes", Kind: model.OptionAllowOnce}, {ID: "opt:1", Label: "No", Kind: model.OptionDeny}}
	if ap.Title != "⚠️ Dangerous command:" || ap.Detail != "rm -rf hello.txt\n\nAllow?" || !slices.Equal(ap.Options, wantOpts) {
		t.Errorf("approval = %+v", ap)
	}
	tools = turn.ItemsOf(model.ItemToolCall)
	if len(tools) != 1 || tools[0].Tool.Title != "rm -rf hello.txt" || tools[0].Status != model.ItemCompleted || tools[0].Tool.Output != "(no output)" {
		t.Errorf("tools = %+v", tools)
	}
	if got := texts(turn.ItemsOf(model.ItemAssistantMessage)); !slices.Equal(got, []string{"`hello.txt` was removed with `rm -rf hello.txt`."}) {
		t.Errorf("assistant = %q", got)
	}
	if end := turn.End; end.Status != model.TurnCompleted || end.Usage.InputTokens != 5944 || end.Usage.OutputTokens != 86 {
		t.Errorf("end = %+v usage %+v", end, end.Usage)
	}
	replaytest.CloseClean(t, rt)
}

func TestReplayInterrupt(t *testing.T) {
	rt, _ := open(t, "testdata/interrupt.ndjson", "01a12104-3979-735b-b1dd-3424002d2ec8")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := rt.Prompt(ctx, harness.Input{Text: "Run `sleep 20` with bash, then reply with one short sentence."}); err != nil {
		t.Fatal(err)
	}
	var tool model.Item
	var end *harness.TurnEnded
	for end == nil {
		select {
		case ev := <-rt.Events():
			switch ev := ev.(type) {
			case harness.ItemEvent:
				if ev.Item.Kind != model.ItemToolCall {
					break
				}
				first := tool.ID == ""
				tool = ev.Item
				if first {
					// pi settles before it answers abort, so the turn ends only once.
					if err := rt.Interrupt(ctx); err != nil {
						t.Fatalf("interrupt: %v", err)
					}
				}
			case harness.TurnEnded:
				end = &ev
			}
		case <-ctx.Done():
			t.Fatal("timed out")
		}
	}
	if end.Status != model.TurnInterrupted {
		t.Errorf("end = %+v", end)
	}
	if tool.Status != model.ItemFailed || tool.Tool.Title != "sleep 20" || tool.Tool.Output != "Command aborted" {
		t.Errorf("tool = %s %+v", tool.Status, tool.Tool)
	}
	for _, ev := range replaytest.CloseClean(t, rt) {
		if _, ok := ev.(harness.TurnEnded); ok {
			t.Error("turn ended twice")
		}
	}
}

func TestReplayDialogs(t *testing.T) {
	rt, _ := open(t, "testdata/dialogs.ndjson", "01a12105-fcf6-7854-88ae-580bac324494")
	// A test extension's /radtest asks to confirm, then to pick one of five
	// colors, then for text. pi answers the prompt only after all of that.
	turn := replaytest.RunTurn(t, rt, "/radtest", func(a harness.ApprovalEvent) string {
		if a.Approval.Special == model.SpecialQuestion {
			return "cancel"
		}
		return "yes"
	})
	if len(turn.Approvals) != 2 {
		t.Fatalf("approvals = %+v", turn.Approvals)
	}
	confirm, sel := turn.Approvals[0].Approval, turn.Approvals[1].Approval
	if confirm.Title != "Deploy to staging?" || confirm.Detail != "This pushes the current branch." || len(confirm.Options) != 2 || confirm.Options[1].Kind != model.OptionDeny {
		t.Errorf("confirm = %+v", confirm)
	}
	if sel.Special != model.SpecialQuestion || len(sel.Questions) != 1 || sel.Questions[0].Question != "Pick a color" || len(sel.Questions[0].Options) != 5 {
		t.Errorf("select = %+v", sel)
	}
	want := []string{"Pi asked for text (Name the release), which the app can't answer yet.", "confirm=true color=undefined name=undefined"}
	if got := texts(turn.ItemsOf(model.ItemNotice)); !slices.Equal(got, want) {
		t.Errorf("notices = %q, want %q", got, want)
	}
	if turn.End.Status != model.TurnCompleted || turn.End.Usage != nil {
		t.Errorf("end = %+v", turn.End)
	}
	replaytest.CloseClean(t, rt)
}

func TestReplayCommandWithoutRun(t *testing.T) {
	rt, _ := open(t, "testdata/command.ndjson", "01a12106-4fd5-704e-acca-a4fe15371ede")
	// The extension's /radquiet does nothing, so pi never starts or settles a run.
	turn := replaytest.RunTurn(t, rt, "/radquiet", func(harness.ApprovalEvent) string { return "" })
	if turn.End.Status != model.TurnCompleted || len(turn.Items) != 0 {
		t.Errorf("end = %+v, items %+v", turn.End, turn.Items)
	}
	replaytest.CloseClean(t, rt)
}

func TestReplayProviderError(t *testing.T) {
	rt, _ := open(t, "testdata/error.ndjson", "01a12103-19d8-7d07-9266-9ddeb87a39ce")
	turn := replaytest.RunTurn(t, rt, "Create hello.txt containing the word hi using the write tool, then show it with `cat hello.txt` using bash. Then reply with one short sentence.",
		func(harness.ApprovalEvent) string { return "" })
	if got, want := turn.ModelInfos(), []harness.ModelInfo{{ID: "anthropic/claude-haiku-4-5", Effort: "low"}}; !slices.Equal(got, want) {
		t.Errorf("model = %v, want %v", got, want)
	}
	if turn.End.Status != model.TurnFailed || turn.End.Error != "404 404 page not found" || turn.End.Usage != nil {
		t.Errorf("end = %+v", turn.End)
	}
	replaytest.CloseClean(t, rt)
}

func TestEfforts(t *testing.T) {
	str := func(s string) *string { return &s }
	ids := func(cs []model.Choice) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.ID)
		}
		return out
	}
	cases := []struct {
		m    piModel
		want []string
	}{
		{piModel{}, nil},
		{piModel{Reasoning: true}, []string{"off", "minimal", "low", "medium", "high"}},
		{piModel{Reasoning: true, ThinkingLevelMap: map[string]*string{"off": nil, "xhigh": str("xhigh"), "max": str("max")}},
			[]string{"minimal", "low", "medium", "high", "xhigh", "max"}},
		{piModel{Reasoning: true, ThinkingLevelMap: map[string]*string{"off": nil, "minimal": nil, "low": nil, "high": nil, "max": nil, "xhigh": str("xhigh")}},
			[]string{"medium", "xhigh"}},
	}
	for _, c := range cases {
		if got := ids(efforts(c.m)); !slices.Equal(got, c.want) {
			t.Errorf("efforts(%+v) = %v, want %v", c.m, got, c.want)
		}
	}
}

func TestChoicesNameSharedModels(t *testing.T) {
	got := choices([]piModel{
		{ID: "gpt-5.1", Name: "GPT-5.1", Provider: "openai"},
		{ID: "gpt-5.1", Name: "GPT-5.1", Provider: "cursor"},
		{ID: "composer-2", Name: "Composer 2", Provider: "cursor"},
	})
	want := []model.Choice{
		{ID: "openai/gpt-5.1", Name: "GPT-5.1 (openai)"},
		{ID: "cursor/gpt-5.1", Name: "GPT-5.1 (cursor)"},
		{ID: "cursor/composer-2", Name: "Composer 2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("choices = %+v, want %+v", got, want)
	}
}

func TestDialogAnswer(t *testing.T) {
	sel := &dialog{method: "select", options: []string{"Yes", "No"}}
	question := &dialog{method: "select", options: []string{"Red", "Green"}, question: "Pick a color"}
	confirm := &dialog{method: "confirm"}
	cases := []struct {
		d    *dialog
		resp harness.Response
		want string
	}{
		{sel, harness.Response{OptionID: "opt:1", Message: "too risky"}, `{"id":"d","type":"extension_ui_response","value":"No"}`},
		{sel, harness.Response{OptionID: "opt:7"}, `{"cancelled":true,"id":"d","type":"extension_ui_response"}`},
		{question, harness.Response{OptionID: "choose", Answers: map[string]string{"Pick a color": "Green"}}, `{"id":"d","type":"extension_ui_response","value":"Green"}`},
		{question, harness.Response{OptionID: "cancel"}, `{"cancelled":true,"id":"d","type":"extension_ui_response"}`},
		{confirm, harness.Response{OptionID: "yes"}, `{"confirmed":true,"id":"d","type":"extension_ui_response"}`},
		{confirm, harness.Response{OptionID: "no"}, `{"confirmed":false,"id":"d","type":"extension_ui_response"}`},
	}
	for _, c := range cases {
		b, _ := json.Marshal(c.d.answer("d", c.resp))
		if string(b) != c.want {
			t.Errorf("answer(%+v, %+v) = %s, want %s", c.d, c.resp, b, c.want)
		}
	}
	for label, want := range map[string]model.OptionKind{"Yes": model.OptionAllowOnce, "No": model.OptionDeny, "Block": model.OptionDeny,
		"Always allow": model.OptionAllowSession, "Allow": model.OptionAllowOnce, "Don't run it": model.OptionDeny} {
		if got := optionKind(label); got != want {
			t.Errorf("optionKind(%q) = %s, want %s", label, got, want)
		}
	}
}

func TestDescribeTool(t *testing.T) {
	cwd := t.TempDir()
	link := filepath.Join(t.TempDir(), "ws")
	if err := os.Symlink(cwd, link); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(cwd)
	cases := []struct {
		name, args string
		kind       model.ToolKind
		title      string
	}{
		{"read", `{"path":"src/main.go"}`, model.ToolRead, "Read src/main.go"},
		{"edit", `{"path":"` + filepath.Join(real, "a.go") + `","oldText":"x","newText":"y"}`, model.ToolEdit, "Edit a.go"},
		{"bash", `{"command":"go test ./...\necho done","timeout":30000}`, model.ToolExecute, "go test ./... …"},
		{"grep", `{"pattern":"TODO","path":"internal"}`, model.ToolSearch, "Grep TODO in internal"},
		{"ls", `{}`, model.ToolSearch, "List ."},
		{"web_search", `{"query":"pi rpc"}`, model.ToolFetch, "web_search: pi rpc"},
		{"subagent", `{"agent":"scout"}`, model.ToolOther, "subagent"},
	}
	for _, c := range cases {
		kind, title, _ := describeTool(c.name, json.RawMessage(c.args), link)
		if kind != c.kind || title != c.title {
			t.Errorf("describeTool(%s, %s) = %s %q, want %s %q", c.name, c.args, kind, title, c.kind, c.title)
		}
	}
}
