package tui

import (
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"remote-agent/internal/client"
	"remote-agent/internal/model"
)

// Notifications, as the Mac app and the web client post them: a session
// needs you, finished its turn, or failed. ra asks the terminal to post
// them: OSC 9 (iTerm2, WezTerm, Ghostty), OSC 99 (kitty) or OSC 777 (foot,
// urxvt), or rings its bell.

type notifier struct {
	osc  string // 9, 99 or 777
	tmux bool
}

type notifyCheckMsg struct {
	id   string
	kind client.AlertKind
}

func newNotifier() notifier {
	n := notifier{osc: "9", tmux: os.Getenv("TMUX") != ""}
	term := os.Getenv("TERM")
	switch {
	case term == "xterm-kitty":
		n.osc = "99"
	case strings.HasPrefix(term, "foot"), strings.HasPrefix(term, "rxvt"):
		n.osc = "777"
	}
	return n
}

// shown: the session is in front of the user, who needs no notification.
func (m *Model) shown(id string) bool {
	return m.sess != nil && m.sess.store.ID == id && m.termFocused
}

func (n notifier) changed(m *Model, old, next *model.Session) tea.Cmd {
	if m.settings.Notifications == "off" {
		return nil
	}
	a := client.Alert(old, next)
	if a.Kind == client.AlertNone || m.shown(next.ID) {
		return nil
	}
	if a.Kind == client.AlertFinished {
		// The next queued prompt starts at once: say it finished only if
		// it still has, a moment later.
		id := next.ID
		return tea.Tick(time.Second, func(time.Time) tea.Msg { return notifyCheckMsg{id: id, kind: a.Kind} })
	}
	return n.post(m, next, a)
}

func (n notifier) check(m *Model, msg notifyCheckMsg) tea.Cmd {
	s := m.conn.Sessions[msg.id]
	if s == nil || s.Status != model.SessionIdle || m.shown(msg.id) {
		return nil
	}
	return n.post(m, s, client.SessionAlert{Kind: msg.kind})
}

func (n notifier) post(m *Model, s *model.Session, a client.SessionAlert) tea.Cmd {
	body := ""
	switch a.Kind {
	case client.AlertNeedsYou:
		body = "needs you"
	case client.AlertFinished:
		body = "finished"
	case client.AlertFailed:
		body = "failed"
		if a.Error != "" {
			body += ": " + a.Error
		}
	}
	if m.settings.Notifications == "bell" {
		return tea.Raw("\a")
	}
	return tea.Raw(n.sequence(clean(s.Title), clean(body)))
}

func (n notifier) sequence(title, body string) string {
	var seq string
	switch n.osc {
	case "99":
		seq = "\x1b]99;i=1:d=0;" + title + "\x1b\\\x1b]99;i=1:d=1:p=body;" + body + "\x1b\\"
	case "777":
		seq = "\x1b]777;notify;" + title + ";" + body + "\a"
	default:
		seq = "\x1b]9;" + title + ": " + body + "\a"
	}
	if n.tmux {
		// tmux passes a sequence on when it's wrapped, with its ESCs doubled.
		seq = "\x1bPtmux;" + strings.ReplaceAll(seq, "\x1b", "\x1b\x1b") + "\x1b\\"
	}
	return seq
}

// clean keeps control characters out of an escape sequence.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == ';' {
			return ' '
		}
		return r
	}, s)
}
