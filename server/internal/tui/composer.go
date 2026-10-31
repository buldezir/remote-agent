package tui

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"remote-agent/internal/client"
	"remote-agent/internal/model"
)

// composer is where prompts are written: a text area, the slash commands
// that match what's typed, and the images attached.
type composer struct {
	ta        textarea.Model
	atts      []*attachment
	nextID    int
	sugg      []model.Command
	suggIdx   int
	dismissed string // the text whose suggestions esc put away
	wasQuery  bool
	focused   bool
}

type attachState int

const (
	uploading attachState = iota
	uploaded
	uploadFailed
)

type attachment struct {
	id    int
	name  string
	path  string
	state attachState
	ref   model.ImageRef
	err   string
}

// Messages for the composer, which carry it so a stale one is ignored.
type uploadMsg struct {
	c   *composer
	id  int
	ref model.ImageRef
	err error
}

type sendMsg struct {
	c      *composer
	text   string
	images []model.ImageRef
	err    error
}

type commandsMsg struct {
	v    *sessionView
	cmds []model.Command
	err  error
}

func newComposer(m *Model) composer {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.MaxHeight = 12
	ta.CharLimit = 0
	ta.MaxWidth = 0
	ta.Prompt = ""
	ta.SetPromptFunc(2, func(info textarea.PromptInfo) string {
		if info.LineNumber == 0 {
			return "› "
		}
		return "  "
	})
	km := textarea.DefaultKeyMap()
	km.InsertNewline = key.NewBinding(key.WithDisabled())
	// ra's own keys.
	km.LineNext = key.NewBinding(key.WithKeys("down"))
	km.LinePrevious = key.NewBinding(key.WithKeys("up"))
	km.DeleteAfterCursor = key.NewBinding(key.WithDisabled())
	km.CharacterBackward = key.NewBinding(key.WithKeys("left"))
	km.SelectAll = key.NewBinding(key.WithDisabled())
	ta.KeyMap = km
	c := composer{ta: ta}
	c.restyle(m)
	return c
}

func (c *composer) restyle(m *Model) {
	s := textarea.DefaultStyles(m.pal.Dark)
	for _, st := range []*textarea.StyleState{&s.Focused, &s.Blurred} {
		st.Base = m.st.Text
		st.CursorLine = m.st.Text
		st.Text = m.st.Text
		st.Placeholder = m.st.Faint
		st.Prompt = m.st.Accent
		st.EndOfBuffer = m.st.Faint
	}
	s.Blurred.Prompt = m.st.Faint
	s.Cursor.Color = m.pal.Accent
	c.ta.SetStyles(s)
}

func (c *composer) value() string { return c.ta.Value() }

func (c *composer) setValue(s string) {
	c.ta.SetValue(s)
	c.ta.MoveToEnd()
}

func (c *composer) reset(m *Model) {
	c.ta.Reset()
	c.atts = nil
	c.dismissed = ""
}

func (c *composer) focus() {
	if !c.focused {
		c.focused = true
		c.ta.Focus()
	}
}

func (c *composer) blur() {
	c.focused = false
	c.ta.Blur()
}

func (c *composer) uploading() bool {
	for _, a := range c.atts {
		if a.state == uploading {
			return true
		}
	}
	return false
}

func (c *composer) suggesting() bool {
	return c.focused && len(c.sugg) > 0 && c.dismissed != c.value()
}

