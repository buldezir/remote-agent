package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"remote-agent/internal/harness"
	"remote-agent/internal/harness/native"
	"remote-agent/internal/model"
)

// Codex keeps each thread as a rollout, JSONL in its home folder:
// ~/.codex/sessions/YYYY/MM/DD/rollout-<time>-<thread id>.jsonl. Its first
// line is the session_meta, with the folder the thread runs in. The
// response_item lines are the conversation as the model sees it (prompts,
// replies, reasoning summaries, tool calls and their output), and event_msg
// lines mark where turns start and end.

// radOriginator is how threads that rad starts are marked: the name rad
// gives itself in initialize.
const radOriginator = "remote-agent"

// home is Codex's folder: CODEX_HOME as the harness's config or rad's
// environment sets it, else ~/.codex.
func (h *Harness) home() string {
	if d := h.cmd.Env["CODEX_HOME"]; d != "" {
		return d
	}
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

func (h *Harness) rollouts() []native.File {
	paths, _ := filepath.Glob(filepath.Join(h.home(), "sessions", "*", "*", "*", "rollout-*.jsonl"))
	return native.Newest(paths)
}

// maxRollouts is how many of the newest rollouts a listing looks into for
// the folder's.
const maxRollouts = 2000

type line struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type sessionMeta struct {
	ID           string          `json:"id"`
	Cwd          string          `json:"cwd"`
	Originator   string          `json:"originator"`
	Source       json.RawMessage `json:"source"`
	ThreadSource string          `json:"thread_source"`
}

// subagent reports whether a thread is a sub-agent's, which Codex marks in
// its source.
func (m *sessionMeta) subagent() bool {
	return m.ThreadSource == "subagent" || bytes.Contains(m.Source, []byte("subagent"))
}

type contentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL string `json:"image_url"`
}

