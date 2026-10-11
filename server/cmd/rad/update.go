package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
	to := fs.String("to", "", "install into `dir` as rad-<version> instead of over this rad, and restart nothing (for the Remote Agent Server app)")
	fs.Parse(args)
	ctx := context.Background()
	u := update.Default(api.Version)

	if *to != "" {
		if *check {
			return errors.New("--check and --to don't go together")
		}
		return installInto(ctx, u, *to, *asJSON)
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

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
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
	if err := keepCopy(exe, old); err != nil {
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

// installInto puts the latest release into dir as rad-<version>, for the
// Remote Agent Server app, which runs it in place of its own copy.
func installInto(ctx context.Context, u *update.Client, dir string, asJSON bool) error {
	rel, err := u.Latest(ctx)
	if err != nil {
		return fmt.Errorf("look up the latest release: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(dir, "rad-"+rel.Version)
	if err := u.Install(ctx, rel, dst); err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"version": rel.Version, "path": dst})
	}
	fmt.Printf("installed rad %s at %s\n", rel.Version, dst)
	return nil
}

// checkReplaceable refuses binaries that aren't rad update's to replace.
func checkReplaceable(exe string) error {
	if strings.Contains(exe, "go-build") {
		return errors.New("this is a temporary `go run` binary; there is nothing to update")
	}
	if i := strings.Index(exe, ".app/Contents/"); i >= 0 {
		return fmt.Errorf("this rad is part of %s; update it from the app's menu", filepath.Base(exe[:i+len(".app")]))
	}
	return nil
}

// keepCopy saves exe as old, with a hard link where it can.
func keepCopy(exe, old string) error {
	os.Remove(old)
	if os.Link(exe, old) == nil {
		return nil
	}
	in, err := os.Open(exe)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(old, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
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
