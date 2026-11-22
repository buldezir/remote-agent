package model

import (
	"regexp"
	"strings"
)

// A prompt mentions another session of the same rad as
// @[title](session:<id>), which the clients' @ picker writes. The title is
// for the user to read; the id is what the agent reads the session by, with
// rad's read_session tool. Clients show a mention as @title.

// SessionMention is a session a prompt mentions.
type SessionMention struct {
	Title string
	ID    string
}

var mentionRE = regexp.MustCompile(`@\[([^\[\]\n]*)\]\(session:([0-9A-Za-z-]+)\)`)

// SessionMentions are the sessions a text mentions, each once, in order.
func SessionMentions(text string) []SessionMention {
	var out []SessionMention
	seen := map[string]bool{}
	for _, m := range mentionRE.FindAllStringSubmatch(text, -1) {
		if seen[m[2]] {
			continue
		}
		seen[m[2]] = true
		out = append(out, SessionMention{Title: m[1], ID: m[2]})
	}
	return out
}

// MentionDisplay is a text as the clients show it: each mention as @title.
func MentionDisplay(text string) string {
	if !strings.Contains(text, "](session:") {
		return text
	}
	return mentionRE.ReplaceAllString(text, "@$1")
}
