package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"remote-agent/internal/client"
	"remote-agent/internal/model"
)

// Searching the sessions, as the apps' sidebars do: a field atop the list
// (atop the session list in Columns), whose results replace the list's
// rows while the query has two characters or more. client.SessionSearch
// asks rad as it's typed; a result opens its session at the item that
// matched.

func (m *Model) newSessionSearch() *client.SessionSearch {
	return client.NewSessionSearch(m.q, func() []client.SearchServer {
		id, name := "", "rad"
		if info := m.conn.Info; info != nil {
			id, name = info.ServerID, info.Name
		}
		return []client.SearchServer{{ID: id, Name: name, Conn: m.conn}}
	})
}

// searchField is the field's line, and the Include archived toggle under
// it while the field has the keys or the results show.
func (s *sidebar) searchField(m *Model, width int, focused bool) []string {
	s.ensureSearch(m)
	s.search.SetWidth(max(4, width-5))
	editing := s.searching && focused
	if editing && !s.search.Focused() {
		s.search.Focus()
	} else if !editing && s.search.Focused() {
		s.search.Blur()
	}
	field := " " + s.search.View()
	if !editing && s.search.Value() == "" {
		field = spread(" "+m.st.Faint.Render("⌕ Search sessions"), m.st.Faint.Render("/")+" ", width)
	}
	lines := []string{pad(field, width)}
	if editing || m.search.Active() {
		box := "☐"
		if m.search.IncludeArchived {
			box = "☑"
		}
		lines = append(lines, pad(" "+m.st.Subtext.Render(box+" Include archived")+"  "+m.st.Faint.Render("ctrl+a"), width))
	}
	return lines
}

func (s *sidebar) ensureSearch(m *Model) {
	if s.search.Placeholder == "" {
		s.search = textinput.New()
		s.search.Prompt = "⌕ "
		s.search.Placeholder = "Search sessions"
		styleInput(&s.search, m)
	}
}

func (s *sidebar) focusSearch(m *Model) {
	s.ensureSearch(m)
	styleInput(&s.search, m) // the theme may have changed
	s.searching = true
	s.search.Focus()
}

// blurSearch leaves the field, keeping what's typed in it.
func (s *sidebar) blurSearch() {
	s.searching = false
	s.search.Blur()
}

// clearSearch empties the field and leaves it, back to the list's rows.
func (s *sidebar) clearSearch(m *Model) {
	s.search.SetValue("")
	m.search.SetQuery("")
	s.blurSearch()
}

func (s *sidebar) toggleArchived(m *Model) {
	m.search.SetIncludeArchived(!m.search.IncludeArchived)
}

func (s *sidebar) setQuery(m *Model, q string) {
	s.search.SetValue(q)
	s.search.CursorEnd()
	m.search.SetQuery(q)
}

func (s *sidebar) searchHints(m *Model) [][2]string {
	if !m.search.Active() {
		return [][2]string{{"type", "search"}, {"esc", "back"}}
	}
	return [][2]string{{"↑↓", "choose"}, {"enter", "open"}, {"ctrl+a", "archived"}, {"esc", "clear"}}
}

// searchKey is a key in the field: typing searches, the arrows choose a
// result and Return opens it; Esc clears the search.
func (s *sidebar) searchKey(m *Model, k tea.KeyPressMsg) tea.Cmd {
	rows := s.rows(m)
	switch k.String() {
	case "esc":
		s.clearSearch(m)
		return nil
	case "ctrl+a":
		s.toggleArchived(m)
		return nil
	case "up", "down", "pgup", "pgdown":
		if !m.search.Active() {
			// Down into the list's rows.
			if k.String() == "down" {
				s.blurSearch()
			}
			return nil
		}
		d := map[string]int{"up": -1, "down": 1, "pgup": -5, "pgdown": 5}[k.String()]
		if _, ok := s.currentHit(rows); !ok {
			s.cursor = s.moveCursor(rows, "", 1)
			return nil
		}
		step := 1
		if d < 0 {
			step = -1
		}
		for range max(d, -d) {
			s.cursor = s.moveCursor(rows, s.cursor, step)
		}
		return nil
	case "enter":
		if !m.search.Active() {
			s.blurSearch()
			return nil
		}
		r, ok := s.currentHit(rows)
		if !ok {
			for _, row := range rows {
				if row.kind == rowHit {
					r, ok = row, true
					break
				}
			}
		}
		if ok {
			m.openHit(r.hit, true)
		}
		return nil
	}
	before := s.search.Value()
	var cmd tea.Cmd
	s.search, cmd = s.search.Update(k)
	if s.search.Value() != before {
		m.search.SetQuery(s.search.Value())
	}
	return cmd
}

func (s *sidebar) currentHit(rows []sideRow) (sideRow, bool) {
	r, ok := s.current(rows)
	return r, ok && r.kind == rowHit
}

// openHit opens a result's session at the item that matched, which is
// selected and flashes; the results stay, the result chosen.
func (m *Model) openHit(h client.Hit, focus bool) {
	s := h.Result.Session
	if s == nil {
		return
	}
	m.openSession(s.ID)
	if m.sess != nil && m.sess.store.ID == s.ID {
		m.sess.focusOn(h.Result.ItemID)
	}
	m.side.cursor = "r:" + h.ID()
	if focus {
		m.setFocus(focusTranscript)
	}
}

// hitFor is the result for a session, if the results show it.
func (s *sidebar) hitFor(m *Model, id string) (client.Hit, bool) {
	if !m.search.Active() {
		return client.Hit{}, false
	}
	for _, h := range m.search.Hits {
		if h.Result.Session != nil && h.Result.Session.ID == id {
			return h, true
		}
	}
	return client.Hit{}, false
}

