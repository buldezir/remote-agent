package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"remote-agent/internal/gitx"
	"remote-agent/internal/harness"
	"remote-agent/internal/model"
	"remote-agent/internal/store"
)

const flushInterval = 100 * time.Millisecond

type queuedPrompt struct {
	item *model.Item
	text string
}

type pendingApproval struct {
	adapterID string
	gen       int
}

type actor struct {
	o       *Orchestrator
	mu      sync.Mutex
	sess    *model.Session
	project *model.Project

	rt    harness.Runtime
	rtGen int

	turn      *model.Turn
	lastTurnN int
	queue     []queuedPrompt
	note      string // prepended to the next prompt (e.g. after a revert)

	keyToID   map[string]string          // adapter item key -> item id (per runtime)
	items     map[string]*model.Item     // live items of the current turn, by id
	flushed   map[string]bool            // ids persisted at least once
	approvals map[string]pendingApproval // approval item id -> adapter id
	dirty     []string
	timer     *time.Timer

	lastActive time.Time
}

func newActor(o *Orchestrator, s *model.Session, p *model.Project) *actor {
	return &actor{
		o: o, sess: s, project: p, lastActive: time.Now(),
		keyToID: map[string]string{}, items: map[string]*model.Item{}, flushed: map[string]bool{},
		approvals: map[string]pendingApproval{},
	}
}

func (a *actor) write(ctx context.Context, f func(w *store.W) error) error {
	_, err := a.o.st.Write(ctx, func(w *store.W) error {
		if err := f(w); err != nil {
			return err
		}
		return w.SetItemCount(a.sess.ID, a.sess.ItemCount)
	})
	if err != nil {
		a.o.log.Error("persist failed", "session", a.sess.ID, "err", err)
	}
	return err
}

func (a *actor) newItem(kind model.ItemKind, status model.ItemStatus) *model.Item {
	a.sess.ItemCount++
	it := &model.Item{ID: store.NewID(), SessionID: a.sess.ID, Order: a.sess.ItemCount, Kind: kind, Status: status}
	if a.turn != nil {
		it.TurnID = a.turn.ID
	}
	return it
}

func (a *actor) putItem(w *store.W, it *model.Item) error {
	a.flushed[it.ID] = true
	cp := *it
	return w.PutItem(&cp)
}

func (a *actor) putSession(w *store.W) error {
	cp := *a.sess
	return w.PutSession(&cp)
}

// prompt records the user message and starts a turn, or queues it.
func (a *actor) prompt(ctx context.Context, text, commandID string) (*model.Item, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sess.Archived {
		return nil, errorf(CodeConflict, "session is archived")
	}
	if res, ok, err := a.o.receipt(ctx, commandID); err != nil {
		return nil, err
	} else if ok {
		var prev model.Item
		return &prev, json.Unmarshal(res, &prev)
	}
	it := a.newItem(model.ItemUserMessage, model.ItemQueued)
	it.TurnID = "" // set when its turn starts
	it.Text = text
	retitle := a.sess.Title == "" || a.sess.Title == "New session"
	if retitle {
		a.sess.Title = titleFrom(text)
	}
	if err := a.write(ctx, func(w *store.W) error {
		if err := a.putItem(w, it); err != nil {
			return err
		}
		if retitle {
			if err := a.putSession(w); err != nil {
				return err
			}
		}
		return w.PutReceipt(commandID, it)
	}); err != nil {
		return nil, err
	}
	a.lastActive = time.Now()
	if a.turn != nil {
		a.queue = append(a.queue, queuedPrompt{it, text})
		cp := *it // the queued item is updated when its turn starts
		return &cp, nil
	}
	return it, a.startTurn(ctx, queuedPrompt{it, text})
}