// sync keeps the placeholder and the suggestions in step, and asks rad for
// the session's commands when a command starts being typed.
func (c *composer) sync(m *Model, v *sessionView) tea.Cmd {
	switch {
	case v.archived():
		c.ta.Placeholder = "This session is archived."
	case v.store.Session != nil && v.store.CanSteer("", m.conn.Harness(v.store.Session.Harness)):
		c.ta.Placeholder = "Queue a follow-up · ctrl+t steers it into the turn"
	case v.store.IsRunning():
		c.ta.Placeholder = "Queue a follow-up…"
	default:
		c.ta.Placeholder = "Prompt · / for commands"
	}
	_, isQuery := client.CommandQuery(c.value())
	isQuery = isQuery && c.focused && !v.archived()
	var cmd tea.Cmd
	if isQuery && !c.wasQuery && v.commandsState != 1 {
		// Discovery is refreshed each time the picker opens.
		v.commandsState = 0
	}
	c.wasQuery = isQuery
	if isQuery && v.commandsState == 0 && m.conn.IsConnected() {
		v.commandsState = 1
		store := v.store
		cmd = func() tea.Msg {
			cmds, err := store.Commands(context.Background())
			return commandsMsg{v: v, cmds: cmds, err: err}
		}
	}
	if !isQuery {
		c.sugg = nil
		return cmd
	}
	all := slices.Clone(v.commands)
	if s := v.store.Session; s != nil {
		if h := m.conn.Harness(s.Harness); h != nil {
			all = append(all, h.Commands...)
		}
	}
	prev := ""
	if c.suggIdx < len(c.sugg) {
		prev = c.sugg[c.suggIdx].Name
	}
	c.sugg = client.CommandSuggestions(c.value(), all)
	c.suggIdx = 0
	for i, s := range c.sugg {
		if s.Name == prev {
			c.suggIdx = i
		}
	}
	return cmd
}

func (c *composer) message(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case uploadMsg:
		if msg.c != c {
			return nil
		}
		for _, a := range c.atts {
			if a.id == msg.id {
				if msg.err != nil {
					a.state, a.err = uploadFailed, msg.err.Error()
					m.fail(fmt.Errorf("%s: %w", a.name, msg.err))
				} else {
					a.state, a.ref = uploaded, msg.ref
				}
			}
		}
	case sendMsg:
		if msg.c == c && msg.err != nil {
			m.fail(msg.err)
			c.restore(m, msg.text, msg.images)
		}
	case commandsMsg:
		if m.sess != msg.v {
			return nil
		}
		if msg.err != nil {
			msg.v.commandsState = 3
			return nil
		}
		msg.v.commands, msg.v.commandsState = msg.cmds, 2
	default:
		if c.focused {
			var cmd tea.Cmd
			c.ta, cmd = c.ta.Update(msg)
			return cmd
		}
	}
	return nil
}

func (c *composer) key(m *Model, v *sessionView, k tea.KeyPressMsg) tea.Cmd {
	if v.archived() {
		switch k.String() {
		case "esc":
			m.toList()
		case "up":
			m.setFocus(focusTranscript)
		}
		return nil
	}
	if c.suggesting() {
		switch k.String() {
		case "up":
			c.suggIdx = (c.suggIdx - 1 + len(c.sugg)) % len(c.sugg)
			return nil
		case "down":
			c.suggIdx = (c.suggIdx + 1) % len(c.sugg)
			return nil
		case "tab":
			c.complete()
			return nil
		case "enter":
			if c.value() != "/"+c.sugg[c.suggIdx].Name {
				c.complete()
				return nil
			}
		case "esc":
			c.dismissed = c.value()
			return nil
		}
	}
	switch k.String() {
	case "enter":
		return c.submit(m, v, false)
	case "ctrl+t":
		// Into the running turn, where the agent takes it; else as enter.
		return c.submit(m, v, true)
	case "shift+enter", "alt+enter", "ctrl+j":
		c.ta.InsertString("\n")
		return nil
	case "ctrl+s":
		return c.saveDraft(m, v)
	case "esc":
		m.toList()
		return nil
	case "up":
		// An empty prompt takes back the last one sent, as a shell does.
		if c.value() == "" && len(c.atts) == 0 {
			if text, images, ok := lastPrompt(v.store); ok {
				c.restore(m, text, images)
			}
			return nil
		}
	case "pgup":
		v.scrollBy(-max(1, v.avail-2))
		return nil
	case "pgdown":
		v.scrollBy(max(1, v.avail-2))
		return nil
	case "backspace":
		if c.value() == "" && len(c.atts) > 0 {
			c.atts = c.atts[:len(c.atts)-1]
			return nil
		}
	}
	var cmd tea.Cmd
	c.ta, cmd = c.ta.Update(k)
	return cmd
}

func (c *composer) complete() {
	c.setValue("/" + c.sugg[c.suggIdx].Name + " ")
	c.sugg = nil
}

