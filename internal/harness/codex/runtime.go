package codex

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

type serverRequest struct {
	id       json.RawMessage
	method   string
	params   json.RawMessage
	question map[string]string // question text -> question id (requestUserInput)
}

type runtime struct {
	p        *proc.Proc
	conn     *jsonrpc.Conn
	cwd      string
	threadID string

	events  chan harness.Event
	emitMu  sync.Mutex
	closed  bool
	closing atomic.Bool

	mu           sync.Mutex
	mode         string
	model        string
	turnID       string
	inTurn       bool
	interrupting bool
	requests     map[string]*serverRequest // approval id -> request
	nextReq      int

	// Read-loop state.
	items      map[string]*model.Item
	plan       *model.Item
	usageTotal *usage
	usageBase  *usage
	lastError  string
}

type usage struct {
	InputTokens       int64 `json:"inputTokens"`
	CachedInputTokens int64 `json:"cachedInputTokens"`
	CacheWriteTokens  int64 `json:"cacheWriteInputTokens"`
	OutputTokens      int64 `json:"outputTokens"`
}

func (r *runtime) NativeID() string             { return r.threadID }
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
			err = fmt.Errorf("codex app-server exited unexpectedly")
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
	r.inTurn, r.interrupting = true, false
	pr := presets[r.mode]
	r.mu.Unlock()
	params := map[string]any{
		"threadId":       r.threadID,
		"input":          []map[string]any{{"type": "text", "text": text}},
		"approvalPolicy": pr.approval,
		"sandboxPolicy":  pr.policy,
	}
	var res struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := r.conn.Call(ctx, "turn/start", params, &res); err != nil {
		r.mu.Lock()
		r.inTurn = false
		r.mu.Unlock()
		return err
	}
	r.mu.Lock()
	r.turnID = res.Turn.ID
	r.mu.Unlock()
	return nil
}

func (r *runtime) Interrupt(ctx context.Context) error {
	r.mu.Lock()
	turnID, inTurn := r.turnID, r.inTurn
	r.interrupting = inTurn
	r.mu.Unlock()
	if !inTurn || turnID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return r.conn.Call(ctx, "turn/interrupt", map[string]any{"threadId": r.threadID, "turnId": turnID}, nil)
}

func (r *runtime) SetMode(ctx context.Context, mode string) error {
	if _, ok := presets[mode]; !ok {
		return fmt.Errorf("unknown mode %q", mode)
	}
	r.mu.Lock()
	r.mode = mode
	r.mu.Unlock()
	r.emit(harness.ModeChanged{Mode: mode}) // applied from the next turn
	return nil
}

func (r *runtime) Respond(ctx context.Context, approvalID string, resp harness.Response) error {
	r.mu.Lock()
	req := r.requests[approvalID]
	delete(r.requests, approvalID)
	r.mu.Unlock()
	if req == nil {
		return fmt.Errorf("no pending approval %s", approvalID)
	}
	switch req.method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		decision := map[string]string{"allow": "accept", "allow_session": "acceptForSession", "deny": "decline"}[resp.OptionID]
		return r.conn.Reply(req.id, map[string]any{"decision": decision})
	case "item/permissions/requestApproval":
		var p struct {
			Permissions json.RawMessage `json:"permissions"`
		}
		json.Unmarshal(req.params, &p)
		switch resp.OptionID {
		case "deny":
			return r.conn.Reply(req.id, map[string]any{"permissions": map[string]any{}, "scope": "turn"})
		case "allow_session":
			return r.conn.Reply(req.id, map[string]any{"permissions": p.Permissions, "scope": "session"})
		default:
			return r.conn.Reply(req.id, map[string]any{"permissions": p.Permissions, "scope": "turn"})
		}
	case "item/tool/requestUserInput":
		answers := map[string]any{}
		if resp.OptionID != "deny" {
			for q, a := range resp.Answers {
				if id, ok := req.question[q]; ok {
					answers[id] = map[string]any{"answers": strings.Split(a, ", ")}
				}
			}
		}
		return r.conn.Reply(req.id, map[string]any{"answers": answers})
	}
	return r.conn.ReplyError(req.id, -32601, "unsupported")
}

// Notifications (read loop).

type threadItem struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	Text             string          `json:"text"`
	Summary          []string        `json:"summary"`
	Content          json.RawMessage `json:"content"`
	Command          string          `json:"command"`
	CommandActions   []commandAction `json:"commandActions"`
	AggregatedOutput *string         `json:"aggregatedOutput"`
	ExitCode         *int            `json:"exitCode"`
	Status           string          `json:"status"`
	Changes          []struct {
		Path string          `json:"path"`
		Kind json.RawMessage `json:"kind"`
		Diff string          `json:"diff"`
	} `json:"changes"`
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Result    json.RawMessage `json:"result"`
	Error     json.RawMessage `json:"error"`
	Query     string          `json:"query"`
	Path      string          `json:"path"`
	Review    string          `json:"review"`
}

