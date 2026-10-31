package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"remote-agent/internal/harness"
	"remote-agent/internal/model"
	"remote-agent/internal/store"
)

// Usage limits are an agent's account's, not a session's: rad keeps the
// latest each agent reported, from any of its sessions (harness.UsageLimits)
// or from asking it (harness.LimitsReader), as a limits.upserted event in the
// index stream, whose change feed keeps it across restarts.

const (
	// limitsRefresh is how old an agent's limits may get while clients are
	// connected before rad asks an agent that answers (Codex) again.
	limitsRefresh = 10 * time.Minute
	// limitsUnchanged is how long a report that changes nothing but its time
	// goes unwritten, so a turn's many reports write once.
	limitsUnchanged = time.Minute
)

type limitsState struct {
	mu      sync.Mutex
	latest  map[string]model.UsageLimits // by harness; nil until loaded from the store
	tried   map[string]time.Time         // when rad last asked an agent
	clients int                          // connected clients (ClientConnected)
	wake    chan struct{}                // a client connected
	reading sync.Mutex                   // one refresh at a time
}

func (l *limitsState) load(ctx context.Context, st *store.Store) {
	if l.latest != nil {
		return
	}
	l.latest = map[string]model.UsageLimits{}
	saved, err := st.Limits(ctx)
	if err != nil {
		return
	}
	for _, x := range saved {
		l.latest[x.Harness] = *x
	}
}

// setLimits keeps an agent's limits and tells the clients, unless only their
// time changed, within limitsUnchanged.
func (o *Orchestrator) setLimits(ctx context.Context, id string, l model.UsageLimits) {
	l.Harness = id
	if l.UpdatedAt.IsZero() {
		l.UpdatedAt = time.Now().UTC()
	}
	if l.Windows == nil {
		l.Windows = []model.UsageWindow{}
	}
	o.limits.mu.Lock()
	defer o.limits.mu.Unlock()
	o.limits.load(ctx, o.st)
	if prev, ok := o.limits.latest[id]; ok && sameLimits(prev, l) && l.UpdatedAt.Sub(prev.UpdatedAt) < limitsUnchanged {
		return
	}
	o.limits.latest[id] = l
	if _, err := o.st.Write(ctx, func(w *store.W) error { return w.PutLimits(&l) }); err != nil {
		o.log.Error("persist usage limits", "harness", id, "err", err)
	}
}

func sameLimits(a, b model.UsageLimits) bool {
	a.UpdatedAt, b.UpdatedAt = time.Time{}, time.Time{}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// Limits lists the latest usage limits of each agent that reported them.
func (o *Orchestrator) Limits(ctx context.Context) map[string]model.UsageLimits {
	o.limits.mu.Lock()
	defer o.limits.mu.Unlock()
	o.limits.load(ctx, o.st)
	out := make(map[string]model.UsageLimits, len(o.limits.latest))
	for id, l := range o.limits.latest {
		out[id] = l
	}
	return out
}

// ClientConnected counts a client while it stays connected (done), for
// which rad keeps agents' limits fresh: it asks those that answer at once if
// theirs are old, and then every limitsRefresh.
func (o *Orchestrator) ClientConnected() (done func()) {
	o.limits.mu.Lock()
	o.limits.clients++
	o.limits.mu.Unlock()
	select {
	case o.limits.wake <- struct{}{}:
	default:
	}
	return sync.OnceFunc(func() {
		o.limits.mu.Lock()
		o.limits.clients--
		o.limits.mu.Unlock()
	})
}

// watchLimits asks the agents that answer for their limits when rad starts,
// and then keeps them fresh while clients are connected.
func (o *Orchestrator) watchLimits(ctx context.Context) {
	o.RefreshLimits(ctx)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-o.limits.wake:
		case <-t.C:
		}
		o.limits.mu.Lock()
		watched := o.limits.clients > 0
		o.limits.mu.Unlock()
		if watched {
			o.RefreshLimits(ctx)
		}
	}
}

