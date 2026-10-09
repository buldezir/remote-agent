package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"remote-agent/internal/harness"
	"remote-agent/internal/model"
)

type pending struct {
	toolName    string
	input       json.RawMessage
	suggestions json.RawMessage
	special     model.ApprovalSpecial
}

type blockState struct {
	key  string
	kind model.ItemKind // assistant_message | reasoning | tool_call
	text strings.Builder
}

type streamState struct {
	msgID  string
	blocks map[int]*blockState
}

type runtime struct {
	conn     *conn
	cwd      string
	nativeID string
	log      *slog.Logger

	events   chan harness.Event
	emitMu   sync.Mutex
	closed   bool
	closing  atomic.Bool
	exitOnce sync.Once

	mu           sync.Mutex
	mode         string
	approvals    map[string]*pending
	interrupting bool
	inTurn       bool

	// Streaming state per agent (main agent is "", subagents are keyed by
	// their Task tool_use id), since parallel subagents interleave events.
	streams  map[string]*streamState
	counters map[string]map[string]int // stream-side block counters: msgID -> block type -> n
	finals   map[string]map[string]int // assistant-frame-side counters
	tools    map[string]*model.Item    // tool_use id -> item
	plan     *model.Item

	// Context usage of the main agent: tokens in its last request, and the
	// window of its model (only reported in result frames).
	ctxModel  string
	ctxUsed   int64
	ctxWindow int64
}

func (r *runtime) NativeID() string             { return r.nativeID }
func (r *runtime) Events() <-chan harness.Event { return r.events }

func (r *runtime) emit(e harness.Event) {
	r.emitMu.Lock()
	defer r.emitMu.Unlock()
	if !r.closed {
		r.events <- e
	}
}

func (r *runtime) waitExit() {
	<-r.conn.p.Done()
	<-r.conn.done
	var err error
	if !r.closing.Load() {
		err = r.conn.p.Err()
		if err == nil {
			err = fmt.Errorf("claude exited unexpectedly")
			if tail := strings.TrimSpace(r.conn.p.StderrTail()); tail != "" {
				err = fmt.Errorf("claude exited: %s", lastLine(tail))
			}
		}
	}
	r.mu.Lock()
	inTurn := r.inTurn
	r.inTurn = false
	r.mu.Unlock()
	if inTurn && err == nil {
		r.emit(harness.TurnEnded{Status: model.TurnInterrupted})
	}
	r.emit(harness.Exited{Err: err})
	r.emitMu.Lock()
	r.closed = true
	close(r.events)
	r.emitMu.Unlock()
}

func (r *runtime) Prompt(ctx context.Context, text string) error {
	r.mu.Lock()
	if r.inTurn {
		r.mu.Unlock()
		return fmt.Errorf("a turn is already running")
	}
	r.inTurn = true
	r.interrupting = false
	r.mu.Unlock()
	err := r.conn.p.WriteJSON(map[string]any{
		"type":               "user",
		"message":            map[string]any{"role": "user", "content": text},
		"parent_tool_use_id": nil,
		"session_id":         r.nativeID,
	})
	if err != nil {
		r.mu.Lock()
		r.inTurn = false
		r.mu.Unlock()
	}
	return err
}

func (r *runtime) Interrupt(ctx context.Context) error {
	r.mu.Lock()
	if !r.inTurn {
		r.mu.Unlock()
		return nil
	}
	r.interrupting = true
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := r.conn.request(ctx, map[string]any{"subtype": "interrupt"})
	return err
}

func (r *runtime) SetMode(ctx context.Context, mode string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := r.conn.request(ctx, map[string]any{"subtype": "set_permission_mode", "mode": mode}); err != nil {
		return err
	}
	r.setMode(mode)
	return nil
}

func (r *runtime) setMode(mode string) {
	r.mu.Lock()
	changed := r.mode != mode
	r.mode = mode
	r.mu.Unlock()
	if changed {
		r.emit(harness.ModeChanged{Mode: mode})
	}
}

func (r *runtime) Close() error {
	r.closing.Store(true)
	r.conn.p.Stop(3 * time.Second)
	return nil
}

