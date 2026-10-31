package tui

import (
	"time"

	"charm.land/lipgloss/v2"

	"remote-agent/internal/client"
	"remote-agent/internal/model"
)

// limitStyle is a usage limit's colour: subtext, yellow from
// client.WarningPercent, red once spent.
func (m *Model) limitStyle(level client.LimitLevel) lipgloss.Style {
	switch level {
	case client.LimitReached:
		return m.st.Red
	case client.LimitWarning:
		return m.st.Yellow
	}
	return m.st.Subtext
}

// limitsGauge is an agent's usage-limits gauge, "5h 34% · week 61%", in
// its colour; "" for an agent that reports none.
func (m *Model) limitsGauge(harness string) string {
	l := m.conn.Limits[harness]
	if l == nil {
		return ""
	}
	now := time.Now()
	g := client.LimitsGauge(l, now)
	if g == "" {
		return ""
	}
	return m.limitStyle(client.LimitsLevel(l, now)).Render(g)
}

// limitsDetails are the lines behind an agent's gauge, which the apps show
// on hover or a tap and ra under the chosen agent, indented by lead.
func (m *Model) limitsDetails(harness, lead string, width int) []string {
	l := m.conn.Limits[harness]
	if l == nil {
		return nil
	}
	var lines []string
	for _, d := range client.LimitsDetails(l, time.Now()) {
		lines = append(lines, lead+m.st.Faint.Render(truncate(d, width-len(lead))))
	}
	return lines
}

// limitNotice is an error whose turn hit a usage limit: which, and when it
// resets, red until it does.
func (m *Model) limitNotice(w model.UsageWindow, width int) []string {
	now := time.Now()
	st := m.st.Red
	if w.ResetsAt != nil && !w.ResetsAt.After(now) {
		st = m.st.Subtext
	}
	lines := wrap(client.LimitNotice(w, now), width-2)
	for i, l := range lines {
		lead := "  "
		if i == 0 {
			lead = "◔ "
		}
		lines[i] = st.Render(lead + l)
	}
	return lines
}
