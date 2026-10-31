package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"remote-agent/internal/client"
	"remote-agent/internal/model"
)

// commitForm is Commit, as CommitView: what a commit of the session's work
// would take, its message, which the system agent writes, and how far to
// go (commit, push, open a pull request); then the steps of the task rad
// runs for it, as they go.
type commitForm struct {
	src     *model.Session
	loading bool
	gen     int // the look asked for last; an older one's answer is dropped
	err     string
	preview *client.CommitPreview
	text    composer
	scope   client.CommitScope
	field   int
	// starting: git.commit is on its way.
	starting bool
	tasks    *client.ServerTasks
	progress *client.ServerTaskProgress
}

const (
	commitFieldMessage = iota
	commitFieldScope
	commitFieldStart
)

// commitLookMsg is what opening the form found: the session's commit that
// runs, or else the preview.
type commitLookMsg struct {
	f       *commitForm
	gen     int
	running *model.Task
	preview client.CommitPreview
	err     error
}

type commitStartedMsg struct {
	f   *commitForm
	t   model.Task
	err error
}

// canCommit: rad commits, and the session is in a git repository and not
// archived.
func (v *sessionView) canCommit(m *Model) bool {
	s := v.store.Session
	if s == nil || s.Archived || m.conn.Info == nil || !m.conn.Info.Commit {
		return false
	}
	p := m.conn.Projects[s.ProjectID]
	return p != nil && p.IsGitRepo
}

// commit opens the commit form for the session.
func (v *sessionView) commit(m *Model) tea.Cmd {
	if !m.conn.IsConnected() {
		m.fail(errors.New("Not connected to rad."))
		return nil
	}
	if v.store.Session == nil {
		return nil
	}
	f := &commitForm{src: v.store.Session, text: newComposer(m), tasks: client.NewServerTasks(m.q, m.conn)}
	f.text.ta.Placeholder = "Commit message (empty: rad writes one)"
	f.text.ta.MaxHeight = 8
	// The form shows less of it on a short screen; it still takes all.
	f.text.ta.MaxContentHeight = 1000
	m.push(f)
	return f.look(m)
}

// look finds the session's commit that runs, if any, or else what a commit
// would take.
func (f *commitForm) look(m *Model) tea.Cmd {
	f.loading, f.err = true, ""
	f.gen++
	gen, id, conn := f.gen, f.src.ID, m.conn
	return func() tea.Msg {
		ctx := context.Background()
		if ts, err := conn.SessionTasks(ctx, id); err == nil {
			for i := len(ts) - 1; i >= 0; i-- {
				if t := ts[i]; t.Kind == model.TaskCommit && t.Status == model.TaskRunning {
					return commitLookMsg{f: f, gen: gen, running: &t}
				}
			}
		}
		p, err := conn.CommitPreview(ctx, id)
		return commitLookMsg{f: f, gen: gen, preview: p, err: err}
	}
}

func (f *commitForm) animating() bool {
	return f.loading || f.starting || f.progress != nil && f.progress.IsActive()
}

func (f *commitForm) close(m *Model) { f.tasks.Stop() }

// looked shows what the look found.
func (f *commitForm) looked(m *Model, msg commitLookMsg) {
	f.loading = false
	if msg.running != nil {
		f.follow(*msg.running)
		return
	}
	if msg.err != nil {
		f.err = commitError(msg.err)
		return
	}
	p := msg.preview
	// Looking again keeps a message the user wrote.
	if cur := strings.TrimSpace(f.text.value()); cur == "" || f.preview != nil && cur == strings.TrimSpace(f.preview.Message) {
		f.text.setValue(p.Message)
	}
	first := f.preview == nil
	f.preview = &p
	if first || f.scope.Unavailable(p) != "" {
		f.scope = client.DefaultCommitScope(m.state.LastCommit, p)
	}
	if first || !f.hasField(f.field) {
		f.field = f.fields()[0]
	}
	f.focusField()
}

func (f *commitForm) message(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case commitLookMsg:
		if msg.f == f && msg.gen == f.gen {
			f.looked(m, msg)
		}
	case commitStartedMsg:
		if msg.f != f {
			return nil
		}
		f.starting = false
		if msg.err != nil {
			m.fail(errors.New(commitError(msg.err)))
			return nil
		}
		f.follow(msg.t)
	}
	return nil
}

