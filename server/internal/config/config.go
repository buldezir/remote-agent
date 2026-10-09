// Package config loads rad's YAML config and resolves its directories.
//
// Layout (overridable with RAD_HOME, which puts both under one directory):
//
//	config: ~/.config/remote-agent/config.yaml
//	data:   ~/Library/Application Support/remote-agent (macOS) or ~/.local/share/remote-agent
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Command struct {
	Command string            `yaml:"command,omitempty"`
	Args    []string          `yaml:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
}

type ACPAgent struct {
	ID      string            `yaml:"id"`
	Name    string            `yaml:"name,omitempty"`
	Command string            `yaml:"command"`
	Args    []string          `yaml:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`
}

type Config struct {
	Name        string             `yaml:"name,omitempty"` // shown in the app once paired; defaults to the host name
	Port        int                `yaml:"port,omitempty"`
	LAN         bool               `yaml:"lan,omitempty"`       // put LAN addresses in the pairing link
	PairURLs    []string           `yaml:"pair_urls,omitempty"` // the pairing link's addresses, instead of the detected ones
	Listen      []string           `yaml:"listen,omitempty"`    // host:port list to bind instead of 0.0.0.0:port
	Roots       DirList            `yaml:"roots,omitempty"`
	IdleTimeout Duration           `yaml:"idle_timeout,omitempty"`
	Fake        bool               `yaml:"fake,omitempty"` // expose the scripted "fake" harness
	ACP         []ACPAgent         `yaml:"acp,omitempty"`
	Harness     map[string]Command `yaml:"harness,omitempty"`

	// Resolved, not from file.
	ConfigPath string `yaml:"-"`
	DataDir    string `yaml:"-"`
}

// DirList is a list of directories. A bare ~ is null in YAML, which would
// silently drop the entry, so it is rejected.
type DirList []string

func (p *DirList) UnmarshalYAML(n *yaml.Node) error {
	for _, item := range n.Content {
		if item.Tag == "!!null" {
			return fmt.Errorf(`line %d: ~ alone means "nothing" in YAML; write "~" in quotes for the home directory`, item.Line)
		}
	}
	return n.Decode((*[]string)(p))
}

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	d.Duration = v
	return err
}

// MarshalText writes 30m rather than 30m0s.
func (d Duration) MarshalText() ([]byte, error) {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return []byte(s), nil
}

const defaultFile = `# rad configuration. See the README for details.

# The server's name in the app, set when a phone pairs (default: this computer's host name).
# name: Work laptop
port: 7421
# rad listens on all interfaces. The pairing link lists its Tailscale address;
# set lan: true to add LAN addresses for a phone on the same Wi-Fi.
lan: false
# Or list the pairing link's addresses yourself; bare hosts get http:// and the port.
# pair_urls: [my-mac.tail1234.ts.net, 192.168.1.20]
# Directories the phone may browse and add as projects.
roots: [~/projects]
# Stop idle agent processes after this long (they resume on the next prompt).
idle_timeout: 30m

# Agents that speak the Agent Client Protocol; add any other ACP agent here.
acp:
  - {id: cursor,   name: Cursor,   command: cursor-agent, args: [acp]}
  - {id: opencode, name: OpenCode, command: opencode,     args: [acp]}
  - {id: gemini,   name: Gemini,   command: gemini,       args: [--acp]}

# Commands for the built-in harnesses; each also takes args and env.
harness:
  claude: {command: claude}
  codex: {command: codex}
`

func Defaults() *Config {
	c, err := parse([]byte(defaultFile))
	if err != nil {
		panic(err)
	}
	return c
}

// parse decodes a config file. Unknown keys are errors, so typos don't go
// unnoticed.
func parse(raw []byte) (*Config, error) {
	c := &Config{}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return c, nil
}

// Paths returns where the config file and the data directory live, without
// creating either.
func Paths() (configPath, dataDir string, err error) {
	if h := os.Getenv("RAD_HOME"); h != "" {
		return filepath.Join(h, "config.yaml"), filepath.Join(h, "data"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	cfgHome := os.Getenv("XDG_CONFIG_HOME")
	if cfgHome == "" {
		cfgHome = filepath.Join(home, ".config")
	}
	configPath = filepath.Join(cfgHome, "remote-agent", "config.yaml")
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
	configPath, dataDir, err := Paths()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(configPath)
	if errors.Is(err, fs.ErrNotExist) {
		raw, err = []byte(defaultFile), writeConfig(configPath, []byte(defaultFile))
	}
	if err != nil {
		return nil, err
	}
	c, err := parse(raw)
	if err != nil {
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

func writeConfig(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (c *Config) normalize() error {
	c.Name = strings.TrimSpace(c.Name)
	if c.Port == 0 {
		c.Port = 7421
	}
	for i, u := range c.PairURLs {
		norm, err := BaseURL(u, c.Port)
		if err != nil {
			return fmt.Errorf("pair_urls: %w", err)
		}
		c.PairURLs[i] = norm
	}
	if c.IdleTimeout.Duration == 0 {
		c.IdleTimeout.Duration = 30 * time.Minute
	}
	if c.Harness == nil {
		c.Harness = map[string]Command{}
	}
	for i, r := range c.Roots {
		if r == "" {
			return errors.New("roots: empty entry")
		}
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

// BaseURL turns "host", "host:port" or "http(s)://host[:port]" into a base
// URL. A bare host gets http:// and the given port.
func BaseURL(s string, port int) (string, error) {
	in := strings.TrimSpace(s)
	s = in
	if !strings.Contains(s, "://") {
		if _, _, err := net.SplitHostPort(s); err != nil {
			s = net.JoinHostPort(strings.Trim(s, "[]"), strconv.Itoa(port))
		}
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.User != nil {
		return "", fmt.Errorf("%q is not a host, host:port or http(s)://host[:port]", in)
	}
	return u.Scheme + "://" + u.Host, nil
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