func (a *actor) startTurn(ctx context.Context, q queuedPrompt) error {
	if a.rt == nil {
		if err := a.open(ctx); err != nil {
			q.item.Status = model.ItemFailed
			e := a.newItem(model.ItemError, model.ItemCompleted)
			e.Text = "Could not start " + a.sess.Harness + ": " + err.Error()
			a.sess.Status, a.sess.Error = model.SessionError, err.Error()
			a.write(ctx, func(w *store.W) error {
				if err := a.putItem(w, q.item); err != nil {
					return err
				}
				if err := a.putItem(w, e); err != nil {
					return err
				}
				return a.putSession(w)
			})
			a.failQueue(ctx)
			return err
		}
	}
	n := a.lastTurnN + 1
	turn := &model.Turn{ID: store.NewID(), SessionID: a.sess.ID, N: n, Status: model.TurnRunning, StartedAt: time.Now().UTC()}
	turn.CheckpointBefore = a.checkpoint(ctx, n, "before")
	a.refreshBranch(ctx)
	a.turn, a.lastTurnN = turn, n
	a.items = map[string]*model.Item{}
	q.item.TurnID, q.item.Status = turn.ID, model.ItemCompleted
	if q.item.Order < a.sess.ItemCount {
		// It waited in the queue: move it below the previous turn's output.
		a.sess.ItemCount++
		q.item.Order = a.sess.ItemCount
	}
	a.sess.Status, a.sess.Error = model.SessionRunning, ""
	if err := a.write(ctx, func(w *store.W) error {
		if err := w.PutTurn(turn); err != nil {
			return err
		}
		if err := a.putItem(w, q.item); err != nil {
			return err
		}
		return a.putSession(w)
	}); err != nil {
		return err
	}
	text := q.text
	if a.note != "" {
		text = a.note + "\n\n" + text
		a.note = ""
	}
	if err := a.rt.Prompt(ctx, text); err != nil {
		a.endTurn(ctx, harness.TurnEnded{Status: model.TurnFailed, Error: err.Error()})
		return err
	}
	return nil
}

func (a *actor) open(ctx context.Context) error {
	h, ok := a.o.reg.Get(a.sess.Harness)
	if !ok {
		return fmt.Errorf("harness %q is not configured", a.sess.Harness)
	}
	opts := harness.OpenOptions{
		SessionID: a.sess.ID, Cwd: a.sess.Workspace.Path, Model: a.sess.Model, Mode: a.sess.Mode,
		ResumeID: a.sess.NativeID, Log: a.o.log.With("session", a.sess.ID, "harness", a.sess.Harness),
	}
	if a.o.opt.LogsDir != "" {
		opts.DiagPath = filepath.Join(a.o.opt.LogsDir, a.sess.ID+".ndjson")
	}
	rt, err := h.Open(ctx, opts)
	if err != nil && opts.ResumeID != "" {
		a.o.log.Warn("resume failed; starting a fresh conversation", "session", a.sess.ID, "err", err)
		opts.ResumeID = ""
		rt, err = h.Open(ctx, opts)
		if err == nil {
			n := a.newItem(model.ItemNotice, model.ItemCompleted)
			n.Text = "Could not resume the previous conversation; started a new one."
			a.write(ctx, func(w *store.W) error { return a.putItem(w, n) })
		}
	}
	if err != nil {
		return err
	}
	a.rtGen++
	a.rt = rt
	a.keyToID = map[string]string{}
	go a.loop(a.rtGen, rt)
	if id := rt.NativeID(); id != "" && id != a.sess.NativeID {
		a.sess.NativeID = id
		a.write(ctx, a.putSession)
	}
	return nil
}

// loop consumes runtime events until the runtime closes its channel.
func (a *actor) loop(gen int, rt harness.Runtime) {
	exited := false
	for ev := range rt.Events() {
		a.mu.Lock()
		if gen == a.rtGen && a.rt != nil {
			a.handle(gen, ev)
		}
		if _, ok := ev.(harness.Exited); ok {
			exited = true
		}
		a.mu.Unlock()
	}
	if !exited {
		a.mu.Lock()
		if gen == a.rtGen && a.rt != nil {
			a.handle(gen, harness.Exited{Err: fmt.Errorf("event stream closed")})
		}
		a.mu.Unlock()
	}
}

