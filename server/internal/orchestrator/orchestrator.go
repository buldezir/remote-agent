// Package orchestrator owns session lifecycles: it turns client commands into
// harness calls, and harness events into persisted, published state.
//
// Each session with activity has an actor guarded by its own mutex. Runtime
// methods are never called in a way that waits on event consumption, and
// runtimes are closed outside the actor lock, so event loops cannot deadlock.
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"remote-agent/internal/gitx"
	"remote-agent/internal/harness"
	"remote-agent/internal/images"
	"remote-agent/internal/model"
	"remote-agent/internal/store"
)

type Options struct {
	WorktreesDir string
	LogsDir      string
	Images       *images.Store // nil: prompts can't carry images, and agents' images are dropped
	IdleTimeout  time.Duration
	Log          *slog.Logger
}

type Orchestrator struct {
	st  *store.Store
	reg *harness.Registry
	opt Options
	log *slog.Logger

	mu       sync.Mutex
	actors   map[string]*actor
	inflight map[string]*sync.Mutex
}

func New(st *store.Store, reg *harness.Registry, opt Options) *Orchestrator {
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	if opt.IdleTimeout == 0 {
		opt.IdleTimeout = 30 * time.Minute
	}
	return &Orchestrator{st: st, reg: reg, opt: opt, log: opt.Log, actors: map[string]*actor{}, inflight: map[string]*sync.Mutex{}}
}

func (o *Orchestrator) Registry() *harness.Registry { return o.reg }

type clientKey struct{}

// WithClient tags ctx with the name of the device a command came from, for the log.
func WithClient(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, clientKey{}, name)
}

func clientOf(ctx context.Context) string {
	name, _ := ctx.Value(clientKey{}).(string)
	return name
}

// lockCommand serializes concurrent retries of the same command id.
func (o *Orchestrator) lockCommand(id string) func() {
	if id == "" {
		return func() {}
	}
	o.mu.Lock()
	m := o.inflight[id]
	if m == nil {
		m = &sync.Mutex{}
		o.inflight[id] = m
	}
	o.mu.Unlock()
	m.Lock()
	return func() {
		m.Unlock()
		o.mu.Lock()
		delete(o.inflight, id)
		o.mu.Unlock()
	}
}

// receipt returns the stored result for an already-applied command.
func (o *Orchestrator) receipt(ctx context.Context, commandID string) (json.RawMessage, bool, error) {
	var res json.RawMessage
	var ok bool
	_, err := o.st.Write(ctx, func(w *store.W) error {
		var err error
		res, ok, err = w.Receipt(commandID)
		return err
	})
	return res, ok, err
}

// Projects.

func (o *Orchestrator) AddProject(ctx context.Context, path string) (*model.Project, error) {
	if p, err := o.st.ProjectByPath(ctx, path); err == nil {
		return p, nil
	}
	fi, err := os.Stat(path)
	if err != nil || !fi.IsDir() {
		return nil, errorf(CodeInvalid, "%s is not a directory", path)
	}
	p := &model.Project{ID: store.NewID(), Path: path, Name: filepath.Base(path), IsGitRepo: gitx.IsRepo(ctx, path), CreatedAt: time.Now().UTC()}
	_, err = o.st.Write(ctx, func(w *store.W) error { return w.PutProject(p) })
	return p, err
}

func (o *Orchestrator) RemoveProject(ctx context.Context, id string) error {
	sessions, err := o.st.Sessions(ctx)
	if err != nil {
		return err
	}
	for _, s := range sessions {
		if s.ProjectID == id && !s.Archived {
			return errorf(CodeConflict, "project has active sessions; archive them first")
		}
	}
	_, err = o.st.Write(ctx, func(w *store.W) error { return w.DeleteProject(id) })
	return err
}

// Sessions.

type WorkspaceParams struct {
	Kind    model.WorkspaceKind `json:"kind"`
	Branch  string              `json:"branch,omitempty"`
	BaseRef string              `json:"baseRef,omitempty"`
}

type CreateSessionParams struct {
	CommandID string          `json:"commandId"`
	ProjectID string          `json:"projectId"`
	Harness   string          `json:"harness"`
	Model     string          `json:"model,omitempty"`
	Effort    string          `json:"effort,omitempty"`
	Mode      string          `json:"mode,omitempty"`
	Workspace WorkspaceParams `json:"workspace"`
	Prompt    string          `json:"prompt"`
	Images    []string        `json:"images,omitempty"` // attached to the prompt
	Title     string          `json:"title,omitempty"`
}

