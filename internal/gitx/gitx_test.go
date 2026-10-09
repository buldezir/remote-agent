package gitx

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	// Isolate from the developer's git config (global ignores, signing, hooks).
	os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		os.Setenv(k, "test")
	}
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		os.Setenv(k, "test@example.com")
	}
	os.Exit(m.Run())
}

var ctx = context.Background()

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	return dir
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(dir, rel string) bool {
	_, err := os.Stat(filepath.Join(dir, rel))
	return err == nil
}

func commitAll(t *testing.T, dir, msg string) string {
	t.Helper()
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", msg)
	return git(t, dir, "rev-parse", "HEAD")
}

func checkpoint(t *testing.T, dir, name string) string {
	t.Helper()
	sha, err := Checkpoint(ctx, dir, "refs/ra/cp/test/"+name, "checkpoint "+name)
	if err != nil {
		t.Fatalf("checkpoint %s: %v", name, err)
	}
	return sha
}

func treeFiles(t *testing.T, dir, rev string) []string {
	t.Helper()
	out := git(t, dir, "ls-tree", "-r", "--name-only", rev)
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func lines(n int, prefix string) string {
	var b strings.Builder
	for i := range n {
		b.WriteString(prefix)
		b.WriteString(strings.Repeat("x", i%7))
		b.WriteString("\n")
	}
	return b.String()
}

// repoState captures what a checkpoint must not change.
type repoState struct {
	status, head, staged string
	index                []byte
}

func stateOf(t *testing.T, dir string) repoState {
	t.Helper()
	// Status first: it may refresh the index's stat cache.
	s := repoState{status: git(t, dir, "status", "--porcelain=v1", "-uall")}
	s.head, _ = Head(ctx, dir)
	s.staged = git(t, dir, "diff", "--cached", "--name-status")
	s.index, _ = os.ReadFile(filepath.Join(dir, ".git", "index"))
	return s
}

func TestCheckpointLeavesIndexAndHeadAlone(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, dir, ".gitignore", "ignored.txt\nbuild/\n")
	writeFile(t, dir, "a.txt", "a1\n")
	writeFile(t, dir, "b.txt", "b1\n")
	writeFile(t, dir, "c.txt", "c1\n")
	head := commitAll(t, dir, "init")

	writeFile(t, dir, "a.txt", "a2\n")          // unstaged change
	writeFile(t, dir, "b.txt", "b2\n")          // staged change...
	git(t, dir, "add", "b.txt")                 //
	writeFile(t, dir, "b.txt", "b3\n")          // ...then changed again
	os.Remove(filepath.Join(dir, "c.txt"))      // deleted
	writeFile(t, dir, "new/untracked.txt", "u") // untracked
	writeFile(t, dir, "ignored.txt", "secret")  // ignored
	writeFile(t, dir, "build/out.o", "obj")     // ignored dir

	before := stateOf(t, dir)
	sha := checkpoint(t, dir, "1-before")
	after := stateOf(t, dir)

	if !bytes.Equal(before.index, after.index) {
		t.Error(".git/index changed")
	}
	if before.status != after.status || before.head != after.head || before.staged != after.staged {
		t.Errorf("repo state changed:\nbefore %+v\nafter  %+v", before, after)
	}
	if after.head != head || !strings.Contains(after.staged, "b.txt") {
		t.Errorf("head %s staged %q", after.head, after.staged)
	}

	if got := git(t, dir, "rev-parse", "refs/ra/cp/test/1-before"); got != sha {
		t.Errorf("ref = %s, want %s", got, sha)
	}
	if got := git(t, dir, "rev-parse", sha+"^"); got != head {
		t.Errorf("checkpoint parent = %s, want HEAD %s", got, head)
	}
	if got, want := treeFiles(t, dir, sha), []string{".gitignore", "a.txt", "b.txt", "new/untracked.txt"}; !slices.Equal(got, want) {
		t.Errorf("checkpoint files = %v, want %v", got, want)
	}
	// The snapshot holds the working tree contents, not the index's.
	if got := git(t, dir, "show", sha+":a.txt"); got != "a2" {
		t.Errorf("a.txt = %q", got)
	}
	if got := git(t, dir, "show", sha+":b.txt"); got != "b3" {
		t.Errorf("b.txt = %q", got)
	}
	// Checkpoints are hidden from branches and the log.
	if out := git(t, dir, "log", "--oneline", "--branches"); strings.Contains(out, "checkpoint") {
		t.Errorf("checkpoint visible in branch log: %s", out)
	}

	// An unchanged tree yields the same tree object.
	sha2 := checkpoint(t, dir, "1-after")
	if git(t, dir, "rev-parse", sha+"^{tree}") != git(t, dir, "rev-parse", sha2+"^{tree}") {
		t.Error("identical workspace produced different trees")
	}
	files, err := DiffFiles(ctx, dir, sha, sha2)
	if err != nil || len(files) != 0 {
		t.Errorf("diff of identical checkpoints = %v, %v", files, err)
	}
}

