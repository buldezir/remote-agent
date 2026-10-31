package gitx

import (
	"context"
	"strconv"
	"strings"
)

// Remotes lists the repository's remotes by name.
func Remotes(ctx context.Context, dir string) []string {
	out, _ := run(ctx, dir, nil, "remote")
	return strings.Fields(out)
}

// Upstream returns the remote and the branch there that branch tracks, or
// "" for a branch without one.
func Upstream(ctx context.Context, dir, branch string) (remote, merge string) {
	remote = config(ctx, dir, "branch."+branch+".remote")
	merge = strings.TrimPrefix(config(ctx, dir, "branch."+branch+".merge"), "refs/heads/")
	if remote == "" || merge == "" {
		return "", ""
	}
	return remote, merge
}

func config(ctx context.Context, dir, key string) string {
	out, _ := run(ctx, dir, nil, "config", "--get", key)
	return strings.TrimSpace(out)
}

// RemoteDefaultBranch returns the branch remote's HEAD names, as the last
// fetch or clone saw it, or "".
func RemoteDefaultBranch(ctx context.Context, dir, remote string) string {
	out, err := run(ctx, dir, nil, "symbolic-ref", "--quiet", "refs/remotes/"+remote+"/HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(out), "refs/remotes/"+remote+"/")
}

// RemoteBranchExists reports whether the last fetch or push saw branch on
// remote.
func RemoteBranchExists(ctx context.Context, dir, remote, branch string) bool {
	_, err := run(ctx, dir, nil, "rev-parse", "--verify", "--quiet", "refs/remotes/"+remote+"/"+branch)
	return err == nil
}

// CountCommits counts the commits in a range, such as "main..HEAD".
func CountCommits(ctx context.Context, dir, rangeSpec string) int {
	out, err := run(ctx, dir, nil, "rev-list", "--count", rangeSpec)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(out))
	return n
}

// MergeBase returns the best common ancestor of two commits.
func MergeBase(ctx context.Context, dir, a, b string) (string, error) {
	out, err := run(ctx, dir, nil, "merge-base", a, b)
	return strings.TrimSpace(out), err
}

type Commit struct {
	SHA     string
	Subject string
	Body    string
}

// Log lists up to max commits of a range, such as "main..HEAD", newest
// first.
func Log(ctx context.Context, dir, rangeSpec string, max int) ([]Commit, error) {
	out, err := run(ctx, dir, nil, "log", "--no-color", "-n", strconv.Itoa(max), "--format=%H%x1f%s%x1f%b%x1e", rangeSpec)
	if err != nil {
		return nil, err
	}
	var commits []Commit
	for _, rec := range strings.Split(out, "\x1e") {
		f := strings.SplitN(strings.TrimLeft(rec, "\n"), "\x1f", 3)
		if len(f) < 3 {
			continue
		}
		commits = append(commits, Commit{SHA: f[0], Subject: f[1], Body: strings.TrimSpace(f[2])})
	}
	return commits, nil
}
