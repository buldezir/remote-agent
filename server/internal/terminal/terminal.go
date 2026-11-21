// Package terminal runs the shells the apps open from a session's menu: a
// login shell per session in a pseudo-terminal, which keeps running while no
// client is attached. It keeps the shell's recent output, so a client that
// reconnects resumes where it left off, and one that starts afresh redraws
// from the start of the buffer.
package terminal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// Terminal is a running shell.
type Terminal struct {
	ID        string // tells a resuming client it's the same shell
	SessionID string
	Started   time.Time

	pty   *os.File
	fd    int
	cmd   *exec.Cmd
	input chan []byte
	read  chan struct{} // closed when the pty has no more output
	done  chan struct{} // closed when the shell has exited and its output is in the ring

	mu     sync.Mutex
	ring   ring
	notify chan struct{} // closed and replaced when output arrives or the shell exits
	exited bool
	code   int
	kill   sync.Once
	// lastInput is when the user last typed, or the shell started: the
	// Manager ends a shell left alone for its idle timeout.
	lastInput time.Time
}

func start(sessionID, dir, shell string, args, env []string, cols, rows int) (*Terminal, error) {
	cmd := exec.Command(shell, args...)
	cmd.Dir = dir
	cmd.Env = env
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, err
	}
	id := make([]byte, 8)
	rand.Read(id)
	now := time.Now()
	t := &Terminal{
		ID: hex.EncodeToString(id), SessionID: sessionID, Started: now, lastInput: now,
		pty: f, fd: int(f.Fd()), cmd: cmd,
		input: make(chan []byte, 64), read: make(chan struct{}), done: make(chan struct{}),
		notify: make(chan struct{}),
	}
	go t.reader()
	go t.writer()
	go t.wait()
	return t, nil
}

func (t *Terminal) reader() {
	defer close(t.read)
	buf := make([]byte, 32<<10)
	for {
		n, err := t.pty.Read(buf)
		if n > 0 {
			t.mu.Lock()
			t.ring.append(buf[:n])
			t.wake()
			t.mu.Unlock()
		}
		if err != nil { // EOF, or EIO on Linux once the shell has gone
			return
		}
	}
}

// writer feeds input to the shell, so a program that isn't reading it holds
// up only its own input, not the client's connection.
func (t *Terminal) writer() {
	for {
		select {
		case p := <-t.input:
			t.pty.Write(p)
		case <-t.done:
			return
		}
	}
}

// wait waits for the shell rather than for the end of its output: a
// background job can keep the pty open long after the shell has exited.
func (t *Terminal) wait() {
	t.cmd.Wait()
	select {
	case <-t.read:
	case <-time.After(300 * time.Millisecond):
	}
	t.mu.Lock()
	t.exited = true
	t.code = exitCode(t.cmd.ProcessState)
	// The pty is in blocking mode, so this doesn't wait for a reader that
	// is still blocked; its descriptor closes when that read returns.
	t.pty.Close()
	t.wake()
	t.mu.Unlock()
	close(t.done)
}

func exitCode(ps *os.ProcessState) int {
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}

func (t *Terminal) wake() {
	close(t.notify)
	t.notify = make(chan struct{})
}

// LastInput is when the user last typed, or the shell started.
func (t *Terminal) LastInput() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastInput
}

// Done is closed when the shell has exited.
func (t *Terminal) Done() <-chan struct{} { return t.done }

// Write sends input to the shell. Input beyond what the shell has room for
// is dropped. Input counts as the user's: it keeps the shell from timing out.
func (t *Terminal) Write(p []byte) bool {
	t.mu.Lock()
	t.lastInput = time.Now()
	t.mu.Unlock()
	select {
	case t.input <- p:
		return true
	case <-t.done:
	default:
	}
	return false
}

// Resize sets the terminal's size. A client that starts afresh asks for a
// redraw, so a full-screen program repaints what the replay can't restore;
// the kernel signals the program only when the size changes.
func (t *Terminal) Resize(cols, rows int, redraw bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.exited {
		return
	}
	ws, err := unix.IoctlGetWinsize(t.fd, unix.TIOCGWINSZ)
	if err == nil && int(ws.Col) == cols && int(ws.Row) == rows {
		if redraw {
			if fg := t.foreground(); fg > 0 {
				syscall.Kill(-fg, syscall.SIGWINCH)
			}
		}
		return
	}
	unix.IoctlSetWinsize(t.fd, unix.TIOCSWINSZ, &unix.Winsize{Col: uint16(cols), Row: uint16(rows)})
}

