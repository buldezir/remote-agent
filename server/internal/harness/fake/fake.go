// Package fake is a scripted in-process harness for tests and UI development.
//
// Prompt keywords drive behavior:
//
//	"approve"  request approval before a tool call
//	"question" ask a multiple-choice question
//	"write <file>" write <file> in the cwd (used by git tests)
//	"fail"     end the turn with an error
//	"slow"     stream slowly (2s), so interrupts can be tested
package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"remote-agent/internal/harness"
	"remote-agent/internal/model"
)

type Harness struct{}

func (Harness) ID() string { return "fake" }

func (Harness) Probe(context.Context) model.HarnessInfo {
	return model.HarnessInfo{
		ID: "fake", Name: "Fake (scripted)", Protocol: "fake", Installed: true, AuthOK: true, Version: "1",
		Models:      []model.Choice{{ID: "echo", Name: "Echo"}},
		Efforts:     []model.Choice{harness.EffortChoice("low", ""), harness.EffortChoice("medium", ""), harness.EffortChoice("high", "")},
		Modes:       []model.Choice{{ID: "ask", Name: "Ask"}, {ID: "auto", Name: "Auto"}},
		DefaultMode: "ask",
		Caps:        model.HarnessCaps{Resume: true, Interrupt: true, SetMode: true},
	}
}

func (Harness) Open(ctx context.Context, o harness.OpenOptions) (harness.Runtime, error) {
	id := o.ResumeID
	if id == "" {
		id = "fake-" + o.SessionID
	}
	r := &runtime{opts: o, id: id, events: make(chan harness.Event, 256), mode: o.Mode, approvals: map[string]chan harness.Response{}}
	r.events <- harness.NativeID{ID: id}
	// Like Codex: only an id, which clients name from the harness's model list.
	effort := o.Effort
	if effort == "" {
		effort = "medium"
	}
	r.events <- harness.ModelInfo{ID: "echo", Effort: effort}
	return r, nil
}

type runtime struct {
	opts      harness.OpenOptions
	id        string
	events    chan harness.Event
	mu        sync.Mutex
	mode      string
	cancel    context.CancelFunc
	approvals map[string]chan harness.Response
	n         int
	closed    bool
}

func (r *runtime) NativeID() string             { return r.id }
func (r *runtime) Events() <-chan harness.Event { return r.events }

func (r *runtime) emit(e harness.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.events <- e
	}
}

func (r *runtime) Prompt(ctx context.Context, text string) error {
	r.mu.Lock()
	if r.cancel != nil {
		r.mu.Unlock()
		return fmt.Errorf("turn already running")
	}
	tctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.n++
	n := r.n
	r.mu.Unlock()
	go r.turn(tctx, n, text)
	return nil
}

