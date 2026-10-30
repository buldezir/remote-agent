package tui

import "testing"

func TestSelectedText(t *testing.T) {
	lines := []string{"  \x1b[1mHello\x1b[0m world", "▌ second line  ", "", "  日本語"}
	for _, c := range []struct {
		a, b textPoint
		want string
	}{
		// Each line from its gutter on, without styles or the spaces after.
		{textPoint{0, 4}, textPoint{1, 40}, "llo world\nsecond line"},
		{textPoint{0, 0}, textPoint{0, 6}, "Hello"},
		{textPoint{1, 9}, textPoint{3, 5}, "line\n\n日本"},
		{textPoint{0, 0}, textPoint{0, 1}, ""},
	} {
		if got := selectedText(lines, c.a, c.b); got != c.want {
			t.Errorf("selectedText(%v, %v) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}
