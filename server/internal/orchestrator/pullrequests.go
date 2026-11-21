package orchestrator

import (
	"context"
	"errors"
	"sync"
	"time"

	"remote-agent/internal/forge"
	"remote-agent/internal/gitx"
	"remote-agent/internal/model"
	"remote-agent/internal/store"
)

// Pull requests: rad looks up the pull request from each session's branch on
// the forge that hosts the repository (internal/forge: GitHub, with gh) and
// keeps it on the session as Session.PullRequest, which the clients show
// with it. It looks while clients are connected, every prRefresh for a
// branch that has an open one or may get one, and every prSettled for one
// merged or closed; and soon after a turn or a commit ends, the branch
// changes, or a client opens the session (LookAtPullRequest), when the
// agent or the user may have opened, pushed to or merged one.
//
// It only asks the forge about branches that are pushed and aren't the
// repository's default branch, so a session that works on main, or on a
// branch of its own that it hasn't pushed, costs a few local git commands.
// What it can't tell (no remote, a forge rad doesn't know, gh missing or
// signed out) leaves what the session had; only a change of branch clears it.

const (
	prRefresh = 3 * time.Minute
	prSettled = 30 * time.Minute
	// prSoon is the shortest time between two looks for a session, however
	// it's nudged; a nudge sooner waits for it.
	prSoon = 20 * time.Second
	// prRetry is how long a forge whose tool failed (not installed, signed
	// out) goes unasked.
	prRetry = 10 * time.Minute
	// prTimeout is a look's time on the forge.
	prTimeout = 30 * time.Second
	// prPoll is how often the watcher wakes to see what's due.
	prPoll = 15 * time.Second
)

type prState struct {
	mu      sync.Mutex
	checked map[string]time.Time // by session: when rad last looked
	nudged  map[string]bool      // sessions to look at as soon as prSoon allows
	failed  map[string]time.Time // by forge: when its tool last failed
	wake    chan struct{}
	running sync.Mutex // one round at a time
}

func newPRState() prState {
	return prState{checked: map[string]time.Time{}, nudged: map[string]bool{}, failed: map[string]time.Time{}, wake: make(chan struct{}, 1)}
}

// nudgePullRequest has rad look at the session's pull request soon.
func (o *Orchestrator) nudgePullRequest(sessionID string) {
	o.prs.mu.Lock()
	o.prs.nudged[sessionID] = true
	o.prs.mu.Unlock()
	o.wakePullRequests()
}

func (o *Orchestrator) wakePullRequests() {
	select {
	case o.prs.wake <- struct{}{}:
	default:
	}
}

// LookAtPullRequest has rad look at the session's pull request soon, as a
// client opens the session, unless it looked within prSoon.
func (o *Orchestrator) LookAtPullRequest(sessionID string) {
	o.nudgePullRequest(sessionID)
}

// watchPullRequests keeps the sessions' pull requests fresh: those nudged
// at any time, the rest while clients are connected.
func (o *Orchestrator) watchPullRequests(ctx context.Context) {
	t := time.NewTicker(prPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-o.prs.wake:
		case <-t.C:
		}
		o.RefreshPullRequests(ctx)
	}
}

// RefreshPullRequests looks up the pull requests that are due: the nudged
// sessions', and, while clients are connected, those not looked at for
// prRefresh (prSettled for one merged or closed).
func (o *Orchestrator) RefreshPullRequests(ctx context.Context) {
	if !o.prs.running.TryLock() {
		return
	}
	defer o.prs.running.Unlock()
	o.limits.mu.Lock()
	watched := o.limits.clients > 0
	o.limits.mu.Unlock()
	wss, err := o.st.GitWorkspaces(ctx)
	if err != nil {
		o.log.Warn("listing workspaces failed", "err", err)
		return
	}
	now := time.Now()
	type found struct {
		pr *model.PullRequest
		ok bool
	}
	looked := map[[2]string]found{} // by remote address and branch: sessions on one branch share it
	live := map[string]bool{}
	for _, ws := range wss {
		live[ws.SessionID] = true
		if !o.prDue(ws, watched, now) {
			continue
		}
		dir, branch := Workdir(ws.Workspace), ws.Workspace.Branch
		f, url, ok := o.prForge(ctx, dir, branch, ws.PullRequest)
		if !ok {
			continue
		}
		key := [2]string{url, branch}
		res, seen := looked[key]
		if !seen {
			res.pr, res.ok = o.lookUpPullRequest(ctx, f, dir, branch)
			looked[key] = res
		}
		if res.ok && !samePullRequest(res.pr, ws.PullRequest) {
			if a, err := o.actor(ctx, ws.SessionID); err == nil {
				a.setPullRequest(ctx, branch, res.pr)
			}
		}
	}
	o.prs.mu.Lock()
	for id := range o.prs.checked {
		if !live[id] {
			delete(o.prs.checked, id)
		}
	}
	for id := range o.prs.nudged {
		if !live[id] {
			delete(o.prs.nudged, id)
		}
	}
	o.prs.mu.Unlock()
}

