package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"remote-agent/internal/client"
	"remote-agent/internal/model"
)

// The session lists, in the layout Settings chooses, as SidebarRows and the
// three sidebars draw them in the apps: Inbox (sessions by what they need),
// Projects (projects folding open to their sessions) and Columns
// (collections beside their sessions).

type rowKind int

const (
	rowHeader rowKind = iota
	rowSession
	rowProject
	rowFold // Archived
	rowCollection
	rowNote
	rowHit // a search result
)

type sideRow struct {
	kind       rowKind
	key        string // the cursor's: "s:<id>", "p:<id>", "c:<collection>"…
	session    *model.Session
	project    *model.Project
	collection client.Collection
	title      string
	count      int
	open       bool // a fold's
	height     int
	hit        client.Hit // a search result's
	lines      []string   // drawn already: a search result's, a long note's
}

func (r sideRow) selectable() bool { return r.kind != rowHeader && r.kind != rowNote }

type sidebar struct {
	cursor    string // a row's key
	offset    int    // lines scrolled
	collCur   string // Columns: the collections' cursor
	inColls   bool   // Columns: the collections column has the keys
	searching bool   // the search field has the keys
	search    textinput.Model
	width     int       // the list's, as last drawn, for the rows that wrap
	lastTop   int       // the lines above the list's rows, as last drawn
	lastRows  []sideRow // as last drawn, for clicks
	lastColls []sideRow
	// revealed is the archived session the Archived fold opened for, so
	// that it opens once and folds again when asked.
	revealed string
}

// collection is the Columns layout's chosen collection.
func (s *sidebar) collection(m *Model) client.Collection {
	if c, ok := client.ParseCollection(m.state.Collection); ok {
		if c.Kind != client.CollectionProject || m.conn.Projects[c.ProjectID] != nil {
			return c
		}
	}
	return client.Collection{Kind: client.CollectionAll}
}

// rows lays out the list for the layout in use, or a search's results in
// its place.
func (s *sidebar) rows(m *Model) []sideRow {
	if m.search.Active() {
		return s.searchRows(m)
	}
	switch m.settings.Layout {
	case client.LayoutProjects:
		return s.projectRows(m)
	case client.LayoutColumns:
		return s.collectionSessionRows(m)
	}
	return s.inboxRows(m)
}

func (s *sidebar) sessionRows(list []*model.Session, height int) []sideRow {
	rows := make([]sideRow, 0, len(list))
	for _, sess := range list {
		rows = append(rows, sideRow{kind: rowSession, key: "s:" + sess.ID, session: sess, height: height})
	}
	return rows
}

func (s *sidebar) archivedRows(m *Model, height int) []sideRow {
	archived := m.conn.ArchivedSessions()
	if len(archived) == 0 {
		return nil
	}
	open := m.state.Folded["archived"]
	rows := []sideRow{{kind: rowFold, key: "f:archived", title: "Archived", count: len(archived), open: open, height: 1}}
	if open {
		rows = append(rows, s.sessionRows(archived, height)...)
	}
	return rows
}

func (s *sidebar) inboxRows(m *Model) []sideRow {
	active := m.conn.ActiveSessions()
	needs, recent := client.InboxGroups(active, m.opened)
	var rows []sideRow
	if len(needs) > 0 {
		rows = append(rows, sideRow{kind: rowHeader, key: "h:needs", title: "Needs You", count: len(needs), height: 1})
		rows = append(rows, s.sessionRows(needs, 2)...)
	}
	if len(recent) > 0 {
		rows = append(rows, sideRow{kind: rowHeader, key: "h:recent", title: "Recent", height: 1})
		rows = append(rows, s.sessionRows(recent, 2)...)
	}
	if len(active) == 0 && m.conn.IndexSynced {
		rows = append(rows, sideRow{kind: rowNote, key: "n:empty", title: "No sessions yet. ctrl+n starts one.", height: 1})
	}
	return append(rows, s.archivedRows(m, 2)...)
}

