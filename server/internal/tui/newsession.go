package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"remote-agent/internal/client"
	"remote-agent/internal/model"
)

// newSession is the new session form, as NewSessionView: a project, an
// agent with its model, effort and permissions, a worktree, and the first
// prompt, or none, or a draft. It remembers what each agent was last
// started with.
type newSession struct {
	projectID string
	harness   string
	model     string // "": the agent's default
	effort    string
	mode      string
	worktree  bool
	branch    textinput.Model
	base      string
	branches  *client.Branches
	prompt    composer
	field     int
	busy      bool
	chose     bool // an agent was chosen here
	leaving   bool // closing, once its prompt is kept as a draft
}

const (
	fieldProject = iota
	fieldAgent
	fieldModel
	fieldEffort
	fieldMode
	fieldWorktree
	fieldBranch
	fieldBase
	fieldPrompt
	fieldStart
)

type branchesMsg struct {
	ns      *newSession
	project string
	b       client.Branches
	err     error
}

func newNewSession(m *Model, projectID string) *newSession {
	ns := &newSession{prompt: newComposer(m)}
	ns.prompt.ta.Placeholder = "First prompt (optional)"
	ns.prompt.ta.MaxHeight = 8
	ns.branch = textinput.New()
	ns.branch.Placeholder = "auto"
	ns.branch.Prompt = ""
	styleInput(&ns.branch, m)
	if projectID == "" {
		projectID = projectAt(m.conn.Projects, m.cwd)
	}
	if projectID == "" {
		projectID = m.state.LastProject
	}
	if m.conn.Projects[projectID] == nil {
		projectID = ""
		if ps := m.conn.SortedProjects(); len(ps) > 0 {
			projectID = ps[0].ID
		}
	}
	ns.projectID = projectID
	ns.setHarness(m, m.state.LastHarness, true)
	if ns.projectID == "" {
		ns.field = fieldProject
	} else {
		ns.field = fieldPrompt
	}
	if u := m.unsent; u != nil {
		ns.prompt.restore(m, u.text, u.images)
		m.unsent = nil
	}
	ns.focusField()
	return ns
}

func (ns *newSession) init(m *Model) tea.Cmd { return nil }

// projectAt is the project that holds dir, the innermost if they nest.
func projectAt(ps map[string]*model.Project, dir string) string {
	if dir == "" {
		return ""
	}
	id, depth := "", -1
	for _, p := range ps {
		path := filepath.Clean(p.Path)
		inside := dir == path || strings.HasPrefix(dir, strings.TrimSuffix(path, string(filepath.Separator))+string(filepath.Separator))
		if inside && len(path) > depth {
			id, depth = p.ID, len(path)
		}
	}
	return id
}

func usable(h *model.HarnessInfo) bool { return h != nil && h.Installed && h.AuthOK }

// setHarness chooses an agent, falling back to Claude Code, then to the
// first usable one, and restores what it was last started with.
func (ns *newSession) setHarness(m *Model, id string, fallback bool) {
	if !usable(m.conn.Harness(id)) && fallback {
		id = ""
		if usable(m.conn.Harness("claude")) {
			id = "claude"
		} else {
			for i := range m.conn.Harnesses {
				if usable(&m.conn.Harnesses[i]) {
					id = m.conn.Harnesses[i].ID
					break
				}
			}
		}
	}
	ns.harness = id
	h := m.conn.Harness(id)
	ns.model, ns.effort, ns.mode = "", "", ""
	if h == nil {
		return
	}
	if v := m.state.LastModel[id]; v != "" && (h.Caps.FreeModel || choiceIndex(h.Models, v) >= 0) {
		ns.model = v
	}
	if v := m.state.LastEffort[id]; v != "" && choiceIndex(ns.efforts(m), v) >= 0 {
		ns.effort = v
	}
	if v := m.state.LastMode[id]; v != "" && choiceIndex(h.Modes, v) >= 0 {
		ns.mode = v
	}
}

