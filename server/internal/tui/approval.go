package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"remote-agent/internal/model"
)

// dock is where the agent's questions are answered, in the composer's
// place while one waits: an approval's options, a plan, or a question's
// choices, as the apps' approval cards.
type dock struct {
	items   []*model.Item
	id      string
	option  int
	zone    int // questions: 0 the choices, 1 the buttons
	q, chip int
	answers map[int][]string // question → labels chosen
	denying *model.ApprovalOption
	reason  textinput.Model
	scroll  int
	busy    bool
}

type respondedMsg struct {
	d   *dock
	err error
}

func (d *dock) active() bool { return len(d.items) > 0 }

func (d *dock) current() *model.Item {
	for _, it := range d.items {
		if it.ID == d.id {
			return it
		}
	}
	return nil
}

// sync follows the approvals that wait, keeping the one shown while it does.
func (d *dock) sync(m *Model, pending []*model.Item) {
	d.items = pending
	if d.current() != nil {
		return
	}
	*d = dock{items: pending}
	if len(pending) == 0 {
		return
	}
	it := pending[0]
	d.id = it.ID
	d.answers = map[int][]string{}
	if it.Approval != nil {
		// The first option that allows, not deny, is chosen to begin with.
		for i, o := range it.Approval.Options {
			if o.Kind != model.OptionDeny {
				d.option = i
				break
			}
		}
	}
}

func (d *dock) hints() [][2]string {
	if d.denying != nil {
		return [][2]string{{"enter", "deny"}, {"esc", "back"}}
	}
	if it := d.current(); it != nil && it.Approval != nil && it.Approval.Special == model.SpecialQuestion {
		return [][2]string{{"↑↓", "question"}, {"←→ space", "choose"}, {"enter", "answer"}}
	}
	return [][2]string{{"1–9", "answer"}, {"←→ enter", "choose"}, {"esc", "stop"}}
}

func (d *dock) allAnswered(ap *model.Approval) bool {
	for i := range ap.Questions {
		if len(d.answers[i]) == 0 {
			return false
		}
	}
	return true
}

func (d *dock) enabled(ap *model.Approval, i int) bool {
	if ap.Special == model.SpecialQuestion && ap.Options[i].Kind != model.OptionDeny {
		return d.allAnswered(ap)
	}
	return true
}

func (d *dock) paste(text string) tea.Cmd {
	if d.denying != nil {
		d.reason.SetValue(d.reason.Value() + text)
		d.reason.CursorEnd()
	}
	return nil
}

func (d *dock) key(m *Model, v *sessionView, k tea.KeyPressMsg) tea.Cmd {
	it := d.current()
	if it == nil || it.Approval == nil || d.busy {
		return nil
	}
	ap := it.Approval
	if d.denying != nil {
		switch k.String() {
		case "esc":
			d.denying = nil
			return nil
		case "enter":
			o := *d.denying
			d.denying = nil
			return d.respond(m, v, it, o, strings.TrimSpace(d.reason.Value()))
		}
		var cmd tea.Cmd
		d.reason, cmd = d.reason.Update(k)
		return cmd
	}
	question := ap.Special == model.SpecialQuestion && len(ap.Questions) > 0
	key := k.String()
	switch key {
	case "pgup":
		d.scroll = max(0, d.scroll-5)
		return nil
	case "pgdown":
		d.scroll += 5
		return nil
	case "esc":
		return v.interrupt(m, false)
	}
	if question && d.zone == 0 {
		q := ap.Questions[d.q]
		switch key {
		case "up", "k":
			d.q = max(0, d.q-1)
			d.chip = 0
		case "down", "j":
			if d.q < len(ap.Questions)-1 {
				d.q++
				d.chip = 0
			} else {
				d.zone = 1
			}
		case "left", "h":
			d.chip = max(0, d.chip-1)
		case "right", "l":
			d.chip = min(len(q.Options)-1, d.chip+1)
		case "space", "enter":
			d.pick(ap, d.q, d.chip)
		default:
			if n, ok := digit(key); ok && n < len(q.Options) {
				d.chip = n
				d.pick(ap, d.q, n)
			}
		}
		return nil
	}
	switch key {
	case "up", "k":
		if question {
			d.zone = 0
		}
	case "left", "h", "shift+tab":
		d.option = max(0, d.option-1)
	case "right", "l", "down", "j":
		d.option = min(len(ap.Options)-1, d.option+1)
	case "enter", "space":
		return d.choose(m, v, it, d.option)
	default:
		if n, ok := digit(key); ok && n < len(ap.Options) {
			d.option = n
			return d.choose(m, v, it, n)
		}
	}
	return nil
}

func digit(key string) (int, bool) {
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		return int(key[0] - '1'), true
	}
	return 0, false
}

// pick chooses a question's answer: one replaces the last, or with
// multiSelect, it toggles. A single choice moves on to the next question.
func (d *dock) pick(ap *model.Approval, qi, ci int) {
	q := ap.Questions[qi]
	label := q.Options[ci].Label
	if q.MultiSelect {
		if i := slices.Index(d.answers[qi], label); i >= 0 {
			d.answers[qi] = slices.Delete(d.answers[qi], i, i+1)
		} else {
			d.answers[qi] = append(d.answers[qi], label)
		}
		return
	}
	d.answers[qi] = []string{label}
	if qi < len(ap.Questions)-1 {
		d.q, d.chip = qi+1, 0
	} else {
		d.zone = 1
	}
}

