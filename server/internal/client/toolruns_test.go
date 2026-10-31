package client

import (
	"slices"
	"testing"

	"remote-agent/internal/model"
)

// The vectors every client's ToolRuns shares: RAKit's ToolRunsTests,
// web/src/store/toolRuns.test.ts and Android's core.

func runItem(id string, kind model.ItemKind, turn string, status model.ItemStatus) *model.Item {
	return &model.Item{ID: id, SessionID: "s", TurnID: turn, Kind: kind, Status: status}
}

func runTool(id string, kind model.ToolKind, turn string, status model.ItemStatus, paths ...string) *model.Item {
	it := runItem(id, model.ItemToolCall, turn, status)
	it.Tool = &model.ToolCall{Name: id, Kind: kind, Title: id, Paths: paths}
	return it
}

func runTurn(id string, status model.TurnStatus) *model.Turn {
	return &model.Turn{ID: id, SessionID: "s", N: 1, Status: status}
}

// runTranscript is a finished turn with a run, a failed command, a lone
// edit and a reply, and a running one with a run still going.
func runTranscript() []*model.Item {
	done := model.ItemCompleted
	return []*model.Item{
		runItem("u1", model.ItemUserMessage, "t1", done),
		runItem("r1", model.ItemReasoning, "t1", done),
		runTool("a", model.ToolExecute, "t1", done),
		runTool("b", model.ToolRead, "t1", done, "README.md"),
		runItem("r2", model.ItemReasoning, "t1", done),
		runTool("c", model.ToolSearch, "t1", done),
		runTool("d", model.ToolRead, "t1", done, "README.md"),
		runTool("e", model.ToolEdit, "t1", done, "main.go"),
		runTool("f", model.ToolEdit, "t1", done, "main.go", "main_test.go"),
		runTool("g", model.ToolExecute, "t1", model.ItemFailed),
		runTool("h", model.ToolEdit, "t1", done, "main.go"),
		runItem("m1", model.ItemAssistantMessage, "t1", done),
		runItem("u2", model.ItemUserMessage, "t2", done),
		runTool("i", model.ToolFetch, "t2", done),
		runTool("j", model.ToolThink, "t2", done),
		runTool("k", model.ToolOther, "t2", model.ItemInProgress),
	}
}

func runTurns() map[string]*model.Turn {
	return map[string]*model.Turn{"t1": runTurn("t1", model.TurnCompleted), "t2": runTurn("t2", model.TurnRunning)}
}

func rowIDs(rows []TranscriptRow) []string {
	var out []string
	for _, r := range rows {
		switch {
		case r.Item != nil:
			out = append(out, r.Item.ID)
		case r.Run != nil && r.Expanded:
			out = append(out, r.Run.ID+" open")
		case r.Run != nil:
			out = append(out, r.Run.ID+" shut")
		default:
			out = append(out, "end "+r.TurnEnd.ID)
		}
	}
	return out
}

