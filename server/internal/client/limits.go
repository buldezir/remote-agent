package client

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"remote-agent/internal/model"
)

// Usage limits: the gauge the clients show for an agent's plan ("5h 34% ·
// week 61%"), its colour, the details behind it, and the notice for a turn
// that hit a limit. RAKit's UsageLimitsFormat, web/src/store/usageLimits.ts
// and android/core's UsageLimitsFormat port it, and their tests share
// protocol/vectors/usage_limits.json.

// LimitLevel is how near a limit is, for the gauge's colour.
type LimitLevel string

const (
	LimitNormal  LimitLevel = "normal"
	LimitWarning LimitLevel = "warning" // amber: WarningPercent or more used
	LimitReached LimitLevel = "limited" // red: spent
)

// WarningPercent is the use from which a window shows amber.
const WarningPercent = 80

// WindowPercent is a window's use now: 0 once it has reset since the agent
// said.
func WindowPercent(w model.UsageWindow, now time.Time) int {
	if w.ResetsAt != nil && !w.ResetsAt.After(now) {
		return 0
	}
	return max(0, min(w.UsedPercent, 100))
}

func percentLevel(p int) LimitLevel {
	switch {
	case p >= 100:
		return LimitReached
	case p >= WarningPercent:
		return LimitWarning
	}
	return LimitNormal
}

// WindowLevel is a window's colour now.
func WindowLevel(w model.UsageWindow, now time.Time) LimitLevel {
	return percentLevel(WindowPercent(w, now))
}

// LimitedNow reports whether the limit the agent hit hasn't reset yet.
func LimitedNow(l *model.UsageLimits, now time.Time) bool {
	return l.Limited && (l.ResetsAt == nil || l.ResetsAt.After(now))
}

// LimitsLevel is the gauge's colour: its most used window's, or red while a
// limit holds.
func LimitsLevel(l *model.UsageLimits, now time.Time) LimitLevel {
	if LimitedNow(l, now) {
		return LimitReached
	}
	level := LimitNormal
	for _, w := range l.Windows {
		switch WindowLevel(w, now) {
		case LimitReached:
			return LimitReached
		case LimitWarning:
			level = LimitWarning
		}
	}
	return level
}

// LimitsGauge is the gauge: each window's label and use, "5h 34% · week
// 61%"; "" with nothing to show.
func LimitsGauge(l *model.UsageLimits, now time.Time) string {
	parts := make([]string, 0, len(l.Windows))
	for _, w := range l.Windows {
		parts = append(parts, fmt.Sprintf("%s %d%%", w.Label, WindowPercent(w, now)))
	}
	if len(parts) == 0 && LimitedNow(l, now) {
		return "Limit reached"
	}
	return strings.Join(parts, " · ")
}

// LimitsDetails are the lines behind the gauge, on hover or a tap: the limit
// hit, each window's use and reset, extra usage, the plan, and how old they
// are.
func LimitsDetails(l *model.UsageLimits, now time.Time) []string {
	var lines []string
	if LimitedNow(l, now) {
		lines = append(lines, "Limit reached"+resetsIn(l.ResetsAt, now))
	}
	for _, w := range l.Windows {
		if w.ResetsAt != nil && !w.ResetsAt.After(now) {
			lines = append(lines, fmt.Sprintf("%s: reset %s ago", w.Label, LimitDuration(now.Sub(*w.ResetsAt), false)))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %d%% used%s", w.Label, WindowPercent(w, now), resetsIn(w.ResetsAt, now)))
	}
	if l.Overage {
		lines = append(lines, "Using extra usage")
	}
	if l.Plan != "" {
		lines = append(lines, l.Plan+" plan")
	}
	if age := now.Sub(l.UpdatedAt); age < time.Minute {
		lines = append(lines, "Updated just now")
	} else {
		lines = append(lines, "Updated "+LimitDuration(age, false)+" ago")
	}
	return lines
}

func resetsIn(t *time.Time, now time.Time) string {
	if t == nil || !t.After(now) {
		return ""
	}
	return " · resets in " + LimitDuration(t.Sub(now), true)
}

// LimitNotice is what the session shows for an error whose turn hit the
// limit w: "5h limit reached · resets in 2h 14m".
func LimitNotice(w model.UsageWindow, now time.Time) string {
	s := w.Label + " limit reached"
	switch {
	case w.ResetsAt == nil:
	case w.ResetsAt.After(now):
		s += " · resets in " + LimitDuration(w.ResetsAt.Sub(now), true)
	default:
		s += " · reset " + LimitDuration(now.Sub(*w.ResetsAt), false) + " ago"
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

// LimitDuration is a span to a reset, or since one: "45m", "2h 14m", "3d 4h".
// A span to come rounds its minutes up, one past down; either is a minute at
// least.
func LimitDuration(d time.Duration, future bool) string {
	m := int(d / time.Minute)
	if future && d%time.Minute != 0 {
		m++
	}
	m = max(m, 1)
	switch {
	case m < 60:
		return fmt.Sprintf("%dm", m)
	case m < 24*60:
		if m%60 == 0 {
			return fmt.Sprintf("%dh", m/60)
		}
		return fmt.Sprintf("%dh %dm", m/60, m%60)
	}
	if h := m % (24 * 60) / 60; h > 0 {
		return fmt.Sprintf("%dd %dh", m/(24*60), h)
	}
	return fmt.Sprintf("%dd", m/(24*60))
}
