// Package claude drives Claude Code through its headless stream-json protocol,
// the same control protocol the official Agent SDKs speak:
//
//	claude --input-format stream-json --output-format stream-json --verbose
//	       --include-partial-messages --permission-prompt-tool stdio ...
//
// stdin carries user messages and control requests/responses; stdout carries
// system/assistant/user/stream_event/result messages and control frames.
// Permission prompts arrive as control_request{subtype:"can_use_tool"}.
package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"remote-agent/internal/config"
	"remote-agent/internal/harness"
	"remote-agent/internal/harness/proc"
	"remote-agent/internal/model"
)

type Harness struct {
	cmd config.Command
}

func New(cmd config.Command) *Harness {
	if cmd.Command == "" {
		cmd.Command = "claude"
	}
	return &Harness{cmd: cmd}
}

func (h *Harness) ID() string { return "claude" }

var modes = []model.Choice{
	{ID: "default", Name: "Ask", Description: "Ask before edits and commands"},
	{ID: "acceptEdits", Name: "Accept edits", Description: "Edit files freely; ask before commands"},
	{ID: "plan", Name: "Plan", Description: "Read-only; propose a plan first"},
	{ID: "auto", Name: "Auto", Description: "A classifier approves safe actions; ask for the rest"},
	{ID: "dontAsk", Name: "Don't ask", Description: "Deny anything not pre-approved"},
	{ID: "bypassPermissions", Name: "Bypass permissions", Description: "Never ask. Dangerous."},
}

func (h *Harness) baseArgs() []string {
	return append(append([]string{}, h.cmd.Args...),
		"--output-format", "stream-json", "--input-format", "stream-json", "--verbose",
		"--include-partial-messages", "--permission-prompt-tool", "stdio")
}

func (h *Harness) spec(cwd string, extra ...string) proc.Spec {
	env := map[string]string{"CLAUDE_CODE_ENTRYPOINT": "sdk-go"}
	for k, v := range h.cmd.Env {
		env[k] = v
	}
	return proc.Spec{
		Command: h.cmd.Command, Args: append(h.baseArgs(), extra...), Dir: cwd,
		Env: env, UnsetEnv: []string{"CLAUDECODE", "CLAUDE_CODE_SSE_PORT"},
	}
}

func (h *Harness) Probe(ctx context.Context) model.HarnessInfo {
	info := model.HarnessInfo{
		ID: "claude", Name: "Claude Code", Protocol: "claude", Modes: modes, DefaultMode: "default",
		Commands: []model.Command{harness.Compact},
		Caps:     model.HarnessCaps{Resume: true, Interrupt: true, SetMode: true, ModelSelect: true, FreeModel: true},
	}
	v, err := proc.Version(ctx, h.cmd.Command, "--version")
	if err != nil {
		info.Hint = "Install Claude Code: https://code.claude.com"
		return info
	}
	info.Installed = true
	info.Version = strings.TrimSuffix(v, " (Claude Code)")

	// Initialize a throwaway process to learn models and login state; no API call is made.
	p, err := proc.Start(h.spec(""))
	if err != nil {
		info.Hint = err.Error()
		return info
	}
	defer p.Stop(time.Second)
	c := newConn(p)
	go c.readLoop(nil)
	res, err := c.request(ctx, map[string]any{"subtype": "initialize", "hooks": nil})
	if err != nil {
		info.Hint = "claude failed to start: " + err.Error()
		return info
	}
	var init initResponse
	json.Unmarshal(res, &init)
	info.Models, info.Efforts = init.choices()
	info.AuthOK = init.Account != nil
	if !info.AuthOK {
		info.Hint = "Not logged in: run `claude` on this machine and sign in"
	}
	return info
}

type initResponse struct {
	Models []struct {
		Value                 string   `json:"value"`
		ResolvedModel         string   `json:"resolvedModel"`
		DisplayName           string   `json:"displayName"`
		Description           string   `json:"description"`
		SupportsEffort        bool     `json:"supportsEffort"`
		SupportedEffortLevels []string `json:"supportedEffortLevels"`
	} `json:"models"`
	Account               *json.RawMessage `json:"account"`
	CurrentPermissionMode string           `json:"current_permission_mode"`
}

func (h *Harness) Open(ctx context.Context, o harness.OpenOptions) (harness.Runtime, error) {
	var extra []string
	if o.Mode != "" {
		extra = append(extra, "--permission-mode", o.Mode)
	}
	if o.Model != "" {
		extra = append(extra, "--model", o.Model)
	}
	if o.Effort != "" {
		extra = append(extra, "--effort", o.Effort)
	}
	if o.Instructions != "" {
		extra = append(extra, "--append-system-prompt", o.Instructions)
	}
	nativeID := o.ResumeID
	if nativeID != "" {
		extra = append(extra, "--resume", nativeID)
	} else {
		nativeID = o.SessionID
		extra = append(extra, "--session-id", nativeID)
	}
	spec := h.spec(o.Cwd, extra...)
	spec.DiagPath = o.DiagPath
	p, err := proc.Start(spec)
	if err != nil {
		return nil, err
	}
	r := &runtime{
		conn: newConn(p), cwd: o.Cwd, nativeID: nativeID, mode: o.Mode, images: o.Images,
		events: make(chan harness.Event, 512), approvals: map[string]*pending{},
		counters: map[string]map[string]int{},
	}
	if o.Log != nil {
		r.log = o.Log
	}
	go r.conn.readLoop(r.onFrame)
	go r.waitExit()

	ictx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	raw, err := r.conn.request(ictx, map[string]any{"subtype": "initialize", "hooks": nil})
	if err != nil {
		r.closing.Store(true)
		p.Stop(time.Second)
		if tail := strings.TrimSpace(p.StderrTail()); tail != "" {
			err = fmt.Errorf("%w: %s", err, lastLine(tail))
		}
		return nil, err
	}
	r.emit(harness.NativeID{ID: nativeID})
	var init initResponse
	json.Unmarshal(raw, &init)
	r.reportModel(ictx, &init)
	return r, nil
}

