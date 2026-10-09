// Package api serves rad's HTTP endpoints and the WebSocket RPC protocol
// described in protocol/PROTOCOL.md.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"remote-agent/internal/events"
	"remote-agent/internal/fsbrowse"
	"remote-agent/internal/model"
	"remote-agent/internal/orchestrator"
	"remote-agent/internal/store"
)

var Version = "dev"

type Server struct {
	st       *store.Store
	hub      *events.Hub
	orch     *orchestrator.Orchestrator
	fs       *fsbrowse.Browser
	log      *slog.Logger
	serverID string
	name     string
}

func NewServer(st *store.Store, hub *events.Hub, orch *orchestrator.Orchestrator, fs *fsbrowse.Browser, serverID, name string, log *slog.Logger) *Server {
	return &Server{st: st, hub: hub, orch: orch, fs: fs, serverID: serverID, name: name, log: log}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.health)
	mux.HandleFunc("POST /v1/pair", s.pair)
	mux.HandleFunc("GET /v1/ws", s.ws)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

type serverInfo struct {
	ServerID        string `json:"serverId"`
	Name            string `json:"name"`
	ProtocolVersion int    `json:"protocolVersion"`
	Version         string `json:"version"`
}

func (s *Server) info() serverInfo {
	return serverInfo{ServerID: s.serverID, Name: s.name, ProtocolVersion: model.ProtocolVersion, Version: Version}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.info())
}

type pairRequest struct {
	Code       string `json:"code"`
	DeviceName string `json:"deviceName"`
}

type pairResponse struct {
	serverInfo
	Token    string `json:"token"`
	DeviceID string `json:"deviceId"`
}

func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	var req pairRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, rpcError{Code: "invalid", Message: "bad request"})
		return
	}
	d, token, err := s.st.RedeemPairingCode(r.Context(), req.Code, req.DeviceName)
	if errors.Is(err, store.ErrBadPairingCode) {
		time.Sleep(500 * time.Millisecond) // slow down guessing
		writeJSON(w, http.StatusUnauthorized, rpcError{Code: "unauthorized", Message: err.Error()})
		return
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, rpcError{Code: "internal", Message: err.Error()})
		return
	}
	s.log.Info("device paired", "device", d.Name, "id", d.ID)
	writeJSON(w, http.StatusOK, pairResponse{serverInfo: s.info(), Token: token, DeviceID: d.ID})
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if t, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(t)
	}
	return ""
}

func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	dev, err := s.st.DeviceByToken(r.Context(), bearer(r))
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, rpcError{Code: "unauthorized", Message: "invalid device token"})
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
	if err != nil {
		return
	}
	c.SetReadLimit(4 << 20)
	s.st.TouchDevice(r.Context(), dev.ID)
	s.log.Info("client connected", "device", dev.Name, "remote", r.RemoteAddr)
	conn := newConn(s, c, dev)
	conn.run(context.Background())
	s.log.Info("client disconnected", "device", dev.Name)
}
