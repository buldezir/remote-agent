package tui

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"remote-agent/internal/client"
	"remote-agent/internal/model"
)

// sessionView is the open session: its header, transcript, and the
// composer or, while the agent waits for an answer, the approval dock.
type sessionView struct {
	store    *client.SessionStore
	composer composer
	dock     dock

	expanded map[string]int // item id → shut, open or full
	cursor   string         // the selected block's key
	scroll   int            // the transcript's top line
	pinned   bool           // follows the end
	cache    map[string]cachedLines
	blocks   []tblock // as last drawn
	total    int
	avail    int // transcript lines last drawn

	bottomHeight int // the dock's or composer's lines, as last drawn
	headerHeight int

	commands      []model.Command
	commandsState int // 0 not asked, 1 asking, 2 known, 3 failed
}

type cachedLines struct {
	key   string
	lines []string
}

// tblock is one block of the transcript: an item, a turn's end, or the
// foot (the outbox, the draft, what runs).
type tblock struct {
	key        string
	item       *model.Item
	lines      []string
	selectable bool
	at         int // its first line
}

func newSessionView(m *Model, store *client.SessionStore) *sessionView {
	v := &sessionView{
		store:    store,
		expanded: map[string]int{},
		cache:    map[string]cachedLines{},
		pinned:   true,
	}
	v.composer = newComposer(m)
	v.composer.setValue(store.Draft())
	return v
}

func (v *sessionView) restyle(m *Model) {
	v.cache = map[string]cachedLines{}
	v.composer.restyle(m)
}

// leave keeps the prompt being written, as the session's view closes.
func (v *sessionView) leave(m *Model) {
	v.store.SetDraft(v.composer.value())
}

func (v *sessionView) expansion(id string) int { return v.expanded[id] }

func (v *sessionView) uploading() bool { return v.composer.uploading() }

func (v *sessionView) archived() bool {
	return v.store.Session != nil && v.store.Session.Archived
}

// sync keeps the dock, the composer and the command list in step with the
// session.
func (v *sessionView) sync(m *Model) tea.Cmd {
	pending := v.store.PendingApprovals()
	if v.archived() {
		pending = nil
	}
	v.dock.sync(m, pending)
	// The text area takes keys only while focused; it follows ra's focus,
	// however that changed.
	if m.focus == focusComposer && !v.dock.active() && len(m.overlays) == 0 {
		v.composer.focus()
	} else {
		v.composer.blur()
	}
	return v.composer.sync(m, v)
}

func (v *sessionView) message(m *Model, msg tea.Msg) tea.Cmd {
	if r, ok := msg.(respondedMsg); ok {
		if r.d == &v.dock {
			v.dock.busy = false
			m.fail(r.err)
		}
		return nil
	}
	return v.composer.message(m, msg)
}

func (v *sessionView) focusChanged(m *Model) {
	if m.focus == focusComposer && !v.dock.active() {
		v.composer.focus()
	} else {
		v.composer.blur()
	}
}

// Drawing

func (v *sessionView) view(m *Model, w, h int) string {
	header := v.header(m, w)
	v.headerHeight = len(header)
	var bottom []string
	if v.dock.active() {
		bottom = v.dock.view(m, v, w, max(6, h/2))
	} else {
		bottom = v.composer.view(m, v, w, m.focus == focusComposer)
	}
	v.bottomHeight = len(bottom)
	avail := max(1, h-len(header)-len(bottom))
	lines := v.transcript(m, w, avail)
	all := append(append(header, lines...), bottom...)
	return fill(all, w, h)
}

