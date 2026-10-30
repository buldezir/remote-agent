package client

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"remote-agent/internal/model"
)

// As web/src/store/sessionStore.test.ts and apple/RAKit's StoreTests.swift.

func itemEvent(seq int64, id string, order int64, text string, kind model.ItemKind, parent string) model.Event {
	return model.Event{Stream: "session:s", Seq: seq, Type: model.EvItemUpserted, Item: &model.Item{
		ID: id, SessionID: "s", Order: order, Kind: kind, Status: model.ItemCompleted, Text: text, ParentItemID: parent,
	}}
}

type call struct {
	method string
	params any
}

type fakeHost struct {
	subscribed int
	activated  int
	deactived  int
	calls      []call
	err        error
	posted     []func()
}

func (h *fakeHost) callTimeout(_ context.Context, method string, params, _ any, _ time.Duration) error {
	h.calls = append(h.calls, call{method, params})
	return h.err
}

func (h *fakeHost) deliver(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	return h.callTimeout(ctx, method, params, out, timeout)
}

func (h *fakeHost) activate(*SessionStore)   { h.activated++ }
func (h *fakeHost) deactivate(*SessionStore) { h.deactived++ }
func (h *fakeHost) subscribe(*SessionStore)  { h.subscribed++ }
func (h *fakeHost) post(f func())            { f() }
func (h *fakeHost) drafts() *Drafts          { return nil }

func ids(items []*model.Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func TestSessionStoreReplaysThenAppliesLive(t *testing.T) {
	s := newSessionStore("s", nil)
	// Replay (seqs need not follow one another before synchronized).
	s.apply([]model.Event{itemEvent(3, "b", 2, "second", model.ItemAssistantMessage, ""), itemEvent(7, "a", 1, "first", model.ItemAssistantMessage, "")})
	if len(s.Transcript) != 0 {
		t.Fatal("rebuilt before synchronized")
	}
	s.synchronized(7, true)
	if got := ids(s.Transcript); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("transcript %v", got)
	}
	s.apply([]model.Event{itemEvent(8, "b", 2, "second, edited", model.ItemAssistantMessage, "")})
	if s.Transcript[1].Text != "second, edited" {
		t.Fatal(s.Transcript[1].Text)
	}
	// A seq seen already is ignored.
	s.apply([]model.Event{itemEvent(8, "b", 2, "stale", model.ItemAssistantMessage, "")})
	if s.Transcript[1].Text != "second, edited" {
		t.Fatal(s.Transcript[1].Text)
	}
}

func TestSessionStoreResetKeepsTheReplay(t *testing.T) {
	s := newSessionStore("s", nil)
	s.synchronized(1, true)
	s.apply([]model.Event{itemEvent(2, "old", 1, "old", model.ItemAssistantMessage, "")})
	// Subscribed again from scratch: rad replays only what exists now.
	s.beginReplay()
	s.apply([]model.Event{itemEvent(9, "new", 1, "new", model.ItemAssistantMessage, "")})
	s.synchronized(9, true)
	if got := ids(s.Transcript); !slices.Equal(got, []string{"new"}) {
		t.Fatalf("transcript %v", got)
	}
}

func TestSessionStoreKeepsTheLastReplayWhenSubscriptionsOverlap(t *testing.T) {
	s := newSessionStore("s", nil)
	// A view subscribes while the connection resubscribes everything.
	s.beginReplay()
	s.beginReplay()
	replay := []model.Event{itemEvent(3, "b", 2, "second", model.ItemAssistantMessage, ""), itemEvent(7, "a", 1, "first", model.ItemAssistantMessage, "")}
	s.apply(replay)
	s.synchronized(7, true)
	if s.Live {
		t.Fatal("live before the second subscription synchronized")
	}
	s.apply(replay)
	s.synchronized(7, true)
	if !s.Live || !slices.Equal(ids(s.Transcript), []string{"a", "b"}) {
		t.Fatalf("live %v, transcript %v", s.Live, ids(s.Transcript))
	}
}

func TestSessionStoreResubscribesAfterAGap(t *testing.T) {
	h := &fakeHost{}
	s := newSessionStore("s", h)
	s.synchronized(1, true)
	s.apply([]model.Event{itemEvent(5, "x", 1, "after a gap", model.ItemAssistantMessage, "")})
	if s.Items["x"] != nil || s.Live || h.subscribed != 1 {
		t.Fatalf("item %v, live %v, subscribed %d", s.Items["x"], s.Live, h.subscribed)
	}
}

