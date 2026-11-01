package claude

import (
	"reflect"
	"testing"
	"time"

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

// Claude says how much of the account's windows it has used with each
// request it makes (rate_limit_event), which the adapter passes on.
func TestReplayUsageLimits(t *testing.T) {
	rt, _ := openReplay(t, "testdata/write.ndjson", "claude-rec/ws", harness.OpenOptions{SessionID: "local-session", Mode: "default"})
	turn := replaytest.RunTurn(t, rt, "Create a file hello.txt containing the word hi. Then reply with one short sentence.",
		func(harness.ApprovalEvent) string { return "allow" })
	want := []model.UsageLimits{{Windows: []model.UsageWindow{
		{ID: "five_hour", Label: "5h", UsedPercent: 7, ResetsAt: at(1791516600), WindowMinutes: 300},
		{ID: "weekly", Label: "week", UsedPercent: 10, ResetsAt: at(1791820800), WindowMinutes: 10080},
	}}}
	if got := limitsOf(turn); !reflect.DeepEqual(got, want) {
		t.Errorf("limits = %+v, want %+v", got, want)
	}
	if turn.End.Status != model.TurnCompleted || turn.End.Limit != nil {
		t.Errorf("end = %+v", turn.End)
	}
	replaytest.CloseClean(t, rt) // and the player reaches the end
}

// A request Claude refused for the 5-hour limit fails the turn on it. The
// transcript is synthetic, in the shape of the recorded rate_limit_events
// with the status "rejected", as no recording hit a limit.
func TestReplayLimitReached(t *testing.T) {
	rt, _ := openReplay(t, "testdata/limit.ndjson", "claude-rec/ws", harness.OpenOptions{SessionID: "local-session", Mode: "default"})
	turn := replaytest.RunTurn(t, rt, "Rename the helper to loadConfig.", nil)
	reset := at(1791531000)
	fiveHour := model.UsageWindow{ID: "five_hour", Label: "5h", UsedPercent: 100, ResetsAt: reset, WindowMinutes: 300}
	want := []model.UsageLimits{{Windows: []model.UsageWindow{fiveHour,
		{ID: "weekly", Label: "week", UsedPercent: 47, ResetsAt: at(1791763200), WindowMinutes: 10080},
	}, Limited: true, ResetsAt: reset}}
	if got := limitsOf(turn); !reflect.DeepEqual(got, want) {
		t.Errorf("limits = %+v, want %+v", got, want)
	}
	if turn.End.Status != model.TurnFailed || !reflect.DeepEqual(turn.End.Limit, &fiveHour) ||
		turn.End.Error != "You've hit your limit · resets 7:30am (Europe/Berlin)" {
		t.Errorf("end = %+v, limit %+v", turn.End, turn.End.Limit)
	}
	replaytest.CloseClean(t, rt) // and the player reaches the end
}

func TestUsageLimitsFromEvent(t *testing.T) {
	util := 0.996
	cases := []struct {
		name string
		info rateLimitInfo
		want model.UsageLimits
		hit  *model.UsageWindow
	}{
		{"windows in Claude's order, unknown ones last", rateLimitInfo{Status: "allowed_warning", UnifiedWindows: map[string]struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    int64   `json:"resetsAt"`
		}{"seven_day_opus": {0.5, 0}, "five_hour": {0.851, 100}, "seven_day": {0.2, 200}, "overage_month": {0.1, 0}}},
			model.UsageLimits{Windows: []model.UsageWindow{
				{ID: "five_hour", Label: "5h", UsedPercent: 85, ResetsAt: at(100), WindowMinutes: 300},
				{ID: "weekly", Label: "week", UsedPercent: 20, ResetsAt: at(200), WindowMinutes: 10080},
				{ID: "weekly_opus", Label: "Opus week", UsedPercent: 50, WindowMinutes: 10080},
				{ID: "overage_month", Label: "overage month", UsedPercent: 10},
			}}, nil},
		{"no unified windows: the one it names", rateLimitInfo{Status: "allowed", RateLimitType: "seven_day", Utilization: &util, ResetsAt: 300},
			model.UsageLimits{Windows: []model.UsageWindow{{ID: "weekly", Label: "week", UsedPercent: 99, ResetsAt: at(300), WindowMinutes: 10080}}}, nil},
		{"rejected but on extra usage", rateLimitInfo{Status: "rejected", RateLimitType: "five_hour", ResetsAt: 400, IsUsingOverage: true},
			model.UsageLimits{Windows: []model.UsageWindow{}, Overage: true}, nil},
		{"rejected for a window it didn't list", rateLimitInfo{Status: "rejected", RateLimitType: "seven_day_sonnet", ResetsAt: 500},
			model.UsageLimits{Windows: []model.UsageWindow{{ID: "weekly_sonnet", Label: "Sonnet week", UsedPercent: 100, ResetsAt: at(500), WindowMinutes: 10080}},
				Limited: true, ResetsAt: at(500)},
			&model.UsageWindow{ID: "weekly_sonnet", Label: "Sonnet week", UsedPercent: 100, ResetsAt: at(500), WindowMinutes: 10080}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, hit := usageLimits(&c.info)
			got.UpdatedAt = time.Time{}
			if !reflect.DeepEqual(got, c.want) || !reflect.DeepEqual(hit, c.hit) {
				t.Errorf("got %+v, %+v\nwant %+v, %+v", got, hit, c.want, c.hit)
			}
		})
	}
}
