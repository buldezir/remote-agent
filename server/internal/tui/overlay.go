package tui

import (
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// overlay is a layer over the main view, such as a menu or a form. The top
// one gets the keys.
type overlay interface {
	update(m *Model, k tea.KeyPressMsg) tea.Cmd
	view(m *Model, width, height int) string
}

// box draws an overlay's frame around its lines.
func box(m *Model, title string, lines []string, width int) string {
	body := strings.Join(lines, "\n")
	st := m.st.Dialog.Width(width)
	if title != "" {
		body = m.st.AccentBold.Render(title) + "\n" + body
	}
	return st.Render(body)
}

// Menus

type menuItem struct {
	label    string
	hint     string // right-aligned, such as a key
	detail   string // a line under it
	header   bool   // a section's title, not chosen
	checked  bool
	disabled bool
	danger   bool
	run      func(m *Model) tea.Cmd
}

// menu is a list to choose from: the actions menu, pickers, confirmations.
type menu struct {
	title   string
	message []string
	items   []menuItem
	cursor  int
	width   int
	// confirm: only enter chooses, so that a space typed by mistake doesn't.
	confirm bool
}

func newMenu(title string, items []menuItem) *menu {
	mn := &menu{title: title, items: items, width: 44}
	mn.cursor = -1
	mn.move(1)
	return mn
}

// newConfirm asks before something that can't be undone. When its first
// choice is dangerous, Cancel comes first and is chosen.
func newConfirm(title string, message string, items ...menuItem) *menu {
	cancel := menuItem{label: "Cancel", run: func(*Model) tea.Cmd { return nil }}
	if len(items) > 0 && items[0].danger {
		items = append([]menuItem{cancel}, items...)
	} else {
		items = append(items, cancel)
	}
	mn := newMenu(title, items)
	mn.message = []string{message}
	mn.width = 52
	mn.confirm = true
	return mn
}

func (mn *menu) selectable(i int) bool {
	return i >= 0 && i < len(mn.items) && !mn.items[i].header && !mn.items[i].disabled
}

func (mn *menu) move(d int) {
	for i := mn.cursor + d; i >= 0 && i < len(mn.items); i += d {
		if mn.selectable(i) {
			mn.cursor = i
			return
		}
	}
}

func (mn *menu) hints() [][2]string {
	return [][2]string{{"↑↓", "choose"}, {"enter", "select"}, {"esc", "close"}}
}

func (mn *menu) update(m *Model, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc", "ctrl+p":
		m.pop()
	case "up", "shift+tab":
		mn.move(-1)
	case "down", "tab":
		mn.move(1)
	case "enter", "space":
		if k.String() == "space" && mn.confirm {
			return nil
		}
		if mn.selectable(mn.cursor) {
			it := mn.items[mn.cursor]
			m.pop()
			if it.run != nil {
				return it.run(m)
			}
		}
	}
	return nil
}

func (mn *menu) view(m *Model, width, height int) string {
	w := min(mn.width, width-4)
	inner := w - 4
	var lines []string
	for _, msg := range mn.message {
		lines = append(lines, wrap(m.st.Text.Render(msg), inner)...)
	}
	if len(mn.message) > 0 {
		lines = append(lines, "")
	}
	cursorLine := 0
	for i, it := range mn.items {
		if i == mn.cursor {
			cursorLine = len(lines)
		}
		if it.header {
			if i > 0 {
				lines = append(lines, "")
			}
			lines = append(lines, m.st.Header.Render(truncate(it.label, inner)))
			continue
		}
		mark := "  "
		if it.checked {
			mark = m.st.Accent.Render("✓ ")
		}
		label := it.label
		style := m.st.Text
		switch {
		case it.disabled:
			style = m.st.Faint
		case it.danger:
			style = m.st.Red
		}
		line := spread(mark+style.Render(label), m.st.Faint.Render(it.hint), inner)
		if i == mn.cursor {
			line = highlight(line, inner, m.pal.Surface0)
		}
		lines = append(lines, line)
		if it.detail != "" {
			details := wrap(it.detail, inner-2)
			if len(details) > 2 {
				details = append(details[:1], truncate(details[1]+"…", inner-2))
			}
			for _, d := range details {
				lines = append(lines, "  "+m.st.Faint.Render(d))
			}
		}
	}
	// A long menu shows the part around its cursor.
	maxLines := max(5, height-6)
	if len(lines) > maxLines {
		start := min(max(0, cursorLine-maxLines/2), len(lines)-maxLines)
		lines = lines[start : start+maxLines]
	}
	return box(m, mn.title, lines, w)
}