func (v *sessionView) header(m *Model, w int) []string {
	s := v.store.Session
	if s == nil {
		s = m.conn.Sessions[v.store.ID]
	}
	if s == nil {
		return []string{m.st.Faint.Render(" " + m.spinner() + " Loading…"), m.st.Rule.Render(strings.Repeat("─", w))}
	}
	title := s.Title
	if title == "" {
		title = "New session"
	}
	if s.Archived {
		title += m.st.Faint.Render("  archived")
	}
	line1 := spread(" "+m.st.Bold.Render(title), m.contextGauge(s)+" ", w)
	var parts []string
	parts = append(parts, m.harnessName(s))
	if name := m.modelName(s); name != "" {
		parts = append(parts, name)
	}
	effort := s.Effort
	if s.ModelInfo != nil && s.ModelInfo.Effort != "" {
		effort = s.ModelInfo.Effort
	}
	if effort != "" {
		parts = append(parts, effort)
	}
	if mode := m.modeName(s); mode != "" {
		parts = append(parts, mode)
	}
	place := ""
	if p := m.conn.Projects[s.ProjectID]; p != nil {
		place = p.Name
	}
	if s.Workspace.Branch != "" {
		place += "  ⑂ " + s.Workspace.Branch
	}
	if s.Workspace.Kind == model.WorkspaceWorktree {
		place += "  worktree"
	}
	line2 := spread(" "+m.st.Faint.Render(strings.Join(parts, " · ")), m.st.Faint.Render(place)+" ", w)
	return []string{line1, line2, m.st.Rule.Render(strings.Repeat("─", w))}
}

// transcript draws the blocks that fit in height, scrolled to the end
// while pinned.
func (v *sessionView) transcript(m *Model, w, height int) []string {
	content := max(10, w-3)
	v.blocks = v.build(m, content)
	var lines []string
	selected := m.focus == focusTranscript && len(m.overlays) == 0
	for i := range v.blocks {
		b := &v.blocks[i]
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		b.at = len(lines)
		gutter := "  "
		if selected && b.key == v.cursor {
			gutter = m.st.Accent.Render("▌ ")
		}
		for _, l := range b.lines {
			lines = append(lines, gutter+l)
		}
	}
	v.total, v.avail = len(lines), height
	maxScroll := max(0, len(lines)-height)
	if v.pinned {
		v.scroll = maxScroll
	}
	v.scroll = max(0, min(v.scroll, maxScroll))
	if v.scroll >= maxScroll {
		v.pinned = true
	}
	end := min(len(lines), v.scroll+height)
	out := lines[v.scroll:end]
	if v.scroll > 0 && len(out) > 0 {
		out[0] = spread(out[0], m.st.Faint.Render("↑ more "), w)
	}
	// The composer stays at the foot of the pane.
	for len(out) < height {
		out = append(out, "")
	}
	return out
}

// build lays out the transcript's blocks, from the cache where nothing
// changed.
func (v *sessionView) build(m *Model, width int) []tblock {
	var blocks []tblock
	turns := v.store.Turns
	lastOfTurn := map[string]int{}
	for i, it := range v.store.Transcript {
		if it.TurnID != "" {
			lastOfTurn[it.TurnID] = i
		}
	}
	for i, it := range v.store.Transcript {
		blocks = append(blocks, tblock{key: it.ID, item: it, lines: v.itemLines(m, it, width), selectable: true})
		if t := turns[it.TurnID]; t != nil && lastOfTurn[it.TurnID] == i {
			switch t.Status {
			case model.TurnInterrupted:
				blocks = append(blocks, tblock{key: "t:" + t.ID, lines: []string{m.st.Faint.Render("— Interrupted")}})
			case model.TurnFailed:
				msg := "— Failed"
				if t.Error != "" {
					msg += ": " + t.Error
				}
				blocks = append(blocks, tblock{key: "t:" + t.ID, lines: wrapStyled(m.st.Red, msg, width)})
			}
		}
	}
	return append(blocks, v.foot(m, width)...)
}

func wrapStyled(st interface{ Render(...string) string }, text string, width int) []string {
	lines := wrap(text, width)
	for i, l := range lines {
		lines[i] = st.Render(l)
	}
	return lines
}

func (v *sessionView) itemLines(m *Model, it *model.Item, width int) []string {
	key := v.itemKey(m, it, width)
	if c, ok := v.cache[it.ID]; ok && c.key == key {
		return c.lines
	}
	lines := m.renderItem(v, it, width)
	v.cache[it.ID] = cachedLines{key, lines}
	return lines
}

