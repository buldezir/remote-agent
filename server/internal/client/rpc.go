package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"remote-agent/internal/model"
)

// RPCError is an error the server answered with, or one of the client's
// own: disconnected, timeout, unauthorized, unreachable, http.
type RPCError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return e.Message }

// ErrorCode is err's RPCError code, or "" if it has none.
func ErrorCode(err error) string {
	var e *RPCError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func errDisconnected() *RPCError {
	return &RPCError{"disconnected", "Not connected to rad"}
}

func errTimeout() *RPCError {
	return &RPCError{"timeout", "rad did not respond in time"}
}

func errUnauthorized() *RPCError {
	return &RPCError{"unauthorized", "rad no longer accepts this device; pair again."}
}

// MessageKind tells server messages apart.
type MessageKind int

const (
	MessageEvents MessageKind = iota
	MessageSynchronized
	MessageResync
	// MessageClosed: the socket closed. Pending calls fail as disconnected.
	MessageClosed
)

// ServerMessage is what the server sends unasked, and the socket closing.
type ServerMessage struct {
	Kind   MessageKind
	Stream string
	Events []model.Event // events
	Seq    int64         // synchronized
	Reset  bool          // synchronized
	Reason string        // closed
}

// Close code rad uses for a revoked device.
const revokedCode = 4001

// RPCClient is one WebSocket connection to rad. Reconnecting is the owner's
// job: when the socket drops, OnMessage gets MessageClosed and pending calls
// fail as disconnected.
//
// A socket can die without a word, as when the computer sleeps: nothing
// arrives, and writes vanish. Ping finds out, and so does a call that goes
// unanswered; either drops a socket that doesn't answer.
type RPCClient struct {
	// OnMessage gets notifications and MessageClosed, in order. It is
	// called with the client's lock held, so it must only hand them on.
	OnMessage func(ServerMessage)
	// PingTimeout is how long a ping waits for its answer.
	PingTimeout time.Duration
	HTTP        *http.Client

	mu      sync.Mutex
	token   string
	sock    *socket
	pending map[int64]chan response
	nextID  int64
	// When the socket last brought something, in UnixNano.
	lastHeard atomic.Int64
}

type socket struct {
	ws    *websocket.Conn
	base  string
	ready bool // server.info answered
}

type response struct {
	result json.RawMessage
	err    error
}

func NewRPCClient(token string) *RPCClient {
	c := &RPCClient{
		OnMessage:   func(ServerMessage) {},
		PingTimeout: 5 * time.Second,
		HTTP:        &http.Client{},
		token:       token,
		pending:     map[int64]chan response{},
	}
	c.lastHeard.Store(time.Now().UnixNano())
	return c
}

func (c *RPCClient) SetToken(token string) {
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
}

func (c *RPCClient) getToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

// IsConnected: a socket is open and rad answered server.info on it.
func (c *RPCClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sock != nil && c.sock.ready
}

// ConnectedURL is the base URL of the socket that is open, or "".
func (c *RPCClient) ConnectedURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sock == nil || !c.sock.ready {
		return ""
	}
	return c.sock.base
}

// Silence is how long the socket has said nothing.
func (c *RPCClient) Silence() time.Duration {
	return time.Since(time.Unix(0, c.lastHeard.Load()))
}

// Connect connects to the first base URL that accepts the token, and
// returns the server's info.
func (c *RPCClient) Connect(ctx context.Context, urls []string) (ServerInfo, error) {
	var last error = errDisconnected()
	for _, base := range urls {
		info, err := c.open(ctx, base)
		if err == nil {
			return info, nil
		}
		last = err
		if ErrorCode(err) == "unauthorized" || ctx.Err() != nil {
			break
		}
	}
	return ServerInfo{}, last
}

