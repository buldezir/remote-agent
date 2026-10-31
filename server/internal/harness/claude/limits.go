package claude

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"remote-agent/internal/harness"
	"remote-agent/internal/model"
)

// rateLimitInfo is a rate_limit_event's: how much of the plan's windows the
// account has used, and whether this request was allowed (status), for the
// window rateLimitType names, which resets at resetsAt.
type rateLimitInfo struct {
	Status         string   `json:"status"` // allowed, allowed_warning, rejected
	ResetsAt       int64    `json:"resetsAt"`
	RateLimitType  string   `json:"rateLimitType"` // five_hour, seven_day, seven_day_opus…
	Utilization    *float64 `json:"utilization"`
	IsUsingOverage bool     `json:"isUsingOverage"`
	UnifiedWindows map[string]struct {
		Utilization float64 `json:"utilization"`
		ResetsAt    int64   `json:"resetsAt"`
	} `json:"unifiedWindows"`
}

type claudeWindowName struct {
	key, id, label string
	minutes        int
}

// claudeWindows are the windows Claude meters, in the order they show.
var claudeWindows = []claudeWindowName{
	{"five_hour", "five_hour", "5h", 300},
	{"seven_day", "weekly", "week", 7 * 24 * 60},
	{"seven_day_opus", "weekly_opus", "Opus week", 7 * 24 * 60},
	{"seven_day_sonnet", "weekly_sonnet", "Sonnet week", 7 * 24 * 60},
}

func claudeWindow(key string) model.UsageWindow {
	for _, w := range claudeWindows {
		if w.key == key {
			return model.UsageWindow{ID: w.id, Label: w.label, WindowMinutes: w.minutes}
		}
	}
	return model.UsageWindow{ID: key, Label: strings.ReplaceAll(key, "_", " ")}
}

// usageLimits reads a rate_limit_event, and the window it hit when Claude
// refused the request for a limit (and has no extra usage to go on with).
func usageLimits(info *rateLimitInfo) (model.UsageLimits, *model.UsageWindow) {
	l := model.UsageLimits{Windows: []model.UsageWindow{}, Overage: info.IsUsingOverage, UpdatedAt: time.Now().UTC()}
	keys := make([]string, 0, len(info.UnifiedWindows))
	for k := range info.UnifiedWindows {
		keys = append(keys, k)
	}
	order := func(k string) int {
		i := slices.IndexFunc(claudeWindows, func(w claudeWindowName) bool { return w.key == k })
		if i < 0 {
			return len(claudeWindows)
		}
		return i
	}
	slices.SortFunc(keys, func(a, b string) int {
		if d := order(a) - order(b); d != 0 {
			return d
		}
		return strings.Compare(a, b)
	})
	for _, k := range keys {
		u := info.UnifiedWindows[k]
		w := claudeWindow(k)
		w.UsedPercent, w.ResetsAt = harness.UsedPercent(u.Utilization*100), harness.UnixTime(u.ResetsAt)
		l.Windows = append(l.Windows, w)
	}
	if len(keys) == 0 && info.RateLimitType != "" && info.Utilization != nil {
		w := claudeWindow(info.RateLimitType)
		w.UsedPercent, w.ResetsAt = harness.UsedPercent(*info.Utilization*100), harness.UnixTime(info.ResetsAt)
		l.Windows = append(l.Windows, w)
	}
	if info.Status != "rejected" || info.IsUsingOverage {
		return l, nil
	}
	l.Limited, l.ResetsAt = true, harness.UnixTime(info.ResetsAt)
	hit := claudeWindow(cmp.Or(info.RateLimitType, "five_hour"))
	i := slices.IndexFunc(l.Windows, func(w model.UsageWindow) bool { return w.ID == hit.ID })
	if i < 0 {
		l.Windows = append(l.Windows, hit)
		i = len(l.Windows) - 1
	}
	l.Windows[i].UsedPercent = 100
	if l.ResetsAt != nil {
		l.Windows[i].ResetsAt = l.ResetsAt
	}
	w := l.Windows[i]
	return l, &w
}

// onRateLimit reports the account's limits, and notes a limit the turn hit.
func (r *runtime) onRateLimit(info *rateLimitInfo) {
	if info == nil {
		return
	}
	l, hit := usageLimits(info)
	r.mu.Lock()
	r.limitHit = hit
	r.mu.Unlock()
	r.emit(harness.UsageLimits{Limits: l})
}