// commitError says what went wrong, as a sentence.
func commitError(err error) string {
	if client.ErrorCode(err) == "method_not_found" {
		return "This rad can't commit from here. Update it."
	}
	s := err.Error()
	if s == "" {
		return "It failed."
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (f *commitForm) follow(t model.Task) {
	f.progress = f.tasks.Follow(t)
	f.text.blur()
}

func (f *commitForm) files() bool { return f.preview != nil && len(f.preview.Files) > 0 }

func (f *commitForm) fields() []int {
	if f.files() {
		return []int{commitFieldMessage, commitFieldScope, commitFieldStart}
	}
	return []int{commitFieldScope, commitFieldStart}
}

func (f *commitForm) hasField(field int) bool {
	for _, x := range f.fields() {
		if x == field {
			return true
		}
	}
	return false
}

func (f *commitForm) focusField() {
	if f.field == commitFieldMessage && f.progress == nil {
		f.text.focus()
	} else {
		f.text.blur()
	}
}

func (f *commitForm) move(d int) {
	f.field = stepField(f.fields(), f.field, d)
	f.focusField()
}

func (f *commitForm) hints() [][2]string {
	switch {
	case f.progress != nil:
		var h [][2]string
		if f.progress.Task.URL != "" {
			h = append(h, [2]string{"o", "open pull request"}, [2]string{"y", "copy link"})
		}
		if f.progress.IsActive() {
			h = append(h, [2]string{"ctrl+x", "cancel"})
		}
		return append(h, [2]string{"esc", "close"})
	case f.preview == nil && f.err != "":
		return [][2]string{{"ctrl+r", "try again"}, {"esc", "close"}}
	case f.preview == nil:
		return [][2]string{{"esc", "close"}}
	}
	start := [2]string{"ctrl+s", strings.ToLower(f.scope.Action(f.files()))}
	switch f.field {
	case commitFieldMessage:
		return [][2]string{start, {"enter", "new line"}, {"tab", "next"}, {"esc", "cancel"}}
	case commitFieldScope:
		return [][2]string{start, {"←→", "choose"}, {"↑↓ tab", "field"}, {"esc", "cancel"}}
	}
	return [][2]string{start, {"↑↓ tab", "field"}, {"esc", "cancel"}}
}

func (f *commitForm) paste(m *Model, text string) tea.Cmd {
	if f.progress != nil || !f.files() {
		return nil
	}
	f.field = commitFieldMessage
	f.focusField()
	f.text.ta.InsertString(text)
	return nil
}

func (f *commitForm) update(m *Model, k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	if f.progress != nil {
		return f.progressKey(m, key)
	}
	switch key {
	case "esc":
		m.pop()
		return nil
	case "ctrl+r":
		if !f.starting {
			return f.look(m)
		}
		return nil
	}
	if f.preview == nil || f.starting {
		return nil
	}
	switch key {
	case "ctrl+s":
		return f.start(m)
	case "tab":
		f.move(1)
		return nil
	case "shift+tab":
		f.move(-1)
		return nil
	}
	switch f.field {
	case commitFieldMessage:
		switch key {
		case "enter", "shift+enter", "alt+enter", "ctrl+j":
			f.text.ta.InsertString("\n")
			return nil
		case "down":
			if f.text.ta.Line() >= f.text.ta.LineCount()-1 {
				f.move(1)
				return nil
			}
		}
		var cmd tea.Cmd
		f.text.ta, cmd = f.text.ta.Update(k)
		return cmd
	case commitFieldScope:
		switch key {
		case "left", "right":
			f.cycle(map[bool]int{true: -1, false: 1}[key == "left"])
		case "enter", "space":
			f.chooseScope(m)
		}
	case commitFieldStart:
		if key == "enter" || key == "space" {
			return f.start(m)
		}
	}
	switch key {
	case "up":
		f.move(-1)
	case "down":
		f.move(1)
	}
	return nil
}

// cycle steps to the next scope that can go ahead.
func (f *commitForm) cycle(d int) {
	n := len(client.CommitScopes)
	for i := 1; i < n; i++ {
		s := client.CommitScopes[(int(f.scope)+d*i+n*n)%n]
		if s.Unavailable(*f.preview) == "" {
			f.scope = s
			return
		}
	}
}

func (f *commitForm) chooseScope(m *Model) {
	var items []menuItem
	cursor := 0
	for i, s := range client.CommitScopes {
		why := s.Unavailable(*f.preview)
		if s == f.scope {
			cursor = i
		}
		items = append(items, menuItem{label: s.Choice(), detail: why, checked: s == f.scope, disabled: why != "",
			run: func(*Model) tea.Cmd { f.scope = s; return nil }})
	}
	mn := newMenu("Commit", items)
	if items[cursor].disabled {
		mn.cursor = -1
		mn.move(1)
	} else {
		mn.cursor = cursor
	}
	m.push(mn)
}

func (f *commitForm) start(m *Model) tea.Cmd {
	p := *f.preview
	if why := f.scope.Unavailable(p); why != "" {
		m.fail(errors.New(why))
		return nil
	}
	text := ""
	if f.files() {
		text = f.text.value()
	}
	m.state.LastCommit = f.scope.String()
	m.saveState()
	f.starting = true
	f.text.blur()
	params, conn := f.scope.Params(f.src.ID, text), m.conn
	return func() tea.Msg {
		t, err := conn.Commit(context.Background(), params)
		return commitStartedMsg{f: f, t: t, err: err}
	}
}

func (f *commitForm) progressKey(m *Model, key string) tea.Cmd {
	t := f.progress.Task
	switch key {
	case "esc":
		m.pop()
	case "enter":
		if !f.progress.IsActive() {
			m.pop()
		}
	case "ctrl+x":
		if f.progress.IsActive() {
			p := f.progress
			return m.do(func(ctx context.Context) error { return p.Cancel(ctx) }, nil)
		}
	case "o":
		if t.URL != "" {
			return openURL(m, t.URL)
		}
	case "y", "c":
		if t.URL != "" {
			m.say("Copied the link.")
			return tea.SetClipboard(t.URL)
		}
	}
	return nil
}

func (f *commitForm) view(m *Model, width, height int) string {
	w := min(80, width-4)
	inner := w - 4
	lines := []string{m.st.Subtext.Render(truncate("“"+f.src.Title+"”", inner))}
	switch {
	case f.progress != nil:
		lines = append(lines, f.progressLines(m, inner, height)...)
	case f.preview == nil && f.err != "":
		lines = append(lines, "")
		for _, l := range wrap(f.err, inner) {
			lines = append(lines, m.st.Red.Render(l))
		}
	case f.preview == nil:
		lines = append(lines, "", m.st.Accent.Render(m.spinner()+" Looking at the changes…"))
	default:
		lines = append(lines, f.formLines(m, inner, height)...)
	}
	return box(m, "Commit", lines, w)
}

// branchLine is where the commit goes: "⑂ ra/fix → origin".
func (f *commitForm) branchLine(m *Model, inner int) string {
	if f.progress != nil {
		return f.followedBranchLine(m, inner)
	}
	p := f.preview
	line := m.st.Text.Render("⑂ " + p.Branch)
	if p.Branch == "" {
		line = m.st.Yellow.Render("Detached HEAD")
	}
	if p.Remote != "" {
		line += m.st.Subtext.Render(" → " + p.Remote)
	} else {
		line += m.st.Faint.Render(" · no remote to push to")
	}
	return truncate(line, inner)
}

// followedBranchLine is where the commit it follows goes: the remote is
// the one its push step names, so that the line reads the same whether the
// commit started here, after a preview, or the form opened onto it running,
// without one.
func (f *commitForm) followedBranchLine(m *Model, inner int) string {
	branch := f.src.Workspace.Branch
	if f.preview != nil {
		branch = f.preview.Branch
	}
	var line string
	switch {
	case branch != "":
		line = m.st.Text.Render("⑂ " + branch)
	case f.preview != nil:
		line = m.st.Yellow.Render("Detached HEAD")
	default:
		return ""
	}
	if remote := pushRemote(f.progress.Task); remote != "" {
		line += m.st.Subtext.Render(" → " + remote)
	}
	return truncate(line, inner)
}

// pushRemote is where a commit task pushes: rad names the remote in its
// push step's title, "Push to origin". "" when it doesn't push, or has no
// remote to.
func pushRemote(t model.Task) string {
	for _, s := range t.Steps {
		if remote, ok := strings.CutPrefix(s.Title, "Push to "); s.ID == "push" && ok {
			return remote
		}
	}
	return ""
}

func (f *commitForm) formLines(m *Model, inner, height int) []string {
	p := f.preview
	lines := []string{f.branchLine(m, inner)}
	if f.scope == client.CommitPushAndPR {
		into := "the default branch"
		if p.Base != "" {
			into = p.Base
		}
		lines = append(lines, m.st.Subtext.Render(truncate("Pull request into "+into, inner)))
	}
	lines = append(lines, "")
	tail := []string{"", labelRow(m, "Then", m.st.Text.Render(f.scope.Choice()), f.field == commitFieldScope, inner)}
	if why := f.scope.Unavailable(*p); why != "" {
		tail = append(tail, "  "+m.st.Yellow.Render(truncate(why, inner-2)))
	}
	tail = append(tail, "", formButton(m, f.scope.Action(len(p.Files) > 0), f.field == commitFieldStart, f.starting))
	if len(p.Files) == 0 {
		lines = append(lines, m.st.Text.Render("Nothing to commit."))
		if p.Ahead > 0 {
			lines = append(lines, m.st.Subtext.Render(plural(p.Ahead, "commit", "commits")+" to push."))
		}
		return append(lines, tail...)
	}
	// The choice and the button show however short the screen: the files
	// give way first, then the message. Its box, the session's title and
	// the hints take 5 lines, and the headers 3.
	free := height - 5 - len(lines) - len(tail) - 3
	f.text.ta.MaxHeight = max(1, min(8, free-2)) // and its rule, and a file
	message := f.text.view(m, nil, inner, f.field == commitFieldMessage)
	room := min(8, free-len(message))
	lines = append(lines, m.st.Header.Render("CHANGES")+m.st.Faint.Render("  "+plural(len(p.Files), "file", "files")))
	shown := len(p.Files)
	if shown > room {
		shown = max(0, room-1) // and a line for the rest
	}
	for _, file := range p.Files[:shown] {
		lines = append(lines, fileLine(m, file, inner))
	}
	// Without room for any, the header has the count.
	if shown > 0 && shown < len(p.Files) {
		lines = append(lines, m.st.Faint.Render(fmt.Sprintf("  …and %d more", len(p.Files)-shown)))
	}
	lines = append(lines, "", m.st.Header.Render("MESSAGE"))
	lines = append(lines, message...)
	return append(lines, tail...)
}

// fileLine is a changed file: its status's letter, its path, and its
// lines added and removed.
func fileLine(m *Model, file client.FileStat, inner int) string {
	letter, st := "M", m.st.Yellow
	switch file.Status {
	case "added":
		letter, st = "A", m.st.Green
	case "deleted":
		letter, st = "D", m.st.Red
	case "renamed":
		letter, st = "R", m.st.Blue
	}
	path := file.Path
	if file.OldPath != "" {
		path = file.OldPath + " → " + file.Path
	}
	stat := m.st.Add.Render(fmt.Sprintf("+%d", file.Additions)) + " " + m.st.Del.Render(fmt.Sprintf("−%d", file.Deletions))
	if file.Binary {
		stat = m.st.Faint.Render("binary")
	}
	return spread("  "+st.Render(letter)+" "+m.st.Text.Render(path), stat, inner)
}

func (f *commitForm) progressLines(m *Model, inner, height int) []string {
	t := f.progress.Task
	var lines []string
	if b := f.branchLine(m, inner); b != "" {
		lines = append(lines, b)
	}
	lines = append(lines, "")
	for _, s := range t.Steps {
		icon, detail := m.st.Faint.Render("○"), m.st.Subtext
		switch s.Status {
		case model.StepRunning:
			icon = m.st.Accent.Render(m.spinner())
		case model.StepSucceeded:
			icon = m.st.Green.Render("✓")
		case model.StepSkipped:
			icon = m.st.Faint.Render("–")
		case model.StepFailed:
			icon, detail = m.st.Red.Render("✗"), m.st.Red
		}
		lines = append(lines, icon+" "+m.st.Text.Render(truncate(s.Title, inner-2)))
		if s.Detail != "" {
			for _, l := range wrap(s.Detail, inner-4) {
				lines = append(lines, "    "+detail.Render(l))
			}
		}
	}
	if len(t.Steps) > 0 {
		lines = append(lines, "")
	}
	switch f.progress.Phase.Kind {
	case client.TaskPhaseRunning, client.TaskPhaseRestarting:
		lines = append(lines, m.st.Accent.Render(m.spinner()+" Working…"))
	case client.TaskPhaseDone:
		lines = append(lines, m.st.Green.Render("✓ Done."))
	case client.TaskPhaseFailed:
		for _, l := range wrap("✗ Failed: "+f.progress.Phase.Message, inner) {
			lines = append(lines, m.st.Red.Render(l))
		}
	case client.TaskPhaseCancelled:
		lines = append(lines, m.st.Faint.Render("Cancelled."))
	}
	if t.URL != "" {
		lines = append(lines, "", m.st.Text.Render("Pull request:"))
		lines = append(lines, "\x1b]8;;"+t.URL+"\x1b\\"+m.st.Blue.Render(truncate(t.URL, inner))+"\x1b]8;;\x1b\\")
	}
	if out := strings.TrimRight(t.Output, "\n"); out != "" {
		outLines := strings.Split(out, "\n")
		room := max(3, height-len(lines)-8)
		if len(outLines) > room {
			outLines = outLines[len(outLines)-room:]
		}
		lines = append(lines, "", m.st.Header.Render("OUTPUT"))
		for _, l := range outLines {
			lines = append(lines, m.st.Faint.Render(truncate(l, inner)))
		}
	}
	return lines
}
