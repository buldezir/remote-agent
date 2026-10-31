package client

import (
	"context"
	"strings"
	"time"

	"remote-agent/internal/model"
)

// Committing a session's work: what a commit would take, and the commit
// itself, which rad runs as a task that commits, pushes and opens a pull
// request, as far as the user asked.

// CommitPreview shows what a commit would take in the session, with a
// message the system agent writes for it, which may take it a minute.
func (c *ServerConnection) CommitPreview(ctx context.Context, sessionID string) (CommitPreview, error) {
	var p CommitPreview
	err := c.rpc.CallTimeout(ctx, "git.commitPreview", map[string]any{"sessionId": sessionID}, &p, 90*time.Second)
	if p.Files == nil {
		p.Files = []FileStat{}
	}
	return p, err
}

// Commit starts the session's commit, a task to follow with
// ServerTaskProgress. Starting it while one runs returns that one.
func (c *ServerConnection) Commit(ctx context.Context, p CommitParams) (model.Task, error) {
	params := map[string]any{"sessionId": p.SessionID}
	if m := strings.TrimSpace(p.Message); m != "" {
		params["message"] = m
	}
	if p.Push || p.PullRequest {
		params["push"] = true
	}
	if p.PullRequest {
		params["pullRequest"] = true
	}
	var t model.Task
	err := c.rpc.Call(ctx, "git.commit", params, &t)
	return t, err
}

// SessionTasks lists the session's tasks, its commits, that run and that
// ended lately, oldest first.
func (c *ServerConnection) SessionTasks(ctx context.Context, sessionID string) ([]model.Task, error) {
	var r taskList
	err := c.rpc.Call(ctx, "task.list", map[string]any{"sessionId": sessionID}, &r)
	return r.Tasks, err
}

// CommitScope is how far a commit goes.
type CommitScope int

const (
	CommitOnly CommitScope = iota
	CommitAndPush
	CommitPushAndPR
)

var CommitScopes = []CommitScope{CommitOnly, CommitAndPush, CommitPushAndPR}

// Choice is the scope as the form offers it.
func (s CommitScope) Choice() string {
	switch s {
	case CommitAndPush:
		return "Commit & push"
	case CommitPushAndPR:
		return "Commit, push & open PR"
	}
	return "Commit only"
}

// Action is the button that starts it: with nothing to commit, it only
// pushes.
func (s CommitScope) Action(files bool) string {
	switch {
	case s == CommitAndPush && files:
		return "Commit & Push"
	case s == CommitAndPush:
		return "Push"
	case s == CommitPushAndPR && files:
		return "Commit, Push & Open PR"
	case s == CommitPushAndPR:
		return "Push & Open PR"
	}
	return "Commit"
}

// String is the scope as settings keep it.
func (s CommitScope) String() string {
	switch s {
	case CommitAndPush:
		return "push"
	case CommitPushAndPR:
		return "pullRequest"
	}
	return "commit"
}

// Unavailable says why the scope can't go ahead with what the preview
// found, or "".
func (s CommitScope) Unavailable(p CommitPreview) string {
	if s == CommitOnly {
		if len(p.Files) == 0 {
			return "Nothing to commit."
		}
		return ""
	}
	switch {
	case p.Branch == "":
		return "HEAD is detached: there's no branch to push."
	case p.Remote == "":
		return "No remote to push to."
	case s == CommitPushAndPR && p.NoPullRequest != "":
		return sentence(p.NoPullRequest)
	}
	return ""
}

// sentence ends s with a full stop.
func sentence(s string) string {
	if strings.HasSuffix(s, ".") {
		return s
	}
	return s + "."
}

// Params are git.commit's for the scope.
func (s CommitScope) Params(sessionID, message string) CommitParams {
	return CommitParams{SessionID: sessionID, Message: strings.TrimSpace(message), Push: s != CommitOnly, PullRequest: s == CommitPushAndPR}
}

// DefaultCommitScope is the scope a form starts at: the one saved, the
// last chosen, where it can go ahead; else commit and push with a remote,
// else commit only.
func DefaultCommitScope(saved string, p CommitPreview) CommitScope {
	for _, s := range CommitScopes {
		if s.String() == saved && s.Unavailable(p) == "" {
			return s
		}
	}
	if CommitAndPush.Unavailable(p) == "" {
		return CommitAndPush
	}
	return CommitOnly
}
