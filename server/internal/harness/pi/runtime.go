package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"remote-agent/internal/harness"
	"remote-agent/internal/harness/proc"
	"remote-agent/internal/model"
)

// conn sends commands to pi and matches responses by id. Every other frame
// goes to onEvent, in order, on the read loop.
type conn struct {
	p       *proc.Proc
	onEvent func(typ string, raw []byte)
	mu      sync.Mutex
	next    int
	pending map[string]chan response
	done    chan struct{}
}

type response struct {
	ID      string          `json:"id"`
	Command string          `json:"command"`
	Success bool            `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
}

func newConn(p *proc.Proc, onEvent func(string, []byte)) *conn {
	return &conn{p: p, onEvent: onEvent, pending: map[string]chan response{}, done: make(chan struct{})}
}

// send writes a command; its response arrives on the returned channel.
func (c *conn) send(typ string, params map[string]any) (<-chan response, error) {
	c.mu.Lock()
	c.next++
	id := strconv.Itoa(c.next)
	ch := make(chan response, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	cmd := map[string]any{"id": id, "type": typ}
	for k, v := range params {
		cmd[k] = v
	}
	if err := c.p.WriteJSON(cmd); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	return ch, nil
}

func (c *conn) call(ctx context.Context, typ string, params map[string]any, out any) error {
	ch, err := c.send(typ, params)
	if err != nil {
		return err
	}
	select {
	case r := <-ch:
		if !r.Success {
			return fmt.Errorf("%s: %s", typ, r.Error)
		}
		if out != nil && len(r.Data) > 0 {
			return json.Unmarshal(r.Data, out)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return errExited
	}
}

var errExited = errors.New("pi exited")

func (c *conn) readLoop() {
	defer close(c.done)
	for {
		line, err := c.p.ReadLine()
		if err != nil {
			return
		}
		var f struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &f) != nil || f.Type == "" {
			continue
		}
		if f.Type == "response" {
			var r response
			json.Unmarshal(line, &r)
			c.mu.Lock()
			ch := c.pending[r.ID]
			delete(c.pending, r.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- r
			}
			continue
		}
		if c.onEvent != nil {
			c.onEvent(f.Type, line)
		}
	}
}

type runtime struct {
	p         *proc.Proc
	c         *conn
	cwd       string
	sessionID string
	images    harness.ImageStore

	events  chan harness.Event
	emitMu  sync.Mutex
	closed  bool
	closing atomic.Bool

	mu           sync.Mutex
	turn         int // counts prompts
	inTurn       bool
	interrupting bool
	started      chan struct{}      // closed once pi starts on the current prompt
	dialogs      map[string]*dialog // approval id (the dialog's) -> dialog

	// Read-loop state. loopMu also lets Prompt and Interrupt end a turn that
	// pi will not settle.
	loopMu    sync.Mutex
	modelID   string
	effort    string
	window    int64
	msgN      int
	blocks    map[int]*model.Item    // content index -> item, for the current assistant message
	tools     map[string]*model.Item // toolCallId -> item
	usage     *model.Usage
	stop      string // the last assistant message's stopReason
	lastError string
}

func newRuntime(p *proc.Proc, cwd, sessionID string) *runtime {
	r := &runtime{p: p, cwd: cwd, sessionID: sessionID, events: make(chan harness.Event, 512),
		dialogs: map[string]*dialog{}, blocks: map[int]*model.Item{}, tools: map[string]*model.Item{}}
	r.c = newConn(p, r.onEvent)
	return r
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
	<-r.c.done
	var err error
	if !r.closing.Load() {
		if err = r.p.Err(); err == nil {
			err = errors.New("pi exited unexpectedly")
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

func (r *runtime) Prompt(ctx context.Context, in harness.Input) error {
	params := map[string]any{"message": in.Text}
	if len(in.Images) > 0 {
		var images []map[string]any
		for _, img := range in.Images {
			data, err := img.Base64()
			if err != nil {
				return err
			}
			images = append(images, map[string]any{"type": "image", "data": data, "mimeType": img.MimeType})
		}
		params["images"] = images
	}
	r.mu.Lock()
	if r.inTurn {
		r.mu.Unlock()
		return errors.New("a turn is already running")
	}
	r.turn++
	turn, started := r.turn, make(chan struct{})
	r.inTurn, r.interrupting, r.started = true, false, started
	r.mu.Unlock()
	res, err := r.c.send("prompt", params)
	if err != nil {
		r.abandon(turn)
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	select {
	case resp := <-res:
		if !resp.Success {
			r.abandon(turn)
			return fmt.Errorf("prompt: %s", resp.Error)
		}
		r.settleIfIdle(ctx, turn)
	case <-started:
		// pi answers a prompt for an extension command once the command is
		// done, and the command may be waiting on a dialog.
		go r.finishPrompt(res, turn)
	case <-ctx.Done():
		r.abandon(turn)
		return ctx.Err()
	case <-r.c.done:
		r.abandon(turn)
		return errExited
	}
	return nil
}

// finishPrompt handles the response to a prompt that pi started on.
func (r *runtime) finishPrompt(res <-chan response, turn int) {
	select {
	case resp := <-res:
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if resp.Success {
			r.settleIfIdle(ctx, turn)
			return
		}
		r.loopMu.Lock()
		r.stop, r.lastError = "error", resp.Error
		r.endTurn(turn)
		r.loopMu.Unlock()
	case <-r.c.done:
	}
}

// abandon forgets a prompt that pi did not take.
func (r *runtime) abandon(turn int) {
	r.mu.Lock()
	if r.turn == turn {
		r.inTurn, r.started = false, nil
	}
	r.mu.Unlock()
}

func (r *runtime) markStarted() {
	r.mu.Lock()
	if r.started != nil {
		close(r.started)
		r.started = nil
	}
	r.mu.Unlock()
}

func (r *runtime) Interrupt(ctx context.Context) error {
	r.mu.Lock()
	inTurn, turn := r.inTurn, r.turn
	r.interrupting = inTurn
	r.mu.Unlock()
	if !inTurn {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := r.c.call(ctx, "abort", nil, nil); err != nil {
		return err
	}
	r.settleIfIdle(ctx, turn)
	return nil
}

// settleIfIdle ends turn when pi is not running the agent. A prompt an
// extension handles itself, such as one of its /commands, starts no run, so
// it never settles; neither does an abort of one.
func (r *runtime) settleIfIdle(ctx context.Context, turn int) {
	var st state
	if r.c.call(ctx, "get_state", nil, &st) == nil && !st.IsStreaming {
		r.loopMu.Lock()
		r.endTurn(turn)
		r.loopMu.Unlock()
	}
}

func (r *runtime) SetMode(ctx context.Context, mode string) error {
	if mode == "full" {
		return nil
	}
	return harness.ErrUnsupported
}

func (r *runtime) active() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inTurn
}

// endTurn reports the end of turn, or of the current turn if 0, once.
// Callers hold loopMu.
func (r *runtime) endTurn(turn int) {
	r.mu.Lock()
	if !r.inTurn || turn != 0 && turn != r.turn {
		r.mu.Unlock()
		return
	}
	interrupted := r.interrupting
	r.inTurn, r.interrupting, r.started = false, false, nil
	dialogs := r.dialogs
	r.dialogs = map[string]*dialog{}
	r.mu.Unlock()
	for id := range dialogs {
		r.p.WriteJSON(map[string]any{"type": "extension_ui_response", "id": id, "cancelled": true})
		r.emit(harness.ApprovalCancelled{ID: id})
	}
	ev := harness.TurnEnded{Status: model.TurnCompleted, Usage: r.usage}
	switch {
	case interrupted || r.stop == "aborted":
		ev.Status = model.TurnInterrupted
	case r.stop == "error":
		ev.Status, ev.Error = model.TurnFailed, r.lastError
		if ev.Error == "" {
			ev.Error = "turn failed"
		}
	}
	r.blocks, r.tools = map[int]*model.Item{}, map[string]*model.Item{}
	r.usage, r.stop, r.lastError = nil, "", ""
	r.emit(ev)
}

// Events (read loop).

type message struct {
	Role         string          `json:"role"`
	Content      json.RawMessage `json:"content"` // a string in user messages
	Provider     string          `json:"provider"`
	Model        string          `json:"model"`
	StopReason   string          `json:"stopReason"`
	ErrorMessage string          `json:"errorMessage"`
	Usage        *struct {
		Input       int64 `json:"input"`
		Output      int64 `json:"output"`
		CacheRead   int64 `json:"cacheRead"`
		CacheWrite  int64 `json:"cacheWrite"`
		TotalTokens int64 `json:"totalTokens"`
		Cost        struct {
			Total float64 `json:"total"`
		} `json:"cost"`
	} `json:"usage"`
}

type block struct {
	Type      string          `json:"type"` // text | thinking | toolCall | image
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type toolResult struct {
	Content []struct {
		Type string `json:"type"` // text | image
		Text string `json:"text"`
		Data string `json:"data"`
	} `json:"content"`
	Details struct {
		Diff string `json:"diff"`
	} `json:"details"`
}

func (t *toolResult) text() string {
	if t == nil {
		return ""
	}
	var parts []string
	for _, c := range t.Content {
		if c.Type == "text" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// keepImages stores the images in a tool result, e.g. an image file read.
func (r *runtime) keepImages(t *toolResult) []model.ImageRef {
	var refs []model.ImageRef
	for _, c := range t.Content {
		if c.Type != "image" || r.images == nil {
			continue
		}
		if ref, err := r.images.PutBase64(c.Data); err == nil {
			refs = append(refs, ref)
		}
	}
	return refs
}

type event struct {
	Message *message `json:"message"`
	Update  *struct {
		Type         string `json:"type"`
		ContentIndex int    `json:"contentIndex"`
		Delta        string `json:"delta"`
		Content      string `json:"content"`
		ToolCall     *block `json:"toolCall"`
	} `json:"assistantMessageEvent"`
	ToolCallID    string          `json:"toolCallId"`
	ToolName      string          `json:"toolName"`
	Args          json.RawMessage `json:"args"`
	PartialResult *toolResult     `json:"partialResult"`
	Result        json.RawMessage `json:"result"`
	IsError       bool            `json:"isError"`
	Success       bool            `json:"success"`
	FinalError    string          `json:"finalError"`
}

func (r *runtime) onEvent(typ string, raw []byte) {
	r.loopMu.Lock()
	defer r.loopMu.Unlock()
	if typ == "extension_ui_request" {
		r.onDialog(raw)
		return
	}
	var ev event
	if json.Unmarshal(raw, &ev) != nil {
		return
	}
	switch typ {
	case "agent_start":
		r.markStarted()
	case "message_start":
		if ev.Message != nil && ev.Message.Role == "assistant" {
			r.msgN++
			r.blocks = map[int]*model.Item{}
		}
	case "message_update":
		if u := ev.Update; u != nil {
			r.onUpdate(u.Type, u.ContentIndex, u.Delta, u.Content, u.ToolCall)
		}
	case "message_end":
		if ev.Message != nil && ev.Message.Role == "assistant" {
			r.onAssistant(ev.Message)
		}
	case "tool_execution_start":
		r.emitItem(r.tool(ev.ToolCallID, ev.ToolName, ev.Args))
	case "tool_execution_update":
		it := r.tool(ev.ToolCallID, ev.ToolName, ev.Args)
		it.Tool.Output = ev.PartialResult.text() // accumulated so far
		r.emitItem(it)
	case "tool_execution_end":
		it := r.tool(ev.ToolCallID, ev.ToolName, nil)
		var res toolResult
		json.Unmarshal(ev.Result, &res)
		it.Tool.Output = res.text()
		if res.Details.Diff != "" {
			it.Tool.Output = res.Details.Diff
		}
		it.Images = r.keepImages(&res)
		it.Status = model.ItemCompleted
		if ev.IsError {
			it.Status = model.ItemFailed
		}
		r.emitItem(it)
	case "compaction_end":
		if len(ev.Result) > 0 && string(ev.Result) != "null" {
			r.emitItem(&model.Item{ID: fmt.Sprintf("compaction:%d", r.msgN), Kind: model.ItemNotice, Status: model.ItemCompleted, Text: "Context compacted"})
		}
	case "auto_retry_end":
		if !ev.Success && ev.FinalError != "" {
			r.lastError = ev.FinalError
		}
	case "agent_settled":
		r.endTurn(0)
	}
}

// block returns the item for content block i of the current assistant message.
func (r *runtime) block(i int, kind model.ItemKind) *model.Item {
	it := r.blocks[i]
	if it == nil {
		it = &model.Item{ID: fmt.Sprintf("msg%d.%d", r.msgN, i), Kind: kind, Status: model.ItemInProgress}
		r.blocks[i] = it
	}
	return it
}

func (r *runtime) onUpdate(typ string, i int, delta, content string, call *block) {
	switch typ {
	case "text_start", "text_delta", "text_end", "thinking_start", "thinking_delta", "thinking_end":
		kind := model.ItemAssistantMessage
		if strings.HasPrefix(typ, "thinking") {
			kind = model.ItemReasoning
		}
		it := r.block(i, kind)
		it.Text += delta
		if strings.HasSuffix(typ, "_end") && content != "" {
			it.Text = content
		}
		if strings.TrimSpace(it.Text) != "" {
			r.emitItem(it)
		}
	case "toolcall_end":
		if call != nil {
			r.emitItem(r.tool(call.ID, call.Name, call.Arguments))
		}
	}
}

// onAssistant finishes a message from its final content, which is authoritative.
func (r *runtime) onAssistant(m *message) {
	var blocks []block
	json.Unmarshal(m.Content, &blocks)
	for i, b := range blocks {
		switch b.Type {
		case "text", "thinking":
			kind, text := model.ItemAssistantMessage, b.Text
			if b.Type == "thinking" {
				kind, text = model.ItemReasoning, b.Thinking
			}
			it := r.block(i, kind)
			it.Text, it.Status = text, model.ItemCompleted
			if strings.TrimSpace(it.Text) != "" {
				r.emitItem(it)
			}
		case "toolCall":
			if r.tools[b.ID] == nil {
				r.emitItem(r.tool(b.ID, b.Name, b.Arguments))
			}
		}
	}
	r.stop = m.StopReason
	if m.StopReason == "error" {
		r.lastError = strings.TrimSpace(m.ErrorMessage)
	}
	if id := m.Provider + "/" + m.Model; m.Model != "" && id != r.modelID {
		r.modelID = id
		r.emit(harness.ModelInfo{ID: id, Effort: r.effort})
	}
	if u := m.Usage; u != nil && u.Input+u.Output+u.CacheRead+u.CacheWrite > 0 {
		if r.usage == nil {
			r.usage = &model.Usage{}
		}
		r.usage.InputTokens += u.Input
		r.usage.OutputTokens += u.Output
		r.usage.CacheReadTokens += u.CacheRead
		r.usage.CacheWriteTokens += u.CacheWrite
		r.usage.CostUSD += u.Cost.Total
		used := u.TotalTokens
		if used == 0 {
			used = u.Input + u.Output + u.CacheRead + u.CacheWrite
		}
		if used > 0 {
			r.emit(harness.ContextUsage{Used: used, Window: r.window})
		}
	}
}

func (r *runtime) tool(id, name string, args json.RawMessage) *model.Item {
	it := r.tools[id]
	if it == nil {
		it = &model.Item{ID: id, Kind: model.ItemToolCall, Status: model.ItemInProgress, Tool: &model.ToolCall{Name: name, Kind: model.ToolOther, Title: name}}
		r.tools[id] = it
	}
	if len(args) > 0 && string(args) != "null" && string(args) != "{}" {
		it.Tool.Input = args
		it.Tool.Kind, it.Tool.Title, it.Tool.Paths = describeTool(it.Tool.Name, args, r.cwd)
	}
	return it
}

// emitItem sends it unless the turn is over: pi can trail a turn that
// Interrupt already ended.
func (r *runtime) emitItem(it *model.Item) {
	if !r.active() {
		return
	}
	c := *it
	if it.Tool != nil {
		t := *it.Tool
		c.Tool = &t
	}
	r.emit(harness.ItemEvent{Item: c})
}

// Extension dialogs.

type dialog struct {
	method   string // select | confirm
	options  []string
	question string // set when the options are shown as a question
}

type dialogRequest struct {
	ID         string   `json:"id"`
	Method     string   `json:"method"`
	Title      string   `json:"title"`
	Message    string   `json:"message"`
	NotifyType string   `json:"notifyType"`
	Options    []string `json:"options"`
	Timeout    int64    `json:"timeout"` // ms; pi then answers the dialog itself
}

func (r *runtime) onDialog(raw []byte) {
	var d dialogRequest
	if json.Unmarshal(raw, &d) != nil {
		return
	}
	cancel := func() {
		r.p.WriteJSON(map[string]any{"type": "extension_ui_response", "id": d.ID, "cancelled": true})
	}
	switch d.Method {
	case "notify":
		kind := model.ItemNotice
		if d.NotifyType == "error" {
			kind = model.ItemError
		}
		r.emitItem(&model.Item{ID: "ui:" + d.ID, Kind: kind, Status: model.ItemCompleted, Text: d.Message})
		return
	case "input", "editor":
		cancel()
		title, _ := splitTitle(d.Title)
		r.emitItem(&model.Item{ID: "ui:" + d.ID, Kind: model.ItemNotice, Status: model.ItemCompleted,
			Text: fmt.Sprintf("Pi asked for text (%s), which the app can't answer yet.", title)})
		return
	case "select", "confirm":
	default:
		return // status lines, widgets and titles are for pi's own TUI
	}
	if !r.active() || d.Method == "select" && len(d.Options) == 0 {
		cancel()
		return
	}
	dl := &dialog{method: d.Method, options: d.Options}
	title, detail := splitTitle(d.Title)
	ap := model.Approval{Title: title, Detail: detail}
	switch {
	case d.Method == "confirm":
		ap.Detail = strings.TrimSpace(detail + "\n\n" + d.Message)
		ap.Options = []model.ApprovalOption{
			{ID: "yes", Label: "Yes", Kind: model.OptionAllowOnce},
			{ID: "no", Label: "No", Kind: model.OptionDeny},
		}
	case len(d.Options) <= 3:
		for i, o := range d.Options {
			ap.Options = append(ap.Options, model.ApprovalOption{ID: "opt:" + strconv.Itoa(i), Label: o, Kind: optionKind(o)})
		}
	default:
		dl.question = strings.TrimSpace(d.Title)
		q := model.Question{Question: dl.question}
		for _, o := range d.Options {
			q.Options = append(q.Options, model.QuestionOption{Label: o})
		}
		ap = model.Approval{Title: "Pi has a question", Special: model.SpecialQuestion, Questions: []model.Question{q},
			Options: []model.ApprovalOption{{ID: "choose", Label: "Submit", Kind: model.OptionAllowOnce}, {ID: "cancel", Label: "Skip", Kind: model.OptionDeny}}}
	}
	r.mu.Lock()
	r.dialogs[d.ID] = dl
	r.mu.Unlock()
	r.markStarted()
	if d.Timeout > 0 {
		time.AfterFunc(time.Duration(d.Timeout)*time.Millisecond, func() {
			r.mu.Lock()
			_, ok := r.dialogs[d.ID]
			delete(r.dialogs, d.ID)
			r.mu.Unlock()
			if ok {
				r.emit(harness.ApprovalCancelled{ID: d.ID})
			}
		})
	}
	r.emit(harness.ApprovalEvent{ID: d.ID, Approval: ap})
}

func (r *runtime) Respond(ctx context.Context, approvalID string, resp harness.Response) error {
	r.mu.Lock()
	d := r.dialogs[approvalID]
	delete(r.dialogs, approvalID)
	r.mu.Unlock()
	if d == nil {
		return fmt.Errorf("no pending approval %s", approvalID)
	}
	return r.p.WriteJSON(d.answer(approvalID, resp))
}

// answer is the extension_ui_response for resp.
func (d *dialog) answer(id string, resp harness.Response) map[string]any {
	out := map[string]any{"type": "extension_ui_response", "id": id}
	i, err := strconv.Atoi(strings.TrimPrefix(resp.OptionID, "opt:"))
	switch {
	case d.method == "confirm":
		out["confirmed"] = resp.OptionID == "yes"
	case strings.HasPrefix(resp.OptionID, "opt:") && err == nil && i >= 0 && i < len(d.options):
		out["value"] = d.options[i]
	case resp.OptionID == "choose" && resp.Answers[d.question] != "":
		out["value"] = resp.Answers[d.question]
	default:
		out["cancelled"] = true
	}
	return out
}

// splitTitle makes a dialog's first line the card title and the rest its detail.
func splitTitle(s string) (title, detail string) {
	title, rest, _ := strings.Cut(strings.TrimSpace(s), "\n")
	lines := strings.Split(strings.TrimSpace(rest), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.TrimSpace(title), strings.Join(lines, "\n")
}

// optionKind styles a choice by its wording, e.g. "No" or "Block" as a denial.
func optionKind(label string) model.OptionKind {
	l := strings.ToLower(strings.TrimSpace(label))
	first, _, _ := strings.Cut(l, " ")
	switch strings.Trim(first, ",.!") {
	case "no", "deny", "block", "reject", "decline", "cancel", "abort", "skip", "stop", "never", "don't", "dont":
		return model.OptionDeny
	}
	if strings.Contains(l, "always") || strings.Contains(l, "session") {
		return model.OptionAllowSession
	}
	return model.OptionAllowOnce
}