// reportModel emits the model and effort the CLI applied after resolving
// aliases, defaults and settings. CLIs without get_settings report nothing.
func (r *runtime) reportModel(ctx context.Context, init *initResponse) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := r.conn.request(ctx, map[string]any{"subtype": "get_settings"})
	var s struct {
		Applied struct {
			Model  string `json:"model"`
			Effort string `json:"effort"`
		} `json:"applied"`
	}
	if err == nil {
		err = json.Unmarshal(raw, &s)
	}
	if err != nil || s.Applied.Model == "" {
		if r.log != nil {
			r.log.Debug("claude: no applied model settings", "err", err)
		}
		return
	}
	r.emit(harness.ModelInfo{ID: s.Applied.Model, Name: init.modelName(s.Applied.Model), Effort: s.Applied.Effort})
}

// choices lists the models with their effort levels, and the levels of the default model.
func (i *initResponse) choices() (models, efforts []model.Choice) {
	for _, m := range i.Models {
		c := model.Choice{ID: m.Value, Name: m.DisplayName, Description: m.Description}
		if m.SupportsEffort {
			for _, e := range m.SupportedEffortLevels {
				c.Efforts = append(c.Efforts, harness.EffortChoice(e, ""))
			}
		}
		if m.Value == "default" {
			efforts = c.Efforts
		}
		models = append(models, c)
	}
	return models, efforts
}

// modelName returns the display name of a resolved model id, if the CLI listed it.
func (i *initResponse) modelName(id string) string {
	for _, m := range i.Models {
		// "default" is named "Default (recommended)"; the alias it resolves to has the model's name.
		if m.ResolvedModel == id && m.Value != "default" {
			return m.DisplayName
		}
	}
	return ""
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// conn handles framing and control request/response correlation.
type conn struct {
	p       *proc.Proc
	mu      sync.Mutex
	pending map[string]chan controlResult
	nextID  int
	done    chan struct{}
}

type controlResult struct {
	resp json.RawMessage
	err  error
}

func newConn(p *proc.Proc) *conn {
	return &conn{p: p, pending: map[string]chan controlResult{}, done: make(chan struct{})}
}

// request sends a control_request and waits for its control_response.
func (c *conn) request(ctx context.Context, req map[string]any) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := fmt.Sprintf("req_%d_%d", c.nextID, time.Now().UnixNano()%1e6)
	ch := make(chan controlResult, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if err := c.p.WriteJSON(map[string]any{"type": "control_request", "request_id": id, "request": req}); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		return r.resp, r.err
	case <-c.done:
		return nil, errors.New("claude exited")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *conn) respond(requestID string, response any) error {
	return c.p.WriteJSON(map[string]any{
		"type":     "control_response",
		"response": map[string]any{"subtype": "success", "request_id": requestID, "response": response},
	})
}

func (c *conn) respondError(requestID, msg string) error {
	return c.p.WriteJSON(map[string]any{
		"type":     "control_response",
		"response": map[string]any{"subtype": "error", "request_id": requestID, "error": msg},
	})
}

type frame struct {
	Type            string          `json:"type"`
	Subtype         string          `json:"subtype"`
	SessionID       string          `json:"session_id"`
	ParentToolUseID string          `json:"parent_tool_use_id"`
	RequestID       string          `json:"request_id"`
	Request         json.RawMessage `json:"request"`
	Response        *struct {
		Subtype   string          `json:"subtype"`
		RequestID string          `json:"request_id"`
		Response  json.RawMessage `json:"response"`
		Error     string          `json:"error"`
	} `json:"response"`
	Message json.RawMessage `json:"message"`
	Event   json.RawMessage `json:"event"`

	// system/init
	Model          string `json:"model"`
	PermissionMode string `json:"permissionMode"`

	// system/compact_boundary
	UUID            string `json:"uuid"`
	CompactMetadata *struct {
		PostTokens int64 `json:"post_tokens"`
	} `json:"compact_metadata"`

	// result
	IsError      bool    `json:"is_error"`
	Result       string  `json:"result"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Usage        *struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
	Errors     []string `json:"errors"`
	ModelUsage map[string]struct {
		ContextWindow  int64  `json:"contextWindow"`
		CanonicalModel string `json:"canonicalModel"`
	} `json:"modelUsage"`
}

// readLoop parses stdout frames, resolving control responses itself and
// passing everything else to onFrame (which may be nil).
func (c *conn) readLoop(onFrame func(frame, []byte)) {
	defer close(c.done)
	for {
		line, err := c.p.ReadLine()
		if err != nil {
			return
		}
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var f frame
		if err := json.Unmarshal(line, &f); err != nil {
			continue
		}
		if f.Type == "control_response" && f.Response != nil {
			c.mu.Lock()
			ch := c.pending[f.Response.RequestID]
			c.mu.Unlock()
			if ch != nil {
				var err error
				if f.Response.Subtype == "error" {
					err = errors.New(f.Response.Error)
				}
				ch <- controlResult{f.Response.Response, err}
			}
			continue
		}
		if onFrame != nil {
			onFrame(f, line)
		}
	}
}
