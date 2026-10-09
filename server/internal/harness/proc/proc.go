// Package proc runs a harness CLI as a child process speaking newline-delimited
// JSON over stdio. It owns process-group lifecycle, stderr capture and the
// optional raw-frame diagnostic log.
package proc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Spec struct {
	Command  string
	Args     []string
	Dir      string
	Env      map[string]string // added to the inherited environment
	UnsetEnv []string
	DiagPath string
}

type Proc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	wmu    sync.Mutex
	stderr *tailBuffer
	diag   *diagLog
	done   chan struct{}
	err    error
}

const maxLine = 64 << 20

func Start(s Spec) (*Proc, error) {
	cmd := exec.Command(s.Command, s.Args...)
	cmd.Dir = s.Dir
	cmd.Env = buildEnv(os.Environ(), s.Env, s.UnsetEnv)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	p := &Proc{cmd: cmd, stdin: stdin, stdout: bufio.NewReaderSize(stdout, 1<<20), stderr: &tailBuffer{max: 16 << 10}, done: make(chan struct{})}
	cmd.Stderr = p.stderr
	if s.DiagPath != "" {
		p.diag = openDiag(s.DiagPath)
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p.diag.write("meta", []byte(fmt.Sprintf("%q", append([]string{s.Command}, s.Args...))))
	go func() {
		p.err = cmd.Wait()
		p.diag.close()
		close(p.done)
	}()
	return p, nil
}

func buildEnv(base []string, add map[string]string, unset []string) []string {
	drop := map[string]bool{}
	for _, k := range unset {
		drop[k] = true
	}
	for k := range add {
		drop[k] = true
	}
	out := make([]string, 0, len(base)+len(add))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			out = append(out, kv)
		}
	}
	for k, v := range add {
		out = append(out, k+"="+v)
	}
	return out
}

// ReadLine returns the next stdout line (without the newline). Non-JSON lines
// are returned too; callers decide whether to skip them.
func (p *Proc) ReadLine() ([]byte, error) {
	var buf []byte
	for {
		chunk, isPrefix, err := p.stdout.ReadLine()
		if err != nil {
			return nil, err
		}
		buf = append(buf, chunk...)
		if len(buf) > maxLine {
			return nil, errors.New("stdout line too long")
		}
		if !isPrefix {
			break
		}
	}
	p.diag.write("in", buf)
	return buf, nil
}

// WriteJSON writes v as one line to stdin.
func (p *Proc) WriteJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	p.diag.write("out", b)
	_, err = p.stdin.Write(append(b, '\n'))
	return err
}

func (p *Proc) Stdin() io.Writer  { return &diagWriter{p: p} }
func (p *Proc) Stdout() io.Reader { return &diagReader{p: p} }

func (p *Proc) Done() <-chan struct{} { return p.done }

// Err returns the process exit error with the stderr tail, after Done.
func (p *Proc) Err() error {
	if p.err == nil {
		return nil
	}
	if tail := strings.TrimSpace(p.stderr.String()); tail != "" {
		return fmt.Errorf("%w: %s", p.err, lastLines(tail, 8))
	}
	return p.err
}

func (p *Proc) StderrTail() string { return p.stderr.String() }

// Stop closes stdin, then signals the process group: SIGTERM, then SIGKILL.
func (p *Proc) Stop(grace time.Duration) {
	p.wmu.Lock()
	p.stdin.Close()
	p.wmu.Unlock()
	select {
	case <-p.done:
		return
	case <-time.After(200 * time.Millisecond):
	}
	pgid := -p.cmd.Process.Pid
	_ = syscall.Kill(pgid, syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(grace):
		_ = syscall.Kill(pgid, syscall.SIGKILL)
		<-p.done
	}
}

// Version runs `command args...` and returns the first output line.
func Version(ctx context.Context, command string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, command, args...).CombinedOutput()
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if err != nil {
		return line, err
	}
	return line, nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(b), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// diagWriter/diagReader expose raw stdio for libraries that do their own
// framing (ACP SDK), while still recording frames in the diag log.
type diagWriter struct{ p *Proc }

func (w *diagWriter) Write(b []byte) (int, error) {
	w.p.wmu.Lock()
	defer w.p.wmu.Unlock()
	w.p.diag.write("out", bytes.TrimRight(b, "\n"))
	return w.p.stdin.Write(b)
}

type diagReader struct{ p *Proc }

func (r *diagReader) Read(b []byte) (int, error) {
	n, err := r.p.stdout.Read(b)
	if n > 0 {
		r.p.diag.writeChunk(b[:n])
	}
	return n, err
}

type diagLog struct {
	mu      sync.Mutex
	f       *os.File
	pending []byte
}

func openDiag(path string) *diagLog {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil
	}
	return &diagLog{f: f}
}

func (d *diagLog) write(dir string, line []byte) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.f == nil {
		return
	}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	if json.Valid(line) {
		fmt.Fprintf(d.f, "{\"ts\":%q,\"dir\":%q,\"frame\":%s}\n", ts, dir, line)
	} else {
		raw, _ := json.Marshal(string(line))
		fmt.Fprintf(d.f, "{\"ts\":%q,\"dir\":%q,\"raw\":%s}\n", ts, dir, raw)
	}
}

// writeChunk splits a raw stdout chunk into lines for the diag log.
func (d *diagLog) writeChunk(b []byte) {
	if d == nil {
		return
	}
	d.pending = append(d.pending, b...)
	for {
		i := bytes.IndexByte(d.pending, '\n')
		if i < 0 {
			break
		}
		d.write("in", d.pending[:i])
		d.pending = d.pending[i+1:]
	}
}

func (d *diagLog) close() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.f != nil {
		d.f.Close()
		d.f = nil
	}
}
