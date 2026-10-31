package claude

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"remote-agent/internal/config"
	"remote-agent/internal/harness"
	"remote-agent/internal/harness/native/nativetest"
	"remote-agent/internal/harness/replaytest"
	"remote-agent/internal/images"
	"remote-agent/internal/model"
)

const (
	importSession = "5b0e7c1a-2f4d-4c8e-9a61-3d7f2b8e4c90"
	otherSession  = "9e8d7c6b-5a49-4382-a1b0-c9d8e7f6a5b4"
)

// importHarness is Claude Code with testdata/import as its config folder, as
// CLAUDE_CONFIG_DIR in the harness's config sets it.
func importHarness(t *testing.T) *Harness {
	t.Helper()
	dir := nativetest.CopyTree(t, "testdata/import", map[string]time.Duration{
		importSession + ".jsonl": 10 * time.Minute,
		otherSession + ".jsonl":  48 * time.Hour,
	})
	return New(config.Command{Env: map[string]string{"CLAUDE_CONFIG_DIR": dir}})
}

func TestListNative(t *testing.T) {
	h := importHarness(t)
	list, err := h.ListNative(context.Background(), "/work/app", nil)
	if err != nil {
		t.Fatal(err)
	}
	// rad's session (sdk-go), the one without a reply and the sub-agent's are
	// left out; the user's name for a session comes before Claude's.
	var ids []string
	for _, s := range list {
		ids = append(ids, s.ID)
	}
	if !slices.Equal(ids, []string{importSession, otherSession}) {
		t.Fatalf("ids = %v", ids)
	}
	if s := list[0]; s.Title != "Login fixes" || s.Messages != 10 || time.Since(s.UpdatedAt) > 11*time.Minute || s.Active {
		t.Errorf("first = %+v", s)
	}
	if s := list[1]; s.Title != "Changelog for every release" || s.Messages != 2 {
		t.Errorf("second = %+v", s)
	}

	list, _ = h.ListNative(context.Background(), "/work/app", map[string]bool{importSession: true})
	if len(list) != 1 || list[0].ID != otherSession {
		t.Errorf("without the known one: %+v", list)
	}
	if list, _ := h.ListNative(context.Background(), "/work/other", nil); len(list) != 0 {
		t.Errorf("another folder's: %+v", list)
	}
}

