package pi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	importSession = "2c7d1e4a-8b3f-4a6d-9e2c-5f1a7b3d9c08"
	olderSession  = "5e9b0c3d-1f2a-4b6c-8d7e-0a1b2c3d4e5f"
)

// importHarness is Pi with testdata/import as its agent folder, as
// PI_CODING_AGENT_DIR in the harness's config sets it.
func importHarness(t *testing.T) *Harness {
	t.Helper()
	dir := nativetest.CopyTree(t, "testdata/import", map[string]time.Duration{
		"2026-10-30T11-00-00-000Z_" + importSession + ".jsonl": 5 * time.Minute,
		"2026-10-29T08-00-00-000Z_" + olderSession + ".jsonl":  30 * time.Hour,
	})
	return New(config.Command{Env: map[string]string{"PI_CODING_AGENT_DIR": dir}})
}

func TestListNative(t *testing.T) {
	h := importHarness(t)
	list, err := h.ListNative(context.Background(), "/work/app", nil)
	if err != nil {
		t.Fatal(err)
	}
	// The session without a reply is left out. A listing counts the
	// messages of every branch.
	var got []string
	for _, s := range list {
		got = append(got, s.ID+" "+s.Title)
	}
	if !slices.Equal(got, []string{importSession + " Login fixes", olderSession + " Add a changelog"}) {
		t.Fatalf("listed %q", got)
	}
	if list[0].Messages != 8 || list[1].Messages != 2 {
		t.Errorf("listed %+v", list)
	}
	if list, _ := h.ListNative(context.Background(), "/work/app", map[string]bool{importSession: true, olderSession: true}); len(list) != 0 {
		t.Errorf("without the known ones: %+v", list)
	}
}

func TestReadNative(t *testing.T) {
	h := importHarness(t)
	tr, err := h.ReadNative(context.Background(), "/work/app", importSession, images.New(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Title != "Login fixes" || tr.Model != "anthropic/claude-sonnet-5-5" {
		t.Errorf("title %q, model %q", tr.Title, tr.Model)
	}
	wants := []struct {
		status model.TurnStatus
		items  []string
	}{
		{model.TurnCompleted, []string{"user_message: Fix the flaky login test", "reasoning: Run the tests first.",
			"assistant_message: Let me run the tests.", "tool_call: go test ./...", "tool_call: Edit login_test.go",
			"assistant_message: Fixed: it waits for the server now."}},
		// The branch the user left ("Try another way") is left out.
		{model.TurnInterrupted, []string{"user_message: Now run the linter", "assistant_message: Running"}},
		// The user's own command, and a compaction, between turns.
		{model.TurnCompleted, []string{"tool_call: ls", "notice: Context compacted"}},
		{model.TurnFailed, []string{"user_message: Thanks", "error: Overloaded"}},
	}
	if len(tr.Turns) != len(wants) {
		t.Fatalf("%d turns: %+v", len(tr.Turns), tr.Turns)
	}
	for i, w := range wants {
		var got []string
		for _, it := range tr.Turns[i].Items {
			text := it.Text
			if it.Tool != nil {
				text = it.Tool.Title
			}
			got = append(got, string(it.Kind)+": "+text)
		}
		if tr.Turns[i].Status != w.status || !slices.Equal(got, w.items) {
			t.Errorf("turn %d = %s %q, want %s %q", i+1, tr.Turns[i].Status, got, w.status, w.items)
		}
	}
	first := tr.Turns[0].Items
	if bash := first[3]; bash.Status != model.ItemFailed || !strings.Contains(bash.Tool.Output, "FAIL") {
		t.Errorf("bash = %+v %+v", bash, bash.Tool)
	}
	if edit := first[4]; edit.Status != model.ItemCompleted || !slices.Equal(edit.Tool.Paths, []string{"login_test.go"}) {
		t.Errorf("edit = %+v %+v", edit, edit.Tool)
	}
	if prompt := tr.Turns[1].Items[0]; len(prompt.Images) != 1 {
		t.Errorf("prompt's images = %+v", prompt.Images)
	}
	if ls := tr.Turns[2].Items[0]; ls.Tool.Output != "README.md\n" || ls.Tool.ExitCode == nil || *ls.Tool.ExitCode != 0 {
		t.Errorf("ls = %+v", ls.Tool)
	}
	if !first[0].CreatedAt.Equal(time.Date(2026, 10, 30, 11, 0, 1, 0, time.UTC)) {
		t.Errorf("prompt's time = %v", first[0].CreatedAt)
	}

	// Sessions from before Pi kept a tree are a list.
	tr, err = h.ReadNative(context.Background(), "/work/app", olderSession, nil)
	if err != nil || len(tr.Turns) != 1 || len(tr.Turns[0].Items) != 2 || tr.Turns[0].Items[1].Text != "Added CHANGELOG.md." {
		t.Errorf("older = %+v, %v", tr, err)
	}
}

func TestReadNativeMissing(t *testing.T) {
	h := importHarness(t)
	for _, id := range []string{"0d0e0f10-0000-4000-8000-000000000000", "../x", ""} {
		if _, err := h.ReadNative(context.Background(), "/work/app", id, nil); !errors.Is(err, harness.ErrNotFound) {
			t.Errorf("ReadNative(%q) = %v", id, err)
		}
	}
	if _, err := h.ReadNative(context.Background(), "/work/other", importSession, nil); !errors.Is(err, harness.ErrNotFound) {
		t.Errorf("from another folder: %v", err)
	}
}

// TestResume opens a session picked up from the terminal: Pi resumes it by
// its id with --session-id, as it does rad's own.
func TestResume(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "pi-rec/ws")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	exe, env := replaytest.Command(t, "testdata/error.ndjson", root)
	diag := filepath.Join(t.TempDir(), "diag.ndjson")
	const id = "01a12103-19d8-7d07-9266-9ddeb87a39ce"
	rt, err := New(config.Command{Command: exe, Env: env}).Open(context.Background(),
		harness.OpenOptions{SessionID: "rad-session", ResumeID: id, Cwd: cwd, DiagPath: diag})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if command, _ := replaytest.Diag(t, diag); !strings.Contains(command, `"--session-id" "`+id+`"`) {
		t.Errorf("command = %s, want --session-id %s", command, id)
	}
	if rt.NativeID() != id {
		t.Errorf("NativeID() = %q", rt.NativeID())
	}
	replaytest.RunTurn(t, rt, "Create hello.txt containing the word hi using the write tool, then show it with `cat hello.txt` using bash. Then reply with one short sentence.",
		func(harness.ApprovalEvent) string { return "" })
	replaytest.CloseClean(t, rt)
}