func choiceIndex(cs []model.Choice, id string) int {
	for i, c := range cs {
		if c.ID == id {
			return i
		}
	}
	return -1
}

func (ns *newSession) h(m *Model) *model.HarnessInfo { return m.conn.Harness(ns.harness) }

// ensureHarness chooses the agent once rad has listed them, which it may
// not have when the form opens.
func (ns *newSession) ensureHarness(m *Model) {
	if ns.harness == "" && !ns.chose && len(m.conn.Harnesses) > 0 {
		ns.setHarness(m, m.state.LastHarness, true)
	}
}

// efforts are the chosen model's, if it lists them, else the agent's.
func (ns *newSession) efforts(m *Model) []model.Choice {
	h := ns.h(m)
	if h == nil {
		return nil
	}
	if i := choiceIndex(h.Models, ns.model); i >= 0 && len(h.Models[i].Efforts) > 0 {
		return h.Models[i].Efforts
	}
	return h.Efforts
}

func (ns *newSession) project(m *Model) *model.Project { return m.conn.Projects[ns.projectID] }

// fields are the rows shown, by what the agent and the project offer.
func (ns *newSession) fields(m *Model) []int {
	f := []int{fieldProject, fieldAgent}
	if h := ns.h(m); h != nil {
		if len(h.Models) > 0 || h.Caps.FreeModel {
			f = append(f, fieldModel)
		}
		if len(ns.efforts(m)) > 0 {
			f = append(f, fieldEffort)
		}
		if len(h.Modes) > 0 {
			f = append(f, fieldMode)
		}
	}
	if p := ns.project(m); p != nil && p.IsGitRepo {
		f = append(f, fieldWorktree)
		if ns.worktree {
			f = append(f, fieldBranch, fieldBase)
		}
	}
	return append(f, fieldPrompt, fieldStart)
}

func (ns *newSession) focusField() {
	ns.branch.Blur()
	ns.prompt.blur()
	switch ns.field {
	case fieldBranch:
		ns.branch.Focus()
	case fieldPrompt:
		ns.prompt.focus()
	}
}

func (ns *newSession) move(m *Model, d int) {
	fields := ns.fields(m)
	i := 0
	for j, f := range fields {
		if f == ns.field {
			i = j
		}
	}
	i = max(0, min(len(fields)-1, i+d))
	ns.field = fields[i]
	ns.focusField()
}

func (ns *newSession) hints() [][2]string {
	h := [][2]string{{"↑↓ tab", "field"}, {"enter", "choose"}}
	if ns.field == fieldPrompt {
		h = [][2]string{{"enter", "start"}, {"shift+enter", "new line"}, {"ctrl+s", "save as draft"}}
	}
	if ns.written() {
		return append(h, [2]string{"esc", "close, keeping the prompt"})
	}
	return append(h, [2]string{"esc", "cancel"})
}

func (ns *newSession) animating() bool { return ns.busy || ns.prompt.uploading() }

func (ns *newSession) message(m *Model, msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case branchesMsg:
		if msg.ns == ns && msg.project == ns.projectID && msg.err == nil {
			b := msg.b
			ns.branches = &b
		}
		return nil
	case uploadMsg:
		return ns.prompt.message(m, msg)
	case createdMsg:
		if msg.ns == ns {
			return ns.created(m, msg)
		}
	}
	return nil
}

func (ns *newSession) paste(m *Model, text string) tea.Cmd {
	switch ns.field {
	case fieldBranch:
		ns.branch.SetValue(ns.branch.Value() + strings.TrimSpace(text))
		ns.branch.CursorEnd()
		return nil
	}
	ns.field = fieldPrompt
	ns.focusField()
	if paths := imagePaths(text); len(paths) > 0 {
		var cmds []tea.Cmd
		for _, p := range paths {
			cmds = append(cmds, ns.prompt.attach(m, p))
		}
		return tea.Batch(cmds...)
	}
	ns.prompt.ta.InsertString(text)
	return nil
}

