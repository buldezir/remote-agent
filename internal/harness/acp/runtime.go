package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"remote-agent/internal/harness"
	"remote-agent/internal/harness/jsonrpc"
	"remote-agent/internal/harness/proc"
	"remote-agent/internal/model"
)

type permRequest struct {
	id      json.RawMessage
	options map[string]bool
}

type runtime struct {
	h         *Harness
	p         *proc.Proc
	conn      *jsonrpc.Conn
	cwd       string
	sessionID string

	events  chan harness.Event
	emitMu  sync.Mutex
	closed  bool
	closing atomic.Bool
	loading atomic.Bool

	mu           sync.Mutex
	mode         string
	configs      map[string]string // category -> config option id
	inTurn       bool
	interrupting bool
	perms        map[string]*permRequest
	nextPerm     int
	cost         float64

	turnSeq atomic.Int64

	// Read-loop state.
	tools   map[string]*model.Item
	nextKey int
	plan    *model.Item

	// The assistant message or reasoning being streamed. The prompt goroutine
	// also completes it when the turn ends, hence the lock.
	curMu   sync.Mutex
	cur     *model.Item
	curTurn int64
}

func (r *runtime) NativeID() string             { return r.sessionID }
func (r *runtime) Events() <-chan harness.Event { return r.events }

func (r *runtime) emit(e harness.Event) {
	r.emitMu.Lock()
	defer r.emitMu.Unlock()
	if !r.closed {
		r.events <- e
	}
}

func (r *runtime) waitExit() {
	<-r.p.Done()
	<-r.conn.Done()
	var err error
	if !r.closing.Load() {
		if err = r.p.Err(); err == nil {
			err = fmt.Errorf("%s exited unexpectedly", r.h.agent.Command)
		}
	}
	r.emit(harness.Exited{Err: err})
	r.emitMu.Lock()
	r.closed = true
	close(r.events)
	r.emitMu.Unlock()
}

func (r *runtime) Close() error {
	r.closing.Store(true)
	r.p.Stop(3 * time.Second)
	return nil
}

func (r *runtime) Prompt(ctx context.Context, text string) error {
	r.mu.Lock()
	if r.inTurn {
		r.mu.Unlock()
		return fmt.Errorf("a turn is already running")
	}
	r.inTurn, r.interrupting, r.cost = true, false, 0
	r.mu.Unlock()
	r.turnSeq.Add(1)
	// session/prompt resolves only when the turn ends, so run it in the background.
	go func() {
		var res struct {
			StopReason string `json:"stopReason"`
			Usage      *struct {
				InputTokens       int64  `json:"inputTokens"`
				OutputTokens      int64  `json:"outputTokens"`
				CachedReadTokens  *int64 `json:"cachedReadTokens"`
				CachedWriteTokens *int64 `json:"cachedWriteTokens"`
			} `json:"usage"`
		}
		err := r.conn.Call(context.Background(), "session/prompt", map[string]any{
			"sessionId": r.sessionID,
			"prompt":    []map[string]any{{"type": "text", "text": text}},
		}, &res)
		r.mu.Lock()
		interrupted := r.interrupting
		r.inTurn, r.interrupting = false, false
		cost := r.cost
		r.mu.Unlock()
		r.finishStreaming()
		r.closeCur() // the last message of the turn is final now
		ev := harness.TurnEnded{Status: model.TurnCompleted}
		switch {
		case err == jsonrpc.ErrClosed:
			return // Exited follows
		case err != nil:
			ev.Status, ev.Error = model.TurnFailed, err.Error()
		case res.StopReason == "cancelled" || interrupted:
			ev.Status = model.TurnInterrupted
		case res.StopReason == "refusal":
			ev.Status, ev.Error = model.TurnFailed, "The agent refused to continue."
		case res.StopReason == "max_tokens" || res.StopReason == "max_turn_requests":
			ev.Error = "Stopped: " + strings.ReplaceAll(res.StopReason, "_", " ")
		}
		if u := res.Usage; u != nil {
			ev.Usage = &model.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CostUSD: cost}
			if u.CachedReadTokens != nil {
				ev.Usage.CacheReadTokens = *u.CachedReadTokens
			}
			if u.CachedWriteTokens != nil {
				ev.Usage.CacheWriteTokens = *u.CachedWriteTokens
			}
		} else if cost > 0 {
			ev.Usage = &model.Usage{CostUSD: cost}
		}
		r.emit(ev)
	}()
	return nil
}