// Prompts for a line of text.

type inputPrompt struct {
	title   string
	message string
	note    string // under the input
	action  string // what enter does, in the hints: "ok" if empty
	input   textinput.Model
	submit  func(m *Model, value string) tea.Cmd
	// paths: tab completes a file path.
	paths bool
	// selected: the whole value is selected, as a text field's is when it
	// opens. Typing or pasting replaces it, backspace clears it, and any
	// other key only drops the selection.
	selected bool
}

func newInput(m *Model, title, message, value string, submit func(*Model, string) tea.Cmd) *inputPrompt {
	in := textinput.New()
	in.SetValue(value)
	in.CursorEnd()
	in.Focus()
	in.SetWidth(46)
	styleInput(&in, m)
	return &inputPrompt{title: title, message: message, input: in, submit: submit}
}

func styleInput(in *textinput.Model, m *Model) {
	s := textinput.DefaultStyles(m.pal.Dark)
	s.Focused.Text = m.st.Text
	s.Focused.Placeholder = m.st.Faint
	s.Focused.Prompt = m.st.Accent
	s.Blurred.Text = m.st.Subtext
	s.Blurred.Placeholder = m.st.Faint
	s.Blurred.Prompt = m.st.Faint
	s.Cursor.Color = m.pal.Accent
	in.SetStyles(s)
}

func (p *inputPrompt) hints() [][2]string {
	action := p.action
	if action == "" {
		action = "ok"
	}
	h := [][2]string{{"enter", action}, {"esc", "cancel"}}
	if p.paths {
		h = append([][2]string{{"tab", "complete"}}, h...)
	}
	return h
}

func (p *inputPrompt) update(m *Model, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		m.pop()
		return nil
	case "enter":
		v := p.input.Value()
		m.pop()
		return p.submit(m, v)
	case "tab":
		if p.paths {
			p.input.SetValue(completePath(p.input.Value()))
			p.input.CursorEnd()
		}
		return nil
	}
	if p.selected {
		p.selected = false
		switch {
		case k.String() == "backspace" || k.String() == "delete":
			p.input.SetValue("")
			return nil
		case k.Text != "":
			p.input.SetValue("")
		}
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(k)
	return cmd
}

func (p *inputPrompt) paste(m *Model, text string) tea.Cmd {
	if p.selected {
		p.selected = false
		p.input.SetValue("")
	}
	p.input.SetValue(p.input.Value() + strings.TrimSpace(text))
	p.input.CursorEnd()
	return nil
}

func (p *inputPrompt) view(m *Model, width, height int) string {
	w := min(56, width-4)
	p.input.SetWidth(w - 6)
	var lines []string
	if p.message != "" {
		lines = append(lines, wrap(m.st.Subtext.Render(p.message), w-4)...)
		lines = append(lines, "")
	}
	if p.selected && p.input.Value() != "" {
		prompt := p.input.Prompt
		lines = append(lines, m.st.Accent.Render(prompt)+m.st.Text.Background(m.pal.Surface2).Render(truncate(p.input.Value(), w-5-lipgloss.Width(prompt))))
	} else {
		lines = append(lines, p.input.View())
	}
	if p.note != "" {
		lines = append(lines, wrap(m.st.Faint.Render(p.note), w-4)...)
	}
	return box(m, p.title, lines, w)
}

// completePath completes a path to its longest unambiguous extension.
func completePath(p string) string {
	expanded := expandHome(p)
	dir, base := filepath.Split(expanded)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return p
	}
	var matches []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), base) && (strings.HasPrefix(base, ".") || !strings.HasPrefix(e.Name(), ".")) {
			name := e.Name()
			if e.IsDir() {
				name += "/"
			}
			matches = append(matches, name)
		}
	}
	if len(matches) == 0 {
		return p
	}
	common := matches[0]
	for _, mt := range matches[1:] {
		for !strings.HasPrefix(mt, common) {
			common = common[:len(common)-1]
		}
	}
	return p + strings.TrimPrefix(common, base)
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// Help

// help lists the keys, scrolling when they don't fit.
type help struct {
	scroll int
}

func newHelp() *help { return &help{} }

func (h *help) hints() [][2]string {
	return [][2]string{{"↑↓", "scroll"}, {"esc", "close"}}
}

func (h *help) update(m *Model, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "up":
		h.scroll--
	case "down":
		h.scroll++
	case "pgup":
		h.scroll -= 10
	case "pgdown", "space":
		h.scroll += 10
	default:
		m.pop()
	}
	h.scroll = max(0, h.scroll)
	return nil
}

