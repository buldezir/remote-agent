package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"remote-agent/internal/client"
	"remote-agent/internal/model"
)

// importList lists the sessions an agent ran in a project's folder outside
// rad, such as in a terminal, to pick one up, as ImportSessionView: the new
// session form's "Continue a terminal session…". Choosing one imports it
// with the form's model, effort and permissions, and opens it.
type importList struct {
	ns        *newSession
	projectID string
	harness   string
	sessions  []model.ImportableSession
	loaded    bool
	loading   bool
	err       string
	cursor    int
	importing string // the session being imported
}

type importableMsg struct {
	il   *importList
	list []model.ImportableSession
	err  error
}

type importedMsg struct {
	il  *importList
	s   model.Session
	err error
}

func newImportList(ns *newSession) *importList {
	return &importList{ns: ns, projectID: ns.projectID, harness: ns.harness}
}

func (il *importList) load(m *Model) tea.Cmd {
	il.loading, il.err = true, ""
	conn, project, harness := m.conn, il.projectID, il.harness
	return func() tea.Msg {
		list, err := conn.Importable(context.Background(), project, harness)
		return importableMsg{il: il, list: list, err: err}
	}
}

func (il *importList) animating() bool { return il.loading || il.importing != "" }

func (il *importList) agentName(m *Model) string {
	if h := m.conn.Harness(il.harness); h != nil {
		return h.Name
	}
	return il.harness
}

func (il *importList) message(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case importableMsg:
		if msg.il != il {
			return nil
		}
		il.loading = false
		if msg.err != nil {
			il.err = msg.err.Error()
			return nil
		}
		il.sessions, il.loaded = msg.list, true
		il.cursor = min(il.cursor, max(0, len(il.sessions)-1))
	case importedMsg:
		if msg.il != il {
			return nil
		}
		il.importing = ""
		if msg.err != nil {
			m.fail(msg.err)
			return nil
		}
		il.ns.remember(m)
		for m.topOverlay() != nil {
			m.pop()
		}
		m.openSession(msg.s.ID)
		m.setFocus(focusComposer)
		m.say("Picked up “" + msg.s.Title + "”.")
	}
	return nil
}

func (il *importList) hints() [][2]string {
	if il.err != "" {
		return [][2]string{{"r", "retry"}, {"esc", "back"}}
	}
	return [][2]string{{"↑↓", "choose"}, {"enter", "continue it here"}, {"esc", "back"}}
}

func (il *importList) update(m *Model, k tea.KeyPressMsg) tea.Cmd {
	if il.importing != "" {
		return nil
	}
	switch k.String() {
	case "esc":
		m.pop()
	case "up", "shift+tab":
		il.cursor = max(0, il.cursor-1)
	case "down", "tab":
		il.cursor = min(len(il.sessions)-1, il.cursor+1)
	case "r":
		if !il.loading {
			return il.load(m)
		}
	case "enter":
		if il.loading || il.cursor >= len(il.sessions) {
			return nil
		}
		ns, s := il.ns, il.sessions[il.cursor]
		il.importing = s.ID
		p := client.ImportSession{ProjectID: il.projectID, Harness: il.harness, NativeID: s.ID}
		if ns.harness == il.harness {
			p.Model, p.Effort, p.Mode = ns.model, ns.effort, ns.mode
		}
		conn := m.conn
		return func() tea.Msg {
			sess, err := conn.Import(context.Background(), p)
			return importedMsg{il: il, s: sess, err: err}
		}
	}
	return nil
}

// importAge is when an agent last wrote to a session, as "2h ago".
func importAge(t time.Time) string {
	age := client.ShortAge(t, time.Now())
	if age == "now" {
		return "just now"
	}
	if strings.ContainsAny(age[len(age)-1:], "mhd") {
		return age + " ago"
	}
	return age
}

func (il *importList) view(m *Model, width, height int) string {
	w := min(76, width-4)
	inner := w - 4
	agent := il.agentName(m)
	project := "this folder"
	if p := m.conn.Projects[il.projectID]; p != nil {
		project = p.Name
	}
	var lines []string
	lines = append(lines, m.st.Subtext.Render(truncate(agent+" · "+project, inner)), "")
	switch {
	case il.loading && !il.loaded:
		lines = append(lines, m.st.Faint.Render(m.spinner()+" Looking for sessions…"))
	case il.err != "":
		lines = append(lines, wrapStyled(m.st.Red, il.err, inner)...)
		lines = append(lines, "", m.st.Faint.Render("r retries"))
	case len(il.sessions) == 0:
		lines = append(lines, wrapStyled(m.st.Faint, fmt.Sprintf("No %s sessions to continue in %s.", agent, project), inner)...)
	default:
		var rows []string
		curStart, curEnd := 0, 0
		for i, s := range il.sessions {
			chosen := i == il.cursor
			if chosen {
				curStart = len(rows)
			}
			marker := "  "
			if chosen {
				marker = m.st.Accent.Render("▌ ")
			}
			title := wrap(s.Title, inner-2)
			if len(title) > 2 {
				title = []string{title[0], truncate(strings.Join(title[1:], " "), inner-2)}
			}
			for j, t := range title {
				line := m.st.Text.Render(t)
				if chosen {
					line = m.st.AccentBold.Render(t)
				}
				if j == 0 && s.ID == il.importing {
					line += " " + m.st.Accent.Render(m.spinner())
				}
				rows = append(rows, marker+line)
			}
			messages := fmt.Sprintf("%d messages", s.Messages)
			if s.Messages == 1 {
				messages = "1 message"
			}
			rows = append(rows, marker+m.st.Faint.Render(truncate(messages+" · "+importAge(s.UpdatedAt), inner-2)))
			if s.Active {
				for _, l := range wrap("⚠ Active in the last minute: if it still runs in a terminal, quit it there first.", inner-2) {
					rows = append(rows, marker+m.st.Yellow.Render(l))
				}
			}
			if chosen {
				curEnd = len(rows)
			}
			if i < len(il.sessions)-1 {
				rows = append(rows, "")
			}
		}
		// The rows that fit, with the chosen session's.
		room := max(4, height-14)
		start := min(max(0, curEnd-room), curStart)
		end := min(len(rows), start+room)
		lines = append(lines, rows[start:end]...)
		if end < len(rows) {
			lines = append(lines, m.st.Faint.Render("  …"))
		}
	}
	lines = append(lines, "")
	lines = append(lines, wrapStyled(m.st.Faint, fmt.Sprintf("Picks up a session %s ran in this folder outside Remote Agent. "+
		"Its next prompt goes on with it here, in the project folder.", agent), inner)...)
	return box(m, "Terminal Sessions", lines, w)
}
