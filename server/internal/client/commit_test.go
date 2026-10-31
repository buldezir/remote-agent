package client

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"remote-agent/internal/model"
)

func TestCommitScopes(t *testing.T) {
	full := CommitPreview{Branch: "ra/x", Remote: "origin", Files: []FileStat{{Path: "a"}}}
	clean := CommitPreview{Branch: "ra/x", Remote: "origin", Files: []FileStat{}}
	local := CommitPreview{Branch: "ra/x", Files: []FileStat{{Path: "a"}}}
	detached := CommitPreview{Remote: "origin", Files: []FileStat{{Path: "a"}}}

	if CommitOnly.Unavailable(full) != "" || CommitOnly.Unavailable(clean) == "" || CommitAndPush.Unavailable(clean) != "" ||
		CommitPushAndPR.Unavailable(local) != "No remote to push to." || CommitAndPush.Unavailable(detached) == "" {
		t.Fatal("availability")
	}
	for s, want := range map[CommitScope][2]string{
		CommitOnly:      {"Commit", "Commit"},
		CommitAndPush:   {"Commit & Push", "Push"},
		CommitPushAndPR: {"Commit, Push & Open PR", "Push & Open PR"},
	} {
		if s.Action(true) != want[0] || s.Action(false) != want[1] {
			t.Errorf("%v: %q %q", s, s.Action(true), s.Action(false))
		}
	}
	// The last choice, where it can go ahead; else push with a remote, else commit.
	for _, c := range []struct {
		saved string
		p     CommitPreview
		want  CommitScope
	}{
		{"", full, CommitAndPush}, {"pullRequest", full, CommitPushAndPR}, {"commit", full, CommitOnly},
		{"commit", clean, CommitAndPush}, {"pullRequest", local, CommitOnly}, {"nonsense", local, CommitOnly},
	} {
		if got := DefaultCommitScope(c.saved, c.p); got != c.want {
			t.Errorf("default for %q with %+v: %v, want %v", c.saved, c.p, got, c.want)
		}
	}
	if p := CommitPushAndPR.Params("s1", "  Fix it \n"); p != (CommitParams{SessionID: "s1", Message: "Fix it", Push: true, PullRequest: true}) {
		t.Errorf("params %+v", p)
	}
	if p := CommitOnly.Params("s1", ""); p.Push || p.PullRequest {
		t.Errorf("commit only %+v", p)
	}
}

// gitEnv keeps git from the developer's config, and puts a gh first on
// PATH that answers as GitHub would. It logs its calls to the file it
// returns.
func gitEnv(t *testing.T) string {
	t.Helper()
	for k, v := range map[string]string{"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "test", "GIT_AUTHOR_EMAIL": "test@example.com", "GIT_COMMITTER_NAME": "test", "GIT_COMMITTER_EMAIL": "test@example.com"} {
		t.Setenv(k, v)
	}
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "gh.log")
	gh := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\ncase \"$1 $2\" in\n\"pr list\") echo '[]' ;;\n" +
		"\"pr create\") cat >/dev/null; echo https://github.com/test/repo/pull/12 ;;\n*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(gh), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// gitRepo makes dir a repository with a commit on main, whose origin is a
// bare repository it returns.
func gitRepo(t *testing.T, dir string) string {
	t.Helper()
	origin := filepath.Join(t.TempDir(), "origin.git")
	for _, args := range [][]string{{"init", "-q", "-b", "main", dir}, {"-C", dir, "commit", "-q", "--allow-empty", "-m", "init"},
		{"init", "-q", "--bare", "-b", "main", origin}, {"-C", dir, "remote", "add", "origin", origin}, {"-C", dir, "push", "-q", "origin", "main"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return origin
}

func TestRadCommitsPushesAndOpensAPullRequest(t *testing.T) {
	ghLog := gitEnv(t)
	r := startRad(t)
	origin := gitRepo(t, r.Root)
	c, q, _ := r.connect(t)
	if !c.Info.Commit {
		t.Fatalf("info %+v", c.Info)
	}
	p := r.project(t, c, q)
	var s model.Session
	if err := run(t, q, func() (err error) {
		s, err = c.CreateSession(context.Background(), NewSession{ProjectID: p.ID, Harness: "fake",
			Workspace: NewWorkspace{Kind: model.WorkspaceWorktree, Branch: "ra/notes"}})
		return
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Workspace.Path, "notes.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var preview CommitPreview
	if err := run(t, q, func() (err error) { preview, err = c.CommitPreview(context.Background(), s.ID); return }); err != nil {
		t.Fatal(err)
	}
	if preview.Branch != "ra/notes" || preview.Remote != "origin" || preview.Base != "main" || len(preview.Files) != 1 ||
		preview.Files[0].Status != "added" || preview.Message == "" {
		t.Fatalf("preview %+v", preview)
	}

	var task model.Task
	if err := run(t, q, func() (err error) {
		task, err = c.Commit(context.Background(), CommitPushAndPR.Params(s.ID, "Add the notes"))
		return
	}); err != nil {
		t.Fatal(err)
	}
	if task.Kind != model.TaskCommit || task.SessionID != s.ID {
		t.Fatalf("task %+v", task)
	}
	tasks := NewServerTasks(q, c)
	defer tasks.Stop()
	prog := tasks.Follow(task)
	eventually(t, q, "the commit to end", func() bool { return !prog.IsActive() })
	if prog.Phase.Kind != TaskPhaseDone || len(prog.Task.Steps) != 3 || prog.Task.URL != "https://github.com/test/repo/pull/12" {
		t.Fatalf("ended %+v %+v", prog.Phase, prog.Task)
	}
	if out, err := exec.Command("git", "-C", origin, "log", "-1", "--format=%s", "ra/notes").CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "Add the notes" {
		t.Fatalf("origin's ra/notes: %s %v", out, err)
	}
	if b, _ := os.ReadFile(ghLog); !strings.Contains(string(b), "pr create --head ra/notes") {
		t.Fatalf("gh calls %s", b)
	}

	// The session's tasks list it; the server's don't.
	var mine, server []model.Task
	run(t, q, func() (err error) { mine, err = c.SessionTasks(context.Background(), s.ID); return })
	run(t, q, func() (err error) { server, err = c.Tasks(context.Background()); return })
	if len(mine) != 1 || mine[0].ID != task.ID || len(server) != 0 {
		t.Fatalf("session's tasks %+v, server's %+v", mine, server)
	}
}
