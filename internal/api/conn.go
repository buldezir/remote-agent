package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"remote-agent/internal/events"
	"remote-agent/internal/model"
	"remote-agent/internal/orchestrator"
	"remote-agent/internal/store"
)

// Frames.

type request struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type response struct {
	ID     json.RawMessage `json:"id"`
	Result any             `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type notification struct {
	Method string `json:"method"`
	Params any    `json:"params"`
}

type rpcError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type eventsParams struct {
	Stream string        `json:"stream"`
	Events []model.Event `json:"events"`
}

type syncParams struct {
	Stream string `json:"stream"`
	Seq    int64  `json:"seq"`
	Reset  bool   `json:"reset,omitempty"` // client must drop its cached state for the stream
}

type streamParams struct {
	Stream string `json:"stream"`
}

const (
	writeTimeout   = 15 * time.Second
	pingInterval   = 25 * time.Second
	revalidateEach = 30 * time.Second
	replayBatch    = 200
)

type conn struct {
	s      *Server
	ws     *websocket.Conn
	device store.Device
	out    chan []byte
	ctx    context.Context
	cancel context.CancelFunc

	mu   sync.Mutex
	subs map[string]*events.Sub
}

func newConn(s *Server, ws *websocket.Conn, d store.Device) *conn {
	return &conn{s: s, ws: ws, device: d, out: make(chan []byte, 256), subs: map[string]*events.Sub{}}
}

func (c *conn) run(parent context.Context) {
	c.ctx, c.cancel = context.WithCancel(parent)
	defer c.cancel()
	go c.writer()
	go c.keepalive()
	defer c.unsubscribeAll()
	for {
		_, data, err := c.ws.Read(c.ctx)
		if err != nil {
			return
		}
		var req request
		if err := json.Unmarshal(data, &req); err != nil || req.Method == "" {
			c.reply(req.ID, nil, &rpcError{Code: "invalid", Message: "malformed request"})
			continue
		}
		go c.handle(req)
	}
}

func (c *conn) writer() {
	defer c.ws.CloseNow()
	for {
		select {
		case <-c.ctx.Done():
			c.ws.Close(websocket.StatusNormalClosure, "")
			return
		case b := <-c.out:
			ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
			err := c.ws.Write(ctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		}
	}
}

func (c *conn) keepalive() {
	ping := time.NewTicker(pingInterval)
	check := time.NewTicker(revalidateEach)
	defer ping.Stop()
	defer check.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ping.C:
			ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
			err := c.ws.Ping(ctx)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		case <-check.C:
			if ok, err := c.s.st.DeviceExists(c.ctx, c.device.ID); err == nil && !ok {
				c.ws.Close(4001, "device revoked")
				c.cancel()
				return
			}
		}
	}
}

func (c *conn) send(v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		c.s.log.Error("marshal frame", "err", err)
		return false
	}
	select {
	case c.out <- b:
		return true
	case <-c.ctx.Done():
		return false
	}
}

func (c *conn) reply(id json.RawMessage, result any, e *rpcError) {
	if len(id) == 0 {
		return
	}
	if e == nil && result == nil {
		result = struct{}{}
	}
	c.send(response{ID: id, Result: result, Error: e})
}

func (c *conn) handle(req request) {
	res, err := c.dispatch(req.Method, req.Params)
	if err != nil {
		code := orchestrator.CodeOf(err)
		var me *methodError
		if errors.As(err, &me) {
			code = me.code
		}
		if code == "internal" {
			c.s.log.Error("request failed", "method", req.Method, "err", err)
		}
		c.reply(req.ID, nil, &rpcError{Code: code, Message: err.Error()})
		return
	}
	c.reply(req.ID, res, nil)
}

type methodError struct {
	code string
	msg  string
}

func (e *methodError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return &methodError{"invalid", fmt.Sprintf(format, args...)}
}

// Subscriptions.

type subscribeParams struct {
	Stream   string `json:"stream"`
	AfterSeq int64  `json:"afterSeq"`
}

type subscribeResult struct {
	Stream string `json:"stream"`
	Seq    int64  `json:"seq"`
}

func (c *conn) subscribe(p subscribeParams) (*subscribeResult, error) {
	if p.Stream != model.IndexStream && !strings.HasPrefix(p.Stream, "session:") {
		return nil, invalid("unknown stream %q", p.Stream)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old := c.subs[p.Stream]; old != nil {
		old.Close()
	}
	sub := c.s.hub.Subscribe(p.Stream)
	c.subs[p.Stream] = sub

	evs, last, err := c.s.st.Changes(c.ctx, p.Stream, p.AfterSeq)
	if err != nil {
		sub.Close()
		delete(c.subs, p.Stream)
		return nil, err
	}
	reset := p.AfterSeq == 0
	if p.AfterSeq > last {
		// Client is ahead of us (e.g. the database was reset): resend everything.
		if evs, last, err = c.s.st.Changes(c.ctx, p.Stream, 0); err != nil {
			return nil, err
		}
		reset = true
	}
	for len(evs) > 0 {
		n := min(len(evs), replayBatch)
		c.send(notification{"events", eventsParams{p.Stream, evs[:n]}})
		evs = evs[n:]
	}
	c.send(notification{"synchronized", syncParams{p.Stream, last, reset}})
	go c.forward(sub, last)
	return &subscribeResult{Stream: p.Stream, Seq: last}, nil
}

// forward relays live events after seq, batching what is already queued.
func (c *conn) forward(sub *events.Sub, after int64) {
	for {
		ev, ok := <-sub.C
		if !ok {
			if sub.Overflowed() {
				c.send(notification{"resync", streamParams{sub.Stream}})
			}
			return
		}
		batch := []model.Event{}
		if ev.Seq > after {
			batch = append(batch, ev)
		}
	drain:
		for len(batch) < replayBatch {
			select {
			case ev, ok = <-sub.C:
				if !ok {
					break drain
				}
				if ev.Seq > after {
					batch = append(batch, ev)
				}
			default:
				break drain
			}
		}
		if len(batch) > 0 {
			after = batch[len(batch)-1].Seq
			if !c.send(notification{"events", eventsParams{sub.Stream, batch}}) {
				return
			}
		}
		if !ok {
			if sub.Overflowed() {
				c.send(notification{"resync", streamParams{sub.Stream}})
			}
			return
		}
	}
}

func (c *conn) unsubscribe(stream string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sub := c.subs[stream]; sub != nil {
		sub.Close()
		delete(c.subs, stream)
	}
}

func (c *conn) unsubscribeAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, sub := range c.subs {
		sub.Close()
		delete(c.subs, k)
	}
}