// lastPrompt is the session's last prompt: the newest one sent, or else
// the last in the transcript.
func lastPrompt(s *client.SessionStore) (string, []model.ImageRef, bool) {
	if n := len(s.Outbox); n > 0 {
		return s.Outbox[n-1].Text, s.Outbox[n-1].Images, true
	}
	for i := len(s.Transcript) - 1; i >= 0; i-- {
		if it := s.Transcript[i]; it.Kind == model.ItemUserMessage {
			return it.Text, it.Images, true
		}
	}
	return "", nil, false
}

// uploaded are the attachments' refs that have finished uploading.
func (c *composer) uploaded() []model.ImageRef {
	var refs []model.ImageRef
	for _, a := range c.atts {
		if a.state == uploaded {
			refs = append(refs, a.ref)
		}
	}
	return refs
}

// images are the attachments' refs, once all are uploaded.
func (c *composer) images(m *Model) ([]model.ImageRef, bool) {
	var refs []model.ImageRef
	for _, a := range c.atts {
		switch a.state {
		case uploading:
			m.fail(fmt.Errorf("Wait for %s to upload.", a.name))
			return nil, false
		case uploadFailed:
			m.fail(fmt.Errorf("%s didn't upload: remove it with backspace, or attach it again.", a.name))
			return nil, false
		}
		refs = append(refs, a.ref)
	}
	return refs, true
}

// submit sends the prompt; with steer, it joins the running turn where the
// agent can take it.
func (c *composer) submit(m *Model, v *sessionView, steer bool) tea.Cmd {
	text := strings.TrimSpace(c.value())
	refs, ok := c.images(m)
	if !ok || (text == "" && len(refs) == 0) {
		return nil
	}
	c.reset(m)
	v.store.SetDraft("")
	return c.send(m, v, text, refs, steer)
}

// send sends a prompt; if it fails, it comes back to the composer.
func (c *composer) send(m *Model, v *sessionView, text string, refs []model.ImageRef, steer bool) tea.Cmd {
	job := v.store.Send(text, refs, steer)
	v.pinned = true
	return func() tea.Msg {
		return sendMsg{c: c, text: text, images: refs, err: job(context.Background())}
	}
}

// restore puts a prompt back, ahead of what was written since.
func (c *composer) restore(m *Model, text string, images []model.ImageRef) {
	if cur := c.value(); strings.TrimSpace(cur) != "" {
		text += "\n" + cur
	}
	c.setValue(text)
	var atts []*attachment
	for _, im := range images {
		c.nextID++
		atts = append(atts, &attachment{id: c.nextID, name: im.ID[:min(12, len(im.ID))], state: uploaded, ref: im})
	}
	c.atts = append(atts, c.atts...)
}

func (c *composer) saveDraft(m *Model, v *sessionView) tea.Cmd {
	if m.conn.Info == nil || !m.conn.Info.Drafts {
		m.say("This rad doesn't keep drafts. Update rad.")
		return nil
	}
	if len(v.store.Transcript) > 0 || len(v.store.Outbox) > 0 {
		m.say("Only a session's first prompt can wait as a draft.")
		return nil
	}
	text := strings.TrimSpace(c.value())
	refs, ok := c.images(m)
	if !ok || (text == "" && len(refs) == 0) {
		return nil
	}
	id := v.store.ID
	c.reset(m)
	v.store.SetDraft("")
	return m.do(func(ctx context.Context) error {
		_, err := m.conn.SaveDraft(ctx, id, text, refs)
		return err
	}, func(m *Model) tea.Cmd { m.say("Saved as a draft."); return nil })
}

// paste attaches pasted image paths, as a file dropped on the terminal
// pastes its path; other text goes in the prompt.
func (c *composer) paste(m *Model, v *sessionView, text string) tea.Cmd {
	if paths := imagePaths(text); len(paths) > 0 && !v.archived() {
		var cmds []tea.Cmd
		for _, p := range paths {
			cmds = append(cmds, c.attach(m, p))
		}
		return tea.Batch(cmds...)
	}
	c.ta.InsertString(text)
	return nil
}