func (s *sidebar) projectRows(m *Model) []sideRow {
	active := m.conn.ActiveSessions()
	byProject := map[string][]*model.Session{}
	for _, sess := range active {
		byProject[sess.ProjectID] = append(byProject[sess.ProjectID], sess)
	}
	var rows []sideRow
	for _, p := range client.ProjectsInOrder(m.conn.SortedProjects(), active) {
		sessions := byProject[p.ID]
		open := !m.state.Folded["p:"+p.ID]
		if m.sess != nil && m.sess.store.Session != nil && m.sess.store.Session.ProjectID == p.ID && s.cursor == "s:"+m.sess.store.ID {
			open = true
		}
		rows = append(rows, sideRow{kind: rowProject, key: "p:" + p.ID, project: p, count: len(sessions), open: open, height: 1})
		if open {
			rows = append(rows, s.sessionRows(client.NeedsYouFirst(sessions), 1)...)
		}
	}
	if len(rows) == 0 && m.conn.IndexSynced {
		rows = append(rows, sideRow{kind: rowNote, key: "n:empty", title: "No projects yet. ctrl+n starts a session.", height: 1})
	}
	return append(rows, s.archivedRows(m, 1)...)
}

// collections are the Columns layout's first column.
func (s *sidebar) collections(m *Model) []sideRow {
	count := func(c client.Collection) int { return len(client.CollectionSessions(c, m.conn)) }
	row := func(c client.Collection) sideRow {
		return sideRow{kind: rowCollection, key: "c:" + c.String(), collection: c, title: client.CollectionTitle(c, m.conn), count: count(c), height: 1}
	}
	rows := []sideRow{
		row(client.Collection{Kind: client.CollectionNeedsYou}),
		row(client.Collection{Kind: client.CollectionRunning}),
		row(client.Collection{Kind: client.CollectionAll}),
	}
	if projects := m.conn.SortedProjects(); len(projects) > 0 {
		rows = append(rows, sideRow{kind: rowHeader, key: "h:projects", title: "Projects", height: 1})
		for _, p := range projects {
			r := row(client.Collection{Kind: client.CollectionProject, ProjectID: p.ID})
			r.project = p
			rows = append(rows, r)
		}
	}
	return append(rows, row(client.Collection{Kind: client.CollectionArchived}))
}

func (s *sidebar) collectionSessionRows(m *Model) []sideRow {
	list := client.CollectionSessions(s.collection(m), m.conn)
	rows := s.sessionRows(list, 3)
	if len(rows) == 0 && m.conn.IndexSynced {
		rows = append(rows, sideRow{kind: rowNote, key: "n:empty", title: collectionEmpty(s.collection(m)), height: 1})
	}
	return rows
}

func collectionEmpty(c client.Collection) string {
	switch c.Kind {
	case client.CollectionNeedsYou:
		return "Nothing needs you."
	case client.CollectionRunning:
		return "Nothing is running."
	case client.CollectionArchived:
		return "Nothing archived."
	case client.CollectionProject:
		return "No sessions in this project. n starts one."
	}
	return "No sessions yet. ctrl+n starts one."
}

// Drawing

func (m *Model) glyph(s *model.Session) string {
	switch s.Status {
	case model.SessionRunning:
		return m.st.Accent.Render(m.spinner())
	case model.SessionAwaitingApproval:
		return m.st.Yellow.Render("◆")
	case model.SessionError:
		return m.st.Red.Render("✗")
	case model.SessionStopped:
		return m.st.Faint.Render("■")
	}
	switch {
	case len(s.BackgroundTasks) > 0:
		return m.st.Blue.Render("◷")
	case s.Unread:
		return m.st.Accent.Render("●")
	case s.Draft != nil:
		return m.st.Faint.Render("✎")
	}
	return m.st.Faint.Render("○")
}

// place is where a session works: its project and branch.
func (m *Model) place(s *model.Session) string {
	var parts []string
	if p := m.conn.Projects[s.ProjectID]; p != nil {
		parts = append(parts, p.Name)
	}
	if s.Workspace.Branch != "" {
		parts = append(parts, "⑂ "+s.Workspace.Branch)
	} else if s.Workspace.Kind == model.WorkspaceWorktree {
		parts = append(parts, "⑂")
	}
	return strings.Join(parts, " ")
}

