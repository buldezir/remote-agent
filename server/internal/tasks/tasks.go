// Package tasks runs the maintenance jobs that clients start: updating rad
// or an agent, and signing an agent in again. Commands run in a
// pseudo-terminal, so they behave as they would in a terminal and can ask
// for input, such as a code to paste; clients see their output as a
// terminal would show it, and poll it while the task runs.
package tasks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"remote-agent/internal/harness"
	"remote-agent/internal/model"
	"remote-agent/internal/store"
)

// ErrNotFound is for a task that isn't running and has been forgotten, or
// never was.
var ErrNotFound = errors.New("no such task")

// Finished tasks are kept this long, and at most keep of them, so that a
// client that reconnects sees how one ended.
const (
	keepFor = 15 * time.Minute
	keep    = 20
)

// The terminal commands run in. CLIs print sign-in links on one line,
// however long, and as hyperlinks (OSC 8) too.
const (
	cols = 120
	rows = 50
)

type Manager struct {
	log    *slog.Logger
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu    sync.Mutex
	tasks []*Task // oldest first
}

func NewManager(log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{log: log, ctx: ctx, cancel: cancel}
}

// Task is one job. Its run function reports through it.
type Task struct {
	mu        sync.Mutex
	t         model.Task
	screen    *screen
	screenURL bool      // the url is the output's last web address, rather than what SetURL sets
	input     io.Writer // the running command's terminal, if it takes input
	cancel    context.CancelFunc
}

// Start runs run as a new task of kind, for harness if it's about an agent,
// unless one of the same kind and harness is running: then it returns that
// one. A nil error from run is success; once ctx ends, the task is
// cancelled. Its url is the last web address in its output.
func (m *Manager) Start(kind model.TaskKind, harnessID string, run func(ctx context.Context, t *Task) error) model.Task {
	return m.start(model.Task{Kind: kind, Harness: harnessID}, true, run)
}

// StartForSession runs run as a new task of kind for a session, as Start
// does. Its url is only what run sets (SetURL), and task.list shows it
// only with the session's.
func (m *Manager) StartForSession(kind model.TaskKind, sessionID string, run func(ctx context.Context, t *Task) error) model.Task {
	return m.start(model.Task{Kind: kind, SessionID: sessionID}, false, run)
}

func (m *Manager) start(proto model.Task, screenURL bool, run func(ctx context.Context, t *Task) error) model.Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tasks {
		if s := t.Snapshot(); s.Kind == proto.Kind && s.Harness == proto.Harness && s.SessionID == proto.SessionID && s.Status == model.TaskRunning {
			return s
		}
	}
	m.prune()
	ctx, cancel := context.WithCancel(m.ctx)
	proto.ID, proto.Status, proto.StartedAt = store.NewID(), model.TaskRunning, time.Now().UTC()
	t := &Task{t: proto, screen: newScreen(rows), screenURL: screenURL, cancel: cancel}
	m.tasks = append(m.tasks, t)
	attrs := []any{"task", proto.ID, "kind", proto.Kind}
	if proto.Harness != "" {
		attrs = append(attrs, "harness", proto.Harness)
	}
	if proto.SessionID != "" {
		attrs = append(attrs, "session", proto.SessionID)
	}
	m.log.Info("task started", attrs...)
	m.wg.Go(func() {
		defer cancel()
		err := run(ctx, t)
		t.finish(ctx, err)
		s := t.Snapshot()
		m.log.Info("task ended", append(attrs, "status", s.Status, "err", s.Error)...)
	})
	return t.Snapshot()
}

// prune forgets finished tasks past keepFor, and beyond the newest keep.
func (m *Manager) prune() {
	finished := 0
	for i := len(m.tasks) - 1; i >= 0; i-- {
		s := m.tasks[i].Snapshot()
		if s.EndedAt == nil {
			continue
		}
		finished++
		if finished > keep || time.Since(*s.EndedAt) > keepFor {
			m.tasks = slices.Delete(m.tasks, i, i+1)
		}
	}
}

func (m *Manager) task(id string) (*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tasks {
		if t.t.ID == id {
			return t, nil
		}
	}
	return nil, ErrNotFound
}

func (m *Manager) Get(id string) (model.Task, error) {
	t, err := m.task(id)
	if err != nil {
		return model.Task{}, err
	}
	return t.Snapshot(), nil
}

// List returns the tasks that run, and those that ended lately, oldest
// first: the session's, or with "" the server's own (updates and logins).
func (m *Manager) List(sessionID string) []model.Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prune()
	out := make([]model.Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		if s := t.Snapshot(); s.SessionID == sessionID {
			out = append(out, s)
		}
	}
	return out
}

