package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"remote-agent/internal/gitx"
	"remote-agent/internal/harness"
	"remote-agent/internal/model"
	"remote-agent/internal/store"
)

const flushInterval = 100 * time.Millisecond

type queuedPrompt struct {
	item     *model.Item
	input    harness.Input
	queuedAt time.Time
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

	rt              harness.Runtime
	rtGen           int
	runtimeNativeID string
	runtimePrompted bool
	archiving       bool

	turn      *model.Turn
	lastTurnN int
	queue     []queuedPrompt
	holds     int    // sendNow calls interrupting the turn; the queue waits for them
	note      string // prepended to the next prompt (e.g. after a revert)

	keyToID   map[string]string          // adapter item key -> item id (per runtime)
	items     map[string]*model.Item     // live items of the current turn, by id
	flushed   map[string]bool            // ids persisted at least once
	approvals map[string]pendingApproval // approval item id -> adapter id
	linked    map[string]linkedImage     // image files replies linked to, by path
	dirty     []string
	timer     *time.Timer

	lastActive time.Time
	deleted    bool // the session is gone from the store; nothing more is written
}

func newActor(o *Orchestrator, s *model.Session, p *model.Project) *actor {
	return &actor{
		o: o, sess: s, project: p, lastActive: time.Now(),
		keyToID: map[string]string{}, items: map[string]*model.Item{}, flushed: map[string]bool{},
		approvals: map[string]pendingApproval{},
	}
}

func (a *actor) write(ctx context.Context, f func(w *store.W) error) error {
	if a.deleted {
		return errorf(CodeNotFound, "session %s not found", a.sess.ID)
	}
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
	err := w.PutSession(&cp)
	a.sess.UpdatedAt = cp.UpdatedAt // which markRead keeps
	return err
}

// prompt records the user message and starts a turn, or queues it.
func (a *actor) prompt(ctx context.Context, text string, refs []model.ImageRef, files []harness.Image, commandID string) (*model.Item, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.promptLocked(ctx, text, refs, files, commandID, false)
}

// sendDraft sends the session's draft as prompt does, and removes it.
func (a *actor) sendDraft(ctx context.Context, commandID string) (*model.Item, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var d model.Draft // none: a retry of a send that went, which its receipt answers
	if a.sess.Draft != nil {
		d = *a.sess.Draft
	}
	ids := make([]string, len(d.Images))
	for i, ref := range d.Images {
		ids[i] = ref.ID
	}
	_, files, err := a.o.images(ids)
	if err != nil {
		return nil, err
	}
	return a.promptLocked(ctx, d.Text, d.Images, files, commandID, true)
}

// promptLocked is prompt, with a.mu held. With draft, the prompt is the
// session's draft, which goes with the user message.
func (a *actor) promptLocked(ctx context.Context, text string, refs []model.ImageRef, files []harness.Image, commandID string, draft bool) (*model.Item, error) {
	if a.deleted {
		return nil, errorf(CodeNotFound, "session %s not found", a.sess.ID)
	}
	if a.sess.Archived || a.archiving {
		return nil, errorf(CodeConflict, "session is archived")
	}
	if res, ok, err := a.o.receipt(ctx, commandID); err != nil {
		return nil, err
	} else if ok {
		var prev model.Item
		return &prev, json.Unmarshal(res, &prev)
	}
	saved := a.sess.Draft
	if draft {
		if saved == nil {
			return nil, errorf(CodeConflict, "the session has no draft")
		}
		a.sess.Draft = nil
	}
	it := a.newItem(model.ItemUserMessage, model.ItemQueued)
	it.TurnID = "" // set when its turn starts
	it.Text, it.Images = text, refs
	retitle := a.sess.Title == "" || a.sess.Title == "New session"
	if retitle {
		a.sess.Title = titleFrom(text)
	}
	if err := a.write(ctx, func(w *store.W) error {
		if err := a.putItem(w, it); err != nil {
			return err
		}
		if retitle || draft {
			if err := a.putSession(w); err != nil {
				return err
			}
		}
		return w.PutReceipt(commandID, it)
	}); err != nil {
		a.sess.Draft = saved // still there to send again
		return nil, err
	}
	if retitle {
		ws := a.sess.Workspace
		a.o.nameSession(a.sess.ID, a.projectName(), text, true, ws.Kind == model.WorkspaceWorktree && isProvisional(ws.Branch, a.sess.ID))
	}
	a.lastActive = time.Now()
	attrs := []any{"chars", utf8.RuneCountInString(text), "lines", strings.Count(text, "\n") + 1}
	if len(refs) > 0 {
		attrs = append(attrs, "images", len(refs))
	}
	busy := a.turn != nil || a.holds > 0
	a.o.log.Info("prompt", a.logAttrs(append(attrs, "queued", busy, "device", clientOf(ctx))...)...)
	q := queuedPrompt{it, harness.Input{Text: text, Images: files}, time.Now()}
	if busy {
		a.queue = append(a.queue, q)
		cp := *it // the queued item is updated when its turn starts
		return &cp, nil
	}
	return it, a.startTurn(ctx, q)
}