func (c *RPCClient) open(ctx context.Context, base string) (ServerInfo, error) {
	c.Disconnect("", false)
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(dctx, "ws"+strings.TrimPrefix(base, "http")+"/v1/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + c.getToken()}},
		HTTPClient: c.HTTP,
	})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return ServerInfo{}, errUnauthorized()
		}
		if ctx.Err() != nil {
			return ServerInfo{}, ctx.Err()
		}
		return ServerInfo{}, &RPCError{"unreachable", fmt.Sprintf("Could not connect to %s: %v", base, unwrapDial(err))}
	}
	ws.SetReadLimit(64 << 20)
	s := &socket{ws: ws, base: base}
	c.mu.Lock()
	c.sock = s
	c.mu.Unlock()
	c.lastHeard.Store(time.Now().UnixNano())
	go c.read(s)
	// Not Call: an answer that doesn't come fails the attempt, without a ping.
	var info ServerInfo
	if err := c.request(ctx, s, "server.info", nil, &info, 8*time.Second, false); err != nil {
		c.closeSocket(s, "", false)
		return ServerInfo{}, err
	}
	c.mu.Lock()
	ok := c.sock == s
	if ok {
		s.ready = true
	}
	c.mu.Unlock()
	if !ok {
		return ServerInfo{}, errDisconnected()
	}
	return info, nil
}

// unwrapDial shortens a dial error to its cause, such as "connection refused".
func unwrapDial(err error) error {
	for {
		u := errors.Unwrap(err)
		if u == nil {
			return err
		}
		err = u
	}
}

// Disconnect closes the socket, if there is one; with notify, OnMessage
// hears of it.
func (c *RPCClient) Disconnect(reason string, notify bool) {
	c.mu.Lock()
	s := c.sock
	c.mu.Unlock()
	if s != nil {
		c.closeSocket(s, reason, notify)
	}
}

func (c *RPCClient) closeSocket(s *socket, reason string, notify bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sock != s {
		return
	}
	c.sock = nil
	go s.ws.Close(websocket.StatusNormalClosure, "")
	for id, ch := range c.pending {
		ch <- response{err: errDisconnected()}
		delete(c.pending, id)
	}
	if notify {
		c.OnMessage(ServerMessage{Kind: MessageClosed, Reason: reason})
	}
}

func (c *RPCClient) read(s *socket) {
	for {
		typ, data, err := s.ws.Read(context.Background())
		if err != nil {
			reason := ""
			if websocket.CloseStatus(err) == revokedCode {
				reason = errUnauthorized().Message
			}
			c.closeSocket(s, reason, true)
			return
		}
		c.lastHeard.Store(time.Now().UnixNano())
		if typ == websocket.MessageText {
			c.dispatch(s, data)
		}
	}
}

func (c *RPCClient) dispatch(s *socket, data []byte) {
	var f struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if json.Unmarshal(data, &f) != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sock != s {
		return
	}
	if len(f.ID) > 0 && f.Method == "" {
		var id int64
		if json.Unmarshal(f.ID, &id) != nil {
			return
		}
		ch := c.pending[id]
		if ch == nil {
			return
		}
		delete(c.pending, id)
		if f.Error != nil {
			ch <- response{err: f.Error}
		} else {
			ch <- response{result: f.Result}
		}
		return
	}
	switch f.Method {
	case "events":
		var p eventsParams
		if json.Unmarshal(f.Params, &p) == nil {
			c.OnMessage(ServerMessage{Kind: MessageEvents, Stream: p.Stream, Events: p.Events})
		}
	case "synchronized":
		var p synchronizedParams
		if json.Unmarshal(f.Params, &p) == nil {
			c.OnMessage(ServerMessage{Kind: MessageSynchronized, Stream: p.Stream, Seq: p.Seq, Reset: p.Reset})
		}
	case "resync":
		var p resyncParams
		if json.Unmarshal(f.Params, &p) == nil {
			c.OnMessage(ServerMessage{Kind: MessageResync, Stream: p.Stream})
		}
	}
}

// Call sends a request and decodes its result into out, which may be nil.
// It waits up to 30 seconds.
func (c *RPCClient) Call(ctx context.Context, method string, params, out any) error {
	return c.CallTimeout(ctx, method, params, out, 30*time.Second)
}

