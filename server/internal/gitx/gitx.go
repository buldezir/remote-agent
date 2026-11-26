// Package gitx wraps the git CLI for worktrees, checkpoints and diffs.
//
// Checkpoints snapshot the whole working tree (tracked and untracked files,
// honoring .gitignore) into a commit referenced by a hidden ref, using an
// index of rad's own so the user's index and HEAD are never touched.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
)

var identityEnv = []string{
	"GIT_AUTHOR_NAME=rad", "GIT_AUTHOR_EMAIL=rad@localhost",
	"GIT_COMMITTER_NAME=rad", "GIT_COMMITTER_EMAIL=rad@localhost",
}

func run(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	return runInput(ctx, dir, env, "", args...)
}

// runInput is run, with input on git's stdin.
func runInput(ctx context.Context, dir string, env []string, input string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"), env...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func IsRepo(ctx context.Context, dir string) bool {
	out, err := run(ctx, dir, nil, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

func Head(ctx context.Context, dir string) (string, error) {
	return Resolve(ctx, dir, "HEAD")
}

// Resolve returns the commit rev names, such as a ref.
func Resolve(ctx context.Context, dir, rev string) (string, error) {
	out, err := run(ctx, dir, nil, "rev-parse", "--verify", "-q", rev)
	return strings.TrimSpace(out), err
}

// CurrentBranch returns the checked-out branch (also when it has no commits
// yet), or "" for a detached HEAD.
func CurrentBranch(ctx context.Context, dir string) string {
	out, _ := run(ctx, dir, nil, "symbolic-ref", "--short", "-q", "HEAD")
	return strings.TrimSpace(out)
}

// HeadFiles returns the files whose contents change when the branch checked
// out in dir does, so that a watcher reads them instead of running git: HEAD,
// and the reftable stack that holds HEAD in a repo with reftable refs, whose
// HEAD file is a stub.
func HeadFiles(ctx context.Context, dir string) ([]string, error) {
	out, err := run(ctx, dir, nil, "rev-parse", "--git-path", "HEAD", "--git-path", "reftable/tables.list")
	if err != nil {
		return nil, err
	}
	files := strings.Split(strings.TrimSpace(out), "\n")
	for i, f := range files {
		if !filepath.IsAbs(f) {
			files[i] = filepath.Join(dir, f)
		}
	}
	return files, nil
}

// RenameBranch renames a branch, also the one checked out in dir.
func RenameBranch(ctx context.Context, dir, from, to string) error {
	_, err := run(ctx, dir, nil, "branch", "-m", from, to)
	return err
}

func Branches(ctx context.Context, dir string) ([]string, error) {
	out, err := run(ctx, dir, nil, "for-each-ref", "--format=%(refname:short)", "--sort=-committerdate", "refs/heads")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// AddWorktree creates a new branch at baseRef checked out at path.
func AddWorktree(ctx context.Context, repo, path, branch, baseRef string) error {
	if baseRef == "" {
		baseRef = "HEAD"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	_, err := run(ctx, repo, nil, "worktree", "add", "-b", branch, "--", path, baseRef)
	return err
}

// CheckoutWorktree checks out an existing branch at path, as a new worktree.
func CheckoutWorktree(ctx context.Context, repo, path, branch string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	_, err := run(ctx, repo, nil, "worktree", "add", "--", path, branch)
	return err
}

// Worktree is a checkout of the repository, as `git worktree list` gives it.
type Worktree struct {
	Path   string
	Branch string // "" for a detached HEAD
	Main   bool   // the repository's own folder
}

// Worktrees lists the repository's checkouts, the main one first, leaving
// out the ones whose folder is gone.
func Worktrees(ctx context.Context, repo string) ([]Worktree, error) {
	out, err := run(ctx, repo, nil, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var wts []Worktree
	for i, block := range strings.Split(strings.TrimSpace(out), "\n\n") {
		var wt Worktree
		skip := false
		for line := range strings.SplitSeq(block, "\n") {
			key, val, _ := strings.Cut(line, " ")
			switch key {
			case "worktree":
				wt.Path = val
			case "branch":
				wt.Branch = strings.TrimPrefix(val, "refs/heads/")
			case "bare", "prunable":
				skip = true
			}
		}
		wt.Main = i == 0
		if wt.Path == "" || skip {
			continue
		}
		if _, err := os.Stat(wt.Path); err != nil {
			continue
		}
		wts = append(wts, wt)
	}
	return wts, nil
}

// SamePath reports whether a and b name the same folder, through symlinks
// (such as macOS's /var and /private/var).
func SamePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

func RemoveWorktree(ctx context.Context, repo, path string) error {
	_, err := run(ctx, repo, nil, "worktree", "remove", "--force", "--", path)
	return err
}

// DeleteBranch deletes a branch, merged or not.
func DeleteBranch(ctx context.Context, repo, branch string) error {
	_, err := run(ctx, repo, nil, "branch", "-D", "--", branch)
	return err
}

// PruneWorktrees drops the metadata of worktrees whose directory is gone.
func PruneWorktrees(ctx context.Context, repo string) error {
	_, err := run(ctx, repo, nil, "worktree", "prune")
	return err
}

func BranchExists(ctx context.Context, dir, branch string) bool {
	_, err := run(ctx, dir, nil, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// Dirty reports whether dir has uncommitted changes, untracked files included.
func Dirty(ctx context.Context, dir string) bool {
	out, err := run(ctx, dir, nil, "status", "--porcelain")
	return err == nil && strings.TrimSpace(out) != ""
}

// Checkpoint snapshots dir's working tree into a commit stored at ref and returns its sha.
func Checkpoint(ctx context.Context, dir, ref, message string) (string, error) {
	var sha string
	err := withTreeIndex(ctx, dir, func(top string, env []string) error {
		env = append(env, identityEnv...)
		tree, err := run(ctx, top, env, "write-tree")
		if err != nil {
			return err
		}
		args := []string{"commit-tree", "--no-gpg-sign", strings.TrimSpace(tree), "-m", message}
		if head, _ := Head(ctx, top); head != "" {
			args = append(args, "-p", head)
		}
		out, err := run(ctx, top, env, args...)
		sha = strings.TrimSpace(out)
		return err
	})
	if err != nil {
		return "", err
	}
	if _, err := run(ctx, dir, nil, "update-ref", ref, sha); err != nil {
		return "", err
	}
	return sha, nil
}

// treeIndex is the name, in a worktree's git dir, of rad's own index: the
// whole working tree, staged as a checkpoint takes it.
const treeIndex = "rad-index"

// treeIndexLocks holds a mutex per tree index, so that two sessions in one
// folder take turns with it.
var treeIndexLocks sync.Map

// withTreeIndex stages dir's whole working tree in rad's own index, and
// calls fn with the repository's top folder and the environment that points
// git at that index. The user's index stays as it is.
//
// The index stays between calls, so that git's stat cache spares it hashing
// the files that haven't changed: a fresh index hashed every file, untracked
// ones too, at every checkpoint, which takes seconds in a tree with large
// files. Should it fail, it's made again from nothing.
func withTreeIndex(ctx context.Context, dir string, fn func(top string, env []string) error) error {
	out, err := run(ctx, dir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	top := strings.TrimSpace(out)
	idx, err := gitPath(ctx, top, treeIndex)
	if err != nil {
		return err
	}
	key := idx
	if real, err := filepath.EvalSymlinks(filepath.Dir(idx)); err == nil {
		key = filepath.Join(real, treeIndex)
	}
	mu, _ := treeIndexLocks.LoadOrStore(key, new(sync.Mutex))
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()

	env := []string{"GIT_INDEX_FILE=" + idx}
	err = stageTree(ctx, top, idx, env, true)
	if err == nil {
		err = fn(top, env)
	}
	if err != nil && ctx.Err() == nil {
		// Its objects may have been pruned, or the user's index it started
		// from may be one git can't use elsewhere: start again from HEAD.
		os.Remove(idx)
		if err = stageTree(ctx, top, idx, env, false); err == nil {
			err = fn(top, env)
		}
	}
	return err
}

// stageTree brings the index at idx in line with the working tree at top:
// what HEAD has and is still there, and the files .gitignore doesn't
// ignore. A new index starts as a copy of the user's, when seed allows,
// for its stat cache, or else from HEAD.
func stageTree(ctx context.Context, top, idx string, env []string, seed bool) error {
	head, _ := Head(ctx, top)
	if _, err := os.Stat(idx); err != nil {
		if !seed || !copyUserIndex(ctx, top, idx) {
			os.Remove(idx) // git wants to create it
			if head != "" {
				if _, err := run(ctx, top, env, "read-tree", "HEAD"); err != nil {
					return err
				}
			}
		}
	}
	if _, err := run(ctx, top, env, "add", "-A"); err != nil {
		return err
	}
	// add -A keeps what the index has. Drop files it has that are ignored
	// now and not in HEAD (untracked when staged, or staged by the user
	// with add -f), and add those HEAD has that it lacks (committed while
	// ignored, since it was made): a fresh index has neither.
	ignored, err := run(ctx, top, env, "ls-files", "-z", "--cached", "--ignored", "--exclude-standard")
	if err != nil {
		return err
	}
	if drop := splitNUL(ignored); len(drop) > 0 {
		if head != "" {
			added, err := run(ctx, top, env, "diff-index", "--cached", "--name-only", "-z", "--diff-filter=A", "HEAD")
			if err != nil {
				return err
			}
			notInHead := map[string]bool{}
			for _, p := range splitNUL(added) {
				notInHead[p] = true
			}
			drop = slices.DeleteFunc(drop, func(p string) bool { return !notInHead[p] })
		}
		if len(drop) > 0 {
			if _, err := runInput(ctx, top, env, strings.Join(drop, "\x00"), "update-index", "-z", "--force-remove", "--stdin"); err != nil {
				return err
			}
		}
	}
	if head == "" {
		return nil
	}
	missing, err := run(ctx, top, env, "diff-index", "--cached", "--name-only", "-z", "--diff-filter=D", "HEAD")
	if err != nil {
		return err
	}
	var restore []string
	for _, p := range splitNUL(missing) {
		if fi, err := os.Lstat(filepath.Join(top, p)); err == nil && !fi.IsDir() {
			restore = append(restore, p)
		}
	}
	if len(restore) > 0 {
		if _, err := runInput(ctx, top, env, strings.Join(restore, "\x00"), "update-index", "-z", "--add", "--stdin"); err != nil {
			return err
		}
	}
	return nil
}

// copyUserIndex copies the worktree's own index to idx, and reports whether
// it did.
func copyUserIndex(ctx context.Context, top, idx string) bool {
	src, err := gitPath(ctx, top, "index")
	if err != nil {
		return false
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return false
	}
	tmp := idx + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return false
	}
	if err := os.Rename(tmp, idx); err != nil {
		os.Remove(tmp)
		return false
	}
	return true
}

// gitPath is the path of name in dir's git dir, as git rev-parse --git-path
// resolves it: a linked worktree's own, for an index.
func gitPath(ctx context.Context, dir, name string) (string, error) {
	out, err := run(ctx, dir, nil, "rev-parse", "--git-path", name)
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(out)
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return p, nil
}

func splitNUL(s string) []string {
	var out []string
	for _, p := range strings.Split(s, "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Uncommitted lists dir's changes since its last commit, untracked files
// included, as committing everything would take them, and their patch cut
// to max bytes. The user's index stays as it is.
func Uncommitted(ctx context.Context, dir string, max int) (files []FileStat, patchText string, truncated bool, err error) {
	err = withTreeIndex(ctx, dir, func(top string, env []string) error {
		// --cached without a commit compares with HEAD, or with nothing on a
		// branch that has no commits yet.
		var err error
		if files, err = diffFiles(ctx, top, env, "--cached"); err != nil {
			return err
		}
		patchText, truncated, err = patch(ctx, top, env, max, "--cached")
		return err
	})
	if err != nil {
		return nil, "", false, err
	}
	return files, patchText, truncated, nil
}

// SetRef points ref at sha.
func SetRef(ctx context.Context, dir, ref, sha string) error {
	_, err := run(ctx, dir, nil, "update-ref", ref, sha)
	return err
}

func DeleteRefs(ctx context.Context, dir, prefix string) error {
	out, err := run(ctx, dir, nil, "for-each-ref", "--format=%(refname)", prefix)
	if err != nil {
		return err
	}
	for _, ref := range strings.Fields(out) {
		if _, err := run(ctx, dir, nil, "update-ref", "-d", ref); err != nil {
			return err
		}
	}
	return nil
}

type FileStatus string

const (
	Added    FileStatus = "added"
	Modified FileStatus = "modified"
	Deleted  FileStatus = "deleted"
	Renamed  FileStatus = "renamed"
)

type FileStat struct {
	Path      string     `json:"path"`
	OldPath   string     `json:"oldPath,omitempty"`
	Status    FileStatus `json:"status"`
	Additions int        `json:"additions"`
	Deletions int        `json:"deletions"`
	Binary    bool       `json:"binary,omitempty"`
}

// DiffFiles lists files changed between two commits.
func DiffFiles(ctx context.Context, dir, from, to string) ([]FileStat, error) {
	return diffFiles(ctx, dir, nil, from, to)
}

// diffFiles lists the files git diff finds with args, such as two commits.
func diffFiles(ctx context.Context, dir string, env []string, args ...string) ([]FileStat, error) {
	ns, err := run(ctx, dir, env, append([]string{"diff", "--name-status", "-z", "-M"}, args...)...)
	if err != nil {
		return nil, err
	}
	var files []FileStat
	byPath := map[string]int{}
	parts := strings.Split(strings.TrimSuffix(ns, "\x00"), "\x00")
	for i := 0; i < len(parts); i++ {
		code := parts[i]
		if code == "" {
			continue
		}
		f := FileStat{}
		switch code[0] {
		case 'A':
			f.Status = Added
		case 'D':
			f.Status = Deleted
		case 'R', 'C':
			f.Status = Renamed
			f.OldPath = parts[i+1]
			i++
		default:
			f.Status = Modified
		}
		i++
		if i >= len(parts) {
			break
		}
		f.Path = parts[i]
		byPath[f.Path] = len(files)
		files = append(files, f)
	}

	num, err := run(ctx, dir, env, append([]string{"diff", "--numstat", "-z", "-M"}, args...)...)
	if err != nil {
		return nil, err
	}
	// Format: "add\tdel\tpath\0" or for renames "add\tdel\t\0old\0new\0".
	parts = strings.Split(strings.TrimSuffix(num, "\x00"), "\x00")
	for i := 0; i < len(parts); i++ {
		fields := strings.SplitN(parts[i], "\t", 3)
		if len(fields) < 3 {
			continue
		}
		path := fields[2]
		if path == "" && i+2 < len(parts) {
			path = parts[i+2]
			i += 2
		}
		j, ok := byPath[path]
		if !ok {
			continue
		}
		if fields[0] == "-" {
			files[j].Binary = true
			continue
		}
		files[j].Additions, _ = strconv.Atoi(fields[0])
		files[j].Deletions, _ = strconv.Atoi(fields[1])
	}
	return files, nil
}

// Patch returns the unified diff between two commits, truncated to max bytes.
func Patch(ctx context.Context, dir, from, to string, paths []string, max int) (string, bool, error) {
	args := []string{from, to}
	if len(paths) > 0 {
		args = append(append(args, "--"), paths...)
	}
	return patch(ctx, dir, nil, max, args...)
}

func patch(ctx context.Context, dir string, env []string, max int, args ...string) (string, bool, error) {
	out, err := run(ctx, dir, env, append([]string{"diff", "--no-color", "--no-ext-diff", "-M"}, args...)...)
	if err != nil {
		return "", false, err
	}
	if max > 0 && len(out) > max {
		cut := strings.LastIndexByte(out[:max], '\n')
		if cut < 0 {
			cut = max
		}
		return out[:cut+1], true, nil
	}
	return out, false, nil
}

// Revert makes the working tree of dir match commit target for every path that
// differs between target and current (a checkpoint of the present state).
// Files outside that diff are untouched.
func Revert(ctx context.Context, dir, target, current string) ([]FileStat, error) {
	files, err := DiffFiles(ctx, dir, target, current)
	if err != nil {
		return nil, err
	}
	var restore []string
	for _, f := range files {
		switch f.Status {
		case Added:
			if err := removeFile(dir, f.Path); err != nil {
				return nil, err
			}
		case Renamed:
			if err := removeFile(dir, f.Path); err != nil {
				return nil, err
			}
			restore = append(restore, f.OldPath)
		default:
			restore = append(restore, f.Path)
		}
	}
	if len(restore) > 0 {
		top, err := run(ctx, dir, nil, "rev-parse", "--show-toplevel")
		if err != nil {
			return nil, err
		}
		args := append([]string{"restore", "--source=" + target, "--worktree", "--"}, restore...)
		if _, err := run(ctx, strings.TrimSpace(top), nil, args...); err != nil {
			return nil, err
		}
	}
	return files, nil
}

func removeFile(dir, rel string) error {
	top, err := run(context.Background(), dir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	p := filepath.Join(strings.TrimSpace(top), rel)
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
