// Package radtest runs a rad in a test, with the fake harness, for the
// tests of ra's client and interface. It links all of rad, so only tests
// may import it.
package radtest

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"remote-agent/internal/api"
	"remote-agent/internal/events"
	"remote-agent/internal/fsbrowse"
	"remote-agent/internal/harness"
	"remote-agent/internal/harness/fake"
	"remote-agent/internal/images"
	"remote-agent/internal/orchestrator"
	"remote-agent/internal/store"
	"remote-agent/internal/tasks"
)

// Rad is a rad serving on a test HTTP server, as internal/api's tests
// start one.
type Rad struct {
	URL   string
	Store *store.Store
	// Root is the folder fs.list starts at, a project's place.
	Root   string
	Server *httptest.Server
}

func Start(t testing.TB) *Rad {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "rad.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hub := events.NewHub()
	st.OnCommit(hub.Publish)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	imgs := images.New(t.TempDir())
	orch := orchestrator.New(st, harness.NewRegistry(fake.Harness{}), orchestrator.Options{WorktreesDir: t.TempDir(), Images: imgs, Log: log})
	t.Cleanup(orch.Shutdown)
	root := t.TempDir()
	srv := api.NewServer(st, hub, orch, fsbrowse.New([]string{root}), imgs, "srv-1", "test-host", log)
	maint := tasks.NewManager(log)
	srv.Tasks = maint
	t.Cleanup(maint.Shutdown)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &Rad{URL: ts.URL, Store: st, Root: root, Server: ts}
}

// Code makes a pairing code, as `rad pair` does.
func (r *Rad) Code(t testing.TB) string {
	t.Helper()
	code, err := r.Store.CreatePairingCode(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return code
}