// CallTimeout is Call with a timeout of its own. A call that goes
// unanswered pings: if the socket doesn't answer either, it's dropped, and
// the call fails as disconnected; otherwise rad is just slow, and the call
// fails as timed out.
func (c *RPCClient) CallTimeout(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	c.mu.Lock()
	s := c.sock
	c.mu.Unlock()
	if s == nil {
		return errDisconnected()
	}
	return c.request(ctx, s, method, params, out, timeout, true)
}

// Ping checks that rad answers, by asking for server.info, which it
// answers at once. A socket that doesn't answer within PingTimeout is
// dropped, so the owner reconnects.
func (c *RPCClient) Ping() bool {
	c.mu.Lock()
	s := c.sock
	c.mu.Unlock()
	if s == nil {
		return false
	}
	return c.ping(s)
}

func (c *RPCClient) ping(s *socket) bool {
	err := c.request(context.Background(), s, "server.info", nil, nil, c.PingTimeout, false)
	// An error is an answer too.
	code := ErrorCode(err)
	alive := code != "timeout" && code != "disconnected"
	if !alive {
		c.closeSocket(s, "rad stopped answering", true)
	}
	return alive
}

func (c *RPCClient) request(ctx context.Context, s *socket, method string, params, out any, timeout time.Duration, pingIfLate bool) error {
	if params == nil {
		params = struct{}{}
	}
	ch := make(chan response, 1)
	c.mu.Lock()
	if c.sock != s {
		c.mu.Unlock()
		return errDisconnected()
	}
	c.nextID++
	id := c.nextID
	c.pending[id] = ch
	c.mu.Unlock()
	forget := func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}
	b, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		forget()
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, timeout)
	err = s.ws.Write(wctx, websocket.MessageText, b)
	cancel()
	if err != nil {
		forget()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// A socket that can't send: drop it now rather than when it says so.
		c.closeSocket(s, "The connection closed", true)
		return errDisconnected()
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		return decode(r, out)
	case <-ctx.Done():
		forget()
		return ctx.Err()
	case <-timer.C:
	}
	if pingIfLate && !c.ping(s) {
		// The ping dropped the socket, which failed the call.
		return decode(<-ch, out)
	}
	c.mu.Lock()
	_, waiting := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if !waiting {
		// It came after all, while the ping went.
		return decode(<-ch, out)
	}
	return errTimeout()
}

func decode(r response, out any) error {
	if r.err != nil {
		return r.err
	}
	if out == nil || len(r.result) == 0 || string(r.result) == "null" {
		return nil
	}
	return json.Unmarshal(r.result, out)
}

// HTTP, beside the socket.

// UploadImage uploads an image to attach to a prompt (POST /v1/images).
func (c *RPCClient) UploadImage(ctx context.Context, data []byte) (model.ImageRef, error) {
	var ref model.ImageRef
	body, err := c.http(ctx, "POST", "/v1/images", data)
	if err != nil {
		return ref, err
	}
	return ref, json.Unmarshal(body, &ref)
}

// Image returns an image's bytes (GET /v1/images/<id>).
func (c *RPCClient) Image(ctx context.Context, id string) ([]byte, error) {
	return c.http(ctx, "GET", "/v1/images/"+id, nil)
}

func (c *RPCClient) http(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	base := c.ConnectedURL()
	if base == "" {
		return nil, errDisconnected()
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.getToken())
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, &RPCError{"http", err.Error()}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &RPCError{"http", err.Error()}
	}
	if resp.StatusCode != http.StatusOK {
		var e RPCError
		if json.Unmarshal(data, &e) == nil && e.Code != "" {
			if e.Message == "" {
				e.Message = e.Code
			}
			return nil, &e
		}
		return nil, &RPCError{"http", fmt.Sprintf("rad answered %d", resp.StatusCode)}
	}
	return data, nil
}