func itemIDs(items []*model.Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func eq[T comparable](t *testing.T, got, want []T) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestToolRunsFoldRunsOfToolCalls(t *testing.T) {
	runs := ToolRuns(runTranscript(), runTurns())
	if len(runs) != 2 || runs[0].ID != "tools:r1" || runs[1].ID != "tools:i" {
		t.Fatalf("runs = %v", runs)
	}
	eq(t, itemIDs(runs[0].Items), []string{"r1", "a", "b", "r2", "c", "d", "e", "f"})
	if n := len(runs[0].Tools()); n != 6 {
		t.Errorf("tools = %d", n)
	}
	if runs[0].Live || runs[0].Working() {
		t.Error("the first run is live")
	}
	if got := runs[0].Title(); got != "Ran 1 command, edited 2 files, read 1 file, searched 1 time" {
		t.Errorf("title = %q", got)
	}
	var kinds []model.ToolKind
	var counts []int
	for _, p := range runs[0].Parts() {
		kinds, counts = append(kinds, p.Kind), append(counts, p.Count)
	}
	eq(t, kinds, []model.ToolKind{model.ToolExecute, model.ToolEdit, model.ToolRead, model.ToolSearch})
	eq(t, counts, []int{1, 2, 1, 1})
	// Its turn runs and nothing came after it.
	eq(t, itemIDs(runs[1].Items), []string{"i", "j", "k"})
	if !runs[1].Live || !runs[1].Working() {
		t.Error("the second run isn't live")
	}
	if got := runs[1].Title(); got != "Fetched 1 page, started 1 agent, used 1 tool" {
		t.Errorf("title = %q", got)
	}
}

func TestToolRunsShowShutUnlessLive(t *testing.T) {
	eq(t, rowIDs(TranscriptRows(runTranscript(), runTurns(), nil, true)),
		[]string{"u1", "tools:r1 shut", "g", "h", "m1", "u2", "tools:i open", "i", "j", "k"})
	// The user's choice wins, either way.
	eq(t, rowIDs(TranscriptRows(runTranscript(), runTurns(), map[string]bool{"tools:r1": true, "tools:i": false}, true)),
		[]string{"u1", "tools:r1 open", "r1", "a", "b", "r2", "c", "d", "e", "f", "g", "h", "m1", "u2", "tools:i shut"})
	// Not grouping: every item is a row.
	eq(t, rowIDs(TranscriptRows(runTranscript(), runTurns(), nil, false)), itemIDs(runTranscript()))
}

func TestToolRunsFoldOnceTheAgentRepliesOrTheTurnEnds(t *testing.T) {
	items, turns := runTranscript(), runTurns()
	if !ToolRuns(items, turns)[1].Live {
		t.Error("not live")
	}
	// A prompt queued meanwhile isn't the turn's.
	items = append(items, runItem("q", model.ItemUserMessage, "", model.ItemQueued))
	if !ToolRuns(items, turns)[1].Live {
		t.Error("not live after a queued prompt")
	}
	items = append(items, runItem("m2", model.ItemAssistantMessage, "t2", model.ItemInProgress))
	if ToolRuns(items, turns)[1].Live {
		t.Error("live after a reply")
	}
	rows := rowIDs(TranscriptRows(items, turns, nil, true))
	eq(t, rows[len(rows)-4:], []string{"u2", "tools:i shut", "q", "m2"})
	turns["t2"] = runTurn("t2", model.TurnCompleted)
	if ToolRuns(runTranscript(), turns)[1].Live {
		t.Error("live after its turn ended")
	}
}

func TestToolRunsLeaveCallsThatNeedAttentionAlone(t *testing.T) {
	done := model.ItemCompleted
	shot := runTool("s", model.ToolOther, "t1", done)
	shot.Images = []model.ImageRef{{ID: "img", MimeType: "image/png", Size: 1}}
	items := []*model.Item{
		runTool("a", model.ToolExecute, "t1", done), runTool("b", model.ToolEdit, "t1", model.ItemPending), runTool("c", model.ToolExecute, "t1", done),
		shot, runTool("d", model.ToolRead, "t1", done), runItem("r", model.ItemReasoning, "t1", done), runTool("e", model.ToolRead, "t1", done),
	}
	// Pending and with images, a call breaks the run; one call alone
	// doesn't fold, and reasoning doesn't count.
	eq(t, rowIDs(TranscriptRows(items, runTurns(), nil, true)), []string{"a", "b", "c", "s", "tools:d shut"})
	if got := ToolRuns(items, runTurns())[0].Title(); got != "Read 2 files" {
		t.Errorf("title = %q", got)
	}
	// Nor does reasoning on its own.
	alone := []*model.Item{runItem("r1", model.ItemReasoning, "t1", done), runItem("r2", model.ItemReasoning, "t1", done), runTool("x", model.ToolRead, "t1", done)}
	if runs := ToolRuns(alone, runTurns()); len(runs) != 0 {
		t.Errorf("runs = %v", runs)
	}
}

func TestToolRunsStayWithinATurn(t *testing.T) {
	done := model.ItemCompleted
	items := []*model.Item{runTool("a", model.ToolExecute, "t1", done), runTool("b", model.ToolExecute, "t2", done), runTool("c", model.ToolExecute, "t2", done)}
	eq(t, rowIDs(TranscriptRows(items, runTurns(), nil, true)), []string{"a", "tools:b open", "b", "c"})
}

func TestToolRunsEndATurnThatEndedBadlyAfterTheRun(t *testing.T) {
	done := model.ItemCompleted
	items := []*model.Item{runItem("u", model.ItemUserMessage, "t1", done), runTool("a", model.ToolExecute, "t1", done), runTool("b", model.ToolRead, "t1", done)}
	failed := map[string]*model.Turn{"t1": runTurn("t1", model.TurnFailed)}
	eq(t, rowIDs(TranscriptRows(items, failed, nil, true)), []string{"u", "tools:a shut", "end t1"})
	eq(t, rowIDs(TranscriptRows(items, failed, map[string]bool{"tools:a": true}, true)), []string{"u", "tools:a open", "a", "b", "end t1"})
	interrupted := map[string]*model.Turn{"t1": runTurn("t1", model.TurnInterrupted)}
	eq(t, rowIDs(TranscriptRows(items, interrupted, nil, false)), []string{"u", "a", "b", "end t1"})
}

func TestToolRunContaining(t *testing.T) {
	for id, want := range map[string]string{"c": "tools:r1", "r2": "tools:r1", "g": "", "m1": ""} {
		got := ""
		if r := ToolRunContaining(id, runTranscript(), runTurns()); r != nil {
			got = r.ID
		}
		if got != want {
			t.Errorf("%s: got %q, want %q", id, got, want)
		}
	}
}

func TestToolRunPartsCountFilesByPathAndTheRestByCall(t *testing.T) {
	done := model.ItemCompleted
	var items []*model.Item
	for _, c := range []struct {
		id   string
		kind model.ToolKind
		path []string
	}{
		{"a", model.ToolRead, nil}, {"b", model.ToolRead, nil}, {"c", model.ToolEdit, []string{"x"}}, {"d", model.ToolEdit, []string{"x"}},
		{"e", model.ToolSearch, nil}, {"f", model.ToolSearch, nil}, {"g", model.ToolFetch, nil}, {"h", model.ToolFetch, nil},
		{"i", model.ToolThink, nil}, {"j", model.ToolThink, nil}, {"k", model.ToolOther, nil}, {"l", model.ToolOther, nil},
		{"m", model.ToolExecute, nil}, {"n", model.ToolExecute, nil},
	} {
		items = append(items, runTool(c.id, c.kind, "t1", done, c.path...))
	}
	if got := ToolRunTitle(ToolRunParts(items)); got != "Ran 2 commands, edited 1 file, read 2 files, searched 2 times, fetched 2 pages, started 2 agents, used 2 tools" {
		t.Errorf("title = %q", got)
	}
	if got := ToolRunTitle(nil); got != "" {
		t.Errorf("title = %q", got)
	}
	// A kind ra doesn't know is a tool.
	if got := ToolRunTitle(ToolRunParts([]*model.Item{runTool("z", "delete", "t1", done)})); got != "Used 1 tool" {
		t.Errorf("title = %q", got)
	}
}
