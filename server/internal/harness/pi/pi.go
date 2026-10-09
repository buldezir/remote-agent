// Package pi drives Pi (pi.dev) through `pi --mode rpc`: JSON commands on
// stdin, and responses and events on stdout, one per line.
//
// Pi has no permission system and runs tools without asking. Extensions can
// still ask the user through dialogs (extension_ui_request), which become
// approval cards.
package pi

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
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
		cmd.Command = "pi"
	}
	return &Harness{cmd: cmd}
}

func (h *Harness) ID() string { return "pi" }

// Pi never asks before running a tool. Its one mode says so in the picker.
var modes = []model.Choice{
	{ID: "full", Name: "Full access", Description: "Pi runs tools without asking. Only extensions can ask."},
}

func (h *Harness) spec(cwd string, extra ...string) proc.Spec {
	args := append(append([]string{}, h.cmd.Args...), "--mode", "rpc")
	return proc.Spec{Command: h.cmd.Command, Args: append(args, extra...), Dir: cwd, Env: h.cmd.Env}
}

type piModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	Reasoning     bool   `json:"reasoning"`
	ContextWindow int64  `json:"contextWindow"`
	// A null level is unsupported; xhigh and max exist only when present.
	ThinkingLevelMap map[string]*string `json:"thinkingLevelMap"`
}

// key is the "provider/id" form that --model and set_model accept.
func (m piModel) key() string { return m.Provider + "/" + m.ID }

type state struct {
	Model         *piModel `json:"model"`
	ThinkingLevel string   `json:"thinkingLevel"`
	IsStreaming   bool     `json:"isStreaming"`
	SessionID     string   `json:"sessionId"`
}

var thinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// efforts are the thinking levels m supports, as pi-ai's getSupportedThinkingLevels
// works them out.
func efforts(m piModel) []model.Choice {
	if !m.Reasoning {
		return nil
	}
	var out []model.Choice
	for _, l := range thinkingLevels {
		mapped, ok := m.ThinkingLevelMap[l]
		if ok && mapped == nil || !ok && (l == "xhigh" || l == "max") {
			continue
		}
		out = append(out, harness.EffortChoice(l, ""))
	}
	return out
}

// choices lists models by name, adding the provider to names that several
// providers share.
func choices(ms []piModel) []model.Choice {
	providers := map[string]map[string]bool{}
	for _, m := range ms {
		if providers[m.Name] == nil {
			providers[m.Name] = map[string]bool{}
		}
		providers[m.Name][m.Provider] = true
	}
	out := make([]model.Choice, 0, len(ms))
	for _, m := range ms {
		name := m.Name
		if name == "" {
			name = m.ID
		}
		if len(providers[m.Name]) > 1 {
			name += " (" + m.Provider + ")"
		}
		out = append(out, model.Choice{ID: m.key(), Name: name, Efforts: efforts(m)})
	}
	return out
}

func (h *Harness) Probe(ctx context.Context) model.HarnessInfo {
	info := model.HarnessInfo{
		ID: "pi", Name: "Pi", Protocol: "pi", Modes: modes, DefaultMode: "full",
		Caps: model.HarnessCaps{Resume: true, Interrupt: true, ModelSelect: true, FreeModel: true},
	}
	v, err := version(ctx, h.cmd.Command)
	if err != nil {
		info.Hint = "Install Pi: npm i -g @earendil-works/pi-coding-agent"
		return info
	}
	info.Installed, info.Version = true, v

	p, err := proc.Start(h.spec("", "--no-session"))
	if err != nil {
		info.Hint = err.Error()
		return info
	}
	defer p.Stop(time.Second)
	c := newConn(p, nil)
	go c.readLoop()
	var st state
	if err := c.call(ctx, "get_state", nil, &st); err != nil {
		info.Hint = "pi --mode rpc failed: " + err.Error()
		return info
	}
	var res struct {
		Models []piModel `json:"models"`
	}
	c.call(ctx, "get_available_models", nil, &res)
	info.Models = choices(res.Models)
	if st.Model != nil {
		info.Efforts = efforts(*st.Model)
	}
	info.AuthOK = len(res.Models) > 0
	if !info.AuthOK {
		info.Hint = "No models available: run `pi` and /login, or set a provider's API key"
	}
	return info
}

// version runs `pi --version`. Pi writes warnings about its settings to
// stderr, so only stdout counts.
func version(ctx context.Context, command string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, command, "--version").Output()
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return line, err
}

func (h *Harness) Open(ctx context.Context, o harness.OpenOptions) (harness.Runtime, error) {
	nativeID := o.ResumeID
	if nativeID == "" {
		nativeID = o.SessionID
	}
	// --session-id resumes the session with that id in the cwd's project, or starts it.
	extra := []string{"--session-id", nativeID}
	if o.Model != "" {
		extra = append(extra, "--model", o.Model)
	}
	if o.Effort != "" {
		extra = append(extra, "--thinking", o.Effort)
	}
	spec := h.spec(o.Cwd, extra...)
	spec.DiagPath = o.DiagPath
	p, err := proc.Start(spec)
	if err != nil {
		return nil, err
	}
	r := newRuntime(p, o.Cwd, nativeID)
	go r.c.readLoop()
	go r.waitExit()

	ictx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var st state
	if err := r.c.call(ictx, "get_state", nil, &st); err != nil {
		r.closing.Store(true)
		p.Stop(time.Second)
		if tail := strings.TrimSpace(p.StderrTail()); tail != "" {
			err = fmt.Errorf("%w: %s", err, lastLine(tail))
		}
		return nil, err
	}
	r.emit(harness.NativeID{ID: nativeID})
	r.loopMu.Lock()
	r.effort = st.ThinkingLevel
	if st.Model != nil && !st.Model.Reasoning {
		r.effort = "" // pi says "off" for models that can't think
	}
	if st.Model != nil {
		r.modelID, r.window = st.Model.key(), st.Model.ContextWindow
		// Display names come from get_available_models, which clients have via harness.list.
		r.emit(harness.ModelInfo{ID: r.modelID, Effort: r.effort})
	}
	r.loopMu.Unlock()
	return r, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