// item is a response_item's payload.
type item struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	Summary   []contentPart   `json:"summary"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Input     string          `json:"input"`
	CallID    string          `json:"call_id"`
	Output    json.RawMessage `json:"output"`
	Status    string          `json:"status"`
	Action    *struct {
		Command []string `json:"command"`
		Query   string   `json:"query"`
	} `json:"action"`
	Meta *struct {
		TurnID string   `json:"turn_id"`
		Kinds  []string `json:"content_item_kinds"`
	} `json:"internal_chat_message_metadata_passthrough"`
}

func (it *item) turnID() string {
	if it.Meta == nil {
		return ""
	}
	return it.Meta.TurnID
}

func (it *item) parts() []contentPart {
	var ps []contentPart
	json.Unmarshal(it.Content, &ps)
	return ps
}

var leadingTag = regexp.MustCompile(`^<([a-zA-Z][a-zA-Z0-9_-]*)[ >]`)

// prompt is a user message's prompt: its text and images, leaving out what
// Codex adds for the model, such as the environment and AGENTS.md. Codex
// marks those parts' kinds; older versions don't, and wrap them in tags.
func (it *item) prompt() (string, []string, bool) {
	if it.Type != "message" || it.Role != "user" {
		return "", nil, false
	}
	var text []string
	var images []string
	for i, p := range it.parts() {
		if it.Meta != nil && i < len(it.Meta.Kinds) && !strings.HasPrefix(it.Meta.Kinds[i], "user") {
			continue
		}
		switch p.Type {
		case "input_text", "text":
			t := strings.TrimSpace(p.Text)
			if t == "" || leadingTag.MatchString(t) || strings.HasPrefix(t, "# AGENTS.md instructions") {
				continue
			}
			text = append(text, t)
		case "input_image":
			images = append(images, p.ImageURL)
		}
	}
	return strings.Join(text, "\n\n"), images, len(text) > 0 || len(images) > 0
}

func (it *item) text() string {
	var b []string
	for _, p := range it.parts() {
		if p.Type == "output_text" || p.Type == "text" {
			b = append(b, p.Text)
		}
	}
	return strings.Join(b, "")
}

var (
	metas     native.Cache[sessionMeta]
	summaries native.Cache[native.Summary]
)

// ListNative lists the threads Codex kept for dir.
func (h *Harness) ListNative(ctx context.Context, dir string, known map[string]bool) ([]model.ImportableSession, error) {
	dirs := native.Dirs(dir)
	names := h.threadNames()
	out := []model.ImportableSession{}
	for i, f := range h.rollouts() {
		if i == maxRollouts || len(out) == harness.ImportLimit || ctx.Err() != nil {
			break
		}
		m, err := metas.Get(f, func() (sessionMeta, error) { return readMeta(f.Path) })
		if err != nil || !native.SameDir(dirs, m.Cwd) || known[m.ID] {
			continue
		}
		sum, err := summaries.Get(f, func() (native.Summary, error) { return summarize(f.Path) })
		if err != nil {
			continue
		}
		sum.ID, sum.FromRad, sum.Subagent = m.ID, m.Originator == radOriginator, m.subagent()
		if names[m.ID] != "" {
			sum.Title = native.Title(names[m.ID])
		}
		if s, ok := sum.Session(f, known); ok {
			out = append(out, s)
		}
	}
	return out, ctx.Err()
}

// readMeta reads a rollout's first line, its session_meta.
func readMeta(path string) (sessionMeta, error) {
	var m sessionMeta
	err := errors.New("no session_meta")
	native.Lines(path, func(raw []byte) bool {
		var l line
		if json.Unmarshal(raw, &l) == nil && l.Type == "session_meta" && json.Unmarshal(l.Payload, &m) == nil {
			err = nil
		}
		return false
	})
	return m, err
}

// summarize reads what a listing shows of a rollout.
func summarize(path string) (native.Summary, error) {
	var s native.Summary
	err := native.Lines(path, func(raw []byte) bool {
		var l line
		if json.Unmarshal(raw, &l) != nil {
			return true
		}
		switch l.Type {
		case "session_meta":
			s.StartedAt = native.Time(l.Timestamp)
		case "response_item":
			var it item
			if json.Unmarshal(l.Payload, &it) != nil {
				return true
			}
			if text, images, ok := it.prompt(); ok {
				if s.Prompts == 0 {
					s.Prompt = text
					if text == "" && len(images) > 0 {
						s.Prompt = "Image"
					}
				}
				s.Prompts++
			} else if it.Type == "message" && it.Role == "assistant" && strings.TrimSpace(it.text()) != "" {
				s.Replies++
			}
		case "event_msg":
			var ev struct {
				Type       string `json:"type"`
				ThreadName string `json:"thread_name"`
			}
			if json.Unmarshal(l.Payload, &ev) == nil && ev.Type == "thread_name_updated" && ev.ThreadName != "" {
				s.Title = native.Title(ev.ThreadName)
			}
		}
		return true
	})
	return s, err
}

// threadNames are the names the user gave threads, which newer versions of
// Codex keep in session_index.jsonl.
func (h *Harness) threadNames() map[string]string {
	names := map[string]string{}
	native.Lines(filepath.Join(h.home(), "session_index.jsonl"), func(raw []byte) bool {
		var e struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
		}
		if json.Unmarshal(raw, &e) == nil && e.ID != "" && strings.TrimSpace(e.ThreadName) != "" {
			names[e.ID] = strings.TrimSpace(e.ThreadName)
		}
		return true
	})
	return names
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)

// ReadNative reads thread id's conversation.
func (h *Harness) ReadNative(ctx context.Context, dir, id string, images harness.ImageStore) (*harness.Transcript, error) {
	if !idPattern.MatchString(id) {
		return nil, harness.ErrNotFound
	}
	dirs := native.Dirs(dir)
	var path string
	var mod time.Time
	for _, f := range h.rollouts() {
		if strings.HasSuffix(f.Path, "-"+id+".jsonl") {
			path, mod = f.Path, f.Info.ModTime().UTC()
			break
		}
	}
	if path == "" {
		return nil, harness.ErrNotFound
	}
	if m, err := readMeta(path); err != nil || !native.SameDir(dirs, m.Cwd) {
		return nil, harness.ErrNotFound
	}
	t := &transcript{dir: dir, images: images, calls: map[string]string{}}
	err := native.Lines(path, func(raw []byte) bool {
		var l line
		if json.Unmarshal(raw, &l) == nil {
			t.add(&l)
		}
		return ctx.Err() == nil
	})
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	tr := t.b.Transcript(native.Title(cmpOr(h.threadNames()[id], t.name)), t.model)
	tr.UpdatedAt = mod
	return tr, nil
}

// transcript turns a rollout's lines into a harness.Transcript.
type transcript struct {
	b      native.Builder
	dir    string
	images harness.ImageStore
	model  string
	turnID string            // the current turn's, where Codex says
	calls  map[string]string // call_id -> the tool's name
	plan   string            // the current turn's plan item
	name   string            // the user's name for the thread
}

func (t *transcript) add(l *line) {
	at := native.Time(l.Timestamp)
	switch l.Type {
	case "turn_context":
		var c struct {
			TurnID string `json:"turn_id"`
			Model  string `json:"model"`
		}
		json.Unmarshal(l.Payload, &c)
		if c.Model != "" {
			t.model = c.Model
		}
		if c.TurnID != "" {
			t.setTurnID(c.TurnID)
		}
	case "compacted":
		t.b.Add(model.Item{Kind: model.ItemNotice, Status: model.ItemCompleted, Text: "Context compacted"}, at)
	case "event_msg":
		t.event(l.Payload, at)
	case "response_item":
		var it item
		if json.Unmarshal(l.Payload, &it) == nil {
			t.item(&it, at)
		}
	}
}

// setTurnID notes the current turn's id, which a fork goes back to.
func (t *transcript) setTurnID(id string) {
	t.turnID = id
	if turn := t.b.Turn(); turn != nil && turn.ForkPoint == "" {
		turn.ForkPoint = id
	}
}

func (t *transcript) event(raw json.RawMessage, at time.Time) {
	var ev struct {
		Type       string `json:"type"`
		TurnID     string `json:"turn_id"`
		Message    string `json:"message"`
		ThreadName string `json:"thread_name"`
	}
	if json.Unmarshal(raw, &ev) != nil {
		return
	}
	switch ev.Type {
	case "task_started":
		if ev.TurnID != "" {
			t.setTurnID(ev.TurnID)
		}
	case "task_complete":
		t.b.End(model.TurnCompleted, "", at)
		t.turnID = ""
	case "turn_aborted":
		t.b.End(model.TurnInterrupted, "", at)
		t.turnID = ""
	case "error":
		if ev.Message != "" && t.b.InTurn() {
			t.b.Add(model.Item{Kind: model.ItemError, Status: model.ItemCompleted, Text: ev.Message}, at)
			t.b.End(model.TurnFailed, ev.Message, at)
		}
	case "context_compacted":
		t.b.Add(model.Item{Kind: model.ItemNotice, Status: model.ItemCompleted, Text: "Context compacted"}, at)
	case "thread_name_updated":
		if ev.ThreadName != "" {
			t.name = ev.ThreadName
		}
	}
}

func (t *transcript) item(it *item, at time.Time) {
	if text, urls, ok := it.prompt(); ok {
		var refs []model.ImageRef
		for _, u := range urls {
			if ref, ok := t.keep(u); ok {
				refs = append(refs, ref)
			}
		}
		// A prompt in the turn that runs was steered into it.
		if turn := t.b.Turn(); turn != nil && it.turnID() != "" && it.turnID() == turn.ForkPoint {
			t.b.Add(model.Item{Kind: model.ItemUserMessage, Status: model.ItemCompleted, Text: text, Images: refs}, at)
			return
		}
		t.b.Prompt(text, refs, at)
		t.plan = ""
		if id := cmpOr(it.turnID(), t.turnID); id != "" {
			t.setTurnID(id)
		}
		return
	}
	switch it.Type {
	case "message":
		if it.Role == "assistant" && strings.TrimSpace(it.text()) != "" {
			t.b.Add(model.Item{Kind: model.ItemAssistantMessage, Status: model.ItemCompleted, Text: it.text()}, at)
		}
	case "reasoning":
		var text []string
		for _, p := range it.Summary {
			if strings.TrimSpace(p.Text) != "" {
				text = append(text, p.Text)
			}
		}
		if len(text) == 0 {
			for _, p := range it.parts() {
				if strings.TrimSpace(p.Text) != "" {
					text = append(text, p.Text)
				}
			}
		}
		if len(text) > 0 {
			t.b.Add(model.Item{Kind: model.ItemReasoning, Status: model.ItemCompleted, Text: strings.Join(text, "\n\n")}, at)
		}
	case "function_call", "custom_tool_call", "local_shell_call":
		t.call(it, at)
	case "web_search_call":
		q := ""
		if it.Action != nil {
			q = it.Action.Query
		}
		t.b.Add(model.Item{Kind: model.ItemToolCall, Status: model.ItemCompleted,
			Tool: &model.ToolCall{Name: "web_search", Kind: model.ToolFetch, Title: strings.TrimSpace("Search " + q)}}, at)
	case "function_call_output", "custom_tool_call_output":
		t.output(it, at)
	}
}

func (t *transcript) keep(url string) (model.ImageRef, bool) {
	if t.images == nil || !strings.HasPrefix(url, "data:") {
		return model.ImageRef{}, false
	}
	ref, err := t.images.PutBase64(url)
	return ref, err == nil
}

func (t *transcript) call(it *item, at time.Time) {
	t.calls[it.CallID] = it.Name
	var args map[string]any
	json.Unmarshal([]byte(it.Arguments), &args)
	str := func(k string) string { s, _ := args[k].(string); return s }
	tool := &model.ToolCall{Name: it.Name, Kind: model.ToolOther}
	if it.Arguments != "" && json.Valid([]byte(it.Arguments)) {
		tool.Input = json.RawMessage(it.Arguments)
	}
	switch {
	case it.Type == "local_shell_call" || it.Name == "shell" || it.Name == "container.exec":
		var cmd []string
		if it.Action != nil {
			cmd = it.Action.Command
		} else if c, ok := args["command"].([]any); ok {
			for _, a := range c {
				s, _ := a.(string)
				cmd = append(cmd, s)
			}
		} else if s := str("command"); s != "" {
			cmd = []string{s}
		}
		tool.Name, tool.Kind, tool.Title = "shell", model.ToolExecute, commandTitle(shellLine(cmd), nil)
	case it.Name == "exec_command" || it.Name == "shell_command":
		tool.Kind, tool.Title = model.ToolExecute, commandTitle(cmpOr(str("cmd"), str("command")), nil)
	case it.Name == "exec":
		tool.Kind, tool.Title = model.ToolExecute, commandTitle(it.Input, nil)
	case it.Name == "apply_patch":
		patch := cmpOr(it.Input, str("input"))
		tool.Kind, tool.Output = model.ToolEdit, patch
		tool.Paths = patchPaths(patch, t.dir)
		switch len(tool.Paths) {
		case 0:
			tool.Title = "Edit files"
		case 1:
			tool.Title = "Edit " + tool.Paths[0]
		default:
			tool.Title = fmt.Sprintf("Edit %d files", len(tool.Paths))
		}
	case it.Name == "update_plan":
		var p struct {
			Plan []struct {
				Step   string `json:"step"`
				Status string `json:"status"`
			} `json:"plan"`
		}
		json.Unmarshal([]byte(it.Arguments), &p)
		plan := &model.Plan{}
		for _, s := range p.Plan {
			plan.Entries = append(plan.Entries, model.PlanEntry{Content: s.Step, Status: s.Status})
		}
		if t.plan != "" && t.b.Update(t.plan, func(i *model.Item) { i.Plan, i.UpdatedAt = plan, at }) {
			return
		}
		t.plan = "plan:" + it.CallID
		t.b.Add(model.Item{ID: t.plan, Kind: model.ItemPlan, Status: model.ItemCompleted, Plan: plan}, at)
		return
	case it.Name == "view_image":
		p := rel(str("path"), t.dir)
		tool.Kind, tool.Title, tool.Paths = model.ToolRead, "View "+p, []string{p}
	default:
		tool.Title = it.Name
		for _, k := range []string{"description", "path", "query", "url", "command", "cmd"} {
			if s := str(k); s != "" {
				tool.Title += ": " + firstLine(s)
				break
			}
		}
	}
	status := model.ItemInProgress
	if it.CallID == "" {
		status = model.ItemCompleted
	}
	t.b.Add(model.Item{ID: "call:" + it.CallID, Kind: model.ItemToolCall, Status: status, Tool: tool}, at)
}

var exitLine = regexp.MustCompile(`(?m)^(?:Exit code:|Process exited with code) (-?\d+)`)

func (t *transcript) output(it *item, at time.Time) {
	name := t.calls[it.CallID]
	if name == "update_plan" {
		return
	}
	text, refs, exit, failed := t.result(it.Output)
	t.b.Update("call:"+it.CallID, func(i *model.Item) {
		if name != "apply_patch" || failed {
			i.Tool.Output = text
		}
		i.Images, i.UpdatedAt = refs, at
		i.Status = model.ItemCompleted
		if exit != nil {
			i.Tool.ExitCode = exit
			if *exit != 0 {
				i.Status = model.ItemFailed
			}
		}
		if failed {
			i.Status = model.ItemFailed
		}
	})
}

// result reads a tool's output: text, which older versions wrap in JSON with
// the exit code, or content parts with images.
func (t *transcript) result(raw json.RawMessage) (text string, refs []model.ImageRef, exit *int, failed bool) {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		var obj struct {
			Content json.RawMessage `json:"content"`
			Success *bool           `json:"success"`
		}
		if json.Unmarshal(raw, &obj) == nil && obj.Content != nil {
			raw = obj.Content
			failed = obj.Success != nil && !*obj.Success
			if json.Unmarshal(raw, &s) != nil {
				s = ""
			}
		}
		var parts []contentPart
		if s == "" && json.Unmarshal(raw, &parts) == nil {
			var b []string
			for _, p := range parts {
				switch p.Type {
				case "input_text", "text", "output_text":
					b = append(b, p.Text)
				case "input_image":
					if ref, ok := t.keep(p.ImageURL); ok {
						refs = append(refs, ref)
					}
				}
			}
			return strings.Join(b, "\n"), refs, nil, failed
		}
	}
	var wrapped struct {
		Output   *string `json:"output"`
		Metadata struct {
			ExitCode *int `json:"exit_code"`
		} `json:"metadata"`
	}
	if strings.HasPrefix(strings.TrimSpace(s), "{") && json.Unmarshal([]byte(s), &wrapped) == nil && wrapped.Output != nil {
		return *wrapped.Output, nil, wrapped.Metadata.ExitCode, failed
	}
	if m := exitLine.FindStringSubmatch(s); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			exit = &n
		}
	}
	return s, nil, exit, failed
}

// shellLine is a command as the user would type it, without the shell that
// runs it (bash -lc '…').
func shellLine(cmd []string) string {
	if len(cmd) == 3 && (cmd[1] == "-lc" || cmd[1] == "-c") && strings.HasSuffix(cmd[0], "sh") {
		return cmd[2]
	}
	return strings.Join(cmd, " ")
}

var patchFile = regexp.MustCompile(`(?m)^\*\*\* (?:Add|Update|Delete) File: (.+)$`)

func patchPaths(patch, dir string) []string {
	var out []string
	for _, m := range patchFile.FindAllStringSubmatch(patch, -1) {
		out = append(out, rel(strings.TrimSpace(m[1]), dir))
	}
	return out
}

func rel(p, dir string) string {
	if r, err := filepath.Rel(dir, p); err == nil && filepath.IsAbs(p) && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}

func firstLine(s string) string { return commandTitle(strings.TrimSpace(s), nil) }

func cmpOr(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
