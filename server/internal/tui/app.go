// Package tui is ra's terminal interface: the apps' views
// (apple/Shared/Views, web/src/ui) drawn with Bubble Tea, for the rad on
// this machine. Its state is the client package's, which it changes only
// in Update: the client's Queue is drained there.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"remote-agent/internal/client"
	"remote-agent/internal/model"
)

// Options are what ra starts the interface with.
type Options struct {
	Queue   *client.Queue
	Conn    *client.ServerConnection
	Store   *client.ServerStore
	Dir     string // settings and state
	Version string
	// Theme and Layout, when set, replace the settings' without saving them.
	Theme  string
	Layout string
	// Cwd is the folder ra was started in: a new session starts in the
	// project that holds it.
	Cwd string
}

type focusArea int

const (
	focusSidebar focusArea = iota
	focusTranscript
	focusComposer // or the approval dock in its place
)

// Messages.

type drainMsg struct{}
type tickMsg time.Time

// doneMsg ends a command run off the dispatcher: then runs on it, if the
// command worked.
type doneMsg struct {
	err  error
	then func(m *Model) tea.Cmd
}

// Model is the whole interface.
type Model struct {
	opts     Options
	q        *client.Queue
	conn     *client.ServerConnection
	settings Settings
	state    State

	pal  Palette
	st   Styles
	dark bool // the terminal's background, until told otherwise

	width, height int
	focus         focusArea
	side          sidebar
	sess          *sessionView
	opened        client.OpenedSession
	overlays      []overlay

	termFocused bool
	frame       int
	toast       string
	toastErr    bool
	toastAt     time.Time
	quitAt      time.Time
	pending     []tea.Cmd
	images      *imageStore
	notifier    notifier
	sshSession  bool
	cwd         string // Options.Cwd with its symlinks resolved, as rad resolves a project's path
	// unsent is a new session's prompt kept for the next one, when its
	// form closed and rad couldn't keep it as a draft.
	unsent *unsentPrompt
}

type unsentPrompt struct {
	text   string
	images []model.ImageRef
}

// New makes the interface; Run runs it.
func New(opts Options) *Model {
	m := &Model{
		opts:        opts,
		q:           opts.Queue,
		conn:        opts.Conn,
		settings:    LoadSettings(opts.Dir),
		state:       loadState(opts.Dir),
		dark:        true,
		termFocused: true,
		focus:       focusSidebar,
		sshSession:  os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != "",
	}
	if opts.Theme != "" {
		m.settings.Theme = opts.Theme
	}
	if opts.Layout != "" {
		m.settings.Layout = client.Layout(opts.Layout)
	}
	m.settings.normalize()
	if real, err := filepath.EvalSymlinks(opts.Cwd); err == nil {
		m.cwd = real
	}
	m.images = newImageStore()
	m.notifier = newNotifier()
	m.applyTheme()
	m.conn.OnSessionChange = m.sessionChanged
	return m
}

// Run runs the interface until it quits.
func Run(opts Options) error {
	m := New(opts)
	_, err := tea.NewProgram(m).Run()
	// The session open now opens again next time.
	if m.sess != nil {
		m.sess.leave(m)
	}
	m.saveState()
	return err
}

func (m *Model) applyTheme() {
	dark := m.dark
	switch m.settings.Theme {
	case "dark":
		dark = true
	case "light":
		dark = false
	}
	if m.pal.Base != nil && m.pal.Dark == dark {
		return
	}
	m.pal = NewPalette(dark)
	m.st = NewStyles(m.pal)
	if m.sess != nil {
		m.sess.restyle(m)
	}
}

func (m *Model) Init() tea.Cmd {
	m.conn.Start()
	cmds := []tea.Cmd{waitQueue(m.q), tick(0), tea.RequestBackgroundColor}
	if id := m.state.LastSession; id != "" {
		m.openSession(id)
		m.setFocus(focusComposer)
	}
	return tea.Batch(cmds...)
}

func waitQueue(q *client.Queue) tea.Cmd {
	return func() tea.Msg {
		<-q.Ready()
		return drainMsg{}
	}
}

