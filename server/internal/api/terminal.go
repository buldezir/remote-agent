package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"remote-agent/internal/model"
	"remote-agent/internal/orchestrator"
	"remote-agent/internal/terminal"
)

// terminalProtocol is the subprotocol of a terminal socket.
const terminalProtocol = "rad.terminal.v1"

// keepalive pings a socket, so a dead connection is noticed, and closes it
// with 4001 once its device is unpaired.
func (s *Server) keepalive(ctx context.Context, ws *websocket.Conn, deviceID string, cancel context.CancelFunc) {
	ping := time.NewTicker(pingInterval)
	check := time.NewTicker(revalidateEach)
	defer ping.Stop()
	defer check.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
			err := ws.Ping(pctx)
			pcancel()
			if err != nil {
				cancel()
				return
			}
		case <-check.C:
			if ok, err := s.st.DeviceExists(ctx, deviceID); err == nil && !ok {
				ws.Close(4001, "device revoked")
				cancel()
				return
			}
		}
	}
}

// terminal attaches a client to a session's shell, starting one when the
// client asks with create=1. See "Terminal" in protocol/PROTOCOL.md.
func (s *Server) terminal(w http.ResponseWriter, r *http.Request) {
	if s.Terminals == nil {
		http.NotFound(w, r)
		return
	}
	token := bearer(r)
	if token == "" {
		token = subprotocolToken(r)
	}
	dev, ok := s.deviceByToken(w, r, token)
	if !ok {
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionContextTakeover,
		Subprotocols:    []string{terminalProtocol},
		OriginPatterns:  s.WebOrigins,
	})
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.st.TouchDevice(r.Context(), dev.ID)

	// Errors come as a frame before the close: a client's WebSocket API
	// doesn't always report the close code.
	fail := func(code, msg string) {
		writeTerminal(ctx, ws, model.TerminalMessage{Type: "error", Code: code, Message: msg})
		ws.Close(4000, msg)
	}
	q := r.URL.Query()
	sess, err := s.st.Session(r.Context(), q.Get("session"))
	switch {
	case err != nil:
		fail("not_found", "session not found")
		return
	case sess.Archived:
		fail("archived", "the session is archived")
		return
	}
	dir := orchestrator.Workdir(sess.Workspace)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		fail("no_dir", "the session's folder is gone")
		return
	}
	cols, rows := termSize(q.Get("cols"), 80), termSize(q.Get("rows"), 24)
	t := s.Terminals.Get(sess.ID)
	if t == nil {
		if q.Get("create") != "1" {
			fail("gone", "the shell has ended")
			return
		}
		if t, err = s.Terminals.Open(sess.ID, dir, cols, rows); errors.Is(err, terminal.ErrShutdown) {
			fail("unavailable", err.Error())
			return
		} else if err != nil {
			s.log.Error("terminal", "session", sess.ID, "err", err)
			fail("unavailable", "can't start a shell: "+err.Error())
			return
		}
	}
	offset, _ := strconv.ParseUint(q.Get("offset"), 10, 64)
	rd := t.Attach(q.Get("id"), offset)
	t.Resize(cols, rows, rd.Reset)
	s.log.Info("terminal attached", "device", dev.Name, "session", sess.ID, "resumed", !rd.Reset, "cols", cols, "rows", rows)
	defer s.log.Info("terminal detached", "device", dev.Name, "session", sess.ID)
	if !writeTerminal(ctx, ws, model.TerminalMessage{Type: "attached", ID: t.ID, Offset: rd.Offset, Reset: rd.Reset, Cols: cols, Rows: rows}) {
		return
	}

	go s.keepalive(ctx, ws, dev.ID, cancel)
	go func() {
		defer cancel()
		for {
			typ, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				t.Write(data)
				continue
			}
			var m model.TerminalMessage
			json.Unmarshal(data, &m)
			switch m.Type {
			case "resize":
				t.Resize(termSize(strconv.Itoa(m.Cols), cols), termSize(strconv.Itoa(m.Rows), rows), false)
			case "close":
				s.log.Info("terminal closed", "device", dev.Name, "session", sess.ID)
				t.Kill()
			}
		}
	}()

	for {
		c, err := rd.Next(ctx, 64<<10)
		if err != nil {
			return
		}
		switch {
		case c.Exited:
			code := c.Code
			if writeTerminal(ctx, ws, model.TerminalMessage{Type: "exit", ExitCode: &code}) {
				ws.Close(websocket.StatusNormalClosure, "")
			}
			return
		case c.Reset:
			if !writeTerminal(ctx, ws, model.TerminalMessage{Type: "reset", Offset: c.Offset}) {
				return
			}
		}
		if len(c.Data) > 0 {
			wctx, wcancel := context.WithTimeout(ctx, writeTimeout)
			err := ws.Write(wctx, websocket.MessageBinary, c.Data)
			wcancel()
			if err != nil {
				return
			}
		}
	}
}

func (s *Server) closeTerminal(sessionID string) {
	if s.Terminals != nil {
		s.Terminals.Close(sessionID)
	}
}

func writeTerminal(ctx context.Context, ws *websocket.Conn, m model.TerminalMessage) bool {
	b, _ := json.Marshal(m)
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return ws.Write(ctx, websocket.MessageText, b) == nil
}

// termSize parses a terminal dimension, keeping it to a sane range.
func termSize(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	return min(max(n, 2), 1000)
}
