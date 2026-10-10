// Package acp drives any agent that speaks the Agent Client Protocol
// (https://agentclientprotocol.com) over stdio: Cursor (`cursor-agent acp`),
// OpenCode (`opencode acp`), Gemini (`gemini --acp`) and others.
//
// rad advertises no fs/terminal client capabilities, so agents use their own
// tools and only ask us for permission decisions.
package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"remote-agent/internal/config"
	"remote-agent/internal/harness"
	"remote-agent/internal/harness/jsonrpc"
	"remote-agent/internal/harness/proc"
	"remote-agent/internal/model"
)

type Harness struct {
	agent config.ACPAgent

	mu      sync.Mutex
	learned *learned // modes/models seen in the last session/new
}

type learned struct {
	modes       []model.Choice
	defaultMode string
	models      []model.Choice
	efforts     []model.Choice
}

func New(a config.ACPAgent) *Harness {
	if a.Name == "" {
		a.Name = a.ID
	}
	return &Harness{agent: a}
}

func (h *Harness) ID() string { return "acp:" + h.agent.ID }

func (h *Harness) spec(cwd string) proc.Spec {
	return proc.Spec{Command: h.agent.Command, Args: h.agent.Args, Dir: cwd, Env: h.agent.Env}
}

type initializeResult struct {
	ProtocolVersion   int `json:"protocolVersion"`
	AgentCapabilities struct {
		LoadSession        bool `json:"loadSession"`
		PromptCapabilities struct {
			Image bool `json:"image"`
		} `json:"promptCapabilities"`
		SessionCapabilities struct {
			Resume *json.RawMessage `json:"resume"`
		} `json:"sessionCapabilities"`
	} `json:"agentCapabilities"`
	AuthMethods []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"authMethods"`
	AgentInfo *struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"agentInfo"`
}

func initialize(ctx context.Context, c *jsonrpc.Conn) (*initializeResult, error) {
	var res initializeResult
	err := c.Call(ctx, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs":       map[string]bool{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]string{"name": "remote-agent", "title": "Remote Agent", "version": "0.1"},
	}, &res)
	return &res, err
}

func (h *Harness) Probe(ctx context.Context) model.HarnessInfo {
	info := model.HarnessInfo{
		ID: h.ID(), Name: h.agent.Name, Protocol: "acp",
		Caps: model.HarnessCaps{Interrupt: true},
	}
	if _, err := exec.LookPath(h.agent.Command); err != nil {
		info.Hint = fmt.Sprintf("`%s` not found in PATH", h.agent.Command)
		return info
	}
	info.Installed = true
	p, err := proc.Start(h.spec(""))
	if err != nil {
		info.Hint = err.Error()
		return info
	}
	defer p.Stop(time.Second)
	c := jsonrpc.New(p, "2.0", jsonrpc.Handler{})
	go c.Run()
	res, err := initialize(ctx, c)
	if err != nil {
		info.Installed = false
		info.Hint = "ACP initialize failed: " + err.Error()
		if tail := strings.TrimSpace(p.StderrTail()); tail != "" {
			info.Hint += ": " + lastLine(tail)
		}
		return info
	}
	if res.AgentInfo != nil {
		info.Version = res.AgentInfo.Version
	}
	if info.Version == "" {
		info.Version, _ = proc.Version(ctx, h.agent.Command, "--version")
	}
	// Login state is only known once a session is created; Open reports auth errors.
	info.AuthOK = true
	info.Caps.Resume = res.AgentCapabilities.LoadSession || res.AgentCapabilities.SessionCapabilities.Resume != nil
	h.Overlay(&info)
	return info
}

// Overlay adds the modes and models learned from the most recent session,
// which ACP only reveals once a session exists.
func (h *Harness) Overlay(info *model.HarnessInfo) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if l := h.learned; l != nil {
		info.Modes, info.DefaultMode, info.Models, info.Efforts = l.modes, l.defaultMode, l.models, l.efforts
		info.Caps.SetMode = len(l.modes) > 0
		info.Caps.ModelSelect = len(l.models) > 0
	}
}