// itemKey changes whenever the item's drawing would: the item, its
// children, its expansion, the width, the theme, the images loaded, and a
// spinner's frame.
func (v *sessionView) itemKey(m *Model, it *model.Item, width int) string {
	h := fnv.New64a()
	fmt.Fprintf(h, "%d|%s|%d|%d|%d|%v|%d", it.UpdatedAt.UnixNano(), it.Status, len(it.Text), width, v.expanded[it.ID], m.pal.Dark, len(it.Images))
	var walk func(*model.Item)
	walk = func(p *model.Item) {
		for _, c := range v.store.ChildrenOf(p) {
			fmt.Fprintf(h, "|%s%d%s%d", c.ID, c.UpdatedAt.UnixNano(), c.Status, v.expanded[c.ID])
			walk(c)
		}
	}
	walk(it)
	if len(it.Images) > 0 || strings.Contains(it.Text, "rad-image:") || it.Kind == model.ItemToolCall {
		fmt.Fprintf(h, "|i%d", m.images.version)
	}
	if it.Status == model.ItemInProgress || it.Status == model.ItemQueued {
		fmt.Fprintf(h, "|f%d", m.frame)
	}
	return fmt.Sprint(h.Sum64())
}

// foot is what follows the transcript: prompts on their way, the draft,
// and what runs.
func (v *sessionView) foot(m *Model, width int) []tblock {
	var blocks []tblock
	if !v.store.Synced {
		return []tblock{{key: "f:loading", lines: []string{m.st.Faint.Render(m.spinner() + " Loading…")}}}
	}
	for _, o := range v.store.Outbox {
		lines := m.renderPrompt(o.Text, o.Images, width, "")
		lines = append(lines, m.st.Faint.Render("  Sending…"))
		blocks = append(blocks, tblock{key: "o:" + o.CommandID, lines: lines})
	}
	if d := v.store.UnsentDraft(); d != nil {
		lines := []string{m.st.Accent.Render("✎ Draft") + m.st.Faint.Render(" · not sent")}
		lines = append(lines, m.renderPrompt(d.Text, d.Images, width, "")...)
		lines = append(lines, m.st.Key.Render("  s")+m.st.Faint.Render(" sends it · ")+m.st.Key.Render("e")+m.st.Faint.Render(" edits it"))
		blocks = append(blocks, tblock{key: "d:draft", lines: lines, selectable: true})
	}
	if len(v.store.Transcript) == 0 && len(v.store.Outbox) == 0 && v.store.UnsentDraft() == nil {
		blocks = append(blocks, tblock{key: "f:empty", lines: []string{m.st.Faint.Render("No prompts yet. Write the first one below.")}})
	}
	s := v.store.Session
	if s == nil {
		return blocks
	}
	if s.Status == model.SessionRunning {
		blocks = append(blocks, tblock{key: "f:working", lines: []string{m.st.Accent.Render(m.spinner()) + m.st.Faint.Render(" Working…  esc stops")}})
	}
	if len(s.BackgroundTasks) > 0 {
		lines := []string{m.st.Header.Render("Running in the background")}
		for _, t := range s.BackgroundTasks {
			glyph := "◷"
			switch t.Kind {
			case model.BackgroundAgent:
				glyph = "◎"
			case model.BackgroundCommand:
				glyph = "❯"
			}
			for i, l := range wrap(t.Description, width-2) {
				lead := "  "
				if i == 0 {
					lead = glyph + " "
				}
				lines = append(lines, m.st.Blue.Render(lead)+m.st.Subtext.Render(l))
				if i == 1 {
					break
				}
			}
		}
		blocks = append(blocks, tblock{key: "f:background", lines: lines})
	}
	if s.Status == model.SessionError && s.Error != "" {
		blocks = append(blocks, tblock{key: "f:error", lines: wrapStyled(m.st.Red, "✗ "+s.Error, width)})
	}
	return blocks
}

// Keys

