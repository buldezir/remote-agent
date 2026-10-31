package harness

import (
	"fmt"
	"math"
	"strings"
	"time"

	"remote-agent/internal/model"
)

// LimitWindow names a usage window by its length, for agents that give only
// that (Codex): the 5 hours, the week, a day or another length.
func LimitWindow(minutes int) (id, label string) {
	switch minutes {
	case 300:
		return "five_hour", "5h"
	case 7 * 24 * 60:
		return "weekly", "week"
	case 24 * 60:
		return "daily", "day"
	}
	switch {
	case minutes <= 0:
		return "window", "limit"
	case minutes%(24*60) == 0:
		return fmt.Sprintf("%dd", minutes/(24*60)), fmt.Sprintf("%dd", minutes/(24*60))
	case minutes%60 == 0:
		return fmt.Sprintf("%dh", minutes/60), fmt.Sprintf("%dh", minutes/60)
	}
	return fmt.Sprintf("%dm", minutes), fmt.Sprintf("%dm", minutes)
}

// UsedPercent rounds a percentage for UsageWindow: 0 to 100, where 100 means
// the limit is reached, so 99.6 is 99.
func UsedPercent(p float64) int {
	switch {
	case math.IsNaN(p) || p <= 0:
		return 0
	case p >= 100:
		return 100
	}
	return min(int(math.Round(p)), 99)
}

// UnixTime is a reset time given in seconds, or nil for none.
func UnixTime(s int64) *time.Time {
	if s <= 0 {
		return nil
	}
	t := time.Unix(s, 0).UTC()
	return &t
}

// PlanName is a plan's id as people know it: "plus" is Plus, "edu_plus" Edu Plus.
func PlanName(id string) string {
	words := strings.FieldsFunc(id, func(r rune) bool { return r == '_' || r == '-' || r == ' ' })
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// LimitReached is the window a turn's failure hit: the most used of limits'
// windows, at 100%, or a window for the plan's usage as a whole when the
// agent didn't say which.
func LimitReached(limits *model.UsageLimits) *model.UsageWindow {
	w := model.UsageWindow{ID: "usage", Label: "usage"}
	if limits != nil {
		for i, x := range limits.Windows {
			if i == 0 || x.UsedPercent > w.UsedPercent {
				w = x
			}
		}
		if limits.ResetsAt != nil {
			w.ResetsAt = limits.ResetsAt
		}
	}
	w.UsedPercent = 100
	return &w
}
