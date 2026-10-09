// Package jsonrpc is a minimal bidirectional JSON-RPC client over a proc's
// newline-delimited stdio. It tolerates peers that omit the "jsonrpc" field.
package jsonrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"remote-agent/internal/harness/proc"
)

type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s (code %d)", e.Message, e.Code) }

type message struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type Handler struct {
	// Notify handles server notifications. Called sequentially from the read loop.
	Notify func(method string, params json.RawMessage)
	// Request handles server→client requests; reply with Conn.Reply or Conn.ReplyError.
	// Called sequentially from the read loop, so it must not block on the peer.
	Request func(id json.RawMessage, method string, params json.RawMessage)
}

type Conn struct {
	p       *proc.Proc
	h       Handler
	version string // "2.0" to send the jsonrpc field, "" to omit it

	mu      sync.Mutex
	next    int64
	pending map[string]chan message
	done    chan struct{}
}

func New(p *proc.Proc, version string, h Handler) *Conn {
	return &Conn{p: p, h: h, version: version, pending: map[string]chan message{}, done: make(chan struct{})}
}

// Done is closed when the read loop ends (process stdout closed).
func (c *Conn) Done() <-chan struct{} { return c.done }

var ErrClosed = errors.New("connection closed")

func (c *Conn) Call(ctx context.Context, method string, params any, out any) error {
	c.mu.Lock()
	c.next++
	id := json.RawMessage(fmt.Sprint(c.next))
	ch := make(chan message, 1)
	c.pending[string(id)] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, string(id))
		c.mu.Unlock()
	}()
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if err := c.p.WriteJSON(message{JSONRPC: c.version, ID: id, Method: method, Params: raw}); err != nil {
		return err
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return m.Error
		}
		if out != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-c.done:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Conn) Notify(method string, params any) error {
	var raw json.RawMessage
	if params != nil {
		var err error
		if raw, err = json.Marshal(params); err != nil {
			return err
		}
	}
	return c.p.WriteJSON(message{JSONRPC: c.version, Method: method, Params: raw})
}

func (c *Conn) Reply(id json.RawMessage, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return c.p.WriteJSON(message{JSONRPC: c.version, ID: id, Result: raw})
}

func (c *Conn) ReplyError(id json.RawMessage, code int, msg string) error {
	return c.p.WriteJSON(message{JSONRPC: c.version, ID: id, Error: &Error{Code: code, Message: msg}})
}

// Run reads frames until stdout closes.
func (c *Conn) Run() {
	defer close(c.done)
	for {
		line, err := c.p.ReadLine()
		if err != nil {
			return
		}
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var m message
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		switch {
		case m.Method != "" && len(m.ID) > 0:
			if c.h.Request != nil {
				c.h.Request(m.ID, m.Method, m.Params)
			} else {
				c.ReplyError(m.ID, -32601, "method not supported")
			}
		case m.Method != "":
			if c.h.Notify != nil {
				c.h.Notify(m.Method, m.Params)
			}
		case len(m.ID) > 0:
			c.mu.Lock()
			ch := c.pending[string(m.ID)]
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}
}