func (v *sessionView) hints(m *Model) [][2]string {
	if v.dock.active() {
		return v.dock.hints()
	}
	if v.composer.suggesting() {
		return [][2]string{{"↑↓", "choose"}, {"tab", "complete"}, {"esc", "dismiss"}}
	}
	if v.archived() {
		return [][2]string{{"↑", "messages"}, {"ctrl+p", "actions"}}
	}
	h := [][2]string{{"enter", "send"}, {"shift+enter", "new line"}}
	if v.store.IsRunning() {
		h = append(h, [2]string{"esc", "stop"})
	}
	return append(h, [2]string{"ctrl+p", "actions"})
}

func (v *sessionView) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	if m.focus == focusComposer {
		if v.dock.active() {
			return v.dock.key(m, v, k)
		}
		return v.composer.key(m, v, k)
	}
	return v.transcriptKey(m, k)
}

func (v *sessionView) selectable() []tblock {
	var out []tblock
	for _, b := range v.blocks {
		if b.selectable {
			out = append(out, b)
		}
	}
	return out
}

func (v *sessionView) current() (tblock, bool) {
	for _, b := range v.blocks {
		if b.key == v.cursor {
			return b, true
		}
	}
	return tblock{}, false
}

// moveCursor moves the selection between items, scrolling to show it.
func (v *sessionView) moveCursor(d int) {
	sel := v.selectable()
	if len(sel) == 0 {
		return
	}
	idx := -1
	for i, b := range sel {
		if b.key == v.cursor {
			idx = i
		}
	}
	if idx < 0 {
		// From the end or from what's in view.
		idx = len(sel)
		if d > 0 {
			idx = -1
		}
		for i, b := range sel {
			if b.at >= v.scroll && d > 0 {
				idx = i - 1
				break
			}
			if b.at < v.scroll+v.avail && d < 0 {
				idx = i + 1
			}
		}
	}
	idx = max(0, min(len(sel)-1, idx+d))
	v.cursor = sel[idx].key
	v.reveal(sel[idx])
}

func (v *sessionView) reveal(b tblock) {
	h := len(b.lines)
	if b.at < v.scroll {
		v.scroll = b.at
		v.pinned = false
	} else if b.at+h > v.scroll+v.avail {
		v.scroll = b.at + min(h, v.avail) - v.avail
		v.pinned = false
	}
	if v.scroll >= v.total-v.avail {
		v.pinned = true
	}
}

func (v *sessionView) scrollBy(d int) {
	v.scroll = max(0, v.scroll+d)
	v.pinned = v.scroll >= v.total-v.avail
}

func (v *sessionView) clickTranscript(m *Model, y int) {
	line := y - v.headerHeight + v.scroll
	for _, b := range v.blocks {
		if b.selectable && line >= b.at && line < b.at+len(b.lines) {
			if v.cursor == b.key {
				v.toggle(b)
			}
			v.cursor = b.key
			return
		}
	}
}

func (v *sessionView) toggle(b tblock) {
	if b.item == nil {
		return
	}
	switch b.item.Kind {
	case model.ItemToolCall, model.ItemReasoning, model.ItemApproval:
		v.expanded[b.item.ID] = (v.expanded[b.item.ID] + 1) % 3
		if b.item.Kind != model.ItemToolCall && v.expanded[b.item.ID] == full {
			v.expanded[b.item.ID] = shut
		}
	}
}

