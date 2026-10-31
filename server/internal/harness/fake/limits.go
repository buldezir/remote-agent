package fake

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"remote-agent/internal/harness"
	"remote-agent/internal/model"
)

// account is the fake's plan, shared by its sessions as an account's is:
// each turn uses 6% of its 5 hours and 2% of its week. "limits <n>" sets
// the 5 hours' use, and "limit" spends them, failing the turn; the next
// turn starts them afresh.
var account = struct {
	sync.Mutex
	fiveHour, week int
	hit            bool
	start          time.Time
}{fiveHour: 12, week: 34}

// ReadLimits answers as Codex's app-server does, without a session.
func (Harness) ReadLimits(context.Context) (model.UsageLimits, error) {
	account.Lock()
	defer account.Unlock()
	return usage(), nil
}

// usage is the account's limits now (account held).
func usage() model.UsageLimits {
	now := time.Now().UTC()
	if account.start.IsZero() {
		account.start = now
	}
	resets := func(first, every time.Duration) *time.Time {
		t := account.start.Add(first)
		for t.Before(now) {
			t = t.Add(every)
		}
		return &t
	}
	l := model.UsageLimits{Plan: "Fake", UpdatedAt: now, Windows: []model.UsageWindow{
		{ID: "five_hour", Label: "5h", UsedPercent: account.fiveHour, ResetsAt: resets(2*time.Hour+14*time.Minute, 5*time.Hour), WindowMinutes: 300},
		{ID: "weekly", Label: "week", UsedPercent: account.week, ResetsAt: resets(76*time.Hour, 7*24*time.Hour), WindowMinutes: 7 * 24 * 60},
	}}
	if account.hit {
		l.Limited, l.ResetsAt = true, l.Windows[0].ResetsAt
	}
	return l
}

// useLimits is the turn's use of the account: what text asks for, else 6%
// of the 5 hours and 2% of the week. hit is the window it spent.
func useLimits(text string) (l model.UsageLimits, hit *model.UsageWindow) {
	account.Lock()
	defer account.Unlock()
	words := strings.Fields(text)
	switch {
	case containsWord(words, "limit"):
		account.fiveHour, account.hit = 100, true
	case containsWord(words, "limits"):
		for i, w := range words[:len(words)-1] {
			if n, err := strconv.Atoi(strings.TrimSuffix(words[i+1], "%")); w == "limits" && err == nil {
				account.fiveHour, account.hit = max(0, min(n, 99)), false
			}
		}
	default:
		if account.hit {
			account.fiveHour, account.hit = 0, false
		}
		account.fiveHour = min(account.fiveHour+6, 99)
		account.week = min(account.week+2, 99)
	}
	l = usage()
	if account.hit {
		w := l.Windows[0]
		hit = &w
	}
	return l, hit
}

func containsWord(words []string, w string) bool {
	for _, x := range words {
		if strings.Trim(x, ".,!?") == w {
			return true
		}
	}
	return false
}

var _ harness.LimitsReader = Harness{}
