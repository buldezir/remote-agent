package terminal

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func newManager(t *testing.T) *Manager {
	t.Helper()
	if _, err := os.Stat("/dev/ptmx"); err != nil {
		t.Skip("no pseudo-terminals here")
	}
	m := NewManager(Options{Shell: "/bin/sh", Env: []string{"HOME=" + t.TempDir(), "PS1=$ ", "ENV="}, Log: slog.New(slog.DiscardHandler)})
	t.Cleanup(m.Shutdown)
	return m
}

func open(t *testing.T, m *Manager, session string) *Terminal {
	t.Helper()
	term, err := m.Open(session, t.TempDir(), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	return term
}

// readUntil reads output until it contains want. The terminal echoes what's
// typed, so tests wait for something the shell computes, like 42 for
// $((6*7)).
func readUntil(t *testing.T, r *Reader, want string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out []byte
	for !bytes.Contains(out, []byte(want)) {
		c, err := r.Next(ctx, 64<<10)
		if err != nil {
			t.Fatalf("waiting for %q: %v; got %q", want, err, out)
		}
		if c.Exited {
			t.Fatalf("shell exited (%d) waiting for %q; got %q", c.Code, want, out)
		}
		out = append(out, c.Data...)
	}
	return string(out)
}

func waitExit(t *testing.T, r *Reader) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		c, err := r.Next(ctx, 64<<10)
		if err != nil {
			t.Fatal("shell didn't exit:", err)
		}
		if c.Exited {
			return c.Code
		}
	}
}

func gone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d is still running", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestShellRunsInSessionFolder(t *testing.T) {
	m := newManager(t)
	dir := t.TempDir()
	term, err := m.Open("s1", dir, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := m.Open("s1", t.TempDir(), 80, 24); again != term {
		t.Error("a second Open started another shell")
	}
	r := term.Attach("", 0)
	term.Write([]byte("pwd; echo $TERM $((6*7))\n"))
	real, _ := filepath.EvalSymlinks(dir)
	out := readUntil(t, r, "xterm-256color 42")
	if !strings.Contains(out, real) {
		t.Errorf("output %q doesn't show the folder %s", out, real)
	}
}

func TestResize(t *testing.T) {
	m := newManager(t)
	term := open(t, m, "s1")
	r := term.Attach("", 0)
	term.Resize(100, 30, false)
	term.Write([]byte("stty size\n"))
	readUntil(t, r, "30 100")
}

func TestExitCode(t *testing.T) {
	m := newManager(t)
	term := open(t, m, "s1")
	r := term.Attach("", 0)
	term.Write([]byte("exit 3\n"))
	if code := waitExit(t, r); code != 3 {
		t.Errorf("exit code = %d, want 3", code)
	}
	<-term.Done()
	deadline := time.Now().Add(2 * time.Second)
	for m.Get("s1") != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if m.Get("s1") != nil {
		t.Error("the manager still has the shell")
	}
	if next := open(t, m, "s1"); next == term {
		t.Error("Open returned the shell that exited")
	}
}