func (o *Orchestrator) CreateSession(ctx context.Context, p CreateSessionParams) (*model.Session, error) {
	defer o.lockCommand(p.CommandID)()
	if res, ok, err := o.receipt(ctx, p.CommandID); err != nil {
		return nil, err
	} else if ok {
		var s model.Session
		return &s, json.Unmarshal(res, &s)
	}

	proj, err := o.st.Project(ctx, p.ProjectID)
	if err != nil {
		return nil, errorf(CodeNotFound, "project %s not found", p.ProjectID)
	}
	h, ok := o.reg.Get(p.Harness)
	if !ok {
		return nil, errorf(CodeInvalid, "unknown harness %q", p.Harness)
	}
	info := o.reg.Info(ctx, h, false)
	if !info.Installed {
		return nil, errorf(CodeUnavailable, "%s is not installed on this machine", info.Name)
	}
	mode := p.Mode
	if mode == "" {
		mode = info.DefaultMode
	}

	s := &model.Session{
		ID: store.NewID(), ProjectID: proj.ID, Harness: p.Harness, Model: p.Model, Effort: p.Effort, Mode: mode,
		Workspace: model.Workspace{Kind: model.WorkspaceRoot, Path: proj.Path},
		Status:    model.SessionIdle, Title: p.Title, CreatedAt: time.Now().UTC(),
	}
	if s.Title == "" {
		s.Title = titleFrom(p.Prompt)
	}
	if p.Workspace.Kind == model.WorkspaceWorktree {
		if !proj.IsGitRepo {
			return nil, errorf(CodeInvalid, "worktrees need a git repository")
		}
		branch := p.Workspace.Branch
		if branch == "" {
			branch = "ra/" + slug(s.Title) + "-" + s.ID[len(s.ID)-6:]
		}
		dir := filepath.Join(o.opt.WorktreesDir, proj.ID, s.ID)
		if err := gitx.AddWorktree(ctx, proj.Path, dir, branch, p.Workspace.BaseRef); err != nil {
			return nil, errorf(CodeInvalid, "create worktree: %v", err)
		}
		s.Workspace = model.Workspace{Kind: model.WorkspaceWorktree, Path: dir, Branch: branch, BaseRef: p.Workspace.BaseRef}
	} else if proj.IsGitRepo {
		s.Workspace.Branch = gitx.CurrentBranch(ctx, proj.Path)
	}

	_, err = o.st.Write(ctx, func(w *store.W) error {
		if err := w.PutSession(s); err != nil {
			return err
		}
		return w.PutReceipt(p.CommandID, s)
	})
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Prompt) != "" || len(p.Images) > 0 {
		cmd := ""
		if p.CommandID != "" {
			cmd = p.CommandID + "/prompt"
		}
		if _, err := o.Prompt(ctx, s.ID, p.Prompt, p.Images, cmd); err != nil {
			o.log.Warn("initial prompt failed", "session", s.ID, "err", err)
		}
	}
	return s, nil
}

