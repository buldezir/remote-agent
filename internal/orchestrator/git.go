package orchestrator

import (
	"context"
	"fmt"

	"remote-agent/internal/gitx"
	"remote-agent/internal/model"
	"remote-agent/internal/store"
)

const maxPatch = 512 << 10

type Diff struct {
	From      string          `json:"from"`
	To        string          `json:"to"`
	Files     []gitx.FileStat `json:"files"`
	Patch     string          `json:"patch"`
	Truncated bool            `json:"truncated"`
}

// liveCheckpoint snapshots the current workspace state for diffs against a running turn.
func (a *actor) liveCheckpoint(ctx context.Context) (string, error) {
	return gitx.Checkpoint(ctx, a.sess.Workspace.Path, fmt.Sprintf("refs/ra/cp/%s/live", a.sess.ID), "rad live checkpoint")
}

func (o *Orchestrator) gitActor(ctx context.Context, sessionID string) (*actor, error) {
	a, err := o.actor(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if a.project == nil || !a.project.IsGitRepo {
		return nil, errorf(CodeInvalid, "project is not a git repository")
	}
	return a, nil
}

func (o *Orchestrator) diff(ctx context.Context, a *actor, from, to string, paths []string) (*Diff, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if to == "" {
		var err error
		if to, err = a.liveCheckpoint(ctx); err != nil {
			return nil, err
		}
	}
	if from == "" {
		return nil, errorf(CodeInvalid, "no checkpoint available")
	}
	dir := a.sess.Workspace.Path
	files, err := gitx.DiffFiles(ctx, dir, from, to)
	if err != nil {
		return nil, err
	}
	patch, truncated, err := gitx.Patch(ctx, dir, from, to, paths, maxPatch)
	if err != nil {
		return nil, err
	}
	if files == nil {
		files = []gitx.FileStat{}
	}
	return &Diff{From: from, To: to, Files: files, Patch: patch, Truncated: truncated}, nil
}

// TurnDiff returns the changes made during one turn (live if still running).
func (o *Orchestrator) TurnDiff(ctx context.Context, turnID string, paths []string) (*Diff, error) {
	t, err := o.st.Turn(ctx, turnID)
	if err != nil {
		return nil, errorf(CodeNotFound, "turn not found")
	}
	a, err := o.gitActor(ctx, t.SessionID)
	if err != nil {
		return nil, err
	}
	return o.diff(ctx, a, t.CheckpointBefore, t.CheckpointAfter, paths)
}

// SessionDiff returns all changes since the session's first turn started.
func (o *Orchestrator) SessionDiff(ctx context.Context, sessionID string, paths []string) (*Diff, error) {
	a, err := o.gitActor(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	turns, err := o.st.Turns(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	from := ""
	for _, t := range turns {
		if t.CheckpointBefore != "" {
			from = t.CheckpointBefore
			break
		}
	}
	return o.diff(ctx, a, from, "", paths)
}

// Revert restores the workspace files to their state before turn n. Only
// worktree sessions can be reverted, so the user's main checkout is never touched.
func (o *Orchestrator) Revert(ctx context.Context, sessionID string, n int) ([]gitx.FileStat, error) {
	a, err := o.gitActor(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sess.Workspace.Kind != model.WorkspaceWorktree {
		return nil, errorf(CodeInvalid, "revert is only available for worktree sessions")
	}
	if a.turn != nil {
		return nil, errorf(CodeConflict, "stop the running turn before reverting")
	}
	turns, err := o.st.Turns(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	target := ""
	for _, t := range turns {
		if t.N == n {
			target = t.CheckpointBefore
		}
	}
	if target == "" {
		return nil, errorf(CodeNotFound, "no checkpoint for turn %d", n)
	}
	now, err := a.liveCheckpoint(ctx)
	if err != nil {
		return nil, err
	}
	files, err := gitx.Revert(ctx, a.sess.Workspace.Path, target, now)
	if err != nil {
		return nil, err
	}
	notice := a.newItem(model.ItemNotice, model.ItemCompleted)
	notice.Text = fmt.Sprintf("Reverted %d file(s) to their state before turn %d.", len(files), n)
	a.note = fmt.Sprintf("[Note from the user's tooling: the workspace files were reverted to their state before turn %d. "+
		"Changes made in turn %d and later are gone from disk; re-read files before editing.]", n, n)
	err = a.write(ctx, func(w *store.W) error { return a.putItem(w, notice) })
	return files, err
}
