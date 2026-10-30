// Package client is ra's side of rad's protocol: the connection, the index
// and the session stores, as RAKit has them for the apps
// (web/src/protocol and web/src/store port it too, with the same names).
//
// State changes on one goroutine only, the one the app gives it as a
// Dispatcher, as the apps change theirs on the main thread. Network
// goroutines do their I/O and post what they found. Blocking calls (Call,
// Deliver, the jobs commands return) are safe from any goroutine; the app
// runs them off its dispatcher.
package client

import "sync"

// Dispatcher runs functions, in the order posted, on the goroutine that
// owns the client's state. Post must not block, and may be called from that
// goroutine too.
type Dispatcher interface {
	Post(f func())
}

// Queue is a Dispatcher whose owner drains it: it keeps what is posted
// until Drain runs it, and says so on Ready. ra drains it from Bubble Tea's
// Update loop, tests from the test goroutine.
type Queue struct {
	mu    sync.Mutex
	fns   []func()
	ready chan struct{}
}

func NewQueue() *Queue {
	return &Queue{ready: make(chan struct{}, 1)}
}

func (q *Queue) Post(f func()) {
	q.mu.Lock()
	q.fns = append(q.fns, f)
	q.mu.Unlock()
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

// Ready receives a value after a Post, once for any number of them.
func (q *Queue) Ready() <-chan struct{} { return q.ready }

// Drain runs what was posted, including what those functions post, and
// reports whether there was anything.
func (q *Queue) Drain() bool {
	ran := false
	for {
		q.mu.Lock()
		fns := q.fns
		q.fns = nil
		q.mu.Unlock()
		if len(fns) == 0 {
			return ran
		}
		ran = true
		for _, f := range fns {
			f()
		}
	}
}