// prDue reports whether it's time to look at the session's pull request,
// and if so marks it looked at.
func (o *Orchestrator) prDue(ws store.GitWorkspace, watched bool, now time.Time) bool {
	o.prs.mu.Lock()
	defer o.prs.mu.Unlock()
	since := now.Sub(o.prs.checked[ws.SessionID])
	every := prRefresh
	if pr := ws.PullRequest; pr != nil && pr.Head == ws.Workspace.Branch && (pr.State == model.PullRequestMerged || pr.State == model.PullRequestClosed) {
		every = prSettled
	}
	due := since >= prSoon && (o.prs.nudged[ws.SessionID] || watched && since >= every)
	if due {
		o.prs.checked[ws.SessionID] = now
		delete(o.prs.nudged, ws.SessionID)
	}
	return due
}

// prForge is the forge to ask about branch, and the address of the remote
// it would be on; ok is false when there's nothing to ask: a detached HEAD,
// no remote, a forge rad doesn't know, the remote's default branch, or a
// branch it hasn't pushed and has no pull request known from.
func (o *Orchestrator) prForge(ctx context.Context, dir, branch string, known *model.PullRequest) (f forge.Forge, url string, ok bool) {
	remote, _ := pushRemote(ctx, dir, branch)
	if remote == "" {
		return nil, "", false
	}
	url = gitx.RemoteURL(ctx, dir, remote)
	if f = forge.For(url, forgeRunner); f == nil {
		return nil, "", false
	}
	if def := gitx.RemoteDefaultBranch(ctx, dir, remote); branch == def || def == "" && (branch == "main" || branch == "master") {
		return nil, "", false
	}
	if (known == nil || known.Head != branch) && !gitx.RemoteBranchExists(ctx, dir, remote, branch) {
		return nil, "", false
	}
	return f, url, true
}

// forgeRunner runs a forge's tool as commits run gh: without a terminal,
// and with nothing that asks.
func forgeRunner(ctx context.Context, dir, name string, args ...string) (string, error) {
	return runCommand(ctx, dir, "", nil, name, args...)
}

// lookUpPullRequest asks f for branch's pull request; ok is false when it
// couldn't tell.
func (o *Orchestrator) lookUpPullRequest(ctx context.Context, f forge.Forge, dir, branch string) (*model.PullRequest, bool) {
	o.prs.mu.Lock()
	failed := time.Since(o.prs.failed[f.ID()]) < prRetry
	o.prs.mu.Unlock()
	if failed {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, prTimeout)
	defer cancel()
	pr, err := f.PullRequest(ctx, dir, branch)
	if err != nil {
		if errors.Is(err, forge.ErrUnavailable) || errors.Is(err, forge.ErrSignedOut) {
			o.prs.mu.Lock()
			o.prs.failed[f.ID()] = time.Now()
			o.prs.mu.Unlock()
		}
		o.log.Debug("looking up a pull request failed", "forge", f.ID(), "dir", dir, "branch", branch, "err", err)
		return nil, false
	}
	return pr, true
}

func samePullRequest(a, b *model.PullRequest) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// setPullRequest keeps pr as the pull request from the session's branch,
// unless the branch has changed since rad looked. It keeps the session's
// place in the lists.
func (a *actor) setPullRequest(ctx context.Context, branch string, pr *model.PullRequest) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.deleted || a.sess.Archived || a.sess.Workspace.Branch != branch || samePullRequest(a.sess.PullRequest, pr) {
		return
	}
	a.sess.PullRequest = pr
	a.write(ctx, func(w *store.W) error {
		cp := *a.sess
		return w.PutSessionKeepingTime(&cp)
	})
}
