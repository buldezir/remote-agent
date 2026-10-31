package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"remote-agent/internal/client"
	"remote-agent/internal/model"
)

// forkSession is the Fork Session form, as ForkSessionView: the turn the
// fork goes on after, the agent with its model, effort and permissions, a
// worktree, and the next prompt, or none. It shares the new session form's
// rows, but not what that remembers: the session's own agent starts where
// the session is, another at its defaults.
type forkSession struct {
	agentChoice
	src      *model.Session
	turns    []forkTurn // newest first
	turn     int        // the chosen one's n
	worktree bool
	branch   textinput.Model
	prompt   composer
	field    int
	busy     bool
}

// forkTurn is a turn that ended, which a fork can go on after.
type forkTurn struct {
	n      int
	prompt string // its prompt's first line
}

func (t forkTurn) label() string {
	if t.prompt == "" {
		return fmt.Sprintf("Turn %d", t.n)
	}
	return fmt.Sprintf("Turn %d · %s", t.n, t.prompt)
}

type forkedMsg struct {
	f   *forkSession
	s   model.Session
	err error
}

const (
	forkPromptFooter = "Optional. Without one, the fork opens with the conversation so far, and you write the next prompt in it. " +
		"The same agent picks up its own conversation; another agent gets it as a transcript with the first prompt."
	forkWorktreeFooter = "The fork gets its own branch, with the files as they were after that turn."
	forkFolderFooter   = "The fork works in the project folder, as it is now."
)

// forkTurns are the session's turns that ended, newest first, with the
// first lines of their prompts.
func forkTurns(store *client.SessionStore) []forkTurn {
	prompts := map[string]string{}
	for _, it := range store.Transcript {
		if _, ok := prompts[it.TurnID]; !ok && it.Kind == model.ItemUserMessage && it.TurnID != "" {
			prompts[it.TurnID] = firstLine(it.Text)
		}
	}
	var out []forkTurn
	turns := store.SortedTurns()
	for i := len(turns) - 1; i >= 0; i-- {
		if t := turns[i]; t.Status != model.TurnRunning {
			out = append(out, forkTurn{n: t.N, prompt: prompts[t.ID]})
		}
	}
	return out
}

// canFork: rad forks, and a turn of the session has ended. Archived
// sessions fork too.
func (v *sessionView) canFork(m *Model) bool {
	return m.conn.Info != nil && m.conn.Info.Forks && v.store.Session != nil && len(forkTurns(v.store)) > 0
}

// fork opens the fork form for the session.
func (v *sessionView) fork(m *Model) tea.Cmd {
	if !m.conn.IsConnected() {
		m.fail(errors.New("Not connected to rad."))
		return nil
	}
	turns := forkTurns(v.store)
	if v.store.Session == nil || len(turns) == 0 {
		return nil
	}
	m.push(newForkSession(m, v.store.Session, turns))
	return nil
}

func newForkSession(m *Model, src *model.Session, turns []forkTurn) *forkSession {
	f := &forkSession{src: src, turns: turns, turn: turns[0].n, prompt: newComposer(m), branch: newBranchInput(m)}
	f.prompt.ta.Placeholder = "Next prompt (optional)"
	f.prompt.ta.MaxHeight = 8
	f.worktree = src.Workspace.Kind == model.WorkspaceWorktree && f.git(m)
	f.pick(m, src.Harness)
	f.field = fieldPrompt
	f.focusField()
	return f
}

// pick chooses the agent: the session's starts with the session's model,
// effort and permissions, another with its own defaults.
func (f *forkSession) pick(m *Model, id string) {
	f.harness = id
	f.model, f.effort, f.mode = "", "", ""
	if id == f.src.Harness {
		f.model, f.effort, f.mode = f.src.Model, f.src.Effort, f.src.Mode
	}
}

func (f *forkSession) git(m *Model) bool {
	p := m.conn.Projects[f.src.ProjectID]
	return p != nil && p.IsGitRepo
}

