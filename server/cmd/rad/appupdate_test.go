package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"remote-agent/internal/api"
	"remote-agent/internal/config"
	"remote-agent/internal/events"
	"remote-agent/internal/fsbrowse"
	"remote-agent/internal/harness"
	"remote-agent/internal/images"
	"remote-agent/internal/model"
	"remote-agent/internal/orchestrator"
	"remote-agent/internal/store"
	"remote-agent/internal/tasks"
)

// installed notes when an update has gone in.
type installed struct {
	*selfUpdater
	done atomic.Bool
}

func (u *installed) Install(ctx context.Context, t *tasks.Task) error {
	err := u.selfUpdater.Install(ctx, t)
	u.done.Store(err == nil)
	return err
}

// serveApp serves u's store as a rad in the data dir, and returns its
// config. Once an update has gone in, its health check reports 0.2.0, as the
// rad that the updated app brings would.
func serveApp(t *testing.T, dataDir string, u *selfUpdater) *config.Config {
	t.Helper()
	hub := events.NewHub()
	u.st.OnCommit(hub.Publish)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	imgs := images.New(t.TempDir())
	orch := orchestrator.New(u.st, harness.NewRegistry(), orchestrator.Options{WorktreesDir: t.TempDir(), Images: imgs, Log: log})
	t.Cleanup(orch.Shutdown)
	srv := api.NewServer(u.st, hub, orch, fsbrowse.New(nil), imgs, "srv-1", "test-host", log)
	srv.Tasks = tasks.NewManager(nil)
	t.Cleanup(srv.Tasks.Shutdown)
	up := &installed{selfUpdater: u}
	srv.Updater = up
	h := srv.Handler()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" && up.done.Load() {
			json.NewEncoder(w).Encode(healthInfo{ServerID: "srv-1", Version: "0.2.0"})
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return &config.Config{DataDir: dataDir, Listen: []string{ts.Listener.Addr().String()}}
}

func storeIn(t *testing.T, dataDir string) *store.Store {
	st, err := store.Open(filepath.Join(dataDir, "rad.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func noDevices(t *testing.T, st *store.Store) {
	t.Helper()
	if devs, err := st.Devices(context.Background()); err != nil || len(devs) != 0 {
		t.Errorf("devices left: %+v %v", devs, err)
	}
}

const serverApp = "/Applications/Remote Agent Server.app"

func TestAppUpdate(t *testing.T) {
	withVersion(t, "0.1.0")
	ctx := context.Background()
	dir := t.TempDir()
	st := storeIn(t, dir)
	st.Write(ctx, func(w *store.W) error {
		return w.PutSession(&model.Session{ID: "s1", ProjectID: "p", Title: "Busy one", Status: model.SessionRunning})
	})
	app := fakeApp(t,
		appMessage{Event: "updateStatus", Status: "checking"},
		appMessage{Event: "updateStatus", Status: "downloading", Version: "0.2.0"},
		appMessage{Event: "updateStatus", Status: "installing", Version: "0.2.0"})
	cfg := serveApp(t, dir, &selfUpdater{client: releases(t), st: st, exe: serverApp + "/Contents/MacOS/rad", app: app,
		restart: func() { t.Error("rad restarted itself") }, appAnswer: 5 * time.Second, appInstall: 5 * time.Second})
	var out strings.Builder
	a := &appUpdate{cfg: cfg, app: serverApp, out: &out, poll: 10 * time.Millisecond, restart: 5 * time.Second}

	err := a.run(ctx)
	if err == nil || !strings.Contains(err.Error(), "Busy one") || !strings.Contains(err.Error(), "restarts Remote Agent Server.app") ||
		!strings.Contains(err.Error(), "--now") {
		t.Fatalf("with a busy session: %v", err)
	}
	noDevices(t, st)

	a.now = true
	if err := a.run(ctx); err != nil {
		t.Fatalf("update: %v\n%s", err, &out)
	}
	for _, want := range []string{
		"Asking the Remote Agent Server app to install 0.2.0…\n",
		"Downloading Remote Agent Server 0.2.0…\n",
		"The app restarts, and rad with it.\n",
		"rad 0.2.0 is running\n",
	} {
		if strings.Count(out.String(), want) != 1 {
			t.Errorf("output lacks %q once:\n%s", want, &out)
		}
	}
	noDevices(t, st)
}

func TestAppUpdateRefuses(t *testing.T) {
	withVersion(t, "0.2.0")
	ctx := context.Background()
	run := func(u *selfUpdater) (string, error) {
		dir := t.TempDir()
		u.client, u.st = releases(t), storeIn(t, dir)
		var out strings.Builder
		a := &appUpdate{cfg: serveApp(t, dir, u), app: serverApp, out: &out, poll: 10 * time.Millisecond, restart: time.Second}
		err := a.run(ctx)
		noDevices(t, u.st)
		return out.String(), err
	}

	if out, err := run(&selfUpdater{exe: serverApp + "/Contents/MacOS/rad", app: fakeApp(t)}); err != nil || out != "rad 0.2.0 is up to date\n" {
		t.Errorf("up to date: %q %v", out, err)
	}

	// Another rad serves the config, such as the background service's.
	exe := filepath.Join(t.TempDir(), "rad")
	os.WriteFile(exe, []byte("#!/bin/sh\necho rad 0.1.0\n"), 0o755)
	if _, err := run(&selfUpdater{exe: exe}); err == nil || !strings.Contains(err.Error(), "isn't Remote Agent Server.app's") {
		t.Errorf("another rad: %v", err)
	}

	withVersion(t, "0.1")
	if _, err := run(&selfUpdater{exe: serverApp + "/Contents/MacOS/rad", app: fakeApp(t)}); err == nil ||
		!strings.Contains(err.Error(), "development build") {
		t.Errorf("a build from Xcode: %v", err)
	}

	// Nothing listens.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	a := &appUpdate{cfg: &config.Config{DataDir: t.TempDir(), Listen: []string{ln.Addr().String()}}, app: serverApp, out: io.Discard}
	if err := a.run(ctx); err == nil || !strings.Contains(err.Error(), "open Remote Agent Server.app") {
		t.Errorf("not running: %v", err)
	}
}

func TestAppBundle(t *testing.T) {
	for exe, want := range map[string]string{
		serverApp + "/Contents/MacOS/rad":                               serverApp,
		"/Users/a/Downloads/Remote Agent Server.app/Contents/MacOS/rad": "/Users/a/Downloads/Remote Agent Server.app",
		"/usr/local/bin/rad":                                            "",
	} {
		if got := appBundle(exe); got != want {
			t.Errorf("appBundle(%q) = %q, want %q", exe, got, want)
		}
	}
}
