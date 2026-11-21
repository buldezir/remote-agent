package terminal

import (
	"errors"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// ErrShutdown is returned by Open once rad has started shutting down.
var ErrShutdown = errors.New("rad is shutting down")

type Options struct {
	// Shell runs with Args in place of the user's login shell (tests).
	Shell string
	Args  []string
	Env   []string // added to the shell's environment
	Log   *slog.Logger
	// Idle is how long a shell may go without the user's input before it's
	// ended, asked at each sweep so that a new setting applies at once.
	// Nil or zero: shells never time out.
	Idle func() time.Duration
	// Sweep is how often idle shells are looked for; default a minute.
	Sweep time.Duration
}

// Manager keeps a shell per session.
type Manager struct {
	opt   Options
	mu    sync.Mutex
	terms map[string]*Terminal
	shut  bool
	stop  chan struct{}
}

func NewManager(opt Options) *Manager {
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	if opt.Sweep <= 0 {
		opt.Sweep = time.Minute
	}
	m := &Manager{opt: opt, terms: map[string]*Terminal{}, stop: make(chan struct{})}
	if opt.Idle != nil {
		go m.reap()
	}
	return m
}

// reap ends the shells nobody has typed into for the idle timeout.
func (m *Manager) reap() {
	tick := time.NewTicker(m.opt.Sweep)
	defer tick.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-tick.C:
		}
		idle := m.opt.Idle()
		if idle <= 0 {
			continue
		}
		for _, t := range m.all() {
			if since := time.Since(t.LastInput()); since >= idle {
				m.opt.Log.Info("terminal timed out", "session", t.SessionID, "idle", since.Round(time.Second))
				t.Kill()
			}
		}
	}
}

func (m *Manager) all() []*Terminal {
	m.mu.Lock()
	defer m.mu.Unlock()
	ts := make([]*Terminal, 0, len(m.terms))
	for _, t := range m.terms {
		ts = append(ts, t)
	}
	return ts
}

// Info is what List says of a running shell.
type Info struct {
	SessionID string
	Started   time.Time
	LastInput time.Time
}

// List is the running shells, by session.
func (m *Manager) List() []Info {
	var infos []Info
	for _, t := range m.all() {
		infos = append(infos, Info{SessionID: t.SessionID, Started: t.Started, LastInput: t.LastInput()})
	}
	slices.SortFunc(infos, func(a, b Info) int { return strings.Compare(a.SessionID, b.SessionID) })
	return infos
}

// CloseAll ends every shell, waiting a few seconds for them to exit, and
// says how many there were. Unlike Shutdown, new ones can start after.
func (m *Manager) CloseAll() int {
	ts := m.all()
	for _, t := range ts {
		t.Kill()
	}
	waitAll(ts)
	return len(ts)
}

// Get is the session's shell, or nil.
func (m *Manager) Get(sessionID string) *Terminal {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.terms[sessionID]
}

// Open returns the session's shell, starting one in dir if it has none.
func (m *Manager) Open(sessionID, dir string, cols, rows int) (*Terminal, error) {
	shell, args := m.opt.Shell, m.opt.Args
	if shell == "" {
		shell, args = loginShell(), []string{"-l"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shut {
		return nil, ErrShutdown
	}
	if t := m.terms[sessionID]; t != nil {
		return t, nil
	}
	t, err := start(sessionID, dir, shell, args, append(environ(os.Environ(), shell), m.opt.Env...), cols, rows)
	if err != nil {
		return nil, err
	}
	m.terms[sessionID] = t
	m.opt.Log.Info("terminal started", "session", sessionID, "shell", shell, "pid", t.cmd.Process.Pid)
	go func() {
		<-t.done
		m.mu.Lock()
		if m.terms[sessionID] == t {
			delete(m.terms, sessionID)
		}
		m.mu.Unlock()
		m.opt.Log.Info("terminal exited", "session", sessionID, "code", t.code)
	}()
	return t, nil
}

// Close ends the session's shell, waiting a few seconds for it to exit.
func (m *Manager) Close(sessionID string) {
	if t := m.Get(sessionID); t != nil {
		t.Kill()
		waitAll([]*Terminal{t})
	}
}

// Shutdown ends every shell, and Open starts no more.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	if !m.shut {
		close(m.stop)
	}
	m.shut = true
	m.mu.Unlock()
	m.CloseAll()
}

func waitAll(ts []*Terminal) {
	deadline := time.After(3 * time.Second)
	for _, t := range ts {
		select {
		case <-t.done:
		case <-deadline:
			return
		}
	}
}