type commandAction struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Path    string `json:"path"`
	Query   string `json:"query"`
}

func (r *runtime) onNotify(method string, raw json.RawMessage) {
	switch method {
	case "item/started", "item/completed":
		var p struct {
			Item threadItem `json:"item"`
		}
		if json.Unmarshal(raw, &p) == nil {
			r.onItem(p.Item, method == "item/completed")
		}
	case "item/agentMessage/delta", "item/reasoning/summaryTextDelta", "item/reasoning/textDelta", "item/commandExecution/outputDelta":
		var p struct {
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
		}
		if json.Unmarshal(raw, &p) != nil {
			return
		}
		it := r.items[p.ItemID]
		if it == nil {
			return
		}
		if it.Tool != nil {
			it.Tool.Output += p.Delta
		} else {
			it.Text += p.Delta
			if it.Kind == "" {
				it.Kind, it.Status = model.ItemAssistantMessage, model.ItemInProgress
				if method != "item/agentMessage/delta" {
					it.Kind = model.ItemReasoning
				}
			}
		}
		r.emit(harness.ItemEvent{Item: cloneItem(it)})
	case "turn/plan/updated":
		var p struct {
			Explanation string `json:"explanation"`
			Plan        []struct {
				Step   string `json:"step"`
				Status string `json:"status"`
			} `json:"plan"`
		}
		if json.Unmarshal(raw, &p) != nil {
			return
		}
		if r.plan == nil {
			r.plan = &model.Item{ID: fmt.Sprintf("plan:%d", time.Now().UnixNano()), Kind: model.ItemPlan, Status: model.ItemCompleted}
		}
		pl := &model.Plan{Text: p.Explanation}
		for _, s := range p.Plan {
			st := map[string]string{"inProgress": "in_progress"}[s.Status]
			if st == "" {
				st = s.Status
			}
			pl.Entries = append(pl.Entries, model.PlanEntry{Content: s.Step, Status: st})
		}
		r.plan.Plan = pl
		r.emit(harness.ItemEvent{Item: cloneItem(r.plan)})
	case "turn/started":
		r.usageBase = r.usageTotal
	case "thread/tokenUsage/updated":
		var p struct {
			TokenUsage struct {
				Total usage `json:"total"`
				Last  struct {
					TotalTokens int64 `json:"totalTokens"`
				} `json:"last"`
				ModelContextWindow *int64 `json:"modelContextWindow"`
			} `json:"tokenUsage"`
		}
		if json.Unmarshal(raw, &p) == nil {
			r.usageTotal = &p.TokenUsage.Total
			if used := p.TokenUsage.Last.TotalTokens; used > 0 {
				ev := harness.ContextUsage{Used: used}
				if w := p.TokenUsage.ModelContextWindow; w != nil {
					ev.Window = *w
				}
				r.emit(ev)
			}
		}
	case "error":
		var p struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			WillRetry bool `json:"willRetry"`
		}
		if json.Unmarshal(raw, &p) == nil && !p.WillRetry {
			r.lastError = p.Error.Message
		}
	case "turn/completed":
		var p struct {
			Turn struct {
				Status string `json:"status"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"turn"`
		}
		json.Unmarshal(raw, &p)
		r.endTurn(p.Turn.Status, p.Turn.Error)
	}
}

func (r *runtime) endTurn(status string, turnErr *struct {
	Message string `json:"message"`
}) {
	r.mu.Lock()
	interrupted := r.interrupting
	r.inTurn, r.interrupting, r.turnID = false, false, ""
	var cancelled []string
	for id := range r.requests {
		cancelled = append(cancelled, id)
	}
	r.requests = map[string]*serverRequest{}
	r.mu.Unlock()
	for _, id := range cancelled {
		r.emit(harness.ApprovalCancelled{ID: id})
	}
	ev := harness.TurnEnded{Status: model.TurnCompleted}
	switch {
	case status == "interrupted" || interrupted:
		ev.Status = model.TurnInterrupted
	case status == "failed":
		ev.Status = model.TurnFailed
		ev.Error = r.lastError
		if turnErr != nil && turnErr.Message != "" {
			ev.Error = turnErr.Message
		}
		if ev.Error == "" {
			ev.Error = "turn failed"
		}
	}
	if t := r.usageTotal; t != nil {
		b := r.usageBase
		if b == nil {
			b = &usage{}
		}
		ev.Usage = &model.Usage{
			InputTokens: t.InputTokens - b.InputTokens, OutputTokens: t.OutputTokens - b.OutputTokens,
			CacheReadTokens: t.CachedInputTokens - b.CachedInputTokens, CacheWriteTokens: t.CacheWriteTokens - b.CacheWriteTokens,
		}
	}
	r.lastError = ""
	r.items = map[string]*model.Item{}
	r.plan = nil
	r.emit(ev)
}

