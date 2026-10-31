package claude

import (
	"bytes"
	"context"
	"encoding/json"
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

// Claude Code keeps each session as JSONL in its config folder,
// ~/.claude/projects/<the folder's path, with every other character than
// letters and digits a dash>/<session id>.jsonl, and its sub-agents' in
// <session id>/subagents/. Each line is an entry: prompts, tool results and
// Claude's messages (one content block each) chain by parentUuid, and
// entries without one name the session (ai-title, custom-title, summary).

// radEntrypoint is the entrypoint of the sessions rad starts
// (CLAUDE_CODE_ENTRYPOINT in spec).
const radEntrypoint = "sdk-go"

// configDir is Claude Code's folder: CLAUDE_CONFIG_DIR as the harness's
// config or rad's environment sets it, else ~/.claude.
func (h *Harness) configDir() string {
	if d := h.cmd.Env["CLAUDE_CONFIG_DIR"]; d != "" {
		return d
	}
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

var notAlnum = regexp.MustCompile(`[^a-zA-Z0-9]`)

// maxSlug is how long Claude Code lets a folder's name get; it shortens a
// longer one and adds a hash, which matches by its start here.
const maxSlug = 200

// projectDirs are the folders Claude Code keeps dir's sessions in.
func (h *Harness) projectDirs(dir string) []string {
	root := filepath.Join(h.configDir(), "projects")
	var out []string
	for _, d := range native.Dirs(dir) {
		slug := notAlnum.ReplaceAllString(d, "-")
		if len(slug) <= maxSlug {
			out = append(out, filepath.Join(root, slug))
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(root, slug[:maxSlug]+"*"))
		out = append(out, matches...)
	}
	return out
}

// entry is a line of a session file, with the fields an import reads.
type entry struct {
	Type              string          `json:"type"`
	Subtype           string          `json:"subtype"`
	UUID              string          `json:"uuid"`
	ParentUUID        string          `json:"parentUuid"`
	LogicalParentUUID string          `json:"logicalParentUuid"`
	SessionID         string          `json:"sessionId"`
	Timestamp         string          `json:"timestamp"`
	Entrypoint        string          `json:"entrypoint"`
	IsSidechain       bool            `json:"isSidechain"`
	IsMeta            bool            `json:"isMeta"`
	IsCompactSummary  bool            `json:"isCompactSummary"`
	IsAPIErrorMessage bool            `json:"isApiErrorMessage"`
	Message           json.RawMessage `json:"message"`

	AITitle     string `json:"aiTitle"`
	CustomTitle string `json:"customTitle"`
	Summary     string `json:"summary"`
}

// worth reports whether a line can matter to a listing, without decoding
// it: prompts, Claude's text and the session's names, not tool results or
// the API requests Claude logs.
func worth(line []byte) bool {
	switch {
	case bytes.Contains(line, []byte(`"type":"user"`)):
		return !bytes.Contains(line, []byte(`"tool_use_id":`))
	case bytes.Contains(line, []byte(`"type":"assistant"`)):
		return bytes.Contains(line, []byte(`"type":"text"`))
	}
	return bytes.Contains(line, []byte(`"type":"ai-title"`)) || bytes.Contains(line, []byte(`"type":"custom-title"`)) ||
		bytes.Contains(line, []byte(`"type":"summary"`))
}

// titles keeps the names a session's entries give it, the user's first.
type titles struct{ custom, ai, summary string }

func (t *titles) note(e *entry) {
	switch e.Type {
	case "custom-title":
		t.custom = cmpOr(strings.TrimSpace(e.CustomTitle), t.custom)
	case "ai-title":
		t.ai = cmpOr(strings.TrimSpace(e.AITitle), t.ai)
	case "summary":
		t.summary = cmpOr(strings.TrimSpace(e.Summary), t.summary)
	}
}

func (t titles) best() string { return native.Title(cmpOr(t.custom, t.ai, t.summary)) }

func cmpOr(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

var summaries native.Cache[native.Summary]

// ListNative lists the sessions Claude Code kept for dir.
func (h *Harness) ListNative(ctx context.Context, dir string, known map[string]bool) ([]model.ImportableSession, error) {
	var paths []string
	for _, d := range h.projectDirs(dir) {
		ps, _ := filepath.Glob(filepath.Join(d, "*.jsonl"))
		paths = append(paths, ps...)
	}
	out := []model.ImportableSession{}
	for _, f := range native.Newest(paths) {
		if len(out) == harness.ImportLimit || ctx.Err() != nil {
			break
		}
		id := strings.TrimSuffix(filepath.Base(f.Path), ".jsonl")
		if known[id] {
			continue
		}
		sum, err := summaries.Get(f, func() (native.Summary, error) { return summarize(f.Path, id) })
		if err != nil {
			continue
		}
		if s, ok := sum.Session(f, known); ok {
			out = append(out, s)
		}
	}
	return out, ctx.Err()
}

// summarize reads what a listing shows of a session file.
func summarize(path, id string) (native.Summary, error) {
	s := native.Summary{ID: id, Subagent: strings.HasPrefix(id, "agent-")}
	var names titles
	replies := map[string]bool{}
	sidechain := true
	err := native.Lines(path, func(line []byte) bool {
		if !worth(line) {
			return true
		}
		var e entry
		if json.Unmarshal(line, &e) != nil {
			return true
		}
		names.note(&e)
		if e.Type != "user" && e.Type != "assistant" {
			return true
		}
		if !e.IsSidechain {
			sidechain = false
		}
		if e.IsSidechain {
			return true
		}
		if e.Type == "assistant" {
			var m message
			if !e.IsAPIErrorMessage && json.Unmarshal(e.Message, &m) == nil && hasText(&m) {
				replies[cmpOr(m.ID, e.UUID)] = true
			}
			return true
		}
		p := promptOf(&e)
		if p.kind != promptUser && p.kind != promptCommand {
			return true
		}
		if s.Prompts == 0 {
			s.Prompt, s.StartedAt = p.text, native.Time(e.Timestamp)
			s.FromRad = e.Entrypoint == radEntrypoint
		}
		s.Prompts++
		return true
	})
	s.Replies = len(replies)
	s.Title = names.best()
	s.Subagent = s.Subagent || sidechain && s.Prompts+s.Replies > 0
	return s, err
}

func hasText(m *message) bool {
	for _, b := range m.blocks() {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			return true
		}
	}
	return false
}

type promptKind int

const (
	promptNone        promptKind = iota // not the user's: tool results, Claude Code's own notes
	promptUser                          // a prompt
	promptCommand                       // a slash command, as /name args
	promptInterrupted                   // the user stopped the turn
	promptTask                          // a background task ended, which starts a turn of Claude's own
)

type prompt struct {
	kind   promptKind
	text   string
	images []contentBlock
}

var (
	commandName = regexp.MustCompile(`(?s)<command-name>\s*(.*?)\s*</command-name>`)
	commandArgs = regexp.MustCompile(`(?s)<command-args>(.*?)</command-args>`)
	taskSummary = regexp.MustCompile(`(?s)<summary>(.*?)</summary>`)
	leadingTag  = regexp.MustCompile(`^<([a-zA-Z][a-zA-Z0-9_-]*)>`)
)

// promptOf tells what a user entry is.
func promptOf(e *entry) prompt {
	if e.IsMeta || e.IsCompactSummary || e.IsSidechain {
		return prompt{}
	}
	var m message
	if json.Unmarshal(e.Message, &m) != nil {
		return prompt{}
	}
	var text strings.Builder
	var images []contentBlock
	for _, b := range m.blocks() {
		switch b.Type {
		case "tool_result":
			return prompt{}
		case "text":
			if text.Len() > 0 {
				text.WriteString("\n\n")
			}
			text.WriteString(b.Text)
		case "image":
			images = append(images, b)
		}
	}
	t := strings.TrimSpace(text.String())
	switch {
	case strings.HasPrefix(t, "[Request interrupted by user"):
		return prompt{kind: promptInterrupted}
	case strings.HasPrefix(t, "<command-name>") || strings.HasPrefix(t, "<command-message>"):
		name := commandName.FindStringSubmatch(t)
		if name == nil {
			return prompt{}
		}
		cmd := "/" + strings.TrimPrefix(name[1], "/")
		if args := commandArgs.FindStringSubmatch(t); args != nil && strings.TrimSpace(args[1]) != "" {
			cmd += " " + strings.TrimSpace(args[1])
		}
		return prompt{kind: promptCommand, text: cmd}
	case strings.HasPrefix(t, "<task-notification>"):
		n := "Background task finished"
		if s := taskSummary.FindStringSubmatch(t); s != nil && strings.TrimSpace(s[1]) != "" {
			n += ": " + strings.TrimSpace(s[1])
		}
		return prompt{kind: promptTask, text: n}
	case leadingTag.MatchString(t):
		return prompt{} // Claude Code's own: command output, bash mode, reminders
	case t == "" && len(images) == 0:
		return prompt{}
	}
	return prompt{kind: promptUser, text: t, images: images}
}

// file finds session id's file among dir's, if it's there.
func (h *Harness) file(dir, id string) (string, error) {
	if !validID(id) {
		return "", harness.ErrNotFound
	}
	for _, d := range h.projectDirs(dir) {
		p := filepath.Join(d, id+".jsonl")
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p, nil
		}
	}
	return "", harness.ErrNotFound
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func validID(id string) bool { return idPattern.MatchString(id) && !strings.Contains(id, "..") }

// ReadNative reads session id's conversation, along the branch it went on
// last: an entry Claude Code went back to (a rewind) leaves the entries
// after it out.
func (h *Harness) ReadNative(ctx context.Context, dir, id string, images harness.ImageStore) (*harness.Transcript, error) {
	path, err := h.file(dir, id)
	if err != nil {
		return nil, err
	}
	var entries []entry
	var names titles
	err = native.Lines(path, func(line []byte) bool {
		var e entry
		if json.Unmarshal(line, &e) == nil {
			names.note(&e)
			// Attachments and the like are links in the chain too.
			if e.UUID != "" && !e.IsSidechain {
				entries = append(entries, e)
			}
		}
		return ctx.Err() == nil
	})
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	t := &transcript{dir: dir, images: images, tools: map[string]string{}}
	for _, e := range branch(entries) {
		t.add(&e)
	}
	tr := t.b.Transcript(names.best(), t.model)
	if fi, err := os.Stat(path); err == nil {
		tr.UpdatedAt = fi.ModTime().UTC()
	}
	return tr, nil
}

// branch is the entries on the way to the last prompt or message, in order.
// A compaction starts a chain of its own, which goes on from logicalParentUuid.
func branch(entries []entry) []entry {
	byID := make(map[string]int, len(entries))
	leaf := -1
	for i, e := range entries {
		byID[e.UUID] = i
		if e.Type == "user" || e.Type == "assistant" {
			leaf = i
		}
	}
	if leaf < 0 {
		return entries
	}
	on := map[int]bool{}
	for i, ok := leaf, true; ok && !on[i]; {
		on[i] = true
		parent := cmpOr(entries[i].ParentUUID, entries[i].LogicalParentUUID)
		i, ok = byID[parent]
	}
	out := make([]entry, 0, len(on))
	for i, e := range entries {
		if on[i] {
			out = append(out, e)
		}
	}
	return out
}

// transcript turns entries into a harness.Transcript.
type transcript struct {
	b      native.Builder
	dir    string
	images harness.ImageStore
	model  string
	tools  map[string]string // tool_use id -> the tool's name
	plan   string            // the current turn's plan item
}

func (t *transcript) keep(sourceType, data string) (model.ImageRef, bool) {
	if t.images == nil || sourceType != "base64" {
		return model.ImageRef{}, false
	}
	ref, err := t.images.PutBase64(data)
	return ref, err == nil
}

func (t *transcript) add(e *entry) {
	at := native.Time(e.Timestamp)
	switch e.Type {
	case "system":
		if e.Subtype == "compact_boundary" {
			t.b.Add(model.Item{Kind: model.ItemNotice, Status: model.ItemCompleted, Text: "Context compacted"}, at)
		}
	case "user":
		t.user(e, at)
	case "assistant":
		t.assistant(e, at)
	}
}

func (t *transcript) user(e *entry, at time.Time) {
	p := promptOf(e)
	switch p.kind {
	case promptUser, promptCommand:
		var refs []model.ImageRef
		for _, img := range p.images {
			if img.Source != nil {
				if ref, ok := t.keep(img.Source.Type, img.Source.Data); ok {
					refs = append(refs, ref)
				}
			}
		}
		t.b.Prompt(p.text, refs, at)
		t.plan = ""
		return
	case promptInterrupted:
		t.b.End(model.TurnInterrupted, "", at)
		return
	case promptTask:
		t.b.StartTurn(at)
		t.plan = ""
		t.b.Add(model.Item{Kind: model.ItemNotice, Status: model.ItemCompleted, Text: p.text}, at)
		return
	}
	if e.IsMeta || e.IsCompactSummary {
		return
	}
	var m message
	if json.Unmarshal(e.Message, &m) != nil {
		return
	}
	for _, b := range m.blocks() {
		if b.Type != "tool_result" {
			continue
		}
		name := t.tools[b.ToolUseID]
		if name == "TodoWrite" || name == "ExitPlanMode" {
			continue
		}
		out, refs := toolResult(b.Content, t.keep)
		t.b.Update(b.ToolUseID, func(it *model.Item) {
			it.Tool.Output, it.Images, it.Status, it.UpdatedAt = out, refs, model.ItemCompleted, at
			if b.IsError {
				it.Status = model.ItemFailed
			}
		})
	}
	t.b.Touch(at)
}

func (t *transcript) assistant(e *entry, at time.Time) {
	var m message
	if json.Unmarshal(e.Message, &m) != nil {
		return
	}
	if m.Model != "" && m.Model != "<synthetic>" {
		t.model = m.Model
	}
	if e.IsAPIErrorMessage {
		var text strings.Builder
		for _, b := range m.blocks() {
			text.WriteString(b.Text)
		}
		t.b.Add(model.Item{Kind: model.ItemError, Status: model.ItemCompleted, Text: strings.TrimSpace(text.String())}, at)
		t.b.End(model.TurnFailed, strings.TrimSpace(text.String()), at)
		return
	}
	for i, b := range m.blocks() {
		key := e.UUID + ":" + strconv.Itoa(i)
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				t.b.Add(model.Item{ID: key, Kind: model.ItemAssistantMessage, Status: model.ItemCompleted, Text: b.Text}, at)
			}
		case "thinking":
			if strings.TrimSpace(b.Thinking) != "" {
				t.b.Add(model.Item{ID: key, Kind: model.ItemReasoning, Status: model.ItemCompleted, Text: b.Thinking}, at)
			}
		case "tool_use", "server_tool_use":
			t.toolUse(b, at)
		}
	}
	if turn := t.b.Turn(); turn != nil {
		turn.ForkPoint = e.UUID
	}
}