// sessionSetup is the common shape of session/new, /load and /resume results.
type sessionSetup struct {
	SessionID string `json:"sessionId"`
	Modes     *struct {
		CurrentModeID  string `json:"currentModeId"`
		AvailableModes []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"availableModes"`
	} `json:"modes"`
	ConfigOptions []configOption `json:"configOptions"`
	// The older way to report models (Cursor sends both); config options win.
	Models *modelState `json:"models"`
}

type modelState struct {
	CurrentModelID  string `json:"currentModelId"`
	AvailableModels []struct {
		ModelID string `json:"modelId"`
		Name    string `json:"name"`
	} `json:"availableModels"`
}

type configOption struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Category     string `json:"category"`
	Type         string `json:"type"`
	CurrentValue any    `json:"currentValue"`
	Options      []struct {
		Value       string `json:"value"`
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"options"`
}

func isAuthError(err error) bool {
	var je *jsonrpc.Error
	if errors.As(err, &je) {
		msg := strings.ToLower(je.Message + string(je.Data))
		return je.Code == -32000 || strings.Contains(msg, "auth")
	}
	return false
}

func (h *Harness) Open(ctx context.Context, o harness.OpenOptions) (harness.Runtime, error) {
	spec := h.spec(o.Cwd)
	spec.DiagPath = o.DiagPath
	p, err := proc.Start(spec)
	if err != nil {
		return nil, err
	}
	r := &runtime{h: h, p: p, cwd: o.Cwd, images: o.Images, events: make(chan harness.Event, 512),
		tools: map[string]*model.Item{}, perms: map[string]*permRequest{}, configs: map[string]string{}}
	r.conn = jsonrpc.New(p, "2.0", jsonrpc.Handler{Notify: r.onNotify, Request: r.onRequest})
	go r.conn.Run()
	go r.waitExit()

	fail := func(err error) (harness.Runtime, error) {
		r.closing.Store(true)
		p.Stop(time.Second)
		if tail := strings.TrimSpace(p.StderrTail()); tail != "" && !errors.Is(err, context.DeadlineExceeded) {
			err = fmt.Errorf("%w (%s)", err, lastLine(tail))
		}
		return nil, err
	}
	ictx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	init, err := initialize(ictx, r.conn)
	if err != nil {
		return fail(fmt.Errorf("initialize: %w", err))
	}
	r.imagePrompts = init.AgentCapabilities.PromptCapabilities.Image

	var setup sessionSetup
	base := map[string]any{"cwd": o.Cwd, "mcpServers": []any{}}
	call := func(method string, params map[string]any) error {
		err := r.conn.Call(ictx, method, params, &setup)
		if err != nil && isAuthError(err) && len(init.AuthMethods) > 0 {
			if aerr := r.conn.Call(ictx, "authenticate", map[string]any{"methodId": init.AuthMethods[0].ID}, nil); aerr != nil {
				return fmt.Errorf("authentication required (%s): %w", init.AuthMethods[0].Name, err)
			}
			err = r.conn.Call(ictx, method, params, &setup)
		}
		return err
	}
	switch {
	case o.ResumeID != "" && init.AgentCapabilities.SessionCapabilities.Resume != nil:
		base["sessionId"] = o.ResumeID
		err = call("session/resume", base)
	case o.ResumeID != "" && init.AgentCapabilities.LoadSession:
		// session/load replays the conversation as updates; we already have it.
		base["sessionId"] = o.ResumeID
		r.loading.Store(true)
		err = call("session/load", base)
		r.loading.Store(false)
	case o.ResumeID != "":
		err = errors.New("agent cannot resume sessions")
	default:
		err = call("session/new", base)
		r.instructions = o.Instructions // a resumed session has them already
	}
	if err != nil {
		return fail(err)
	}
	r.sessionID = setup.SessionID
	if r.sessionID == "" {
		r.sessionID = o.ResumeID
	}
	r.applySetup(&setup)
	if r.mode != "" {
		r.emit(harness.ModeChanged{Mode: r.mode})
	}
	if o.Mode != "" && o.Mode != r.mode {
		if err := r.SetMode(ictx, o.Mode); err != nil && o.Log != nil {
			o.Log.Warn("acp: could not set mode", "mode", o.Mode, "err", err)
		}
	}
	if o.Model != "" {
		if err := r.setConfig(ictx, "model", o.Model); err != nil && o.Log != nil {
			o.Log.Warn("acp: could not set model", "model", o.Model, "err", err)
		}
	}
	if o.Effort != "" {
		if err := r.setConfig(ictx, "thought_level", o.Effort); err != nil && o.Log != nil {
			o.Log.Warn("acp: could not set effort", "effort", o.Effort, "err", err)
		}
	}
	r.emit(harness.NativeID{ID: r.sessionID})
	return r, nil
}

// applySetup records modes and models from a session setup response, and
// remembers them on the harness so later probes can offer them.
func (r *runtime) applySetup(s *sessionSetup) {
	l := &learned{}
	if s.Modes != nil {
		r.mode = s.Modes.CurrentModeID
		for _, m := range s.Modes.AvailableModes {
			l.modes = append(l.modes, model.Choice{ID: m.ID, Name: m.Name, Description: m.Description})
		}
		l.defaultMode = s.Modes.CurrentModeID
	}
	r.mu.Lock()
	r.models = s.Models
	r.mu.Unlock()
	r.updateOptions(s.ConfigOptions)
	for _, c := range s.ConfigOptions {
		var choices []model.Choice
		for _, o := range c.Options {
			choices = append(choices, model.Choice{ID: o.Value, Name: o.Name, Description: o.Description})
		}
		cur, _ := c.CurrentValue.(string)
		switch c.Category {
		case "mode":
			if len(l.modes) == 0 {
				l.modes, l.defaultMode, r.mode = choices, cur, cur
			}
		case "model":
			l.models = choices
		case "thought_level":
			l.efforts = choices
		}
	}
	if len(l.modes) == 0 && len(l.models) == 0 && len(l.efforts) == 0 {
		return
	}
	r.h.mu.Lock()
	r.h.learned = l
	r.h.mu.Unlock()
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
