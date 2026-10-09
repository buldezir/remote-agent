package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"remote-agent/internal/config"
	"remote-agent/internal/gitx"
	"remote-agent/internal/model"
)

// removal is everything rad created on this machine.
type removal struct {
	service    string // LaunchAgent plist or systemd unit, if installed
	configPath string
	dataDir    string // database, protocol logs, session worktrees
	logFile    string // LaunchAgent output
	worktrees  []worktree
	repos      []string // project repos that may hold refs/ra/* checkpoints
	branches   map[string][]string
	cfg        *config.Config
	serverID   string
}

type worktree struct {
	repo, path string
	dirty      bool
}

// uninstall removes rad's service, config, data, session worktrees and the
// checkpoint refs it wrote into project repos. Session branches and the rad
// binary are kept.
func uninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	yes := fs.Bool("yes", false, "don't ask for confirmation")
	fs.Parse(args)
	ctx := context.Background()

	r, err := findInstall(ctx)
	if err != nil {
		return err
	}
	if r.empty() {
		fmt.Println("nothing to remove: no rad config, data or service found")
		return nil
	}
	r.describe()
	running := r.running()
	if running != "" && r.service == "" {
		return fmt.Errorf("rad is running at %s; stop it, then run `rad uninstall` again", running)
	}
	if !*yes && !confirm("\nRemove all of this? [y/N] ") {
		return errors.New("cancelled; nothing was removed")
	}

	if r.service != "" {
		if _, err := removeService(); err != nil {
			return fmt.Errorf("remove service: %w", err)
		}
		fmt.Println("removed", tilde(r.service))
		if addr := r.waitStopped(); addr != "" {
			return fmt.Errorf("rad is still running at %s (not as the service); stop it, then run `rad uninstall` again", addr)
		}
	}
	var prune []string // repos whose worktree metadata outlives the deleted data directory
	for _, w := range r.worktrees {
		if err := gitx.RemoveWorktree(ctx, w.repo, w.path); err != nil {
			fmt.Fprintf(os.Stderr, "warning: git worktree remove %s: %v\n", tilde(w.path), err)
			prune = append(prune, w.repo)
		}
	}
	for _, repo := range r.repos {
		if err := gitx.DeleteRefs(ctx, repo, "refs/ra/"); err != nil {
			fmt.Fprintf(os.Stderr, "warning: delete refs/ra/ in %s: %v\n", tilde(repo), err)
		}
	}
	if len(r.worktrees) > 0 || len(r.repos) > 0 {
		fmt.Printf("removed %d worktree(s) and the checkpoints in %d repo(s)\n", len(r.worktrees), len(r.repos))
	}
	for _, p := range []string{r.dataDir, r.configPath, r.logFile} {
		if p == "" {
			continue
		}
		if err := os.RemoveAll(p); err != nil {
			return err
		}
		if exists(p) {
			continue
		}
		fmt.Println("removed", tilde(p))
	}
	slices.Sort(prune)
	for _, repo := range slices.Compact(prune) {
		gitx.PruneWorktrees(ctx, repo)
	}
	// The config's directory (or RAD_HOME), when nothing else is in it.
	if dir := filepath.Dir(r.configPath); exists(dir) && os.Remove(dir) == nil {
		fmt.Println("removed", tilde(dir))
	}

	fmt.Println("\nrad is uninstalled. Kept:")
	if exe, err := os.Executable(); err == nil && !strings.Contains(exe, "go-build") {
		fmt.Printf("  the rad binary: %s\n", tilde(exe))
	}
	for _, repo := range sortedKeys(r.branches) {
		fmt.Printf("  session branches in %s: %s\n", tilde(repo), strings.Join(r.branches[repo], ", "))
	}
	if runtime.GOOS == "linux" && r.service != "" && lingering() {
		fmt.Printf("  lingering for %s; turn it off with `sudo loginctl disable-linger %s` if nothing else needs it\n", currentUser(), currentUser())
	}
	fmt.Println("Paired phones can no longer connect; remove the server in the app.")
	return nil
}

