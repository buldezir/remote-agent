// Package api serves rad's HTTP endpoints and the WebSocket RPC protocol
// described in protocol/PROTOCOL.md.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"

	"remote-agent/internal/events"
	"remote-agent/internal/fsbrowse"
	"remote-agent/internal/images"
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
	images   *images.Store
	log      *slog.Logger
	serverID string
	name     string
}

func NewServer(st *store.Store, hub *events.Hub, orch *orchestrator.Orchestrator, fs *fsbrowse.Browser, imgs *images.Store, serverID, name string, log *slog.Logger) *Server {
	return &Server{st: st, hub: hub, orch: orch, fs: fs, images: imgs, serverID: serverID, name: name, log: log}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.health)
	mux.HandleFunc("POST /v1/pair", s.pair)
	mux.HandleFunc("GET /v1/ws", s.ws)
	mux.HandleFunc("POST /v1/images", s.uploadImage)
	mux.HandleFunc("GET /v1/images/{id}", s.getImage)
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

// device authenticates a request by its bearer token, answering 401 if it
// has none or a revoked one.
func (s *Server) device(w http.ResponseWriter, r *http.Request) (store.Device, bool) {
	dev, err := s.st.DeviceByToken(r.Context(), bearer(r))
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, rpcError{Code: "unauthorized", Message: "invalid device token"})
		return dev, false
	}
	return dev, true
}

// uploadImage stores an image for a prompt. The body is the image's bytes;
// the response is its ImageRef, whose id goes in session.prompt.
func (s *Server) uploadImage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.device(w, r); !ok {
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, images.MaxSize))
	if tooLarge := (*http.MaxBytesError)(nil); errors.As(err, &tooLarge) {
		writeJSON(w, http.StatusRequestEntityTooLarge, rpcError{Code: "invalid", Message: images.ErrTooLarge.Error()})
		return
	} else if err != nil {
		writeJSON(w, http.StatusBadRequest, rpcError{Code: "invalid", Message: "could not read the image"})
		return
	}
	ref, err := s.images.Put(data)
	switch {
	case errors.Is(err, images.ErrUnsupported), errors.Is(err, images.ErrTooLarge):
		writeJSON(w, http.StatusBadRequest, rpcError{Code: "invalid", Message: err.Error()})
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, rpcError{Code: "internal", Message: err.Error()})
	default:
		writeJSON(w, http.StatusOK, ref)
	}
}

// getImage serves a stored image. Ids name the content, so it never changes.
func (s *Server) getImage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.device(w, r); !ok {
		return
	}
	ref, path, err := s.images.Get(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, rpcError{Code: "not_found", Message: err.Error()})
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeJSON(w, http.StatusNotFound, rpcError{Code: "not_found", Message: images.ErrNotFound.Error()})
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", ref.MimeType)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("ETag", `"`+ref.ID+`"`)
	http.ServeContent(w, r, "", time.Time{}, f)
}

func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	dev, ok := s.device(w, r)
	if !ok {
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
