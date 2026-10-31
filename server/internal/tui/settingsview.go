package tui

import (
	tea "charm.land/bubbletea/v2"

	"remote-agent/internal/client"
)

// settingsView is Settings: the theme, the sidebar's layout,
// notifications, the mouse and tool calls.
type settingsView struct {
	row int
}

type setting struct {
	title   string
	summary string
	values  []string
	names   []string
	get     func(*Settings) string
	set     func(*Settings, string)
}

var settingRows = []setting{
	{
		title: "Theme", summary: "Catppuccin: Frappé when dark, Latte when light. System follows the terminal.",
		values: []string{"system", "dark", "light"}, names: []string{"System", "Dark", "Light"},
		get: func(s *Settings) string { return s.Theme }, set: func(s *Settings, v string) { s.Theme = v },
	},
	{
		title: "Sidebar", summary: "Inbox: sessions by what they need. Projects: each project's sessions under it. Columns: collections beside their sessions.",
		values: []string{string(client.LayoutInbox), string(client.LayoutProjects), string(client.LayoutColumns)}, names: []string{"Inbox", "Projects", "Columns"},
		get: func(s *Settings) string { return string(s.Layout) }, set: func(s *Settings, v string) { s.Layout = client.Layout(v) },
	},
	{
		title: "Notifications", summary: "When a session needs you, finishes or fails while you look elsewhere. Desktop asks the terminal to post them.",
		values: []string{"desktop", "bell", "off"}, names: []string{"Desktop", "Bell", "Off"},
		get: func(s *Settings) string { return s.Notifications }, set: func(s *Settings, v string) { s.Notifications = v },
	},
	{
		title: "Mouse", summary: "Scroll and click. Off leaves the mouse to the terminal, to select text.",
		values: []string{"on", "off"}, names: []string{"On", "Off"},
		get: func(s *Settings) string {
			if s.Mouse {
				return "on"
			}
			return "off"
		},
		set: func(s *Settings, v string) { s.Mouse = v == "on" },
	},
	{
		title: "Group tool calls", summary: "Folds each run of the agent's tool calls into one line, such as “Ran 4 commands, edited 3 files”, which enter opens.",
		values: []string{"on", "off"}, names: []string{"On", "Off"},
		get: func(s *Settings) string {
			if s.GroupToolCalls {
				return "on"
			}
			return "off"
		},
		set: func(s *Settings, v string) { s.GroupToolCalls = v == "on" },
	},
}

func newSettingsView(m *Model) *settingsView { return &settingsView{} }

func (sv *settingsView) hints() [][2]string {
	return [][2]string{{"↑↓", "setting"}, {"←→ enter", "change"}, {"esc", "close"}}
}

func (sv *settingsView) update(m *Model, k tea.KeyPressMsg) tea.Cmd {
	n := len(settingRows) + 1 // and Restore Defaults
	switch k.String() {
	case "esc", "q":
		m.pop()
	case "up", "shift+tab":
		sv.row = max(0, sv.row-1)
	case "down", "tab":
		sv.row = min(n-1, sv.row+1)
	case "left":
		sv.step(m, -1)
	case "right", "enter", "space":
		sv.step(m, 1)
	}
	return nil
}

func (sv *settingsView) step(m *Model, d int) {
	if sv.row == len(settingRows) {
		if d > 0 {
			m.settings = DefaultSettings()
			sv.changed(m)
		}
		return
	}
	r := settingRows[sv.row]
	cur := r.get(&m.settings)
	i := 0
	for j, v := range r.values {
		if v == cur {
			i = j
		}
	}
	r.set(&m.settings, r.values[(i+d+len(r.values))%len(r.values)])
	sv.changed(m)
}

func (sv *settingsView) changed(m *Model) {
	m.settings.normalize()
	m.applyTheme()
	m.saveSettings()
}

func (sv *settingsView) view(m *Model, width, height int) string {
	w := min(64, width-4)
	inner := w - 4
	var lines []string
	for i, r := range settingRows {
		cur := r.get(&m.settings)
		var opts []string
		for j, v := range r.values {
			if v == cur {
				opts = append(opts, m.st.ChipOn.Render(r.names[j]))
			} else {
				opts = append(opts, m.st.Chip.Render(r.names[j]))
			}
		}
		title := m.st.Subtext.Render(pad(r.title, 18))
		marker := "  "
		if i == sv.row {
			title = m.st.AccentBold.Render(pad(r.title, 18))
			marker = m.st.Accent.Render("▌ ")
		}
		line := marker + title
		for _, o := range opts {
			line += o + " "
		}
		lines = append(lines, truncate(line, inner))
		if i == sv.row {
			for _, l := range wrap(r.summary, inner-4) {
				lines = append(lines, "    "+m.st.Faint.Render(l))
			}
		}
		lines = append(lines, "")
	}
	restore := m.st.Button.Render("Restore Defaults")
	if sv.row == len(settingRows) {
		restore = m.st.ButtonOn.Render("Restore Defaults")
	}
	lines = append(lines, "  "+restore)
	return box(m, "Settings", lines, w)
}