func TestReadNative(t *testing.T) {
	h := importHarness(t)
	store := images.New(t.TempDir())
	tr, err := h.ReadNative(context.Background(), "/work/app", importSession, store)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Title != "Login fixes" || tr.Model != "claude-opus-5-5" || time.Since(tr.UpdatedAt) > 11*time.Minute {
		t.Errorf("transcript = %q %q %v", tr.Title, tr.Model, tr.UpdatedAt)
	}
	type want struct {
		status    model.TurnStatus
		forkPoint string
		items     []string // kind: text or title
	}
	wants := []want{
		{model.TurnCompleted, "a6", []string{
			"user_message: Fix the flaky login test", "reasoning: The test probably races the server.",
			"tool_call: Read login_test.go", "tool_call: go test ./...", "plan: ", "tool_call: mcp__browser__screenshot: http://localhost:8080/login",
			"assistant_message: The test waited on a timer. It now waits for the server instead, and passes."}},
		{model.TurnCompleted, "", []string{"user_message: /compact keep the test names", "notice: Context compacted"}},
		// The branch the user rewound ("Explain it again") is left out.
		{model.TurnCompleted, "a7", []string{"user_message: What does this screenshot show?", "assistant_message: The login form, with the error under the password."}},
		{model.TurnInterrupted, "a9", []string{"user_message: Now refactor the session store", "tool_call: Edit store.go"}},
		{model.TurnCompleted, "a10", []string{`notice: Background task finished: Background command "Watch the build" completed`, "assistant_message: The build finished."}},
		{model.TurnFailed, "", []string{"user_message: One more thing", "error: API Error: 529 Overloaded"}},
	}
	if len(tr.Turns) != len(wants) {
		t.Fatalf("%d turns: %+v", len(tr.Turns), tr.Turns)
	}
	for i, w := range wants {
		turn := tr.Turns[i]
		var got []string
		for _, it := range turn.Items {
			text := it.Text
			if it.Tool != nil {
				text = it.Tool.Title
			}
			got = append(got, string(it.Kind)+": "+text)
		}
		if turn.Status != w.status || turn.ForkPoint != w.forkPoint || !slices.Equal(got, w.items) {
			t.Errorf("turn %d = %s %q %q, want %s %q %q", i+1, turn.Status, turn.ForkPoint, got, w.status, w.forkPoint, w.items)
		}
		if turn.StartedAt.IsZero() || turn.EndedAt.Before(turn.StartedAt) {
			t.Errorf("turn %d ran %v–%v", i+1, turn.StartedAt, turn.EndedAt)
		}
	}

	first := tr.Turns[0].Items
	if bash := first[3]; bash.Status != model.ItemFailed || !strings.Contains(bash.Tool.Output, "FAIL") || bash.Tool.Kind != model.ToolExecute {
		t.Errorf("bash = %+v %+v", bash, bash.Tool)
	}
	if read := first[2]; read.Status != model.ItemCompleted || !strings.HasPrefix(read.Tool.Output, "package app") || !slices.Equal(read.Tool.Paths, []string{"login_test.go"}) {
		t.Errorf("read = %+v %+v", read, read.Tool)
	}
	if plan := first[4].Plan; plan == nil || len(plan.Entries) != 2 || plan.Entries[1] != (model.PlanEntry{Content: "Run the tests again", Status: "in_progress"}) {
		t.Errorf("plan = %+v", plan)
	}
	if shot := first[5]; shot.Tool.Output != "Screenshot taken" || len(shot.Images) != 1 || shot.Images[0].Width != 8 {
		t.Errorf("screenshot = %+v", shot)
	}
	if prompt := tr.Turns[2].Items[0]; len(prompt.Images) != 1 || prompt.Images[0].MimeType != "image/png" {
		t.Errorf("prompt's images = %+v", prompt.Images)
	}
	if edit := tr.Turns[3].Items[1]; edit.Status != model.ItemFailed {
		t.Errorf("edit = %+v", edit)
	}
	if !first[0].CreatedAt.Equal(time.Date(2026, 10, 30, 9, 0, 1, 0, time.UTC)) {
		t.Errorf("prompt's time = %v", first[0].CreatedAt)
	}
}

func TestReadNativeMissing(t *testing.T) {
	h := importHarness(t)
	for _, id := range []string{"0f0f0f0f-0000-4000-8000-000000000000", "../../x", "", importSession + "/../" + importSession} {
		if _, err := h.ReadNative(context.Background(), "/work/app", id, nil); !errors.Is(err, harness.ErrNotFound) {
			t.Errorf("ReadNative(%q) = %v, want ErrNotFound", id, err)
		}
	}
	if _, err := h.ReadNative(context.Background(), "/work/other", importSession, nil); !errors.Is(err, harness.ErrNotFound) {
		t.Errorf("from another folder: %v", err)
	}
}

// TestReplayResume replays the turn of a session picked up from the
// terminal: Claude resumes it by its id, and keeps it. The transcript is the
// first turn of compact.ndjson, whose session id it resumes.
func TestReplayResume(t *testing.T) {
	diag := t.TempDir() + "/diag.ndjson"
	const id = "01a1268a-c7f9-77b8-ad00-c550f1168608"
	rt, _ := openReplay(t, "testdata/resume.ndjson", "claude-rec/ws",
		harness.OpenOptions{SessionID: "rad-session", ResumeID: id, Mode: "default", DiagPath: diag})
	command, _ := replaytest.Diag(t, diag)
	if !strings.Contains(command, `"--resume" "`+id+`"`) || strings.Contains(command, "--session-id") {
		t.Errorf("command = %s, want --resume %s", command, id)
	}
	turn := replaytest.RunTurn(t, rt, "Reply with just OK.", nil)
	if turn.End.Status != model.TurnCompleted || rt.NativeID() != id {
		t.Errorf("end = %+v, native id %q", turn.End, rt.NativeID())
	}
	replaytest.CloseClean(t, rt)
}
