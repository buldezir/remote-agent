// Package replaytest replays recorded CLI transcripts against harness adapters.
//
// A test binary re-executes itself as a fake agent CLI: TestMain calls Main,
// which, when RAD_REPLAY names a transcript, walks it on stdio and exits.
// Transcript lines are {"dir":"in"|"out","frame":{...}}: "in" frames are
// written to stdout (agent → adapter) and "out" frames are expected on stdin
// (adapter → agent).
//
// Matching is loose: an expected frame matches by type/method (and, for
// replies to agent requests, by id). Ids of the adapter's own requests are
// learned from each matched frame and rewritten in the recorded responses.
// Recorded requests the adapter never sends (e.g. a model/list the recorder
// made) are skipped along with their responses once a later expected frame
// arrives; unrecorded requests get an empty success reply.
//
// The recorded workspace prefix /tmp/ws is replaced with RAD_REPLAY_ROOT.
// The player logs skipped and unrecorded frames to RAD_REPLAY_REPORT, which
// Command checks when the test ends.
package replaytest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"remote-agent/internal/harness"
	"remote-agent/internal/model"
)

const (
	EnvTranscript = "RAD_REPLAY"
	EnvRoot       = "RAD_REPLAY_ROOT"
	EnvReport     = "RAD_REPLAY_REPORT"
	Placeholder   = "/tmp/ws" // workspace prefix used in scrubbed transcripts
)