func statusOf(s string, completed bool) model.ItemStatus {
	switch s {
	case "completed":
		return model.ItemCompleted
	case "failed", "declined":
		return model.ItemFailed
	case "inProgress", "in_progress":
		return model.ItemInProgress
	}
	if completed {
		return model.ItemCompleted
	}
	return model.ItemInProgress
}

func (r *runtime) onItem(ti threadItem, completed bool) {
	it := r.items[ti.ID]
	if it == nil {
		it = &model.Item{ID: ti.ID}
		r.items[ti.ID] = it
	}
	st := statusOf(ti.Status, completed)
	switch ti.Type {
	case "userMessage", "hookPrompt":
		return
	case "agentMessage":
		it.Kind, it.Status = model.ItemAssistantMessage, st
		if ti.Text != "" || completed {
			it.Text = ti.Text
		}
		if strings.TrimSpace(it.Text) == "" {
			return // shown once text streams in
		}
	case "reasoning":
		it.Kind, it.Status = model.ItemReasoning, st
		text := strings.Join(ti.Summary, "\n\n")
		if text == "" {
			var parts []string
			json.Unmarshal(ti.Content, &parts)
			text = strings.Join(parts, "\n\n")
		}
		if text != "" {
			it.Text = text
		}
		if strings.TrimSpace(it.Text) == "" {
			return // nothing to show yet
		}
	case "plan":
		it.Kind, it.Status, it.Plan = model.ItemPlan, model.ItemCompleted, &model.Plan{Text: ti.Text}
	case "commandExecution":
		it.Kind, it.Status = model.ItemToolCall, st
		if it.Tool == nil {
			it.Tool = &model.ToolCall{Name: "shell", Kind: commandKind(ti.CommandActions)}
		}
		it.Tool.Title = commandTitle(ti.Command, ti.CommandActions)
		it.Tool.Input, _ = json.Marshal(map[string]any{"command": ti.Command})
		if ti.AggregatedOutput != nil {
			it.Tool.Output = *ti.AggregatedOutput
		}
		it.Tool.ExitCode = ti.ExitCode
	case "fileChange":
		it.Kind, it.Status = model.ItemToolCall, st
		if it.Tool == nil {
			it.Tool = &model.ToolCall{Name: "apply_patch", Kind: model.ToolEdit}
		}
		var paths []string
		var diff strings.Builder
		for _, c := range ti.Changes {
			paths = append(paths, r.rel(c.Path))
			diff.WriteString(c.Diff)
		}
		it.Tool.Paths = paths
		it.Tool.Output = diff.String()
		switch len(paths) {
		case 0:
			it.Tool.Title = "Edit files"
		case 1:
			it.Tool.Title = "Edit " + paths[0]
		default:
			it.Tool.Title = fmt.Sprintf("Edit %d files", len(paths))
		}
	case "mcpToolCall", "dynamicToolCall":
		it.Kind, it.Status = model.ItemToolCall, st
		name := ti.Tool
		if ti.Server != "" {
			name = ti.Server + "." + ti.Tool
		}
		it.Tool = &model.ToolCall{Name: name, Kind: model.ToolOther, Title: name, Input: ti.Arguments}
		if len(ti.Result) > 0 && string(ti.Result) != "null" {
			it.Tool.Output = string(ti.Result)
		}
		if len(ti.Error) > 0 && string(ti.Error) != "null" {
			it.Tool.Output = string(ti.Error)
		}
	case "webSearch":
		it.Kind, it.Status = model.ItemToolCall, st
		it.Tool = &model.ToolCall{Name: "web_search", Kind: model.ToolFetch, Title: "Search " + ti.Query}
	case "collabAgentToolCall", "subAgentActivity":
		it.Kind, it.Status = model.ItemToolCall, st
		it.Tool = &model.ToolCall{Name: "agent", Kind: model.ToolThink, Title: "Sub-agent"}
	case "contextCompaction":
		it.Kind, it.Status, it.Text = model.ItemNotice, model.ItemCompleted, "Context compacted"
	case "enteredReviewMode", "exitedReviewMode":
		it.Kind, it.Status, it.Text = model.ItemNotice, model.ItemCompleted, ti.Review
	default:
		return
	}
	r.emit(harness.ItemEvent{Item: cloneItem(it)})
}

func (r *runtime) rel(p string) string {
	if rel, err := filepath.Rel(r.cwd, p); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return p
}

