package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"remote-agent/internal/model"
)

// termClient is the client side of a terminal socket.
type termClient struct {
	t      *testing.T
	ws     *websocket.Conn
	out    []byte
	offset uint64
}

func (s *testServer) dialTerminal(t *testing.T, token, query string) (*termClient, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h := http.Header{}
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	ws, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(s.url, "http")+"/v1/terminal?"+query, &websocket.DialOptions{HTTPHeader: h, Subprotocols: []string{"rad.terminal.v1"}})
	if err != nil {
		return nil, resp, err
	}
	t.Cleanup(func() { ws.CloseNow() })
	return &termClient{t: t, ws: ws}, resp, nil
}

// next reads a frame: output is collected, and a message is returned.
func (c *termClient) next(ctx context.Context) (model.TerminalMessage, error) {
	typ, data, err := c.ws.Read(ctx)
	if err != nil {
		return model.TerminalMessage{}, err
	}
	if typ == websocket.MessageBinary {
		c.out = append(c.out, data...)
		c.offset += uint64(len(data))
		return model.TerminalMessage{}, nil
	}
	var m model.TerminalMessage
	if err := json.Unmarshal(data, &m); err != nil {
		c.t.Fatalf("bad frame %s", data)
	}
	if m.Type == "attached" || m.Type == "reset" {
		c.offset = m.Offset
	}
	return m, nil
}

func (c *termClient) message(typ string) model.TerminalMessage {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		m, err := c.next(ctx)
		if err != nil || m.Type != "" && m.Type != typ {
			c.t.Fatalf("want %s, got %+v (%v); output %q", typ, m, err, c.out)
		}
		if m.Type == typ {
			return m
		}
	}
}

// until reads output until it contains want.
func (c *termClient) until(want string) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for !bytes.Contains(c.out, []byte(want)) {
		if m, err := c.next(ctx); err != nil {
			c.t.Fatalf("waiting for %q: %v; output %q", want, err, c.out)
		} else if m.Type == "exit" || m.Type == "error" {
			c.t.Fatalf("waiting for %q: %+v", want, m)
		}
	}
}

func (c *termClient) send(s string) {
	c.t.Helper()
	if err := c.ws.Write(context.Background(), websocket.MessageBinary, []byte(s)); err != nil {
		c.t.Fatal(err)
	}
}

func (c *termClient) control(m model.TerminalMessage) {
	c.t.Helper()
	b, _ := json.Marshal(m)
	if err := c.ws.Write(context.Background(), websocket.MessageText, b); err != nil {
		c.t.Fatal(err)
	}
}

func TestTerminal(t *testing.T) {
	s := startServer(t)
	token := s.token(t)
	c := connect(t, s)

	var info struct {
		Terminal bool `json:"terminal"`
	}
	c.mustCall("server.info", nil, &info)
	if !info.Terminal {
		t.Error("server.info doesn't offer terminals")
	}

	dir := filepath.Join(s.root, "proj")
	os.Mkdir(dir, 0o755)
	var proj model.Project
	c.mustCall("project.add", map[string]any{"path": dir}, &proj)
	var sess model.Session
	c.mustCall("session.create", map[string]any{"commandId": "c1", "projectId": proj.ID, "harness": "fake", "prompt": "hi"}, &sess)
	q := "session=" + sess.ID + "&cols=100&rows=30"

	if _, resp, err := s.dialTerminal(t, "wrong", q+"&create=1"); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: %v", err)
	}
	for _, tc := range []struct{ query, code string }{
		{"session=nope&create=1", "not_found"},
		{q, "gone"}, // no shell yet, and the client didn't ask for one
	} {
		tc2, _, err := s.dialTerminal(t, token, tc.query)
		if err != nil {
			t.Fatal(err)
		}
		if m := tc2.message("error"); m.Code != tc.code {
			t.Errorf("%s: error %+v, want %s", tc.query, m, tc.code)
		}
	}

	// A new shell, at the client's size, in the session's folder.
	t1, _, err := s.dialTerminal(t, token, q+"&create=1")
	if err != nil {
		t.Fatal(err)
	}
	if t1.ws.Subprotocol() != "rad.terminal.v1" {
		t.Errorf("subprotocol = %q", t1.ws.Subprotocol())
	}
	att := t1.message("attached")
	if att.ID == "" || !att.Reset || att.Offset != 0 || att.Cols != 100 || att.Rows != 30 {
		t.Errorf("attached = %+v", att)
	}
	t1.send("stty size; pwd; echo $((6*7))\n")
	t1.until("42")
	real, _ := filepath.EvalSymlinks(dir)
	if out := string(t1.out); !strings.Contains(out, "30 100") || !strings.Contains(out, real) {
		t.Errorf("output %q", out)
	}
	t1.control(model.TerminalMessage{Type: "resize", Cols: 90, Rows: 20})
	t1.send("stty size\n")
	t1.until("20 90")

	// Reconnecting with the shell's id and offset resumes; nothing repeats.
	t1.ws.Close(websocket.StatusNormalClosure, "")
	t2, _, err := s.dialTerminal(t, token, fmt.Sprintf("%s&id=%s&offset=%d", q, att.ID, t1.offset))
	if err != nil {
		t.Fatal(err)
	}
	if m := t2.message("attached"); m.Reset || m.ID != att.ID || m.Offset != t1.offset {
		t.Errorf("resumed attached = %+v, want offset %d", m, t1.offset)
	}
	t2.send("echo $((7*8))\n")
	t2.until("56")
	if bytes.Contains(t2.out, []byte("42")) {
		t.Errorf("resumed output repeats earlier output: %q", t2.out)
	}

	// A browser on an allowed origin, with the token as a subprotocol, starts
	// afresh and gets the shell's output so far.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(s.url, "http")+"/v1/terminal?"+q,
		&websocket.DialOptions{HTTPHeader: http.Header{"Origin": {"http://localhost:5173"}}, Subprotocols: []string{"rad.terminal.v1", "rad.token." + token}})
	if err != nil {
		t.Fatal(err)
	}
	t3 := &termClient{t: t, ws: ws}
	t.Cleanup(func() { ws.CloseNow() })
	if m := t3.message("attached"); !m.Reset {
		t.Errorf("browser attached = %+v", m)
	}
	t3.until("56")
	if !bytes.Contains(t3.out, []byte("42")) {
		t.Errorf("replay %q misses earlier output", t3.out)
	}

	// Archiving the session ends its shell; every client hears it exit.
	c.mustCall("session.archive", map[string]any{"sessionId": sess.ID}, nil)
	for _, tc := range []*termClient{t2, t3} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		for {
			m, err := tc.next(ctx)
			if err != nil {
				t.Fatalf("no exit after archiving: %v", err)
			}
			if m.Type == "exit" {
				if m.ExitCode == nil || *m.ExitCode == 0 {
					t.Errorf("exit = %+v", m)
				}
				break
			}
		}
		cancel()
	}
	t4, _, err := s.dialTerminal(t, token, q+"&create=1")
	if err != nil {
		t.Fatal(err)
	}
	if m := t4.message("error"); m.Code != "archived" {
		t.Errorf("archived session: %+v", m)
	}
}

