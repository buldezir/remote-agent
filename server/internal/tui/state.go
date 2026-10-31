package tui

import (
	"encoding/json"
	"os"
	"path/filepath"

	"remote-agent/internal/client"
)

// Settings are what Settings changes, in settings.json.
type Settings struct {
	Theme         string        `json:"theme"`         // system, dark or light
	Layout        client.Layout `json:"layout"`        // inbox, projects or columns
	Notifications string        `json:"notifications"` // off, bell or desktop
	Mouse         bool          `json:"mouse"`
	// GroupToolCalls folds runs of tool calls into a line that sums them up
	// (client.ToolRuns).
	GroupToolCalls bool `json:"groupToolCalls"`
}

func DefaultSettings() Settings {
	return Settings{Theme: "system", Layout: client.LayoutInbox, Notifications: "desktop", Mouse: true, GroupToolCalls: true}
}

func (s *Settings) normalize() {
	d := DefaultSettings()
	switch s.Theme {
	case "system", "dark", "light":
	default:
		s.Theme = d.Theme
	}
	switch s.Layout {
	case client.LayoutInbox, client.LayoutProjects, client.LayoutColumns:
	default:
		s.Layout = d.Layout
	}
	switch s.Notifications {
	case "off", "bell", "desktop":
	default:
		s.Notifications = d.Notifications
	}
}

// State is what ra remembers between runs, in state.json: where it was, and
// what new sessions were last started with.
type State struct {
	LastSession   string            `json:"lastSession,omitempty"`
	Recent        []string          `json:"recent,omitempty"`
	Collection    string            `json:"collection,omitempty"`
	Folded        map[string]bool   `json:"folded,omitempty"` // projects folded shut, "archived" open
	SidebarHidden bool              `json:"sidebarHidden,omitempty"`
	LastProject   string            `json:"lastProject,omitempty"`
	LastHarness   string            `json:"lastHarness,omitempty"`
	LastModel     map[string]string `json:"lastModel,omitempty"`
	LastEffort    map[string]string `json:"lastEffort,omitempty"`
	LastMode      map[string]string `json:"lastMode,omitempty"`
	// LastCommit is how far the last commit went: commit, push or
	// pullRequest (client.CommitScope).
	LastCommit string `json:"lastCommit,omitempty"`
}

func loadJSON(path string, v any) {
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, v)
	}
}

func saveJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func LoadSettings(dir string) Settings {
	s := DefaultSettings()
	loadJSON(filepath.Join(dir, "settings.json"), &s)
	s.normalize()
	return s
}

func loadState(dir string) State {
	var s State
	loadJSON(filepath.Join(dir, "state.json"), &s)
	if s.Folded == nil {
		s.Folded = map[string]bool{}
	}
	for _, m := range []*map[string]string{&s.LastModel, &s.LastEffort, &s.LastMode} {
		if *m == nil {
			*m = map[string]string{}
		}
	}
	return s
}