// Main turns the process into the fake CLI when RAD_REPLAY is set. Call it
// first thing in TestMain.
func Main() {
	path := os.Getenv(EnvTranscript)
	if path == "" {
		return
	}
	report := io.Writer(os.Stderr)
	if f, err := os.OpenFile(os.Getenv(EnvReport), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		report = io.MultiWriter(f, os.Stderr) // unbuffered, so os.Exit loses nothing
	}
	if err := Play(path, os.Getenv(EnvRoot), os.Stdin, os.Stdout, report); err != nil {
		fmt.Fprintln(report, "error", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// Command returns the executable and env for a harness command that replays
// transcript, mapping the recorded workspace to root. When the test ends it
// fails if the player did not reach the end of the transcript or received
// frames that are not in it.
func Command(t testing.TB, transcript, root string) (string, map[string]string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	abs, err := filepath.Abs(transcript)
	if err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(t.TempDir(), "replay-report.txt")
	t.Cleanup(func() {
		data, _ := os.ReadFile(report)
		done := false
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			switch {
			case line == "done":
				done = true
			case strings.HasPrefix(line, "skip "):
				t.Logf("replay: %s", line)
			case line != "":
				t.Errorf("replay: %s", line)
			}
		}
		if !done {
			t.Errorf("replay did not reach the end of %s", transcript)
		}
	})
	return exe, map[string]string{EnvTranscript: abs, EnvRoot: root, EnvReport: report}
}

type entry struct {
	Dir   string          `json:"dir"`
	Frame json.RawMessage `json:"frame"`
}

type player struct {
	entries []entry
	root    []byte // JSON-escaped root, without quotes
	in      *bufio.Reader
	out     io.Writer
	report  io.Writer
	pool    [][]byte          // adapter frames received ahead of their turn
	ids     map[string]string // recorded request id -> actual id (raw JSON / claude string)
	skipped map[string]bool   // recorded request ids the adapter never sent
}

// Play runs the transcript at path over in/out.
func Play(path, root string, in io.Reader, out, report io.Writer) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	p := &player{in: bufio.NewReaderSize(in, 1<<20), out: out, report: report, ids: map[string]string{}, skipped: map[string]bool{}}
	if root != "" {
		b, _ := json.Marshal(root)
		p.root = b[1 : len(b)-1]
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		var e entry
		if len(bytes.TrimSpace(line)) == 0 || json.Unmarshal(line, &e) != nil || len(e.Frame) == 0 {
			continue
		}
		p.entries = append(p.entries, e)
	}
	for i, e := range p.entries {
		switch e.Dir {
		case "in":
			if err := p.send(e.Frame); err != nil {
				return err
			}
		case "out":
			if err := p.expect(i); err != nil {
				return err
			}
		}
	}
	// Transcript done: keep answering until the adapter closes stdin.
	fmt.Fprintln(p.report, "done")
	for _, f := range p.pool {
		p.unexpected(f)
	}
	for {
		line, err := p.read()
		if err != nil {
			return nil
		}
		p.unexpected(line)
	}
}

type frame struct {
	Type      string          `json:"type"`
	Method    string          `json:"method"`
	ID        json.RawMessage `json:"id"`
	JSONRPC   string          `json:"jsonrpc"`
	Error     json.RawMessage `json:"error"`
	RequestID string          `json:"request_id"`
	Request   struct {
		Subtype string `json:"subtype"`
	} `json:"request"`
	Response struct {
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
	} `json:"response"`
}

func parse(raw []byte) frame {
	var f frame
	json.Unmarshal(raw, &f)
	return f
}

func canon(id json.RawMessage) string {
	var v any
	if json.Unmarshal(id, &v) != nil {
		return string(id)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// key identifies what a frame is, loosely enough to match a live frame
// against its recorded counterpart.
func key(raw []byte) string {
	f := parse(raw)
	switch {
	case f.Type == "control_request":
		return "control_request:" + f.Request.Subtype
	case f.Type == "control_response":
		return "control_response:" + f.Response.Subtype + ":" + f.Response.RequestID
	case f.Type != "":
		return "claude:" + f.Type
	case f.Method != "" && len(f.ID) > 0:
		return "request:" + f.Method
	case f.Method != "":
		return "notify:" + f.Method
	case len(f.Error) > 0 && string(f.Error) != "null":
		return "error:" + canon(f.ID)
	default:
		return "result:" + canon(f.ID)
	}
}

// requestID returns the id of a request we sent, keyed for p.ids.
func requestID(raw []byte) (string, bool) {
	f := parse(raw)
	switch {
	case f.Type == "control_request":
		return "claude:" + f.RequestID, true
	case f.Type == "" && f.Method != "" && len(f.ID) > 0:
		return canon(f.ID), true
	}
	return "", false
}

func (p *player) read() ([]byte, error) {
	for {
		line, err := p.in.ReadBytes('\n')
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			return line, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func (p *player) write(v []byte) error {
	if p.root != nil {
		v = bytes.ReplaceAll(v, []byte(Placeholder), p.root)
	}
	_, err := p.out.Write(append(v, '\n'))
	return err
}

// later reports whether k matches an expected frame after entry i.
func (p *player) later(i int, k string) bool {
	for _, e := range p.entries[i+1:] {
		if e.Dir == "out" && key(e.Frame) == k {
			return true
		}
	}
	return false
}

func (p *player) expect(i int) error {
	want := key(p.entries[i].Frame)
	for {
		if j := slices.IndexFunc(p.pool, func(f []byte) bool { return key(f) == want }); j >= 0 {
			got := p.pool[j]
			p.pool = slices.Delete(p.pool, j, j+1)
			p.learn(p.entries[i].Frame, got)
			return nil
		}
		if slices.ContainsFunc(p.pool, func(f []byte) bool { return p.later(i, key(f)) }) {
			// The adapter moved on without sending this frame.
			fmt.Fprintf(p.report, "skip %s\n", want)
			if id, ok := requestID(p.entries[i].Frame); ok {
				p.skipped[id] = true
			}
			return nil
		}
		line, err := p.read()
		if err != nil {
			return fmt.Errorf("stdin closed while waiting for %s: %w", want, err)
		}
		if k := key(line); k == want || p.later(i, k) {
			p.pool = append(p.pool, line)
			continue
		}
		p.unexpected(line)
	}
}

func (p *player) learn(recorded, actual []byte) {
	rid, ok := requestID(recorded)
	if !ok {
		return
	}
	f := parse(actual)
	if f.Type == "control_request" {
		p.ids[rid] = f.RequestID
	} else {
		p.ids[rid] = string(f.ID)
	}
}

// send writes an agent frame, rewriting ids of responses to our requests.
func (p *player) send(raw []byte) error {
	f := parse(raw)
	switch {
	case f.Type == "control_response":
		rid := "claude:" + f.Response.RequestID
		if p.skipped[rid] {
			return nil
		}
		if actual, ok := p.ids[rid]; ok {
			var m map[string]json.RawMessage
			json.Unmarshal(raw, &m)
			var resp map[string]json.RawMessage
			json.Unmarshal(m["response"], &resp)
			resp["request_id"], _ = json.Marshal(actual)
			m["response"], _ = json.Marshal(resp)
			raw, _ = json.Marshal(m)
		}
	case f.Type == "" && f.Method == "" && len(f.ID) > 0:
		rid := canon(f.ID)
		if p.skipped[rid] {
			return nil
		}
		if actual, ok := p.ids[rid]; ok {
			var m map[string]json.RawMessage
			json.Unmarshal(raw, &m)
			m["id"] = json.RawMessage(actual)
			raw, _ = json.Marshal(m)
		}
	}
	return p.write(raw)
}

// unexpected answers requests that are not in the transcript with an empty success.
func (p *player) unexpected(raw []byte) {
	fmt.Fprintf(p.report, "unrecorded %s\n", raw)
	f := parse(raw)
	switch {
	case f.Type == "control_request":
		b, _ := json.Marshal(map[string]any{"type": "control_response",
			"response": map[string]any{"subtype": "success", "request_id": f.RequestID, "response": map[string]any{}}})
		p.write(b)
	case f.Type == "" && f.Method != "" && len(f.ID) > 0:
		m := map[string]any{"id": f.ID, "result": map[string]any{}}
		if f.JSONRPC != "" {
			m["jsonrpc"] = f.JSONRPC
		}
		b, _ := json.Marshal(m)
		p.write(b)
	}
}

// Turn is what a runtime emitted during one prompt.
type Turn struct {
	Events    []harness.Event
	Items     map[string]model.Item // latest state per adapter key
	Keys      []string              // adapter keys in first-seen order
	Approvals []harness.ApprovalEvent
	End       harness.TurnEnded
}

// ItemsOf returns the latest state of items of kind, in first-seen order.
func (t *Turn) ItemsOf(kind model.ItemKind) []model.Item {
	var out []model.Item
	for _, k := range t.Keys {
		if it := t.Items[k]; it.Kind == kind {
			out = append(out, it)
		}
	}
	return out
}

// RunTurn prompts rt and collects its events until the turn ends, answering
// each approval with the option id returned by choose.
func RunTurn(t testing.TB, rt harness.Runtime, prompt string, choose func(harness.ApprovalEvent) string) *Turn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := rt.Prompt(ctx, prompt); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	res := &Turn{Items: map[string]model.Item{}}
	for {
		select {
		case ev, ok := <-rt.Events():
			if !ok {
				t.Fatalf("events closed before the turn ended; got %d events", len(res.Events))
			}
			res.Events = append(res.Events, ev)
			switch ev := ev.(type) {
			case harness.ItemEvent:
				if _, seen := res.Items[ev.Item.ID]; !seen {
					res.Keys = append(res.Keys, ev.Item.ID)
				}
				res.Items[ev.Item.ID] = ev.Item
			case harness.ApprovalEvent:
				res.Approvals = append(res.Approvals, ev)
				if err := rt.Respond(ctx, ev.ID, harness.Response{OptionID: choose(ev)}); err != nil {
					t.Fatalf("respond: %v", err)
				}
			case harness.TurnEnded:
				res.End = ev
				return res
			case harness.Exited:
				t.Fatalf("runtime exited mid-turn: %v", ev.Err)
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for the turn to end; got %d events", len(res.Events))
		}
	}
}

// CloseClean closes rt and checks that it reports a clean exit and then
// closes its event channel. It returns the events drained on the way.
func CloseClean(t testing.TB, rt harness.Runtime) []harness.Event {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- rt.Close() }()
	var evs []harness.Event
	exited := false
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-rt.Events():
			if !ok {
				if !exited {
					t.Fatal("event channel closed without Exited")
				}
				if err := <-done; err != nil {
					t.Fatalf("close: %v", err)
				}
				return evs
			}
			evs = append(evs, ev)
			if x, ok := ev.(harness.Exited); ok {
				if exited {
					t.Fatal("Exited delivered twice")
				}
				exited = true
				if x.Err != nil {
					t.Fatalf("Exited with error: %v", x.Err)
				}
			}
		case <-timeout:
			t.Fatal("timed out waiting for the runtime to exit")
		}
	}
}
