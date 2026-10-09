package main

import (
	"errors"
	"flag"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

const launchLabel = "dev.remote-agent.rad"

// installLaunchAgent writes a per-user LaunchAgent that keeps `rad serve`
// running. It captures the current PATH so harness CLIs resolve the same way
// they do in the user's shell.
func installLaunchAgent(args []string) error {
	fs := flag.NewFlagSet("install-launchagent", flag.ExitOnError)
	uninstall := fs.Bool("uninstall", false, "remove the LaunchAgent")
	fs.Parse(args)
	if runtime.GOOS != "darwin" {
		return errors.New("LaunchAgents are macOS-only; use a systemd user unit elsewhere")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", launchLabel+".plist")
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	exec.Command("launchctl", "bootout", domain, plist).Run()
	if *uninstall {
		os.Remove(plist)
		fmt.Println("removed", plist)
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	logDir := filepath.Join(home, "Library", "Logs")
	esc := html.EscapeString
	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array><string>%s</string><string>serve</string></array>
  <key>EnvironmentVariables</key><dict>
    <key>PATH</key><string>%s</string>
    <key>HOME</key><string>%s</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Interactive</string>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, launchLabel, esc(exe), esc(os.Getenv("PATH")), esc(home),
		esc(filepath.Join(logDir, "rad.log")), esc(filepath.Join(logDir, "rad.log")))
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(plist, []byte(body), 0o644); err != nil {
		return err
	}
	if out, err := exec.Command("launchctl", "bootstrap", domain, plist).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %v: %s", err, out)
	}
	fmt.Printf("installed %s\nlogs: %s\n", plist, filepath.Join(logDir, "rad.log"))
	return nil
}