func (d *dock) choose(m *Model, v *sessionView, it *model.Item, i int) tea.Cmd {
	ap := it.Approval
	if !d.enabled(ap, i) {
		m.say("Answer every question first.")
		return nil
	}
	o := ap.Options[i]
	if o.Kind == model.OptionDeny && ap.Special != model.SpecialQuestion {
		d.denying = &o
		d.reason = textinput.New()
		d.reason.Placeholder = "Reason (optional)"
		d.reason.Prompt = "› "
		styleInput(&d.reason, m)
		d.reason.Focus()
		return nil
	}
	return d.respond(m, v, it, o, "")
}

func (d *dock) respond(m *Model, v *sessionView, it *model.Item, o model.ApprovalOption, message string) tea.Cmd {
	var answers map[string]string
	if it.Approval.Special == model.SpecialQuestion {
		answers = map[string]string{}
		for i, q := range it.Approval.Questions {
			labels := slices.Clone(d.answers[i])
			slices.Sort(labels)
			if len(labels) > 0 {
				answers[q.Question] = strings.Join(labels, ", ")
			}
		}
	}
	d.busy = true
	id, store := it.ID, v.store
	return func() tea.Msg {
		return respondedMsg{d: d, err: store.Respond(context.Background(), id, o.ID, message, answers)}
	}
}

func (d *dock) view(m *Model, v *sessionView, w, maxH int) []string {
	it := d.current()
	if it == nil || it.Approval == nil {
		return nil
	}
	ap := it.Approval
	inner := w - 2
	head := "Needs your answer"
	switch ap.Special {
	case model.SpecialQuestion:
		head = "A question for you"
	case model.SpecialPlan:
		head = "A plan to approve"
	}
	if len(d.items) > 1 {
		head += fmt.Sprintf(" · 1 of %d", len(d.items))
	}
	lines := []string{m.st.Yellow.Render("──◆ "+head+" ") + m.st.Yellow.Render(strings.Repeat("─", max(0, w-len(head)-5)))}
	add := func(l string) { lines = append(lines, " "+l) }
	for _, l := range wrap(ap.Title, inner) {
		add(m.st.Bold.Render(l))
	}
	if ap.Detail != "" {
		for _, l := range wrap(ap.Detail, inner) {
			add(m.st.Subtext.Render(l))
		}
	}
	var body []string
	switch {
	case ap.Special == model.SpecialPlan && ap.PlanText != "":
		body = renderMarkdownLines(m, ap.PlanText, inner)
	case ap.Special == model.SpecialQuestion:
		body = d.questionLines(m, ap, inner)
	case len(ap.Input) > 0:
		tool := &model.ToolCall{Name: ap.ToolName, Input: ap.Input}
		body = m.toolInput(tool, inner, false)
	}
	// What doesn't fit scrolls, with pgup and pgdn.
	room := max(3, maxH-len(lines)-4)
	if len(body) > room {
		d.scroll = min(d.scroll, len(body)-room)
		more := len(body) - room - d.scroll
		body = body[d.scroll : d.scroll+room]
		if more > 0 {
			body = append(body, m.st.Faint.Render(fmt.Sprintf("… %d more lines: pgdn", more)))
		}
	} else {
		d.scroll = 0
	}
	for _, l := range body {
		add(l)
	}
	if d.denying != nil {
		d.reason.SetWidth(inner - 4)
		add("")
		add(m.st.Red.Render(d.denying.Label) + m.st.Faint.Render(" · say why, or just press enter"))
		add(d.reason.View())
		return lines
	}
	var buttons []string
	for i, o := range ap.Options {
		label := fmt.Sprintf("%d %s", i+1, o.Label)
		style := m.st.Button
		switch {
		case !d.enabled(ap, i):
			style = m.st.Chip.Foreground(m.pal.Overlay)
		case i == d.option && (ap.Special != model.SpecialQuestion || d.zone == 1):
			style = m.st.ButtonOn
		case o.Kind == model.OptionDeny:
			style = m.st.ButtonDeny
		}
		buttons = append(buttons, style.Render(label))
	}
	add("")
	row := strings.Join(buttons, " ")
	if d.busy {
		row += " " + m.st.Accent.Render(m.spinner())
	}
	for _, l := range wrap(row, inner) {
		add(l)
	}
	return lines
}

func (d *dock) questionLines(m *Model, ap *model.Approval, width int) []string {
	var lines []string
	for qi, q := range ap.Questions {
		if qi > 0 {
			lines = append(lines, "")
		}
		title := q.Question
		if q.Header != "" {
			title = q.Header + ": " + q.Question
		}
		style := m.st.Text
		if qi == d.q && d.zone == 0 {
			style = m.st.AccentBold
		}
		for _, l := range wrap(title, width) {
			lines = append(lines, style.Render(l))
		}
		var chips []string
		for ci, o := range q.Options {
			label := fmt.Sprintf("%d %s", ci+1, o.Label)
			st := m.st.Chip
			if slices.Contains(d.answers[qi], o.Label) {
				st = m.st.ChipOn
			}
			if qi == d.q && ci == d.chip && d.zone == 0 {
				st = st.Underline(true).Bold(true)
			}
			chips = append(chips, st.Render(label))
		}
		lines = append(lines, wrap(strings.Join(chips, " "), width)...)
		if qi == d.q && d.zone == 0 && d.chip < len(q.Options) && q.Options[d.chip].Description != "" {
			for _, l := range wrap(q.Options[d.chip].Description, width) {
				lines = append(lines, m.st.Faint.Render(l))
			}
		}
		if q.MultiSelect {
			lines = append(lines, m.st.Faint.Render("Choose any."))
		}
	}
	return lines
}
