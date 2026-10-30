package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"remote-agent/internal/api"
	"remote-agent/internal/config"
	"remote-agent/internal/model"
	"remote-agent/internal/update"
)

// checkResult is what `rad update --check --json` prints.
type checkResult struct {
	Current     string   `json:"current"`
	Latest      string   `json:"latest"`
	URL         string   `json:"url"`
	Available   bool     `json:"available"`
	Development bool     `json:"development"`
	Busy        []string `json:"busy"` // sessions a restart would interrupt
}

// updateRad replaces this rad with the latest release and restarts the
// background service if that is what runs it.
func updateRad(args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	check := fs.Bool("check", false, "only report whether a newer release is out")
	now := fs.Bool("now", false, "restart the service even if sessions are running")
	force := fs.Bool("force", false, "install the latest release over a development build, or again")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	busyOnly := fs.Bool("busy", false, "only list the sessions a restart would interrupt, without looking for a release (for the Remote Agent Server app)")
	fs.Parse(args)
	ctx := context.Background()
	u := update.Default(api.Version)

	if *busyOnly {
		return printBusy(busySessions(ctx), *asJSON)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	if app := appBundle(exe); app != "" && !*check {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		a := &appUpdate{cfg: cfg, app: app, now: *now, out: os.Stdout, poll: 500 * time.Millisecond, restart: 2 * time.Minute}
		return a.run(ctx)
	}
	rel, err := u.Latest(ctx)
	if err != nil {
		return fmt.Errorf("look up the latest release: %w", err)
	}
	dev := !update.IsRelease(api.Version)
	newer := !dev && update.Compare(rel.Version, api.Version) > 0
	if *check {
		return printCheck(ctx, rel, dev, newer, *asJSON)
	}

	if err := checkReplaceable(exe); err != nil {
		return err
	}
	switch {
	case dev && !*force:
		return fmt.Errorf("rad %s is a development build; `rad update --force` replaces it with %s", api.Version, rel.Version)
	case !dev && !newer && !*force:
		fmt.Printf("rad %s is up to date\n", api.Version)
		return nil
	}
	service, ours := installedService(exe)
	restart := ours && serviceActive()
	if restart && !*now {
		if busy := busySessions(ctx); len(busy) > 0 {
			return fmt.Errorf("%s; updating restarts rad and interrupts them. Run `rad update` again when they're done, or pass --now", sessionsRunning(busy))
		}
	}

	fmt.Printf("downloading rad %s (%s)\n", rel.Version, u.Asset())
	old := exe + ".old"
	if err := update.KeepCopy(exe, old); err != nil {
		return writeHint(err, exe)
	}
	if err := u.Install(ctx, rel, exe); err != nil {
		return writeHint(err, exe)
	}
	fmt.Printf("installed rad %s at %s; the previous version is %s\n", rel.Version, tilde(exe), tilde(old))
	switch {
	case restart:
		return restartInto(rel.Version)
	case ours:
		fmt.Printf("the background service is stopped; it runs %s when it starts\n", rel.Version)
	case radRunning():
		fmt.Printf("the rad that is running is still the old version; restart it to use %s\n", rel.Version)
	}
	if service != "" && !ours {
		fmt.Printf("note: the background service (%s) runs another rad binary; update that one with its own `rad update`\n", tilde(service))
	}
	return nil
}

func printCheck(ctx context.Context, rel *update.Release, dev, newer, asJSON bool) error {
	busy := busySessions(ctx)
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(checkResult{
			Current: api.Version, Latest: rel.Version, URL: rel.URL,
			Available: newer, Development: dev, Busy: busy,
		})
	}
	switch {
	case dev:
		fmt.Printf("rad %s is a development build; the latest release is %s: %s\n", api.Version, rel.Version, rel.URL)
	case newer:
		fmt.Printf("rad %s is available (this is %s): %s\n", rel.Version, api.Version, rel.URL)
		if len(busy) > 0 {
			fmt.Println(sessionsRunning(busy))
		}
	default:
		fmt.Printf("rad %s is up to date\n", api.Version)
	}
	return nil
}

