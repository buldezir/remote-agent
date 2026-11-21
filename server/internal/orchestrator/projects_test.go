package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"remote-agent/internal/model"
	"remote-agent/internal/store"
)

// A project added before `git init` becomes a git project: its open
// session's next turn checkpoints, and the clients hear of it.
func TestProjectBecomesGitRepo(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	s := e.create(CreateSessionParams{Mode: "auto", Prompt: "write one.txt"})
	if turn := e.waitTurn(s.ID, 1); turn.CheckpointBefore != "" {
		t.Fatalf("checkpoint in a plain folder: %+v", turn)
	}
	_, seq, err := e.st.Changes(bg, model.IndexStream, 0)
	if err != nil {
		t.Fatal(err)
	}

	run(t, e.dir, "git", "init", "-q", "-b", "main")
	run(t, e.dir, "git", "add", "-A")
	run(t, e.dir, "git", "commit", "-q", "-m", "init")

	e.prompt(s.ID, "write two.txt", "")
	turn := e.waitTurn(s.ID, 2)
	if turn.CheckpointBefore == "" || turn.CheckpointAfter == "" {
		t.Errorf("turn after git init has no checkpoints: %+v", turn)
	}
	if b := e.session(s.ID).Workspace.Branch; b != "main" {
		t.Errorf("branch = %q", b)
	}
	d, err := e.o.SessionDiff(bg, s.ID, nil)
	if err != nil || len(d.Files) != 1 || d.Files[0].Path != "two.txt" {
		t.Errorf("session diff = %+v, %v", d, err)
	}
	if p, _ := e.st.Project(bg, e.proj.ID); !p.IsGitRepo {
		t.Error("project not saved as a git repository")
	}
	evs, _, err := e.st.Changes(bg, model.IndexStream, seq)
	if err != nil {
		t.Fatal(err)
	}
	upserted := 0
	for _, ev := range evs {
		if ev.Type == model.EvProjectUpserted && ev.Project.IsGitRepo {
			upserted++
		}
	}
	if upserted != 1 {
		t.Errorf("project.upserted with isGitRepo: %d, want 1", upserted)
	}
}

func TestRefreshProjects(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	other := filepath.Join(t.TempDir(), "other")
	os.Mkdir(other, 0o755)
	po, err := e.o.AddProject(bg, other)
	if err != nil {
		t.Fatal(err)
	}

	run(t, e.dir, "git", "init", "-q", "-b", "main")
	e.o.RefreshProjects(bg)
	if p, _ := e.st.Project(bg, e.proj.ID); !p.IsGitRepo {
		t.Error("git init project not refreshed")
	}
	if p, _ := e.st.Project(bg, po.ID); p.IsGitRepo {
		t.Error("plain folder taken for a repository")
	}
	if p, err := e.o.AddProject(bg, e.dir); err != nil || !p.IsGitRepo {
		t.Errorf("adding it again = %+v, %v", p, err)
	}

	// A check that lands after project.remove doesn't bring it back.
	if err := e.o.RemoveProject(bg, po.ID); err != nil {
		t.Fatal(err)
	}
	run(t, other, "git", "init", "-q")
	if !e.o.RefreshGit(bg, po) {
		t.Error("RefreshGit = false for a repository")
	}
	if _, err := e.st.Project(bg, po.ID); err != store.ErrNotFound {
		t.Errorf("removed project: %v", err)
	}
}