func findInstall(ctx context.Context) (*removal, error) {
	configPath, dataDir, err := config.Paths()
	if err != nil {
		return nil, err
	}
	r := &removal{configPath: configPath, dataDir: dataDir, branches: map[string][]string{}}
	if svc, err := servicePath(); err == nil && exists(svc) {
		r.service = svc
	}
	if runtime.GOOS == "darwin" && exists(launchAgentLog()) {
		r.logFile = launchAgentLog()
	}
	// Load only an existing config: Load writes the default one when missing.
	if exists(configPath) {
		if r.cfg, err = config.Load(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v; using the default port and listen addresses\n", err)
		}
	}
	if r.cfg == nil {
		r.cfg = config.Defaults()
		r.cfg.ConfigPath, r.cfg.DataDir = configPath, dataDir
	}
	if !exists(r.cfg.DBPath()) {
		return r, nil
	}
	st, err := openStore(r.cfg)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	if r.serverID, err = st.ServerID(ctx); err != nil {
		return nil, err
	}
	projects, err := st.Projects(ctx)
	if err != nil {
		return nil, err
	}
	sessions, err := st.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	repoOf := map[string]string{}
	for _, p := range projects {
		if p.IsGitRepo && exists(p.Path) {
			repoOf[p.ID] = p.Path
			r.repos = append(r.repos, p.Path)
		}
	}
	for _, s := range sessions {
		repo := repoOf[s.ProjectID]
		if s.Workspace.Kind != model.WorkspaceWorktree || repo == "" {
			continue
		}
		if s.Workspace.Branch != "" && gitx.BranchExists(ctx, repo, s.Workspace.Branch) &&
			!slices.Contains(r.branches[repo], s.Workspace.Branch) {
			r.branches[repo] = append(r.branches[repo], s.Workspace.Branch)
		}
		if exists(s.Workspace.Path) {
			r.worktrees = append(r.worktrees, worktree{repo: repo, path: s.Workspace.Path, dirty: gitx.Dirty(ctx, s.Workspace.Path)})
		}
	}
	return r, nil
}

func (r *removal) empty() bool {
	return r.service == "" && r.logFile == "" && !exists(r.configPath) && !exists(r.dataDir)
}

func (r *removal) describe() {
	fmt.Println("This removes rad from this computer:")
	line := func(what, path string) {
		if path != "" && exists(path) {
			fmt.Printf("  %-12s %s\n", what, tilde(path))
		}
	}
	line("service", r.service)
	line("config", r.configPath)
	line("data", r.dataDir)
	line("log", r.logFile)
	if len(r.worktrees) > 0 {
		fmt.Printf("  %-12s %d session worktree(s) in the data directory\n", "worktrees", len(r.worktrees))
		for _, w := range r.worktrees {
			if w.dirty {
				fmt.Printf("  %-12s   uncommitted changes will be lost: %s\n", "", tilde(w.path))
			}
		}
	}
	if len(r.repos) > 0 {
		fmt.Printf("  %-12s refs/ra/* in %d project repo(s)\n", "checkpoints", len(r.repos))
	}
}

// running returns the address of this install's server if it answers.
func (r *removal) running() string {
	var hosts []string
	if len(r.cfg.Listen) > 0 {
		hosts = r.cfg.Listen
	} else {
		hosts = []string{net.JoinHostPort("127.0.0.1", fmt.Sprint(r.cfg.Port))}
	}
	client := http.Client{Timeout: 2 * time.Second}
	for _, hp := range hosts {
		host, port, err := net.SplitHostPort(hp)
		if err != nil {
			continue
		}
		if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
			hp = net.JoinHostPort("127.0.0.1", port)
		}
		resp, err := client.Get("http://" + hp + "/v1/health")
		if err != nil {
			continue
		}
		var info struct {
			ServerID string `json:"serverId"`
		}
		json.NewDecoder(resp.Body).Decode(&info)
		resp.Body.Close()
		// Another install (another RAD_HOME) may be using the port.
		if r.serverID == "" || info.ServerID == r.serverID {
			return "http://" + hp
		}
	}
	return ""
}

// waitStopped gives the stopped service time to exit; it returns the address
// of a server that is still up.
func (r *removal) waitStopped() string {
	deadline := time.Now().Add(10 * time.Second)
	for {
		addr := r.running()
		if addr == "" || time.Now().After(deadline) {
			return addr
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func confirm(prompt string) bool {
	fmt.Print(prompt)
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func tilde(p string) string {
	home, err := os.UserHomeDir()
	if err == nil && (p == home || strings.HasPrefix(p, home+string(filepath.Separator))) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