func (a *actor) handle(gen int, ev harness.Event) {
	ctx := context.Background()
	a.lastActive = time.Now()
	switch ev := ev.(type) {
	case harness.ItemEvent:
		a.onItem(ctx, ev.Item)
	case harness.ApprovalEvent:
		a.onApproval(ctx, gen, ev)
	case harness.ApprovalCancelled:
		a.onApprovalCancelled(ctx, ev.ID)
	case harness.TurnEnded:
		a.endTurn(ctx, ev)
	case harness.NativeID:
		if ev.ID != "" && ev.ID != a.sess.NativeID {
			a.sess.NativeID = ev.ID
			a.write(ctx, a.putSession)
		}
	case harness.ModeChanged:
		if ev.Mode != a.sess.Mode {
			a.sess.Mode = ev.Mode
			a.write(ctx, a.putSession)
		}
	case harness.ContextUsage:
		c := model.ContextUsage{Used: ev.Used, Window: ev.Window}
		if c.Window == 0 && a.sess.Context != nil {
			c.Window = a.sess.Context.Window
		}
		if a.sess.Context == nil || *a.sess.Context != c {
			a.sess.Context = &c
			a.write(ctx, a.putSession)
		}
	case harness.Exited:
		a.onExit(ctx, ev.Err)
	}
}

func (a *actor) onItem(ctx context.Context, in model.Item) {
	id, known := a.keyToID[in.ID]
	var it *model.Item
	if known {
		it = a.items[id]
	}
	if it == nil {
		it = a.newItem(in.Kind, in.Status)
		if known {
			it.ID = id // update for an item from an earlier turn
		}
		a.keyToID[in.ID] = it.ID
		a.items[it.ID] = it
	}
	it.Kind, it.Status, it.Text, it.Tool, it.Plan = in.Kind, in.Status, in.Text, in.Tool, in.Plan
	if in.ParentItemID != "" {
		it.ParentItemID = a.keyToID[in.ParentItemID]
	}
	if it.Tool != nil && len(it.Tool.Output) > model.MaxToolOutput {
		t := *it.Tool
		t.Output = "…\n" + t.Output[len(t.Output)-model.MaxToolOutput:]
		it.Tool = &t
	}
	if a.flushed[it.ID] && it.Status == model.ItemInProgress {
		a.markDirty(it.ID)
		return
	}
	a.flush(ctx, it.ID)
}

func (a *actor) markDirty(id string) {
	if !slices.Contains(a.dirty, id) {
		a.dirty = append(a.dirty, id)
	}
	if a.timer == nil {
		a.timer = time.AfterFunc(flushInterval, func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.timer = nil
			a.flush(context.Background())
		})
	}
}

