// Package gitx wraps the git CLI for worktrees, checkpoints and diffs.
//
// Checkpoints snapshot the whole working tree (tracked and untracked files,
// honoring .gitignore) into a commit referenced by a hidden ref, using a
// temporary index so the user's index and HEAD are never touched.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

var identityEnv = []string{
	"GIT_AUTHOR_NAME=rad", "GIT_AUTHOR_EMAIL=rad@localhost",
	"GIT_COMMITTER_NAME=rad", "GIT_COMMITTER_EMAIL=rad@localhost",
}

func run(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"), env...)
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
	out, err := run(ctx, dir, nil, "rev-parse", "--verify", "-q", "HEAD")
	return strings.TrimSpace(out), err
}

func CurrentBranch(ctx context.Context, dir string) string {
	out, _ := run(ctx, dir, nil, "rev-parse", "--abbrev-ref", "HEAD")
	return strings.TrimSpace(out)
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
	_, err := run(ctx, repo, nil, "worktree", "add", "-b", branch, path, baseRef)
	return err
}

func RemoveWorktree(ctx context.Context, repo, path string) error {
	_, err := run(ctx, repo, nil, "worktree", "remove", "--force", path)
	return err
}

// Checkpoint snapshots dir's working tree into a commit stored at ref and returns its sha.
func Checkpoint(ctx context.Context, dir, ref, message string) (string, error) {
	f, err := os.CreateTemp("", "rad-index-*")
	if err != nil {
		return "", err
	}
	idx := f.Name()
	f.Close()
	os.Remove(idx) // git wants to create it
	defer os.Remove(idx)
	env := append([]string{"GIT_INDEX_FILE=" + idx}, identityEnv...)

	head, _ := Head(ctx, dir)
	if head != "" {
		if _, err := run(ctx, dir, env, "read-tree", "HEAD"); err != nil {
			return "", err
		}
	}
	if _, err := run(ctx, dir, env, "add", "-A"); err != nil {
		return "", err
	}
	tree, err := run(ctx, dir, env, "write-tree")
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", "--no-gpg-sign", strings.TrimSpace(tree), "-m", message}
	if head != "" {
		args = append(args, "-p", head)
	}
	sha, err := run(ctx, dir, env, args...)
	if err != nil {
		return "", err
	}
	sha = strings.TrimSpace(sha)
	if _, err := run(ctx, dir, nil, "update-ref", ref, sha); err != nil {
		return "", err
	}
	return sha, nil
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
	ns, err := run(ctx, dir, nil, "diff", "--name-status", "-z", "-M", from, to)
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

	num, err := run(ctx, dir, nil, "diff", "--numstat", "-z", "-M", from, to)
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
	args := []string{"diff", "--no-color", "--no-ext-diff", "-M", from, to}
	if len(paths) > 0 {
		args = append(append(args, "--"), paths...)
	}
	out, err := run(ctx, dir, nil, args...)
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