// Close ends the shell for everyone; the next open starts a new one.
func TestTerminalClose(t *testing.T) {
	s := startServer(t)
	token := s.token(t)
	c := connect(t, s)
	dir := filepath.Join(s.root, "proj")
	os.Mkdir(dir, 0o755)
	var proj model.Project
	c.mustCall("project.add", map[string]any{"path": dir}, &proj)
	var sess model.Session
	c.mustCall("session.create", map[string]any{"commandId": "c1", "projectId": proj.ID, "harness": "fake", "prompt": "hi"}, &sess)
	q := "session=" + sess.ID + "&create=1"

	t1, _, err := s.dialTerminal(t, token, q)
	if err != nil {
		t.Fatal(err)
	}
	first := t1.message("attached")
	t1.send("exit 7\n")
	if m := t1.message("exit"); m.ExitCode == nil || *m.ExitCode != 7 {
		t.Errorf("exit = %+v", m)
	}

	t2, _, err := s.dialTerminal(t, token, q)
	if err != nil {
		t.Fatal(err)
	}
	second := t2.message("attached")
	if second.ID == first.ID {
		t.Error("the shell that exited came back")
	}
	t2.control(model.TerminalMessage{Type: "close"})
	if m := t2.message("exit"); m.ExitCode == nil || *m.ExitCode == 0 {
		t.Errorf("exit after close = %+v", m)
	}
}

func TestTerminalListAndCloseAll(t *testing.T) {
	s := startServer(t)
	token := s.token(t)
	c := connect(t, s)
	var info struct{ Terminals bool }
	c.mustCall("server.info", nil, &info)
	if !info.Terminals {
		t.Error("server.info doesn't say terminals")
	}
	dir := filepath.Join(s.root, "proj")
	os.Mkdir(dir, 0o755)
	var proj model.Project
	c.mustCall("project.add", map[string]any{"path": dir}, &proj)
	var sess model.Session
	c.mustCall("session.create", map[string]any{"commandId": "c1", "projectId": proj.ID, "harness": "fake", "prompt": "hi"}, &sess)
	c.mustCall("server.setSettings", map[string]any{"terminalTimeoutDays": 2}, nil)

	var list model.TerminalList
	c.mustCall("terminal.list", nil, &list)
	if len(list.Terminals) != 0 || list.TimeoutDays != 2 {
		t.Fatalf("list before = %+v", list)
	}
	term, _, err := s.dialTerminal(t, token, "session="+sess.ID+"&create=1")
	if err != nil {
		t.Fatal(err)
	}
	term.message("attached")
	c.mustCall("terminal.list", nil, &list)
	if len(list.Terminals) != 1 {
		t.Fatalf("list = %+v", list)
	}
	if got := list.Terminals[0]; got.SessionID != sess.ID || got.ExpiresAt.Sub(got.LastInputAt) != 48*time.Hour {
		t.Errorf("terminal = %+v", got)
	}

	var closed struct{ Closed int }
	c.mustCall("terminal.closeAll", nil, &closed)
	if closed.Closed != 1 {
		t.Errorf("closed = %d", closed.Closed)
	}
	if m := term.message("exit"); m.ExitCode == nil || *m.ExitCode == 0 {
		t.Errorf("exit after closeAll = %+v", m)
	}
}
