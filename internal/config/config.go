// Package config loads rad's TOML config and resolves its directories.
//
// Layout (overridable with RAD_HOME, which puts both under one directory):
//
//	config: ~/.config/remote-agent/config.toml
//	data:   ~/Library/Application Support/remote-agent (macOS) or ~/.local/share/remote-agent
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type Command struct {
	Command string            `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
}

type ACPAgent struct {
	ID      string            `toml:"id"`
	Name    string            `toml:"name"`
	Command string            `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
}

type Config struct {
	Name        string             `toml:"name"` // shown in the app once paired; defaults to the host name
	Port        int                `toml:"port"`
	LAN         bool               `toml:"lan"`
	Listen      []string           `toml:"listen"` // explicit host:port list; overrides port/lan discovery
	Roots       []string           `toml:"roots"`
	IdleTimeout Duration           `toml:"idle_timeout"`
	Fake        bool               `toml:"fake"` // expose the scripted "fake" harness
	ACP         []ACPAgent         `toml:"acp"`
	Harness     map[string]Command `toml:"harness"`

	// Resolved, not from file.
	ConfigPath string `toml:"-"`
	DataDir    string `toml:"-"`
}

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	d.Duration = v
	return err
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

const defaultFile = `# rad configuration. See README for details.
# The server's name in the app, set when a phone pairs (default: this computer's host name).
# name = "Work laptop"
port = 7421
# Also listen on LAN addresses (default: loopback + Tailscale only).
lan = false
# Directories the phone may browse and add as projects.
roots = ["~/projects"]
# Stop idle agent processes after this long (they resume on the next prompt).
idle_timeout = "30m"

acp = [
  { id = "cursor",   name = "Cursor",   command = "cursor-agent", args = ["acp"] },
  { id = "opencode", name = "OpenCode", command = "opencode",     args = ["acp"] },
  { id = "gemini",   name = "Gemini",   command = "gemini",       args = ["--acp"] },
]

[harness.claude]
command = "claude"

[harness.codex]
command = "codex"
`

func Defaults() *Config {
	c := &Config{}
	if _, err := toml.Decode(defaultFile, c); err != nil {
		panic(err)
	}
	return c
}

func dirs() (configPath, dataDir string, err error) {
	if h := os.Getenv("RAD_HOME"); h != "" {
		return filepath.Join(h, "config.toml"), filepath.Join(h, "data"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	cfgHome := os.Getenv("XDG_CONFIG_HOME")
	if cfgHome == "" {
		cfgHome = filepath.Join(home, ".config")
	}
	configPath = filepath.Join(cfgHome, "remote-agent", "config.toml")
	switch {
	case os.Getenv("XDG_DATA_HOME") != "":
		dataDir = filepath.Join(os.Getenv("XDG_DATA_HOME"), "remote-agent")
	case runtime.GOOS == "darwin":
		dataDir = filepath.Join(home, "Library", "Application Support", "remote-agent")
	default:
		dataDir = filepath.Join(home, ".local", "share", "remote-agent")
	}
	return configPath, dataDir, nil
}

// Load reads the config file, writing the default one first if it is missing.
func Load() (*Config, error) {
	configPath, dataDir, err := dirs()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(configPath)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(configPath, []byte(defaultFile), 0o600); err != nil {
			return nil, err
		}
		raw = []byte(defaultFile)
	} else if err != nil {
		return nil, err
	}
	c := &Config{}
	if _, err := toml.Decode(string(raw), c); err != nil {
		return nil, fmt.Errorf("%s: %w", configPath, err)
	}
	c.ConfigPath, c.DataDir = configPath, dataDir
	if err := c.normalize(); err != nil {
		return nil, fmt.Errorf("%s: %w", configPath, err)
	}
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) normalize() error {
	c.Name = strings.TrimSpace(c.Name)
	if c.Port == 0 {
		c.Port = 7421
	}
	if c.IdleTimeout.Duration == 0 {
		c.IdleTimeout.Duration = 30 * time.Minute
	}
	if c.Harness == nil {
		c.Harness = map[string]Command{}
	}
	for i, r := range c.Roots {
		p, err := ExpandHome(r)
		if err != nil {
			return err
		}
		c.Roots[i] = p
	}
	seen := map[string]bool{}
	for _, a := range c.ACP {
		if a.ID == "" || a.Command == "" {
			return fmt.Errorf("acp agent needs id and command: %+v", a)
		}
		if seen[a.ID] {
			return fmt.Errorf("duplicate acp agent id %q", a.ID)
		}
		seen[a.ID] = true
	}
	return nil
}

func (c *Config) DBPath() string       { return filepath.Join(c.DataDir, "rad.db") }
func (c *Config) WorktreesDir() string { return filepath.Join(c.DataDir, "worktrees") }
func (c *Config) LogsDir() string      { return filepath.Join(c.DataDir, "logs") }
func (c *Config) HarnessCmd(id string) Command {
	cmd := c.Harness[id]
	if cmd.Command == "" {
		cmd.Command = id
	}
	return cmd
}

func ExpandHome(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
	}
	return filepath.Abs(p)
}