func (a *actor) projectName() string {
	if a.project == nil {
		return ""
	}
	return a.project.Name
}

// logAttrs describes the session for the log, leaving out the conversation:
// no prompt text, and no title, which is made from the first prompt.
func (a *actor) logAttrs(extra ...any) []any {
	s := a.sess
	attrs := []any{"session", s.ID}
	if a.project != nil {
		attrs = append(attrs, "project", a.project.Name)
	}
	attrs = append(attrs, "harness", s.Harness)
	m, effort := s.Model, s.Effort
	if s.ModelInfo != nil {
		m, effort = s.ModelInfo.ID, s.ModelInfo.Effort
	}
	for _, kv := range [][2]string{{"model", m}, {"effort", effort}, {"mode", s.Mode}, {"branch", s.Workspace.Branch}} {
		if kv[1] != "" {
			attrs = append(attrs, kv[0], kv[1])
		}
	}
	if s.Workspace.Kind == model.WorkspaceWorktree {
		attrs = append(attrs, "worktree", true)
	}
	return append(attrs, extra...)
}

func (a *actor) startTurn(ctx context.Context, q queuedPrompt) error {
	if a.rt == nil {
		if err := a.open(ctx); err != nil {
			if harness.IsAuthError(err.Error()) {
				a.o.reg.SignedOut(a.sess.Harness, "Signed out: "+err.Error())
			}
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
	turn := a.newTurn(ctx)
	a.o.log.Info("turn started", "session", a.sess.ID, "turn", turn.N, "waited", time.Since(q.queuedAt).Round(time.Millisecond))
	q.item.TurnID, q.item.Status = turn.ID, model.ItemCompleted
	if q.item.Order < a.sess.ItemCount {
		// It waited in the queue: move it below the previous turn's output.
		a.sess.ItemCount++
		q.item.Order = a.sess.ItemCount
	}
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
	in := q.input
	// The note waits out a /compact for the next prompt: Codex compacts
	// without the prompt's text, and Claude and Pi take it as instructions
	// for the summary.
	if _, compact := in.Command(harness.Compact.Name); a.note != "" && !compact {
		if strings.HasPrefix(strings.TrimSpace(in.Text), "/") || strings.HasPrefix(strings.TrimSpace(in.Text), "$") {
			in.Text = strings.TrimSpace(in.Text) + "\n\n" + a.note
		} else {
			in.Text = strings.TrimSuffix(a.note+"\n\n"+in.Text, "\n\n")
		}
		a.note = ""
	}
	if err := a.rt.Prompt(ctx, in); err != nil {
		a.endTurn(ctx, harness.TurnEnded{Status: model.TurnFailed, Error: err.Error()})
		return err
	}
	a.runtimePrompted = true
	a.persistNativeID(ctx)
	return nil
}

// newTurn makes the next turn the current one, with a checkpoint of the
// files before it, and the session running.
func (a *actor) newTurn(ctx context.Context) *model.Turn {
	n := a.lastTurnN + 1
	turn := &model.Turn{ID: store.NewID(), SessionID: a.sess.ID, N: n, Status: model.TurnRunning, StartedAt: time.Now().UTC()}
	turn.CheckpointBefore = a.checkpoint(ctx, n, "before")
	a.refreshBranch(ctx)
	a.turn, a.lastTurnN = turn, n
	a.sess.Status, a.sess.Error = model.SessionRunning, ""
	return turn
}

// onTurnStarted records a turn the agent began on its own, with no prompt:
// its notice, if any, is the turn's first item.
func (a *actor) onTurnStarted(ctx context.Context, ev harness.TurnStarted) {
	if a.turn != nil {
		return
	}
	a.runtimePrompted = true
	a.persistNativeID(ctx)
	turn := a.newTurn(ctx)
	a.o.log.Info("turn started", "session", a.sess.ID, "turn", turn.N, "by", "agent")
	var notice *model.Item
	if ev.Notice != "" {
		notice = a.newItem(model.ItemNotice, model.ItemCompleted)
		notice.Text = ev.Notice
	}
	a.write(ctx, func(w *store.W) error {
		if err := w.PutTurn(turn); err != nil {
			return err
		}
		if notice != nil {
			if err := a.putItem(w, notice); err != nil {
				return err
			}
		}
		return a.putSession(w)
	})
}

func (a *actor) open(ctx context.Context) error {
	h, ok := a.o.reg.Get(a.sess.Harness)
	if !ok {
		return fmt.Errorf("harness %q is not configured", a.sess.Harness)
	}
	opts := a.openOptions()
	rt, err := h.Open(ctx, opts)
	if err != nil && opts.ResumeID != "" && ctx.Err() == nil {
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
	a.attachRuntime(rt)
	return nil
}

func (a *actor) openOptions() harness.OpenOptions {
	opts := harness.OpenOptions{
		SessionID: a.sess.ID, Cwd: a.sess.Workspace.Path, Model: a.sess.Model, Effort: a.sess.Effort, Mode: a.sess.Mode,
		ResumeID: a.sess.NativeID, Log: a.o.log.With("session", a.sess.ID, "harness", a.sess.Harness),
		Instructions: instructions,
	}
	if a.o.opt.Images != nil {
		opts.Images = a.o.opt.Images
	}
	if a.o.opt.LogsDir != "" {
		opts.DiagPath = filepath.Join(a.o.opt.LogsDir, a.sess.ID+".ndjson")
	}
	return opts
}

func (a *actor) attachRuntime(rt harness.Runtime) {
	a.rtGen++
	a.rt = rt
	a.runtimeNativeID = rt.NativeID()
	a.runtimePrompted = false
	a.keyToID = map[string]string{}
	go a.loop(a.rtGen, rt)
}

func (a *actor) persistNativeID(ctx context.Context) {
	if a.runtimePrompted && a.runtimeNativeID != "" && a.runtimeNativeID != a.sess.NativeID {
		a.sess.NativeID = a.runtimeNativeID
		a.write(ctx, a.putSession)
	}
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
	case harness.TurnStarted:
		a.onTurnStarted(ctx, ev)
	case harness.TurnEnded:
		a.endTurn(ctx, ev)
	case harness.NativeID:
		a.runtimeNativeID = ev.ID
		a.persistNativeID(ctx)
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
	case harness.ModelInfo:
		m := model.ModelInfo{ID: ev.ID, Name: ev.Name, Effort: ev.Effort}
		if a.sess.ModelInfo == nil || *a.sess.ModelInfo != m {
			a.sess.ModelInfo = &m
			a.write(ctx, a.putSession)
		}
	case harness.BackgroundTasks:
		if !slices.Equal(ev.Tasks, a.sess.BackgroundTasks) {
			a.sess.BackgroundTasks = ev.Tasks
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
	it.Kind, it.Status, it.Text, it.Tool, it.Plan, it.Images = in.Kind, in.Status, in.Text, in.Tool, in.Plan, in.Images
	if it.Kind == model.ItemAssistantMessage {
		var linked []model.ImageRef
		it.Text, linked = a.linkImages(it.Text)
		it.Images = append(slices.Clip(it.Images), linked...)
	}
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
	a.sess.Unread = true
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
	carried := map[string]*model.Item{}
	for id, it := range a.items {
		switch {
		case it.Kind == model.ItemApproval && it.Status == model.ItemPending:
			it.Status = model.ItemCancelled
			delete(a.approvals, id)
		case open(it) && a.goesOn(it):
			carried[id] = it
			continue
		case open(it):
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
	a.sess.Unread = true
	a.logTurnEnd(turn)
	switch {
	case ev.Status == model.TurnFailed && harness.IsAuthError(ev.Error):
		line, _, _ := strings.Cut(strings.TrimSpace(ev.Error), "\n")
		a.o.reg.SignedOut(a.sess.Harness, "Signed out: "+line)
	case ev.Status == model.TurnCompleted:
		a.o.reg.SignedIn(a.sess.Harness)
	}
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
	a.items = carried
	if a.rt != nil { // else the agent exited, which cancels the queue
		a.startNext(ctx)
	}
}

// startNext starts the first queued prompt, unless a turn is running or a
// sendNow is still interrupting one.
func (a *actor) startNext(ctx context.Context) {
	if len(a.queue) == 0 || a.turn != nil || a.holds > 0 {
		return
	}
	next := a.queue[0]
	a.queue = a.queue[1:]
	a.startTurn(ctx, next)
}

func open(it *model.Item) bool {
	return it.Status == model.ItemInProgress || it.Status == model.ItemPending
}

// goesOn reports whether an unfinished item outlives its turn: it is a
// background subagent's, under a tool call that has returned or that an
// earlier turn made.
func (a *actor) goesOn(it *model.Item) bool {
	if it.ParentItemID == "" {
		return false
	}
	parent := a.items[it.ParentItemID]
	return parent == nil || parent.Status == model.ItemCompleted
}

func (a *actor) logTurnEnd(t *model.Turn) {
	attrs := []any{"turn", t.N, "status", t.Status, "duration", t.EndedAt.Sub(t.StartedAt).Round(time.Millisecond)}
	if c := a.sess.Context; c != nil && c.Window > 0 {
		attrs = append(attrs, "context", fmt.Sprintf("%d%%", c.Used*100/c.Window))
	}
	if u := t.Usage; u != nil {
		attrs = append(attrs, "input_tokens", u.InputTokens+u.CacheReadTokens+u.CacheWriteTokens, "output_tokens", u.OutputTokens)
		if u.CostUSD > 0 {
			attrs = append(attrs, "cost_usd", fmt.Sprintf("%.4f", u.CostUSD))
		}
	}
	if t.Error != "" {
		attrs = append(attrs, "error", t.Error)
	}
	level := slog.LevelInfo
	if t.Status == model.TurnFailed {
		level = slog.LevelWarn
	}
	a.o.log.Log(context.Background(), level, "turn ended", a.logAttrs(attrs...)...)
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
	a.sess.BackgroundTasks = nil // they end with the agent's process
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
	// What background subagents left unfinished ends with the process.
	var left []*model.Item
	for _, it := range a.items {
		if open(it) {
			it.Status = model.ItemFailed
			left = append(left, it)
		}
	}
	a.items = map[string]*model.Item{}
	a.write(ctx, func(w *store.W) error {
		for _, it := range left {
			if err := a.putItem(w, it); err != nil {
				return err
			}
		}
		return a.putSession(w)
	})
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
	// Stopping the agent would stop its background tasks too, and it reports
	// on them when they end.
	if a.rt != nil && a.turn == nil && len(a.approvals) == 0 && len(a.sess.BackgroundTasks) == 0 && time.Since(a.lastActive) > timeout {
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

// sendNow moves the queued prompt itemID to the front of the queue and
// interrupts the running turn, so that the prompt starts next. Once the
// prompt has left the queue, it does nothing.
func (a *actor) sendNow(ctx context.Context, itemID string) error {
	a.mu.Lock()
	i := slices.IndexFunc(a.queue, func(q queuedPrompt) bool { return q.item.ID == itemID })
	if i < 0 || a.rt == nil {
		a.mu.Unlock()
		return nil
	}
	q := a.queue[i]
	a.queue = slices.Insert(slices.Delete(a.queue, i, i+1), 0, q)
	a.o.log.Info("send now", "session", a.sess.ID, "waited", time.Since(q.queuedAt).Round(time.Millisecond), "device", clientOf(ctx))
	// The turn may end on its own before the agent gets the interrupt. The
	// prompt then waits for the interrupt, which would otherwise stop it.
	a.holds++
	rt := a.rt
	a.mu.Unlock()
	err := rt.Interrupt(ctx)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.holds--
	// If the agent exited meanwhile, what is queued came after: it starts
	// the agent again, as it would have in an idle session.
	a.startNext(ctx)
	return err
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
	a.archiving = true
	rt := a.detachLocked("archived")
	a.mu.Unlock()
	if rt != nil {
		rt.Close()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dropWorkspace(ctx, removeWorktree)
	a.sess.Archived = true
	a.sess.Status = model.SessionStopped
	a.sess.Unread = false // and not for the turn the agent stopping ended
	return a.write(ctx, a.putSession)
}

// saveDraft keeps a prompt in the session to send later, or, with nothing
// in it, removes the draft. A draft names a session that has the default
// title, as a first prompt does. Removing the draft of a session that never
// had a prompt gives it the default title back, so that the next draft or
// prompt names it.
func (a *actor) saveDraft(ctx context.Context, text string, refs []model.ImageRef) (*model.Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.deleted {
		return nil, errorf(CodeNotFound, "session %s not found", a.sess.ID)
	}
	if a.sess.Archived || a.archiving {
		return nil, errorf(CodeConflict, "session is archived")
	}
	retitle := false
	if strings.TrimSpace(text) == "" && len(refs) == 0 {
		if a.sess.Draft == nil {
			cp := *a.sess
			return &cp, nil
		}
		a.sess.Draft = nil
		if a.sess.ItemCount == 0 {
			a.sess.Title = "New session"
		}
	} else {
		a.sess.Draft = &model.Draft{Text: text, Images: refs}
		retitle = a.sess.Title == "" || a.sess.Title == "New session"
		if retitle {
			a.sess.Title = titleFrom(text)
		}
	}
	if err := a.write(ctx, a.putSession); err != nil {
		return nil, err
	}
	if a.sess.Draft == nil {
		a.o.log.Info("draft removed", a.logAttrs("device", clientOf(ctx))...)
	} else {
		a.o.log.Info("draft saved", a.logAttrs("chars", utf8.RuneCountInString(text), "images", len(refs), "device", clientOf(ctx))...)
	}
	if retitle {
		ws := a.sess.Workspace
		a.o.nameSession(a.sess.ID, a.projectName(), text, true, ws.Kind == model.WorkspaceWorktree && isProvisional(ws.Branch, a.sess.ID))
	}
	cp := *a.sess
	return &cp, nil
}

// markRead records that a client showed the session. It keeps the session's
// place in the lists.
func (a *actor) markRead(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.sess.Unread {
		return nil
	}
	a.sess.Unread = false
	return a.write(ctx, func(w *store.W) error {
		cp := *a.sess
		return w.PutSessionKeepingTime(&cp)
	})
}

// delete stops the agent and removes the session, its transcript, its
// checkpoints and its diagnostic log; the worktree too when asked.
func (a *actor) delete(ctx context.Context, removeWorktree bool) error {
	a.mu.Lock()
	// From here prompts are refused, and nothing is written: not even what
	// stopping the agent would record.
	a.deleted = true
	rt := a.detachLocked("deleted")
	a.mu.Unlock()
	if rt != nil {
		rt.Close()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dropWorkspace(ctx, removeWorktree)
	if a.o.opt.LogsDir != "" {
		os.Remove(filepath.Join(a.o.opt.LogsDir, a.sess.ID+".ndjson"))
	}
	_, err := a.o.st.Write(ctx, func(w *store.W) error { return w.DeleteSession(a.sess.ID) })
	return err
}

// dropWorkspace deletes the session's checkpoints and, when asked, its worktree.
func (a *actor) dropWorkspace(ctx context.Context, removeWorktree bool) {
	if removeWorktree && a.sess.Workspace.Kind == model.WorkspaceWorktree && a.project != nil {
		if err := gitx.RemoveWorktree(ctx, a.project.Path, a.sess.Workspace.Path); err != nil {
			a.o.log.Warn("remove worktree", "err", err)
		}
	}
	if a.project != nil && a.project.IsGitRepo {
		gitx.DeleteRefs(ctx, a.project.Path, "refs/ra/cp/"+a.sess.ID+"/")
	}
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
