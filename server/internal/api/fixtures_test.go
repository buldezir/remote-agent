package api

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"remote-agent/internal/fsbrowse"
	"remote-agent/internal/gitx"
	"remote-agent/internal/model"
	"remote-agent/internal/orchestrator"
)

var update = flag.Bool("update", false, "rewrite protocol/fixtures")

// Golden frames shared with the Swift client tests (apple/RAKit). They are
// generated from the Go types so the two sides cannot drift silently.
func fixtures() map[string]any {
	t0 := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(5 * time.Second)
	exit := 0
	sess := &model.Session{
		ID: "s1", ProjectID: "p1", Harness: "claude", Model: "sonnet", Effort: "high", Mode: "default",
		Workspace: model.Workspace{Kind: model.WorkspaceWorktree, Path: "/w/s1", Branch: "ra/fix-tests", BaseRef: "main"},
		Status:    model.SessionAwaitingApproval, NativeID: "n1", Title: "Fix the tests", CreatedAt: t0, UpdatedAt: t1,
		Context:   &model.ContextUsage{Used: 52000, Window: 200000},
		ModelInfo: &model.ModelInfo{ID: "claude-sonnet-5-5", Name: "Sonnet 5.5", Effort: "high"},
	}
	turn := &model.Turn{ID: "t1", SessionID: "s1", N: 1, Status: model.TurnCompleted, CheckpointBefore: "aaa", CheckpointAfter: "bbb",
		Usage: &model.Usage{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 5, CostUSD: 0.0123}, StartedAt: t0, EndedAt: &t1}
	item := func(id string, order int64, kind model.ItemKind, st model.ItemStatus) *model.Item {
		return &model.Item{ID: id, SessionID: "s1", TurnID: "t1", Order: order, Kind: kind, Status: st, CreatedAt: t0, UpdatedAt: t1}
	}
	user := item("i1", 1, model.ItemUserMessage, model.ItemCompleted)
	user.Text = "Fix the failing tests"
	user.Images = []model.ImageRef{{ID: "9f2c3a7d8e1b4c5a6f7e8d9c0b1a2f3e4d5c6b7a8f9e0d1c2b3a4f5e6d7c8b9a.png", MimeType: "image/png", Width: 1170, Height: 2532, Size: 482113}}
	reasoning := item("i2", 2, model.ItemReasoning, model.ItemCompleted)
	reasoning.Text = "Let me look at the test output."
	tool := item("i3", 3, model.ItemToolCall, model.ItemCompleted)
	tool.Tool = &model.ToolCall{Name: "Bash", Kind: model.ToolExecute, Title: "go test ./...", Input: json.RawMessage(`{"command":"go test ./..."}`), Output: "ok", ExitCode: &exit}
	tool.Images = []model.ImageRef{{ID: "0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9.webp", MimeType: "image/webp", Size: 20480}}
	assistant := item("i4", 4, model.ItemAssistantMessage, model.ItemInProgress)
	assistant.Text = "All **tests** pass now.\n\n```go\nfunc x() {}\n```\n\n![Screenshot](rad-image:5e6d7c8b9a0f1e2d3c4b5a69788796a5b4c3d2e1f0a9b8c7d6e5f4a3b2c1d0e9.png)"
	assistant.Images = []model.ImageRef{{ID: "5e6d7c8b9a0f1e2d3c4b5a69788796a5b4c3d2e1f0a9b8c7d6e5f4a3b2c1d0e9.png", MimeType: "image/png", Width: 2880, Height: 1800, Size: 1048576}}
	approval := item("i5", 5, model.ItemApproval, model.ItemPending)
	approval.Approval = &model.Approval{ToolItemID: "i3", ToolName: "Write", Title: "Write main.go", Input: json.RawMessage(`{"file_path":"main.go"}`),
		Options: []model.ApprovalOption{{ID: "allow", Label: "Allow", Kind: model.OptionAllowOnce}, {ID: "allow_session", Label: "Allow all edits this session", Kind: model.OptionAllowSession}, {ID: "deny", Label: "Deny", Kind: model.OptionDeny}}}
	question := item("i6", 6, model.ItemApproval, model.ItemResolved)
	question.Approval = &model.Approval{Title: "Claude has a question", Special: model.SpecialQuestion,
		Questions: []model.Question{{Question: "Which DB?", Header: "DB", Options: []model.QuestionOption{{Label: "SQLite", Description: "embedded"}, {Label: "Postgres"}}}},
		Options:   []model.ApprovalOption{{ID: "allow", Label: "Submit", Kind: model.OptionAllowOnce}, {ID: "deny", Label: "Skip", Kind: model.OptionDeny}},
		Decision:  &model.Decision{OptionID: "allow", Answers: map[string]string{"Which DB?": "SQLite"}, At: t1}}
	planApproval := item("i7", 7, model.ItemApproval, model.ItemCancelled)
	planApproval.Approval = &model.Approval{Title: "Ready to code?", Special: model.SpecialPlan, PlanText: "1. Do it",
		Options: []model.ApprovalOption{{ID: "allow", Label: "Yes", Kind: model.OptionAllowOnce}, {ID: "deny", Label: "Keep planning", Kind: model.OptionDeny}}}
	plan := item("i8", 8, model.ItemPlan, model.ItemCompleted)
	plan.Plan = &model.Plan{Entries: []model.PlanEntry{{Content: "Run tests", Status: "completed"}, {Content: "Fix bug", Status: "in_progress"}}}
	notice := item("i9", 9, model.ItemNotice, model.ItemCompleted)
	notice.Text = "Reverted 2 file(s) to their state before turn 1."
	errItem := item("i10", 10, model.ItemError, model.ItemCompleted)
	errItem.Text = "Agent exited: signal: killed"
	sub := item("i11", 11, model.ItemAssistantMessage, model.ItemCompleted)
	sub.ParentItemID, sub.Text = "i3", "sub-agent text"

	stream := model.SessionStream("s1")
	var evs []model.Event
	seq := int64(0)
	add := func(e model.Event) {
		seq++
		e.Stream, e.Seq, e.TS = stream, seq, t1
		evs = append(evs, e)
	}
	add(model.Event{Type: model.EvSessionUpserted, Session: sess})
	add(model.Event{Type: model.EvTurnUpserted, Turn: turn})
	for _, it := range []*model.Item{user, reasoning, tool, assistant, approval, question, planApproval, plan, notice, errItem, sub} {
		add(model.Event{Type: model.EvItemUpserted, Item: it})
	}
	add(model.Event{Type: "future.event", ID: "x"}) // clients must ignore unknown types

	proj := &model.Project{ID: "p1", Path: "/Users/me/projects/app", Name: "app", IsGitRepo: true, CreatedAt: t0}
	index := []model.Event{
		{Stream: model.IndexStream, Seq: 1, Type: model.EvProjectUpserted, Project: proj, TS: t0},
		{Stream: model.IndexStream, Seq: 2, Type: model.EvSessionUpserted, Session: sess, TS: t1},
		{Stream: model.IndexStream, Seq: 3, Type: model.EvProjectRemoved, ID: "p0", TS: t1},
	}

	return map[string]any{
		"notification_events_session.json": notification{"events", eventsParams{stream, evs}},
		"notification_events_index.json":   notification{"events", eventsParams{model.IndexStream, index}},
		"notification_synchronized.json":   notification{"synchronized", syncParams{stream, seq, true}},
		"notification_resync.json":         notification{"resync", streamParams{stream}},
		"response_error.json":              response{ID: json.RawMessage("7"), Error: &rpcError{Code: "conflict", Message: "approval is no longer pending"}},
		"response_subscribe.json":          response{ID: json.RawMessage("3"), Result: subscribeResult{Stream: stream, Seq: seq}},
		"response_harness_list.json": response{ID: json.RawMessage("4"), Result: map[string]any{"harnesses": []model.HarnessInfo{
			{ID: "claude", Name: "Claude Code", Protocol: "claude", Installed: true, Version: "2.1.295", AuthOK: true,
				Models: []model.Choice{{ID: "sonnet", Name: "Sonnet 5.5", Description: "Fast",
					Efforts: []model.Choice{{ID: "low", Name: "Low"}, {ID: "high", Name: "High"}}}},
				Efforts:     []model.Choice{{ID: "low", Name: "Low"}, {ID: "medium", Name: "Medium"}, {ID: "high", Name: "High"}},
				Modes:       []model.Choice{{ID: "default", Name: "Ask"}},
				DefaultMode: "default", Caps: model.HarnessCaps{Resume: true, Interrupt: true, SetMode: true, FreeModel: true, ModelSelect: true}},
			{ID: "codex", Name: "Codex", Protocol: "codex", Installed: true, Version: "0.161.0", Hint: "Not logged in: run `codex login` on this machine"},
		}}},
		"response_fs_list.json": response{ID: json.RawMessage("5"), Result: fsbrowse.Listing{Path: "/Users/me/projects", Parent: "/Users/me",
			Entries: []fsbrowse.Entry{{Name: "app", Path: "/Users/me/projects/app", IsGitRepo: true}, {Name: "notes", Path: "/Users/me/projects/notes"}}}},
		"response_diff.json": response{ID: json.RawMessage(`"d1"`), Result: orchestrator.Diff{From: "aaa", To: "bbb",
			Files: []gitx.FileStat{{Path: "main.go", Status: gitx.Modified, Additions: 3, Deletions: 1}, {Path: "new.go", OldPath: "old.go", Status: gitx.Renamed}, {Path: "logo.png", Status: gitx.Added, Binary: true}},
			Patch: "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1,2 +1,4 @@\n package main\n-func a() {}\n+func a() { b() }\n+func b() {}\n+\n", Truncated: false}},
		"response_session.json": response{ID: json.RawMessage("6"), Result: sess},
		"http_pair_response.json": pairResponse{serverInfo: serverInfo{ServerID: "srv1", Name: "studio", ProtocolVersion: model.ProtocolVersion, Version: "dev"},
			Token: "0123abcd", DeviceID: "dev1"},
	}
}

func TestFixtures(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "protocol", "fixtures")
	for name, v := range fixtures() {
		got, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, '\n')
		path := filepath.Join(dir, name)
		if *update {
			os.MkdirAll(dir, 0o755)
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (run go test ./internal/api -update)", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is stale; run go test ./internal/api -update and update the Swift client if needed", name)
		}
		// Every event in a notification must decode back into model.Event.
		var n struct {
			Params struct {
				Events []model.Event `json:"events"`
			} `json:"params"`
		}
		if err := json.Unmarshal(want, &n); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