func tick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// do runs job off the dispatcher, then then on it.
func (m *Model) do(job func(ctx context.Context) error, then func(m *Model) tea.Cmd) tea.Cmd {
	return func() tea.Msg {
		return doneMsg{err: job(context.Background()), then: then}
	}
}

func (m *Model) say(text string) {
	m.toast, m.toastErr, m.toastAt = text, false, time.Now()
}

func (m *Model) fail(err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	m.toast, m.toastErr, m.toastAt = err.Error(), true, time.Now()
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// What the client posted happens first, so a command's result finds the
	// state its job left.
	m.q.Drain()
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case drainMsg:
		cmds = append(cmds, waitQueue(m.q))
	case tickMsg:
		m.frame++
		if m.toast != "" && time.Since(m.toastAt) > 6*time.Second {
			m.toast = ""
		}
		d := 2 * time.Second
		if m.animating() {
			d = 120 * time.Millisecond
		}
		cmds = append(cmds, tick(d))
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.BackgroundColorMsg:
		m.dark = msg.IsDark()
		m.applyTheme()
	case tea.FocusMsg:
		m.termFocused = true
		m.conn.Check()
	case tea.BlurMsg:
		m.termFocused = false
	case doneMsg:
		if msg.err != nil {
			m.fail(msg.err)
		} else if msg.then != nil {
			cmds = append(cmds, msg.then(m))
		}
	case imageLoadedMsg:
		m.images.loaded(msg)
	case notifyCheckMsg:
		cmds = append(cmds, m.notifier.check(m, msg))
	case tea.KeyPressMsg:
		cmds = append(cmds, m.key(msg))
	case tea.PasteMsg:
		cmds = append(cmds, m.paste(msg.Content))
	case tea.MouseWheelMsg:
		m.wheel(msg)
	case tea.MouseClickMsg:
		cmds = append(cmds, m.click(msg))
	case tea.MouseMotionMsg:
		m.motion(msg)
	case tea.MouseReleaseMsg:
		cmds = append(cmds, m.release(msg))
	default:
		for _, o := range slices.Clone(m.overlays) {
			if u, ok := o.(interface{ message(*Model, tea.Msg) tea.Cmd }); ok {
				cmds = append(cmds, u.message(m, msg))
			}
		}
		if m.sess != nil {
			cmds = append(cmds, m.sess.message(m, msg))
		}
	}
	m.q.Drain()
	cmds = append(cmds, m.afterUpdate()...)
	cmds = append(cmds, m.pending...)
	m.pending = nil
	return m, tea.Batch(cmds...)
}

// afterUpdate keeps the views in step with the state: the open session's
// read mark, a session deleted elsewhere, the images to load.
func (m *Model) afterUpdate() []tea.Cmd {
	var cmds []tea.Cmd
	// Focus stays on what shows, as the window narrows or widens.
	if l := m.layout(); m.focus == focusSidebar && !l.showSide && m.sess != nil {
		m.setFocus(focusComposer)
	} else if m.focus != focusSidebar && (m.sess == nil || !l.showPane) {
		m.focus = focusSidebar
	}
	if m.sess != nil {
		id := m.sess.store.ID
		gone := m.sess.store.Removed || (m.conn.IndexSynced && m.conn.Sessions[id] == nil && m.sess.store.Session == nil)
		if gone {
			m.closeSession()
			m.say("The session was deleted.")
		} else if s := m.conn.Sessions[id]; s != nil && s.Unread && m.termFocused {
			m.conn.MarkRead(id)
		}
	}
	// Landing on an archived session shows its row, once: the fold still
	// closes.
	if m.sess != nil && m.sess.store.Session != nil && m.sess.store.Session.Archived {
		if id := m.sess.store.ID; m.side.revealed != id {
			m.side.revealed = id
			if !m.state.Folded["archived"] {
				m.state.Folded["archived"] = true
				m.saveState()
			}
		}
	} else {
		m.side.revealed = ""
	}
	if m.sess != nil {
		cmds = append(cmds, m.sess.sync(m))
	}
	cmds = append(cmds, m.images.fetch(m))
	return cmds
}