func TestDiffFiles(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, dir, "keep.txt", "same\n")
	writeFile(t, dir, "mod.txt", "one\ntwo\nthree\n")
	writeFile(t, dir, "del.txt", "d1\nd2\nd3\nd4\n")
	writeFile(t, dir, "old name.txt", lines(20, "rename me "))
	writeFile(t, dir, "moved.txt", lines(20, "move+edit "))
	writeFile(t, dir, "bin.dat", "\x00\x01\x02binary")
	commitAll(t, dir, "init")
	from := checkpoint(t, dir, "from")

	writeFile(t, dir, "mod.txt", "one\nTWO\nthree\nfour\n")
	os.Remove(filepath.Join(dir, "del.txt"))
	os.Remove(filepath.Join(dir, "old name.txt"))
	writeFile(t, dir, "dir/new name.txt", lines(20, "rename me "))
	os.Remove(filepath.Join(dir, "moved.txt"))
	writeFile(t, dir, "sub/moved.txt", lines(20, "move+edit ")+"extra\n")
	writeFile(t, dir, "added.txt", "x\ny\n")
	writeFile(t, dir, "bin.dat", "\x00\x03\x04binary changed")
	to := checkpoint(t, dir, "to")

	files, err := DiffFiles(ctx, dir, from, to)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]FileStat{}
	for _, f := range files {
		got[f.Path] = f
	}
	want := map[string]FileStat{
		"added.txt":        {Path: "added.txt", Status: Added, Additions: 2},
		"mod.txt":          {Path: "mod.txt", Status: Modified, Additions: 2, Deletions: 1},
		"del.txt":          {Path: "del.txt", Status: Deleted, Deletions: 4},
		"dir/new name.txt": {Path: "dir/new name.txt", OldPath: "old name.txt", Status: Renamed},
		"sub/moved.txt":    {Path: "sub/moved.txt", OldPath: "moved.txt", Status: Renamed, Additions: 1},
		"bin.dat":          {Path: "bin.dat", Status: Modified, Binary: true},
	}
	if len(got) != len(want) {
		t.Errorf("files = %+v", files)
	}
	for p, w := range want {
		if got[p] != w {
			t.Errorf("%s = %+v, want %+v", p, got[p], w)
		}
	}
}

func TestPatch(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, dir, "a.txt", "a\n")
	writeFile(t, dir, "b.txt", "b\n")
	commitAll(t, dir, "init")
	from := checkpoint(t, dir, "from")
	writeFile(t, dir, "a.txt", lines(200, "new a "))
	writeFile(t, dir, "b.txt", "b changed\n")
	to := checkpoint(t, dir, "to")

	full, truncated, err := Patch(ctx, dir, from, to, nil, 0)
	if err != nil || truncated {
		t.Fatalf("patch: %v truncated=%v", err, truncated)
	}
	if !strings.Contains(full, "diff --git a/a.txt b/a.txt") || !strings.Contains(full, "+b changed") {
		t.Errorf("patch lacks expected hunks:\n%s", full)
	}

	only, _, err := Patch(ctx, dir, from, to, []string{"b.txt"}, 0)
	if err != nil || strings.Contains(only, "a.txt") || !strings.Contains(only, "+b changed") {
		t.Errorf("path-filtered patch = %q, %v", only, err)
	}

	const max = 1000
	cut, truncated, err := Patch(ctx, dir, from, to, nil, max)
	if err != nil || !truncated {
		t.Fatalf("truncated patch: %v truncated=%v", err, truncated)
	}
	if len(cut) > max || !strings.HasSuffix(cut, "\n") || !strings.HasPrefix(full, cut) {
		t.Errorf("truncated patch is %d bytes, ends %q", len(cut), cut[len(cut)-10:])
	}
	if same, truncated, _ := Patch(ctx, dir, from, to, nil, len(full)); truncated || same != full {
		t.Error("patch exactly at the limit was truncated")
	}
}

func TestRevert(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, dir, ".gitignore", "*.log\n")
	writeFile(t, dir, "mod.txt", "original\n")
	writeFile(t, dir, "del.txt", "keep me\n")
	writeFile(t, dir, "ren.txt", lines(20, "renamed "))
	writeFile(t, dir, "untouched.txt", "same\n")
	head := commitAll(t, dir, "init")
	writeFile(t, dir, "wip.txt", "uncommitted but present before the turn\n")
	target := checkpoint(t, dir, "1-before")

	// The "turn": add, modify, delete, rename; plus an ignored file.
	writeFile(t, dir, "added/deep/new.txt", "new\n")
	writeFile(t, dir, "mod.txt", "changed\n")
	writeFile(t, dir, "wip.txt", "changed too\n")
	os.Remove(filepath.Join(dir, "del.txt"))
	os.Rename(filepath.Join(dir, "ren.txt"), filepath.Join(dir, "ren2.txt"))
	writeFile(t, dir, "debug.log", "ignored\n")
	now := checkpoint(t, dir, "live")

	files, err := Revert(ctx, dir, target, now)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, string(f.Status)+" "+f.Path)
	}
	slices.Sort(got)
	want := []string{"added added/deep/new.txt", "deleted del.txt", "modified mod.txt", "modified wip.txt", "renamed ren2.txt"}
	if !slices.Equal(got, want) {
		t.Errorf("reverted = %v, want %v", got, want)
	}

	if exists(dir, "added/deep/new.txt") || exists(dir, "ren2.txt") {
		t.Error("added/renamed files still exist")
	}
	for f, content := range map[string]string{
		"mod.txt": "original\n", "del.txt": "keep me\n", "ren.txt": lines(20, "renamed "),
		"wip.txt": "uncommitted but present before the turn\n", "untouched.txt": "same\n",
		"debug.log": "ignored\n", // ignored files are not part of checkpoints
	} {
		if got := readFile(t, dir, f); got != content {
			t.Errorf("%s = %q, want %q", f, got, content)
		}
	}
	// The workspace now matches the target checkpoint.
	if files, _ := DiffFiles(ctx, dir, target, checkpoint(t, dir, "after-revert")); len(files) != 0 {
		t.Errorf("workspace differs from target after revert: %+v", files)
	}
	// Only the working tree is touched.
	if h, _ := Head(ctx, dir); h != head {
		t.Errorf("HEAD moved to %s", h)
	}
	if staged := git(t, dir, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("revert staged changes: %s", staged)
	}
}