func (ns *newSession) update(m *Model, k tea.KeyPressMsg) tea.Cmd {
	ns.ensureHarness(m)
	if ns.busy {
		return nil
	}
	key := k.String()
	switch key {
	case "esc":
		return ns.leave(m)
	case "tab":
		ns.move(m, 1)
		return nil
	case "shift+tab":
		ns.move(m, -1)
		return nil
	case "ctrl+s":
		return ns.start(m, true)
	}
	switch ns.field {
	case fieldPrompt:
		switch key {
		case "enter":
			return ns.start(m, false)
		case "shift+enter", "alt+enter", "ctrl+j":
			ns.prompt.ta.InsertString("\n")
			return nil
		case "up":
			if ns.prompt.ta.Line() == 0 {
				ns.move(m, -1)
				return nil
			}
		case "down":
			if ns.prompt.ta.Line() >= ns.prompt.ta.LineCount()-1 {
				ns.move(m, 1)
				return nil
			}
		case "backspace":
			if ns.prompt.value() == "" && len(ns.prompt.atts) > 0 {
				ns.prompt.atts = ns.prompt.atts[:len(ns.prompt.atts)-1]
				return nil
			}
		}
		var cmd tea.Cmd
		ns.prompt.ta, cmd = ns.prompt.ta.Update(k)
		return cmd
	case fieldBranch:
		switch key {
		case "up", "down", "enter":
			ns.move(m, map[bool]int{true: -1, false: 1}[key == "up"])
			return nil
		}
		var cmd tea.Cmd
		ns.branch, cmd = ns.branch.Update(k)
		return cmd
	}
	switch key {
	case "up":
		ns.move(m, -1)
	case "down":
		ns.move(m, 1)
	case "left", "right":
		ns.cycle(m, map[bool]int{true: -1, false: 1}[key == "left"])
	case "enter", "space":
		return ns.choose(m)
	}
	return nil
}

// cycle steps a field's value without opening its menu.
func (ns *newSession) cycle(m *Model, d int) {
	h := ns.h(m)
	step := func(cs []model.Choice, cur string, withDefault bool) string {
		ids := []string{}
		if withDefault {
			ids = append(ids, "")
		}
		for _, c := range cs {
			ids = append(ids, c.ID)
		}
		i := 0
		for j, id := range ids {
			if id == cur {
				i = j
			}
		}
		return ids[(i+d+len(ids))%len(ids)]
	}
	switch ns.field {
	case fieldProject:
		ps := m.conn.SortedProjects()
		if len(ps) == 0 {
			return
		}
		i := 0
		for j, p := range ps {
			if p.ID == ns.projectID {
				i = j
			}
		}
		ns.setProject(m, ps[(i+d+len(ps))%len(ps)].ID)
	case fieldAgent:
		var cs []model.Choice
		for _, hi := range m.conn.Harnesses {
			if usable(&hi) {
				cs = append(cs, model.Choice{ID: hi.ID})
			}
		}
		if len(cs) > 0 {
			ns.chose = true
			ns.setHarness(m, step(cs, ns.harness, false), false)
		}
	case fieldModel:
		if h != nil {
			ns.model = step(h.Models, ns.model, true)
			ns.fixEffort(m)
		}
	case fieldEffort:
		ns.effort = step(ns.efforts(m), ns.effort, true)
	case fieldMode:
		if h != nil {
			ns.mode = step(h.Modes, ns.mode, true)
		}
	case fieldWorktree:
		ns.toggleWorktree(m)
	}
}

func (ns *newSession) fixEffort(m *Model) {
	if ns.effort != "" && choiceIndex(ns.efforts(m), ns.effort) < 0 {
		ns.effort = ""
	}
}

func (ns *newSession) setProject(m *Model, id string) {
	ns.projectID = id
	ns.branches = nil
	ns.base = ""
	if p := ns.project(m); p == nil || !p.IsGitRepo {
		ns.worktree = false
	}
}