// fields are the rows shown, by what the agent and the project offer.
func (f *forkSession) fields(m *Model) []int {
	fs := append([]int{fieldTurn, fieldAgent}, f.optionFields(m)...)
	if f.git(m) {
		fs = append(fs, fieldWorktree)
		if f.worktree {
			fs = append(fs, fieldBranch)
		}
	}
	return append(fs, fieldPrompt, fieldStart)
}

func (f *forkSession) focusField() {
	f.branch.Blur()
	f.prompt.blur()
	switch f.field {
	case fieldBranch:
		f.branch.Focus()
	case fieldPrompt:
		f.prompt.focus()
	}
}

func (f *forkSession) move(m *Model, d int) {
	f.field = stepField(f.fields(m), f.field, d)
	f.focusField()
}

func (f *forkSession) hints() [][2]string {
	if f.field == fieldPrompt {
		return [][2]string{{"enter", "fork"}, {"shift+enter", "new line"}, {"esc", "cancel"}}
	}
	return [][2]string{{"↑↓ tab", "field"}, {"enter", "choose"}, {"esc", "cancel"}}
}

func (f *forkSession) animating() bool { return f.busy }

func (f *forkSession) close(m *Model) { f.busy = false }

func (f *forkSession) message(m *Model, msg tea.Msg) tea.Cmd {
	if r, ok := msg.(forkedMsg); ok && r.f == f {
		return f.forked(m, r)
	}
	return nil
}

// paste goes into the branch or the prompt, as text: a fork's prompt takes
// no images.
func (f *forkSession) paste(m *Model, text string) tea.Cmd {
	if f.field == fieldBranch {
		f.branch.SetValue(f.branch.Value() + strings.TrimSpace(text))
		f.branch.CursorEnd()
		return nil
	}
	f.field = fieldPrompt
	f.focusField()
	f.prompt.ta.InsertString(text)
	return nil
}

func (f *forkSession) update(m *Model, k tea.KeyPressMsg) tea.Cmd {
	if f.busy {
		return nil
	}
	key := k.String()
	switch key {
	case "esc":
		m.pop()
		return nil
	case "tab":
		f.move(m, 1)
		return nil
	case "shift+tab":
		f.move(m, -1)
		return nil
	}
	switch f.field {
	case fieldPrompt:
		switch key {
		case "enter":
			return f.start(m)
		case "shift+enter", "alt+enter", "ctrl+j":
			f.prompt.ta.InsertString("\n")
			return nil
		case "up":
			if f.prompt.ta.Line() == 0 {
				f.move(m, -1)
				return nil
			}
		case "down":
			if f.prompt.ta.Line() >= f.prompt.ta.LineCount()-1 {
				f.move(m, 1)
				return nil
			}
		}
		var cmd tea.Cmd
		f.prompt.ta, cmd = f.prompt.ta.Update(k)
		return cmd
	case fieldBranch:
		switch key {
		case "up", "down", "enter":
			f.move(m, map[bool]int{true: -1, false: 1}[key == "up"])
			return nil
		}
		var cmd tea.Cmd
		f.branch, cmd = f.branch.Update(k)
		return cmd
	}
	switch key {
	case "up":
		f.move(m, -1)
	case "down":
		f.move(m, 1)
	case "left", "right":
		f.cycle(m, map[bool]int{true: -1, false: 1}[key == "left"])
	case "enter", "space":
		return f.choose(m)
	}
	return nil
}

// cycle steps a field's value without opening its menu.
func (f *forkSession) cycle(m *Model, d int) {
	switch f.field {
	case fieldTurn:
		i := 0
		for j, t := range f.turns {
			if t.n == f.turn {
				i = j
			}
		}
		f.turn = f.turns[(i+d+len(f.turns))%len(f.turns)].n
	case fieldWorktree:
		f.worktree = !f.worktree
	default:
		f.cycleOption(m, f.field, d, f.pick)
	}
}