func (o *Orchestrator) actor(ctx context.Context, sessionID string) (*actor, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if a := o.actors[sessionID]; a != nil {
		return a, nil
	}
	s, err := o.st.Session(ctx, sessionID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errorf(CodeNotFound, "session %s not found", sessionID)
	} else if err != nil {
		return nil, err
	}
	turns, err := o.st.Turns(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	proj, _ := o.st.Project(ctx, s.ProjectID)
	a := newActor(o, s, proj)
	if len(turns) > 0 {
		a.lastTurnN = turns[len(turns)-1].N
	}
	o.actors[sessionID] = a
	return a, nil
}

// Prompt sends text and the images with the given ids (from POST
// /v1/images) to a session.
func (o *Orchestrator) Prompt(ctx context.Context, sessionID, text string, imageIDs []string, commandID string) (*model.Item, error) {
	if strings.TrimSpace(text) == "" && len(imageIDs) == 0 {
		return nil, errorf(CodeInvalid, "empty prompt")
	}
	var refs []model.ImageRef
	var files []harness.Image
	for _, id := range imageIDs {
		if o.opt.Images == nil {
			return nil, errorf(CodeInvalid, "this server does not keep images")
		}
		ref, path, err := o.opt.Images.Get(id)
		if err != nil {
			return nil, errorf(CodeInvalid, "unknown image %q", id)
		}
		refs = append(refs, ref)
		files = append(files, harness.Image{Path: path, MimeType: ref.MimeType})
	}
	a, err := o.actor(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return a.prompt(ctx, text, refs, files, commandID)
}

func (o *Orchestrator) Interrupt(ctx context.Context, sessionID string, force bool) error {
	a, err := o.actor(ctx, sessionID)
	if err != nil {
		return err
	}
	return a.interrupt(ctx, force)
}

func (o *Orchestrator) Respond(ctx context.Context, sessionID, approvalID string, r harness.Response, commandID string) error {
	a, err := o.actor(ctx, sessionID)
	if err != nil {
		return err
	}
	return a.respond(ctx, approvalID, r, commandID)
}

func (o *Orchestrator) SetMode(ctx context.Context, sessionID, mode string) error {
	a, err := o.actor(ctx, sessionID)
	if err != nil {
		return err
	}
	return a.setMode(ctx, mode)
}

func (o *Orchestrator) Archive(ctx context.Context, sessionID string, removeWorktree bool) error {
	a, err := o.actor(ctx, sessionID)
	if err != nil {
		return err
	}
	if err := a.archive(ctx, removeWorktree); err != nil {
		return err
	}
	o.mu.Lock()
	delete(o.actors, sessionID)
	o.mu.Unlock()
	return nil
}

// Recover fixes up state left behind by a previous process: running turns are
// interrupted, pending approvals expire, streaming items are finalized.
func (o *Orchestrator) Recover(ctx context.Context) error {
	sessions, err := o.st.Sessions(ctx)
	if err != nil {
		return err
	}
	pending, err := o.st.ItemsWithStatus(ctx, "", model.ItemPending)
	if err != nil {
		return err
	}
	inProgress, err := o.st.ItemsWithStatus(ctx, "", model.ItemInProgress)
	if err != nil {
		return err
	}
	queued, err := o.st.ItemsWithStatus(ctx, "", model.ItemQueued)
	if err != nil {
		return err
	}
	_, err = o.st.Write(ctx, func(w *store.W) error {
		for _, it := range pending {
			if it.Kind == model.ItemApproval {
				it.Status = model.ItemExpired
				if err := w.PutItem(it); err != nil {
					return err
				}
			} else {
				inProgress = append(inProgress, it) // e.g. a tool call awaiting approval
			}
		}
		for _, it := range inProgress {
			it.Status = model.ItemCompleted
			if it.Kind == model.ItemToolCall {
				it.Status = model.ItemFailed
			}
			if err := w.PutItem(it); err != nil {
				return err
			}
		}
		for _, it := range queued {
			it.Status = model.ItemCancelled
			if err := w.PutItem(it); err != nil {
				return err
			}
		}
		for _, s := range sessions {
			if s.Status != model.SessionRunning && s.Status != model.SessionAwaitingApproval {
				continue
			}
			turns, err := o.st.Turns(ctx, s.ID)
			if err != nil {
				return err
			}
			for _, t := range turns {
				if t.Status == model.TurnRunning {
					t.Status = model.TurnInterrupted
					now := w.Now()
					t.EndedAt = &now
					if err := w.PutTurn(t); err != nil {
						return err
					}
				}
			}
			s.ItemCount++
			if err := w.PutItem(&model.Item{ID: store.NewID(), SessionID: s.ID, Order: s.ItemCount, Kind: model.ItemNotice,
				Status: model.ItemCompleted, Text: "Server restarted while a turn was running; it was interrupted."}); err != nil {
				return err
			}
			if err := w.SetItemCount(s.ID, s.ItemCount); err != nil {
				return err
			}
			s.Status = model.SessionIdle
			if err := w.PutSession(s); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

// Run reaps idle runtimes until ctx is done.
func (o *Orchestrator) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			o.mu.Lock()
			actors := make([]*actor, 0, len(o.actors))
			for _, a := range o.actors {
				actors = append(actors, a)
			}
			o.mu.Unlock()
			for _, a := range actors {
				a.reapIfIdle(o.opt.IdleTimeout)
			}
		}
	}
}

// Shutdown stops every runtime.
func (o *Orchestrator) Shutdown() {
	o.mu.Lock()
	actors := make([]*actor, 0, len(o.actors))
	for _, a := range o.actors {
		actors = append(actors, a)
	}
	o.mu.Unlock()
	var wg sync.WaitGroup
	for _, a := range actors {
		if rt := a.detach("server shutting down"); rt != nil {
			wg.Add(1)
			go func() { defer wg.Done(); rt.Close() }()
		}
	}
	wg.Wait()
}

func titleFrom(prompt string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(prompt), "\n")
	if line == "" {
		return "New session"
	}
	if utf8.RuneCountInString(line) > 80 {
		r := []rune(line)
		line = string(r[:79]) + "…"
	}
	return line
}

func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 32 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "session"
	}
	return out
}

func checkpointRef(sessionID string, n int, phase string) string {
	return fmt.Sprintf("refs/ra/cp/%s/%d-%s", sessionID, n, phase)
}
