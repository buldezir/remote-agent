package orchestrator

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"remote-agent/internal/harness"
	"remote-agent/internal/model"
	"remote-agent/internal/store"
)

// Reports: when an agent gives another session work with rad's tools
// (start_session, send_prompt, fork_session), the turn that does it reports
// back when it ends. rad sends the agent's session a prompt of its own, a
// user message with Item.Report, which says how the turn ended and what the
// session replied. It steers into the agent's turn when the agent is
// working and takes one, else waits for that turn, or starts one. An agent
// that waits for the session with wait_for_session hears it there and gets
// no report, and one that stopped the turn gets none either.

type reportKey struct{}

// ReportTo makes the prompts sent with ctx report to the agent of the
// session sessionID when their turns end; "" makes them report to none.
func ReportTo(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, reportKey{}, sessionID)
}

func reportToOf(ctx context.Context) string {
	id, _ := ctx.Value(reportKey{}).(string)
	return id
}

// maxReportReply is how much of the session's last reply a report quotes.
const maxReportReply = 16000

// Waiting records that the agent of parent waits for child with
// wait_for_session, until release: child's turns that end meanwhile report
// to it there, not with a prompt.
func (o *Orchestrator) Waiting(child, parent string) (release func()) {
	if parent == "" {
		return func() {}
	}
	k := [2]string{child, parent}
	o.mu.Lock()
	o.waiters[k]++
	o.mu.Unlock()
	return func() {
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.waiters[k]--; o.waiters[k] <= 0 {
			delete(o.waiters, k)
		}
	}
}

func (o *Orchestrator) waitedOn(child, parent string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.waiters[[2]string{child, parent}] > 0
}

// addReport makes the running turn report to the agent of the session to.
func (a *actor) addReport(to string) {
	if to != "" && to != a.sess.ID && !slices.Contains(a.reportTo, to) {
		a.reportTo = append(a.reportTo, to)
	}
}

// dropReport keeps the running turn from reporting to the agent of the
// session from, which stopped it.
func (a *actor) dropReport(from string) {
	a.reportTo = slices.DeleteFunc(a.reportTo, func(id string) bool { return id == from })
}

// takeReports is who the turn that ends reports to: not an agent waiting
// for it with wait_for_session, which hears it there, and none once the
// session is archived or rad shuts down. The caller holds a.mu, and calls it
// before the session's new status is written, which wakes the waiters.
func (a *actor) takeReports() []string {
	to := a.reportTo
	a.reportTo = nil
	if a.deleted || a.archiving || a.sess.Archived || a.o.bg.Err() != nil {
		return nil
	}
	return slices.DeleteFunc(to, func(parent string) bool { return a.o.waitedOn(a.sess.ID, parent) })
}

// sendReports tells the agents of the sessions in to how the turn turnID of
// child ended, once it's written.
func (o *Orchestrator) sendReports(child model.Session, turnID string, status model.TurnStatus, errText string, to []string) {
	if len(to) == 0 {
		return
	}
	o.reports.Add(1)
	go func() {
		defer o.reports.Done()
		ctx := o.bg
		reply := ""
		if turnID != "" {
			if items, err := o.st.Items(ctx, child.ID); err == nil {
				for _, it := range slices.Backward(items) {
					if it.TurnID == turnID && it.Kind == model.ItemAssistantMessage && it.ParentItemID == "" && strings.TrimSpace(it.Text) != "" {
						reply = it.Text
						break
					}
				}
			}
		}
		rep := &model.Report{SessionID: child.ID, Title: child.Title, Status: status}
		display, input := reportText(child, status, errText, reply)
		for _, parent := range to {
			a, err := o.actor(ctx, parent)
			if err != nil {
				continue // gone
			}
			if err := a.report(ctx, display, input, rep); err != nil {
				o.log.Warn("report failed", "session", parent, "from", child.ID, "err", err)
			}
		}
	}()
}

// reportText is a report as the user reads it, which mentions the session,
// and as the agent does, with the session's last reply.
func reportText(child model.Session, status model.TurnStatus, errText, reply string) (display, input string) {
	title := strings.Map(func(r rune) rune {
		if r == '[' || r == ']' || r == '\n' {
			return ' '
		}
		return r
	}, child.Title)
	how := "finished"
	switch status {
	case model.TurnFailed:
		how = "failed"
		if line := firstLine(errText); line != "" {
			how += ": " + line
		}
	case model.TurnInterrupted:
		how = "was stopped"
	}
	display = fmt.Sprintf("@[%s](session:%s) %s.", title, child.ID, strings.TrimSuffix(how, "."))

	var b strings.Builder
	fmt.Fprintf(&b, "<rad-report>\nThe session “%s” (id %s), which you gave work to, ", child.Title, child.ID)
	switch status {
	case model.TurnFailed:
		b.WriteString("failed")
		if errText != "" {
			b.WriteString(": " + strings.TrimSpace(errText))
		}
		b.WriteString("\n")
	case model.TurnInterrupted:
		b.WriteString("was stopped before it finished.\n")
	default:
		b.WriteString("finished its turn.\n")
	}
	if n := len(child.BackgroundTasks); n > 0 {
		fmt.Fprintf(&b, "It left %d task(s) running in the background.\n", n)
	}
	if reply = strings.TrimSpace(reply); reply == "" {
		b.WriteString("It gave no reply.\n")
	} else {
		if r := []rune(reply); len(r) > maxReportReply {
			reply = string(r[:maxReportReply]) + "\n… (read_session has the rest)"
		}
		b.WriteString("Its last reply:\n\n" + reply + "\n")
	}
	b.WriteString("</rad-report>\nThis message is from rad, not the user. Read the session with read_session for more, or give it more work with send_prompt.")
	return display, b.String()
}

// report sends the session's agent a report: as a prompt, which starts a
// turn, or steers into the running one, or waits for it.
func (a *actor) report(ctx context.Context, display, input string, rep *model.Report) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Shutdown stops the background context before it detaches the actors,
	// which wait for a.mu: nothing opens an agent after.
	if a.deleted || a.archiving || a.sess.Archived || a.o.bg.Err() != nil {
		return nil
	}
	it := a.newItem(model.ItemUserMessage, model.ItemQueued)
	it.TurnID = "" // set when its turn starts
	it.Text, it.Report = display, rep
	if err := a.write(ctx, func(w *store.W) error { return a.putItem(w, it) }); err != nil {
		return err
	}
	a.lastActive = time.Now()
	busy := a.turn != nil || a.holds > 0
	a.o.log.Info("report", "session", a.sess.ID, "from", rep.SessionID, "status", rep.Status, "queued", busy)
	q := queuedPrompt{item: it, input: harness.Input{Text: input}, queuedAt: time.Now()}
	if !busy {
		return a.startTurn(ctx, q)
	}
	a.queue = append(a.queue, q)
	if _, can := a.rt.(harness.Steerer); can {
		if _, err := a.steerLocked(ctx, len(a.queue)-1); err != nil {
			a.o.log.Warn("steering a report failed; it's queued", "session", a.sess.ID, "err", err)
		}
	}
	return nil
}