// RefreshLimits asks each agent that answers (harness.LimitsReader) whose
// limits are older than limitsRefresh, or whose limit or window has reset
// since, for them: through a session's running agent where there is one,
// else on its own. It leaves agents that aren't installed or signed in, and
// any it asked within limitsRefresh.
func (o *Orchestrator) RefreshLimits(ctx context.Context) {
	if !o.limits.reading.TryLock() {
		return
	}
	defer o.limits.reading.Unlock()
	now := time.Now()
	for _, h := range o.reg.All() {
		id := h.ID()
		if _, ok := h.(harness.LimitsReader); !ok {
			continue
		}
		if info, ok := o.reg.Cached(id); ok && (!info.Installed || !info.AuthOK) {
			continue
		}
		o.limits.mu.Lock()
		o.limits.load(ctx, o.st)
		l, known := o.limits.latest[id]
		tried := o.limits.tried[id]
		due := (!known || stale(l, now)) && now.Sub(tried) >= limitsRefresh
		if due {
			o.limits.tried[id] = now
		}
		o.limits.mu.Unlock()
		if !due {
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		got, err := o.limitsReader(id, h).ReadLimits(rctx)
		cancel()
		if err != nil {
			o.log.Debug("read usage limits", "harness", id, "err", err)
			continue
		}
		o.setLimits(ctx, id, got)
	}
}

// stale reports whether limits are worth asking for again: they're old, or
// a limit or window they name has reset since.
func stale(l model.UsageLimits, now time.Time) bool {
	if now.Sub(l.UpdatedAt) >= limitsRefresh {
		return true
	}
	if l.Limited && l.ResetsAt != nil && now.After(*l.ResetsAt) {
		return true
	}
	return slices.ContainsFunc(l.Windows, func(w model.UsageWindow) bool { return w.ResetsAt != nil && now.After(*w.ResetsAt) })
}

// limitsReader is a running agent of the harness that answers, so that rad
// needn't start one, else the harness.
func (o *Orchestrator) limitsReader(id string, h harness.Harness) harness.LimitsReader {
	o.mu.Lock()
	actors := make([]*actor, 0, len(o.actors))
	for _, a := range o.actors {
		actors = append(actors, a)
	}
	o.mu.Unlock()
	for _, a := range actors {
		if !a.mu.TryLock() { // busy: another will do, or the harness
			continue
		}
		rt, hid := a.rt, a.sess.Harness
		a.mu.Unlock()
		if r, ok := rt.(harness.LimitsReader); ok && hid == id {
			return r
		}
	}
	return h.(harness.LimitsReader)
}

// limitError says which limit a turn hit and when it resets, in the
// server's time zone, for the turn's error.
func limitError(agent string, w model.UsageWindow, now time.Time) string {
	name := map[string]string{"five_hour": "5-hour", "weekly": "weekly", "daily": "daily", "weekly_opus": "weekly Opus",
		"weekly_sonnet": "weekly Sonnet", "usage": "usage"}[w.ID]
	if name == "" {
		name = w.Label
	}
	s := fmt.Sprintf("%s's %s limit is reached", agent, name)
	if w.ResetsAt != nil && w.ResetsAt.After(now) {
		s += "; it resets " + resetTime(w.ResetsAt.Local(), now.Local())
	}
	return s + "."
}

func resetTime(t, now time.Time) string {
	y, m, d := t.Date()
	ny, nm, nd := now.Date()
	switch {
	case y == ny && m == nm && d == nd:
		return "at " + t.Format("15:04 MST")
	case t.Sub(now) < 6*24*time.Hour:
		return t.Format("on Mon at 15:04 MST")
	}
	return t.Format("on 2 Jan at 15:04 MST")
}

// agentName is the harness's name as clients show it.
func (o *Orchestrator) agentName(id string) string {
	if info, ok := o.reg.Cached(id); ok && info.Name != "" {
		return info.Name
	}
	return strings.ToUpper(id[:1]) + id[1:]
}
