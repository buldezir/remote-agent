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
	"net/url"
	"os"
	"path"
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

	// WebOrigins are the web pages allowed to call rad, as host patterns
	// ("localhost:*") or, with a scheme, origin patterns ("https://x.dev").
	WebOrigins []string
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
	return s.cors(mux)
}

// cors lets pages on the allowed web origins call rad from a browser.
// Preflights also allow Chrome's Private Network Access, for a page that
// reaches rad on a LAN or Tailscale address.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || !s.webOrigin(origin) {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Add("Vary", "Origin")
		h.Set("Access-Control-Expose-Headers", "ETag")
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			h.Set("Access-Control-Allow-Methods", "GET, POST")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Max-Age", "86400")
			if r.Header.Get("Access-Control-Request-Private-Network") == "true" {
				h.Set("Access-Control-Allow-Private-Network", "true")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// webOrigin reports whether origin matches WebOrigins, the way the WebSocket
// library matches its OriginPatterns.
func (s *Server) webOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	for _, p := range s.WebOrigins {
		target := u.Host
		if strings.Contains(p, "://") {
			target = u.Scheme + "://" + u.Host
		}
		if ok, _ := path.Match(strings.ToLower(p), strings.ToLower(target)); ok {
			return true
		}
	}
	return false
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

// Browsers can't set headers on a WebSocket, so a page offers its token as a
// subprotocol, tokenProtocol plus the token, beside wsProtocol, and rad
// answers with wsProtocol.
const (
	wsProtocol    = "rad.v1"
	tokenProtocol = "rad.token."
)

// subprotocolToken is the token a page offered in Sec-WebSocket-Protocol.
func subprotocolToken(r *http.Request) string {
	for _, h := range r.Header.Values("Sec-WebSocket-Protocol") {
		for p := range strings.SplitSeq(h, ",") {
			if t, ok := strings.CutPrefix(strings.TrimSpace(p), tokenProtocol); ok {
				return t
			}
		}
	}
	return ""
}

// device authenticates a request by its bearer token, answering 401 if it
// has none or a revoked one.
func (s *Server) device(w http.ResponseWriter, r *http.Request) (store.Device, bool) {
	return s.deviceByToken(w, r, bearer(r))
}

func (s *Server) deviceByToken(w http.ResponseWriter, r *http.Request, token string) (store.Device, bool) {
	dev, err := s.st.DeviceByToken(r.Context(), token)
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
	token := bearer(r)
	if token == "" {
		token = subprotocolToken(r)
	}
	dev, ok := s.deviceByToken(w, r, token)
	if !ok {
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionContextTakeover,
		Subprotocols:    []string{wsProtocol},
		OriginPatterns:  s.WebOrigins,
	})
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