func TestCloseHangsUp(t *testing.T) {
	m := newManager(t)
	term := open(t, m, "s1")
	r := term.Attach("", 0)
	term.Write([]byte("echo st''arted; sleep 100\n"))
	readUntil(t, r, "started")
	var fg int
	deadline := time.Now().Add(5 * time.Second)
	for fg == 0 || fg == term.cmd.Process.Pid { // until sleep is in the foreground
		if time.Now().After(deadline) {
			break
		}
		term.mu.Lock()
		fg = term.foreground()
		term.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	m.Close("s1")
	select {
	case <-term.Done():
	default:
		t.Fatal("Close returned before the shell exited")
	}
	if code := waitExit(t, r); code != 128+int(syscall.SIGHUP) && code != 128+int(syscall.SIGKILL) {
		t.Errorf("exit code = %d, want a hangup", code)
	}
	gone(t, term.cmd.Process.Pid)
	if fg > 0 {
		gone(t, fg)
	}
}

func TestResumeAndReplay(t *testing.T) {
	m := newManager(t)
	term := open(t, m, "s1")
	r1 := term.Attach("", 0)
	if !r1.Reset || r1.Offset != 0 {
		t.Errorf("first reader: reset %v offset %d", r1.Reset, r1.Offset)
	}
	term.Write([]byte("echo $((6*7))\n"))
	readUntil(t, r1, "42")

	// A client that names the shell and its offset resumes there.
	r2 := term.Attach(term.ID, r1.off)
	if r2.Reset || r2.Offset != r1.off {
		t.Errorf("resumed reader: reset %v offset %d, want %d", r2.Reset, r2.Offset, r1.off)
	}
	term.Write([]byte("echo $((7*8))\n"))
	if out := readUntil(t, r2, "56"); strings.Contains(out, "42") {
		t.Errorf("resumed output repeats earlier output: %q", out)
	}

	// Another shell's id, or an offset past the end, starts afresh.
	for _, r := range []*Reader{term.Attach("other", r1.off), term.Attach(term.ID, 1<<40)} {
		if !r.Reset || r.Offset != 0 {
			t.Errorf("fresh reader: reset %v offset %d", r.Reset, r.Offset)
		}
		out := readUntil(t, r, "56")
		if !strings.Contains(out, "42") {
			t.Errorf("replay %q misses earlier output", out)
		}
	}
}

func TestReplayAfterWrap(t *testing.T) {
	m := newManager(t)
	term := open(t, m, "s1")
	behind := term.Attach("", 0)
	r := term.Attach("", 0)
	term.Write([]byte(`printf '\033[?2004h'; i=0; while [ $i -lt 8000 ]; do echo line-$i-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx; i=$((i+1)); done; echo fin''ished` + "\n"))
	readUntil(t, r, "finished")

	fresh := term.Attach("", 0)
	if !fresh.Reset || fresh.Offset == 0 {
		t.Fatalf("fresh reader after a wrap: reset %v offset %d", fresh.Reset, fresh.Offset)
	}
	out := readUntil(t, fresh, "finished")
	if !strings.HasPrefix(out, "\x1b[?2004hline-") {
		t.Errorf("replay starts %q, want bracketed paste restored and a whole line", out[:min(len(out), 40)])
	}

	// A reader that fell behind the buffer is told to start again.
	c, err := behind.Next(context.Background(), 64<<10)
	if err != nil || !c.Reset || c.Offset != fresh.Offset {
		t.Errorf("reader behind the buffer: %+v %v, want a reset at %d", c, err, fresh.Offset)
	}
}

func TestReplayQuietsQueries(t *testing.T) {
	m := newManager(t)
	term := open(t, m, "s1")
	live := term.Attach("", 0)
	term.Write([]byte(`printf 'a\033[>qb\033[cc%s\n' $((6*7))` + "\n"))
	if out := readUntil(t, live, "42"); !strings.Contains(out, "a\x1b[>qb\x1b[cc42") {
		t.Errorf("live output %q lost its queries", out)
	}

	// A client shown the output afterwards gets the queries blanked, so its
	// terminal doesn't answer them into the shell.
	quieted := "a\x00\x00\x00\x00b\x00\x00\x00c42"
	if out := readUntil(t, term.Attach("", 0), "42"); !strings.Contains(out, quieted) {
		t.Errorf("replay %q, want the queries blanked: %q", out, quieted)
	}
	if out := readUntil(t, term.Attach(term.ID, 0), "42"); !strings.Contains(out, quieted) {
		t.Errorf("resumed output %q, want the queries blanked: %q", out, quieted)
	}
}

func TestQuiet(t *testing.T) {
	q := func(s string) string { return strings.Repeat("_", len(s)) }
	for _, tc := range []struct{ in, want string }{
		{"\x1b[c\x1b[0c\x1b[>c\x1b[=c", "*"},             // device attributes
		{"\x1b[6n\x1b[?996n\x1b[>q\x1b[>0q\x1b[?u", "*"}, // status, version, kitty keyboard
		{"\x1b[?2026$p\x1b[4$p", "*"},                    // DECRQM
		{"\x1b[16t\x1b[14;2t\x1b[21t", "*"},              // window reports
		{"\x1b]11;?\x07\x1b]4;1;?\x1b\\\x1b]52;c;?\x07", "*"},
		{"\x1bP$qm\x1b\\\x1bP+q544e\x1b\\", "*"},                             // DECRQSS, XTGETTCAP
		{"\x1b_Gi=31,a=q;AAAA\x1b\\x", q("\x1b_Gi=31,a=q;AAAA\x1b\\") + "x"}, // kitty graphics
		{"\x1bZ", "*"},
		{"ab\x1b[cd", "ab" + q("\x1b[c") + "d"},
		// An escape sequence ends a string early.
		{"\x1b]2;x\x1b[c\x07", "\x1b]2;x" + q("\x1b[c") + "\x07"},
		// Sequences that do something keep it.
		{"\x1b[2J\x1b[u\x1b[>1u\x1b[1 q\x1b[8;24;80t\x1b[22t\x1b[!p\x1b[>4;1m", ""},
		{"\x1b]0;title?\x07\x1b]11;#000000\x07\x1b]8;;http://x?\x1b\\", ""},
		{"\x1bP1;1;1q#0~\x1b\\\x1b_Xyz\x1b\\\x1b7\x1b(B", ""}, // sixel, another APC, escapes
		// A query cut short is still being written; CAN cancels one.
		{"x\x1b[", ""},
		{"\x1b[?2026", ""},
		{"\x1bP$q", ""},
		{"\x1b[\x18c", ""},
	} {
		want := tc.want
		switch want {
		case "":
			want = tc.in
		case "*":
			want = q(tc.in)
		}
		p := []byte(tc.in)
		quiet(p)
		if got := strings.ReplaceAll(string(p), "\x00", "_"); got != want {
			t.Errorf("quiet(%q) = %q, want %q", tc.in, got, want)
		}
	}
}

func TestShutdown(t *testing.T) {
	m := newManager(t)
	term := open(t, m, "s1")
	m.Shutdown()
	select {
	case <-term.Done():
	default:
		t.Error("Shutdown returned before the shell exited")
	}
	if _, err := m.Open("s2", t.TempDir(), 80, 24); !errors.Is(err, ErrShutdown) {
		t.Errorf("Open after Shutdown: %v", err)
	}
}

func TestRing(t *testing.T) {
	var r ring
	r.append([]byte("\x1b[?1049h\x1b[?25"))
	r.append([]byte("l\x1b="))
	line := []byte(strings.Repeat("x", 99) + "\n")
	for range 3 * ringSize / len(line) {
		r.append(line)
	}
	r.append([]byte("\x1b[?1049l"))
	if r.end()-r.start() != ringSize || len(r.data) > 2*ringSize {
		t.Errorf("ring keeps %d bytes in %d", r.end()-r.start(), len(r.data))
	}
	off, prefix := r.fresh()
	if want := "\x1b[?1049h\x1b[?25l\x1b="; string(prefix) != want {
		t.Errorf("restore = %q, want %q", prefix, want)
	}
	if off <= r.start() || r.slice(off-1, off)[0] != '\n' {
		t.Errorf("fresh offset %d doesn't follow a newline", off)
	}
	if !bytes.HasSuffix(r.slice(off, r.end()), []byte("\x1b[?1049l")) {
		t.Error("slice misses the newest output")
	}
}

func TestModes(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"\x1b[?1;2004h", "\x1b[?1h\x1b[?2004h"},
		{"\x1b[?1h\x1b[?1l", ""},
		{"\x1b[?7l\x1b[?1000h\x1b[?1006h", "\x1b[?7l\x1b[?1000h\x1b[?1006h"},
		{"\x1b[?1049h\x1bc", ""},                     // a full reset
		{"\x1b]0;[?1h\x07", ""},                      // inside a title
		{"\x1b]0;title\x1b\\\x1b[?25l", "\x1b[?25l"}, // after one ended by ST
		{"\x1b[2J\x1b[>4;1m\x1b[1 q", ""},
	} {
		var m modes
		for i := range len(tc.in) { // one byte at a time: sequences split across reads
			m.feed([]byte{tc.in[i]})
		}
		if got := string(m.restore()); got != tc.want {
			t.Errorf("modes after %q = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEnviron(t *testing.T) {
	env := environ([]string{"PATH=/bin", "TERM=dumb", "TERM_PROGRAM=iTerm.app", "TMUX=/tmp/x", "SHELL=/bin/bash", "LC_ALL=C"}, "/bin/zsh")
	for _, want := range []string{"PATH=/bin", "TERM=xterm-256color", "COLORTERM=truecolor", "SHELL=/bin/zsh", "LC_ALL=C"} {
		if !slices.Contains(env, want) {
			t.Errorf("environment %v lacks %s", env, want)
		}
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "TERM_PROGRAM=") || strings.HasPrefix(kv, "TMUX=") || kv == "TERM=dumb" || kv == "SHELL=/bin/bash" || strings.HasPrefix(kv, "LANG=") {
			t.Errorf("environment keeps %s", kv)
		}
	}
	if env := environ(nil, "/bin/sh"); !slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "LANG=") }) {
		t.Errorf("environment %v has no LANG", env)
	}
}