func (r *runtime) turn(ctx context.Context, n int, text string) {
	end := func(st model.TurnStatus, errMsg string) {
		r.mu.Lock()
		r.cancel = nil
		r.mu.Unlock()
		// Only the first turn reports the window, like Claude before its first result.
		var window int64
		if n == 1 {
			window = 200000
		}
		r.emit(harness.ContextUsage{Used: int64(30000 * n), Window: window})
		r.emit(harness.TurnEnded{Status: st, Error: errMsg, Usage: &model.Usage{InputTokens: int64(len(text)), OutputTokens: 42}})
	}
	delay := 30 * time.Millisecond
	if strings.Contains(text, "slow") {
		delay = 200 * time.Millisecond
	}
	key := func(s string) string { return fmt.Sprintf("t%d-%s", n, s) }

	r.emit(harness.ItemEvent{Item: model.Item{ID: key("think"), Kind: model.ItemReasoning, Status: model.ItemCompleted, Text: "Considering: " + text}})

	if strings.Contains(text, "approve") || strings.Contains(text, "write ") {
		file := "fake.txt"
		if _, after, ok := strings.Cut(text, "write "); ok && len(strings.Fields(after)) > 0 {
			file = strings.Fields(after)[0]
		}
		input, _ := json.Marshal(map[string]string{"file_path": file, "content": "hello from fake\n"})
		tool := model.Item{ID: key("tool"), Kind: model.ItemToolCall, Status: model.ItemPending,
			Tool: &model.ToolCall{Name: "Write", Kind: model.ToolEdit, Title: "Write " + file, Input: input, Paths: []string{file}}}
		r.emitItem(tool)
		if r.currentMode() != "auto" {
			resp, ok := r.ask(ctx, key("approval"), model.Approval{
				ToolItemID: tool.ID, ToolName: "Write", Title: "Write " + file, Input: input,
				Options: []model.ApprovalOption{
					{ID: "allow", Label: "Allow", Kind: model.OptionAllowOnce},
					{ID: "always", Label: "Allow for session", Kind: model.OptionAllowSession},
					{ID: "deny", Label: "Deny", Kind: model.OptionDeny},
				},
			})
			if !ok {
				tool.Status = model.ItemFailed
				r.emitItem(tool)
				end(model.TurnInterrupted, "")
				return
			}
			if resp.OptionID == "deny" {
				tool.Status = model.ItemFailed
				tool.Tool.Output = "denied: " + resp.Message
				r.emitItem(tool)
				r.emit(harness.ItemEvent{Item: model.Item{ID: key("msg"), Kind: model.ItemAssistantMessage, Status: model.ItemCompleted, Text: "OK, I won't."}})
				end(model.TurnCompleted, "")
				return
			}
		}
		tool.Status = model.ItemInProgress
		r.emitItem(tool)
		err := os.WriteFile(filepath.Join(r.opts.Cwd, file), []byte("hello from fake\n"), 0o644)
		tool.Status, tool.Tool.Output = model.ItemCompleted, "wrote "+file
		if err != nil {
			tool.Status, tool.Tool.Output = model.ItemFailed, err.Error()
		}
		r.emitItem(tool)
	}

	if strings.Contains(text, "question") {
		resp, ok := r.ask(ctx, key("q"), model.Approval{
			Title: "Pick a color", Special: model.SpecialQuestion,
			Questions: []model.Question{{Question: "Which color?", Header: "Color", Options: []model.QuestionOption{{Label: "Red"}, {Label: "Blue"}}}},
			Options:   []model.ApprovalOption{{ID: "submit", Label: "Submit", Kind: model.OptionAllowOnce}, {ID: "deny", Label: "Skip", Kind: model.OptionDeny}},
		})
		if !ok {
			end(model.TurnInterrupted, "")
			return
		}
		text += fmt.Sprintf(" (answer: %v)", resp.Answers)
	}

	if strings.Contains(text, "fail") {
		end(model.TurnFailed, "fake failure requested")
		return
	}

	reply := "Echo: " + text
	msg := model.Item{ID: key("msg"), Kind: model.ItemAssistantMessage, Status: model.ItemInProgress}
	words := strings.Fields(reply)
	for i, w := range words {
		select {
		case <-ctx.Done():
			msg.Status = model.ItemCompleted
			r.emit(harness.ItemEvent{Item: msg})
			end(model.TurnInterrupted, "")
			return
		case <-time.After(delay):
		}
		if i > 0 {
			msg.Text += " "
		}
		msg.Text += w
		r.emit(harness.ItemEvent{Item: msg})
	}
	msg.Status = model.ItemCompleted
	r.emit(harness.ItemEvent{Item: msg})
	end(model.TurnCompleted, "")
}

// emitItem sends a snapshot of it, since the turn keeps updating its ToolCall.
func (r *runtime) emitItem(it model.Item) {
	if it.Tool != nil {
		t := *it.Tool
		it.Tool = &t
	}
	r.emit(harness.ItemEvent{Item: it})
}

func (r *runtime) currentMode() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mode
}

func (r *runtime) ask(ctx context.Context, id string, a model.Approval) (harness.Response, bool) {
	ch := make(chan harness.Response, 1)
	r.mu.Lock()
	r.approvals[id] = ch
	r.mu.Unlock()
	r.emit(harness.ApprovalEvent{ID: id, Approval: a})
	defer func() {
		r.mu.Lock()
		delete(r.approvals, id)
		r.mu.Unlock()
	}()
	select {
	case resp := <-ch:
		return resp, true
	case <-ctx.Done():
		r.emit(harness.ApprovalCancelled{ID: id})
		return harness.Response{}, false
	}
}

func (r *runtime) Interrupt(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		r.cancel()
	}
	return nil
}

func (r *runtime) Respond(_ context.Context, id string, resp harness.Response) error {
	r.mu.Lock()
	ch, ok := r.approvals[id]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("no pending approval %q", id)
	}
	ch <- resp
	return nil
}

func (r *runtime) SetMode(_ context.Context, mode string) error {
	r.mu.Lock()
	r.mode = mode
	r.mu.Unlock()
	r.emit(harness.ModeChanged{Mode: mode})
	return nil
}

func (r *runtime) Close() error {
	r.Interrupt(context.Background())
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.closed = true
		r.events <- harness.Exited{}
		close(r.events)
	}
	return nil
}
