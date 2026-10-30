package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Selecting the transcript's text with the mouse. A terminal selects whole
// rows of the screen, the sidebar's half of them too, so ra selects within
// the messages itself, and copies the text when the button comes up.

// textPoint is a cell of the transcript: its line and column.
type textPoint struct{ line, col int }

type textSelection struct {
	down     bool // the button is held
	from, to textPoint
	pressY   int // the row pressed, for a click
	width    int // the transcript's, which a resize reflows
}

func (s textSelection) shown() bool { return s.from != s.to }

// span is the selection in reading order, both ends in it.
func (s textSelection) span() (textPoint, textPoint) {
	a, b := s.from, s.to
	if b.line < a.line || (b.line == a.line && b.col < a.col) {
		a, b = b, a
	}
	return a, b
}

// gutterW is the transcript's left margin, where the cursor shows.
const gutterW = 2

// pointAt is the transcript's cell at x and y in the pane, and which edge
// they're past: -1 above the transcript, 1 below.
func (v *sessionView) pointAt(x, y int) (textPoint, int) {
	top, edge := v.headerHeight, 0
	switch {
	case y < top:
		edge, y = -1, top
	case y >= top+v.avail:
		edge, y = 1, top+v.avail-1
	}
	line := min(max(0, y-top+v.scroll), max(0, len(v.lines)-1))
	return textPoint{line: line, col: max(0, x)}, edge
}

// press starts a drag, or a click, where the button went down.
func (v *sessionView) press(x, y int) {
	p, _ := v.pointAt(x, y)
	v.sel = textSelection{down: true, from: p, to: p, pressY: y, width: v.linesW}
}

// drag extends the selection, scrolling past the transcript's edges.
func (v *sessionView) drag(x, y int) {
	p, edge := v.pointAt(x, y)
	if edge != 0 {
		v.scrollBy(edge)
		p, _ = v.pointAt(x, y)
	}
	v.sel.to = p
}

// release copies what a drag selected; without one, it was a click.
func (v *sessionView) release(m *Model) tea.Cmd {
	v.sel.down = false
	if !v.sel.shown() {
		y := v.sel.pressY
		v.sel = textSelection{}
		v.clickTranscript(m, y)
		return nil
	}
	a, b := v.sel.span()
	text := selectedText(v.lines, a, b)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	m.say("Copied.")
	return tea.SetClipboard(text)
}

// selectedText is the text from a to b, without styles, the gutter or the
// spaces that end lines.
func selectedText(lines []string, a, b textPoint) string {
	var out []string
	for i := a.line; i <= b.line && i < len(lines); i++ {
		from, to := gutterW, 1<<30
		if i == a.line {
			from = max(from, a.col)
		}
		if i == b.line {
			to = b.col + 1
		}
		text := ""
		if to > from {
			text = ansi.Strip(ansi.Cut(lines[i], from, to))
		}
		out = append(out, strings.TrimRight(text, " "))
	}
	return strings.Join(out, "\n")
}

// showSelection draws the selection over the transcript's lines on screen,
// the first of which is line top.
func (v *sessionView) showSelection(m *Model, lines []string, top int) {
	if !v.sel.shown() {
		return
	}
	a, b := v.sel.span()
	for i, l := range lines {
		n := top + i
		if n < a.line || n > b.line {
			continue
		}
		from, to := 0, ansi.StringWidth(l)
		if n == a.line {
			from = a.col
		}
		if n == b.line {
			to = min(to, b.col+1)
		}
		if to > from {
			lines[i] = ansi.Truncate(l, from, "") + highlight(ansi.Cut(l, from, to), to-from, m.pal.Surface2) + ansi.TruncateLeft(l, to, "")
		}
	}
}