// Respond answers a can_use_tool request.
func (r *runtime) Respond(ctx context.Context, approvalID string, resp harness.Response) error {
	r.mu.Lock()
	p := r.approvals[approvalID]
	delete(r.approvals, approvalID)
	r.mu.Unlock()
	if p == nil {
		return fmt.Errorf("no pending approval %s", approvalID)
	}
	var body map[string]any
	switch resp.OptionID {
	case "deny":
		msg := resp.Message
		if msg == "" {
			msg = "The user denied this action."
			if p.special == model.SpecialPlan {
				msg = "The user wants to keep planning. Revise the plan."
			}
		}
		body = map[string]any{"behavior": "deny", "message": msg}
	default:
		input := p.input
		if p.special == model.SpecialQuestion {
			var m map[string]any
			json.Unmarshal(p.input, &m)
			if m == nil {
				m = map[string]any{}
			}
			m["answers"] = resp.Answers
			input, _ = json.Marshal(m)
		}
		body = map[string]any{"behavior": "allow", "updatedInput": input}
		if resp.OptionID == "allow_session" && len(p.suggestions) > 0 && string(p.suggestions) != "null" {
			body["updatedPermissions"] = p.suggestions
		}
	}
	if err := r.conn.respond(approvalID, body); err != nil {
		return err
	}
	// Approving a plan leaves plan mode; session-wide grants may switch modes.
	if body["behavior"] == "allow" {
		if p.special == model.SpecialPlan {
			if resp.OptionID == "allow_session" {
				r.setMode("acceptEdits")
			} else {
				r.setMode("default")
			}
		} else if resp.OptionID == "allow_session" {
			if m := suggestedMode(p.suggestions); m != "" {
				r.setMode(m)
			}
		}
	}
	return nil
}

type suggestion struct {
	Type     string `json:"type"`
	Mode     string `json:"mode"`
	Behavior string `json:"behavior"`
	Rules    []struct {
		ToolName    string `json:"toolName"`
		RuleContent string `json:"ruleContent"`
	} `json:"rules"`
}

func suggestedMode(raw json.RawMessage) string {
	var ss []suggestion
	json.Unmarshal(raw, &ss)
	for _, s := range ss {
		if s.Type == "setMode" {
			return s.Mode
		}
	}
	return ""
}

func sessionLabel(raw json.RawMessage) string {
	var ss []suggestion
	if json.Unmarshal(raw, &ss) != nil || len(ss) == 0 {
		return ""
	}
	s := ss[0]
	switch s.Type {
	case "setMode":
		if s.Mode == "acceptEdits" {
			return "Allow all edits this session"
		}
		return "Allow & switch to " + s.Mode
	case "addRules":
		if len(s.Rules) > 0 {
			r := s.Rules[0]
			if r.RuleContent != "" {
				return fmt.Sprintf("Always allow %s(%s)", r.ToolName, r.RuleContent)
			}
			return "Always allow " + r.ToolName
		}
	case "addDirectories":
		return "Allow access to this directory"
	}
	return "Allow for session"
}

// Frame handling.

func (r *runtime) onFrame(f frame, raw []byte) {
	switch f.Type {
	case "system":
		if f.Subtype == "init" {
			if f.SessionID != "" && f.SessionID != r.nativeID {
				r.nativeID = f.SessionID
				r.emit(harness.NativeID{ID: f.SessionID})
			}
			if f.PermissionMode != "" {
				r.setMode(f.PermissionMode)
			}
		}
	case "stream_event":
		r.onStreamEvent(f)
	case "assistant":
		r.onAssistant(f)
	case "user":
		r.onUser(f)
	case "result":
		r.onResult(f)
	case "control_request":
		r.onControlRequest(f)
	case "control_cancel_request":
		r.mu.Lock()
		_, ok := r.approvals[f.RequestID]
		delete(r.approvals, f.RequestID)
		r.mu.Unlock()
		if ok {
			r.emit(harness.ApprovalCancelled{ID: f.RequestID})
		}
	}
}

type streamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		ID string `json:"id"`
	} `json:"message"`
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta *struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	} `json:"delta"`
}

func (r *runtime) blockKey(counters map[string]map[string]int, msgID, typ string) string {
	if counters[msgID] == nil {
		counters[msgID] = map[string]int{}
	}
	n := counters[msgID][typ]
	counters[msgID][typ] = n + 1
	return fmt.Sprintf("%s:%s:%d", msgID, typ, n)
}

