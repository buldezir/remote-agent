package main

import (
	"errors"
	"flag"
	"fmt"
	"html"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"remote-agent/internal/config"
)

// rad runs in the background as a per-user LaunchAgent on macOS and a systemd
// user unit on Linux. Both capture the current PATH so harness CLIs resolve
// the same way they do in the user's shell.

const (
	launchLabel = "dev.remote-agent.rad"
	systemdUnit = "remote-agent.service"
)

// serviceEnv lists the variables copied into the service: PATH for the agent
// CLIs, the rest so it reads the same config as this shell.
var serviceEnv = []string{"PATH", "RAD_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME"}

func installService(args []string) error {
	fs := flag.NewFlagSet("install-service", flag.ExitOnError)
	uninstall := fs.Bool("uninstall", false, "stop and remove the service")
	fs.Parse(args)
	path, err := servicePath()
	if err != nil {
		return err
	}
	if *uninstall {
		removed, err := removeService()
		if err != nil {
			return err
		}
		if !removed {
			fmt.Println("no service installed at", path)
			return nil
		}
		fmt.Println("removed", path)
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	if strings.Contains(exe, "go-build") {
		return errors.New("this is a temporary `go run` binary; build rad (go build -o bin/rad ./cmd/rad) and run install-service from it")
	}
	// Reinstalling replaces the service's own rad. Any other running rad
	// would make the new service fail over and over.
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := checkNotRunning(); err != nil {
			return err
		}
	}
	if runtime.GOOS == "darwin" {
		return installLaunchAgent(path, exe)
	}
	return installSystemdUnit(path, exe)
}

// checkNotRunning fails if a rad, such as one in a terminal or the one the
// Remote Agent Server app runs, already serves this config's data dir.
func checkNotRunning() error {
	_, dataDir, err := config.Paths()
	if err != nil {
		return err
	}
	lock, err := lockDataDir(dataDir)
	if _, busy := err.(*busyError); busy {
		return errors.New("rad is already running with " + dataDir + " (in a terminal, or the Remote Agent Server app); stop it first")
	}
	if err != nil {
		return err
	}
	return lock.Close()
}

// servicePath is where the LaunchAgent plist or systemd unit lives.
func servicePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", launchLabel+".plist"), nil
	case "linux":
		dir := os.Getenv("XDG_CONFIG_HOME")
		if dir == "" {
			dir = filepath.Join(home, ".config")
		}
		return filepath.Join(dir, "systemd", "user", systemdUnit), nil
	}
	return "", fmt.Errorf("no background service support on %s; run `rad serve` under your own supervisor", runtime.GOOS)
}

// launchAgentLog is where the LaunchAgent sends rad's output.
func launchAgentLog() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Logs", "rad.log")
}

func installLaunchAgent(plist, exe string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	exec.Command("launchctl", "bootout", domain, plist).Run()
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(plist, []byte(launchAgentPlist(exe, home, launchAgentLog(), environ())), 0o644); err != nil {
		return err
	}
	if out, err := exec.Command("launchctl", "bootstrap", domain, plist).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s", err, out)
	}
	fmt.Printf("installed %s\nlogs: %s\n", plist, launchAgentLog())
	return nil
}

func launchAgentPlist(exe, home, logFile string, env [][2]string) string {
	esc := html.EscapeString
	var vars strings.Builder
	for _, kv := range append(env, [2]string{"HOME", home}) {
		fmt.Fprintf(&vars, "    <key>%s</key><string>%s</string>\n", kv[0], esc(kv[1]))
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>%s
  <key>EnvironmentVariables</key><dict>
%s  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Interactive</string>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, launchLabel, launchArgs(exe), vars.String(), esc(logFile), esc(logFile))
}

func installSystemdUnit(unit, exe string) error {
	// Without lingering, systemd stops user services when the user's last
	// session ends, e.g. when the SSH connection closes. Enabling it also
	// starts the user's service manager, which systemctl --user talks to.
	linger := lingering()
	if !linger {
		exec.Command("loginctl", "--no-ask-password", "enable-linger").Run()
		if linger = lingering(); linger {
			fmt.Println("enabled lingering, so rad keeps running after you log out and starts at boot")
			waitUserManager()
		}
	}
	if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(unit, []byte(systemdUnitFile(exe, environ())), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", systemdUnit}, {"restart", systemdUnit}} {
		if err := systemctl(args...); err != nil {
			if !linger {
				err = fmt.Errorf("%w\n\nsystemctl --user needs your user's service manager. Log in as this user (e.g. over SSH) rather than with su or sudo, or run:\n  sudo loginctl enable-linger %s", err, currentUser())
			}
			return err
		}
	}
	fmt.Printf("installed %s\nlogs: journalctl --user-unit %s -f\n", unit, strings.TrimSuffix(systemdUnit, ".service"))
	if !linger {
		fmt.Printf("\nrad stops when you log out. To keep it running and start it at boot, run:\n  sudo loginctl enable-linger %s\n", currentUser())
	}
	return nil
}

func systemdUnitFile(exe string, env [][2]string) string {
	var b strings.Builder
	b.WriteString("# Installed by `rad install-service`; remove with `rad install-service --uninstall`.\n")
	b.WriteString("[Unit]\nDescription=Remote Agent server (rad)\n\n[Service]\n")
	b.WriteString(execStart(exe) + "\n")
	for _, kv := range env {
		fmt.Fprintf(&b, "Environment=%s\n", systemdQuote(kv[0]+"="+kv[1]))
	}
	b.WriteString("Restart=always\nRestartSec=5\n\n[Install]\nWantedBy=default.target\n")
	return b.String()
}

// launchArgs is the LaunchAgent's ProgramArguments, which run exe.
func launchArgs(exe string) string {
	return "<array><string>" + html.EscapeString(exe) + "</string><string>serve</string></array>"
}

// execStart is the systemd unit's ExecStart line, which runs exe.
func execStart(exe string) string {
	return "ExecStart=" + strings.ReplaceAll(systemdQuote(exe), "$", "$$") + " serve"
}

// startsBinary reports whether a service file, plist or unit, runs exe.
func startsBinary(service, exe string) bool {
	return strings.Contains(service, launchArgs(exe)) || strings.Contains(service, execStart(exe)+"\n")
}

// systemdQuote double-quotes s for a unit file, escaping specifiers (%).
func systemdQuote(s string) string {
	s = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%").Replace(s)
	return `"` + s + `"`
}

func systemctl(args ...string) error {
	out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl --user %s: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// waitUserManager waits briefly for a just-started user service manager.
func waitUserManager() {
	for range 20 {
		if exec.Command("systemctl", "--user", "show-environment").Run() == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func lingering() bool {
	out, err := exec.Command("loginctl", "show-user", currentUser(), "--property=Linger", "--value").Output()
	return err == nil && strings.TrimSpace(string(out)) == "yes"
}

func currentUser() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return os.Getenv("USER")
}

// removeService stops the service and deletes its file. It reports whether
// one was installed.
func removeService() (bool, error) {
	path, err := servicePath()
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	switch runtime.GOOS {
	case "darwin":
		exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d", os.Getuid()), path).Run()
	case "linux":
		systemctl("disable", "--now", systemdUnit)
	}
	if err := os.Remove(path); err != nil {
		return true, err
	}
	if runtime.GOOS == "linux" {
		systemctl("daemon-reload")
		systemctl("reset-failed", systemdUnit)
	}
	return true, nil
}

func environ() [][2]string {
	var env [][2]string
	for _, k := range serviceEnv {
		if v := os.Getenv(k); v != "" {
			env = append(env, [2]string{k, v})
		}
	}
	return env
}
