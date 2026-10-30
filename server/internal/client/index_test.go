package client

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"remote-agent/internal/model"
)

// As web/src/store/sessionAlert.test.ts, and SessionAlertTests and
// IndexChangeTests in apple/RAKit's StoreTests.swift.

func indexSession(id string, status model.SessionStatus, archived bool, errText string, seq int64) *model.Session {
	return &model.Session{
		ID: id, ProjectID: "p", Harness: "fake", Status: status, Title: id, Archived: archived, Error: errText,
		Workspace: model.Workspace{Kind: model.WorkspaceRoot, Path: "/tmp/p"},
		CreatedAt: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 10, 9, 12, 0, int(seq%60), 0, time.UTC),
	}
}

func indexEvent(seq int64, id string, status model.SessionStatus, errText string) model.Event {
	return model.Event{Stream: "index", Seq: seq, Type: model.EvSessionUpserted, Session: indexSession(id, status, false, errText, seq)}
}

func TestAlertFollowsStatusChanges(t *testing.T) {
	alert := func(from, to model.SessionStatus, archived bool) SessionAlert {
		errText := ""
		if to == model.SessionError {
			errText = "boom"
		}
		return Alert(indexSession("s", from, false, "", 1), indexSession("s", to, archived, errText, 2))
	}
	cases := []struct {
		from, to model.SessionStatus
		archived bool
		want     SessionAlert
	}{
		{model.SessionIdle, model.SessionRunning, false, SessionAlert{}},
		{model.SessionRunning, model.SessionAwaitingApproval, false, SessionAlert{Kind: AlertNeedsYou}},
		{model.SessionIdle, model.SessionAwaitingApproval, false, SessionAlert{Kind: AlertNeedsYou}},
		{model.SessionAwaitingApproval, model.SessionRunning, false, SessionAlert{}},
		{model.SessionRunning, model.SessionIdle, false, SessionAlert{Kind: AlertFinished}},
		// Answered elsewhere and done, or interrupted: nothing to tell.
		{model.SessionAwaitingApproval, model.SessionIdle, false, SessionAlert{}},
		{model.SessionRunning, model.SessionError, false, SessionAlert{Kind: AlertFailed, Error: "boom"}},
		{model.SessionIdle, model.SessionError, false, SessionAlert{Kind: AlertFailed, Error: "boom"}},
		{model.SessionRunning, model.SessionRunning, false, SessionAlert{}},
		{model.SessionRunning, model.SessionIdle, true, SessionAlert{}},
		{model.SessionRunning, model.SessionStopped, false, SessionAlert{}},
	}
	for _, c := range cases {
		if got := alert(c.from, c.to, c.archived); got != c.want {
			t.Errorf("%s → %s (archived %v): %+v, want %+v", c.from, c.to, c.archived, got, c.want)
		}
	}
}

func alertingConnection(t *testing.T) (*ServerConnection, *[]string) {
	c := NewServerConnection(NewQueue(), []string{"http://127.0.0.1:1"}, "", nil)
	var seen []string
	c.OnSessionChange = func(old, next *model.Session) {
		// Told once the batch is applied.
		if c.Sessions[next.ID] != next {
			t.Errorf("%s told before the batch was applied", next.ID)
		}
		switch a := Alert(old, next); a.Kind {
		case AlertNeedsYou:
			seen = append(seen, next.ID+" needsYou")
		case AlertFinished:
			seen = append(seen, next.ID+" finished")
		case AlertFailed:
			seen = append(seen, fmt.Sprintf("%s failed %s", next.ID, a.Error))
		}
	}
	return c, &seen
}

func indexEvents(evs ...model.Event) ServerMessage {
	return ServerMessage{Kind: MessageEvents, Stream: "index", Events: evs}
}

func synced(seq int64, reset bool) ServerMessage {
	return ServerMessage{Kind: MessageSynchronized, Stream: "index", Seq: seq, Reset: reset}
}

var closed = ServerMessage{Kind: MessageClosed}

func TestSessionChangeIsNotToldForTheFirstSync(t *testing.T) {
	c, seen := alertingConnection(t)
	c.handle(indexEvents(indexEvent(3, "a", model.SessionRunning, "")))
	c.handle(synced(3, true))
	if len(*seen) != 0 {
		t.Fatal(*seen)
	}
	c.handle(indexEvents(indexEvent(4, "a", model.SessionAwaitingApproval, ""), indexEvent(5, "b", model.SessionRunning, "")))
	if !slices.Equal(*seen, []string{"a needsYou"}) {
		t.Fatal(*seen)
	}
}

func TestSessionChangeCoversWhatChangedWhileAway(t *testing.T) {
	c, seen := alertingConnection(t)
	c.handle(indexEvents(indexEvent(2, "a", model.SessionRunning, ""), indexEvent(3, "b", model.SessionRunning, "")))
	c.handle(synced(3, true))
	// Back after a sleep: the replay holds what changed meanwhile.
	c.handle(closed)
	c.handle(indexEvents(indexEvent(7, "a", model.SessionIdle, "")))
	c.handle(synced(7, false))
	if !slices.Equal(*seen, []string{"a finished"}) {
		t.Fatal(*seen)
	}
	// rad lost its place: a reset, compared with what was cached.
	c.handle(closed)
	c.handle(indexEvents(indexEvent(1, "a", model.SessionIdle, ""), indexEvent(2, "b", model.SessionError, "boom")))
	c.handle(synced(2, true))
	if !slices.Equal(*seen, []string{"a finished", "b failed boom"}) {
		t.Fatal(*seen)
	}
}

func TestSessionChangeIsToldOnceForARepeatedReplay(t *testing.T) {
	c, seen := alertingConnection(t)
	c.handle(indexEvents(indexEvent(2, "a", model.SessionRunning, "")))
	c.handle(synced(2, true))
	c.handle(closed)
	replay := indexEvent(5, "a", model.SessionAwaitingApproval, "")
	c.handle(indexEvents(replay, replay))
	c.handle(synced(5, false))
	if !slices.Equal(*seen, []string{"a needsYou"}) {
		t.Fatal(*seen)
	}
}

func TestIndexResubscribesAfterAGap(t *testing.T) {
	c, _ := alertingConnection(t)
	c.handle(indexEvents(indexEvent(1, "a", model.SessionRunning, "")))
	c.handle(synced(1, true))
	if !c.indexLive {
		t.Fatal("not live")
	}
	c.handle(indexEvents(indexEvent(3, "a", model.SessionIdle, "")))
	if c.indexLive || c.Sessions["a"].Status != model.SessionRunning || c.indexOutstanding != 1 {
		t.Fatalf("live %v, status %s, outstanding %d", c.indexLive, c.Sessions["a"].Status, c.indexOutstanding)
	}
}

func TestStoresGetTheirSessionFromTheIndex(t *testing.T) {
	c, _ := alertingConnection(t)
	s := c.SessionStore("a")
	c.handle(indexEvents(indexEvent(1, "a", model.SessionRunning, "")))
	c.handle(synced(1, true))
	if s.Session == nil || s.Session.Status != model.SessionRunning {
		t.Fatalf("store's session %+v", s.Session)
	}
}
