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
	own     map[int]string   // question → an answer in the user's own words
	// input edits own[inputQ] while its chip, the last, is chosen.
	input   *textinput.Model
	inputQ  int
	denying *model.ApprovalOption
	reason  textinput.Model
	scroll  int
	busy    bool
	// picked: an arrow has chosen an answer. Until then nothing is
	// highlighted and enter answers nothing, so that what's typed as the
	// approval appears can't answer it.
	picked bool
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
	d.own = map[int]string{}
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
	it := d.current()
	question := it != nil && it.Approval != nil && it.Approval.Special == model.SpecialQuestion
	switch {
	case !d.picked:
		return [][2]string{{"←→", "pick an answer"}, {"ctrl+x", "stop"}}
	case question && d.zone == 0 && d.onOwn(it.Approval):
		return [][2]string{{"↑↓", "question"}, {"type", "your own answer"}, {"enter", "done"}, {"ctrl+x", "stop"}}
	case question && d.zone == 0:
		return [][2]string{{"↑↓", "question"}, {"←→ space", "choose"}, {"ctrl+x", "stop"}}
	}
	return [][2]string{{"←→", "pick"}, {"enter", "answer"}, {"ctrl+x", "stop"}}
}

func (d *dock) allAnswered(ap *model.Approval) bool {
	for i := range ap.Questions {
		if len(d.answer(ap, i)) == 0 {
			return false
		}
	}
	return true
}

// answer is a question's labels chosen, then the user's own words.
func (d *dock) answer(ap *model.Approval, qi int) []string {
	labels := slices.Clone(d.answers[qi])
	slices.Sort(labels)
	if text := strings.TrimSpace(d.own[qi]); text != "" && !ap.Questions[qi].OptionsOnly {
		labels = append(labels, text)
	}
	return labels
}

// chips counts a question's choices: its options, then, unless it takes
// only those, the user's own answer.
func chips(q model.Question) int {
	if q.OptionsOnly {
		return len(q.Options)
	}
	return len(q.Options) + 1
}

// onOwn: the user's own answer is the choice under the cursor.
func (d *dock) onOwn(ap *model.Approval) bool {
	if d.q >= len(ap.Questions) {
		return false
	}
	q := ap.Questions[d.q]
	return d.picked && d.zone == 0 && !q.OptionsOnly && d.chip == len(q.Options)
}

// ownInput is the field for question qi's own answer.
func (d *dock) ownInput(m *Model, qi int) *textinput.Model {
	if d.input == nil || d.inputQ != qi {
		in := textinput.New()
		in.Prompt = ""
		in.Placeholder = "type here"
		styleInput(&in, m)
		in.SetValue(d.own[qi])
		in.CursorEnd()
		in.Focus()
		d.input, d.inputQ = &in, qi
	}
	return d.input
}

// typed takes what was typed as question qi's own answer, which replaces
// a single answer's label.
func (d *dock) typed(ap *model.Approval, qi int) {
	d.own[qi] = d.input.Value()
	if !ap.Questions[qi].MultiSelect && strings.TrimSpace(d.own[qi]) != "" {
		d.answers[qi] = nil
	}
}

func (d *dock) enabled(ap *model.Approval, i int) bool {
	if ap.Special == model.SpecialQuestion && ap.Options[i].Kind != model.OptionDeny {
		return d.allAnswered(ap)
	}
	return true
}