func (v *sessionView) transcriptKey(m *Model, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "up", "k":
		v.moveCursor(-1)
	case "down", "j":
		v.moveCursor(1)
	case "pgup", "ctrl+u":
		v.scrollBy(-max(1, v.avail-2))
	case "pgdown", "ctrl+d":
		v.scrollBy(max(1, v.avail-2))
	case "home", "g":
		v.scroll, v.pinned = 0, false
		if sel := v.selectable(); len(sel) > 0 {
			v.cursor = sel[0].key
		}
	case "end", "G":
		v.pinned = true
		if sel := v.selectable(); len(sel) > 0 {
			v.cursor = sel[len(sel)-1].key
		}
	case "esc":
		if v.store.IsRunning() {
			return v.interrupt(m, false)
		}
		m.setFocus(focusComposer)
	case "enter", "space":
		if b, ok := v.current(); ok {
			v.toggle(b)
		}
	case "y":
		if b, ok := v.current(); ok && b.item != nil {
			m.say("Copied.")
			return tea.SetClipboard(itemText(b.item))
		}
	case "o":
		if b, ok := v.current(); ok {
			return v.openImages(m, b)
		}
	case "s":
		b, ok := v.current()
		if !ok {
			return nil
		}
		if b.key == "d:draft" {
			return v.sendDraft(m)
		}
		if b.item != nil && b.item.Kind == model.ItemUserMessage && b.item.Status == model.ItemQueued {
			id := b.item.ID
			return m.do(func(ctx context.Context) error { return v.store.SendNow(ctx, id) }, nil)
		}
	case "e":
		if b, ok := v.current(); ok && b.key == "d:draft" {
			return v.editDraft(m)
		}
	default:
		// Typing goes to the prompt.
		if k.Text != "" && !v.dock.active() && !v.archived() {
			m.setFocus(focusComposer)
			return v.composer.key(m, v, k)
		}
	}
	return nil
}

func (v *sessionView) openImages(m *Model, b tblock) tea.Cmd {
	var refs []model.ImageRef
	switch {
	case b.item != nil:
		refs = b.item.Images
	case b.key == "d:draft":
		if d := v.store.UnsentDraft(); d != nil {
			refs = d.Images
		}
	}
	if len(refs) == 0 {
		m.say("This item has no image.")
		return nil
	}
	return m.openImages(refs)
}

func (v *sessionView) interrupt(m *Model, force bool) tea.Cmd {
	m.say("Stopping…")
	return m.do(func(ctx context.Context) error { return v.store.Interrupt(ctx, force) }, nil)
}

func (v *sessionView) sendDraft(m *Model) tea.Cmd {
	job, err := v.store.SendDraft()
	if err != nil {
		m.fail(err)
		return nil
	}
	v.pinned = true
	return m.do(job, nil)
}

// editDraft takes the draft off rad and into the composer.
func (v *sessionView) editDraft(m *Model) tea.Cmd {
	d := v.store.UnsentDraft()
	if d == nil {
		return nil
	}
	text, images := d.Text, d.Images
	id := v.store.ID
	return m.do(func(ctx context.Context) error {
		_, err := m.conn.SaveDraft(ctx, id, "", nil)
		return err
	}, func(m *Model) tea.Cmd {
		if m.sess == v {
			v.composer.restore(m, text, images)
			m.setFocus(focusComposer)
		}
		return nil
	})
}

func (v *sessionView) paste(m *Model, text string) tea.Cmd {
	if v.dock.active() {
		return v.dock.paste(text)
	}
	return v.composer.paste(m, v, text)
}

// The actions menu (⋯).