func TestSessionStoreEmptiesWhenTheSessionIsDeleted(t *testing.T) {
	s := newSessionStore("s", &fakeHost{})
	s.synchronized(1, true)
	s.setSession(&model.Session{ID: "s"})
	s.apply([]model.Event{itemEvent(2, "a", 1, "first", model.ItemAssistantMessage, "")})
	s.Send("pending", nil)
	s.apply([]model.Event{{Stream: "session:s", Seq: 3, Type: model.EvSessionRemoved, ID: "s"}})
	if !s.Removed || s.Session != nil || len(s.Transcript) != 0 || len(s.Items) != 0 || len(s.Turns) != 0 || len(s.Outbox) != 0 {
		t.Fatalf("not emptied: %+v", s)
	}
}

func TestSessionStoreIsRemovedWhenALaterSubscriptionReplaysTheDeletion(t *testing.T) {
	s := newSessionStore("s", nil)
	s.synchronized(2, true)
	s.apply([]model.Event{itemEvent(3, "a", 1, "first", model.ItemAssistantMessage, "")})
	// Away while it was deleted: the stream holds only the removal now.
	s.beginReplay()
	s.apply([]model.Event{{Stream: "session:s", Seq: 4, Type: model.EvSessionRemoved, ID: "s"}})
	s.synchronized(4, true)
	if !s.Removed || len(s.Transcript) != 0 {
		t.Fatal("not removed")
	}
}

func TestSessionStoreNestsChildrenUnderTheirParent(t *testing.T) {
	s := newSessionStore("s", nil)
	s.apply([]model.Event{itemEvent(1, "tool", 1, "", model.ItemNotice, ""), itemEvent(2, "child", 2, "sub", model.ItemAssistantMessage, "tool"), itemEvent(3, "orphan", 3, "", model.ItemNotice, "gone")})
	s.synchronized(3, true)
	if got := ids(s.Transcript); !slices.Equal(got, []string{"tool", "orphan"}) {
		t.Fatalf("transcript %v", got)
	}
	if got := ids(s.ChildrenOf(s.Transcript[0])); !slices.Equal(got, []string{"child"}) {
		t.Fatalf("children %v", got)
	}
}

func TestSessionStoreStaysActiveWhileAnyViewShowsIt(t *testing.T) {
	h := &fakeHost{}
	s := newSessionStore("s", h)
	s.Activate()
	s.Activate()
	s.Deactivate()
	if !s.IsActive() {
		t.Fatal("inactive with a view left")
	}
	s.Deactivate()
	if s.IsActive() {
		t.Fatal("active without views")
	}
	s.Deactivate()
	s.Activate()
	if !s.IsActive() || h.activated != 2 || h.deactived != 1 {
		t.Fatalf("active %v, activated %d, deactivated %d", s.IsActive(), h.activated, h.deactived)
	}
}

func TestSessionStoreKeepsAPromptInTheOutboxUntilConfirmed(t *testing.T) {
	h := &fakeHost{}
	s := newSessionStore("s", h)
	s.synchronized(0, true)
	if err := s.Send("hello", nil)(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.Outbox) != 1 || s.Outbox[0].Text != "hello" || h.calls[0].method != "session.prompt" {
		t.Fatalf("outbox %+v, calls %+v", s.Outbox, h.calls)
	}
	s.apply([]model.Event{itemEvent(1, "u", 1, "hello", model.ItemUserMessage, "")})
	if len(s.Outbox) != 0 {
		t.Fatal("outbox kept a confirmed prompt")
	}
}

func TestSessionStoreDropsTheOutboxEntryWhenSendingFails(t *testing.T) {
	h := &fakeHost{err: errors.New("nope")}
	s := newSessionStore("s", h)
	if err := s.Send("hello", nil)(context.Background()); err == nil || len(s.Outbox) != 0 {
		t.Fatalf("err %v, outbox %+v", err, s.Outbox)
	}
}

