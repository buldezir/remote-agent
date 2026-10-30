package tui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"remote-agent/internal/client"
	"remote-agent/internal/model"
)

// switcher jumps to a session by a few letters of its title or project.
type switcher struct {
	input  textinput.Model
	cursor int
}

func newSwitcher(m *Model) *switcher {
	in := textinput.New()
	in.Placeholder = "Jump to a session"
	in.Prompt = "› "
	styleInput(&in, m)
	in.Focus()
	return &switcher{input: in}
}

func (sw *switcher) hints() [][2]string {
	return [][2]string{{"↑↓", "choose"}, {"enter", "open"}, {"esc", "close"}}
}

// fuzzy reports whether the letters of q come in order in s.
func fuzzy(s, q string) bool {
	s = strings.ToLower(s)
	for _, r := range strings.ToLower(q) {
		if r == ' ' {
			continue
		}
		i := strings.IndexRune(s, r)
		if i < 0 {
			return false
		}
		s = s[i+len(string(r)):]
	}
	return true
}

func (sw *switcher) results(m *Model) []*model.Session {
	q := strings.TrimSpace(sw.input.Value())
	all := append(client.NeedsYouFirst(m.conn.ActiveSessions()), m.conn.ArchivedSessions()...)
	if q == "" {
		// The ones opened lately first.
		byID := map[string]*model.Session{}
		for _, s := range all {
			byID[s.ID] = s
		}
		var out []*model.Session
		seen := map[string]bool{}
		for _, id := range m.state.Recent {
			if s := byID[id]; s != nil && !s.Archived {
				out = append(out, s)
				seen[id] = true
			}
		}
		for _, s := range all {
			if !seen[s.ID] {
				out = append(out, s)
			}
		}
		return out
	}
	var exact, loose []*model.Session
	for _, s := range all {
		project := ""
		if p := m.conn.Projects[s.ProjectID]; p != nil {
			project = p.Name
		}
		switch {
		case strings.Contains(strings.ToLower(s.Title), strings.ToLower(q)):
			exact = append(exact, s)
		case fuzzy(s.Title+" "+project, q):
			loose = append(loose, s)
		}
	}
	return append(exact, loose...)
}

func (sw *switcher) update(m *Model, k tea.KeyPressMsg) tea.Cmd {
	res := sw.results(m)
	switch k.String() {
	case "esc", "ctrl+k":
		m.pop()
		return nil
	case "up", "ctrl+p":
		sw.cursor = max(0, sw.cursor-1)
		return nil
	case "down", "ctrl+n":
		sw.cursor = min(len(res)-1, sw.cursor+1)
		return nil
	case "enter":
		if sw.cursor < len(res) {
			m.pop()
			m.openSession(res[sw.cursor].ID)
			m.setFocus(focusComposer)
		}
		return nil
	}
	var cmd tea.Cmd
	sw.input, cmd = sw.input.Update(k)
	sw.cursor = 0
	return cmd
}

func (sw *switcher) paste(m *Model, text string) tea.Cmd {
	sw.input.SetValue(sw.input.Value() + strings.TrimSpace(text))
	sw.input.CursorEnd()
	return nil
}

func (sw *switcher) view(m *Model, width, height int) string {
	w := min(72, width-4)
	inner := w - 4
	sw.input.SetWidth(inner - 2)
	lines := []string{sw.input.View(), ""}
	res := sw.results(m)
	room := max(3, min(14, height-8))
	start := max(0, min(sw.cursor-room/2, len(res)-room))
	for i := start; i < len(res) && i < start+room; i++ {
		s := res[i]
		title := s.Title
		if s.Archived {
			title += m.st.Faint.Render("  archived")
		}
		right := m.st.Faint.Render(truncate(m.place(s), inner/3) + "  " + client.ShortAge(s.UpdatedAt, time.Now()))
		line := spread(m.glyph(s)+" "+title, right, inner)
		if i == sw.cursor {
			line = highlight(line, inner, m.pal.Surface0)
		}
		lines = append(lines, line)
	}
	if len(res) == 0 {
		lines = append(lines, m.st.Faint.Render("No sessions match."))
	}
	return box(m, "", lines, w)
}