func (v *sessionView) actions(m *Model) overlay {
	s := v.store.Session
	if s == nil {
		return m.appMenu()
	}
	var items []menuItem
	h := m.conn.Harness(s.Harness)
	if h != nil && len(h.Modes) > 0 && h.Caps.SetMode && !s.Archived {
		items = append(items, menuItem{label: "Permissions", header: true})
		for _, mode := range h.Modes {
			id := mode.ID
			items = append(items, menuItem{label: mode.Name, detail: mode.Description, checked: s.Mode == id || (s.Mode == "" && id == h.DefaultMode),
				run: func(m *Model) tea.Cmd {
					return m.do(func(ctx context.Context) error { return v.store.SetMode(ctx, id) }, nil)
				}})
		}
	}
	if h != nil && len(h.Commands) > 0 && !s.Archived {
		items = append(items, menuItem{label: "Commands", header: true})
		for _, c := range h.Commands {
			name := c.Name
			items = append(items, menuItem{label: "/" + name, detail: c.Description, run: func(m *Model) tea.Cmd {
				return v.composer.send(m, v, "/"+name, nil)
			}})
		}
	}
	project := "Project"
	path := s.Workspace.Path
	if s.Workspace.AgentPath != "" {
		path = s.Workspace.AgentPath
	}
	if p := m.conn.Projects[s.ProjectID]; p != nil {
		project = p.Name
	}
	items = append(items, menuItem{label: project, header: true})
	if s.Workspace.Branch != "" {
		items = append(items, menuItem{label: "⑂ " + s.Workspace.Branch, disabled: true})
	} else if m.conn.Projects[s.ProjectID] != nil && m.conn.Projects[s.ProjectID].IsGitRepo {
		items = append(items, menuItem{label: "Detached HEAD", disabled: true})
	}
	if s.Workspace.Kind == model.WorkspaceWorktree {
		items = append(items, menuItem{label: "Worktree", disabled: true})
	}
	items = append(items, menuItem{label: "Copy Path", detail: path, run: func(m *Model) tea.Cmd {
		m.say("Copied " + path)
		return tea.SetClipboard(path)
	}})
	items = append(items, menuItem{label: "Session", header: true})
	if !s.Archived {
		items = append(items, menuItem{label: "Attach Image…", run: func(m *Model) tea.Cmd {
			p := newInput(m, "Attach Image", "The path to a PNG, JPEG, GIF or WebP file.", "", func(m *Model, path string) tea.Cmd {
				if m.sess != v || strings.TrimSpace(path) == "" {
					return nil
				}
				m.setFocus(focusComposer)
				return v.composer.attach(m, expandHome(strings.TrimSpace(path)))
			})
			p.paths = true
			m.push(p)
			return nil
		}})
	}
	if v.store.IsRunning() {
		items = append(items, menuItem{label: "Stop", hint: "esc", run: func(m *Model) tea.Cmd { return v.interrupt(m, false) }})
		items = append(items, menuItem{label: "Force Stop Agent", danger: true, detail: "Kills the agent's process.", run: func(m *Model) tea.Cmd { return v.interrupt(m, true) }})
	}
	if !s.Archived {
		items = append(items, menuItem{label: "Archive…", run: func(m *Model) tea.Cmd { m.push(archiveConfirm(m, s)); return nil }})
	}
	items = append(items, menuItem{label: "Delete…", danger: true, run: func(m *Model) tea.Cmd { m.push(deleteConfirm(m, s)); return nil }})
	items = append(items, menuItem{label: "ra", header: true})
	items = append(items,
		menuItem{label: "New Session…", hint: "ctrl+n", run: func(m *Model) tea.Cmd { return m.newSession(s.ProjectID) }},
		menuItem{label: "Server Settings…", run: func(m *Model) tea.Cmd { return m.openServerSettings() }},
		menuItem{label: "Settings…", run: func(m *Model) tea.Cmd { m.push(newSettingsView(m)); return nil }},
	)
	mn := newMenu(s.Title, items)
	mn.width = 52
	return mn
}

func archiveConfirm(m *Model, s *model.Session) overlay {
	id := s.ID
	archive := func(removeWorktree bool) func(m *Model) tea.Cmd {
		return func(m *Model) tea.Cmd {
			return m.do(func(ctx context.Context) error { return m.conn.Archive(ctx, id, removeWorktree) }, nil)
		}
	}
	items := []menuItem{{label: "Archive", run: archive(false)}}
	if s.Workspace.Kind == model.WorkspaceWorktree {
		items = append(items, menuItem{label: "Archive and Delete Worktree", danger: true, run: archive(true)})
	}
	return newConfirm("Archive “"+s.Title+"”?", "Archiving stops the agent and its shell. The transcript stays.", items...)
}

func deleteConfirm(m *Model, s *model.Session) overlay {
	id := s.ID
	del := func(removeWorktree bool) func(m *Model) tea.Cmd {
		return func(m *Model) tea.Cmd {
			return m.do(func(ctx context.Context) error { return m.conn.DeleteSession(ctx, id, removeWorktree) }, nil)
		}
	}
	items := []menuItem{{label: "Delete", danger: true, run: del(false)}}
	if s.Workspace.Kind == model.WorkspaceWorktree {
		items = append(items, menuItem{label: "Delete with Worktree", danger: true, run: del(true)})
	}
	return newConfirm("Delete “"+s.Title+"”?", "This deletes the session, its transcript and its checkpoints for good. The branch stays.", items...)
}