// printBusy prints what `rad update --busy` reports: the sessions a restart
// would interrupt. The Remote Agent Server app asks before an update that
// restarts it would.
func printBusy(busy []string, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(struct {
			Busy []string `json:"busy"`
		}{busy})
	}
	if len(busy) == 0 {
		fmt.Println("no session is busy")
		return nil
	}
	fmt.Println(sessionsRunning(busy))
	return nil
}

// checkReplaceable refuses binaries that aren't rad update's to replace.
func checkReplaceable(exe string) error {
	if strings.Contains(exe, "go-build") {
		return errors.New("this is a temporary `go run` binary; there is nothing to update")
	}
	if app := appBundle(exe); app != "" {
		return fmt.Errorf("this rad is part of %s; update it from the app's menu", filepath.Base(app))
	}
	return nil
}

// appBundle is the Mac app that exe is part of, such as
// /Applications/Remote Agent Server.app, or "".
func appBundle(exe string) string {
	if i := strings.Index(exe, ".app/Contents/"); i >= 0 {
		return exe[:i+len(".app")]
	}
	return ""
}

func writeHint(err error, exe string) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("%w\n\nrad can't replace %s. Run rad update as the user who owns it, or reinstall rad in a folder you own", err, exe)
	}
	return err
}

// installedService returns the background service's file, if there is one,
// and whether it runs exe.
func installedService(exe string) (path string, runsExe bool) {
	path, err := servicePath()
	if err != nil {
		return "", false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return path, startsBinary(string(b), exe)
}

func launchTarget() string { return fmt.Sprintf("gui/%d/%s", os.Getuid(), launchLabel) }

// serviceActive reports whether the background service is running.
func serviceActive() bool {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("launchctl", "print", launchTarget()).Run() == nil
	case "linux":
		return exec.Command("systemctl", "--user", "is-active", "--quiet", systemdUnit).Run() == nil
	}
	return false
}

// restartInto restarts the background service and waits for it to answer
// as the new version.
func restartInto(version string) error {
	fmt.Println("restarting the background service")
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("launchctl", "kickstart", "-k", launchTarget()).CombinedOutput(); err != nil {
			return fmt.Errorf("launchctl kickstart: %v: %s", err, out)
		}
	} else if err := systemctl("restart", systemdUnit); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
		if _, h := health(cfg, ""); h.Version == version {
			fmt.Println("rad", version, "is running")
			return nil
		}
	}
	log := "journalctl --user-unit " + strings.TrimSuffix(systemdUnit, ".service")
	if runtime.GOOS == "darwin" {
		log = tilde(launchAgentLog())
	}
	return fmt.Errorf("the service hasn't come back as rad %s in 30 s; see its log: %s", version, log)
}

// radRunning reports whether a rad serves this config's data dir.
func radRunning() bool {
	_, dataDir, err := config.Paths()
	if err != nil {
		return false
	}
	if _, err := os.Stat(dataDir); err != nil {
		return false
	}
	lock, err := lockDataDir(dataDir)
	if err == nil {
		lock.Close()
		return false
	}
	_, busy := err.(*busyError)
	return busy
}

// busySessions lists the sessions a restart would interrupt. It reads the
// database next to the running rad, which WAL mode allows.
func busySessions(ctx context.Context) []string {
	_, dataDir, err := config.Paths()
	if err != nil {
		return []string{}
	}
	cfg := &config.Config{DataDir: dataDir}
	if _, err := os.Stat(cfg.DBPath()); err != nil {
		return []string{}
	}
	st, err := openStore(cfg)
	if err != nil {
		return []string{}
	}
	defer st.Close()
	sessions, err := st.Sessions(ctx)
	if err != nil {
		return []string{}
	}
	return busyTitles(sessions)
}

// busyTitles names the sessions with a turn running or an approval pending.
func busyTitles(sessions []*model.Session) []string {
	titles := []string{}
	for _, s := range sessions {
		if s.Status == model.SessionRunning || s.Status == model.SessionAwaitingApproval {
			titles = append(titles, cmp.Or(s.Title, "Untitled"))
		}
	}
	return titles
}

func sessionsRunning(titles []string) string {
	if len(titles) == 1 {
		return fmt.Sprintf("1 session is busy (%s)", titles[0])
	}
	return fmt.Sprintf("%d sessions are busy (%s)", len(titles), strings.Join(titles, ", "))
}
