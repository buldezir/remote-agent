package codex

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"remote-agent/internal/config"
	"remote-agent/internal/harness"
	"remote-agent/internal/harness/replaytest"
	"remote-agent/internal/model"
)

func limitsOf(turn *replaytest.Turn) []model.UsageLimits {
	var out []model.UsageLimits
	for _, ev := range turn.Events {
		if ev, ok := ev.(harness.UsageLimits); ok {
			ev.Limits.UpdatedAt = time.Time{}
			out = append(out, ev.Limits)
		}
	}
	return out
}

func at(s int64) *time.Time { return harness.UnixTime(s) }

// Codex sends the account's limits at the end of each turn
// (account/rateLimits/updated), recorded in a fork's turn.
func TestReplayUsageLimits(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "codex-fork/ws")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	exe, env := replaytest.Command(t, "testdata/fork.ndjson", root)
	rt, err := New(config.Command{Command: exe, Env: env}).Open(context.Background(), harness.OpenOptions{SessionID: "local", Cwd: cwd,
		Mode: "auto", Model: "gpt-6-luna", Effort: "low", ResumeID: "01a18f38-e752-7043-a080-f16a53d65767", Fork: true,
		ForkAt: "01a18f38-e784-7600-93cd-5055c9f7b559"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	turn := replaytest.RunTurn(t, rt, "Without using any tools: list every request I have made to you in this conversation, in one short sentence.", nil)
	want := []model.UsageLimits{{Windows: []model.UsageWindow{
		{ID: "five_hour", Label: "5h", UsedPercent: 0, ResetsAt: at(1793422236), WindowMinutes: 300},
		{ID: "weekly", Label: "week", UsedPercent: 0, ResetsAt: at(1793785422), WindowMinutes: 10080},
	}, Plan: "Plus"}}
	if got := limitsOf(turn); !reflect.DeepEqual(got, want) {
		t.Errorf("limits = %+v, want %+v", got, want)
	}
	replaytest.CloseClean(t, rt)
}

// A turn Codex refused for the plan's limit (usageLimitExceeded) fails on
// the window it spent. The transcript is synthetic, written from the
// app-server's schema (Codex 0.164), as no recording hit a limit.
func TestReplayLimitReached(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "codex-rec/ws")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	exe, env := replaytest.Command(t, "testdata/limit.ndjson", root)
	rt, err := New(config.Command{Command: exe, Env: env}).Open(context.Background(), harness.OpenOptions{SessionID: "local", Cwd: cwd, Mode: "auto"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	turn := replaytest.RunTurn(t, rt, "Rename the helper to loadConfig.", nil)
	reset := at(1793422236)
	fiveHour := model.UsageWindow{ID: "five_hour", Label: "5h", UsedPercent: 100, ResetsAt: reset, WindowMinutes: 300}
	want := []model.UsageLimits{{Windows: []model.UsageWindow{fiveHour,
		{ID: "weekly", Label: "week", UsedPercent: 41, ResetsAt: at(1793785422), WindowMinutes: 10080},
	}, Limited: true, ResetsAt: reset, Plan: "Plus"}}
	if got := limitsOf(turn); !reflect.DeepEqual(got, want) {
		t.Errorf("limits = %+v, want %+v", got, want)
	}
	if turn.End.Status != model.TurnFailed || !reflect.DeepEqual(turn.End.Limit, &fiveHour) {
		t.Errorf("end = %+v, limit %+v", turn.End, turn.End.Limit)
	}
	replaytest.CloseClean(t, rt)
}

// ReadLimits asks an app-server of its own, without a thread. The answer is
// recorded from Codex 0.164.
func TestReadLimits(t *testing.T) {
	exe, env := replaytest.Command(t, "testdata/limits-read.ndjson", t.TempDir())
	got, err := New(config.Command{Command: exe, Env: env}).ReadLimits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got.UpdatedAt = time.Time{}
	want := model.UsageLimits{Windows: []model.UsageWindow{
		{ID: "five_hour", Label: "5h", UsedPercent: 0, ResetsAt: at(1793504336), WindowMinutes: 300},
		{ID: "weekly", Label: "week", UsedPercent: 2, ResetsAt: at(1793785422), WindowMinutes: 10080},
	}, Plan: "Plus"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("limits = %+v, want %+v", got, want)
	}
}

func TestUsageOfSnapshot(t *testing.T) {
	ptr := func(s string) *string { return &s }
	mins := func(n int) *int { return &n }
	secs := func(n int64) *int64 { return &n }
	no := false
	cases := []struct {
		name    string
		s       rateLimits
		allowed *bool
		want    model.UsageLimits
	}{
		{"other lengths, no plan", rateLimits{Primary: &rateLimitWindow{UsedPercent: 12.5, WindowDurationMins: mins(1440)},
			Secondary: &rateLimitWindow{UsedPercent: 3, WindowDurationMins: mins(120)}, PlanType: ptr("unknown")},
			nil, model.UsageLimits{Windows: []model.UsageWindow{
				{ID: "daily", Label: "day", UsedPercent: 13, WindowMinutes: 1440},
				{ID: "2h", Label: "2h", UsedPercent: 3, WindowMinutes: 120}}}},
		{"both spent: the later reset", rateLimits{Primary: &rateLimitWindow{UsedPercent: 100, WindowDurationMins: mins(300), ResetsAt: secs(10)},
			Secondary: &rateLimitWindow{UsedPercent: 100, WindowDurationMins: mins(10080), ResetsAt: secs(20)},
			PlanType:  ptr("edu_plus"), RateLimitReachedType: ptr("rate_limit_reached")},
			nil, model.UsageLimits{Windows: []model.UsageWindow{
				{ID: "five_hour", Label: "5h", UsedPercent: 100, ResetsAt: at(10), WindowMinutes: 300},
				{ID: "weekly", Label: "week", UsedPercent: 100, ResetsAt: at(20), WindowMinutes: 10080}},
				Limited: true, ResetsAt: at(20), Plan: "Edu Plus"}},
		{"ordinary usage not allowed, no window spent", rateLimits{Primary: &rateLimitWindow{UsedPercent: 40, WindowDurationMins: mins(300)}},
			&no, model.UsageLimits{Windows: []model.UsageWindow{{ID: "five_hour", Label: "5h", UsedPercent: 40, WindowMinutes: 300}}, Limited: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.s.usage(c.allowed)
			got.UpdatedAt = time.Time{}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %+v\nwant %+v", got, c.want)
			}
		})
	}
}