// finishStreaming cancels outstanding permission requests.
func (r *runtime) finishStreaming() {
	r.mu.Lock()
	var pending []string
	for id, p := range r.perms {
		pending = append(pending, id)
		r.conn.Reply(p.id, map[string]any{"outcome": map[string]any{"outcome": "cancelled"}})
	}
	r.perms = map[string]*permRequest{}
	r.mu.Unlock()
	for _, id := range pending {
		r.emit(harness.ApprovalCancelled{ID: id})
	}
}

func (r *runtime) Interrupt(ctx context.Context) error {
	r.mu.Lock()
	if !r.inTurn {
		r.mu.Unlock()
		return nil
	}
	r.interrupting = true
	r.mu.Unlock()
	// The protocol requires pending permission requests to be answered "cancelled".
	r.finishStreaming()
	return r.conn.Notify("session/cancel", map[string]any{"sessionId": r.sessionID})
}

func (r *runtime) setConfig(ctx context.Context, category, value string) error {
	r.mu.Lock()
	id := r.configs[category]
	r.mu.Unlock()
	if id == "" {
		return harness.ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return r.conn.Call(ctx, "session/set_config_option", map[string]any{"sessionId": r.sessionID, "configId": id, "value": value}, nil)
}

func (r *runtime) SetMode(ctx context.Context, mode string) error {
	err := r.setConfig(ctx, "mode", mode)
	if err == harness.ErrUnsupported {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		err = r.conn.Call(cctx, "session/set_mode", map[string]any{"sessionId": r.sessionID, "modeId": mode}, nil)
	}
	if err != nil {
		return err
	}
	r.setMode(mode)
	return nil
}

func (r *runtime) setMode(mode string) {
	r.mu.Lock()
	changed := mode != "" && mode != r.mode
	r.mode = mode
	r.mu.Unlock()
	if changed {
		r.emit(harness.ModeChanged{Mode: mode})
	}
}

func (r *runtime) Respond(ctx context.Context, approvalID string, resp harness.Response) error {
	r.mu.Lock()
	p := r.perms[approvalID]
	delete(r.perms, approvalID)
	r.mu.Unlock()
	if p == nil {
		return fmt.Errorf("no pending approval %s", approvalID)
	}
	if !p.options[resp.OptionID] {
		return fmt.Errorf("unknown option %q", resp.OptionID)
	}
	return r.conn.Reply(p.id, map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": resp.OptionID}})
}

// session/update handling (read loop).

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolContent struct {
	Type       string        `json:"type"` // content | diff | terminal
	Content    *contentBlock `json:"content"`
	Path       string        `json:"path"`
	OldText    *string       `json:"oldText"`
	NewText    string        `json:"newText"`
	TerminalID string        `json:"terminalId"`
}

type update struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Content       json.RawMessage `json:"content"`
	MessageID     string          `json:"messageId"`

	ToolCallID string          `json:"toolCallId"`
	Title      *string         `json:"title"`
	Kind       *string         `json:"kind"`
	Status     *string         `json:"status"`
	RawInput   json.RawMessage `json:"rawInput"`
	RawOutput  json.RawMessage `json:"rawOutput"`
	Locations  []struct {
		Path string `json:"path"`
	} `json:"locations"`

	Entries []struct {
		Content string `json:"content"`
		Status  string `json:"status"`
	} `json:"entries"`

	CurrentModeID string         `json:"currentModeId"`
	ConfigOptions []configOption `json:"configOptions"`
	Cost          *struct {
		Amount float64 `json:"amount"`
	} `json:"cost"`
}

func (r *runtime) onNotify(method string, raw json.RawMessage) {
	if method != "session/update" || r.loading.Load() {
		return
	}
	var n struct {
		Update update `json:"update"`
	}
	if json.Unmarshal(raw, &n) != nil {
		return
	}
	u := n.Update
	switch u.SessionUpdate {
	case "agent_message_chunk", "agent_thought_chunk":
		var c contentBlock
		if json.Unmarshal(u.Content, &c) != nil || c.Type != "text" || c.Text == "" {
			return
		}
		kind := model.ItemAssistantMessage
		if u.SessionUpdate == "agent_thought_chunk" {
			kind = model.ItemReasoning
		}
		turn := r.turnSeq.Load()
		r.curMu.Lock()
		defer r.curMu.Unlock()
		if r.cur == nil || r.cur.Kind != kind || r.curTurn != turn || (u.MessageID != "" && !strings.HasSuffix(r.cur.ID, ":"+u.MessageID)) {
			r.closeCurLocked()
			r.curTurn = turn
			r.nextKey++
			key := fmt.Sprintf("m%d", r.nextKey)
			if u.MessageID != "" {
				key += ":" + u.MessageID
			}
			r.cur = &model.Item{ID: key, Kind: kind, Status: model.ItemInProgress}
		}
		r.cur.Text += c.Text
		r.emit(harness.ItemEvent{Item: *r.cur})
	case "tool_call", "tool_call_update":
		r.closeCur()
		r.onToolCall(u)
	case "plan":
		r.closeCur()
		if r.plan == nil {
			r.nextKey++
			r.plan = &model.Item{ID: fmt.Sprintf("plan%d", r.nextKey), Kind: model.ItemPlan, Status: model.ItemCompleted}
		}
		p := &model.Plan{}
		for _, e := range u.Entries {
			p.Entries = append(p.Entries, model.PlanEntry{Content: e.Content, Status: e.Status})
		}
		r.plan.Plan = p
		r.emit(harness.ItemEvent{Item: *r.plan})
	case "current_mode_update":
		r.setMode(u.CurrentModeID)
	case "config_option_update":
		for _, c := range u.ConfigOptions {
			if v, ok := c.CurrentValue.(string); ok && c.Category == "mode" {
				r.setMode(v)
			}
		}
	case "usage_update":
		if u.Cost != nil {
			r.mu.Lock()
			r.cost = u.Cost.Amount
			r.mu.Unlock()
		}
	}
}

func (r *runtime) closeCur() {
	r.curMu.Lock()
	defer r.curMu.Unlock()
	r.closeCurLocked()
}

func (r *runtime) closeCurLocked() {
	if r.cur != nil {
		r.cur.Status = model.ItemCompleted
		r.emit(harness.ItemEvent{Item: *r.cur})
		r.cur = nil
	}
}

func toolKind(k string) model.ToolKind {
	switch k {
	case "read":
		return model.ToolRead
	case "edit", "delete", "move":
		return model.ToolEdit
	case "search":
		return model.ToolSearch
	case "execute":
		return model.ToolExecute
	case "think":
		return model.ToolThink
	case "fetch":
		return model.ToolFetch
	}
	return model.ToolOther
}

func toolStatus(s string) model.ItemStatus {
	switch s {
	case "completed":
		return model.ItemCompleted
	case "failed":
		return model.ItemFailed
	}
	return model.ItemInProgress
}

func (r *runtime) rel(p string) string {
	if rel, err := filepath.Rel(r.cwd, p); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return p
}

func (r *runtime) toolItem(u update) *model.Item {
	it := r.tools[u.ToolCallID]
	if it == nil {
		it = &model.Item{ID: u.ToolCallID, Kind: model.ItemToolCall, Status: model.ItemInProgress, Tool: &model.ToolCall{Kind: model.ToolOther}}
		r.tools[u.ToolCallID] = it
	}
	t := it.Tool
	if u.Title != nil && *u.Title != "" {
		title := strings.ReplaceAll(*u.Title, "`", "")
		for _, l := range u.Locations {
			title = strings.ReplaceAll(title, l.Path, r.rel(l.Path))
		}
		if t.Name == "" {
			t.Name = *u.Title
		}
		t.Title = title
	}
	if u.Kind != nil {
		t.Kind = toolKind(*u.Kind)
	}
	if u.Status != nil {
		it.Status = toolStatus(*u.Status)
	}
	if len(u.RawInput) > 0 && string(u.RawInput) != "{}" && string(u.RawInput) != "null" {
		t.Input = u.RawInput
	}
	if len(u.Locations) > 0 {
		t.Paths = nil
		for _, l := range u.Locations {
			t.Paths = append(t.Paths, r.rel(l.Path))
		}
	}
	if len(u.Content) > 0 {
		var cs []toolContent
		json.Unmarshal(u.Content, &cs)
		var out strings.Builder
		for _, c := range cs {
			switch c.Type {
			case "content":
				if c.Content != nil && c.Content.Type == "text" {
					out.WriteString(c.Content.Text)
					out.WriteByte('\n')
				}
			case "diff":
				fmt.Fprintf(&out, "Edited %s\n", r.rel(c.Path))
				if c.Path != "" && !contains(t.Paths, r.rel(c.Path)) {
					t.Paths = append(t.Paths, r.rel(c.Path))
				}
			}
		}
		if s := strings.TrimRight(out.String(), "\n"); s != "" {
			t.Output = s
		}
	}
	if t.Title == "" {
		t.Title = t.Name
	}
	return it
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func (r *runtime) onToolCall(u update) {
	it := r.toolItem(u)
	c := *it
	tc := *it.Tool
	c.Tool = &tc
	r.emit(harness.ItemEvent{Item: c})
	if it.Status == model.ItemCompleted || it.Status == model.ItemFailed {
		delete(r.tools, u.ToolCallID)
	}
}

// session/request_permission and unsupported client methods (read loop).
func (r *runtime) onRequest(id json.RawMessage, method string, raw json.RawMessage) {
	if method != "session/request_permission" {
		r.conn.ReplyError(id, -32601, "remote-agent does not provide "+method)
		return
	}
	var p struct {
		ToolCall update `json:"toolCall"`
		Options  []struct {
			OptionID string `json:"optionId"`
			Name     string `json:"name"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	if json.Unmarshal(raw, &p) != nil {
		r.conn.ReplyError(id, -32602, "bad params")
		return
	}
	r.closeCur()
	ap := model.Approval{}
	if p.ToolCall.ToolCallID != "" {
		it := r.toolItem(p.ToolCall)
		it.Status = model.ItemPending
		c := *it
		tc := *it.Tool
		c.Tool = &tc
		r.emit(harness.ItemEvent{Item: c})
		ap.ToolItemID, ap.ToolName, ap.Title, ap.Input = it.ID, it.Tool.Name, it.Tool.Title, it.Tool.Input
	}
	if ap.Title == "" {
		ap.Title = "Permission requested"
	}
	req := &permRequest{id: id, options: map[string]bool{}}
	for _, o := range p.Options {
		kind := model.OptionAllowOnce
		switch o.Kind {
		case "allow_always":
			kind = model.OptionAllowSession
		case "reject_once", "reject_always":
			kind = model.OptionDeny
		}
		ap.Options = append(ap.Options, model.ApprovalOption{ID: o.OptionID, Label: o.Name, Kind: kind})
		req.options[o.OptionID] = true
	}
	r.mu.Lock()
	r.nextPerm++
	aid := fmt.Sprintf("perm-%d", r.nextPerm)
	r.perms[aid] = req
	r.mu.Unlock()
	r.emit(harness.ApprovalEvent{ID: aid, Approval: ap})
}
