package client

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"remote-agent/internal/model"
)

// As web/src/ui/sidebarLayout.test.ts.

func sess(id string, status model.SessionStatus, patch func(*model.Session)) *model.Session {
	s := &model.Session{ID: id, ProjectID: "p", Harness: "fake", Status: status, Title: id}
	if patch != nil {
		patch(s)
	}
	return s
}

func unread(s *model.Session) { s.Unread = true }
func background(s *model.Session) {
	s.BackgroundTasks = []model.BackgroundTask{{ID: "b", Kind: model.BackgroundCommand}}
}

func sessionIDs(list []*model.Session) []string {
	var out []string
	for _, s := range list {
		out = append(out, s.ID)
	}
	return out
}

func TestCollectionsRoundTrip(t *testing.T) {
	for _, c := range []Collection{{Kind: CollectionNeedsYou}, {Kind: CollectionRunning}, {Kind: CollectionAll}, {Kind: CollectionArchived}, {Kind: CollectionProject, ProjectID: "prj"}} {
		if got, ok := ParseCollection(c.String()); !ok || got != c {
			t.Errorf("%v: %v %v", c, got, ok)
		}
	}
	for _, s := range []string{"", "project", "bogus"} {
		if _, ok := ParseCollection(s); ok {
			t.Errorf("%q parsed", s)
		}
	}
}

func TestGroupsSessionsByWhatTheyNeed(t *testing.T) {
	if !NeedsYou(sess("s", model.SessionAwaitingApproval, nil)) || !NeedsYou(sess("s", model.SessionError, nil)) || NeedsYou(sess("s", model.SessionRunning, nil)) {
		t.Error("NeedsYou")
	}
	if !IsBusy(sess("s", model.SessionRunning, nil)) || !IsBusy(sess("s", model.SessionIdle, background)) || IsBusy(sess("s", model.SessionIdle, nil)) || IsBusy(sess("s", model.SessionStopped, background)) {
		t.Error("IsBusy")
	}
}

func TestInboxPutsWhatNeedsYouOrIsUnreadFirst(t *testing.T) {
	items := []*model.Session{
		sess("running", model.SessionRunning, nil),
		sess("unread", model.SessionIdle, unread),
		sess("background", model.SessionIdle, background),
		sess("waiting", model.SessionAwaitingApproval, nil),
		sess("idle", model.SessionIdle, nil),
		sess("failed", model.SessionError, nil),
	}
	if !StandsOut(sess("s", model.SessionIdle, unread)) || StandsOut(sess("s", model.SessionRunning, nil)) {
		t.Error("StandsOut")
	}
	needs, recent := InboxGroups(items, OpenedSession{})
	if !slices.Equal(sessionIDs(needs), []string{"unread", "waiting", "failed"}) || !slices.Equal(sessionIDs(recent), []string{"running", "background", "idle"}) {
		t.Fatalf("needs %v, recent %v", sessionIDs(needs), sessionIDs(recent))
	}
	// The open session stays where it was when it was opened: read there,
	// or finishing a turn while it shows.
	read := make([]*model.Session, len(items))
	for i, s := range items {
		c := *s
		c.Unread = false
		read[i] = &c
	}
	if needs, _ := InboxGroups(read, OpenedSession{ID: "unread", StoodOut: true}); !slices.Equal(sessionIDs(needs), []string{"unread", "waiting", "failed"}) {
		t.Fatal(sessionIDs(needs))
	}
	if needs, _ := InboxGroups(items, OpenedSession{ID: "unread"}); !slices.Equal(sessionIDs(needs), []string{"waiting", "failed"}) {
		t.Fatal(sessionIDs(needs))
	}
	// But one that comes to need you moves up.
	if needs, _ := InboxGroups(items, OpenedSession{ID: "waiting"}); !slices.Equal(sessionIDs(needs), []string{"unread", "waiting", "failed"}) {
		t.Fatal(sessionIDs(needs))
	}
}