func (ns *newSession) toggleWorktree(m *Model) tea.Cmd {
	ns.worktree = !ns.worktree
	if !ns.worktree || ns.branches != nil {
		return nil
	}
	project := ns.projectID
	conn := m.conn
	return func() tea.Msg {
		b, err := conn.Branches(context.Background(), project)
		return branchesMsg{ns: ns, project: project, b: b, err: err}
	}
}

func (ns *newSession) choose(m *Model) tea.Cmd {
	h := ns.h(m)
	switch ns.field {
	case fieldProject:
		var items []menuItem
		for _, p := range m.conn.SortedProjects() {
			id := p.ID
			items = append(items, menuItem{label: p.Name, detail: p.Path, checked: id == ns.projectID, run: func(m *Model) tea.Cmd {
				ns.setProject(m, id)
				return nil
			}})
		}
		items = append(items, menuItem{label: "Add a Folder…", run: func(m *Model) tea.Cmd {
			fb := newFolderBrowser(m, ns)
			m.push(fb)
			return fb.load(m, "")
		}})
		mn := newMenu("Project", items)
		mn.width = 60
		m.push(mn)
	case fieldAgent:
		var items []menuItem
		for _, hi := range m.conn.Harnesses {
			id := hi.ID
			detail := hi.Version
			if !hi.Installed {
				detail = "Not installed"
			} else if !hi.AuthOK {
				detail = "Not signed in"
				if hi.Hint != "" {
					detail = hi.Hint
				}
			}
			items = append(items, menuItem{label: hi.Name, detail: detail, checked: id == ns.harness, disabled: !usable(&hi), run: func(m *Model) tea.Cmd {
				ns.chose = true
				ns.setHarness(m, id, false)
				return nil
			}})
		}
		if len(items) == 0 {
			items = append(items, menuItem{label: "No agents are installed on this computer.", disabled: true})
		}
		m.push(newMenu("Agent", items))
	case fieldModel:
		if h == nil {
			return nil
		}
		items := []menuItem{{label: "Default", checked: ns.model == "", run: func(m *Model) tea.Cmd { ns.model = ""; ns.fixEffort(m); return nil }}}
		for _, c := range h.Models {
			id := c.ID
			items = append(items, menuItem{label: c.Name, detail: c.Description, checked: id == ns.model, run: func(m *Model) tea.Cmd { ns.model = id; ns.fixEffort(m); return nil }})
		}
		if h.Caps.FreeModel {
			custom := ns.model != "" && choiceIndex(h.Models, ns.model) < 0
			items = append(items, menuItem{label: "Custom…", checked: custom, run: func(m *Model) tea.Cmd {
				m.push(newInput(m, "Model", "The model's id, as the agent takes it.", ns.model, func(m *Model, v string) tea.Cmd {
					ns.model = strings.TrimSpace(v)
					ns.fixEffort(m)
					return nil
				}))
				return nil
			}})
		}
		m.push(newMenu("Model", items))
	case fieldEffort:
		items := []menuItem{{label: "Default", checked: ns.effort == "", run: func(m *Model) tea.Cmd { ns.effort = ""; return nil }}}
		for _, c := range ns.efforts(m) {
			id := c.ID
			items = append(items, menuItem{label: c.Name, detail: c.Description, checked: id == ns.effort, run: func(m *Model) tea.Cmd { ns.effort = id; return nil }})
		}
		m.push(newMenu("Effort", items))
	case fieldMode:
		if h == nil {
			return nil
		}
		var items []menuItem
		for _, c := range h.Modes {
			id := c.ID
			checked := id == ns.mode || (ns.mode == "" && id == h.DefaultMode)
			items = append(items, menuItem{label: c.Name, detail: c.Description, checked: checked, run: func(m *Model) tea.Cmd { ns.mode = id; return nil }})
		}
		m.push(newMenu("Permissions", items))
	case fieldWorktree:
		return ns.toggleWorktree(m)
	case fieldBase:
		items := []menuItem{{label: "Current HEAD", checked: ns.base == "", run: func(m *Model) tea.Cmd { ns.base = ""; return nil }}}
		if ns.branches != nil {
			for _, b := range ns.branches.Branches {
				name := b
				label := b
				if b == ns.branches.Current {
					label += "  (current)"
				}
				items = append(items, menuItem{label: label, checked: name == ns.base, run: func(m *Model) tea.Cmd { ns.base = name; return nil }})
			}
		}
		m.push(newMenu("Based On", items))
	case fieldStart:
		return ns.start(m, false)
	}
	return nil
}

