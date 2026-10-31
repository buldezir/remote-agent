package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"remote-agent/internal/model"
)

// The vectors every client's usage-limits formatting shares (RAKit,
// web/src/store, android/core).
type limitVectors struct {
	Now   time.Time `json:"now"`
	Cases []struct {
		Name           string            `json:"name"`
		Limits         model.UsageLimits `json:"limits"`
		Gauge          string            `json:"gauge"`
		Level          LimitLevel        `json:"level"`
		WindowLevels   []LimitLevel      `json:"windowLevels"`
		WindowPercents []int             `json:"windowPercents"`
		Details        []string          `json:"details"`
	} `json:"cases"`
	Notices []struct {
		Window model.UsageWindow `json:"window"`
		Text   string            `json:"text"`
	} `json:"notices"`
	Durations []struct {
		Seconds int64  `json:"seconds"`
		Future  string `json:"future"`
		Past    string `json:"past"`
	} `json:"durations"`
}

func TestUsageLimitsVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "protocol", "vectors", "usage_limits.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v limitVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Cases {
		t.Run(c.Name, func(t *testing.T) {
			l := &c.Limits
			if got := LimitsGauge(l, v.Now); got != c.Gauge {
				t.Errorf("gauge = %q, want %q", got, c.Gauge)
			}
			if got := LimitsLevel(l, v.Now); got != c.Level {
				t.Errorf("level = %q, want %q", got, c.Level)
			}
			levels, percents := []LimitLevel{}, []int{}
			for _, w := range l.Windows {
				levels, percents = append(levels, WindowLevel(w, v.Now)), append(percents, WindowPercent(w, v.Now))
			}
			if !slices.Equal(levels, c.WindowLevels) || !slices.Equal(percents, c.WindowPercents) {
				t.Errorf("windows = %v %v, want %v %v", levels, percents, c.WindowLevels, c.WindowPercents)
			}
			if got := LimitsDetails(l, v.Now); !slices.Equal(got, c.Details) {
				t.Errorf("details = %q, want %q", got, c.Details)
			}
		})
	}
	for _, n := range v.Notices {
		if got := LimitNotice(n.Window, v.Now); got != n.Text {
			t.Errorf("notice = %q, want %q", got, n.Text)
		}
	}
	for _, d := range v.Durations {
		span := time.Duration(d.Seconds) * time.Second
		if f, p := LimitDuration(span, true), LimitDuration(span, false); f != d.Future || p != d.Past {
			t.Errorf("%ds = %q, %q, want %q, %q", d.Seconds, f, p, d.Future, d.Past)
		}
	}
}
