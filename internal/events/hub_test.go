package events

import (
	"testing"

	"remote-agent/internal/model"
)

func ev(stream string, seq int64) model.Event {
	return model.Event{Stream: stream, Seq: seq, Type: model.EvItemUpserted, Item: &model.Item{ID: "i"}}
}

func TestHubRoutesByStream(t *testing.T) {
	h := NewHub()
	a, b := h.Subscribe("session:a"), h.Subscribe("session:b")
	a2 := h.Subscribe("session:a")
	h.Publish([]model.Event{ev("session:a", 1), ev("session:b", 1), ev("session:a", 2)})

	for _, s := range []*Sub{a, a2} {
		if got := []int64{(<-s.C).Seq, (<-s.C).Seq}; got[0] != 1 || got[1] != 2 {
			t.Errorf("session:a got %v", got)
		}
	}
	if e := <-b.C; e.Stream != "session:b" || len(b.C) != 0 {
		t.Errorf("session:b got %+v (+%d)", e, len(b.C))
	}

	a.Close()
	a.Close() // idempotent
	if _, ok := <-a.C; ok {
		t.Error("closed sub still open")
	}
	h.Publish([]model.Event{ev("session:a", 3)})
	if e := <-a2.C; e.Seq != 3 {
		t.Errorf("remaining sub got %+v", e)
	}
	if a.Overflowed() {
		t.Error("closed sub reports overflow")
	}
}

func TestHubDropsSlowSubscribers(t *testing.T) {
	h := NewHub()
	slow, other := h.Subscribe("index"), h.Subscribe("session:x")
	evs := make([]model.Event, subBuffer+1)
	for i := range evs {
		evs[i] = ev("index", int64(i+1))
	}
	h.Publish(evs) // never blocks
	if !slow.Overflowed() {
		t.Fatal("overflow not reported")
	}
	n := 0
	for range slow.C {
		n++
	}
	if n != subBuffer {
		t.Errorf("buffered %d events before closing, want %d", n, subBuffer)
	}
	h.Publish([]model.Event{ev("session:x", 1)})
	if e := <-other.C; e.Seq != 1 || other.Overflowed() {
		t.Error("other subscriber affected by overflow")
	}
	slow.Close() // safe after the hub dropped it
}