func (ns *newSession) written() bool {
	return strings.TrimSpace(ns.prompt.value()) != "" || len(ns.prompt.atts) > 0
}

// leave closes the form, keeping a prompt written in it: as a draft
// session, as Save as Draft does, or when rad can't keep that, for the
// next new session.
func (ns *newSession) leave(m *Model) tea.Cmd {
	if !ns.written() {
		m.pop()
		return nil
	}
	if m.conn.Info != nil && m.conn.Info.Drafts && ns.project(m) != nil && usable(ns.h(m)) {
		cmd := ns.start(m, true)
		ns.leaving = cmd != nil
		return cmd
	}
	m.unsent = &unsentPrompt{text: ns.prompt.value(), images: ns.prompt.uploaded()}
	m.pop()
	m.say("Kept the prompt for the next new session.")
	return nil
}

func (ns *newSession) start(m *Model, draft bool) tea.Cmd {
	if ns.project(m) == nil {
		m.fail(fmt.Errorf("Choose a project first."))
		return nil
	}
	if !usable(ns.h(m)) {
		m.fail(fmt.Errorf("Choose an agent that's installed and signed in."))
		return nil
	}
	refs, ok := ns.prompt.images(m)
	if !ok {
		return nil
	}
	prompt := strings.TrimSpace(ns.prompt.value())
	if draft {
		if m.conn.Info == nil || !m.conn.Info.Drafts {
			m.say("This rad doesn't keep drafts. Update rad.")
			return nil
		}
		if prompt == "" && len(refs) == 0 {
			m.say("Write the prompt to keep as a draft.")
			return nil
		}
	}
	p := client.NewSession{
		ProjectID: ns.projectID, Harness: ns.harness, Model: ns.model, Effort: ns.effort, Mode: ns.mode,
		Workspace: client.NewWorkspace{Kind: model.WorkspaceRoot}, Prompt: prompt, Images: refs, Draft: draft,
	}
	if ns.worktree {
		p.Workspace = client.NewWorkspace{Kind: model.WorkspaceWorktree, Branch: strings.TrimSpace(ns.branch.Value()), BaseRef: ns.base}
	}
	ns.busy = true
	conn := m.conn
	return func() tea.Msg {
		s, err := conn.CreateSession(context.Background(), p)
		return createdMsg{ns: ns, s: s, draft: draft, err: err}
	}
}

type createdMsg struct {
	ns    *newSession
	s     model.Session
	draft bool
	err   error
}

func (ns *newSession) created(m *Model, msg createdMsg) tea.Cmd {
	ns.busy = false
	leaving := ns.leaving
	ns.leaving = false
	if msg.err != nil {
		m.fail(msg.err)
		return nil
	}
	ns.remember(m)
	for m.topOverlay() != nil {
		m.pop()
	}
	if leaving {
		m.say("Saved “" + msg.s.Title + "” as a draft.")
		return nil
	}
	m.openSession(msg.s.ID)
	m.setFocus(focusComposer)
	if msg.draft {
		m.say("Saved as a draft.")
	}
	return nil
}

