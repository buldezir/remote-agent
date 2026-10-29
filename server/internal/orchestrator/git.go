package orchestrator

import (
	"context"
	"fmt"
	"os"
	"time"

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

// Workdir is the folder the agent works in: the workspace, or the folder it
// moved to, such as a worktree it made itself, while that folder exists.
func Workdir(ws model.Workspace) string {
	if ws.AgentPath != "" {
		if fi, err := os.Stat(ws.AgentPath); err == nil && fi.IsDir() {
			return ws.AgentPath
		}
	}
	return ws.Path
}

// sameDir reports whether a and b name the same folder, through symbolic
// links too, such as /tmp and /private/tmp on macOS.
func sameDir(a, b string) bool {
	if a == b {
		return true
	}
	fa, err1 := os.Stat(a)
	fb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(fa, fb)
}

// movedRef holds the checkpoint taken when the agent last moved to a folder
// of its own, from which the session's changes there are counted.
func movedRef(sessionID string) string { return fmt.Sprintf("refs/ra/cp/%s/moved", sessionID) }

// liveCheckpoint snapshots the current workspace state for diffs against a running turn.
func (a *actor) liveCheckpoint(ctx context.Context) (string, error) {
	return gitx.Checkpoint(ctx, a.dir(), fmt.Sprintf("refs/ra/cp/%s/live", a.sess.ID), "rad live checkpoint")
}

// branchPoll is how often Run looks for branches switched in the sessions'
// workspaces.
const branchPoll = 2 * time.Second

// head is what the last poll saw of a workspace's HEAD.
type head struct {
	files    []string // gitx.HeadFiles; none when git found no repo
	contents string   // of the files
	branch   string   // checked out when the files had those contents
	ok       bool     // the last poll read the files
}

// pollBranches records the branches switched in the sessions' workspaces
// since the last poll: turns record the branch only as they start and end,
// and it may be switched in a terminal, or by the agent, in between. It
// reads each workspace's HEAD files, and runs git only where they changed.
func (o *Orchestrator) pollBranches(ctx context.Context, heads map[string]*head) {
	wss, err := o.st.GitWorkspaces(ctx)
	if err != nil {
		o.log.Warn("listing workspaces failed", "err", err)
		return
	}
	polled := map[string]bool{}
	for _, ws := range wss {
		dir := Workdir(ws.Workspace)
		h := heads[dir]
		if h == nil {
			h = &head{}
			h.files, _ = gitx.HeadFiles(ctx, dir)
			heads[dir] = h
		}
		if !polled[dir] { // sessions in the project's root share it
			polled[dir] = true
			h.read(ctx, dir)
		}
		if !h.ok || h.branch == ws.Workspace.Branch {
			continue
		}
		if a, err := o.actor(ctx, ws.SessionID); err == nil {
			a.syncBranch(ctx)
		}
	}
	for path := range heads {
		if !polled[path] {
			delete(heads, path)
		}
	}
}

func (h *head) read(ctx context.Context, dir string) {
	var contents []byte
	for _, f := range h.files {
		data, _ := os.ReadFile(f) // tables.list exists only with reftable refs
		contents = append(contents, data...)
	}
	if h.ok = len(contents) > 0; h.ok && string(contents) != h.contents {
		h.contents, h.branch = string(contents), gitx.CurrentBranch(ctx, dir)
	}
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
	dir := a.dir()
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

// SessionDiff returns all changes since the session's first turn started,
// or, while the agent works in a folder of its own, since it moved there.
func (o *Orchestrator) SessionDiff(ctx context.Context, sessionID string, paths []string) (*Diff, error) {
	a, err := o.gitActor(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	moved := a.dir() != a.sess.Workspace.Path
	a.mu.Unlock()
	if moved {
		if from, err := gitx.Resolve(ctx, a.project.Path, movedRef(sessionID)); err == nil {
			return o.diff(ctx, a, from, "", paths)
		}
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
	files, err := gitx.Revert(ctx, a.dir(), target, now)
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
