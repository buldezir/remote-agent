package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// TestBridge passes stdio's messages to rad's MCP server and back: replies a
// line each, nothing for notifications, and an error for a request rad
// refused. A slow request doesn't hold up the others.
func TestBridge(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"code":"unauthorized","message":"invalid device token"}`)
			return
		}
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&m)
		switch m.Method {
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "slow":
			<-release
			io.WriteString(w, `{"jsonrpc":"2.0","id":`+string(m.ID)+`,"result":{"slow":true}}`+"\n")
		default:
			io.WriteString(w, `{"jsonrpc":"2.0","id":`+string(m.ID)+`,"result":{}}`)
		}
	}))
	defer srv.Close()

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"slow"}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n\n" +
		`{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n")
	var out strings.Builder
	// Only once the ping's reply is out does the slow one answer.
	w := writerFunc(func(b []byte) (int, error) {
		if strings.Contains(string(b), `"id":2`) {
			close(release)
		}
		return out.Write(b)
	})
	if err := bridge(context.Background(), in, w, srv.URL, "tok", srv.Client()); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if !slices.Equal(lines, []string{`{"jsonrpc":"2.0","id":2,"result":{}}`, `{"jsonrpc":"2.0","id":1,"result":{"slow":true}}`}) {
		t.Errorf("out = %q", out.String())
	}

	out.Reset()
	bridge(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":"a","method":"ping"}`+"\n"), &out, srv.URL, "wrong", srv.Client())
	if got := out.String(); got != `{"jsonrpc":"2.0","id":"a","error":{"code":-32603,"message":"rad answered 401 Unauthorized: invalid device token"}}`+"\n" {
		t.Errorf("refused = %q", got)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(b []byte) (int, error) { return f(b) }