func (ns *newSession) remember(m *Model) {
	m.state.LastProject = ns.projectID
	m.state.LastHarness = ns.harness
	m.state.LastModel[ns.harness] = ns.model
	m.state.LastEffort[ns.harness] = ns.effort
	m.state.LastMode[ns.harness] = ns.mode
	m.saveState()
}

func (ns *newSession) close(m *Model) {
	ns.busy = false
}

func (ns *newSession) view(m *Model, width, height int) string {
	ns.ensureHarness(m)
	w := min(76, width-4)
	inner := w - 4
	h := ns.h(m)
	value := func(f int) string {
		switch f {
		case fieldProject:
			if p := ns.project(m); p != nil {
				return p.Name + m.st.Faint.Render("  "+p.Path)
			}
			return m.st.Faint.Render("Choose…")
		case fieldAgent:
			if h != nil {
				return h.Name + m.st.Faint.Render("  "+h.Version)
			}
			return m.st.Faint.Render("Choose…")
		case fieldModel:
			if ns.model == "" {
				return "Default"
			}
			if i := choiceIndex(h.Models, ns.model); i >= 0 {
				return h.Models[i].Name
			}
			return ns.model
		case fieldEffort:
			if i := choiceIndex(ns.efforts(m), ns.effort); i >= 0 {
				return ns.efforts(m)[i].Name
			}
			return "Default"
		case fieldMode:
			id := ns.mode
			if id == "" {
				id = h.DefaultMode
			}
			if i := choiceIndex(h.Modes, id); i >= 0 {
				return h.Modes[i].Name
			}
			return "Default"
		case fieldWorktree:
			if ns.worktree {
				return m.st.Accent.Render("◉") + " Isolated git worktree"
			}
			return m.st.Faint.Render("○") + " Isolated git worktree"
		case fieldBranch:
			ns.branch.SetWidth(inner - 16)
			return ns.branch.View()
		case fieldBase:
			if ns.base == "" {
				return "Current HEAD"
			}
			return ns.base
		}
		return ""
	}
	labels := map[int]string{fieldProject: "Project", fieldAgent: "Agent", fieldModel: "Model", fieldEffort: "Effort", fieldMode: "Permissions", fieldWorktree: "Workspace", fieldBranch: "Branch", fieldBase: "Based on"}
	var lines []string
	for _, f := range ns.fields(m) {
		switch f {
		case fieldPrompt:
			lines = append(lines, "")
			for _, l := range ns.prompt.view(m, nil, inner, ns.field == fieldPrompt) {
				lines = append(lines, l)
			}
			continue
		case fieldStart:
			lines = append(lines, "")
			start := m.st.Button.Render("Start")
			if ns.field == fieldStart {
				start = m.st.ButtonOn.Render("Start")
			}
			if ns.busy {
				start += " " + m.st.Accent.Render(m.spinner())
			}
			hint := ""
			if m.conn.Info != nil && m.conn.Info.Drafts {
				hint = m.st.Faint.Render("ctrl+s saves the prompt as a draft")
			}
			lines = append(lines, spread(start, hint, inner))
			continue
		}
		label := pad(labels[f], 13)
		marker := "  "
		if f == ns.field {
			label = m.st.AccentBold.Render(label)
			marker = m.st.Accent.Render("▌ ")
		} else {
			label = m.st.Subtext.Render(label)
		}
		lines = append(lines, truncate(marker+label+value(f), inner))
	}
	if ns.worktree && ns.field >= fieldWorktree && ns.field <= fieldBase {
		lines = append(lines, "", m.st.Faint.Render(truncate("The agent works on its own branch, in a folder of its own.", inner)))
	}
	return box(m, "New Session", lines, w)
}

// folderBrowser browses the folders under rad's roots for a project.
type folderBrowser struct {
	ns      *newSession
	listing *client.FSListing
	cursor  int
	loading bool
	err     string
}

type listingMsg struct {
	fb  *folderBrowser
	l   client.FSListing
	err error
}

func newFolderBrowser(m *Model, ns *newSession) *folderBrowser {
	return &folderBrowser{ns: ns}
}

