package pi

import (
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

// Pi keeps each session as JSONL in its agent folder:
// ~/.pi/agent/sessions/--<the folder's path, with its separators dashes>--/
// <time>_<session id>.jsonl. The first line is the session's header, with
// its id and folder; the entries after it make a tree by parentId, whose
// last entry is where the session stands. Messages are pi-ai's: user,
// assistant (text, thinking and tool calls), toolResult and bashExecution
// (the user's own ! commands).

// agentDir is Pi's folder: PI_CODING_AGENT_DIR as the harness's config or
// rad's environment sets it, else ~/.pi/agent.
func (h *Harness) agentDir() string {
	if d := h.cmd.Env["PI_CODING_AGENT_DIR"]; d != "" {
		return d
	}
	if d := os.Getenv("PI_CODING_AGENT_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pi", "agent")
}

var separators = regexp.MustCompile(`[/\\:]`)

// sessionDirs are the folders Pi keeps dir's sessions in.
func (h *Harness) sessionDirs(dir string) []string {
	var out []string
	for _, d := range native.Dirs(dir) {
		slug := "--" + separators.ReplaceAllString(strings.TrimLeft(d, `/\`), "-") + "--"
		out = append(out, filepath.Join(h.agentDir(), "sessions", slug))
	}
	return out
}

func (h *Harness) files(dir string) []native.File {
	var paths []string
	for _, d := range h.sessionDirs(dir) {
		ps, _ := filepath.Glob(filepath.Join(d, "*.jsonl"))
		paths = append(paths, ps...)
	}
	return native.Newest(paths)
}

// entry is a line of a session file.
type entry struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	ParentID  string          `json:"parentId"`
	Timestamp string          `json:"timestamp"`
	Cwd       string          `json:"cwd"`     // session
	Message   json.RawMessage `json:"message"` // message
	Name      string          `json:"name"`    // session_info
	Provider  string          `json:"provider"`
	ModelID   string          `json:"modelId"` // model_change
}

func (m *message) blocks() []block {
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return []block{{Type: "text", Text: s}}
	}
	var bs []block
	json.Unmarshal(m.Content, &bs)
	return bs
}

func (m *message) text() string {
	var b []string
	for _, bl := range m.blocks() {
		if bl.Type == "text" && strings.TrimSpace(bl.Text) != "" {
			b = append(b, bl.Text)
		}
	}
	return strings.Join(b, "\n\n")
}

func (m *message) hasImage() bool {
	for _, bl := range m.blocks() {
		if bl.Type == "image" {
			return true
		}
	}
	return false
}

var summaries native.Cache[native.Summary]

// ListNative lists the sessions Pi kept for dir.
func (h *Harness) ListNative(ctx context.Context, dir string, known map[string]bool) ([]model.ImportableSession, error) {
	dirs := native.Dirs(dir)
	out := []model.ImportableSession{}
	for _, f := range h.files(dir) {
		if len(out) == harness.ImportLimit || ctx.Err() != nil {
			break
		}
		sum, err := summaries.Get(f, func() (native.Summary, error) { return summarize(f.Path, dirs) })
		if err != nil {
			continue
		}
		if s, ok := sum.Session(f, known); ok {
			out = append(out, s)
		}
	}
	return out, ctx.Err()
}

// summarize reads what a listing shows of a session file. Its folder is in
// the file's name, so dirs only checks the header.
func summarize(path string, dirs []string) (native.Summary, error) {
	var s native.Summary
	first := true
	err := native.Lines(path, func(raw []byte) bool {
		var e entry
		if json.Unmarshal(raw, &e) != nil {
			return true
		}
		if first {
			first = false
			if e.Type != "session" {
				s.OtherDir = true
				return false
			}
			s.ID, s.StartedAt = e.ID, native.Time(e.Timestamp)
			s.OtherDir = e.Cwd != "" && !native.SameDir(dirs, e.Cwd)
			return !s.OtherDir
		}
		switch e.Type {
		case "session_info":
			if strings.TrimSpace(e.Name) != "" {
				s.Title = native.Title(e.Name)
			}
		case "message":
			var m message
			if json.Unmarshal(e.Message, &m) != nil {
				return true
			}
			switch {
			case m.Role == "user" && (m.text() != "" || m.hasImage()):
				if s.Prompts == 0 {
					s.Prompt = m.text()
				}
				s.Prompts++
			case m.Role == "assistant" && m.text() != "":
				s.Replies++
			}
		}
		return true
	})
	return s, err
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ReadNative reads session id's conversation, along the branch it stands on.
func (h *Harness) ReadNative(ctx context.Context, dir, id string, images harness.ImageStore) (*harness.Transcript, error) {
	if !idPattern.MatchString(id) || strings.Contains(id, "..") {
		return nil, harness.ErrNotFound
	}
	dirs := native.Dirs(dir)
	for _, f := range h.files(dir) {
		if !strings.HasSuffix(f.Path, "_"+id+".jsonl") && filepath.Base(f.Path) != id+".jsonl" {
			continue
		}
		var entries []entry
		var head entry
		err := native.Lines(f.Path, func(raw []byte) bool {
			var e entry
			if json.Unmarshal(raw, &e) == nil {
				if e.Type == "session" {
					head = e
				} else {
					entries = append(entries, e)
				}
			}
			return ctx.Err() == nil
		})
		if err != nil {
			return nil, err
		}
		if head.ID != id || head.Cwd != "" && !native.SameDir(dirs, head.Cwd) {
			continue
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		t := &transcript{dir: dir, images: images}
		for _, e := range branch(entries) {
			t.add(&e)
		}
		tr := t.b.Transcript(t.title, t.model)
		tr.UpdatedAt = f.Info.ModTime().UTC()
		return tr, nil
	}
	return nil, harness.ErrNotFound
}

// branch is the entries on the way to the last one. Files from before Pi
// kept a tree have no ids, and are a list.
func branch(entries []entry) []entry {
	byID := map[string]int{}
	for i, e := range entries {
		if e.ID == "" {
			return entries
		}
		byID[e.ID] = i
	}
	on := map[int]bool{}
	for i, ok := len(entries)-1, len(entries) > 0; ok && !on[i]; {
		on[i] = true
		i, ok = byID[entries[i].ParentID]
	}
	out := make([]entry, 0, len(on))
	for i, e := range entries {
		if on[i] {
			out = append(out, e)
		}
	}
	return out
}

// transcript turns a session's entries into a harness.Transcript.
type transcript struct {
	b      native.Builder
	dir    string
	images harness.ImageStore
	title  string
	model  string
}

func (t *transcript) keep(b block) (model.ImageRef, bool) {
	if t.images == nil || b.Data == "" {
		return model.ImageRef{}, false
	}
	ref, err := t.images.PutBase64(b.Data)
	return ref, err == nil
}

func (t *transcript) add(e *entry) {
	at := native.Time(e.Timestamp)
	switch e.Type {
	case "session_info":
		if strings.TrimSpace(e.Name) != "" {
			t.title = strings.TrimSpace(e.Name)
		}
	case "model_change":
		if e.ModelID != "" {
			t.model = e.Provider + "/" + e.ModelID
		}
	case "compaction":
		t.b.Add(model.Item{Kind: model.ItemNotice, Status: model.ItemCompleted, Text: "Context compacted"}, at)
	case "message":
		var m message
		if json.Unmarshal(e.Message, &m) == nil {
			if m.Timestamp > 0 {
				at = time.UnixMilli(m.Timestamp).UTC()
			}
			t.message(e.ID, &m, at)
		}
	}
}

func (t *transcript) message(id string, m *message, at time.Time) {
	switch m.Role {
	case "user":
		var refs []model.ImageRef
		for _, b := range m.blocks() {
			if b.Type == "image" {
				if ref, ok := t.keep(b); ok {
					refs = append(refs, ref)
				}
			}
		}
		if m.text() != "" || len(refs) > 0 {
			t.b.Prompt(m.text(), refs, at)
		}
	case "assistant":
		if m.Model != "" {
			t.model = m.Provider + "/" + m.Model
		}
		for i, b := range m.blocks() {
			key := id + ":" + strconv.Itoa(i)
			switch b.Type {
			case "text":
				if strings.TrimSpace(b.Text) != "" {
					t.b.Add(model.Item{ID: key, Kind: model.ItemAssistantMessage, Status: model.ItemCompleted, Text: b.Text}, at)
				}
			case "thinking":
				if strings.TrimSpace(b.Thinking) != "" && !b.Redacted {
					t.b.Add(model.Item{ID: key, Kind: model.ItemReasoning, Status: model.ItemCompleted, Text: b.Thinking}, at)
				}
			case "toolCall":
				kind, title, paths := describeTool(b.Name, b.Arguments, t.dir)
				t.b.Add(model.Item{ID: "tool:" + b.ID, Kind: model.ItemToolCall, Status: model.ItemInProgress,
					Tool: &model.ToolCall{Name: b.Name, Kind: kind, Title: title, Input: b.Arguments, Paths: paths}}, at)
			}
		}
		switch m.StopReason {
		case "aborted":
			t.b.End(model.TurnInterrupted, "", at)
		case "error":
			if m.ErrorMessage != "" {
				t.b.Add(model.Item{Kind: model.ItemError, Status: model.ItemCompleted, Text: m.ErrorMessage}, at)
			}
			t.b.End(model.TurnFailed, m.ErrorMessage, at)
		}
	case "toolResult":
		var text []string
		var refs []model.ImageRef
		for _, b := range m.blocks() {
			switch b.Type {
			case "text":
				text = append(text, b.Text)
			case "image":
				if ref, ok := t.keep(b); ok {
					refs = append(refs, ref)
				}
			}
		}
		t.b.Update("tool:"+m.ToolCallID, func(it *model.Item) {
			it.Tool.Output, it.Images, it.Status, it.UpdatedAt = strings.Join(text, "\n"), refs, model.ItemCompleted, at
			if m.IsError {
				it.Status = model.ItemFailed
			}
		})
		t.b.Touch(at)
	case "bashExecution":
		// The user's own command (!), which the agent sees with the next prompt.
		status := model.ItemCompleted
		if m.Cancelled || m.ExitCode != nil && *m.ExitCode != 0 {
			status = model.ItemFailed
		}
		t.b.Add(model.Item{Kind: model.ItemToolCall, Status: status, Tool: &model.ToolCall{Name: "bash", Kind: model.ToolExecute,
			Title: firstLine(m.Command), Output: m.Output, ExitCode: m.ExitCode}}, at)
	}
}
