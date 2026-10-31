package gitx

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestUncommitted(t *testing.T) {
	dir := newRepo(t)
	// A branch without commits: everything is new.
	writeFile(t, dir, "a.txt", "one\n")
	files, patch, truncated, err := Uncommitted(ctx, dir, 0)
	if err != nil || len(files) != 1 || files[0].Status != Added || !strings.Contains(patch, "+one") || truncated {
		t.Fatalf("unborn: %+v %q %v %v", files, patch, truncated, err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "init")

	writeFile(t, dir, "a.txt", "two\n")
	writeFile(t, dir, "new/b.txt", strings.Repeat("line\n", 100))
	git(t, dir, "add", "a.txt") // the user's index, which stays as it is
	files, patch, truncated, err = Uncommitted(ctx, dir, 200)
	if err != nil || len(files) != 2 || files[0].Path != "a.txt" || files[0].Status != Modified || files[0].Additions != 1 ||
		files[1].Path != "new/b.txt" || files[1].Additions != 100 || !truncated || len(patch) > 200 {
		t.Fatalf("changes: %+v %d %v %v", files, len(patch), truncated, err)
	}
	if staged := git(t, dir, "diff", "--cached", "--name-only"); staged != "a.txt" {
		t.Fatalf("the index has %q", staged)
	}
}

func TestRemotes(t *testing.T) {
	origin := filepath.Join(t.TempDir(), "origin.git")
	git(t, t.TempDir(), "init", "-q", "--bare", "-b", "main", origin)
	dir := newRepo(t)
	if r := Remotes(ctx, dir); len(r) != 0 {
		t.Fatalf("remotes %q", r)
	}
	writeFile(t, dir, "a.txt", "one\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "first")
	git(t, dir, "remote", "add", "origin", origin)
	git(t, dir, "push", "-q", "-u", "origin", "main")
	git(t, dir, "remote", "set-head", "origin", "main")

	if r := Remotes(ctx, dir); len(r) != 1 || r[0] != "origin" {
		t.Fatalf("remotes %q", r)
	}
	if r, m := Upstream(ctx, dir, "main"); r != "origin" || m != "main" {
		t.Fatalf("upstream %q %q", r, m)
	}
	if r, m := Upstream(ctx, dir, "nope"); r != "" || m != "" {
		t.Fatalf("no upstream %q %q", r, m)
	}
	if b := RemoteDefaultBranch(ctx, dir, "origin"); b != "main" {
		t.Fatalf("default branch %q", b)
	}
	if !RemoteBranchExists(ctx, dir, "origin", "main") || RemoteBranchExists(ctx, dir, "origin", "x") {
		t.Fatal("remote branches")
	}

	git(t, dir, "switch", "-q", "-c", "feature")
	writeFile(t, dir, "b.txt", "two\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "second", "-m", "With a body.")
	if n := CountCommits(ctx, dir, "origin/main..HEAD"); n != 1 {
		t.Fatalf("ahead %d", n)
	}
	log, err := Log(ctx, dir, "main..HEAD", 10)
	if err != nil || len(log) != 1 || log[0].Subject != "second" || log[0].Body != "With a body." || len(log[0].SHA) != 40 {
		t.Fatalf("log %+v %v", log, err)
	}
	if mb, err := MergeBase(ctx, dir, "origin/main", "HEAD"); err != nil || mb != git(t, dir, "rev-parse", "main") {
		t.Fatalf("merge base %q %v", mb, err)
	}
}