func (fb *folderBrowser) animating() bool { return fb.loading }

func (fb *folderBrowser) load(m *Model, path string) tea.Cmd {
	fb.loading = true
	conn := m.conn
	return func() tea.Msg {
		l, err := conn.ListDirectory(context.Background(), path)
		return listingMsg{fb: fb, l: l, err: err}
	}
}

func (fb *folderBrowser) message(m *Model, msg tea.Msg) tea.Cmd {
	if lm, ok := msg.(listingMsg); ok && lm.fb == fb {
		fb.loading = false
		if lm.err != nil {
			fb.err = lm.err.Error()
			return nil
		}
		fb.err = ""
		fb.listing = &lm.l
		fb.cursor = 0
	}
	return nil
}

func (fb *folderBrowser) hints() [][2]string {
	return [][2]string{{"enter", "open"}, {"a", "add the project"}, {"←", "up"}, {"esc", "cancel"}}
}

func (fb *folderBrowser) update(m *Model, k tea.KeyPressMsg) tea.Cmd {
	if fb.loading {
		if k.String() == "esc" {
			m.pop()
		}
		return nil
	}
	var entries []client.FSEntry
	if fb.listing != nil {
		entries = fb.listing.Entries
	}
	switch k.String() {
	case "esc":
		m.pop()
	case "up":
		fb.cursor = max(0, fb.cursor-1)
	case "down":
		fb.cursor = min(len(entries)-1, fb.cursor+1)
	case "left", "backspace":
		if fb.listing != nil && fb.listing.Parent != "" {
			return fb.load(m, fb.listing.Parent)
		} else if fb.listing != nil && fb.listing.Path != "" {
			return fb.load(m, "")
		}
	case "enter", "right":
		if fb.cursor < len(entries) {
			return fb.load(m, entries[fb.cursor].Path)
		}
	case "a":
		// The folder chosen, or in an empty one, that one.
		if fb.cursor < len(entries) {
			return fb.use(m, entries[fb.cursor].Path)
		}
		if fb.listing != nil && fb.listing.Path != "" {
			return fb.use(m, fb.listing.Path)
		}
	}
	return nil
}

func (fb *folderBrowser) use(m *Model, path string) tea.Cmd {
	ns := fb.ns
	var p model.Project
	return m.do(func(ctx context.Context) (err error) {
		p, err = m.conn.AddProject(ctx, path)
		return err
	}, func(m *Model) tea.Cmd {
		if m.topOverlay() == fb {
			m.pop()
		}
		ns.setProject(m, p.ID)
		return nil
	})
}

func (fb *folderBrowser) view(m *Model, width, height int) string {
	w := min(70, width-4)
	inner := w - 4
	var lines []string
	where := "rad's roots"
	if fb.listing != nil && fb.listing.Path != "" {
		where = fb.listing.Path
	}
	lines = append(lines, m.st.Subtext.Render(truncate(where, inner)), "")
	switch {
	case fb.loading:
		lines = append(lines, m.st.Faint.Render(m.spinner()+" Loading…"))
	case fb.err != "":
		lines = append(lines, m.st.Red.Render(truncate(fb.err, inner)))
	case fb.listing != nil && len(fb.listing.Entries) == 0:
		lines = append(lines, m.st.Faint.Render("No folders here."))
	case fb.listing != nil:
		room := max(3, height-12)
		start := max(0, min(fb.cursor-room/2, len(fb.listing.Entries)-room))
		for i := start; i < len(fb.listing.Entries) && i < start+room; i++ {
			e := fb.listing.Entries[i]
			badge := ""
			if e.IsGitRepo {
				badge = m.st.Green.Render("git ")
			}
			line := spread("▸ "+e.Name, badge, inner)
			if i == fb.cursor {
				line = highlight(line, inner, m.pal.Surface0)
			}
			lines = append(lines, line)
		}
	}
	return box(m, "Add a Folder", lines, w)
}
