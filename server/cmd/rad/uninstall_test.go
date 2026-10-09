package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-agent/internal/config"
	"remote-agent/internal/harness"
	"remote-agent/internal/harness/fake"
	"remote-agent/internal/model"
	"remote-agent/internal/orchestrator"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestUninstall(t *testing.T) {
	tmp := t.TempDir()
	// Keep the real service, config and logs out of reach.
	t.Setenv("HOME", filepath.Join(tmp, "home"))
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	radHome := filepath.Join(tmp, "radhome")
	t.Setenv("RAD_HOME", radHome)
	os.MkdirAll(radHome, 0o700)
	os.WriteFile(filepath.Join(radHome, "config.toml"), []byte("port = 1\n"), 0o600)

	repo := filepath.Join(tmp, "proj")
	os.MkdirAll(repo, 0o755)
	git(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644)
	git(t, repo, "add", "-A")
	git(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init")

	// A worktree session with one finished turn, so there are checkpoints.
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	st, err := openStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	o := orchestrator.New(st, harness.NewRegistry(fake.Harness{}), orchestrator.Options{
		WorktreesDir: cfg.WorktreesDir(), LogsDir: cfg.LogsDir(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ctx := context.Background()
	proj, err := o.AddProject(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	s, err := o.CreateSession(ctx, orchestrator.CreateSessionParams{
		ProjectID: proj.ID, Harness: "fake", Prompt: "hi",
		Workspace: orchestrator.WorkspaceParams{Kind: model.WorkspaceWorktree},
	})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		turns, _ := st.Turns(ctx, s.ID)
		if len(turns) == 1 && turns[0].Status == model.TurnCompleted && turns[0].CheckpointAfter != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("turn did not finish")
		}
	}
	o.Shutdown()
	st.Close()
	if git(t, repo, "for-each-ref", "refs/ra/") == "" {
		t.Fatal("no checkpoints to remove")
	}
	os.WriteFile(filepath.Join(s.Workspace.Path, "wip.txt"), []byte("wip\n"), 0o644)

	r, err := findInstall(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.worktrees) != 1 || !r.worktrees[0].dirty || len(r.repos) != 1 || r.running() != "" {
		t.Fatalf("found %+v", r)
	}

	if err := uninstall([]string{"--yes"}); err != nil {
		t.Fatal(err)
	}
	if exists(radHome) {
		t.Errorf("RAD_HOME still exists")
	}
	if refs := git(t, repo, "for-each-ref", "refs/ra/"); refs != "" {
		t.Errorf("refs left: %s", refs)
	}
	if wts := git(t, repo, "worktree", "list", "--porcelain"); strings.Count(wts, "worktree ") != 1 {
		t.Errorf("worktrees left:\n%s", wts)
	}
	if git(t, repo, "branch", "--list", s.Workspace.Branch) == "" {
		t.Errorf("session branch %s was deleted", s.Workspace.Branch)
	}
	if err := uninstall([]string{"--yes"}); err != nil {
		t.Fatal(err)
	}
}

func TestSystemdUnitFile(t *testing.T) {
	got := systemdUnitFile("/opt/my rad/rad$1", [][2]string{{"PATH", `/usr/bin:/home/u/100%/bin`}, {"RAD_HOME", `/srv/"rad"`}})
	for _, want := range []string{
		`ExecStart="/opt/my rad/rad$$1" serve`,
		`Environment="PATH=/usr/bin:/home/u/100%%/bin"`,
		`Environment="RAD_HOME=/srv/\"rad\""`,
		"Restart=always",
		"WantedBy=default.target",
	} {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

func TestLaunchAgentPlist(t *testing.T) {
	got := launchAgentPlist("/Users/u/bin/rad", "/Users/u", "/Users/u/Library/Logs/rad.log", [][2]string{{"PATH", "/bin"}, {"RAD_HOME", "/tmp/a&b"}})
	for _, want := range []string{
		"<key>PATH</key><string>/bin</string>",
		"<key>RAD_HOME</key><string>/tmp/a&amp;b</string>",
		"<key>HOME</key><string>/Users/u</string>",
		"<array><string>/Users/u/bin/rad</string><string>serve</string></array>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}