func (d *dock) paste(m *Model, text string) tea.Cmd {
	if d.denying != nil {
		d.reason.SetValue(d.reason.Value() + text)
		d.reason.CursorEnd()
	}
	if it := d.current(); it != nil && it.Approval != nil && d.onOwn(it.Approval) {
		in := d.ownInput(m, d.q)
		in.SetValue(in.Value() + text)
		in.CursorEnd()
		d.typed(it.Approval, d.q)
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
		m.toList()
		return nil
	}
	if !d.picked {
		// The first arrow shows the answer it starts on.
		d.picked = key == "up" || key == "down" || key == "left" || key == "right"
		return nil
	}
	if question && d.zone == 0 && d.onOwn(ap) {
		in := d.ownInput(m, d.q)
		switch {
		case key == "up" || key == "down":
		case key == "left" && in.Position() == 0:
		case key == "enter":
			// Done: on to the next question, or the buttons.
			if strings.TrimSpace(d.own[d.q]) != "" {
				d.next(ap, d.q)
			}
			return nil
		default:
			var cmd tea.Cmd
			*in, cmd = in.Update(k)
			d.typed(ap, d.q)
			return cmd
		}
	}
	if question && d.zone == 0 {
		q := ap.Questions[d.q]
		switch key {
		case "up":
			d.q = max(0, d.q-1)
			d.chip = 0
		case "down":
			if d.q < len(ap.Questions)-1 {
				d.q++
				d.chip = 0
			} else {
				d.zone = 1
			}
		case "left":
			d.chip = max(0, d.chip-1)
		case "right":
			d.chip = max(0, min(chips(q)-1, d.chip+1))
		case "space", "enter":
			if d.chip < len(q.Options) {
				d.pick(ap, d.q, d.chip)
			}
		}
		return nil
	}
	switch key {
	case "up":
		if question {
			d.zone = 0
		}
	case "left":
		d.option = max(0, d.option-1)
	case "right", "down":
		d.option = min(len(ap.Options)-1, d.option+1)
	case "enter":
		return d.choose(m, v, it, d.option)
	}
	return nil
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
	// One answer: a label replaces the user's own.
	delete(d.own, qi)
	d.input = nil
	d.next(ap, qi)
}

// next moves on from question qi to the next, or to the buttons.
func (d *dock) next(ap *model.Approval, qi int) {
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
			if a := d.answer(it.Approval, i); len(a) > 0 {
				answers[q.Question] = strings.Join(a, ", ")
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
		style := m.st.Button
		switch {
		case !d.enabled(ap, i):
			style = m.st.Chip.Foreground(m.pal.Overlay)
		case d.picked && i == d.option && (ap.Special != model.SpecialQuestion || d.zone == 1):
			style = m.st.ButtonOn
		case o.Kind == model.OptionDeny:
			style = m.st.ButtonDeny
		}
		buttons = append(buttons, style.Render(o.Label))
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

// ownChip is the last choice, the user's own answer: the field while it's
// chosen, else the text typed.
func (d *dock) ownChip(m *Model, q model.Question, qi int, at bool) string {
	label := "Other"
	if len(q.Options) == 0 {
		label = "Your answer"
	}
	text := d.own[qi]
	st := m.st.Chip
	if strings.TrimSpace(text) != "" {
		st = m.st.ChipOn
	}
	switch {
	case at:
		// The chip's padding spaces the field from its label.
		return st.Underline(true).Bold(true).Render(label+":") + d.ownInput(m, qi).View()
	case strings.TrimSpace(text) != "":
		return st.Render(label + ": " + strings.TrimSpace(text))
	}
	return st.Render(label + "…")
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
		at := d.picked && qi == d.q && d.zone == 0
		style := m.st.Text
		if at {
			style = m.st.AccentBold
		}
		for _, l := range wrap(title, width) {
			lines = append(lines, style.Render(l))
		}
		var chips []string
		for ci, o := range q.Options {
			st := m.st.Chip
			if slices.Contains(d.answers[qi], o.Label) {
				st = m.st.ChipOn
			}
			if at && ci == d.chip {
				st = st.Underline(true).Bold(true)
			}
			chips = append(chips, st.Render(o.Label))
		}
		if !q.OptionsOnly {
			chips = append(chips, d.ownChip(m, q, qi, at && d.chip == len(q.Options)))
		}
		lines = append(lines, wrap(strings.Join(chips, " "), width)...)
		if at && d.chip < len(q.Options) && q.Options[d.chip].Description != "" {
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