func (m *Model) animating() bool {
	for _, s := range m.conn.Sessions {
		if s.Status == model.SessionRunning {
			return true
		}
	}
	if m.conn.State.Kind == client.Connecting {
		return true
	}
	if m.sess != nil && (!m.sess.store.Synced || m.sess.uploading()) {
		return true
	}
	for _, o := range m.overlays {
		if a, ok := o.(interface{ animating() bool }); ok && a.animating() {
			return true
		}
	}
	return false
}

func (m *Model) spinner() string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	return frames[m.frame%len(frames)]
}

// Sessions

func (m *Model) openSession(id string) {
	if m.sess != nil && m.sess.store.ID == id {
		return
	}
	m.closeSession()
	store := m.conn.SessionStore(id)
	store.Activate()
	m.sess = newSessionView(m, store)
	stood := false
	if s := m.conn.Sessions[id]; s != nil {
		stood = client.StandsOut(s)
	}
	m.opened = client.OpenedSession{ID: id, StoodOut: stood}
	// Columns shows it in its project, or the archive, when the chosen
	// collection doesn't have it.
	if s := m.conn.Sessions[id]; s != nil && m.settings.Layout == client.LayoutColumns {
		in := false
		for _, cs := range client.CollectionSessions(m.side.collection(m), m.conn) {
			in = in || cs.ID == id
		}
		if !in {
			c := client.RevealCollection(s, m.conn)
			m.state.Collection = c.String()
			m.side.collCur = "c:" + c.String()
		}
	}
	m.state.LastSession = id
	m.state.Recent = client.AddingRecent(m.state.Recent, id)
	m.side.cursor = "s:" + id
	m.saveState()
}

func (m *Model) closeSession() {
	if m.sess == nil {
		return
	}
	m.sess.leave(m)
	m.sess.store.Deactivate()
	m.sess = nil
	m.opened = client.OpenedSession{}
	m.state.LastSession = ""
	if m.focus != focusSidebar {
		m.focus = focusSidebar
	}
}

// sessionChanged hears of a session in the index that changed, for
// notifications.
func (m *Model) sessionChanged(old, next *model.Session) {
	if cmd := m.notifier.changed(m, old, next); cmd != nil {
		m.pending = append(m.pending, cmd)
	}
}

func (m *Model) saveState() {
	if m.opts.Dir != "" {
		saveJSON(m.opts.Dir+"/state.json", m.state)
	}
}

func (m *Model) saveSettings() {
	if m.opts.Dir != "" {
		saveJSON(m.opts.Dir+"/settings.json", m.settings)
	}
}

// Overlays

func (m *Model) push(o overlay) { m.overlays = append(m.overlays, o) }

func (m *Model) pop() {
	if n := len(m.overlays); n > 0 {
		if c, ok := m.overlays[n-1].(interface{ close(*Model) }); ok {
			c.close(m)
		}
		m.overlays = m.overlays[:n-1]
	}
}

func (m *Model) topOverlay() overlay {
	if n := len(m.overlays); n > 0 {
		return m.overlays[n-1]
	}
	return nil
}

// Layout

type layout struct {
	wide     bool
	sideW    int // 0: no sidebar
	collW    int // Columns: the collections' column, inside sideW
	paneW    int // 0: no session pane
	bodyH    int
	showSide bool
	showPane bool
}

func (m *Model) layout() layout {
	l := layout{bodyH: max(1, m.height-1), wide: m.width >= 100}
	if !l.wide {
		if m.sess != nil {
			l.showPane, l.paneW = true, m.width
		} else {
			l.showSide, l.sideW = true, m.width
		}
		return l
	}
	l.showPane = true
	if m.state.SidebarHidden && m.sess != nil {
		l.paneW = m.width
		return l
	}
	l.showSide = true
	l.sideW = min(44, max(30, m.width/3))
	if m.settings.Layout == client.LayoutColumns && m.width >= 150 {
		l.collW = 22
		l.sideW = l.collW + 1 + 36
	}
	l.paneW = m.width - l.sideW - 1
	return l
}

// paneX is the session pane's first column.
func (l layout) paneX() int {
	if l.showSide {
		return l.sideW + 1
	}
	return 0
}