func (f *forkSession) choose(m *Model) tea.Cmd {
	switch f.field {
	case fieldTurn:
		var items []menuItem
		cursor := 0
		for i, t := range f.turns {
			n := t.n
			if n == f.turn {
				cursor = i
			}
			items = append(items, menuItem{label: t.label(), checked: n == f.turn, run: func(*Model) tea.Cmd { f.turn = n; return nil }})
		}
		mn := newMenu("After Turn", items)
		mn.width = 60
		mn.cursor = cursor
		m.push(mn)
	case fieldWorktree:
		f.worktree = !f.worktree
	case fieldStart:
		return f.start(m)
	default:
		f.chooseOption(m, f.field, f.pick)
	}
	return nil
}

func (f *forkSession) start(m *Model) tea.Cmd {
	if !usable(f.h(m)) {
		m.fail(errors.New("Choose an agent that's installed and signed in."))
		return nil
	}
	p := client.ForkSession{
		SessionID: f.src.ID, Turn: f.turn, Harness: f.harness, Model: f.model, Effort: f.effort, Mode: f.mode,
		Prompt: strings.TrimSpace(f.prompt.value()),
	}
	if f.git(m) {
		p.Workspace = &client.NewWorkspace{Kind: model.WorkspaceRoot}
		if f.worktree {
			p.Workspace = &client.NewWorkspace{Kind: model.WorkspaceWorktree, Branch: strings.TrimSpace(f.branch.Value())}
		}
	}
	f.busy = true
	conn := m.conn
	return func() tea.Msg {
		s, err := conn.Fork(context.Background(), p)
		return forkedMsg{f: f, s: s, err: err}
	}
}

// forked opens the fork, as a new session opens once it's made.
func (f *forkSession) forked(m *Model, msg forkedMsg) tea.Cmd {
	f.busy = false
	if msg.err != nil {
		m.fail(msg.err)
		return nil
	}
	for m.topOverlay() != nil {
		m.pop()
	}
	m.openSession(msg.s.ID)
	m.setFocus(focusComposer)
	return nil
}

func (f *forkSession) view(m *Model, width, height int) string {
	w := min(76, width-4)
	inner := w - 4
	value := func(field int) string {
		switch field {
		case fieldTurn:
			for _, t := range f.turns {
				if t.n == f.turn {
					v := fmt.Sprintf("Turn %d", t.n)
					if t.prompt != "" {
						v += m.st.Faint.Render("  " + t.prompt)
					}
					return v
				}
			}
		case fieldWorktree:
			return worktreeValue(m, f.worktree)
		case fieldBranch:
			f.branch.SetWidth(inner - 16)
			return f.branch.View()
		}
		return f.optionValue(m, field)
	}
	lines := []string{m.st.Subtext.Render(truncate("From “"+f.src.Title+"”", inner)), ""}
	for _, field := range f.fields(m) {
		switch field {
		case fieldPrompt:
			lines = append(lines, "")
			lines = append(lines, f.prompt.view(m, nil, inner, f.field == fieldPrompt)...)
			continue
		case fieldStart:
			lines = append(lines, "", formButton(m, "Fork", f.field == fieldStart, f.busy))
			continue
		}
		lines = append(lines, formRow(m, field, value(field), field == f.field, inner))
	}
	footer := ""
	switch f.field {
	case fieldWorktree, fieldBranch:
		footer = forkFolderFooter
		if f.worktree {
			footer = forkWorktreeFooter
		}
	case fieldPrompt, fieldStart:
		footer = forkPromptFooter
	}
	if footer != "" {
		lines = append(lines, "")
		for _, l := range wrap(footer, inner) {
			lines = append(lines, m.st.Faint.Render(l))
		}
	}
	return box(m, "Fork Session", lines, w)
}
