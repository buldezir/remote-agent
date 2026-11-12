package tui

import (
	"slices"
	"testing"
)

func TestClipLines(t *testing.T) {
	lines := []string{"one", "two", "three", "four", "five"}
	if got := clipLines(lines[:2], 2, 10); !slices.Equal(got, lines[:2]) {
		t.Errorf("clipLines of what fits = %q", got)
	}
	if got, want := clipLines(lines, 2, 10), []string{"one", "two…"}; !slices.Equal(got, want) {
		t.Errorf("clipLines = %q, want %q", got, want)
	}
	// The ellipsis takes the last line's last column when it's full.
	if got, want := clipLines([]string{"abcd", "efgh", "ijkl"}, 2, 4), []string{"abcd", "efg…"}; !slices.Equal(got, want) {
		t.Errorf("clipLines of full lines = %q, want %q", got, want)
	}
	if lines[1] != "two" {
		t.Errorf("clipLines changed its argument: %q", lines)
	}
}
