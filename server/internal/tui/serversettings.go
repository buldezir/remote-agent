package tui

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"remote-agent/internal/client"
	"remote-agent/internal/model"
)

// serverSettings is Server Settings, as ServerSettingsView: rad's version
// and its update, the agents' versions, sign-ins and updates, and the
// system agent that names sessions.
type serverSettings struct {
	info      *client.ServerInfo // the connection's, when loaded
	upd       *model.UpdateInfo
	updateErr string
	checking  bool
	settings  *model.ServerSettings
	tasks     *client.ServerTasks
	row       int
}

type updateInfoMsg struct {
	ss  *serverSettings
	u   model.UpdateInfo
	err error
}

type settingsMsg struct {
	ss  *serverSettings
	s   model.ServerSettings
	err error
	// rollback: what to go back to if saving failed.
	rollback *model.ServerSettings
}

type tasksMsg struct {
	ss    *serverSettings
	tasks []model.Task
}

type taskStartedMsg struct {
	ss  *serverSettings
	t   model.Task
	err error
}

func (m *Model) openServerSettings() tea.Cmd {
	if !m.conn.IsConnected() {
		m.fail(fmt.Errorf("Not connected to rad."))
		return nil
	}
	ss := &serverSettings{tasks: client.NewServerTasks(m.q, m.conn)}
	m.push(ss)
	return ss.reload(m)
}

func (ss *serverSettings) reload(m *Model) tea.Cmd {
	ss.info = m.conn.Info
	ss.checking = true
	conn := m.conn
	return tea.Batch(
		func() tea.Msg {
			u, err := conn.CheckUpdate(context.Background(), false)
			return updateInfoMsg{ss: ss, u: u, err: err}
		},
		func() tea.Msg {
			s, err := conn.Settings(context.Background())
			return settingsMsg{ss: ss, s: s, err: err}
		},
		func() tea.Msg {
			ts, _ := conn.Tasks(context.Background())
			return tasksMsg{ss: ss, tasks: ts}
		},
	)
}

func (ss *serverSettings) close(m *Model) { ss.tasks.Stop() }

func (ss *serverSettings) animating() bool {
	if ss.checking {
		return true
	}
	return false
}

func (ss *serverSettings) message(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case updateInfoMsg:
		if msg.ss != ss {
			return nil
		}
		ss.checking = false
		if msg.err != nil {
			ss.updateErr = msg.err.Error()
			if client.ErrorCode(msg.err) == "method_not_found" {
				ss.updateErr = "This rad is too old to update from here. Update it on the computer."
			}
			return nil
		}
		ss.updateErr = ""
		u := msg.u
		ss.upd = &u
	case settingsMsg:
		if msg.ss != ss {
			return nil
		}
		if msg.err != nil {
			m.fail(msg.err)
			ss.settings = msg.rollback
			return nil
		}
		s := msg.s
		ss.settings = &s
	case tasksMsg:
		if msg.ss != ss {
			return nil
		}
		for _, t := range msg.tasks {
			if t.Status == model.TaskRunning {
				ss.tasks.Follow(t)
			}
		}
	case taskStartedMsg:
		if msg.ss != ss {
			return nil
		}
		if msg.err != nil {
			m.fail(msg.err)
			return nil
		}
		m.push(&taskSheet{p: ss.tasks.Follow(msg.t)})
	}
	// After a reconnect, as after rad updated, look again.
	if m.conn.Info != nil && m.conn.Info != ss.info && !ss.checking {
		return ss.reload(m)
	}
	return nil
}

func (ss *serverSettings) hints() [][2]string {
	return [][2]string{{"↑↓", "choose"}, {"enter", "act"}, {"c", "check again"}, {"esc", "close"}}
}

// rows: rad, each agent, then the system agent's agent, model and effort.
func (ss *serverSettings) rowCount(m *Model) int {
	return 1 + len(m.conn.Harnesses) + 3
}

func (ss *serverSettings) update(m *Model, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc", "q":
		m.pop()
	case "up", "k":
		ss.row = max(0, ss.row-1)
	case "down", "j", "tab":
		ss.row = min(ss.rowCount(m)-1, ss.row+1)
	case "c":
		ss.checking, ss.updateErr = true, ""
		conn := m.conn
		return func() tea.Msg {
			u, err := conn.CheckUpdate(context.Background(), true)
			return updateInfoMsg{ss: ss, u: u, err: err}
		}
	case "enter", "space":
		return ss.act(m)
	}
	return nil
}

func (ss *serverSettings) act(m *Model) tea.Cmd {
	n := len(m.conn.Harnesses)
	switch {
	case ss.row == 0:
		return ss.updateRad(m)
	case ss.row <= n:
		return ss.agentMenu(m, m.conn.Harnesses[ss.row-1])
	default:
		return ss.systemAgentMenu(m, ss.row-n-1)
	}
}