func commandKind(actions []commandAction) model.ToolKind {
	if len(actions) == 1 {
		switch actions[0].Type {
		case "read":
			return model.ToolRead
		case "listFiles", "search":
			return model.ToolSearch
		}
	}
	return model.ToolExecute
}

// commandTitle prefers the parsed command over the `/bin/zsh -lc "..."` wrapper.
func commandTitle(cmd string, actions []commandAction) string {
	if len(actions) > 0 && actions[0].Command != "" {
		parts := make([]string, 0, len(actions))
		for _, a := range actions {
			parts = append(parts, a.Command)
		}
		cmd = strings.Join(parts, " && ")
	}
	line, _, more := strings.Cut(cmd, "\n")
	if len(line) > 160 {
		line = line[:160] + "…"
	} else if more {
		line += " …"
	}
	return line
}

func cloneItem(it *model.Item) model.Item {
	c := *it
	if it.Tool != nil {
		t := *it.Tool
		c.Tool = &t
	}
	return c
}

// Server→client requests (read loop): approvals and questions.
func (r *runtime) onRequest(id json.RawMessage, method string, raw json.RawMessage) {
	var p struct {
		ItemID      string          `json:"itemId"`
		Reason      string          `json:"reason"`
		Command     string          `json:"command"`
		Actions     []commandAction `json:"commandActions"`
		GrantRoot   string          `json:"grantRoot"`
		Decisions   json.RawMessage `json:"availableDecisions"`
		Permissions json.RawMessage `json:"permissions"`
		Questions   []struct {
			ID       string `json:"id"`
			Header   string `json:"header"`
			Question string `json:"question"`
			Options  []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	}
	json.Unmarshal(raw, &p)
	req := &serverRequest{id: id, method: method, params: raw}
	ap := model.Approval{ToolItemID: p.ItemID, Detail: p.Reason}
	allowSession := model.ApprovalOption{ID: "allow_session", Label: "Allow for session", Kind: model.OptionAllowSession}
	std := []model.ApprovalOption{
		{ID: "allow", Label: "Allow", Kind: model.OptionAllowOnce}, allowSession,
		{ID: "deny", Label: "Deny", Kind: model.OptionDeny},
	}
	switch method {
	case "item/commandExecution/requestApproval":
		ap.ToolName = "shell"
		ap.Title = commandTitle(p.Command, p.Actions)
		ap.Input, _ = json.Marshal(map[string]any{"command": p.Command})
		ap.Options = std
		if len(p.Decisions) > 0 && !strings.Contains(string(p.Decisions), `"acceptForSession"`) {
			ap.Options = []model.ApprovalOption{std[0], std[2]}
		}
	case "item/fileChange/requestApproval":
		ap.ToolName = "apply_patch"
		ap.Title = "Apply file changes"
		if it := r.items[p.ItemID]; it != nil && it.Tool != nil {
			ap.Title = it.Tool.Title
			ap.Input, _ = json.Marshal(map[string]any{"paths": it.Tool.Paths})
		}
		if p.GrantRoot != "" {
			ap.Detail = strings.TrimSpace(ap.Detail + " (grants write access to " + p.GrantRoot + ")")
		}
		ap.Options = std
	case "item/permissions/requestApproval":
		ap.ToolName = "permissions"
		ap.Title = "Grant additional permissions"
		ap.Input = p.Permissions
		ap.Options = std
	case "item/tool/requestUserInput":
		ap.Special, ap.Title = model.SpecialQuestion, "Codex has a question"
		req.question = map[string]string{}
		for _, q := range p.Questions {
			mq := model.Question{Question: q.Question, Header: q.Header}
			for _, o := range q.Options {
				mq.Options = append(mq.Options, model.QuestionOption{Label: o.Label, Description: o.Description})
			}
			ap.Questions = append(ap.Questions, mq)
			req.question[q.Question] = q.ID
		}
		ap.Options = []model.ApprovalOption{{ID: "allow", Label: "Submit", Kind: model.OptionAllowOnce}, {ID: "deny", Label: "Skip", Kind: model.OptionDeny}}
	case "mcpServer/elicitation/request":
		r.conn.Reply(id, map[string]any{"action": "decline"})
		return
	default:
		r.conn.ReplyError(id, -32601, "remote-agent does not support "+method)
		return
	}
	if it := r.items[p.ItemID]; it != nil && it.Kind == model.ItemToolCall {
		it.Status = model.ItemPending
		r.emit(harness.ItemEvent{Item: cloneItem(it)})
	}
	r.mu.Lock()
	r.nextReq++
	aid := fmt.Sprintf("req-%d", r.nextReq)
	r.requests[aid] = req
	r.mu.Unlock()
	r.emit(harness.ApprovalEvent{ID: aid, Approval: ap})
}