// View

func (m *Model) View() tea.View {
	var v tea.View
	v.AltScreen = true
	v.ReportFocus = true
	if m.settings.Mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	v.BackgroundColor = m.pal.Base
	v.ForegroundColor = m.pal.Text
	v.WindowTitle = m.windowTitle()
	if m.width <= 0 || m.height <= 0 {
		return v
	}
	v.SetContent(m.render())
	return v
}

func (m *Model) windowTitle() string {
	n := 0
	for _, s := range m.conn.Sessions {
		if !s.Archived && client.StandsOut(s) {
			n++
		}
	}
	title := "ra"
	if m.sess != nil && m.sess.store.Session != nil {
		title = m.sess.store.Session.Title + " — ra"
	}
	if n > 0 {
		title = fmt.Sprintf("(%d) %s", n, title)
	}
	return title
}

func (m *Model) render() string {
	l := m.layout()
	var cols []string
	if l.showSide {
		cols = append(cols, m.side.view(m, l))
	}
	if l.showSide && l.showPane {
		sep := m.st.Rule.Render(strings.TrimSuffix(strings.Repeat("│\n", l.bodyH), "\n"))
		cols = append(cols, sep)
	}
	if l.showPane {
		if m.sess != nil {
			cols = append(cols, m.sess.view(m, l.paneW, l.bodyH))
		} else {
			cols = append(cols, m.emptyPane(l.paneW, l.bodyH))
		}
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top, cols...)
	screen := body + "\n" + m.statusBar()
	if len(m.overlays) == 0 {
		return screen
	}
	layers := []*lipgloss.Layer{lipgloss.NewLayer(screen)}
	for i, o := range m.overlays {
		box := o.view(m, m.width, m.height)
		x := max(0, (m.width-lipgloss.Width(box))/2)
		y := max(0, (m.height-lipgloss.Height(box))/3)
		if f, ok := o.(interface{ fullscreen() bool }); ok && f.fullscreen() {
			x, y = 0, 0
		}
		layers = append(layers, lipgloss.NewLayer(box).X(x).Y(y).Z(i+1))
	}
	canvas := lipgloss.NewCanvas(m.width, m.height)
	canvas.Compose(lipgloss.NewCompositor(layers...))
	return canvas.Render()
}

func (m *Model) emptyPane(w, h int) string {
	lines := make([]string, h)
	msg := []string{
		m.st.Bold.Render("No session open"),
		"",
		m.st.Faint.Render("Choose one on the left, or press ") + m.st.Key.Render("ctrl+n") + m.st.Faint.Render(" to start one."),
	}
	top := max(0, h/2-2)
	for i, s := range msg {
		if top+i < h {
			lines[top+i] = lipgloss.PlaceHorizontal(w, lipgloss.Center, s)
		}
	}
	return fill(lines, w, h)
}

// connectionLine says where the connection is, when it isn't simply up.
func (m *Model) connectionLine() (string, bool) {
	s := m.conn.State
	switch s.Kind {
	case client.Connecting:
		msg := s.Message
		if msg == "" {
			msg = "Connecting to rad…"
		}
		return m.spinner() + " " + msg, false
	case client.Failed:
		msg := s.Message
		if s.Code == "unreachable" {
			msg = "rad isn't running: start `rad serve` or the Server app."
		}
		return "⚠ " + msg + "  r retries", true
	case client.Idle:
		return "Not connected.", true
	}
	return "", false
}

func (m *Model) statusBar() string {
	left, isErr := m.connectionLine()
	if m.toast != "" {
		left, isErr = m.toast, m.toastErr
	}
	style := m.st.Faint
	if isErr {
		style = m.st.Red
	}
	hints := m.hints()
	var parts []string
	for _, h := range hints {
		parts = append(parts, m.st.Key.Render(h[0])+" "+m.st.Faint.Render(h[1]))
	}
	right := strings.Join(parts, m.st.Faint.Render(" · "))
	if left == "" {
		return pad(" "+right, m.width)
	}
	return spread(" "+style.Render(left), right+" ", m.width)
}