func (t *transcript) toolUse(b contentBlock, at time.Time) {
	t.tools[b.ID] = b.Name
	switch b.Name {
	case "TodoWrite":
		var in struct {
			Todos []model.PlanEntry `json:"todos"`
		}
		json.Unmarshal(b.Input, &in)
		p := &model.Plan{Entries: in.Todos}
		if t.plan != "" && t.b.Update(t.plan, func(it *model.Item) { it.Plan, it.UpdatedAt = p, at }) {
			return
		}
		t.plan = "plan:" + b.ID
		t.b.Add(model.Item{ID: t.plan, Kind: model.ItemPlan, Status: model.ItemCompleted, Plan: p}, at)
		return
	case "ExitPlanMode":
		var in struct {
			Plan string `json:"plan"`
		}
		json.Unmarshal(b.Input, &in)
		if strings.TrimSpace(in.Plan) != "" {
			t.b.Add(model.Item{ID: b.ID, Kind: model.ItemPlan, Status: model.ItemCompleted, Plan: &model.Plan{Text: in.Plan}}, at)
		}
		return
	}
	title, paths := describeTool(b.Name, b.Input, t.dir)
	if b.Name == "AskUserQuestion" {
		var in struct {
			Questions []model.Question `json:"questions"`
		}
		json.Unmarshal(b.Input, &in)
		if len(in.Questions) > 0 {
			title = "Asked: " + firstLine(in.Questions[0].Question)
		}
	}
	t.b.Add(model.Item{ID: b.ID, Kind: model.ItemToolCall, Status: model.ItemInProgress,
		Tool: &model.ToolCall{Name: b.Name, Kind: toolKind(b.Name), Title: cmpOr(title, b.Name), Input: b.Input, Paths: paths}}, at)
}
