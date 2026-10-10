// Package codex drives OpenAI Codex through `codex app-server`, a JSON-RPC
// protocol over stdio (newline-delimited, without the "jsonrpc" field).
//
// Types here cover the subset we use. The full schema can be regenerated with
// `codex app-server generate-json-schema --out <dir>`.
package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"remote-agent/internal/config"
	"remote-agent/internal/harness"
	"remote-agent/internal/harness/jsonrpc"
	"remote-agent/internal/harness/proc"
	"remote-agent/internal/model"
)

type Harness struct {
	cmd config.Command
}

func New(cmd config.Command) *Harness {
	if cmd.Command == "" {
		cmd.Command = "codex"
	}
	return &Harness{cmd: cmd}
}

func (h *Harness) ID() string { return "codex" }

// Modes are presets over (approvalPolicy, sandbox), like Codex's /approvals.
type preset struct {
	approval string
	sandbox  string // thread/start form
	policy   map[string]any
}

var presets = map[string]preset{
	"untrusted": {"untrusted", "workspace-write", map[string]any{"type": "workspaceWrite"}},
	"read-only": {"on-request", "read-only", map[string]any{"type": "readOnly"}},
	"auto":      {"on-request", "workspace-write", map[string]any{"type": "workspaceWrite"}},
	"full":      {"never", "danger-full-access", map[string]any{"type": "dangerFullAccess"}},
}

var modes = []model.Choice{
	{ID: "untrusted", Name: "Ask", Description: "Ask before running commands that aren't known-safe"},
	{ID: "read-only", Name: "Read only", Description: "Read files; ask before any change"},
	{ID: "auto", Name: "Auto", Description: "Edit and run commands in the workspace; ask to go beyond it"},
	{ID: "full", Name: "Full access", Description: "No sandbox, never ask. Dangerous."},
}

func (h *Harness) spec(cwd string) proc.Spec {
	return proc.Spec{Command: h.cmd.Command, Args: append(append([]string{}, h.cmd.Args...), "app-server"), Dir: cwd, Env: h.cmd.Env}
}

type initializeParams struct {
	ClientInfo struct {
		Name    string `json:"name"`
		Title   string `json:"title"`
		Version string `json:"version"`
	} `json:"clientInfo"`
}

func initialize(ctx context.Context, c *jsonrpc.Conn) error {
	var p initializeParams
	p.ClientInfo.Name, p.ClientInfo.Title, p.ClientInfo.Version = "remote-agent", "Remote Agent", "0.1"
	if err := c.Call(ctx, "initialize", p, nil); err != nil {
		return err
	}
	return c.Notify("initialized", nil)
}

func (h *Harness) Probe(ctx context.Context) model.HarnessInfo {
	info := model.HarnessInfo{
		ID: "codex", Name: "Codex", Protocol: "codex", Modes: modes, DefaultMode: "auto",
		Caps: model.HarnessCaps{Resume: true, Interrupt: true, SetMode: true, ModelSelect: true, FreeModel: true},
	}
	v, err := proc.Version(ctx, h.cmd.Command, "--version")
	if err != nil {
		info.Hint = "Install Codex: npm i -g @openai/codex"
		return info
	}
	info.Installed = true
	info.Version = strings.TrimPrefix(v, "codex-cli ")

	p, err := proc.Start(h.spec(""))
	if err != nil {
		info.Hint = err.Error()
		return info
	}
	defer p.Stop(time.Second)
	c := jsonrpc.New(p, "", jsonrpc.Handler{})
	go c.Run()
	if err := initialize(ctx, c); err != nil {
		info.Hint = "codex app-server failed: " + err.Error()
		return info
	}
	var models struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
			Description string `json:"description"`
			Hidden      bool   `json:"hidden"`
			IsDefault   bool   `json:"isDefault"`
			Efforts     []struct {
				ReasoningEffort string `json:"reasoningEffort"`
				Description     string `json:"description"`
			} `json:"supportedReasoningEfforts"`
		} `json:"data"`
	}
	if c.Call(ctx, "model/list", map[string]any{}, &models) == nil {
		for _, m := range models.Data {
			if m.Hidden {
				continue
			}
			c := model.Choice{ID: m.ID, Name: m.DisplayName, Description: m.Description}
			for _, e := range m.Efforts {
				c.Efforts = append(c.Efforts, harness.EffortChoice(e.ReasoningEffort, e.Description))
			}
			if m.IsDefault {
				info.Efforts = c.Efforts
			}
			info.Models = append(info.Models, c)
		}
	}
	var acct struct {
		Account            json.RawMessage `json:"account"`
		RequiresOpenaiAuth bool            `json:"requiresOpenaiAuth"`
	}
	if err := c.Call(ctx, "account/read", map[string]any{}, &acct); err == nil {
		info.AuthOK = !acct.RequiresOpenaiAuth || (len(acct.Account) > 0 && string(acct.Account) != "null")
	}
	if !info.AuthOK {
		info.Hint = "Not logged in: run `codex login` on this machine"
	}
	return info
}

func (h *Harness) Open(ctx context.Context, o harness.OpenOptions) (harness.Runtime, error) {
	mode := o.Mode
	if _, ok := presets[mode]; !ok {
		mode = "auto"
	}
	spec := h.spec(o.Cwd)
	spec.DiagPath = o.DiagPath
	p, err := proc.Start(spec)
	if err != nil {
		return nil, err
	}
	r := &runtime{p: p, cwd: o.Cwd, images: o.Images, mode: mode, model: o.Model, effort: o.Effort, events: make(chan harness.Event, 512),
		items: map[string]*model.Item{}, requests: map[string]*serverRequest{}}
	r.conn = jsonrpc.New(p, "", jsonrpc.Handler{Notify: r.onNotify, Request: r.onRequest})
	go r.conn.Run()
	go r.waitExit()

	fail := func(err error) (harness.Runtime, error) {
		r.closing.Store(true)
		p.Stop(time.Second)
		if tail := strings.TrimSpace(p.StderrTail()); tail != "" {
			err = fmt.Errorf("%w: %s", err, lastLine(tail))
		}
		return nil, err
	}
	ictx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := initialize(ictx, r.conn); err != nil {
		return fail(err)
	}
	pr := presets[mode]
	params := map[string]any{"cwd": o.Cwd, "approvalPolicy": pr.approval, "sandbox": pr.sandbox}
	if o.Model != "" {
		params["model"] = o.Model
	}
	if o.Instructions != "" {
		params["developerInstructions"] = o.Instructions
	}
	var res struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Model           string `json:"model"`
		ReasoningEffort string `json:"reasoningEffort"`
	}
	method := "thread/start"
	if o.ResumeID != "" {
		method = "thread/resume"
		params["threadId"] = o.ResumeID
		params["excludeTurns"] = true
	}
	if err := r.conn.Call(ictx, method, params, &res); err != nil {
		return fail(fmt.Errorf("%s: %w", method, err))
	}
	r.threadID = res.Thread.ID
	r.emit(harness.NativeID{ID: r.threadID})
	if res.Model != "" {
		effort := res.ReasoningEffort
		if o.Effort != "" {
			effort = o.Effort // sent with each turn
		}
		// Display names come from model/list, which clients have via harness.list.
		r.emit(harness.ModelInfo{ID: res.Model, Effort: effort})
	}
	return r, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
