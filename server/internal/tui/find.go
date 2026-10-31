package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"remote-agent/internal/client"
	"remote-agent/internal/model"
)

// Finding in the open session, as the apps' find bar does: a field atop
// the transcript, ctrl+f, whose matches (client.FindMatches) are tinted,
// their words marked, the current one selected and scrolled to the middle.
// And opening a session at an item, as a search result does: it's
// selected, scrolled to the middle and flashes.

// findBar is the session's find bar, open.
type findBar struct {
	input   textinput.Model
	query   string   // what matches were found for
	version uint64   // and the store's version then
	matches []string // item ids, in the transcript's order
	current int      // the match selected; -1 none
	back    focusArea
}

const flashFor = 2 * time.Second

func (v *sessionView) openFind(m *Model) {
	if v.find == nil {
		in := textinput.New()
		in.Prompt = "⌕ "
		in.Placeholder = "Find in session"
		styleInput(&in, m)
		back := m.focus
		if back != focusComposer {
			back = focusTranscript
		}
		v.find = &findBar{input: in, current: -1, back: back}
	}
	m.setFocus(focusFind)
	v.find.input.Focus()
}

// closeFind closes the find bar, its tints with it; refocus goes back to
// where ctrl+f was pressed.
func (v *sessionView) closeFind(m *Model, refocus bool) {
	if v.find == nil {
		return
	}
	back := v.find.back
	v.find = nil
	if refocus {
		m.setFocus(back)
	} else if m.focus == focusFind {
		m.setFocus(focusTranscript)
	}
}

// refreshFind finds again when the query or the transcript changed. A new
// query starts at its newest match, the transcript opening at its end;
// otherwise the current match stays.
func (v *sessionView) refreshFind() {
	f := v.find
	if f == nil {
		return
	}
	q := strings.TrimSpace(f.input.Value())
	if q == f.query && v.store.Version == f.version {
		return
	}
	was := ""
	if f.current >= 0 && f.current < len(f.matches) {
		was = f.matches[f.current]
	}
	newQuery := q != f.query
	f.query, f.version = q, v.store.Version
	f.matches = client.FindMatches(v.store.Transcript, q)
	switch {
	case len(f.matches) == 0:
		f.current = -1
	case newQuery || was == "":
		v.showMatch(len(f.matches) - 1)
	default:
		f.current = min(f.current, len(f.matches)-1)
		for i, id := range f.matches {
			if id == was {
				f.current = i
			}
		}
	}
}

// stepMatch moves to the next match, or the previous, round the ends.
func (v *sessionView) stepMatch(d int) {
	f := v.find
	if f == nil || len(f.matches) == 0 {
		return
	}
	v.showMatch((f.current + d + len(f.matches)) % len(f.matches))
}

// showMatch makes a match current: selected, scrolled to the middle, and
// a tool call or a plan opened when what matched is folded inside it.
func (v *sessionView) showMatch(i int) {
	f := v.find
	f.current = i
	id := f.matches[i]
	v.cursor, v.center = id, id
	if it := v.store.Items[id]; it != nil && v.expanded[id] == shut {
		switch {
		case it.Kind == model.ItemToolCall && it.Tool != nil && !strings.Contains(strings.ToLower(it.Tool.Title), strings.ToLower(f.query)):
			v.expanded[id] = open
		case it.Kind == model.ItemApproval && it.Approval != nil && it.Approval.Special == model.SpecialPlan:
			v.expanded[id] = open
		}
	}
}

func (v *sessionView) findKey(m *Model, k tea.KeyPressMsg) tea.Cmd {
	f := v.find
	switch k.String() {
	case "esc":
		v.closeFind(m, true)
		return nil
	case "enter", "down", "ctrl+g":
		v.stepMatch(1)
		return nil
	case "up", "shift+enter", "ctrl+shift+g":
		v.stepMatch(-1)
		return nil
	case "pgup":
		v.scrollBy(-max(1, v.avail-2))
		return nil
	case "pgdown":
		v.scrollBy(max(1, v.avail-2))
		return nil
	}
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(k)
	v.refreshFind()
	return cmd
}

func (v *sessionView) findPaste(text string) {
	v.find.input.SetValue(v.find.input.Value() + strings.TrimSpace(strings.ReplaceAll(text, "\n", " ")))
	v.find.input.CursorEnd()
	v.refreshFind()
}

func (v *sessionView) findHints() [][2]string {
	return [][2]string{{"enter ↓", "next"}, {"↑", "previous"}, {"esc", "close"}}
}

// findLines draw the find bar: the field, the count, and a rule.
func (v *sessionView) findLines(m *Model, w int) []string {
	f := v.find
	focused := m.focus == focusFind && len(m.overlays) == 0
	if focused && !f.input.Focused() {
		f.input.Focus()
	} else if !focused && f.input.Focused() {
		f.input.Blur()
	}
	count := ""
	switch {
	case f.query == "":
	case len(f.matches) == 0:
		count = "No matches"
	default:
		count = fmt.Sprintf("%d of %d", f.current+1, len(f.matches))
	}
	f.input.SetWidth(max(8, w-len(count)-8))
	rule := m.st.Rule
	if focused {
		rule = m.st.Accent
	}
	return []string{
		spread(" "+f.input.View(), m.st.Subtext.Render(count)+" ", w),
		rule.Render(strings.Repeat("─", w)),
	}
}

