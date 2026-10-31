package tui

import (
	"fmt"
	"image/color"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// wrap word-wraps plain or styled text to width, breaking words longer
// than a line.
func wrap(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	return strings.Split(ansi.Wrap(strings.ReplaceAll(s, "\t", "    "), width, ""), "\n")
}

// truncate cuts a line to width, ending it with "…" if it was longer.
func truncate(s string, width int) string {
	if width < 1 {
		return ""
	}
	return ansi.Truncate(s, width, "…")
}

// pad fills a line with spaces to width, cutting it if longer.
func pad(s string, width int) string {
	w := ansi.StringWidth(s)
	if w > width {
		return ansi.Truncate(s, width, "")
	}
	return s + strings.Repeat(" ", width-w)
}

// spread puts left at the start and right at the end of a line width wide,
// cutting left to make room.
func spread(left, right string, width int) string {
	rw := ansi.StringWidth(right)
	if rw >= width {
		return truncate(right, width)
	}
	left = truncate(left, width-rw-1)
	return left + strings.Repeat(" ", width-ansi.StringWidth(left)-rw) + right
}

// indent prefixes each line.
func indent(lines []string, prefix string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = prefix + l
	}
	return out
}

// firstLine is the first line of s, trimmed.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// fill makes lines exactly height lines of width, padding or cutting.
func fill(lines []string, width, height int) string {
	var b strings.Builder
	for i := 0; i < height; i++ {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		b.WriteString(pad(l, width))
		if i < height-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// highlight pads a styled line to width on bg, keeping bg across the
// resets its styles end with, which a style around it would lose.
func highlight(line string, width int, bg color.Color) string {
	seq := bgSeq(bg)
	l := pad(line, width)
	for _, reset := range []string{"\x1b[0m", "\x1b[m", "\x1b[49m"} {
		l = strings.ReplaceAll(l, reset, reset+seq)
	}
	return seq + l + "\x1b[0m"
}

// bgSeq is the sequence that sets the background to c.
func bgSeq(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r>>8, g>>8, b>>8)
}