// flush persists dirty items followed by extra ids, in that order.
func (a *actor) flush(ctx context.Context, extra ...string) {
	ids := append(a.dirty, extra...)
	a.dirty = nil
	if a.timer != nil {
		a.timer.Stop()
		a.timer = nil
	}
	if len(ids) == 0 {
		return
	}
	a.write(ctx, func(w *store.W) error {
		seen := map[string]bool{}
		for _, id := range ids {
			if it := a.items[id]; it != nil && !seen[id] {
				seen[id] = true
				if err := a.putItem(w, it); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (a *actor) onApproval(ctx context.Context, gen int, ev harness.ApprovalEvent) {
	ap := ev.Approval
	if ap.ToolItemID != "" {
		ap.ToolItemID = a.keyToID[ap.ToolItemID]
	}
	it := a.newItem(model.ItemApproval, model.ItemPending)
	it.Approval = &ap
	a.items[it.ID] = it
	a.approvals[it.ID] = pendingApproval{adapterID: ev.ID, gen: gen}
	a.sess.Status = model.SessionAwaitingApproval
	ids := append(a.dirty, it.ID)
	a.dirty = nil
	a.write(ctx, func(w *store.W) error {
		for _, id := range ids {
			if x := a.items[id]; x != nil {
				if err := a.putItem(w, x); err != nil {
					return err
				}
			}
		}
		return a.putSession(w)
	})
}

func (a *actor) approvalItemByAdapterID(adapterID string) string {
	for id, p := range a.approvals {
		if p.adapterID == adapterID && p.gen == a.rtGen {
			return id
		}
	}
	return ""
}

func (a *actor) onApprovalCancelled(ctx context.Context, adapterID string) {
	id := a.approvalItemByAdapterID(adapterID)
	if id == "" {
		return
	}
	delete(a.approvals, id)
	it := a.items[id]
	if it == nil {
		return
	}
	it.Status = model.ItemCancelled
	a.syncStatus()
	a.write(ctx, func(w *store.W) error {
		if err := a.putItem(w, it); err != nil {
			return err
		}
		return a.putSession(w)
	})
}

// syncStatus derives the session status from the current turn and approvals.
func (a *actor) syncStatus() {
	switch {
	case len(a.approvals) > 0:
		a.sess.Status = model.SessionAwaitingApproval
	case a.turn != nil:
		a.sess.Status = model.SessionRunning
	case a.sess.Status != model.SessionError:
		a.sess.Status = model.SessionIdle
	}
}

func (a *actor) respond(ctx context.Context, approvalID string, r harness.Response, commandID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if res, ok, err := a.o.receipt(ctx, commandID); err != nil {
		return err
	} else if ok && res != nil {
		return nil
	}
	p, ok := a.approvals[approvalID]
	it := a.items[approvalID]
	if !ok || it == nil || a.rt == nil || p.gen != a.rtGen {
		return errorf(CodeConflict, "approval is no longer pending")
	}
	valid := false
	for _, opt := range it.Approval.Options {
		if opt.ID == r.OptionID {
			valid = true
		}
	}
	if !valid {
		return errorf(CodeInvalid, "unknown option %q", r.OptionID)
	}
	if err := a.rt.Respond(ctx, p.adapterID, r); err != nil {
		return err
	}
	delete(a.approvals, approvalID)
	ap := *it.Approval
	ap.Decision = &model.Decision{OptionID: r.OptionID, Message: r.Message, Answers: r.Answers, At: time.Now().UTC()}
	it.Approval = &ap
	it.Status = model.ItemResolved
	a.syncStatus()
	return a.write(ctx, func(w *store.W) error {
		if err := a.putItem(w, it); err != nil {
			return err
		}
		if err := a.putSession(w); err != nil {
			return err
		}
		return w.PutReceipt(commandID, map[string]bool{"ok": true})
	})
}

// endTurn finalizes the current turn and starts the next queued prompt.
func (a *actor) endTurn(ctx context.Context, ev harness.TurnEnded) {
	turn := a.turn
	if turn == nil {
		return
	}
	a.dirty = nil
	if a.timer != nil {
		a.timer.Stop()
		a.timer = nil
	}
	var changed []*model.Item
	for id, it := range a.items {
		switch {
		case it.Kind == model.ItemApproval && it.Status == model.ItemPending:
			it.Status = model.ItemCancelled
			delete(a.approvals, id)
		case it.Status == model.ItemInProgress || it.Status == model.ItemPending:
			it.Status = model.ItemCompleted
			if it.Kind == model.ItemToolCall && ev.Status != model.TurnCompleted {
				it.Status = model.ItemFailed
			}
		}
		changed = append(changed, it)
	}
	slices.SortFunc(changed, func(x, y *model.Item) int { return int(x.Order - y.Order) })
	if ev.Status == "" {
		ev.Status = model.TurnCompleted
	}
	now := time.Now().UTC()
	turn.Status, turn.Usage, turn.Error, turn.EndedAt = ev.Status, ev.Usage, ev.Error, &now
	turn.CheckpointAfter = a.checkpoint(ctx, turn.N, "after")
	a.refreshBranch(ctx)
	if ev.Error != "" {
		e := a.newItem(model.ItemError, model.ItemCompleted)
		e.Text = ev.Error
		changed = append(changed, e)
	}
	a.turn = nil
	a.lastActive = time.Now()
	a.syncStatus()
	a.write(ctx, func(w *store.W) error {
		for _, it := range changed {
			if err := a.putItem(w, it); err != nil {
				return err
			}
		}
		if err := w.PutTurn(turn); err != nil {
			return err
		}
		return a.putSession(w)
	})
	a.items = map[string]*model.Item{}
	if len(a.queue) > 0 && a.rt != nil {
		next := a.queue[0]
		a.queue = a.queue[1:]
		a.startTurn(ctx, next)
	}
}

func (a *actor) failQueue(ctx context.Context) {
	if len(a.queue) == 0 {
		return
	}
	q := a.queue
	a.queue = nil
	a.write(ctx, func(w *store.W) error {
		for _, p := range q {
			p.item.Status = model.ItemCancelled
			if err := a.putItem(w, p.item); err != nil {
				return err
			}
		}
		return nil
	})
}

func (a *actor) onExit(ctx context.Context, err error) {
	a.rt = nil
	a.rtGen++
	if a.turn != nil {
		msg := ""
		if err != nil {
			msg = "Agent exited: " + err.Error()
		}
		st := model.TurnInterrupted
		if err != nil {
			st = model.TurnFailed
		}
		a.endTurn(ctx, harness.TurnEnded{Status: st, Error: msg})
	}
	a.failQueue(ctx)
	if err != nil {
		a.sess.Status, a.sess.Error = model.SessionError, err.Error()
	} else {
		a.syncStatus()
	}
	a.write(ctx, a.putSession)
}

// detach takes the runtime away from the actor, finalizing state as if it
// exited. The caller must Close the returned runtime without holding a.mu.
func (a *actor) detach(reason string) harness.Runtime {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.detachLocked(reason)
}

func (a *actor) detachLocked(reason string) harness.Runtime {
	rt := a.rt
	if rt == nil {
		return nil
	}
	a.onExit(context.Background(), nil)
	if reason != "" {
		a.o.log.Info("runtime stopped", "session", a.sess.ID, "reason", reason)
	}
	return rt
}

func (a *actor) reapIfIdle(timeout time.Duration) {
	a.mu.Lock()
	var rt harness.Runtime
	if a.rt != nil && a.turn == nil && len(a.approvals) == 0 && time.Since(a.lastActive) > timeout {
		rt = a.detachLocked("idle")
	}
	a.mu.Unlock()
	if rt != nil {
		rt.Close()
	}
}

func (a *actor) interrupt(ctx context.Context, force bool) error {
	a.mu.Lock()
	a.failQueue(ctx)
	if a.turn == nil || a.rt == nil {
		a.mu.Unlock()
		return nil
	}
	if force {
		rt := a.detachLocked("force stop")
		a.mu.Unlock()
		go rt.Close()
		return nil
	}
	rt := a.rt
	a.mu.Unlock()
	return rt.Interrupt(ctx)
}

func (a *actor) setMode(ctx context.Context, mode string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.rt != nil {
		if err := a.rt.SetMode(ctx, mode); err != nil {
			return err
		}
	}
	a.sess.Mode = mode
	return a.write(ctx, a.putSession)
}

func (a *actor) archive(ctx context.Context, removeWorktree bool) error {
	a.mu.Lock()
	rt := a.detachLocked("archived")
	a.mu.Unlock()
	if rt != nil {
		rt.Close()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if removeWorktree && a.sess.Workspace.Kind == model.WorkspaceWorktree && a.project != nil {
		if err := gitx.RemoveWorktree(ctx, a.project.Path, a.sess.Workspace.Path); err != nil {
			a.o.log.Warn("remove worktree", "err", err)
		}
	}
	if a.project != nil && a.project.IsGitRepo {
		gitx.DeleteRefs(ctx, a.project.Path, "refs/ra/cp/"+a.sess.ID+"/")
	}
	a.sess.Archived = true
	a.sess.Status = model.SessionStopped
	return a.write(ctx, a.putSession)
}

// refreshBranch records the branch checked out in the workspace, which the
// agent (or the user) may have switched. The caller persists the session.
func (a *actor) refreshBranch(ctx context.Context) {
	if a.project == nil || !a.project.IsGitRepo {
		return
	}
	a.sess.Workspace.Branch = gitx.CurrentBranch(ctx, a.sess.Workspace.Path)
}

// checkpoint snapshots the workspace; it returns "" for non-git projects.
func (a *actor) checkpoint(ctx context.Context, n int, phase string) string {
	if a.project == nil || !a.project.IsGitRepo {
		return ""
	}
	sha, err := gitx.Checkpoint(ctx, a.sess.Workspace.Path, checkpointRef(a.sess.ID, n, phase),
		fmt.Sprintf("rad checkpoint %s turn %d %s", a.sess.ID, n, phase))
	if err != nil {
		a.o.log.Warn("checkpoint failed", "session", a.sess.ID, "err", err)
		return ""
	}
	return sha
}
