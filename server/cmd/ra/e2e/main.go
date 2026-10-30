// Command e2e drives ra in a pseudo-terminal, for checking it by hand or
// from a script: it runs ra at a fixed size behind a terminal emulator and
// serves its screen and keyboard on a Unix socket.
//
//	go run ./cmd/ra/e2e -listen /tmp/ra.sock -- /path/to/ra &
//	curl --unix-socket /tmp/ra.sock -d 'hello<enter>' http://ra/keys
//	curl --unix-socket /tmp/ra.sock http://ra/screen
//	curl --unix-socket /tmp/ra.sock 'http://ra/png?path=/tmp/shot.png'
//
// Keys are text with names in angle brackets: <enter> <esc> <tab> <s-tab>
// <up> <down> <left> <right> <pgup> <pgdn> <home> <end> <bs> <space>
// <s-enter> <a-enter> and <c-x> for a control key. /wait?text=… waits until
// the screen shows text; /resize?cols=…&rows=… resizes. The environment
// passes through, so RA_HOME and RAD_HOME choose ra's and rad's homes.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

func main() {
	sock := flag.String("listen", "", "a Unix socket's path, or host:port, to serve on")
	cols := flag.Int("cols", 160, "columns")
	rows := flag.Int("rows", 45, "rows")
	flag.Parse()
	if *sock == "" || flag.NArg() == 0 {
		log.Fatal("usage: e2e -listen PATH|HOST:PORT [-cols N -rows N] -- COMMAND [ARGS…]")
	}
	cmd := exec.Command(flag.Arg(0), flag.Args()[1:]...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(*cols), Rows: uint16(*rows)})
	if err != nil {
		log.Fatal(err)
	}
	t := &term{emu: vt.NewSafeEmulator(*cols, *rows), pty: f}
	go t.pump()
	// What the terminal answers, such as its background colour, goes back.
	go io.Copy(f, t.emu)
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()

	network := "unix"
	if _, _, err := net.SplitHostPort(*sock); err == nil {
		network = "tcp"
	} else {
		os.Remove(*sock)
	}
	l, err := net.Listen(network, *sock)
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if _, err := f.Write(keys(string(b))); err != nil {
			http.Error(w, err.Error(), 500)
		}
	})
	mux.HandleFunc("/paste", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.Write([]byte("\x1b[200~" + string(b) + "\x1b[201~"))
	})
	mux.HandleFunc("/screen", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, t.text())
	})
	mux.HandleFunc("/wait", func(w http.ResponseWriter, r *http.Request) {
		text := r.URL.Query().Get("text")
		timeout := 10 * time.Second
		if s, err := strconv.Atoi(r.URL.Query().Get("timeout")); err == nil {
			timeout = time.Duration(s) * time.Second
		}
		deadline := time.Now().Add(timeout)
		for !strings.Contains(t.text(), text) {
			if time.Now().After(deadline) {
				http.Error(w, "timed out waiting for "+strconv.Quote(text)+"\n"+t.text(), 408)
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		fmt.Fprint(w, t.text())
	})
	mux.HandleFunc("/png", func(w http.ResponseWriter, r *http.Request) {
		if err := t.png(r.URL.Query().Get("path")); err != nil {
			http.Error(w, err.Error(), 500)
		}
	})
	mux.HandleFunc("/resize", func(w http.ResponseWriter, r *http.Request) {
		c, _ := strconv.Atoi(r.URL.Query().Get("cols"))
		h, _ := strconv.Atoi(r.URL.Query().Get("rows"))
		if c < 10 || h < 5 {
			http.Error(w, "cols and rows", 400)
			return
		}
		t.emu.Resize(c, h)
		pty.Setsize(f, &pty.Winsize{Cols: uint16(c), Rows: uint16(h)})
	})
	mux.HandleFunc("/quit", func(w http.ResponseWriter, r *http.Request) {
		cmd.Process.Signal(os.Interrupt)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(l)
	<-exited
	srv.Shutdown(context.Background())
	if network == "unix" {
		os.Remove(*sock)
	}
}

type term struct {
	emu *vt.SafeEmulator
	pty *os.File
	mu  sync.Mutex
}

func (t *term) pump() {
	buf := make([]byte, 64<<10)
	for {
		n, err := t.pty.Read(buf)
		if n > 0 {
			t.mu.Lock()
			t.emu.Write(buf[:n])
			t.mu.Unlock()
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.Print(err)
			}
			return
		}
	}
}

// text is the screen as plain text, without trailing spaces.
func (t *term) text() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var b strings.Builder
	w, h := t.emu.Width(), t.emu.Height()
	for y := 0; y < h; y++ {
		var line strings.Builder
		for x := 0; x < w; x++ {
			c := t.emu.CellAt(x, y)
			if c == nil || c.Content == "" {
				if c != nil && c.Width == 0 {
					continue
				}
				line.WriteByte(' ')
				continue
			}
			line.WriteString(c.Content)
			if c.Width > 1 {
				x += c.Width - 1
			}
		}
		b.WriteString(strings.TrimRight(line.String(), " "))
		b.WriteByte('\n')
	}
	return b.String()
}

var named = map[string]string{
	"enter": "\r", "esc": "\x1b", "tab": "\t", "s-tab": "\x1b[Z", "bs": "\x7f", "space": " ",
	"up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C", "left": "\x1b[D",
	"pgup": "\x1b[5~", "pgdn": "\x1b[6~", "home": "\x1b[H", "end": "\x1b[F",
	"s-enter": "\x1b[13;2u", "a-enter": "\x1b\r", "f1": "\x1bOP",
}

// keys turns a key spec into the bytes a terminal sends.
func keys(spec string) []byte {
	var out strings.Builder
	for len(spec) > 0 {
		i := strings.IndexByte(spec, '<')
		j := strings.IndexByte(spec, '>')
		if i != 0 || j < 0 {
			if i < 0 {
				out.WriteString(spec)
				break
			}
			if i > 0 {
				out.WriteString(spec[:i])
				spec = spec[i:]
				continue
			}
			out.WriteByte('<')
			spec = spec[1:]
			continue
		}
		name := spec[1:j]
		spec = spec[j+1:]
		if s, ok := named[name]; ok {
			out.WriteString(s)
		} else if c, ok := strings.CutPrefix(name, "c-"); ok && len(c) == 1 {
			out.WriteByte(c[0] & 0x1f)
		} else {
			out.WriteString("<" + name + ">")
		}
	}
	return []byte(out.String())
}