// activity is what a session does, for the Columns layout's rows.
func (m *Model) activity(s *model.Session) string {
	switch {
	case s.Status == model.SessionAwaitingApproval:
		return "Waiting for your answer"
	case s.Status == model.SessionError:
		if s.Error != "" {
			return s.Error
		}
		return "Failed"
	case s.Status == model.SessionRunning:
		return "Working…"
	case s.Status == model.SessionStopped:
		return "Stopped"
	case len(s.BackgroundTasks) > 0:
		return "In the background: " + s.BackgroundTasks[0].Description
	}
	var parts []string
	if name := m.modelName(s); name != "" {
		parts = append(parts, name)
	}
	if s.Context != nil && s.Context.Window > 0 {
		parts = append(parts, fmt.Sprintf("%d%% context", s.Context.Used*100/s.Context.Window))
	}
	return strings.Join(parts, " · ")
}

func (m *Model) modelName(s *model.Session) string {
	if s.ModelInfo != nil && s.ModelInfo.Name != "" {
		return s.ModelInfo.Name
	}
	id := s.Model
	if s.ModelInfo != nil && s.ModelInfo.ID != "" {
		id = s.ModelInfo.ID
	}
	if h := m.conn.Harness(s.Harness); h != nil {
		for _, c := range h.Models {
			if c.ID == id {
				return c.Name
			}
		}
	}
	return id
}

func (s *sidebar) renderRow(m *Model, r sideRow, width int, selected, focused bool) []string {
	sel := func(lines []string) []string {
		if !selected {
			return lines
		}
		for i, l := range lines {
			if focused {
				lines[i] = highlight(l, width, m.pal.Surface0)
			} else {
				lines[i] = m.st.Accent.Render("▌") + pad(l, width-1)[1:]
				lines[i] = pad(lines[i], width)
			}
		}
		return lines
	}
	switch r.kind {
	case rowHeader:
		title := strings.ToUpper(r.title)
		style := m.st.Header
		if r.key == "h:needs" {
			style = m.st.Yellow.Bold(true)
		}
		label := style.Render(title)
		if r.count > 0 {
			label += " " + m.st.Faint.Render(fmt.Sprint(r.count))
		}
		return []string{pad(" "+label, width)}
	case rowNote:
		if r.lines != nil {
			return slices.Clone(r.lines)
		}
		return []string{pad(" "+m.st.Faint.Render(truncate(r.title, width-2)), width)}
	case rowHit:
		return append(sel(slices.Clone(r.lines)), "")
	case rowFold:
		arrow := "▸"
		if r.open {
			arrow = "▾"
		}
		return sel([]string{pad(" "+m.st.Faint.Render(arrow+" ")+m.st.Header.Render(r.title)+" "+m.st.Faint.Render(fmt.Sprint(r.count)), width)})
	case rowProject:
		arrow := "▸"
		if r.open {
			arrow = "▾"
		}
		right := m.st.Faint.Render(fmt.Sprint(r.count))
		if !r.open {
			var sessions []*model.Session
			for _, sess := range m.conn.ActiveSessions() {
				if sess.ProjectID == r.project.ID {
					sessions = append(sessions, sess)
				}
			}
			if p := client.MostPressing(sessions); p != nil {
				right = m.glyph(p) + " " + right
			}
		}
		return sel([]string{spread(" "+m.st.Faint.Render(arrow)+" "+m.st.Bold.Render(r.project.Name), right+" ", width)})
	case rowCollection:
		title := r.title
		style := m.st.Text
		if r.collection.Kind == client.CollectionNeedsYou && r.count > 0 {
			style = m.st.Yellow.Bold(true)
		}
		right := ""
		if r.count > 0 {
			right = m.st.Faint.Render(fmt.Sprint(r.count)) + " "
		}
		if r.project != nil {
			title = "  " + title
		}
		return sel([]string{spread(" "+style.Render(title), right, width)})
	case rowSession:
		return sel(s.sessionLines(m, r, width))
	}
	return nil
}

func (s *sidebar) sessionLines(m *Model, r sideRow, width int) []string {
	sess := r.session
	title := sess.Title
	if title == "" {
		title = "New session"
	}
	titleStyle := m.st.Text
	if client.StandsOut(sess) {
		titleStyle = m.st.Bold
	}
	age := m.st.Faint.Render(client.ShortAge(sess.UpdatedAt, time.Now())) + " "
	switch r.height {
	case 1:
		lead := "   "
		if m.settings.Layout != client.LayoutProjects {
			lead = " "
		}
		wt := ""
		if sess.Workspace.Kind == model.WorkspaceWorktree {
			wt = m.st.Faint.Render(" ⑂")
		}
		return []string{spread(lead+m.glyph(sess)+" "+titleStyle.Render(title)+wt, age, width)}
	case 3:
		return []string{
			spread(" "+m.glyph(sess)+" "+titleStyle.Render(title), age, width),
			pad("   "+m.st.Faint.Render(truncate(m.place(sess), width-4)), width),
			pad("   "+m.st.Subtext.Render(truncate(m.activity(sess), width-4)), width),
		}
	}
	return []string{
		spread(" "+m.glyph(sess)+" "+titleStyle.Render(title), age, width),
		pad("   "+m.st.Faint.Render(truncate(m.place(sess), width-4)), width),
	}
}