// foreground is the terminal's foreground process group. Callers hold mu
// and have checked the shell is running, so the descriptor is open.
func (t *Terminal) foreground() int {
	fg, err := unix.IoctlGetInt(t.fd, unix.TIOCGPGRP)
	if err != nil {
		return 0
	}
	return fg
}

// Kill hangs up the shell and the program in the foreground, and kills them
// if they're still there two seconds later.
func (t *Terminal) Kill() {
	t.kill.Do(func() {
		t.mu.Lock()
		if t.exited {
			t.mu.Unlock()
			return
		}
		groups := []int{t.cmd.Process.Pid}
		if fg := t.foreground(); fg > 0 && fg != groups[0] {
			groups = append(groups, fg)
		}
		t.mu.Unlock()
		for _, g := range groups {
			syscall.Kill(-g, syscall.SIGHUP)
		}
		go func() {
			time.Sleep(2 * time.Second)
			for _, g := range groups[1:] {
				syscall.Kill(-g, syscall.SIGKILL)
			}
			select {
			case <-t.done:
			default:
				syscall.Kill(-groups[0], syscall.SIGKILL)
			}
		}()
	})
}

// Reader is one client's place in a shell's output.
type Reader struct {
	t       *Terminal
	off     uint64 // the client's next byte in the ring, after pending
	pending []byte // what the client gets first: output from before it attached, quieted

	// Offset is where the client's stream starts, and Reset says it starts
	// afresh rather than resuming, so the client clears its screen first.
	Offset uint64
	Reset  bool
}

// Attach starts reading the shell's output. A client that names this shell
// and an offset still in the buffer resumes there; any other starts afresh.
func (t *Terminal) Attach(id string, offset uint64) *Reader {
	t.mu.Lock()
	defer t.mu.Unlock()
	r := &Reader{t: t}
	if id == t.ID && offset >= t.ring.start() && offset <= t.ring.end() {
		r.Offset = offset
		r.catchUp(offset, nil)
		return r
	}
	r.reset()
	return r
}

// reset starts the reader afresh. The restore sequences come first and count
// as part of the stream, so Offset is that much before the output's offset.
func (r *Reader) reset() {
	off, prefix := r.t.ring.fresh()
	r.Reset = true
	r.Offset = off - uint64(len(prefix))
	r.catchUp(off, prefix)
}

// catchUp queues the output from off that the shell has already written,
// after head, with its queries quieted. Output written from now on reaches
// the client as it is, queries and all, for it to answer.
func (r *Reader) catchUp(off uint64, head []byte) {
	end := r.t.ring.end()
	data := r.t.ring.slice(off, end)
	quiet(data)
	r.off, r.pending = end, append(head, data...)
}

// take hands over up to max bytes of what's pending.
func (r *Reader) take(max int) []byte {
	n := min(len(r.pending), max)
	p := r.pending[:n:n]
	r.pending = r.pending[n:]
	return p
}

// Chunk is what a Reader hands its client next.
type Chunk struct {
	Data   []byte
	Reset  bool   // the client fell behind the buffer: it clears its screen, and Offset starts the stream again
	Offset uint64 // with Reset
	Exited bool
	Code   int // with Exited
}

// Next waits for output past the reader's place and returns up to max bytes
// of it, then tells the client when the shell exits.
func (r *Reader) Next(ctx context.Context, max int) (Chunk, error) {
	t := r.t
	for {
		t.mu.Lock()
		if len(r.pending) > 0 {
			p := r.take(max)
			t.mu.Unlock()
			return Chunk{Data: p}, nil
		}
		if r.off < t.ring.start() {
			r.reset()
			p := r.take(max)
			t.mu.Unlock()
			return Chunk{Reset: true, Offset: r.Offset, Data: p}, nil
		}
		if end := t.ring.end(); r.off < end {
			to := min(end, r.off+uint64(max))
			data := t.ring.slice(r.off, to)
			r.off = to
			t.mu.Unlock()
			return Chunk{Data: data}, nil
		}
		if t.exited {
			t.mu.Unlock()
			return Chunk{Exited: true, Code: t.code}, nil
		}
		wait := t.notify
		t.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return Chunk{}, ctx.Err()
		}
	}
}
