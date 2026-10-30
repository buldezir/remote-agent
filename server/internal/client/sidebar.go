package client

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"remote-agent/internal/model"
)

// How the sidebar groups sessions, as SidebarLayout does in the apps
// (web/src/ui/sidebarLayout.ts), for the one server ra talks to.

// Layout is how the sidebar lays out sessions, chosen in Settings.
type Layout string

const (
	LayoutInbox    Layout = "inbox"
	LayoutProjects Layout = "projects"
	LayoutColumns  Layout = "columns"
)

// NeedsYou: waiting for an answer, or failed; what a person has to look at.
func NeedsYou(s *model.Session) bool {
	return s.Status == model.SessionAwaitingApproval || s.Status == model.SessionError
}

// StandsOut: needs you or is unread; its title is bold in the lists.
func StandsOut(s *model.Session) bool {
	return NeedsYou(s) || s.Unread
}

// IsBusy: working, or done with tasks left running in the background.
func IsBusy(s *model.Session) bool {
	return s.Status == model.SessionRunning || (s.Status == model.SessionIdle && len(s.BackgroundTasks) > 0)
}

// Urgency is how much the session asks for a look: an answer it waits for,
// then a failure, then what ended since it was last read, then work, then
// work left in the background. Other sessions ask nothing.
func Urgency(s *model.Session) int {
	switch {
	case s.Status == model.SessionAwaitingApproval:
		return 5
	case s.Status == model.SessionError:
		return 4
	case s.Unread:
		return 3
	case s.Status == model.SessionRunning:
		return 2
	case s.Status == model.SessionIdle && len(s.BackgroundTasks) > 0:
		return 1
	}
	return 0
}

// MostPressing is the session whose state a project's row shows for all
// of them: the one that asks most, if any does.
func MostPressing(sessions []*model.Session) *model.Session {
	var found *model.Session
	for _, s := range sessions {
		if found == nil && Urgency(s) > 0 || found != nil && Urgency(s) > Urgency(found) {
			found = s
		}
	}
	return found
}

// OpenedSession is the open session, and whether it stood out when it was
// opened.
type OpenedSession struct {
	ID       string
	StoodOut bool
}

// InboxGroups splits sessions into the Inbox's groups, each in the order
// given: what needs you or is unread, then the rest. The open session
// counts as unread if it was when it was opened: reading it leaves its row
// where it is, and so does a turn that ends in view.
func InboxGroups(active []*model.Session, opened OpenedSession) (needsYou, recent []*model.Session) {
	for _, s := range active {
		stands := NeedsYou(s)
		if !stands {
			if opened.ID != "" && s.ID == opened.ID {
				stands = opened.StoodOut
			} else {
				stands = s.Unread
			}
		}
		if stands {
			needsYou = append(needsYou, s)
		} else {
			recent = append(recent, s)
		}
	}
	return needsYou, recent
}

// NeedsYouFirst puts what needs you first, then the rest, each in the order
// given.
func NeedsYouFirst(sessions []*model.Session) []*model.Session {
	out := make([]*model.Session, 0, len(sessions))
	for _, s := range sessions {
		if NeedsYou(s) {
			out = append(out, s)
		}
	}
	for _, s := range sessions {
		if !NeedsYou(s) {
			out = append(out, s)
		}
	}
	return out
}

// ProjectsInOrder orders projects for the Projects layout: those with
// sessions by their latest session, then the rest by name.
func ProjectsInOrder(projects []*model.Project, active []*model.Session) []*model.Project {
	latest := map[string]int{}
	for i, s := range active { // latest first
		if _, ok := latest[s.ProjectID]; !ok {
			latest[s.ProjectID] = i
		}
	}
	var with, without []*model.Project
	for _, p := range projects { // by name
		if _, ok := latest[p.ID]; ok {
			with = append(with, p)
		} else {
			without = append(without, p)
		}
	}
	slices.SortStableFunc(with, func(a, b *model.Project) int { return cmp.Compare(latest[a.ID], latest[b.ID]) })
	return append(with, without...)
}

// CollectionKind is what the Columns layout's first column lists.
type CollectionKind string