func TestSessionStoreSendsTheDraftThroughTheOutbox(t *testing.T) {
	h := &fakeHost{}
	s := newSessionStore("s", h)
	s.synchronized(0, true)
	image := model.ImageRef{ID: "img.png", MimeType: "image/png", Size: 1}
	s.setSession(&model.Session{ID: "s", Draft: &model.Draft{Text: "later", Images: []model.ImageRef{image}}})
	if d := s.UnsentDraft(); d == nil || d.Text != "later" {
		t.Fatalf("draft %+v", d)
	}
	job, err := s.SendDraft()
	if err != nil {
		t.Fatal(err)
	}
	if err := job(context.Background()); err != nil {
		t.Fatal(err)
	}
	// On its way: in the outbox, as a prompt sent, and no longer a draft to show.
	if len(s.Outbox) != 1 || s.Outbox[0].Text != "later" || s.UnsentDraft() != nil {
		t.Fatalf("outbox %+v, draft %+v", s.Outbox, s.UnsentDraft())
	}
	if len(h.calls) != 1 || h.calls[0].method != "session.sendDraft" || h.calls[0].params.(map[string]any)["commandId"] == "" {
		t.Fatalf("calls %+v", h.calls)
	}
	// The user message comes with the session without its draft.
	msg := itemEvent(2, "u", 1, "later", model.ItemUserMessage, "")
	msg.Item.Images = []model.ImageRef{image}
	s.apply([]model.Event{{Stream: "session:s", Seq: 1, Type: model.EvSessionUpserted, Session: &model.Session{ID: "s"}}, msg})
	if len(s.Outbox) != 0 || s.UnsentDraft() != nil || s.Transcript[0].Text != "later" {
		t.Fatalf("outbox %+v, transcript %v", s.Outbox, ids(s.Transcript))
	}
}

func TestSessionStoreKeepsTheDraftWhenSendingItFails(t *testing.T) {
	h := &fakeHost{err: errors.New("nope")}
	s := newSessionStore("s", h)
	s.setSession(&model.Session{ID: "s", Draft: &model.Draft{Text: "later"}})
	job, _ := s.SendDraft()
	if job(context.Background()) == nil || len(s.Outbox) != 0 || s.UnsentDraft() == nil {
		t.Fatal("draft lost")
	}
}

func TestSessionStoreHasNoDraftToSendWithoutOne(t *testing.T) {
	h := &fakeHost{}
	s := newSessionStore("s", h)
	s.setSession(&model.Session{ID: "s"})
	if _, err := s.SendDraft(); ErrorCode(err) != "conflict" || len(h.calls) != 0 || len(s.Outbox) != 0 {
		t.Fatalf("err %v", err)
	}
}

func TestSessionStoreSendsAQueuedPromptNow(t *testing.T) {
	h := &fakeHost{}
	s := newSessionStore("s", h)
	if err := s.SendNow(context.Background(), "u"); err != nil || h.calls[0].method != "session.sendNow" {
		t.Fatalf("err %v, calls %+v", err, h.calls)
	}
	h.err = &RPCError{"method_not_found", "unknown method session.sendNow"}
	if err := s.SendNow(context.Background(), "u"); err == nil || err.Error() != "This rad is too old to send a queued prompt now. Update rad." {
		t.Fatalf("err %v", err)
	}
	h.err = &RPCError{"not_found", "no such item"}
	if err := s.SendNow(context.Background(), "u"); err == nil || err.Error() != "no such item" {
		t.Fatalf("err %v", err)
	}
}

func TestSessionStoreCancelsAQueuedPrompt(t *testing.T) {
	h := &fakeHost{}
	s := newSessionStore("s", h)
	if err := s.CancelQueued(context.Background(), "u"); err != nil || h.calls[0].method != "session.cancelQueued" {
		t.Fatalf("err %v, calls %+v", err, h.calls)
	}
	h.err = &RPCError{"method_not_found", "unknown method session.cancelQueued"}
	if err := s.CancelQueued(context.Background(), "u"); err == nil || err.Error() != "This rad is too old to cancel a queued prompt. Update rad." {
		t.Fatalf("err %v", err)
	}
	h.err = &RPCError{"not_found", "no such item"}
	if err := s.CancelQueued(context.Background(), "u"); err == nil || err.Error() != "no such item" {
		t.Fatalf("err %v", err)
	}
}

func TestSessionStoreCommandsFallBackOnOldServers(t *testing.T) {
	h := &fakeHost{err: &RPCError{"method_not_found", "old server"}}
	s := newSessionStore("s", h)
	if cmds, err := s.Commands(context.Background()); err != nil || cmds != nil {
		t.Fatalf("cmds %v, err %v", cmds, err)
	}
	h.err = &RPCError{"conflict", "runtime replaced"}
	if _, err := s.Commands(context.Background()); ErrorCode(err) != "conflict" {
		t.Fatalf("err %v", err)
	}
}
