// Package events fans committed store events out to live subscribers.
package events

import (
	"sync"

	"remote-agent/internal/model"
)

// Sub receives live events for one stream. If the subscriber falls behind its
// buffer, the hub closes C and sets Overflowed; the client must resubscribe.
type Sub struct {
	Stream     string
	C          chan model.Event
	hub        *Hub
	overflowed bool
	closed     bool
}

func (s *Sub) Overflowed() bool {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	return s.overflowed
}

func (s *Sub) Close() {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	s.hub.removeLocked(s)
}

type Hub struct {
	mu   sync.Mutex
	subs map[string]map[*Sub]struct{}
}

func NewHub() *Hub { return &Hub{subs: map[string]map[*Sub]struct{}{}} }

const subBuffer = 1024

func (h *Hub) Subscribe(stream string) *Sub {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := &Sub{Stream: stream, C: make(chan model.Event, subBuffer), hub: h}
	if h.subs[stream] == nil {
		h.subs[stream] = map[*Sub]struct{}{}
	}
	h.subs[stream][s] = struct{}{}
	return s
}

// Publish delivers events without blocking; slow subscribers are dropped.
func (h *Hub) Publish(evs []model.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ev := range evs {
		for s := range h.subs[ev.Stream] {
			select {
			case s.C <- ev:
			default:
				s.overflowed = true
				h.removeLocked(s)
			}
		}
	}
}

func (h *Hub) removeLocked(s *Sub) {
	if s.closed {
		return
	}
	s.closed = true
	delete(h.subs[s.Stream], s)
	if len(h.subs[s.Stream]) == 0 {
		delete(h.subs, s.Stream)
	}
	close(s.C)
}