func TestMostPressing(t *testing.T) {
	bg := sess("background", model.SessionIdle, background)
	running := sess("running", model.SessionRunning, nil)
	failed := sess("failed", model.SessionError, nil)
	waiting := sess("waiting", model.SessionAwaitingApproval, nil)
	un := sess("unread", model.SessionIdle, unread)
	idle := []*model.Session{sess("idle", model.SessionIdle, nil), sess("stopped", model.SessionStopped, nil)}
	id := func(s *model.Session) string {
		if s == nil {
			return ""
		}
		return s.ID
	}
	cases := []struct {
		in   []*model.Session
		want string
	}{
		{nil, ""},
		{idle, ""},
		{append(slices.Clone(idle), bg), "background"},
		{append([]*model.Session{bg, running}, idle...), "running"},
		{[]*model.Session{running, failed}, "failed"},
		{[]*model.Session{failed, waiting, running}, "waiting"},
		// What ended since it was read comes after what asks for the user, before work.
		{append(slices.Clone(idle), un), "unread"},
		{[]*model.Session{bg, running, un}, "unread"},
		{[]*model.Session{un, failed}, "failed"},
		{[]*model.Session{waiting, un}, "waiting"},
	}
	for _, c := range cases {
		if got := id(MostPressing(c.in)); got != c.want {
			t.Errorf("%v: %q, want %q", sessionIDs(c.in), got, c.want)
		}
	}
}

func TestUrgencyOfUnreadSessions(t *testing.T) {
	cases := []struct {
		s    *model.Session
		want int
	}{
		{sess("s", model.SessionAwaitingApproval, unread), 5},
		{sess("s", model.SessionError, unread), 4},
		{sess("s", model.SessionIdle, unread), 3},
		{sess("s", model.SessionRunning, unread), 3},
		{sess("s", model.SessionStopped, unread), 3},
		{sess("s", model.SessionRunning, nil), 2},
		{sess("s", model.SessionIdle, background), 1},
		{sess("s", model.SessionIdle, nil), 0},
		{sess("s", model.SessionStopped, nil), 0},
	}
	for _, c := range cases {
		if got := Urgency(c.s); got != c.want {
			t.Errorf("%+v: %d, want %d", c.s, got, c.want)
		}
	}
}

func TestShortAge(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{20 * time.Second: "now", 4 * time.Minute: "4m", 3 * time.Hour: "3h", 48 * time.Hour: "2d"} {
		if got := ShortAge(now.Add(-d), now); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
	if got := ShortAge(now.Add(-30*24*time.Hour), now); strings.HasSuffix(got, "d") {
		t.Errorf("a month ago: %q", got)
	}
}

func TestRevealsASessionInItsProjectOrTheArchive(t *testing.T) {
	c := NewServerConnection(NewQueue(), nil, "", nil)
	c.Projects["p"] = &model.Project{ID: "p", Name: "p"}
	if got := RevealCollection(sess("s", model.SessionIdle, nil), c); got != (Collection{Kind: CollectionProject, ProjectID: "p"}) {
		t.Error(got)
	}
	if got := RevealCollection(sess("s", model.SessionIdle, func(s *model.Session) { s.Archived = true }), c); got.Kind != CollectionArchived {
		t.Error(got)
	}
	if got := RevealCollection(sess("s", model.SessionIdle, func(s *model.Session) { s.ProjectID = "forgotten" }), c); got.Kind != CollectionAll {
		t.Error(got)
	}
}

func TestOpensACollectionsSessionOpenedLast(t *testing.T) {
	items := []*model.Session{sess("a", model.SessionIdle, nil), sess("b", model.SessionIdle, nil), sess("c", model.SessionIdle, nil)}
	if s := SessionToOpen(items, nil); s.ID != "a" {
		t.Error(s.ID)
	}
	if s := SessionToOpen(items, []string{"x", "c", "b"}); s.ID != "c" {
		t.Error(s.ID)
	}
	if s := SessionToOpen(nil, []string{"c"}); s != nil {
		t.Error(s.ID)
	}
}

func TestKeepsTheSessionsOpenedLately(t *testing.T) {
	recent := AddingRecent(nil, "a")
	recent = AddingRecent(recent, "b")
	recent = AddingRecent(recent, "a")
	if !slices.Equal(recent, []string{"a", "b"}) {
		t.Fatal(recent)
	}
	for i := range 150 {
		recent = AddingRecent(recent, fmt.Sprint(i))
	}
	if len(recent) != 100 || recent[0] != "149" {
		t.Fatal(len(recent), recent[0])
	}
}

func TestProjectsInOrder(t *testing.T) {
	projects := []*model.Project{{ID: "a", Name: "a"}, {ID: "b", Name: "b"}, {ID: "c", Name: "c"}, {ID: "d", Name: "d"}}
	active := []*model.Session{{ID: "1", ProjectID: "c"}, {ID: "2", ProjectID: "a"}, {ID: "3", ProjectID: "c"}}
	var got []string
	for _, p := range ProjectsInOrder(projects, active) {
		got = append(got, p.ID)
	}
	if !slices.Equal(got, []string{"c", "a", "b", "d"}) {
		t.Fatal(got)
	}
}