// focusOn shows an item once the transcript has it, as a search result
// opens its session: selected, in the middle, flashing. Without one, the
// session opens at its end, as usual.
func (v *sessionView) focusOn(itemID string) {
	v.focusItem = itemID
	if itemID == "" {
		v.pinned = true
	}
}

// applyFocus selects the item asked for once its block is there; it gives
// up once the store is live without it, as when the item was deleted.
func (v *sessionView) applyFocus() {
	if v.focusItem == "" {
		return
	}
	for _, b := range v.blocks {
		if b.key == v.focusItem {
			v.cursor, v.center = b.key, b.key
			v.flashKey, v.flashAt = b.key, time.Now()
			v.focusItem = ""
			return
		}
	}
	if v.store.Synced && v.store.Live {
		v.focusItem = ""
		v.pinned = true
	}
}

// flashing reports whether an item still flashes, for the ticks that fade
// it.
func (v *sessionView) flashing() bool {
	return v.flashKey != "" && time.Since(v.flashAt) < flashFor
}

// paintMarks tints the lines on screen, the first of which is the
// transcript's line top: the item flashing, the matches of the find bar
// (the current one more) with their words marked.
func (v *sessionView) paintMarks(m *Model, lines []string, top, w int) {
	var flash color.Color
	if v.flashing() {
		left := 1 - float64(time.Since(v.flashAt))/float64(flashFor)
		flash = blend(m.pal.Base, m.pal.Accent, 0.22*left)
	} else {
		v.flashKey = ""
	}
	var matched map[string]bool
	current, query := "", ""
	if f := v.find; f != nil && len(f.matches) > 0 {
		matched = make(map[string]bool, len(f.matches))
		for _, id := range f.matches {
			matched[id] = true
		}
		if f.current >= 0 {
			current = f.matches[f.current]
		}
		query = f.query
	}
	if flash == nil && matched == nil {
		return
	}
	tint := blend(m.pal.Base, m.pal.Yellow, 0.12)
	strong := blend(m.pal.Base, m.pal.Yellow, 0.28)
	word := blend(m.pal.Base, m.pal.Yellow, 0.55)
	for _, b := range v.blocks {
		var bg color.Color
		switch {
		case flash != nil && b.key == v.flashKey:
			bg = flash
		case b.key == current:
			bg = strong
		case matched[b.key]:
			bg = tint
		default:
			continue
		}
		for n := max(b.at, top); n < b.at+len(b.lines) && n < top+len(lines); n++ {
			l := lines[n-top]
			if matched[b.key] {
				l = markOccurrences(l, query, word)
			}
			lines[n-top] = highlight(l, w, bg)
		}
	}
}

// markOccurrences puts bg behind each occurrence of query in a styled
// line, ignoring case. It ends each with the default background, which
// highlight, around it, takes for a reset.
func markOccurrences(line, query string, bg color.Color) string {
	if query == "" {
		return line
	}
	plain := ansi.Strip(line)
	found := foldIndexAll(plain, query)
	if len(found) == 0 {
		return line
	}
	// The occurrences' cells.
	spans := make([][2]int, 0, len(found))
	for _, f := range found {
		from := ansi.StringWidth(plain[:f[0]])
		spans = append(spans, [2]int{from, from + ansi.StringWidth(plain[f[0]:f[1]])})
	}
	on := bgSeq(bg)
	var b strings.Builder
	var state byte
	col, si, in := 0, 0, false
	for s := line; len(s) > 0; {
		seq, width, n, next := ansi.DecodeSequence(s, state, nil)
		state = next
		n = max(n, 1)
		if width > 0 && !in && si < len(spans) && col >= spans[si][0] {
			b.WriteString(on)
			in = true
		}
		b.WriteString(seq)
		if in && width == 0 && strings.HasPrefix(seq, "\x1b[") && strings.HasSuffix(seq, "m") {
			// A style inside it may reset the background.
			b.WriteString(on)
		}
		col += width
		if in && col >= spans[si][1] {
			b.WriteString("\x1b[49m")
			in = false
			si++
		}
		s = s[n:]
	}
	if in {
		b.WriteString("\x1b[49m")
	}
	return b.String()
}

// foldIndexAll is where q is in s, ignoring case, as byte ranges of s
// that don't overlap.
func foldIndexAll(s, q string) [][2]int {
	qr := []rune(q)
	for i, r := range qr {
		qr[i] = unicode.ToLower(r)
	}
	if len(qr) == 0 {
		return nil
	}
	var sr []rune
	var at []int // each rune's byte offset, and the end's
	for i, r := range s {
		sr = append(sr, unicode.ToLower(r))
		at = append(at, i)
	}
	at = append(at, len(s))
	var out [][2]int
	for i := 0; i+len(qr) <= len(sr); {
		ok := true
		for j, r := range qr {
			if sr[i+j] != r {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, [2]int{at[i], at[i+len(qr)]})
			i += len(qr)
		} else {
			i++
		}
	}
	return out
}
