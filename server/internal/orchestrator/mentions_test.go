package orchestrator

import (
	"strings"
	"testing"
)

func TestMentionNote(t *testing.T) {
	if note := mentionNote("no mentions here", "self"); note != "" {
		t.Errorf("note without mentions: %q", note)
	}
	if note := mentionNote("about @[Me](session:self)", "self"); note != "" {
		t.Errorf("note for the session itself: %q", note)
	}
	note := mentionNote("as in @[Fix \"login\"](session:0193-a) and @[Me](session:self)", "self")
	for _, want := range []string{`- "Fix \"login\"": 0193-a`, "read_session"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "self") {
		t.Errorf("note lists the session itself:\n%s", note)
	}
}

func TestTitleFromMention(t *testing.T) {
	if got := titleFrom("compare @[Fix login](session:0193-a) with this\nmore"); got != "compare @Fix login with this" {
		t.Errorf("title = %q", got)
	}
}