// searchRows are the results, then what the search says under them.
func (s *sidebar) searchRows(m *Model) []sideRow {
	width := s.width
	if width <= 0 {
		width = 40
	}
	sr := m.search
	var rows []sideRow
	for _, h := range sr.Hits {
		sess := h.Result.Session
		// The index's copy is the newer.
		if live := m.conn.Sessions[sess.ID]; live != nil {
			sess = live
		}
		lines := m.hitLines(h, sess, width)
		rows = append(rows, sideRow{kind: rowHit, key: "r:" + h.ID(), hit: h, session: sess, lines: lines, height: len(lines) + 1})
	}
	note := func(key string, style lipgloss.Style, text string) {
		var lines []string
		for _, l := range wrap(text, width-2) {
			lines = append(lines, pad(" "+style.Render(l), width))
		}
		rows = append(rows, sideRow{kind: rowNote, key: key, lines: lines, height: len(lines)})
	}
	q := strings.TrimSpace(sr.Query)
	switch {
	case sr.IsSearching:
		note("n:searching", m.st.Faint, m.spinner()+" Searching…")
	case len(sr.Hits) == 0 && len(sr.Failures) == 0 && m.conn.IsConnected() && m.conn.Info != nil && m.conn.Info.Search:
		note("n:none", m.st.Subtext, "No sessions match “"+q+"”.")
	}
	if sr.More {
		note("n:more", m.st.Subtext, "More sessions match. Add words to narrow it down.")
	}
	for _, name := range sr.Indexing {
		note("n:indexing:"+name, m.st.Subtext, name+" is still indexing older sessions, so some may be missing.")
	}
	for _, f := range sr.Failures {
		note("n:failed:"+f.Server, m.st.Subtext, "Couldn't search "+f.Server+": "+f.Message)
	}
	for _, name := range sr.Unsupported {
		note("n:old:"+name, m.st.Subtext, "Update rad on "+name+" to search its sessions.")
	}
	return rows
}

// hitLines draw a result: its session's title and an Archived badge;
// where it is, its agent and age, and how many items matched; and the
// snippet, its matches marked, on two lines at most.
func (m *Model) hitLines(h client.Hit, sess *model.Session, width int) []string {
	title := sess.Title
	if title == "" {
		title = "New session"
	}
	titleStyle := m.st.Text
	if client.StandsOut(sess) {
		titleStyle = m.st.Bold
	}
	badge := ""
	if sess.Archived {
		badge = m.st.Chip.Render("Archived") + " "
	}
	lines := []string{spread(" "+m.glyph(sess)+" "+titleStyle.Render(title), badge, width)}
	var meta []string
	if p := m.conn.Projects[sess.ProjectID]; p != nil {
		meta = append(meta, p.Name)
	}
	meta = append(meta, m.harnessName(sess), client.ShortAge(sess.UpdatedAt, time.Now()))
	matches := ""
	if h.Result.Matches > 1 {
		matches = m.st.Faint.Render(fmt.Sprintf("%d matches", h.Result.Matches)) + " "
	}
	lines = append(lines, spread("   "+m.st.Faint.Render(strings.Join(meta, " · ")), matches, width))
	for _, l := range snippetLines(h.Result.Snippet, width-4, 2, m.st.Subtext, m.st.Match) {
		lines = append(lines, pad("   "+l, width))
	}
	return lines
}

// snippetLines word-wraps a snippet to width, in lines at most, the
// matches in match's style; what doesn't fit ends in "…".
func snippetLines(parts []model.SnippetPart, width, lines int, plain, match lipgloss.Style) []string {
	var text []rune
	var marked []bool
	for _, p := range parts {
		for _, r := range p.Text {
			if unicode.IsSpace(r) {
				r = ' '
				// Runs of space are one.
				if len(text) > 0 && text[len(text)-1] == ' ' {
					continue
				}
			}
			text = append(text, r)
			marked = append(marked, p.Match)
		}
	}
	ranges := wrapRunes(text, max(1, width))
	more := len(ranges) > lines
	if more {
		ranges = ranges[:lines]
	}
	out := make([]string, 0, len(ranges))
	for i, rg := range ranges {
		from, to := rg[0], rg[1]
		for to > from && text[to-1] == ' ' {
			to--
		}
		last := more && i == len(ranges)-1
		if last {
			// Room for the ellipsis.
			for to > from && runesWidth(text[from:to])+1 > width {
				to--
			}
		}
		var b strings.Builder
		for j := from; j < to; {
			k := j
			for k < to && marked[k] == marked[j] {
				k++
			}
			style := plain
			if marked[j] {
				style = match
			}
			b.WriteString(style.Render(string(text[j:k])))
			j = k
		}
		if last {
			b.WriteString(plain.Render("…"))
		}
		out = append(out, b.String())
	}
	return out
}

func runesWidth(rs []rune) int { return ansi.StringWidth(string(rs)) }

// wrapRunes word-wraps text to width, returning each line's runes as
// [from, to); a word longer than a line is cut.
func wrapRunes(text []rune, width int) [][2]int {
	var out [][2]int
	start := 0
	for start < len(text) {
		for start < len(text) && text[start] == ' ' {
			start++
		}
		if start >= len(text) {
			break
		}
		w, end, lastSpace := 0, start, -1
		for end < len(text) {
			cw := ansi.StringWidth(string(text[end]))
			if w+cw > width {
				break
			}
			if text[end] == ' ' {
				lastSpace = end
			}
			w += cw
			end++
		}
		if end < len(text) && text[end] != ' ' && lastSpace > start {
			end = lastSpace
		}
		if end == start {
			end = start + 1
		}
		out = append(out, [2]int{start, end})
		start = end
	}
	return out
}
