package orchestrator

import (
	"context"

	"remote-agent/internal/gitx"
	"remote-agent/internal/model"
	"remote-agent/internal/store"
)

// RefreshGit notices that p, added before its folder was a git repository,
// has become one (`git init` after project.add): it saves the project, which
// tells the clients, and its sessions' actors start checkpointing, diffing
// and tracking the branch. It reports whether p is a repository now; the
// caller may set p.IsGitRepo from it when p is its own copy.
//
// A check runs git, a few milliseconds, so it happens only for projects that
// aren't repositories yet, and only where a client or a turn is about to
// need the answer, never on every read. A repository that stops being one
// is left as it was.
func (o *Orchestrator) RefreshGit(ctx context.Context, p *model.Project) bool {
	if p == nil {
		return false
	}
	if p.IsGitRepo {
		return true
	}
	if !gitx.IsRepo(ctx, p.Path) {
		return false
	}
	var marked bool
	if _, err := o.st.Write(ctx, func(w *store.W) (err error) {
		marked, err = w.MarkGitRepo(p.ID)
		return err
	}); err != nil {
		o.log.Warn("mark git repository", "project", p.Name, "err", err)
		return false
	}
	if marked {
		o.log.Info("project became a git repository", "project", p.Name)
	}
	o.mu.Lock()
	for _, a := range o.actors {
		if a.project != nil && a.project.ID == p.ID {
			a.git.Store(true)
		}
	}
	o.mu.Unlock()
	return true
}

// RefreshProjects checks every project that isn't a git repository yet, as
// RefreshGit does; a client that connects calls it. Calls while one runs
// return at once: the running one finds what they would.
func (o *Orchestrator) RefreshProjects(ctx context.Context) {
	if !o.gitScan.TryLock() {
		return
	}
	defer o.gitScan.Unlock()
	ps, err := o.st.Projects(ctx)
	if err != nil {
		return
	}
	for _, p := range ps {
		if !p.IsGitRepo {
			o.RefreshGit(ctx, p)
		}
	}
}

// LookAtProject checks the project of a session a client opens, in the
// background, as RefreshGit does.
func (o *Orchestrator) LookAtProject(ctx context.Context, sessionID string) {
	go func() {
		s, err := o.st.Session(ctx, sessionID)
		if err != nil {
			return
		}
		if p, err := o.st.Project(ctx, s.ProjectID); err == nil {
			o.RefreshGit(ctx, p)
		}
	}()
}
