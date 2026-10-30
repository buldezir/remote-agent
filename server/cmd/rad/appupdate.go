package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"remote-agent/internal/config"
	"remote-agent/internal/model"
)

// appUpdate is `rad update` for the rad inside the Remote Agent Server app,
// which the app brings when it updates itself. It asks the rad the app runs
// for the update (server.update), as Server Settings does in the apps, so
// the app installs it without asking anyone at the Mac, then waits for the
// app to come back with the new rad.
type appUpdate struct {
	cfg *config.Config
	app string // the app's bundle, such as /Applications/Remote Agent Server.app
	now bool   // go ahead even if sessions are busy
	out io.Writer
	// poll is how often it asks how the update goes, and restart how long
	// the app may take to come back.
	poll, restart time.Duration
}

func (a *appUpdate) run(ctx context.Context) error {
	name := filepath.Base(a.app)
	base, h := health(a.cfg, "")
	if base == "" {
		return fmt.Errorf("rad isn't running: open %s, then run `rad update` again", name)
	}
	// A device of its own, for as long as the update takes.
	st, err := openStore(a.cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	dev, tok, err := st.AddDevice(ctx, "rad update")
	if err != nil {
		return err
	}
	revoke := sync.OnceFunc(func() { st.DeleteDevice(context.WithoutCancel(ctx), dev.ID) })
	defer revoke()
	c, err := connect(ctx, base, tok)
	if err != nil {
		return err
	}
	defer c.ws.CloseNow()

	var info model.UpdateInfo
	if err := c.call(ctx, "server.checkUpdate", map[string]any{"refresh": true}, &info); err != nil {
		return err
	}
	switch {
	case info.Via == "rad":
		return fmt.Errorf("the rad that is running isn't %s's; quit it, or update it with its own `rad update`", name)
	case info.Via == "" || info.Latest == "":
		return errors.New(strings.TrimSuffix(info.Reason, "."))
	case !info.Available:
		fmt.Fprintf(a.out, "rad %s is up to date\n", info.Current)
		return nil
	case len(info.Busy) > 0 && !a.now:
		return fmt.Errorf("%s; updating restarts %s, and rad with it, which interrupts them. Run `rad update` again when they're done, or pass --now", sessionsRunning(info.Busy), name)
	}

	var task model.Task
	if err := c.call(ctx, "server.update", nil, &task); err != nil {
		return err
	}
	printed := 0
	for {
		var lines []string
		if task.Output != "" {
			lines = strings.Split(task.Output, "\n")
		}
		for _, line := range lines[min(printed, len(lines)):] {
			fmt.Fprintln(a.out, line)
		}
		printed = len(lines)
		if task.Status != model.TaskRunning {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(a.poll):
		}
		if !a.taskGet(ctx, c, &task) {
			// The app quit to install the update, and rad with it.
			break
		}
	}
	if task.Status == model.TaskFailed {
		return errors.New(task.Error)
	}
	revoke()

	version := cmp.Or(task.Version, info.Latest)
	for deadline := time.Now().Add(a.restart); time.Now().Before(deadline); {
		if _, h := health(a.cfg, h.ServerID); h.Version == version {
			fmt.Fprintln(a.out, "rad", version, "is running")
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(a.poll):
		}
	}
	return fmt.Errorf("%s hasn't come back with rad %s; Open Log in its menu shows why", name, version)
}

// taskGet updates task, unless rad has gone.
func (a *appUpdate) taskGet(ctx context.Context, c *client, task *model.Task) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var t model.Task
	if err := c.call(ctx, "task.get", map[string]string{"id": task.ID}, &t); err != nil {
		return false
	}
	*task = t
	return true
}