const (
	CollectionNeedsYou CollectionKind = "needsYou"
	CollectionRunning  CollectionKind = "running"
	CollectionAll      CollectionKind = "all"
	CollectionArchived CollectionKind = "archived"
	CollectionProject  CollectionKind = "project"
)

// Collection is what the Columns layout shows the sessions of.
type Collection struct {
	Kind      CollectionKind
	ProjectID string
}

// ParseCollection reads a stored collection: "needsYou", or
// "project <project id>".
func ParseCollection(stored string) (Collection, bool) {
	parts := strings.Fields(stored)
	if len(parts) == 0 {
		return Collection{}, false
	}
	switch k := CollectionKind(parts[0]); k {
	case CollectionNeedsYou, CollectionRunning, CollectionAll, CollectionArchived:
		return Collection{Kind: k}, len(parts) == 1
	case CollectionProject:
		if len(parts) == 2 {
			return Collection{Kind: k, ProjectID: parts[1]}, true
		}
	}
	return Collection{}, false
}

func (c Collection) String() string {
	if c.Kind == CollectionProject {
		return string(c.Kind) + " " + c.ProjectID
	}
	return string(c.Kind)
}

// CollectionSessions lists a collection's sessions, from the connection's.
func CollectionSessions(c Collection, conn *ServerConnection) []*model.Session {
	active := conn.ActiveSessions()
	var out []*model.Session
	switch c.Kind {
	case CollectionNeedsYou:
		for _, s := range active {
			if NeedsYou(s) {
				out = append(out, s)
			}
		}
	case CollectionRunning:
		for _, s := range active {
			if IsBusy(s) {
				out = append(out, s)
			}
		}
	case CollectionAll:
		// What needs you first, as in the other layouts.
		out = NeedsYouFirst(active)
	case CollectionArchived:
		out = conn.ArchivedSessions()
	case CollectionProject:
		for _, s := range active {
			if s.ProjectID == c.ProjectID {
				out = append(out, s)
			}
		}
	}
	return out
}

// RevealCollection is where the Columns layout shows a session when the
// chosen collection doesn't have it: in its project, or in the archive.
func RevealCollection(s *model.Session, conn *ServerConnection) Collection {
	if s.Archived {
		return Collection{Kind: CollectionArchived}
	}
	if conn.Projects[s.ProjectID] != nil {
		return Collection{Kind: CollectionProject, ProjectID: s.ProjectID}
	}
	return Collection{Kind: CollectionAll}
}

// SessionToOpen is the session to open on choosing a collection: of its
// sessions, the one opened last, else its first. recent holds session ids,
// the latest first.
func SessionToOpen(sessions []*model.Session, recent []string) *model.Session {
	byID := map[string]*model.Session{}
	for _, s := range sessions {
		byID[s.ID] = s
	}
	for _, id := range recent {
		if s := byID[id]; s != nil {
			return s
		}
	}
	if len(sessions) > 0 {
		return sessions[0]
	}
	return nil
}

const recentLimit = 100

// AddingRecent returns recent with id first, at most 100 long.
func AddingRecent(recent []string, id string) []string {
	out := []string{id}
	for _, r := range recent {
		if r != id && len(out) < recentLimit {
			out = append(out, r)
		}
	}
	return out
}

// CollectionTitle names a collection.
func CollectionTitle(c Collection, conn *ServerConnection) string {
	switch c.Kind {
	case CollectionNeedsYou:
		return "Needs You"
	case CollectionRunning:
		return "Running"
	case CollectionAll:
		return "All Sessions"
	case CollectionArchived:
		return "Archived"
	}
	if p := conn.Projects[c.ProjectID]; p != nil {
		return p.Name
	}
	return "Project"
}

// ShortAge is a session's age where a row has little room: "now", "4m",
// "3h", "2d", then the date.
func ShortAge(t, now time.Time) string {
	s := now.Sub(t)
	switch {
	case s < time.Minute:
		return "now"
	case s < time.Hour:
		return fmt.Sprintf("%dm", int(s/time.Minute))
	case s < 24*time.Hour:
		return fmt.Sprintf("%dh", int(s/time.Hour))
	case s < 7*24*time.Hour:
		return fmt.Sprintf("%dd", int(s/(24*time.Hour)))
	}
	return t.Local().Format("Jan 2")
}
