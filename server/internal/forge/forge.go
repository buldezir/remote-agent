// Package forge finds the pull requests from a repository's branches on the
// forge that hosts it, for the sessions that work on them.
//
// GitHub, through its command line tool gh, is the only forge so far. Another
// (GitLab with glab, Bitbucket, Gitea…) implements Forge, recognizes its
// remotes in Hosts, and goes in forges; nothing else changes: model's
// PullRequest names its forge, and the clients only draw what it says.
package forge

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"remote-agent/internal/model"
)

// Runner runs a command in dir without a terminal, and returns its stdout.
// The orchestrator's keeps the command from asking anything.
type Runner func(ctx context.Context, dir, name string, args ...string) (string, error)

// A Forge hosts repositories and their pull requests.
type Forge interface {
	ID() string   // as model.PullRequest's Forge: github
	Name() string // as the user knows it: GitHub
	// Hosts reports whether the forge hosts the repository at remote.
	Hosts(remote Remote) bool
	// PullRequest finds the pull request from branch of the repository in
	// dir: an open one first, else the latest; nil for none.
	PullRequest(ctx context.Context, dir, branch string) (*model.PullRequest, error)
}

var (
	// ErrUnavailable: the forge's tool isn't installed on the computer.
	ErrUnavailable = errors.New("the forge's command line tool isn't installed")
	// ErrSignedOut: its tool isn't signed in.
	ErrSignedOut = errors.New("the forge's command line tool isn't signed in")
)

// forges are the forges rad knows, in the order For asks them.
func forges(run Runner) []Forge {
	return []Forge{&GitHub{Run: run}}
}

// For is the forge that hosts the repository at the remote address, or nil
// for none rad knows.
func For(remoteURL string, run Runner) Forge {
	r, ok := ParseRemote(remoteURL)
	if !ok {
		return nil
	}
	for _, f := range forges(run) {
		if f.Hosts(r) {
			return f
		}
	}
	return nil
}

// Remote is a git remote's address, taken apart.
type Remote struct {
	Host string // lower case, without a user or port: github.com
	Path string // without slashes around it or .git: acme/app
}

// ParseRemote takes apart a remote's address: https://github.com/acme/app.git,
// git@github.com:acme/app.git, ssh://git@github.com:22/acme/app. A local
// path or file:// address has no host, and isn't one.
func ParseRemote(s string) (Remote, bool) {
	s = strings.TrimSpace(s)
	var host, path string
	switch {
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil || u.Scheme == "file" {
			return Remote{}, false
		}
		host, path = u.Hostname(), u.Path
	case strings.Contains(s, ":") && !strings.HasPrefix(s, "/"):
		// scp-like: [user@]host:path
		host, path, _ = strings.Cut(s, ":")
		if i := strings.LastIndex(host, "@"); i >= 0 {
			host = host[i+1:]
		}
		if strings.Contains(host, "/") {
			return Remote{}, false // a relative path with a colon in it
		}
	default:
		return Remote{}, false
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if host == "" || path == "" {
		return Remote{}, false
	}
	return Remote{Host: strings.ToLower(host), Path: path}, true
}