func (s *sidebar) view(m *Model, l layout) string {
	focused := m.focus == focusSidebar && len(m.overlays) == 0
	h := l.bodyH
	if l.collW > 0 {
		left := s.drawColumn(m, s.collections(m), s.collKey(m), l.collW, h, focused && s.inColls, nil, &s.lastColls)
		sep := m.st.Rule.Render(strings.TrimSuffix(strings.Repeat("│\n", h), "\n"))
		right := s.drawList(m, l.sideW-l.collW-1, h, focused && !s.inColls)
		return joinColumns(left, sep, right)
	}
	return s.drawList(m, l.sideW, h, focused)
}

func (s *sidebar) collKey(m *Model) string {
	if s.collCur == "" {
		s.collCur = "c:" + s.collection(m).String()
	}
	return s.collCur
}

func (s *sidebar) drawList(m *Model, width, height int, focused bool) string {
	s.width = width
	top := s.searchField(m, width, focused)
	if m.settings.Layout == client.LayoutColumns && !m.search.Active() {
		c := s.collection(m)
		title := m.st.AccentBold.Render(client.CollectionTitle(c, m.conn))
		n := len(client.CollectionSessions(c, m.conn))
		if m.layout().collW == 0 {
			title = m.st.Faint.Render("‹ ") + title + m.st.Faint.Render(" ›")
		}
		top = append(top, spread(" "+title, m.st.Faint.Render(plural(n, "session", "sessions")+" "), width), "")
	}
	s.lastTop = len(top)
	return s.drawColumn(m, s.rows(m), s.cursor, width, height, focused, top, &s.lastRows)
}

// drawColumn draws rows under top, scrolled to show the cursor.
func (s *sidebar) drawColumn(m *Model, rows []sideRow, cursor string, width, height int, focused bool, top []string, keep *[]sideRow) string {
	*keep = rows
	openKey := ""
	if m.sess != nil {
		openKey = "s:" + m.sess.store.ID
	}
	var lines []string
	cursorAt, cursorH := -1, 0
	for _, r := range rows {
		selected := r.key == cursor
		if !focused && r.kind == rowSession {
			selected = r.key == openKey
		} else if !focused && r.kind == rowHit {
			selected = "s:"+r.session.ID == openKey
		} else if !focused && r.kind == rowCollection {
			selected = r.key == cursor
		}
		if r.key == cursor {
			cursorAt, cursorH = len(lines), r.height
		}
		lines = append(lines, s.renderRow(m, r, width, selected, focused)...)
	}
	avail := max(1, height-len(top))
	offset := 0
	if keep == &s.lastRows {
		if cursorAt >= 0 {
			if cursorAt < s.offset {
				s.offset = cursorAt
			}
			if cursorAt+cursorH > s.offset+avail {
				s.offset = cursorAt + cursorH - avail
			}
		}
		s.offset = max(0, min(s.offset, len(lines)-avail))
		offset = s.offset
	} else if cursorAt >= avail {
		offset = cursorAt - avail + 1
	}
	if offset > 0 && offset < len(lines) {
		lines = lines[offset:]
	}
	return fill(append(top, lines...), width, height)
}

