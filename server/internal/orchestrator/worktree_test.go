package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"remote-agent/internal/gitx"
	"remote-agent/internal/model"
)

func TestSessionsInExistingWorktreesAndBranches(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	worktree := func(p WorkspaceParams) (*model.Session, error) {
		p.Kind = model.WorkspaceWorktree
		return e.o.CreateSession(bg, CreateSessionParams{ProjectID: e.proj.ID, Harness: "fake", Mode: "auto", Workspace: p})
	}
	exists := func(path string) bool { _, err := os.Stat(path); return err == nil }

	first := e.create(CreateSessionParams{Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree, Branch: "ra/one"}})

	// A second session works in the first one's worktree, on its branch.
	second, err := worktree(WorkspaceParams{Path: first.Workspace.Path})
	if err != nil {
		t.Fatal(err)
	}
	if ws := second.Workspace; ws.Path != first.Workspace.Path || ws.Branch != "ra/one" || ws.BaseRef != "" {
		t.Errorf("workspace = %+v", ws)
	}
	e.prompt(second.ID, "write shared.txt", "")
	e.waitTurn(second.ID, 1)
	if !exists(filepath.Join(first.Workspace.Path, "shared.txt")) {
		t.Error("the second session didn't write in the shared worktree")
	}

	// A worktree made by hand (or by an agent) can be picked too.
	hand := filepath.Join(t.TempDir(), "hand")
	run(t, e.dir, "git", "worktree", "add", "-q", "-b", "hand", hand)
	byHand, err := worktree(WorkspaceParams{Path: hand})
	if err != nil {
		t.Fatal(err)
	}
	if byHand.Workspace.Branch != "hand" {
		t.Errorf("branch = %q", byHand.Workspace.Branch)
	}

	// Only the project's worktrees, besides its own folder, and without a branch to make.
	for name, p := range map[string]WorkspaceParams{
		"project folder": {Path: e.dir},
		"elsewhere":      {Path: t.TempDir()},
		"with a branch":  {Path: hand, Branch: "x"},
		"with a base":    {Path: hand, BaseRef: "main"},
	} {
		if _, err := worktree(p); CodeOf(err) != CodeInvalid {
			t.Errorf("%s: err = %v", name, err)
		}
	}

	// An existing branch is checked out into a new worktree, not made again.
	run(t, e.dir, "git", "branch", "existing")
	onBranch, err := worktree(WorkspaceParams{Branch: "existing"})
	if err != nil {
		t.Fatal(err)
	}
	if ws := onBranch.Workspace; ws.Branch != "existing" || gitx.CurrentBranch(bg, ws.Path) != "existing" {
		t.Errorf("workspace = %+v", ws)
	}
	if _, err := worktree(WorkspaceParams{Branch: "main", BaseRef: "main"}); CodeOf(err) != CodeInvalid {
		t.Errorf("an existing branch with a base: err = %v", err)
	}
	if _, err := worktree(WorkspaceParams{Branch: "ra/one"}); CodeOf(err) != CodeInvalid {
		t.Errorf("a branch checked out elsewhere: err = %v", err)
	}

	// git.branches' worktrees: every one but the project folder.
	wts, err := gitx.Worktrees(bg, e.dir)
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, wt := range wts {
		if !wt.Main {
			listed = append(listed, wt.Branch)
		}
	}
	if len(wts) != 4 || !wts[0].Main || len(listed) != 3 {
		t.Errorf("worktrees = %+v", wts)
	}

	// Removing a worktree leaves one another session works in, and one rad didn't make.
	if err := e.o.Archive(bg, second.ID, true); err != nil {
		t.Fatal(err)
	}
	if !exists(first.Workspace.Path) {
		t.Error("archiving removed a worktree another session works in")
	}
	if err := e.o.Archive(bg, byHand.ID, true); err != nil {
		t.Fatal(err)
	}
	if !exists(hand) {
		t.Error("archiving removed a worktree made by hand")
	}
	if err := e.o.Archive(bg, first.ID, true); err != nil {
		t.Fatal(err)
	}
	if exists(first.Workspace.Path) {
		t.Error("the last session's worktree wasn't removed")
	}
}

func TestWorktreeFolders(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	s := e.create(CreateSessionParams{Workspace: WorkspaceParams{Kind: model.WorkspaceWorktree}})
	name := slug(e.proj.Name) + "-"
	if want := filepath.Join(e.o.opt.WorktreesDir, name+branchSuffix(s.ID)); s.Workspace.Path != want {
		t.Errorf("path = %s, want %s", s.Workspace.Path, want)
	}

	// A taken folder makes the name longer.
	id := "019a8b2c-3d4e-7f00-8a1b-2c3d4e5f6a7b"
	for _, want := range []string{name + "5f6a7b", name + "2c3d4e5f6a7b", name + "019a8b2c3d4e7f008a1b2c3d4e5f6a7b"} {
		dir := e.o.worktreeDir(e.proj, id)
		if got := filepath.Base(dir); got != want {
			t.Fatalf("folder = %s, want %s", got, want)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWorktreeRefusalsLeaveNothing(t *testing.T) {
	t.Parallel()
	e := newEnv(t, true)
	create := func(p CreateSessionParams) error {
		p.ProjectID, p.Harness, p.Mode = e.proj.ID, "fake", "auto"
		p.Workspace.Kind = model.WorkspaceWorktree
		_, err := e.o.CreateSession(bg, p)
		return err
	}
	branches := func() string { return run(t, e.dir, "git", "branch", "--format=%(refname:short)") }
	before := branches()

	// git would read these as options.
	for name, wp := range map[string]WorkspaceParams{
		"option as base":   {BaseRef: "--no-checkout"},
		"option as branch": {Branch: "-x"},
		"unknown base":     {BaseRef: "no-such-ref"},
	} {
		if err := create(CreateSessionParams{Workspace: wp}); CodeOf(err) != CodeInvalid {
			t.Errorf("%s: err = %v", name, err)
		}
	}

	// A session that fails after its worktree was made takes it back, and
	// the branch made for it.
	for _, branch := range []string{"", "ra/undone"} {
		if err := create(CreateSessionParams{Workspace: WorkspaceParams{Branch: branch}, Prompt: "hi", StartedBy: "no-such-session"}); CodeOf(err) != CodeNotFound {
			t.Fatalf("err = %v", err)
		}
	}
	// And an existing branch it checked out stays.
	run(t, e.dir, "git", "branch", "kept")
	if err := create(CreateSessionParams{Workspace: WorkspaceParams{Branch: "kept"}, StartedBy: "no-such-session"}); CodeOf(err) != CodeNotFound {
		t.Fatalf("err = %v", err)
	}
	run(t, e.dir, "git", "branch", "-D", "kept")

	if after := branches(); after != before {
		t.Errorf("branches = %q, want %q", after, before)
	}
	if entries, _ := os.ReadDir(e.o.opt.WorktreesDir); len(entries) != 0 {
		t.Errorf("worktrees folder has %d entries", len(entries))
	}
	if out := run(t, e.dir, "git", "worktree", "list", "--porcelain"); strings.Count(out, "worktree ") != 1 {
		t.Errorf("worktrees:\n%s", out)
	}
}
