package orchestrator

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"remote-agent/internal/harness"
	"remote-agent/internal/harness/fake"
	"remote-agent/internal/model"
)

// limitEvents are the index's limits.upserted events.
func (e *env) limitEvents() []*model.UsageLimits {
	e.t.Helper()
	evs, _, err := e.st.Changes(bg, model.IndexStream, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []*model.UsageLimits
	for _, ev := range evs {
		if ev.Type == model.EvLimitsUpserted {
			out = append(out, ev.Limits)
		}
	}
	return out
}

func TestUsageLimitsFromTurns(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	s := e.create(CreateSessionParams{Prompt: "hello"})
	e.waitTurn(s.ID, 1)
	l, ok := e.o.Limits(bg)["fake"]
	if !ok || l.Harness != "fake" || l.Plan != "Fake" || len(l.Windows) != 2 || l.Windows[0].ID != "five_hour" || l.Windows[1].Label != "week" || l.Limited {
		t.Fatalf("limits = %+v, %v", l, ok)
	}
	// In the index, where the change feed keeps the latest across restarts.
	saved := e.limitEvents()
	if len(saved) != 1 || saved[0].Harness != "fake" || saved[0].Windows[0].UsedPercent != l.Windows[0].UsedPercent {
		t.Errorf("index has %+v", saved)
	}
	again := New(e.st, harness.NewRegistry(fake.Harness{}), Options{WorktreesDir: t.TempDir()})
	defer again.Shutdown()
	if got := again.Limits(bg)["fake"]; got.Windows[0].UsedPercent != l.Windows[0].UsedPercent || !got.UpdatedAt.Equal(l.UpdatedAt) {
		t.Errorf("after a restart: %+v, want %+v", got, l)
	}
}

func TestLimitReachedFailsTheTurn(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	s := e.create(CreateSessionParams{Prompt: "limit"})
	turn := e.waitTurn(s.ID, 1)
	if turn.Status != model.TurnFailed || !strings.HasPrefix(turn.Error, "Fake (scripted)'s 5-hour limit is reached; it resets ") {
		t.Fatalf("turn = %+v", turn)
	}
	errs := byKind(e.items(s.ID), model.ItemError)
	if len(errs) != 1 || errs[0].Text != turn.Error || errs[0].Limit == nil || errs[0].Limit.ID != "five_hour" ||
		errs[0].Limit.UsedPercent != 100 || errs[0].Limit.ResetsAt == nil {
		t.Fatalf("error items = %+v", errs)
	}
	if l := e.o.Limits(bg)["fake"]; !l.Limited || l.ResetsAt == nil || !l.ResetsAt.Equal(*errs[0].Limit.ResetsAt) {
		t.Errorf("limits = %+v", l)
	}
	// A limit isn't a login: the agent stays signed in.
	if info, _ := e.o.Registry().Cached("fake"); !info.AuthOK {
		t.Errorf("fake signed out: %+v", info)
	}

	e.prompt(s.ID, "hello again", "")
	if turn := e.waitTurn(s.ID, 2); turn.Status != model.TurnCompleted {
		t.Errorf("turn 2 = %+v", turn)
	}
	if byKind(e.items(s.ID), model.ItemError)[0].Limit == nil {
		t.Error("the error lost its limit")
	}
	e.prompt(s.ID, "limits 85", "")
	e.waitTurn(s.ID, 3)
	if l := e.o.Limits(bg)["fake"]; l.Limited || l.Windows[0].UsedPercent != 85 {
		t.Errorf("limits after limits 85 = %+v", l)
	}
}

func TestLimitsWrittenOnChange(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	now := time.Now().UTC()
	l := model.UsageLimits{Windows: []model.UsageWindow{{ID: "five_hour", Label: "5h", UsedPercent: 3}}, UpdatedAt: now}
	e.o.setLimits(bg, "claude", l)
	l.UpdatedAt = now.Add(10 * time.Second)
	e.o.setLimits(bg, "claude", l) // only the time changed
	if n := len(e.limitEvents()); n != 1 {
		t.Fatalf("%d events, want 1", n)
	}
	l.Windows[0].UsedPercent = 4
	e.o.setLimits(bg, "claude", l)
	l2 := l
	l2.Windows = []model.UsageWindow{{ID: "five_hour", Label: "5h", UsedPercent: 4}}
	l2.UpdatedAt = now.Add(2 * time.Minute) // the same, but its age shows
	e.o.setLimits(bg, "claude", l2)
	e.o.setLimits(bg, "codex", model.UsageLimits{UpdatedAt: now})
	evs := e.limitEvents()
	if len(evs) != 2 || evs[0].Harness != "claude" || !evs[0].UpdatedAt.Equal(l2.UpdatedAt) || evs[1].Harness != "codex" || evs[1].Windows == nil {
		t.Errorf("events = %+v", evs)
	}
}

// reader is an agent that answers for its limits (as Codex does), whose
// runtimes answer too.
type reader struct {
	fake.Harness
	id    string
	calls atomic.Int32
	rt    atomic.Int32 // calls its runtimes answered
	err   error
}

func (h *reader) ID() string { return h.id }
func (h *reader) Probe(ctx context.Context) model.HarnessInfo {
	info := h.Harness.Probe(ctx)
	info.ID = h.id
	return info
}
func (h *reader) ReadLimits(context.Context) (model.UsageLimits, error) {
	h.calls.Add(1)
	return model.UsageLimits{Windows: []model.UsageWindow{{ID: "five_hour", Label: "5h", UsedPercent: 20}}}, h.err
}
func (h *reader) Open(ctx context.Context, o harness.OpenOptions) (harness.Runtime, error) {
	rt, err := h.Harness.Open(ctx, o)
	return readerRuntime{rt, h}, err
}

type readerRuntime struct {
	harness.Runtime
	h *reader
}

func (r readerRuntime) ReadLimits(context.Context) (model.UsageLimits, error) {
	r.h.rt.Add(1)
	return model.UsageLimits{Windows: []model.UsageWindow{{ID: "five_hour", Label: "5h", UsedPercent: 30}}}, nil
}

func TestRefreshLimits(t *testing.T) {
	t.Parallel()
	h := &reader{id: "codexlike"}
	e := newEnvAt(t, t.TempDir(), false, h)
	e.o.RefreshLimits(bg)
	if h.calls.Load() != 1 || e.o.Limits(bg)["codexlike"].Windows[0].UsedPercent != 20 {
		t.Fatalf("calls = %d, limits %+v", h.calls.Load(), e.o.Limits(bg))
	}
	e.o.RefreshLimits(bg) // fresh
	if h.calls.Load() != 1 {
		t.Errorf("asked again for fresh limits")
	}

	// Old ones, or a window that reset since, are asked for again, once
	// limitsRefresh after the last time rad asked; a session's running
	// agent answers.
	s := e.create(CreateSessionParams{Harness: "codexlike", Prompt: "hello"})
	e.waitTurn(s.ID, 1)
	past := time.Now().Add(-time.Minute).UTC()
	e.o.setLimits(bg, "codexlike", model.UsageLimits{Windows: []model.UsageWindow{{ID: "five_hour", Label: "5h", UsedPercent: 90, ResetsAt: &past}}})
	e.o.RefreshLimits(bg)
	if h.calls.Load() != 1 || h.rt.Load() != 0 {
		t.Errorf("asked within limitsRefresh of the last time")
	}
	e.o.limits.mu.Lock()
	e.o.limits.tried["codexlike"] = time.Now().Add(-limitsRefresh)
	e.o.limits.mu.Unlock()
	e.o.RefreshLimits(bg)
	if h.calls.Load() != 1 || h.rt.Load() != 1 || e.o.Limits(bg)["codexlike"].Windows[0].UsedPercent != 30 {
		t.Errorf("calls = %d, runtime's %d, limits %+v", h.calls.Load(), h.rt.Load(), e.o.Limits(bg))
	}
}

func TestRefreshLimitsSkipsSignedOut(t *testing.T) {
	t.Parallel()
	h := &reader{id: "codexlike", err: errors.New("not signed in")}
	e := newEnvAt(t, t.TempDir(), false, h)
	e.o.Registry().Infos(bg, false)
	e.o.Registry().SignedOut("codexlike", "Signed out: run codex login")
	e.o.RefreshLimits(bg)
	if h.calls.Load() != 0 {
		t.Errorf("asked a signed-out agent")
	}
	e.o.Registry().SignedIn("codexlike")
	e.o.RefreshLimits(bg)
	if h.calls.Load() != 1 || len(e.o.Limits(bg)) != 0 {
		t.Errorf("calls = %d, limits %+v", h.calls.Load(), e.o.Limits(bg))
	}
}

// rad asks the agents that answer when it starts, so their limits show
// before any session runs.
func TestRunReadsLimits(t *testing.T) {
	t.Parallel()
	e := newEnv(t, false)
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	go e.o.Run(ctx)
	e.waitFor("the fake's limits", func() bool { _, ok := e.o.Limits(bg)["fake"]; return ok })
	done := e.o.ClientConnected()
	done()
	done() // once
	e.o.limits.mu.Lock()
	clients := e.o.limits.clients
	e.o.limits.mu.Unlock()
	if clients != 0 {
		t.Errorf("clients = %d", clients)
	}
}

func TestLimitError(t *testing.T) {
	now := time.Date(2026, 10, 31, 10, 0, 0, 0, time.Local)
	today, tue, later := now.Add(5*time.Hour), now.Add(4*24*time.Hour), now.Add(10*24*time.Hour)
	for _, c := range []struct {
		w    model.UsageWindow
		want string
	}{
		{model.UsageWindow{ID: "five_hour", Label: "5h", ResetsAt: &today}, "Claude Code's 5-hour limit is reached; it resets at 15:00 " + today.Format("MST") + "."},
		{model.UsageWindow{ID: "weekly", Label: "week", ResetsAt: &tue}, "Claude Code's weekly limit is reached; it resets on Wed at 10:00 " + tue.Format("MST") + "."},
		{model.UsageWindow{ID: "2h", Label: "2h", ResetsAt: &later}, "Claude Code's 2h limit is reached; it resets on 10 Nov at 10:00 " + later.Format("MST") + "."},
		{model.UsageWindow{ID: "usage", Label: "usage"}, "Claude Code's usage limit is reached."},
	} {
		if got := limitError("Claude Code", c.w, now); got != c.want {
			t.Errorf("limitError(%+v) = %q, want %q", c.w, got, c.want)
		}
	}
}