var helpSections = []struct {
	title string
	keys  [][2]string
}{
	{"Anywhere", [][2]string{
		{"ctrl+n", "new session"},
		{"ctrl+k", "jump to a session"},
		{"ctrl+p", "actions: modes, commands, commit, rename, fork, archive, delete, settings"},
		{"ctrl+x", "stop the agent"},
		{"esc", "back: close what's open, or leave the session for the list"},
		{"tab · shift+tab", "move between the list, the messages and the prompt"},
		{"ctrl+b", "hide the list, or go back to it"},
		{"ctrl+c ctrl+c · ctrl+q", "quit"},
		{"r", "reconnect, after rad went away"},
		{"ctrl+h · f1", "these keys; ? too, outside the prompt"},
	}},
	{"List", [][2]string{
		{"↑↓ · home end · pgup pgdn", "move"},
		{"enter", "open the session"},
		{"space", "open the session, staying in the list"},
		{"←→ · space", "fold or unfold a project"},
		{"n", "new session here"},
		{"a · D · R", "archive · delete · rename"},
		{".", "the row's actions"},
		{"/ · ctrl+f", "search the sessions: what was said in them, their titles and branches"},
	}},
	{"Search", [][2]string{
		{"type", "search, from two letters on; the results replace the list"},
		{"↑↓ · enter", "choose a result · open its session at what matched"},
		{"ctrl+a", "include archived sessions, or leave them out"},
		{"esc", "clear the search, back to the list"},
	}},
	{"Messages", [][2]string{
		{"↑↓", "move between items"},
		{"enter · space", "expand a tool call or thinking; twice shows it all"},
		{"pgup pgdn · home end", "scroll"},
		{"o", "open the item's image"},
		{"y", "copy the item's text"},
		{"s", "send a queued prompt now, or send the draft"},
		{"t", "steer a queued prompt into the turn, where the agent can take it"},
		{"x", "cancel a queued prompt"},
		{"e", "edit the draft"},
		{"R", "rename the session"},
		{"ctrl+f", "find in the session, from the messages or the prompt"},
		{"drag the mouse", "select text, which is copied when you let go"},
	}},
	{"Find in the session", [][2]string{
		{"enter · ↓ · ctrl+g", "the next match"},
		{"↑ · shift+enter", "the previous match"},
		{"tab", "to the messages, the matches still marked"},
		{"esc", "close it"},
	}},
	{"Prompt", [][2]string{
		{"enter", "send"},
		{"ctrl+t", "send into the running turn, where the agent can take it"},
		{"shift+enter · alt+enter · ctrl+j", "new line"},
		{"/", "commands and skills"},
		{"↑", "the last prompt again, when the prompt is empty"},
		{"pgup pgdn", "scroll the messages"},
		{"ctrl+s", "save as a draft, in a new session"},
		{"esc", "in a new session: close it, keeping the prompt as a draft"},
		{"ctrl+c", "clear the prompt"},
		{"paste a path", "attach that image"},
		{"backspace", "remove the last image, when the prompt is empty"},
	}},
	{"Approvals", [][2]string{
		{"←→ then enter", "pick an answer, then send it"},
		{"↑↓ ←→ space", "choose a question's answers"},
		{"pgup pgdn · wheel", "scroll a plan"},
	}},
}

func (h *help) view(m *Model, width, height int) string {
	w := min(76, width-4)
	inner := w - 4
	keyW := 0
	for _, s := range helpSections {
		for _, k := range s.keys {
			keyW = max(keyW, lipgloss.Width(k[0])+2)
		}
	}
	keyW = min(keyW, inner/2)
	var lines []string
	for i, s := range helpSections {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, m.st.Header.Render(s.title))
		// Descriptions wrap under themselves, so that the box is as many
		// lines high as it has.
		for _, k := range s.keys {
			key := truncate(k[0], keyW-1)
			for _, d := range wrap(k[1], inner-keyW) {
				lines = append(lines, pad(m.st.Key.Render(key), keyW)+m.st.Text.Render(d))
				key = ""
			}
		}
	}
	if fit := max(1, height-4); len(lines) > fit {
		h.scroll = min(h.scroll, len(lines)-fit)
		lines = lines[h.scroll : h.scroll+fit]
	}
	return box(m, "Keys", lines, w)
}

// centered places a line in the middle of width.
func centered(s string, width int) string {
	return lipgloss.PlaceHorizontal(width, lipgloss.Center, s)
}