func (m *Model) hints() [][2]string {
	if o := m.topOverlay(); o != nil {
		if h, ok := o.(interface{ hints() [][2]string }); ok {
			return h.hints()
		}
		return [][2]string{{"esc", "close"}}
	}
	switch m.focus {
	case focusSidebar:
		return [][2]string{{"enter", "open"}, {"ctrl+n", "new"}, {"ctrl+k", "jump"}, {"ctrl+h", "help"}}
	case focusTranscript:
		return [][2]string{{"enter", "expand"}, {"esc", "list"}, {"ctrl+p", "actions"}, {"ctrl+h", "help"}}
	}
	if m.sess != nil {
		return m.sess.hints(m)
	}
	return nil
}

// Keys

func (m *Model) key(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	if key == "ctrl+q" {
		return tea.Quit
	}
	// Help opens from anywhere, the prompt and forms included, and closes.
	if key == "ctrl+h" || key == "f1" {
		if _, ok := m.topOverlay().(*help); ok {
			m.pop()
		} else {
			m.push(newHelp())
		}
		return nil
	}
	if o := m.topOverlay(); o != nil {
		return o.update(m, k)
	}
	// A key clears a selection; Esc only that.
	if m.sess != nil && m.sess.sel.shown() && !m.sess.sel.down {
		m.sess.sel = textSelection{}
		if key == "esc" {
			return nil
		}
	}
	switch key {
	case "ctrl+c":
		if m.sess != nil && m.focus == focusComposer && m.sess.composer.value() != "" {
			m.sess.composer.reset(m)
			return nil
		}
		if time.Since(m.quitAt) < 2*time.Second {
			return tea.Quit
		}
		m.quitAt = time.Now()
		m.say("Press ctrl+c again to quit.")
		return nil
	case "ctrl+n":
		return m.newSession("")
	case "ctrl+x":
		if m.sess != nil && m.sess.store.IsRunning() {
			return m.sess.interrupt(m, false)
		}
		return nil
	case "ctrl+k":
		m.push(newSwitcher(m))
		return nil
	case "ctrl+p":
		if m.sess != nil {
			m.push(m.sess.actions(m))
		} else {
			m.push(m.appMenu())
		}
		return nil
	case "ctrl+b":
		if m.layout().wide {
			if m.sess != nil {
				m.state.SidebarHidden = !m.state.SidebarHidden
				if m.state.SidebarHidden && m.focus == focusSidebar {
					m.setFocus(focusComposer)
				}
				m.saveState()
			}
		} else if m.sess != nil {
			m.closeSession()
		}
		return nil
	case "tab", "shift+tab":
		if m.focus == focusComposer && m.sess != nil && m.sess.composer.suggesting() && key == "tab" {
			break
		}
		m.cycleFocus(key == "shift+tab")
		return nil
	}
	if key == "r" && m.conn.State.Kind == client.Failed && m.focus != focusComposer {
		m.conn.Retry()
		return nil
	}
	if key == "?" && m.focus != focusComposer {
		m.push(newHelp())
		return nil
	}
	switch m.focus {
	case focusSidebar:
		return m.side.key(m, k)
	case focusTranscript, focusComposer:
		if m.sess != nil {
			return m.sess.key(m, k)
		}
		m.focus = focusSidebar
	}
	return nil
}

func (m *Model) cycleFocus(back bool) {
	l := m.layout()
	var order []focusArea
	if l.showSide {
		order = append(order, focusSidebar)
	}
	if m.sess != nil && l.showPane {
		order = append(order, focusTranscript, focusComposer)
	}
	if len(order) == 0 {
		return
	}
	i := 0
	for j, f := range order {
		if f == m.focus {
			i = j
		}
	}
	if back {
		i = (i - 1 + len(order)) % len(order)
	} else {
		i = (i + 1) % len(order)
	}
	m.setFocus(order[i])
}