func joinColumns(cols ...string) string {
	split := make([][]string, len(cols))
	n := 0
	for i, c := range cols {
		split[i] = strings.Split(c, "\n")
		n = max(n, len(split[i]))
	}
	var b strings.Builder
	for y := 0; y < n; y++ {
		for _, lines := range split {
			if y < len(lines) {
				b.WriteString(lines[y])
			}
		}
		if y < n-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// Keys

func (s *sidebar) moveCursor(rows []sideRow, cursor string, d int) string {
	idx := -1
	for i, r := range rows {
		if r.key == cursor {
			idx = i
		}
	}
	for i := idx + d; i >= 0 && i < len(rows); i += d {
		if rows[i].selectable() {
			return rows[i].key
		}
	}
	if idx < 0 {
		for _, r := range rows {
			if r.selectable() {
				return r.key
			}
		}
	}
	return cursor
}

func (s *sidebar) current(rows []sideRow) (sideRow, bool) {
	for _, r := range rows {
		if r.key == s.cursor {
			return r, true
		}
	}
	return sideRow{}, false
}

func (s *sidebar) scrollBy(d int) {
	s.offset = max(0, s.offset+d)
}

func (s *sidebar) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	if s.searching {
		return s.searchKey(m, k)
	}
	l := m.layout()
	if m.settings.Layout == client.LayoutColumns && (s.inColls && l.collW > 0) {
		return s.collectionKey(m, k)
	}
	rows := s.rows(m)
	switch k.String() {
	case "up":
		s.cursor = s.moveCursor(rows, s.cursor, -1)
	case "down":
		s.cursor = s.moveCursor(rows, s.cursor, 1)
	case "home":
		s.cursor = s.moveCursor(rows, "", 1)
	case "end":
		s.cursor = s.moveCursor(rows, rows[len(rows)-1].key+"\x00", -1)
		if len(rows) > 0 && rows[len(rows)-1].selectable() {
			s.cursor = rows[len(rows)-1].key
		}
	case "pgdown":
		for range 8 {
			s.cursor = s.moveCursor(rows, s.cursor, 1)
		}
	case "pgup":
		for range 8 {
			s.cursor = s.moveCursor(rows, s.cursor, -1)
		}
	case "/":
		s.focusSearch(m)
	case "ctrl+a":
		if m.search.Active() {
			s.toggleArchived(m)
		}
	case "esc":
		// Up a level: out of the search, or to the collections.
		if s.search.Value() != "" {
			s.clearSearch(m)
		} else if m.settings.Layout == client.LayoutColumns && l.collW > 0 {
			s.inColls = true
		}
	case "left", "right":
		left := k.String() == "left"
		if m.settings.Layout == client.LayoutColumns {
			if l.collW > 0 {
				s.inColls = s.inColls || left
				return nil
			}
			s.stepCollection(m, map[bool]int{true: -1, false: 1}[left])
			return nil
		}
		if r, ok := s.current(rows); ok && (r.kind == rowProject || r.kind == rowFold) {
			s.setFold(m, r, !left)
		}
	default:
		return s.rowKey(m, rows, k)
	}
	return nil
}

func (s *sidebar) setFold(m *Model, r sideRow, open bool) {
	switch r.kind {
	case rowProject:
		m.state.Folded["p:"+r.project.ID] = !open
	case rowFold:
		m.state.Folded["archived"] = open
	}
	m.saveState()
}

func (s *sidebar) rowKey(m *Model, rows []sideRow, k tea.KeyPressMsg) tea.Cmd {
	r, ok := s.current(rows)
	if !ok {
		if len(rows) > 0 {
			s.cursor = s.moveCursor(rows, "", 1)
		}
		if k.String() == "n" {
			return m.newSession("")
		}
		return nil
	}
	switch k.String() {
	case "enter":
		switch r.kind {
		case rowSession:
			m.openSession(r.session.ID)
			m.setFocus(focusComposer)
		case rowHit:
			m.openHit(r.hit, true)
		case rowProject, rowFold:
			s.setFold(m, r, !r.open)
		}
	case "space":
		switch r.kind {
		case rowProject, rowFold:
			s.setFold(m, r, !r.open)
		case rowSession:
			m.openSession(r.session.ID)
		case rowHit:
			m.openHit(r.hit, false)
		}
	case "n":
		switch {
		case r.project != nil:
			return m.newSession(r.project.ID)
		case r.session != nil:
			return m.newSession(r.session.ProjectID)
		}
		return m.newSession("")
	case "a":
		if r.session != nil && !r.session.Archived {
			m.push(archiveConfirm(m, r.session))
		}
	case "D":
		if r.session != nil {
			m.push(deleteConfirm(m, r.session))
		}
	case "R":
		if r.session != nil && canRename(m) {
			return rename(m, r.session)
		}
	case ".":
		m.push(s.rowMenu(m, r))
	}
	return nil
}

func (s *sidebar) rowMenu(m *Model, r sideRow) overlay {
	switch {
	case r.session != nil:
		sess := r.session
		items := []menuItem{{label: "Open", run: func(m *Model) tea.Cmd { m.openSession(sess.ID); m.setFocus(focusComposer); return nil }}}
		if r.kind == rowHit {
			h := r.hit
			items[0].run = func(m *Model) tea.Cmd { m.openHit(h, true); return nil }
		}
		if canRename(m) {
			items = append(items, menuItem{label: "Rename…", hint: "R", run: func(m *Model) tea.Cmd { return rename(m, sess) }})
		}
		if !sess.Archived {
			items = append(items, menuItem{label: "Archive…", hint: "a", run: func(m *Model) tea.Cmd { m.push(archiveConfirm(m, sess)); return nil }})
		}
		items = append(items, menuItem{label: "Delete…", hint: "D", danger: true, run: func(m *Model) tea.Cmd { m.push(deleteConfirm(m, sess)); return nil }})
		return newMenu(sess.Title, items)
	case r.project != nil:
		p := r.project
		busy := false
		for _, sess := range m.conn.ActiveSessions() {
			if sess.ProjectID == p.ID {
				busy = true
			}
		}
		return newMenu(p.Name, []menuItem{
			{label: "New Session…", run: func(m *Model) tea.Cmd { return m.newSession(p.ID) }},
			{label: "Forget Project", disabled: busy, detail: p.Path, run: func(m *Model) tea.Cmd {
				return m.do(func(ctx context.Context) error { return m.conn.RemoveProject(ctx, p.ID) }, nil)
			}},
		})
	}
	return newMenu("", []menuItem{{label: "New Session…", run: func(m *Model) tea.Cmd { return m.newSession("") }}})
}

func (s *sidebar) collectionKey(m *Model, k tea.KeyPressMsg) tea.Cmd {
	colls := s.collections(m)
	cur := s.collKey(m)
	switch k.String() {
	case "up":
		s.collCur = s.moveCursor(colls, cur, -1)
		s.chooseCollection(m)
	case "down":
		s.collCur = s.moveCursor(colls, cur, 1)
		s.chooseCollection(m)
	case "right", "enter":
		s.inColls = false
		list := client.CollectionSessions(s.collection(m), m.conn)
		if open := client.SessionToOpen(list, m.state.Recent); open != nil {
			s.cursor = "s:" + open.ID
			if k.String() == "enter" {
				m.openSession(open.ID)
			}
		}
	case "n":
		c := s.collection(m)
		return m.newSession(c.ProjectID)
	case "/":
		s.inColls = false
		s.focusSearch(m)
	case ".":
		for _, r := range colls {
			if r.key == cur && r.project != nil {
				m.push(s.rowMenu(m, r))
			}
		}
	}
	return nil
}

func (s *sidebar) chooseCollection(m *Model) {
	stored := strings.TrimPrefix(s.collCur, "c:")
	if c, ok := client.ParseCollection(stored); ok {
		m.state.Collection = c.String()
		m.saveState()
	}
}

// stepCollection moves to the next or previous collection, where the
// collections have no column of their own.
func (s *sidebar) stepCollection(m *Model, d int) {
	colls := s.collections(m)
	s.collCur = s.moveCursor(colls, s.collKey(m), d)
	s.chooseCollection(m)
}

func (s *sidebar) click(m *Model, l layout, x, y int) tea.Cmd {
	rows, cursor := s.lastRows, &s.cursor
	top := 0
	offset := s.offset
	if l.collW > 0 && x < l.collW {
		rows, cursor, offset = s.lastColls, &s.collCur, 0
		s.inColls = true
	} else {
		s.inColls = false
		top = s.lastTop
		switch {
		case y == 0:
			s.focusSearch(m)
			return nil
		case y == 1 && (s.searching || m.search.Active()):
			s.toggleArchived(m)
			return nil
		}
	}
	line := y - top + offset
	at := 0
	for _, r := range rows {
		if line >= at && line < at+r.height {
			if !r.selectable() {
				return nil
			}
			*cursor = r.key
			switch r.kind {
			case rowSession:
				m.openSession(r.session.ID)
			case rowHit:
				m.openHit(r.hit, false)
			case rowProject, rowFold:
				s.setFold(m, r, !r.open)
			case rowCollection:
				s.chooseCollection(m)
			}
			return nil
		}
		at += r.height
	}
	return nil
}