func TestPasswdShell(t *testing.T) {
	lines := "root:x:0:0:root:/root:/bin/bash\nme:x:1000:1000::/home/me:/usr/bin/fish\n"
	if s := passwdShell(lines, "me"); s != "/usr/bin/fish" {
		t.Errorf("shell = %q", s)
	}
	if s := passwdShell(lines, "you"); s != "" {
		t.Errorf("unknown user's shell = %q", s)
	}
}

func TestIdleTimeout(t *testing.T) {
	if _, err := os.Stat("/dev/ptmx"); err != nil {
		t.Skip("no pseudo-terminals here")
	}
	idle := 600 * time.Millisecond
	m := NewManager(Options{
		Shell: "/bin/sh", Env: []string{"HOME=" + t.TempDir(), "PS1=$ ", "ENV="}, Log: slog.New(slog.DiscardHandler),
		Idle: func() time.Duration { return idle }, Sweep: 50 * time.Millisecond,
	})
	t.Cleanup(m.Shutdown)
	busy, quiet := open(t, m, "busy"), open(t, m, "quiet")
	r := busy.Attach("", 0)

	// Typing keeps one alive; the other is ended once it's been idle long enough.
	deadline := time.After(5 * time.Second)
	for {
		busy.Write([]byte("echo $((6*7))\n"))
		readUntil(t, r, "42")
		select {
		case <-quiet.Done():
		case <-time.After(100 * time.Millisecond):
			continue
		case <-deadline:
			t.Fatal("the idle shell is still running")
		}
		if since := time.Since(quiet.Started); since < idle {
			t.Errorf("the idle shell ended after %v, before its timeout", since)
		}
		break
	}
	select {
	case <-busy.Done():
		t.Fatal("the shell being typed into timed out")
	default:
	}
	if got := m.List(); len(got) != 1 || got[0].SessionID != "busy" {
		t.Errorf("List after the timeout = %+v", got)
	}
	if !busy.LastInput().After(busy.Started) {
		t.Error("input didn't move LastInput")
	}
}

func TestListAndCloseAll(t *testing.T) {
	m := newManager(t)
	a, b := open(t, m, "b"), open(t, m, "a")
	got := m.List()
	if len(got) != 2 || got[0].SessionID != "a" || got[1].SessionID != "b" {
		t.Fatalf("List = %+v", got)
	}
	if got[0].LastInput != got[0].Started {
		t.Errorf("a new shell's last input %v isn't its start %v", got[0].LastInput, got[0].Started)
	}
	if n := m.CloseAll(); n != 2 {
		t.Errorf("CloseAll = %d, want 2", n)
	}
	for _, term := range []*Terminal{a, b} {
		select {
		case <-term.Done():
		default:
			t.Error("CloseAll returned before the shell exited")
		}
	}
	// The Manager forgets an exited shell a moment after it's done.
	deadline := time.Now().Add(2 * time.Second)
	for len(m.List()) > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := m.List(); len(got) != 0 {
		t.Errorf("List after CloseAll = %+v", got)
	}
	// New shells start afterwards.
	open(t, m, "c")
}