// Input types a line into the task's command, as if the user typed it and
// pressed Return.
func (m *Manager) Input(id, text string) error {
	t, err := m.task(id)
	if err != nil {
		return err
	}
	t.mu.Lock()
	w := t.input
	t.mu.Unlock()
	if w == nil {
		return errors.New("the task doesn't take input")
	}
	text = strings.NewReplacer("\r", "", "\n", "").Replace(text)
	if _, err := io.WriteString(w, text); err != nil {
		return err
	}
	// Return on its own, as a keypress: a terminal UI may read a chunk with
	// text and a carriage return in it as a paste.
	time.Sleep(100 * time.Millisecond)
	_, err = io.WriteString(w, "\r")
	return err
}

func (m *Manager) Cancel(id string) error {
	t, err := m.task(id)
	if err != nil {
		return err
	}
	t.cancel()
	return nil
}

// Shutdown cancels the tasks that run, and waits for them to end.
func (m *Manager) Shutdown() {
	m.cancel()
	m.wg.Wait()
}

// Snapshot is the task as clients see it.
func (t *Task) Snapshot() model.Task {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.t
	s.Steps = slices.Clone(t.t.Steps)
	s.Output = t.screen.Text()
	if t.screenURL {
		s.URL = t.screen.URL()
	}
	s.Input = t.input != nil
	return s
}

// Step adds a step to the task's list, or replaces the one with its id.
func (t *Task) Step(st model.TaskStep) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if i := slices.IndexFunc(t.t.Steps, func(s model.TaskStep) bool { return s.ID == st.ID }); i >= 0 {
		t.t.Steps[i] = st
	} else {
		t.t.Steps = append(t.t.Steps, st)
	}
}

// SetURL sets the task's url, for a task started with StartForSession.
func (t *Task) SetURL(url string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.t.URL = url
}

// Println adds a line of rad's own to the output.
func (t *Task) Println(format string, args ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.screen.Println(fmt.Sprintf(format, args...))
}

func (t *Task) SetVersion(v string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.t.Version = v
}

func (t *Task) finish(ctx context.Context, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now().UTC()
	t.t.EndedAt = &now
	t.input = nil
	switch {
	case ctx.Err() != nil:
		t.t.Status = model.TaskCancelled
	case err != nil:
		t.t.Status, t.t.Error = model.TaskFailed, err.Error()
	default:
		t.t.Status = model.TaskSucceeded
	}
}

func (t *Task) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.screen.Write(p)
}

// Run runs a command in a terminal of its own, its output going to the
// task's, and lines that clients send (Manager.Input) to it. When ctx ends,
// it stops the command's process group. The error is the command's exit
// status.
func (t *Task) Run(ctx context.Context, c harness.Command) error {
	if len(c.Args) == 0 {
		return errors.New("no command")
	}
	cmd := exec.Command(c.Args[0], c.Args[1:]...)
	cmd.Env = environ(os.Environ(), c)
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return err
	}
	defer f.Close()
	t.mu.Lock()
	t.input = f
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.input = nil
		t.mu.Unlock()
	}()

	copied := make(chan struct{})
	go func() {
		defer close(copied)
		io.Copy(t, f) // until the terminal closes; EIO once the command exits
	}()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err = <-exited:
	case <-ctx.Done():
		syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case err = <-exited:
		case <-time.After(3 * time.Second):
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			err = <-exited
		}
	}
	// The rest of its output: the terminal stays readable until every
	// process that holds it is gone.
	select {
	case <-copied:
	case <-time.After(500 * time.Millisecond):
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("%s: %w", c.Args[0], err)
	}
	return nil
}

// dropped are variables about the terminal rad runs in, if any.
var dropped = []string{"TERM", "COLORTERM", "COLUMNS", "LINES", "ITERM_", "TMUX", "KITTY_", "WEZTERM_", "GHOSTTY_", "VSCODE_"}

// environ is rad's environment with the command's variables, for a
// terminal that isn't a person's: browsers don't open on this machine, as
// the user isn't at it, and colours are off where CLIs allow.
func environ(base []string, c harness.Command) []string {
	set := map[string]string{"TERM": "xterm-256color", "NO_COLOR": "1", "BROWSER": "true", "NO_OPEN_BROWSER": "1"}
	for k, v := range c.Env {
		set[k] = v
	}
	var env []string
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := set[k]; ok || slices.Contains(c.Unset, k) || slices.ContainsFunc(dropped, func(d string) bool { return k == d || strings.HasSuffix(d, "_") && strings.HasPrefix(k, d) }) {
			continue
		}
		env = append(env, kv)
	}
	for k, v := range set {
		env = append(env, k+"="+v)
	}
	return env
}
