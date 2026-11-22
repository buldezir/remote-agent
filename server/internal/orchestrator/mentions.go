package orchestrator

import (
	"fmt"
	"strings"

	"remote-agent/internal/harness"
	"remote-agent/internal/model"
)

// withMentions adds a note after a prompt that mentions other sessions
// (model.SessionMentions), which tells the agent to read them itself with
// rad's read_session tool: written is the prompt as the user wrote it, which
// their message keeps. An agent without rad's MCP server gets the prompt as
// it is.
func (a *actor) withMentions(in harness.Input, written string) harness.Input {
	if a.o.opt.MCP == nil {
		return in
	}
	if note := mentionNote(written, a.sess.ID); note != "" {
		in.Text = strings.TrimSpace(in.Text) + "\n\n" + note
	}
	return in
}

// mentionNote is the note for the sessions a prompt mentions, but the
// session itself; "" when it mentions none.
func mentionNote(text, self string) string {
	var lines []string
	for _, m := range model.SessionMentions(text) {
		if m.ID == self {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %q: %s", m.Title, m.ID))
	}
	if len(lines) == 0 {
		return ""
	}
	return "<rad-mentions>\nThe user's request mentions other sessions of this rad, which you can't see unless you read them:\n" +
		strings.Join(lines, "\n") +
		"\nRead each with rad's read_session tool, passing its id as session (turns: 0 reads its whole transcript), before you act on the request.\n</rad-mentions>"
}