// imagePaths reads pasted text as paths of image files, or nil if it's
// anything else.
func imagePaths(text string) []string {
	var paths []string
	for _, field := range shellFields(strings.TrimSpace(text)) {
		if u, err := url.Parse(field); err == nil && u.Scheme == "file" {
			field = u.Path
		}
		field = expandHome(field)
		switch strings.ToLower(filepath.Ext(field)) {
		case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		default:
			return nil
		}
		if st, err := os.Stat(field); err != nil || st.IsDir() {
			return nil
		}
		paths = append(paths, field)
	}
	return paths
}

// shellFields splits text as a shell would split dropped paths: on
// unescaped spaces, with quotes.
func shellFields(s string) []string {
	var fields []string
	var b strings.Builder
	var quote rune
	escaped, any := false, false
	for _, r := range s {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped, any = true, true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, any = r, true
		case r == ' ' || r == '\n' || r == '\t':
			if b.Len() > 0 || any {
				fields = append(fields, b.String())
				b.Reset()
				any = false
			}
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() > 0 || any {
		fields = append(fields, b.String())
	}
	return fields
}

// attach readies an image file for agents and uploads it.
func (c *composer) attach(m *Model, path string) tea.Cmd {
	c.nextID++
	a := &attachment{id: c.nextID, name: filepath.Base(path), path: path, state: uploading}
	c.atts = append(c.atts, a)
	conn := m.conn
	return func() tea.Msg {
		data, err := os.ReadFile(path)
		if err == nil {
			data, err = client.PrepareImage(data)
		}
		var ref model.ImageRef
		if err == nil {
			ref, err = conn.UploadImage(context.Background(), data)
		}
		return uploadMsg{c: c, id: a.id, ref: ref, err: err}
	}
}

func (c *composer) view(m *Model, v *sessionView, w int, focused bool) []string {
	var lines []string
	if v != nil && (c.suggesting() || (c.focused && v.commandsState == 1 && c.wasQuery)) {
		lines = append(lines, c.suggestionLines(m, v, w)...)
	}
	if len(c.atts) > 0 {
		var chips []string
		for _, a := range c.atts {
			label := "▣ " + a.name
			switch a.state {
			case uploading:
				chips = append(chips, m.st.Faint.Render(label+" "+m.spinner()))
			case uploadFailed:
				chips = append(chips, m.st.Red.Render(label+" ✗"))
			default:
				chips = append(chips, m.st.Chip.Render(label))
			}
		}
		lines = append(lines, truncate(" "+strings.Join(chips, " "), w))
	}
	rule := m.st.Rule
	if focused && (v == nil || !v.archived()) {
		rule = m.st.Accent
	}
	lines = append(lines, rule.Render(strings.Repeat("─", w)))
	c.ta.SetWidth(max(10, w-1))
	for _, l := range strings.Split(c.ta.View(), "\n") {
		lines = append(lines, " "+l)
	}
	return lines
}

func (c *composer) suggestionLines(m *Model, v *sessionView, w int) []string {
	inner := w - 4
	var rows []string
	if len(c.sugg) == 0 {
		msg := m.spinner() + " Loading skills…"
		if v.commandsState == 3 {
			msg = "Skills unavailable; the agent's commands still work."
		}
		rows = append(rows, m.st.Faint.Render(msg))
	}
	start := max(0, min(c.suggIdx-2, len(c.sugg)-6))
	for i := start; i < len(c.sugg) && i < start+6; i++ {
		s := c.sugg[i]
		meta := s.Description
		if s.Scope != "" {
			meta = strings.TrimSpace(meta + " · " + s.Scope)
		}
		line := spread(m.st.Accent.Render("/"+s.Name), m.st.Faint.Render(truncate(meta, max(0, inner-len(s.Name)-4))), inner)
		if i == c.suggIdx {
			line = highlight(line, inner, m.pal.Surface0)
		}
		rows = append(rows, line)
	}
	box := m.st.Panel.BorderForeground(m.pal.Surface1).Width(w).Render(strings.Join(rows, "\n"))
	return strings.Split(box, "\n")
}