// toList goes up from the session to the list: beside it in a wide window,
// showing the list again if it was hidden, or in its place in a narrow one.
func (m *Model) toList() {
	if !m.layout().wide {
		m.closeSession()
		return
	}
	if m.state.SidebarHidden {
		m.state.SidebarHidden = false
		m.saveState()
	}
	if m.sess != nil {
		m.side.cursor = "s:" + m.sess.store.ID
		m.side.inColls = false
		// An archived session folded away: its fold.
		if _, ok := m.side.current(m.side.rows(m)); !ok && m.sess.store.Session != nil && m.sess.store.Session.Archived {
			m.side.cursor = "f:archived"
		}
	}
	m.setFocus(focusSidebar)
}

func (m *Model) setFocus(f focusArea) {
	m.focus = f
	if m.sess != nil {
		m.sess.focusChanged(m)
	}
}

func (m *Model) paste(text string) tea.Cmd {
	if o := m.topOverlay(); o != nil {
		if p, ok := o.(interface{ paste(*Model, string) tea.Cmd }); ok {
			return p.paste(m, text)
		}
		return nil
	}
	if m.sess != nil {
		if m.focus != focusComposer {
			m.setFocus(focusComposer)
		}
		return m.sess.paste(m, text)
	}
	return nil
}

func (m *Model) wheel(w tea.MouseWheelMsg) {
	if len(m.overlays) > 0 {
		return
	}
	l := m.layout()
	mouse := w.Mouse()
	delta := 3
	if mouse.Button == tea.MouseWheelUp {
		delta = -3
	} else if mouse.Button != tea.MouseWheelDown {
		return
	}
	if l.showSide && mouse.X < l.sideW {
		m.side.scrollBy(delta)
		return
	}
	if m.sess != nil {
		// Over an approval, the wheel scrolls its plan.
		if mouse.Y >= l.bodyH-m.sess.bottomHeight && m.sess.dock.active() {
			m.sess.dock.scroll = max(0, m.sess.dock.scroll+delta)
			return
		}
		m.sess.scrollBy(delta)
	}
}

func (m *Model) click(c tea.MouseClickMsg) tea.Cmd {
	if len(m.overlays) > 0 || c.Mouse().Button != tea.MouseLeft {
		return nil
	}
	l := m.layout()
	mouse := c.Mouse()
	if l.showSide && mouse.X < l.sideW {
		m.setFocus(focusSidebar)
		return m.side.click(m, l, mouse.X, mouse.Y)
	}
	if m.sess != nil && mouse.Y < l.bodyH {
		if mouse.Y >= l.bodyH-m.sess.bottomHeight {
			m.setFocus(focusComposer)
		} else {
			m.setFocus(focusTranscript)
			m.sess.press(mouse.X-l.paneX(), mouse.Y)
		}
	}
	return nil
}

// motion drags a selection over the transcript.
func (m *Model) motion(e tea.MouseMotionMsg) {
	if m.sess == nil || !m.sess.sel.down || len(m.overlays) > 0 {
		return
	}
	mouse := e.Mouse()
	m.sess.drag(mouse.X-m.layout().paneX(), mouse.Y)
}

func (m *Model) release(e tea.MouseReleaseMsg) tea.Cmd {
	if m.sess == nil || !m.sess.sel.down {
		return nil
	}
	return m.sess.release(m)
}

// newSession opens the new session form, in a project if given.
func (m *Model) newSession(projectID string) tea.Cmd {
	if !m.conn.IsConnected() {
		m.fail(errors.New("Not connected to rad."))
		return nil
	}
	ns := newNewSession(m, projectID)
	m.push(ns)
	return ns.init(m)
}

// appMenu is the actions menu when no session is open.
func (m *Model) appMenu() overlay {
	items := []menuItem{
		{label: "New Session…", hint: "ctrl+n", run: func(m *Model) tea.Cmd { return m.newSession("") }},
		{label: "Server Settings…", run: func(m *Model) tea.Cmd { return m.openServerSettings() }},
		{label: "Settings…", run: func(m *Model) tea.Cmd { m.push(newSettingsView(m)); return nil }},
	}
	if m.conn.State.Kind == client.Failed {
		items = append(items, menuItem{label: "Reconnect", run: func(m *Model) tea.Cmd { m.conn.Retry(); return nil }})
	}
	return newMenu("ra", items)
}
