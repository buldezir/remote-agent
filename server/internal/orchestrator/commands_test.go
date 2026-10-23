package orchestrator

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"remote-agent/internal/harness"
	"remote-agent/internal/harness/fake"
	"remote-agent/internal/model"
)

type blockedCatalogHarness struct {
	fake.Harness
	entered chan struct{}
	release chan struct{}
	opened  chan *blockedCatalogRuntime
}

type blockedCatalogRuntime struct {
	harness.Runtime
	entered chan struct{}
	release chan struct{}
	events  chan harness.Event
}

func (h blockedCatalogHarness) Open(ctx context.Context, opts harness.OpenOptions) (harness.Runtime, error) {
	inner, err := h.Harness.Open(ctx, opts)
	if err != nil {
		return nil, err
	}
	rt := &blockedCatalogRuntime{Runtime: inner, entered: h.entered, release: h.release, events: make(chan harness.Event, 256)}
	go func() {
		for ev := range inner.Events() {
			rt.events <- ev
		}
		close(rt.events)
	}()
	h.opened <- rt
	return rt, nil
}
func (r *blockedCatalogRuntime) Events() <-chan harness.Event { return r.events }
func (r *blockedCatalogRuntime) Commands(ctx context.Context) ([]model.Command, error) {
	close(r.entered)
	select {
	case <-r.release:
		return []model.Command{{Name: "stale-skill"}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestCommandDiscoveryAllowsEventsAndStop(t *testing.T) {
	h := blockedCatalogHarness{entered: make(chan struct{}), release: make(chan struct{}), opened: make(chan *blockedCatalogRuntime, 1)}
	e := newEnvAt(t, t.TempDir(), false, h)
	s := e.create(CreateSessionParams{Prompt: "slow slow slow slow slow slow slow slow"})
	rt := <-h.opened
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	type result struct {
		commands []model.Command
		err      error
	}
	listed := make(chan result, 1)
	go func() { commands, err := e.o.Commands(ctx, s.ID); listed <- result{commands, err} }()
	select {
	case <-h.entered:
	case <-time.After(time.Second):
		t.Fatal("discovery never entered")
	}
	// This frame must be consumed even though the catalog response is still pending.
	rt.events <- harness.ItemEvent{Item: model.Item{ID: "during-discovery", Kind: model.ItemNotice, Status: model.ItemCompleted, Text: "streaming during discovery"}}
	deadline := time.After(time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	streamed := false
	for !streamed {
		select {
		case <-deadline:
			t.Fatal("command discovery blocked streamed events")
		case <-ticker.C:
			for _, it := range e.items(s.ID) {
				if it.Text == "streaming during discovery" {
					streamed = true
				}
			}
		}
	}
	stopped := make(chan error, 1)
	go func() { stopped <- e.o.Interrupt(bg, s.ID, true) }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("command discovery blocked force-stop")
	}
	e.prompt(s.ID, "resume after force-stop", "restart-after-discovery")
	close(h.release)
	select {
	case res := <-listed:
		if CodeOf(res.err) != CodeConflict || len(res.commands) != 0 {
			t.Fatalf("stopped runtime returned obsolete catalog: %+v", res)
		}
	case <-time.After(time.Second):
		t.Fatal("catalog did not finish")
	}
}

func TestCommandsRejectDeletedSession(t *testing.T) {
	e := newEnv(t, false)
	s := e.create(CreateSessionParams{})
	a, err := e.o.actor(bg, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A concurrent caller can retain this actor before Delete removes it from the map.
	if err := a.delete(bg, false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.o.Commands(bg, s.ID); CodeOf(err) != CodeNotFound {
		t.Fatalf("deleted session catalog: %v", err)
	}
}

type observedCommandHarness struct {
	fake.Harness
	supported bool
	opens     atomic.Int32
	openGate  <-chan struct{}
	closeGate <-chan struct{}
	opened    chan harness.OpenOptions
	closed    chan struct{}
	closing   chan struct{}
	listed    chan time.Duration
	prompts   chan harness.Input
}

func (h *observedCommandHarness) SupportsCommands() bool { return h.supported }
func (h *observedCommandHarness) Open(ctx context.Context, opts harness.OpenOptions) (harness.Runtime, error) {
	h.opens.Add(1)
	if h.opened != nil {
		h.opened <- opts
	}
	if h.openGate != nil {
		select {
		case <-h.openGate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if deadline, ok := ctx.Deadline(); ok && h.listed != nil {
		h.listed <- time.Until(deadline)
	}
	rt, err := h.Harness.Open(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &observedCommandRuntime{Runtime: rt, h: h}, nil
}

type observedCommandRuntime struct {
	harness.Runtime
	h    *observedCommandHarness
	once sync.Once
}

func (r *observedCommandRuntime) Commands(ctx context.Context) ([]model.Command, error) {
	if r.h.listed != nil {
		if deadline, ok := ctx.Deadline(); ok {
			r.h.listed <- time.Until(deadline)
		}
	}
	return r.Runtime.(harness.CommandLister).Commands(ctx)
}
func (r *observedCommandRuntime) Prompt(ctx context.Context, in harness.Input) error {
	if r.h.prompts != nil {
		r.h.prompts <- in
	}
	return r.Runtime.Prompt(ctx, in)
}
func (r *observedCommandRuntime) Close() error {
	r.once.Do(func() {
		if r.h.closing != nil {
			r.h.closing <- struct{}{}
		}
		if r.h.closeGate != nil {
			<-r.h.closeGate
		}
	})
	err := r.Runtime.Close()
	if r.h.closed != nil {
		select {
		case r.h.closed <- struct{}{}:
		default:
		}
	}
	return err
}

func TestDiscoverySkipsUnsupportedHarness(t *testing.T) {
	h := &observedCommandHarness{}
	e := newEnvAt(t, t.TempDir(), false, h)
	s := e.create(CreateSessionParams{})
	commands, err := e.o.Commands(bg, s.ID)
	if err != nil || len(commands) != 0 || h.opens.Load() != 0 {
		t.Fatalf("commands=%v err=%v opens=%d", commands, err, h.opens.Load())
	}
}

func TestDiscoveryDoesNotPersistUnpromptedNativeID(t *testing.T) {
	h := &observedCommandHarness{supported: true, opened: make(chan harness.OpenOptions, 4)}
	e := newEnvAt(t, t.TempDir(), false, h)
	s := e.create(CreateSessionParams{})
	if _, err := e.o.Commands(bg, s.ID); err != nil {
		t.Fatal(err)
	}
	if id := e.session(s.ID).NativeID; id != "" {
		t.Fatalf("inspection persisted NativeID=%q", id)
	}
	a, _ := e.o.actor(bg, s.ID)
	a.reapIfIdle(0)
	e.prompt(s.ID, "hello", "first-real-prompt")
	<-h.opened
	if opts := <-h.opened; opts.ResumeID != "" {
		t.Fatalf("first prompt tried to resume inspection: %+v", opts)
	}
	e.waitFor("prompted native id", func() bool { return e.session(s.ID).NativeID != "" })
}

func TestDiscoveryStartupBudgetAndLock(t *testing.T) {
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	h := &observedCommandHarness{supported: true, openGate: gate, opened: make(chan harness.OpenOptions, 1), listed: make(chan time.Duration, 2)}
	e := newEnvAt(t, t.TempDir(), false, h)
	s := e.create(CreateSessionParams{})
	done := make(chan error, 1)
	go func() { _, err := e.o.Commands(bg, s.ID); done <- err }()
	select {
	case <-h.opened:
	case <-time.After(time.Second):
		t.Fatal("Open did not start")
	}
	a, _ := e.o.actor(bg, s.ID)
	if !a.mu.TryLock() {
		t.Fatal("agent startup holds session lock")
	}
	a.mu.Unlock()
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	startup, listing := <-h.listed, <-h.listed
	if startup < 50*time.Second || listing > 10*time.Second || listing < 8*time.Second {
		t.Fatalf("startup=%v listing=%v", startup, listing)
	}
}

func TestDiscoveryClosesCandidateAfterArchive(t *testing.T) {
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	h := &observedCommandHarness{supported: true, openGate: gate, opened: make(chan harness.OpenOptions, 1), closed: make(chan struct{}, 1)}
	e := newEnvAt(t, t.TempDir(), false, h)
	s := e.create(CreateSessionParams{})
	done := make(chan error, 1)
	go func() { _, err := e.o.Commands(bg, s.ID); done <- err }()
	<-h.opened
	archived := make(chan error, 1)
	go func() { archived <- e.o.Archive(bg, s.ID, false) }()
	select {
	case err := <-archived:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("archive blocked by discovery startup")
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.closed:
	case <-time.After(time.Second):
		t.Fatal("orphaned discovery runtime")
	}
}

func TestDiscoveryDoesNotOpenDuringArchiveClose(t *testing.T) {
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	h := &observedCommandHarness{supported: true, closeGate: gate, closing: make(chan struct{}, 2)}
	e := newEnvAt(t, t.TempDir(), false, h)
	s := e.create(CreateSessionParams{Prompt: "slow slow slow slow"})
	done := make(chan error, 1)
	go func() { done <- e.o.Archive(bg, s.ID, false) }()
	<-h.closing
	commands, err := e.o.Commands(bg, s.ID)
	if err != nil || len(commands) != 0 || h.opens.Load() != 1 {
		t.Fatalf("commands=%v err=%v opens=%d", commands, err, h.opens.Load())
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRevertNoteKeepsExplicitCommandFirst(t *testing.T) {
	for _, text := range []string{"/skill:prototype args", "$prototype args", "ordinary prompt"} {
		t.Run(text, func(t *testing.T) {
			h := &observedCommandHarness{supported: true, prompts: make(chan harness.Input, 1)}
			e := newEnvAt(t, t.TempDir(), false, h)
			s := e.create(CreateSessionParams{})
			a, _ := e.o.actor(bg, s.ID)
			a.mu.Lock()
			a.note = "[Note from the user's tooling: files reverted]"
			a.mu.Unlock()
			e.prompt(s.ID, text, "after-revert")
			input := <-h.prompts
			if text == "ordinary prompt" {
				if !strings.HasPrefix(input.Text, "[Note") {
					t.Fatalf("note missing: %q", input.Text)
				}
			} else if !strings.HasPrefix(input.Text, text+"\n\n[Note") {
				t.Fatalf("command prefix lost: %q", input.Text)
			}
		})
	}
}

func TestExpiredOpenDoesNotRetryResume(t *testing.T) {
	h := &observedCommandHarness{openGate: make(chan struct{})}
	e := newEnvAt(t, t.TempDir(), false, h)
	s := e.create(CreateSessionParams{})
	a, _ := e.o.actor(bg, s.ID)
	ctx, cancel := context.WithCancel(bg)
	cancel()
	a.mu.Lock()
	a.sess.NativeID = "previous-conversation"
	err := a.open(ctx)
	a.mu.Unlock()
	if err != context.Canceled || h.opens.Load() != 1 {
		t.Fatalf("err=%v opens=%d", err, h.opens.Load())
	}
}
