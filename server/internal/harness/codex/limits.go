package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"remote-agent/internal/harness"
	"remote-agent/internal/harness/jsonrpc"
	"remote-agent/internal/harness/proc"
	"remote-agent/internal/model"
)

// rateLimits is Codex's RateLimitSnapshot: the account's two windows (5
// hours and a week, by their length), its plan, and the limit it reached.
type rateLimits struct {
	Primary              *rateLimitWindow `json:"primary"`
	Secondary            *rateLimitWindow `json:"secondary"`
	PlanType             *string          `json:"planType"`
	RateLimitReachedType *string          `json:"rateLimitReachedType"`
}

type rateLimitWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins *int    `json:"windowDurationMins"`
	ResetsAt           *int64  `json:"resetsAt"`
}

// readLimits is account/rateLimits/read's answer.
type readLimits struct {
	RateLimits *rateLimits `json:"rateLimits"`
	// OrdinaryUsageAllowed false: the plan's usage is spent; null: unknown.
	OrdinaryUsageAllowed *bool `json:"ordinaryUsageAllowed"`
}

func (s rateLimits) usage(allowed *bool) model.UsageLimits {
	l := model.UsageLimits{Windows: []model.UsageWindow{}, UpdatedAt: time.Now().UTC(),
		Limited: s.RateLimitReachedType != nil || allowed != nil && !*allowed}
	if s.PlanType != nil && *s.PlanType != "unknown" {
		l.Plan = harness.PlanName(*s.PlanType)
	}
	for _, w := range []*rateLimitWindow{s.Primary, s.Secondary} {
		if w == nil {
			continue
		}
		mins := 0
		if w.WindowDurationMins != nil {
			mins = *w.WindowDurationMins
		}
		uw := model.UsageWindow{UsedPercent: harness.UsedPercent(w.UsedPercent), WindowMinutes: mins}
		uw.ID, uw.Label = harness.LimitWindow(mins)
		if w.ResetsAt != nil {
			uw.ResetsAt = harness.UnixTime(*w.ResetsAt)
		}
		l.Windows = append(l.Windows, uw)
		// A spent window holds the limit until it resets: the last to, if both are.
		if l.Limited && uw.UsedPercent >= 100 && uw.ResetsAt != nil && (l.ResetsAt == nil || uw.ResetsAt.After(*l.ResetsAt)) {
			l.ResetsAt = uw.ResetsAt
		}
	}
	return l
}

func (r readLimits) usage() (model.UsageLimits, error) {
	if r.RateLimits == nil {
		return model.UsageLimits{}, fmt.Errorf("codex reported no rate limits")
	}
	return r.RateLimits.usage(r.OrdinaryUsageAllowed), nil
}

// ReadLimits asks a Codex of its own for the account's limits: an
// app-server, without a thread, as Probe starts one.
func (h *Harness) ReadLimits(ctx context.Context) (model.UsageLimits, error) {
	p, err := proc.Start(h.spec(""))
	if err != nil {
		return model.UsageLimits{}, err
	}
	defer p.Stop(time.Second)
	c := jsonrpc.New(p, "", jsonrpc.Handler{})
	go c.Run()
	if err := initialize(ctx, c, false); err != nil {
		return model.UsageLimits{}, err
	}
	return callReadLimits(ctx, c)
}

// ReadLimits asks the session's Codex, which needn't be in a turn.
func (r *runtime) ReadLimits(ctx context.Context) (model.UsageLimits, error) {
	l, err := callReadLimits(ctx, r.conn)
	if err == nil {
		r.mu.Lock()
		r.limits = &l
		r.mu.Unlock()
	}
	return l, err
}

func callReadLimits(ctx context.Context, c *jsonrpc.Conn) (model.UsageLimits, error) {
	var res readLimits
	// A background poll: the reset credits' details are another lookup.
	if err := c.Call(ctx, "account/rateLimits/read", map[string]any{"excludeResetCreditDetails": true}, &res); err != nil {
		return model.UsageLimits{}, err
	}
	return res.usage()
}

// onRateLimits reports the limits Codex sends as they change, such as
// after each of its turns.
func (r *runtime) onRateLimits(raw json.RawMessage) {
	var p struct {
		RateLimits *rateLimits `json:"rateLimits"`
	}
	if json.Unmarshal(raw, &p) != nil || p.RateLimits == nil {
		return
	}
	l := p.RateLimits.usage(nil)
	r.mu.Lock()
	r.limits = &l
	r.mu.Unlock()
	r.emit(harness.UsageLimits{Limits: l})
}

// limitError reports whether a turn's error is Codex's for a spent usage
// limit (codexErrorInfo usageLimitExceeded), as opposed to the API's rate.
func limitError(info json.RawMessage) bool {
	var code string
	return json.Unmarshal(info, &code) == nil && code == "usageLimitExceeded"
}