func (ss *serverSettings) startTask(m *Model, start func(ctx context.Context) (model.Task, error)) tea.Cmd {
	return func() tea.Msg {
		t, err := start(context.Background())
		return taskStartedMsg{ss: ss, t: t, err: err}
	}
}

func (ss *serverSettings) updateRad(m *Model) tea.Cmd {
	if p := ss.tasks.Active(model.TaskUpdateServer, ""); p != nil {
		m.push(&taskSheet{p: p})
		return nil
	}
	u := ss.upd
	if u == nil || !u.Available || u.Via == "" {
		return nil
	}
	conn := m.conn
	run := func(m *Model) tea.Cmd { return ss.startTask(m, conn.UpdateServer) }
	if len(u.Busy) > 0 {
		msg := fmt.Sprintf("rad restarts to update, which stops %s: %s.", plural(len(u.Busy), "session", "sessions"), strings.Join(u.Busy, ", "))
		m.push(newConfirm(fmt.Sprintf("Interrupt %s?", plural(len(u.Busy), "session", "sessions")), msg,
			menuItem{label: "Update to " + u.Latest, danger: true, run: run}))
		return nil
	}
	return run(m)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func (ss *serverSettings) agentMenu(m *Model, h model.HarnessInfo) tea.Cmd {
	for _, kind := range []model.TaskKind{model.TaskLogin, model.TaskUpdateAgent} {
		if p := ss.tasks.Active(kind, h.ID); p != nil {
			m.push(&taskSheet{p: p})
			return nil
		}
	}
	id := h.ID
	conn := m.conn
	var items []menuItem
	if h.Caps.Login {
		label := "Sign In Again"
		if !h.AuthOK {
			label = "Sign In"
		}
		items = append(items, menuItem{label: label, detail: "Runs the agent's sign-in on the computer and gives you its page.", run: func(m *Model) tea.Cmd {
			return ss.startTask(m, func(ctx context.Context) (model.Task, error) { return conn.LoginHarness(ctx, id) })
		}})
	}
	if h.Caps.Update {
		items = append(items, menuItem{label: "Update", detail: "Runs the agent's own updater.", run: func(m *Model) tea.Cmd {
			return ss.startTask(m, func(ctx context.Context) (model.Task, error) { return conn.UpdateHarness(ctx, id) })
		}})
	}
	if len(items) == 0 {
		items = append(items, menuItem{label: "Nothing to do from here.", disabled: true})
	}
	m.push(newMenu(h.Name, items))
	return nil
}

func (ss *serverSettings) systemAgentMenu(m *Model, field int) tea.Cmd {
	if ss.settings == nil {
		return nil
	}
	cur := ss.settings.SystemAgent
	save := func(next *model.SystemAgent) func(*Model) tea.Cmd {
		return func(m *Model) tea.Cmd {
			before := *ss.settings
			ss.settings = &model.ServerSettings{SystemAgent: next}
			conn := m.conn
			want := *ss.settings
			return func() tea.Msg {
				s, err := conn.SetSettings(context.Background(), want)
				return settingsMsg{ss: ss, s: s, err: err, rollback: &before}
			}
		}
	}
	var items []menuItem
	switch field {
	case 0:
		items = append(items, menuItem{label: "None", detail: "Sessions are named after their first prompt.", checked: cur == nil, run: save(nil)})
		for _, h := range m.conn.Harnesses {
			if !h.Caps.OneShot || !usable(&h) {
				continue
			}
			id := h.ID
			items = append(items, menuItem{label: h.Name, checked: cur != nil && cur.Harness == id, run: save(&model.SystemAgent{Harness: id})})
		}
		m.push(newMenu("System Agent", items))
	case 1, 2:
		if cur == nil {
			return nil
		}
		h := m.conn.Harness(cur.Harness)
		if h == nil {
			return nil
		}
		if field == 1 {
			items = append(items, menuItem{label: "Default", checked: cur.Model == "", run: save(&model.SystemAgent{Harness: cur.Harness})})
			for _, c := range h.Models {
				id := c.ID
				items = append(items, menuItem{label: c.Name, checked: cur.Model == id, run: save(&model.SystemAgent{Harness: cur.Harness, Model: id})})
			}
			m.push(newMenu("Model", items))
			return nil
		}
		efforts := h.Efforts
		if i := choiceIndex(h.Models, cur.Model); i >= 0 && len(h.Models[i].Efforts) > 0 {
			efforts = h.Models[i].Efforts
		}
		items = append(items, menuItem{label: "Default", checked: cur.Effort == "", run: save(&model.SystemAgent{Harness: cur.Harness, Model: cur.Model})})
		for _, c := range efforts {
			id := c.ID
			items = append(items, menuItem{label: c.Name, checked: cur.Effort == id, run: save(&model.SystemAgent{Harness: cur.Harness, Model: cur.Model, Effort: id})})
		}
		m.push(newMenu("Effort", items))
	}
	return nil
}

func (ss *serverSettings) view(m *Model, width, height int) string {
	w := min(76, width-4)
	inner := w - 4
	var lines []string
	row := 0
	line := func(left, right string) {
		marker := "  "
		if row == ss.row {
			marker = m.st.Accent.Render("▌ ")
		}
		lines = append(lines, spread(marker+left, right, inner))
		row++
	}
	version := "rad"
	if m.conn.Info != nil {
		version = "rad " + m.conn.Info.Version
	}
	lines = append(lines, m.st.Header.Render("RAD"))
	status := ""
	action := ""
	switch {
	case ss.tasks.Active(model.TaskUpdateServer, "") != nil:
		status = m.spinner() + " Updating…"
	case ss.checking:
		status = m.spinner() + " Checking…"
	case ss.updateErr != "":
		status = m.st.Red.Render(ss.updateErr)
	case ss.upd != nil && !ss.upd.Available:
		status = "Up to date."
		if ss.upd.Latest == "" && ss.upd.Reason != "" {
			status = ss.upd.Reason
		}
	case ss.upd != nil && ss.upd.Via == "":
		status = ss.upd.Latest + " is out. " + ss.upd.Reason
	case ss.upd != nil:
		status = ss.upd.Latest + " is out."
		action = m.st.ButtonOn.Render("Update to " + ss.upd.Latest)
	}
	line(m.st.Bold.Render(version), action)
	for _, l := range wrap(status, inner-4) {
		lines = append(lines, "    "+m.st.Subtext.Render(l))
	}
	if ss.upd != nil && ss.upd.Available && ss.upd.URL != "" {
		lines = append(lines, "    "+m.st.Faint.Render(truncate("What's new: "+ss.upd.URL, inner-4)))
	}
	if ss.upd != nil && ss.upd.Via == "app" {
		lines = append(lines, "    "+m.st.Faint.Render("The Remote Agent Server app installs it, and restarts rad."))
	}

	lines = append(lines, "", m.st.Header.Render("AGENTS"))
	for _, h := range m.conn.Harnesses {
		state := m.st.Green.Render("Ready")
		switch {
		case ss.tasks.Active(model.TaskLogin, h.ID) != nil:
			state = m.st.Accent.Render(m.spinner() + " Signing in…")
		case ss.tasks.Active(model.TaskUpdateAgent, h.ID) != nil:
			state = m.st.Accent.Render(m.spinner() + " Updating…")
		case !h.Installed:
			state = m.st.Faint.Render("Not installed")
		case !h.AuthOK:
			hint := h.Hint
			if hint == "" {
				hint = "Not signed in"
			}
			state = m.st.Yellow.Render(truncate(hint, inner/2))
		}
		line(m.st.Text.Render(h.Name)+" "+m.st.Faint.Render(h.Version), state)
	}
	if len(m.conn.Harnesses) == 0 {
		lines = append(lines, "  "+m.st.Faint.Render("No agents are installed on this computer."))
	}

	lines = append(lines, "", m.st.Header.Render("SYSTEM AGENT"))
	agent, mdl, effort := "None", "Default", "Default"
	if ss.settings == nil {
		agent = m.spinner()
	} else if sa := ss.settings.SystemAgent; sa != nil {
		agent = sa.Harness
		if h := m.conn.Harness(sa.Harness); h != nil {
			agent = h.Name
			if i := choiceIndex(h.Models, sa.Model); i >= 0 {
				mdl = h.Models[i].Name
			} else if sa.Model != "" {
				mdl = sa.Model
			}
		}
		if sa.Effort != "" {
			effort = sa.Effort
		}
	}
	line(m.st.Subtext.Render(pad("Agent", 10))+m.st.Text.Render(agent), "")
	line(m.st.Subtext.Render(pad("Model", 10))+m.st.Text.Render(mdl), "")
	line(m.st.Subtext.Render(pad("Effort", 10))+m.st.Text.Render(effort), "")
	for _, l := range wrap("It names new sessions, and their worktrees' branches, from their first prompt.", inner-4) {
		lines = append(lines, "    "+m.st.Faint.Render(l))
	}
	return box(m, "Server Settings", lines, w)
}

// taskSheet follows an update or a sign-in: what it printed, and the
// sign-in's page and code.
type taskSheet struct {
	p    *client.ServerTaskProgress
	code textinput.Model
	init bool
}

func (ts *taskSheet) animating() bool { return ts.p.IsActive() }

func (ts *taskSheet) hints() [][2]string {
	h := [][2]string{{"esc", "close"}}
	if ts.p.Task.URL != "" {
		h = append([][2]string{{"o", "open page"}, {"c", "copy link"}}, h...)
	}
	if ts.p.Task.Input && ts.p.IsActive() {
		h = [][2]string{{"enter", "send code"}, {"esc", "close"}}
	}
	if ts.p.IsActive() {
		h = append(h, [2]string{"ctrl+x", "cancel task"})
	}
	return h
}

func (ts *taskSheet) update(m *Model, k tea.KeyPressMsg) tea.Cmd {
	t := ts.p.Task
	takesCode := t.Input && ts.p.IsActive()
	switch k.String() {
	case "esc":
		m.pop()
		return nil
	case "ctrl+x":
		p := ts.p
		return m.do(func(ctx context.Context) error { return p.Cancel(ctx) }, nil)
	case "enter":
		if takesCode && strings.TrimSpace(ts.code.Value()) != "" {
			code := strings.TrimSpace(ts.code.Value())
			ts.code.SetValue("")
			p := ts.p
			return m.do(func(ctx context.Context) error { return p.Send(ctx, code) }, nil)
		}
		return nil
	}
	if takesCode {
		var cmd tea.Cmd
		ts.code, cmd = ts.code.Update(k)
		return cmd
	}
	switch k.String() {
	case "c":
		if t.URL != "" {
			m.say("Copied the link.")
			return tea.SetClipboard(t.URL)
		}
	case "o":
		if t.URL != "" {
			return openURL(m, t.URL)
		}
	}
	return nil
}

func (ts *taskSheet) paste(m *Model, text string) tea.Cmd {
	ts.code.SetValue(ts.code.Value() + strings.TrimSpace(text))
	ts.code.CursorEnd()
	return nil
}

func openURL(m *Model, url string) tea.Cmd {
	if m.sshSession {
		m.say("Open the link on your own computer: c copies it.")
		return nil
	}
	return m.do(func(context.Context) error {
		opener := "xdg-open"
		if runtime.GOOS == "darwin" {
			opener = "open"
		}
		return exec.Command(opener, url).Start()
	}, nil)
}

func (ts *taskSheet) view(m *Model, width, height int) string {
	if !ts.init {
		ts.code = textinput.New()
		ts.code.Placeholder = "Paste the code"
		ts.code.Prompt = "› "
		styleInput(&ts.code, m)
		ts.code.Focus()
		ts.init = true
	}
	t := ts.p.Task
	w := min(84, width-4)
	inner := w - 4
	title := "Updating rad"
	switch t.Kind {
	case model.TaskUpdateAgent:
		title = "Updating " + t.Harness
	case model.TaskLogin:
		title = "Signing In to " + t.Harness
	}
	if h := m.conn.Harness(t.Harness); h != nil {
		title = strings.Replace(title, t.Harness, h.Name, 1)
	}
	var lines []string
	switch ts.p.Phase.Kind {
	case client.TaskPhaseRunning:
		lines = append(lines, m.st.Accent.Render(m.spinner()+" Running"))
	case client.TaskPhaseRestarting:
		lines = append(lines, m.st.Accent.Render(m.spinner()+" rad is restarting…"))
	case client.TaskPhaseDone:
		done := "Done."
		if t.Version != "" {
			done = "Done: " + t.Version
		}
		lines = append(lines, m.st.Green.Render("✓ "+done))
	case client.TaskPhaseFailed:
		lines = append(lines, m.st.Red.Render(truncate("✗ "+ts.p.Phase.Message, inner)))
	case client.TaskPhaseCancelled:
		lines = append(lines, m.st.Faint.Render("Cancelled."))
	}
	if t.URL != "" {
		lines = append(lines, "", m.st.Text.Render("Sign-in page:"))
		lines = append(lines, "\x1b]8;;"+t.URL+"\x1b\\"+m.st.Blue.Render(truncate(t.URL, inner))+"\x1b]8;;\x1b\\")
	}
	if t.Input && ts.p.IsActive() {
		ts.code.SetWidth(inner - 4)
		lines = append(lines, "", ts.code.View())
	}
	out := strings.Split(strings.TrimRight(t.Output, "\n"), "\n")
	room := max(3, height-len(lines)-8)
	if len(out) > room {
		out = out[len(out)-room:]
	}
	lines = append(lines, "", m.st.Header.Render("OUTPUT"))
	for _, l := range out {
		lines = append(lines, m.st.Subtext.Render(truncate(l, inner)))
	}
	return box(m, title, lines, w)
}