func TestRepoWithoutCommits(t *testing.T) {
	dir := newRepo(t)
	if !IsRepo(ctx, dir) {
		t.Fatal("IsRepo = false")
	}
	if _, err := Head(ctx, dir); err == nil {
		t.Fatal("unborn HEAD resolved")
	}
	writeFile(t, dir, "a.txt", "a1\n")
	writeFile(t, dir, "b.txt", "b1\n")
	before := stateOf(t, dir)
	cp1 := checkpoint(t, dir, "1-before")
	after := stateOf(t, dir)
	if before.status != after.status || after.head != "" || !bytes.Equal(before.index, after.index) {
		t.Errorf("checkpoint changed an empty repo: %+v -> %+v", before, after)
	}
	if parents := strings.Fields(git(t, dir, "rev-list", "--parents", "-n1", cp1)); len(parents) != 1 {
		t.Errorf("root checkpoint has parents: %v", parents)
	}
	if got := treeFiles(t, dir, cp1); !slices.Equal(got, []string{"a.txt", "b.txt"}) {
		t.Errorf("files = %v", got)
	}

	writeFile(t, dir, "a.txt", "a2\n")
	writeFile(t, dir, "c.txt", "c\n")
	os.Remove(filepath.Join(dir, "b.txt"))
	cp2 := checkpoint(t, dir, "live")
	files, err := DiffFiles(ctx, dir, cp1, cp2)
	if err != nil || len(files) != 3 {
		t.Fatalf("diff = %+v, %v", files, err)
	}
	if _, err := Revert(ctx, dir, cp1, cp2); err != nil {
		t.Fatal(err)
	}
	if readFile(t, dir, "a.txt") != "a1\n" || readFile(t, dir, "b.txt") != "b1\n" || exists(dir, "c.txt") {
		t.Error("revert did not restore the first checkpoint")
	}
	if _, err := Head(ctx, dir); err == nil {
		t.Error("revert created a commit")
	}
}

func TestWorktreesAndRefs(t *testing.T) {
	repo := newRepo(t)
	writeFile(t, repo, "a.txt", "a\n")
	commitAll(t, repo, "init")
	if b := CurrentBranch(ctx, repo); b != "main" {
		t.Errorf("branch = %q", b)
	}
	wt := filepath.Join(t.TempDir(), "wts", "s1")
	if err := AddWorktree(ctx, repo, wt, "ra/test", ""); err != nil {
		t.Fatal(err)
	}
	if CurrentBranch(ctx, wt) != "ra/test" || readFile(t, wt, "a.txt") != "a\n" {
		t.Error("worktree not checked out on its branch")
	}
	branches, err := Branches(ctx, repo)
	if err != nil || !slices.Contains(branches, "main") || !slices.Contains(branches, "ra/test") {
		t.Errorf("branches = %v, %v", branches, err)
	}
	if err := AddWorktree(ctx, repo, filepath.Join(t.TempDir(), "dup"), "ra/test", ""); err == nil {
		t.Error("duplicate branch accepted")
	}

	// Checkpoints in a worktree land in the shared ref namespace.
	writeFile(t, wt, "b.txt", "b\n")
	sha := checkpoint(t, wt, "wt")
	if git(t, repo, "rev-parse", "refs/ra/cp/test/wt") != sha {
		t.Error("worktree checkpoint ref not visible from the main repo")
	}
	if err := DeleteRefs(ctx, repo, "refs/ra/cp/test/"); err != nil {
		t.Fatal(err)
	}
	if out := git(t, repo, "for-each-ref", "refs/ra/"); out != "" {
		t.Errorf("refs left: %s", out)
	}

	if err := RemoveWorktree(ctx, repo, wt); err != nil {
		t.Fatal(err)
	}
	if exists(wt, "") {
		t.Error("worktree dir still exists")
	}
	if IsRepo(ctx, t.TempDir()) {
		t.Error("plain dir is a repo")
	}
}
