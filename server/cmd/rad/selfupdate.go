package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"remote-agent/internal/api"
	"remote-agent/internal/model"
	"remote-agent/internal/store"
	"remote-agent/internal/tasks"
	"remote-agent/internal/update"
)

// selfUpdater updates rad when a client asks (server.checkUpdate,
// server.update). It puts the latest release in place of its own binary and
// restarts into it, which keeps the PID, so a background service carries
// on. Under the Remote Agent Server app it has the app update itself, as
// the app brings its own rad.
type selfUpdater struct {
	client *update.Client
	st     *store.Store
	exe    string      // this rad's binary
	app    *supervisor // the Server app, with --supervised
	// restart stops rad and starts exe in its place.
	restart func()

	// The app answers an update request within appAnswer, and installs
	// within appInstall.
	appAnswer, appInstall time.Duration

	mu        sync.Mutex
	latest    *update.Release
	lookupErr error
	checkedAt time.Time
}

// checkTTL keeps GitHub's answer for a while: it allows 60 lookups an hour
// without a token.
const checkTTL = 10 * time.Minute

// restartDelay lets clients see that the update is in before rad goes.
const restartDelay = 2 * time.Second

func (u *selfUpdater) release(ctx context.Context, refresh bool) (*update.Release, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !refresh && !u.checkedAt.IsZero() && time.Since(u.checkedAt) < checkTTL {
		return u.latest, u.lookupErr
	}
	u.latest, u.lookupErr = u.client.Latest(ctx)
	u.checkedAt = time.Now()
	return u.latest, u.lookupErr
}

func (u *selfUpdater) Check(ctx context.Context, refresh bool) model.UpdateInfo {
	info := model.UpdateInfo{Current: api.Version, Busy: []string{}}
	if sessions, err := u.st.Sessions(ctx); err == nil {
		info.Busy = busyTitles(sessions)
	}
	rel, err := u.release(ctx, refresh)
	if rel != nil {
		info.Latest, info.URL = rel.Version, rel.URL
		info.Available = update.IsRelease(api.Version) && update.Compare(rel.Version, api.Version) > 0
	}
	info.Via, info.Reason = u.via()
	if info.Via != "" && err != nil {
		info.Reason = "Couldn't look up the latest release: " + err.Error()
	}
	return info
}

// via is how rad updates: "rad" or "app", or "" and why it can't.
func (u *selfUpdater) via() (string, string) {
	switch {
	case !update.IsRelease(api.Version):
		return "", fmt.Sprintf("rad %s is a development build, which doesn't update itself.", api.Version)
	case u.app != nil:
		return "app", ""
	}
	if err := checkReplaceable(u.exe); err != nil {
		msg := err.Error()
		return "", strings.ToUpper(msg[:1]) + msg[1:] + "."
	}
	if syscall.Access(filepath.Dir(u.exe), 2 /* W_OK */) != nil {
		name := "rad's user"
		if me, err := user.Current(); err == nil {
			name = me.Username
		}
		return "", fmt.Sprintf("%s can't replace %s: %s can't write to its folder. Reinstall rad in a folder of yours.", name, tilde(u.exe), name)
	}
	return "rad", ""
}

func (u *selfUpdater) Install(ctx context.Context, t *tasks.Task) error {
	rel, err := u.release(ctx, true)
	if err != nil {
		return fmt.Errorf("look up the latest release: %w", err)
	}
	if update.Compare(rel.Version, api.Version) <= 0 {
		return fmt.Errorf("rad %s is up to date", api.Version)
	}
	if u.app != nil {
		return u.installByApp(ctx, t, rel)
	}
	t.Println("Downloading rad %s (%s)…", rel.Version, u.client.Asset())
	old := u.exe + ".old"
	if err := update.KeepCopy(u.exe, old); err != nil {
		return writeHint(err, u.exe)
	}
	if err := u.client.Install(ctx, rel, u.exe); err != nil {
		return writeHint(err, u.exe)
	}
	t.SetVersion(rel.Version)
	t.Println("Installed rad %s; the previous version is %s.", rel.Version, tilde(old))
	t.Println("Restarting…")
	go func() {
		time.Sleep(restartDelay)
		u.restart()
	}()
	return nil
}

// installByApp asks the Server app to update itself, and follows it until it
// installs: then it quits, and rad with it, and opens again.
func (u *selfUpdater) installByApp(ctx context.Context, t *tasks.Task, rel *update.Release) error {
	msgs, stop := u.app.Subscribe()
	defer stop()
	t.Println("Asking the Remote Agent Server app to install %s…", rel.Version)
	u.app.Send(updateRequest{Event: "update", Version: rel.Version})
	deadline := time.NewTimer(u.appAnswer)
	defer deadline.Stop()
	answered := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			if !answered {
				return errors.New("the Remote Agent Server app didn't answer; update it from its menu on the Mac")
			}
			return errors.New("the Remote Agent Server app didn't install the update in time")
		case m := <-msgs:
			if m.Event != "updateStatus" {
				continue
			}
			if !answered {
				answered = true
				deadline.Reset(u.appInstall)
			}
			version := cmp.Or(m.Version, rel.Version)
			switch m.Status {
			case "checking":
				t.Println("The app is looking for the update…")
			case "downloading":
				t.Println("Downloading Remote Agent Server %s…", version)
			case "installing":
				t.SetVersion(version)
				t.Println("Installing Remote Agent Server %s. The app restarts, and rad with it.", version)
				return nil
			case "upToDate":
				return errors.New(cmp.Or(m.Message, "the app found no newer version"))
			case "failed":
				return errors.New(cmp.Or(m.Message, "the app couldn't update"))
			}
		}
	}
}

// restartError ends serve so that main starts exe in rad's place.
type restartError struct{ exe string }

func (e *restartError) Error() string { return "restarting into " + e.exe }

// execRestart replaces this process with exe, run with rad's arguments.
func execRestart(exe string) error {
	return syscall.Exec(exe, append([]string{exe}, os.Args[1:]...), os.Environ())
}
