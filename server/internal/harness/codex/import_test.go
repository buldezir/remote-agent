package codex

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
	importThread = "019a3e10-7f3a-7c22-9d1e-5b4a3c2d1e0f"
	olderThread  = "0199f000-1111-7222-8333-944455556666"
)

// importHarness is Codex with testdata/import as its home, as CODEX_HOME in
// the harness's config sets it.
func importHarness(t *testing.T) *Harness {
	t.Helper()
	dir := nativetest.CopyTree(t, "testdata/import", map[string]time.Duration{
		"rollout-2026-10-30T10-00-00-" + importThread + ".jsonl": 20 * time.Second,
		"rollout-2026-10-30T09-00-00-" + olderThread + ".jsonl":  3 * time.Hour,
	})
	return New(config.Command{Env: map[string]string{"CODEX_HOME": dir}})
}

func TestListNative(t *testing.T) {
	h := importHarness(t)
	list, err := h.ListNative(context.Background(), "/work/app/", nil)
	if err != nil {
		t.Fatal(err)
	}
	// rad's thread, another folder's, a sub-agent's and the one without a
	// reply are left out.
	var got []string
	for _, s := range list {
		got = append(got, s.ID+" "+s.Title)
	}
	if !slices.Equal(got, []string{importThread + " Login fixes", olderThread + " Changelog"}) {
		t.Fatalf("listed %q", got)
	}
	if list[0].Messages != 7 || list[1].Messages != 2 || time.Since(list[0].UpdatedAt) > time.Minute {
		t.Errorf("listed %+v", list)
	}
	if list, _ := h.ListNative(context.Background(), "/work/app", map[string]bool{importThread: true}); len(list) != 1 {
		t.Errorf("without the known one: %+v", list)
	}
	if list, _ := h.ListNative(context.Background(), "/work/other", nil); len(list) != 1 || list[0].Title != "Elsewhere" {
		t.Errorf("the other folder's: %+v", list)
	}
}

func TestReadNative(t *testing.T) {
	h := importHarness(t)
	tr, err := h.ReadNative(context.Background(), "/work/app", importThread, images.New(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Title != "Login fixes" || tr.Model != "gpt-6-luna" {
		t.Errorf("title %q, model %q", tr.Title, tr.Model)
	}
	type want struct {
		status    model.TurnStatus
		forkPoint string
		items     []string
	}
	wants := []want{
		{model.TurnCompleted, "019a3e10-aaaa-7000-8000-000000000001", []string{"user_message: Fix the flaky login test",
			"reasoning: **Looking at the test**", "tool_call: go test ./...", "tool_call: Edit login_test.go", "plan: ",
			"assistant_message: The test now waits for the server."}},
		// A prompt in the same turn was steered into it.
		{model.TurnInterrupted, "019a3e10-aaaa-7000-8000-000000000002", []string{"user_message: Now refactor the session store",
			"user_message: Keep the old API", "assistant_message: Keeping it."}},
		{model.TurnCompleted, "019a3e10-aaaa-7000-8000-000000000003", []string{"user_message: What does this show?",
			"tool_call: Search go test flaky timers", "tool_call: View shot.png", "assistant_message: The login form."}},
		{model.TurnCompleted, "", []string{"notice: Context compacted"}},
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
		if tr.Turns[i].Status != w.status || tr.Turns[i].ForkPoint != w.forkPoint || !slices.Equal(got, w.items) {
			t.Errorf("turn %d = %s %q %q, want %s %q %q", i+1, tr.Turns[i].Status, tr.Turns[i].ForkPoint, got, w.status, w.forkPoint, w.items)
		}
	}
	first := tr.Turns[0].Items
	if test := first[2]; test.Status != model.ItemFailed || test.Tool.ExitCode == nil || *test.Tool.ExitCode != 1 ||
		!strings.Contains(test.Tool.Output, "FAIL") || test.Tool.Kind != model.ToolExecute {
		t.Errorf("test = %+v %+v", test, test.Tool)
	}
	if patch := first[3]; patch.Tool.Kind != model.ToolEdit || !strings.Contains(patch.Tool.Output, "+waitForServer(t)") ||
		!slices.Equal(patch.Tool.Paths, []string{"login_test.go"}) {
		t.Errorf("patch = %+v", patch.Tool)
	}
	if plan := first[4].Plan; plan == nil || len(plan.Entries) != 2 || plan.Entries[0].Content != "Wait for the server" {
		t.Errorf("plan = %+v", plan)
	}
	third := tr.Turns[2].Items
	if len(third[0].Images) != 1 || len(third[2].Images) != 1 {
		t.Errorf("images: prompt %+v, view %+v", third[0].Images, third[2].Images)
	}

	// Older versions: no turn ids or kinds, and output wrapped in JSON.
	tr, err = h.ReadNative(context.Background(), "/work/app", olderThread, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Turns) != 1 || tr.Title != "Changelog" {
		t.Fatalf("older: %+v", tr)
	}
	items := tr.Turns[0].Items
	if len(items) != 3 || items[0].Text != "Add a changelog" || items[1].Tool.Title != "ls" || items[1].Tool.Output != "README.md\n" ||
		*items[1].Tool.ExitCode != 0 || items[1].Status != model.ItemCompleted || items[2].Text != "Added CHANGELOG.md." {
		t.Errorf("older items = %+v", items)
	}
}

func TestReadNativeMissing(t *testing.T) {
	h := importHarness(t)
	for _, id := range []string{"019a3f00-0000-7000-8000-00000000ffff", "../x", ""} {
		if _, err := h.ReadNative(context.Background(), "/work/app", id, nil); !errors.Is(err, harness.ErrNotFound) {
			t.Errorf("ReadNative(%q) = %v", id, err)
		}
	}
	// It ran in another folder.
	if _, err := h.ReadNative(context.Background(), "/work/other", importThread, nil); !errors.Is(err, harness.ErrNotFound) {
		t.Errorf("from another folder: %v", err)
	}
}

// TestReplayResume replays a thread picked up from the terminal: Codex
// resumes it with thread/resume, and keeps its id. The transcript is
// fork.ndjson's, with thread/resume in place of thread/fork.
func TestReplayResume(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "codex-resume/ws")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	exe, env := replaytest.Command(t, "testdata/resume.ndjson", root)
	diag := filepath.Join(t.TempDir(), "diag.ndjson")
	const id = "01a18f38-e752-7043-a080-f16a53d65767"
	rt, err := New(config.Command{Command: exe, Env: env}).Open(context.Background(), harness.OpenOptions{SessionID: "local", Cwd: cwd,
		Mode: "auto", Model: "gpt-6-luna", Effort: "low", ResumeID: id, DiagPath: diag})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, sent := replaytest.Diag(t, diag)
	i := slices.IndexFunc(sent, func(f map[string]any) bool { return f["method"] == "thread/resume" })
	if i < 0 {
		t.Fatalf("sent %v, want thread/resume", sent)
	}
	if params := sent[i]["params"].(map[string]any); params["threadId"] != id || params["lastTurnId"] != nil || params["cwd"] != cwd {
		t.Errorf("thread/resume params = %v", params)
	}
	turn := replaytest.RunTurn(t, rt, "Without using any tools: list every request I have made to you in this conversation, in one short sentence.", nil)
	if turn.End.Status != model.TurnCompleted || rt.NativeID() != id {
		t.Errorf("end = %+v, native id %q", turn.End, rt.NativeID())
	}
	replaytest.CloseClean(t, rt)
}