func (r *runtime) onStreamEvent(f frame) {
	var ev streamEvent
	if json.Unmarshal(f.Event, &ev) != nil {
		return
	}
	if r.streams == nil {
		r.streams = map[string]*streamState{}
	}
	st := r.streams[f.ParentToolUseID]
	if st == nil {
		st = &streamState{blocks: map[int]*blockState{}}
		r.streams[f.ParentToolUseID] = st
	}
	switch ev.Type {
	case "message_start":
		if ev.Message != nil {
			st.msgID = ev.Message.ID
		}
		st.blocks = map[int]*blockState{}
	case "content_block_start":
		if ev.ContentBlock == nil {
			return
		}
		switch ev.ContentBlock.Type {
		case "text":
			st.blocks[ev.Index] = &blockState{key: r.blockKey(r.counters, st.msgID, "text"), kind: model.ItemAssistantMessage}
		case "thinking":
			st.blocks[ev.Index] = &blockState{key: r.blockKey(r.counters, st.msgID, "thinking"), kind: model.ItemReasoning}
		case "tool_use", "server_tool_use":
			// The full input arrives with the assistant frame; show the call early.
			if special(ev.ContentBlock.Name) || ev.ContentBlock.Name == "TodoWrite" {
				return
			}
			it := r.toolItem(ev.ContentBlock.ID, ev.ContentBlock.Name, nil, f.ParentToolUseID)
			it.Status = model.ItemInProgress
			r.emit(harness.ItemEvent{Item: snapshot(it)})
		}
	case "content_block_delta":
		b := st.blocks[ev.Index]
		if b == nil || ev.Delta == nil {
			return
		}
		switch ev.Delta.Type {
		case "text_delta":
			b.text.WriteString(ev.Delta.Text)
		case "thinking_delta":
			b.text.WriteString(ev.Delta.Thinking)
		default:
			return
		}
		if b.text.Len() == 0 {
			return
		}
		r.emit(harness.ItemEvent{Item: model.Item{ID: b.key, ParentItemID: f.ParentToolUseID, Kind: b.kind, Status: model.ItemInProgress, Text: b.text.String()}})
	}
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type message struct {
	ID      string          `json:"id"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Usage   *struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

func (m *message) blocks() []contentBlock {
	var bs []contentBlock
	if json.Unmarshal(m.Content, &bs) == nil {
		return bs
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil && s != "" {
		return []contentBlock{{Type: "text", Text: s}}
	}
	return nil
}

func (r *runtime) onAssistant(f frame) {
	var m message
	if json.Unmarshal(f.Message, &m) != nil {
		return
	}
	if r.finals == nil {
		r.finals = map[string]map[string]int{}
	}
	// Subagents have their own context; synthetic messages have no usage.
	if u := m.Usage; u != nil && f.ParentToolUseID == "" && m.Model != "<synthetic>" {
		used := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens + u.OutputTokens
		if used > 0 && used != r.ctxUsed {
			r.ctxModel, r.ctxUsed = m.Model, used
			r.emit(harness.ContextUsage{Used: used, Window: r.ctxWindow})
		}
	}
	for _, b := range m.blocks() {
		switch b.Type {
		case "text":
			key := r.blockKey(r.finals, m.ID, "text")
			if strings.TrimSpace(b.Text) == "" {
				continue
			}
			r.emit(harness.ItemEvent{Item: model.Item{ID: key, ParentItemID: f.ParentToolUseID, Kind: model.ItemAssistantMessage, Status: model.ItemCompleted, Text: b.Text}})
		case "thinking":
			key := r.blockKey(r.finals, m.ID, "thinking")
			if strings.TrimSpace(b.Thinking) == "" {
				continue
			}
			r.emit(harness.ItemEvent{Item: model.Item{ID: key, ParentItemID: f.ParentToolUseID, Kind: model.ItemReasoning, Status: model.ItemCompleted, Text: b.Thinking}})
		case "tool_use", "server_tool_use":
			if b.Name == "TodoWrite" {
				r.emitPlan(b.Input)
				continue
			}
			if special(b.Name) {
				continue // shown as an approval card instead
			}
			it := r.toolItem(b.ID, b.Name, b.Input, f.ParentToolUseID)
			r.emit(harness.ItemEvent{Item: snapshot(it)})
		}
	}
}

func special(name string) bool { return name == "AskUserQuestion" || name == "ExitPlanMode" }

func (r *runtime) toolItem(id, name string, input json.RawMessage, parent string) *model.Item {
	if r.tools == nil {
		r.tools = map[string]*model.Item{}
	}
	it := r.tools[id]
	if it == nil {
		it = &model.Item{ID: id, Kind: model.ItemToolCall, Status: model.ItemInProgress, ParentItemID: parent,
			Tool: &model.ToolCall{Name: name, Kind: toolKind(name)}}
		r.tools[id] = it
	}
	if len(input) > 0 {
		it.Tool.Input = input
		it.Tool.Title, it.Tool.Paths = describeTool(name, input, r.cwd)
	} else if it.Tool.Title == "" {
		it.Tool.Title = name
	}
	return it
}

// snapshot copies a tool item for emitting: the read loop keeps updating the
// ToolCall (title, output) while consumers may still be reading the last event.
func snapshot(it *model.Item) model.Item {
	c := *it
	if it.Tool != nil {
		t := *it.Tool
		c.Tool = &t
	}
	return c
}

func (r *runtime) emitPlan(input json.RawMessage) {
	var in struct {
		Todos []struct {
			Content    string `json:"content"`
			Status     string `json:"status"`
			ActiveForm string `json:"activeForm"`
		} `json:"todos"`
	}
	if json.Unmarshal(input, &in) != nil {
		return
	}
	if r.plan == nil {
		r.plan = &model.Item{ID: fmt.Sprintf("plan:%d", time.Now().UnixNano()), Kind: model.ItemPlan, Status: model.ItemCompleted}
	}
	p := &model.Plan{}
	for _, t := range in.Todos {
		p.Entries = append(p.Entries, model.PlanEntry{Content: t.Content, Status: t.Status})
	}
	r.plan.Plan = p
	r.emit(harness.ItemEvent{Item: *r.plan})
}

func (r *runtime) onUser(f frame) {
	var m message
	if json.Unmarshal(f.Message, &m) != nil {
		return
	}
	for _, b := range m.blocks() {
		if b.Type != "tool_result" {
			continue
		}
		it := r.tools[b.ToolUseID]
		if it == nil {
			continue // TodoWrite, AskUserQuestion, ExitPlanMode
		}
		it.Tool.Output = resultText(b.Content)
		it.Status = model.ItemCompleted
		if b.IsError {
			it.Status = model.ItemFailed
		}
		r.emit(harness.ItemEvent{Item: snapshot(it)})
		delete(r.tools, b.ToolUseID)
	}
}

func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	json.Unmarshal(raw, &parts)
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(p.Text)
		} else if p.Type == "image" {
			b.WriteString("[image]")
		}
	}
	return b.String()
}

func (r *runtime) onResult(f frame) {
	r.mu.Lock()
	interrupted := r.interrupting
	r.inTurn, r.interrupting = false, false
	pendingIDs := make([]string, 0, len(r.approvals))
	for id := range r.approvals {
		pendingIDs = append(pendingIDs, id)
	}
	r.approvals = map[string]*pending{}
	r.mu.Unlock()
	for _, id := range pendingIDs {
		r.emit(harness.ApprovalCancelled{ID: id})
	}
	// Tool calls that never got a result (e.g. after an interrupt) end here.
	for id, it := range r.tools {
		it.Status = model.ItemFailed
		r.emit(harness.ItemEvent{Item: snapshot(it)})
		delete(r.tools, id)
	}
	if w := contextWindow(f, r.ctxModel); w > 0 && w != r.ctxWindow {
		r.ctxWindow = w
		if r.ctxUsed > 0 {
			r.emit(harness.ContextUsage{Used: r.ctxUsed, Window: w})
		}
	}
	ev := harness.TurnEnded{Status: model.TurnCompleted}
	if f.Usage != nil {
		ev.Usage = &model.Usage{
			InputTokens: f.Usage.InputTokens, OutputTokens: f.Usage.OutputTokens,
			CacheReadTokens: f.Usage.CacheReadInputTokens, CacheWriteTokens: f.Usage.CacheCreationInputTokens,
			CostUSD: f.TotalCostUSD,
		}
	}
	switch {
	case interrupted:
		ev.Status = model.TurnInterrupted
	case f.IsError || (f.Subtype != "" && f.Subtype != "success"):
		ev.Status = model.TurnFailed
		ev.Error = f.Result
		if ev.Error == "" {
			ev.Error = strings.Join(f.Errors, "; ")
		}
		if ev.Error == "" {
			ev.Error = f.Subtype
		}
	}
	r.emit(ev)
}

// contextWindow finds the main model's window in a result's per-model usage
// (which also lists models used by subagents and background calls).
func contextWindow(f frame, mainModel string) int64 {
	var max int64
	for name, u := range f.ModelUsage {
		if mainModel != "" && (name == mainModel || u.CanonicalModel == mainModel) {
			return u.ContextWindow
		}
		if u.ContextWindow > max {
			max = u.ContextWindow
		}
	}
	return max
}

type canUseTool struct {
	Subtype              string          `json:"subtype"`
	ToolName             string          `json:"tool_name"`
	DisplayName          string          `json:"display_name"`
	Input                json.RawMessage `json:"input"`
	Description          string          `json:"description"`
	DecisionReason       string          `json:"decision_reason"`
	BlockedPath          string          `json:"blocked_path"`
	PermissionSuggestion json.RawMessage `json:"permission_suggestions"`
	ToolUseID            string          `json:"tool_use_id"`
}

func (r *runtime) onControlRequest(f frame) {
	var req canUseTool
	json.Unmarshal(f.Request, &req)
	if req.Subtype != "can_use_tool" {
		r.conn.respondError(f.RequestID, "unsupported control request "+req.Subtype)
		return
	}
	p := &pending{toolName: req.ToolName, input: req.Input, suggestions: req.PermissionSuggestion}
	ap := model.Approval{ToolName: req.ToolName, Input: req.Input}
	switch req.ToolName {
	case "AskUserQuestion":
		p.special = model.SpecialQuestion
		var in struct {
			Questions []model.Question `json:"questions"`
		}
		json.Unmarshal(req.Input, &in)
		ap.Special, ap.Questions, ap.Title = model.SpecialQuestion, in.Questions, "Claude has a question"
		ap.Options = []model.ApprovalOption{
			{ID: "allow", Label: "Submit", Kind: model.OptionAllowOnce},
			{ID: "deny", Label: "Skip", Kind: model.OptionDeny},
		}
	case "ExitPlanMode":
		p.special = model.SpecialPlan
		var in struct {
			Plan string `json:"plan"`
		}
		json.Unmarshal(req.Input, &in)
		ap.Special, ap.PlanText, ap.Title = model.SpecialPlan, in.Plan, "Ready to code?"
		ap.Options = []model.ApprovalOption{
			{ID: "allow_session", Label: "Yes, auto-accept edits", Kind: model.OptionAllowSession},
			{ID: "allow", Label: "Yes, ask before edits", Kind: model.OptionAllowOnce},
			{ID: "deny", Label: "Keep planning", Kind: model.OptionDeny},
		}
	default:
		ap.ToolItemID = req.ToolUseID
		ap.Title, _ = describeTool(req.ToolName, req.Input, r.cwd)
		ap.Detail = req.DecisionReason
		if ap.Detail == "" && req.BlockedPath != "" {
			ap.Detail = "Outside the workspace: " + req.BlockedPath
		}
		ap.Options = []model.ApprovalOption{{ID: "allow", Label: "Allow", Kind: model.OptionAllowOnce}}
		if l := sessionLabel(req.PermissionSuggestion); l != "" {
			ap.Options = append(ap.Options, model.ApprovalOption{ID: "allow_session", Label: l, Kind: model.OptionAllowSession})
		}
		ap.Options = append(ap.Options, model.ApprovalOption{ID: "deny", Label: "Deny", Kind: model.OptionDeny})
		if it := r.tools[req.ToolUseID]; it != nil && it.Status != model.ItemPending {
			it.Status = model.ItemPending
			r.emit(harness.ItemEvent{Item: snapshot(it)})
		}
	}
	r.mu.Lock()
	r.approvals[f.RequestID] = p
	r.mu.Unlock()
	r.emit(harness.ApprovalEvent{ID: f.RequestID, Approval: ap})
}
