package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"remote-agent/internal/model"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "rad.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// recorder captures published events.
type recorder struct {
	mu  sync.Mutex
	evs []model.Event
}

func (r *recorder) publish(evs []model.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, evs...)
}

func (r *recorder) stream(name string) []model.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []model.Event
	for _, e := range r.evs {
		if e.Stream == name {
			out = append(out, e)
		}
	}
	return out
}

func write(t *testing.T, st *Store, f func(w *W) error) []model.Event {
	t.Helper()
	evs, err := st.Write(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func seqs(evs []model.Event) []int64 {
	out := make([]int64, len(evs))
	for i, e := range evs {
		out[i] = e.Seq
	}
	return out
}

func TestChangeFeedKeepsLatestEventPerEntity(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	sess := &model.Session{ID: "s1", ProjectID: "p1", Title: "one", Status: model.SessionIdle}
	it := &model.Item{ID: "i1", SessionID: "s1", Order: 1, Kind: model.ItemAssistantMessage, Status: model.ItemInProgress}
	write(t, st, func(w *W) error {
		if err := w.PutProject(&model.Project{ID: "p1", Path: "/p1", Name: "p1"}); err != nil {
			return err
		}
		return w.PutSession(sess)
	})
	// Stream an item: many upserts of the same entity.
	for _, text := range []string{"a", "ab", "abc"} {
		it.Text = text
		write(t, st, func(w *W) error { return w.PutItem(it) })
	}
	it.Status = model.ItemCompleted
	write(t, st, func(w *W) error { return w.PutItem(it) })
	sess.Title = "renamed"
	write(t, st, func(w *W) error { return w.PutSession(sess) })

	evs, last, err := st.Changes(ctx, model.SessionStream("s1"), 0)
	if err != nil {
		t.Fatal(err)
	}
	// Session stream: 1 session upsert + 4 item upserts + 1 session upsert = seq 6,
	// but only the latest event per entity is kept.
	if last != 6 {
		t.Errorf("last seq = %d, want 6", last)
	}
	if len(evs) != 2 {
		t.Fatalf("events = %+v, want one per entity", evs)
	}
	if e := evs[0]; e.Type != model.EvItemUpserted || e.Seq != 5 || e.Item.Text != "abc" || e.Item.Status != model.ItemCompleted {
		t.Errorf("item event = %+v %+v", e, e.Item)
	}
	if e := evs[1]; e.Type != model.EvSessionUpserted || e.Seq != 6 || e.Session.Title != "renamed" {
		t.Errorf("session event = %+v", e)
	}
	// The item keeps its creation time across upserts.
	if evs[0].Item.CreatedAt.IsZero() || evs[0].Item.CreatedAt.After(evs[0].Item.UpdatedAt) {
		t.Errorf("createdAt %v after updatedAt %v", evs[0].Item.CreatedAt, evs[0].Item.UpdatedAt)
	}

	idx, idxLast, err := st.Changes(ctx, model.IndexStream, 0)
	if err != nil {
		t.Fatal(err)
	}
	if idxLast != 3 || len(idx) != 2 || idx[0].Project == nil || idx[1].Session == nil || idx[1].Session.Title != "renamed" {
		t.Errorf("index = last %d, %+v", idxLast, idx)
	}

	items, err := st.Items(ctx, "s1")
	if err != nil || len(items) != 1 || items[0].Text != "abc" {
		t.Errorf("items = %+v, %v", items, err)
	}
}

func TestSeqIsGapFreePerStream(t *testing.T) {
	st := openTest(t)
	rec := &recorder{}
	st.OnCommit(rec.publish)

	for i := range 3 {
		s := &model.Session{ID: "s" + string(rune('a'+i)), ProjectID: "p"}
		write(t, st, func(w *W) error {
			if err := w.PutSession(s); err != nil {
				return err
			}
			for j := range 2 {
				if err := w.PutItem(&model.Item{ID: s.ID + "-" + string(rune('0'+j)), SessionID: s.ID, Order: int64(j + 1)}); err != nil {
					return err
				}
			}
			return w.PutTurn(&model.Turn{ID: s.ID + "-t", SessionID: s.ID, N: 1})
		})
	}
	// A failed write rolls back and must not consume seqs or publish anything.
	boom := errors.New("boom")
	_, err := st.Write(context.Background(), func(w *W) error {
		if err := w.PutSession(&model.Session{ID: "sa", ProjectID: "p"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	write(t, st, func(w *W) error { return w.PutSession(&model.Session{ID: "sa", ProjectID: "p", Title: "after"}) })

	check := func(stream string, want int) {
		t.Helper()
		got := seqs(rec.stream(stream))
		if len(got) != want {
			t.Fatalf("%s: %d events, want %d", stream, len(got), want)
		}
		for i, s := range got {
			if s != int64(i+1) {
				t.Fatalf("%s: seqs %v are not 1..%d", stream, got, want)
			}
		}
		if _, last, _ := st.Changes(context.Background(), stream, 0); last != int64(want) {
			t.Errorf("%s: last seq %d, want %d", stream, last, want)
		}
	}
	check(model.IndexStream, 4)         // 3 sessions + 1 update
	check(model.SessionStream("sa"), 5) // session, 2 items, turn, update
	check(model.SessionStream("sb"), 4) // session, 2 items, turn
	check(model.SessionStream("sc"), 4)

	// Events carry their stream, seq and a timestamp.
	for _, e := range rec.stream(model.SessionStream("sb")) {
		if e.TS.IsZero() || e.EntityKey() == "" {
			t.Errorf("event %+v lacks ts or entity", e)
		}
	}
}

func TestChangesAfter(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	stream := model.SessionStream("s1")
	put := func(id, text string) {
		write(t, st, func(w *W) error {
			return w.PutItem(&model.Item{ID: id, SessionID: "s1", Text: text})
		})
	}
	put("a", "a1") // seq 1
	put("b", "b1") // seq 2
	put("c", "c1") // seq 3
	put("a", "a2") // seq 4 (replaces a@1)
	put("d", "d1") // seq 5

	evs, last, err := st.Changes(ctx, stream, 2)
	if err != nil {
		t.Fatal(err)
	}
	if last != 5 {
		t.Errorf("last = %d", last)
	}
	var got []string
	for _, e := range evs {
		got = append(got, e.Item.Text)
	}
	// b@2 is not newer than 2; a's latest version is at 4. Replay is in seq order.
	if strings.Join(got, ",") != "c1,a2,d1" {
		t.Errorf("changes after 2 = %v", got)
	}
	if evs, last, _ := st.Changes(ctx, stream, 5); len(evs) != 0 || last != 5 {
		t.Errorf("changes after last = %v, %d", evs, last)
	}
	if evs, last, err := st.Changes(ctx, "session:nope", 0); err != nil || len(evs) != 0 || last != 0 {
		t.Errorf("unknown stream = %v, %d, %v", evs, last, err)
	}
	// A full replay is a snapshot: one event per entity.
	if evs, _, _ := st.Changes(ctx, stream, 0); len(evs) != 4 {
		t.Errorf("snapshot has %d events, want 4", len(evs))
	}
}

func TestRemovedEventsReplaceEntity(t *testing.T) {
	st := openTest(t)
	write(t, st, func(w *W) error { return w.PutProject(&model.Project{ID: "p1", Path: "/p1"}) })
	write(t, st, func(w *W) error { return w.DeleteProject("p1") })
	evs, last, err := st.Changes(context.Background(), model.IndexStream, 0)
	if err != nil {
		t.Fatal(err)
	}
	if last != 2 || len(evs) != 1 || evs[0].Type != model.EvProjectRemoved || evs[0].ID != "p1" {
		t.Errorf("index = %d %+v", last, evs)
	}
	if _, err := st.Project(context.Background(), "p1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("project lookup err = %v", err)
	}
	// Events without an entity are rejected.
	_, err = st.Write(context.Background(), func(w *W) error {
		return w.Emit(model.Event{Stream: model.IndexStream, Type: "bogus"})
	})
	if err == nil {
		t.Error("emit without entity succeeded")
	}
}

func TestReceipts(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	receipt := func(id string) (json.RawMessage, bool) {
		var res json.RawMessage
		var ok bool
		write(t, st, func(w *W) error {
			var err error
			res, ok, err = w.Receipt(id)
			return err
		})
		return res, ok
	}
	if _, ok := receipt("cmd-1"); ok {
		t.Fatal("receipt exists before it was stored")
	}
	write(t, st, func(w *W) error { return w.PutReceipt("cmd-1", map[string]string{"id": "x"}) })
	res, ok := receipt("cmd-1")
	if !ok || string(res) != `{"id":"x"}` {
		t.Errorf("receipt = %s, %v", res, ok)
	}
	// Commands without an id are not recorded.
	write(t, st, func(w *W) error { return w.PutReceipt("", "ignored") })
	if _, ok := receipt(""); ok {
		t.Error("empty command id has a receipt")
	}
	// A command id is applied at most once.
	if _, err := st.Write(ctx, func(w *W) error { return w.PutReceipt("cmd-1", "again") }); err == nil {
		t.Error("duplicate receipt accepted")
	}
	// A receipt written in a failed transaction does not stick.
	st.Write(ctx, func(w *W) error {
		w.PutReceipt("cmd-2", "x")
		return errors.New("rollback")
	})
	if _, ok := receipt("cmd-2"); ok {
		t.Error("receipt survived rollback")
	}
}

func TestPairingCodes(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()

	code, err := st.CreatePairingCode(ctx, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RedeemPairingCode(ctx, "wrong", "phone"); !errors.Is(err, ErrBadPairingCode) {
		t.Errorf("wrong code: %v", err)
	}
	d, token, err := st.RedeemPairingCode(ctx, code, "Sasha's iPhone")
	if err != nil {
		t.Fatal(err)
	}
	if d.ID == "" || d.Name != "Sasha's iPhone" || len(token) != 64 {
		t.Errorf("device = %+v, token %q", d, token)
	}
	// Single use.
	if _, _, err := st.RedeemPairingCode(ctx, code, "again"); !errors.Is(err, ErrBadPairingCode) {
		t.Errorf("second redeem: %v", err)
	}

	// Expired codes are rejected.
	old, err := st.CreatePairingCode(ctx, -2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RedeemPairingCode(ctx, old, "late"); !errors.Is(err, ErrBadPairingCode) {
		t.Errorf("expired code: %v", err)
	}

	// Default device name.
	code, _ = st.CreatePairingCode(ctx, time.Minute)
	d2, _, err := st.RedeemPairingCode(ctx, code, "")
	if err != nil || d2.Name != "device" {
		t.Errorf("unnamed device = %+v, %v", d2, err)
	}
}

func TestDeviceTokens(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	d1, tok1, err := st.AddDevice(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	d2, tok2, err := st.AddDevice(ctx, "two")
	if err != nil {
		t.Fatal(err)
	}
	if tok1 == tok2 {
		t.Fatal("tokens collide")
	}

	got, err := st.DeviceByToken(ctx, tok1)
	if err != nil || got.ID != d1.ID || got.Name != "one" {
		t.Errorf("auth = %+v, %v", got, err)
	}
	for _, bad := range []string{"", "nope", tok1 + "x"} {
		if _, err := st.DeviceByToken(ctx, bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("token %q: %v", bad, err)
		}
	}

	if err := st.TouchDevice(ctx, d1.ID); err != nil {
		t.Fatal(err)
	}
	devs, err := st.Devices(ctx)
	if err != nil || len(devs) != 2 {
		t.Fatalf("devices = %+v, %v", devs, err)
	}
	for _, d := range devs {
		if (d.ID == d1.ID) != (d.LastSeenAt != nil) {
			t.Errorf("device %s lastSeen = %v", d.Name, d.LastSeenAt)
		}
	}

	// Ids are UUIDv7, so devices paired together share a timestamp prefix:
	// an ambiguous prefix must not revoke both. LIKE wildcards are literal.
	common := 0
	for common < len(d1.ID) && d1.ID[common] == d2.ID[common] {
		common++
	}
	if common < 8 {
		t.Fatalf("ids %s and %s should share a timestamp prefix", d1.ID, d2.ID)
	}
	if _, err := st.DeleteDevice(ctx, d1.ID[:common]); !errors.Is(err, ErrAmbiguousPrefix) {
		t.Errorf("ambiguous prefix: %v", err)
	}
	for _, p := range []string{"____", "%%%%", "zzzz"} {
		if n, err := st.DeleteDevice(ctx, p); n != 0 || err != nil {
			t.Errorf("delete %q = %d, %v", p, n, err)
		}
	}
	if devs, _ := st.Devices(ctx); len(devs) != 2 {
		t.Fatalf("devices after refused deletes = %d", len(devs))
	}

	// Revoke by a unique prefix.
	n, err := st.DeleteDevice(ctx, d1.ID[:common+1])
	if err != nil || n != 1 {
		t.Fatalf("delete = %d, %v", n, err)
	}
	if _, err := st.DeviceByToken(ctx, tok1); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoked token still works: %v", err)
	}
	if ok, _ := st.DeviceExists(ctx, d1.ID); ok {
		t.Error("revoked device exists")
	}
	if ok, _ := st.DeviceExists(ctx, d2.ID); !ok {
		t.Error("other device was revoked too")
	}
	if _, err := st.DeviceByToken(ctx, tok2); err != nil {
		t.Errorf("other token: %v", err)
	}
}

func TestServerIDIsStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rad.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id1, err := st.ServerID(context.Background())
	if err != nil || id1 == "" {
		t.Fatalf("server id = %q, %v", id1, err)
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if id2, _ := st.ServerID(context.Background()); id2 != id1 {
		t.Errorf("server id changed: %s -> %s", id1, id2)
	}
}

func TestItemsWithStatus(t *testing.T) {
	st := openTest(t)
	write(t, st, func(w *W) error {
		for _, it := range []*model.Item{
			{ID: "a", SessionID: "s1", Order: 1, Kind: model.ItemApproval, Status: model.ItemPending},
			{ID: "b", SessionID: "s1", Order: 2, Kind: model.ItemToolCall, Status: model.ItemPending},
			{ID: "c", SessionID: "s2", Order: 1, Kind: model.ItemApproval, Status: model.ItemResolved},
			{ID: "d", SessionID: "s2", Order: 2, Kind: model.ItemAssistantMessage, Status: model.ItemInProgress},
		} {
			if err := w.PutItem(it); err != nil {
				return err
			}
		}
		return nil
	})
	ids := func(kind model.ItemKind, st2 model.ItemStatus) string {
		items, err := st.ItemsWithStatus(context.Background(), kind, st2)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, it := range items {
			out = append(out, it.ID)
		}
		return strings.Join(out, ",")
	}
	if got := ids(model.ItemApproval, model.ItemPending); got != "a" {
		t.Errorf("pending approvals = %s", got)
	}
	if got := ids("", model.ItemPending); got != "a,b" && got != "b,a" {
		t.Errorf("pending items = %s", got)
	}
	if got := ids("", model.ItemInProgress); got != "d" {
		t.Errorf("in progress = %s", got)
	}
}
