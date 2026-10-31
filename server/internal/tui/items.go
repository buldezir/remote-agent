package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"remote-agent/internal/model"
)

// How each item of a transcript is drawn, as ItemView does in the apps.

// Expansion of an item: shut, open, or open with long output in full.
const (
	shut = iota
	open
	full
)

func (m *Model) toolGlyph(k model.ToolKind) string {
	switch k {
	case model.ToolRead:
		return "▤"
	case model.ToolEdit:
		return "✎"
	case model.ToolExecute:
		return "❯"
	case model.ToolSearch:
		return "⌕"
	case model.ToolFetch:
		return "↓"
	case model.ToolThink:
		return "◎"
	}
	return "◇"
}

func (m *Model) itemStatus(it *model.Item) string {
	switch it.Status {
	case model.ItemInProgress, model.ItemQueued:
		return m.st.Accent.Render(m.spinner())
	case model.ItemPending:
		return m.st.Yellow.Render("◆")
	case model.ItemFailed:
		return m.st.Red.Render("✗")
	case model.ItemCompleted:
		return m.st.Green.Render("✓")
	}
	return ""
}

// renderItem draws an item and, under a tool call, its children.
func (m *Model) renderItem(v *sessionView, it *model.Item, width int) []string {
	switch it.Kind {
	case model.ItemUserMessage:
		return m.renderPrompt(it.Text, it.Images, width, it.Status, v.canSteer(m, it))
	case model.ItemAssistantMessage:
		return m.renderMarkdownWithImages(it.Text, it.Images, width)
	case model.ItemReasoning:
		label := "Thought"
		if it.Status == model.ItemInProgress {
			label = m.spinner() + " Thinking…"
		}
		lines := []string{m.st.Faint.Render("∴ " + label)}
		if v.expansion(it.ID) != shut && strings.TrimSpace(it.Text) != "" {
			for _, l := range wrap(strings.TrimSpace(it.Text), width-2) {
				lines = append(lines, "  "+m.st.Faint.Render(l))
			}
		}
		return lines
	case model.ItemToolCall:
		return m.renderTool(v, it, width)
	case model.ItemApproval:
		return m.renderApproval(v, it, width)
	case model.ItemPlan:
		return m.renderPlan(it.Plan, width)
	case model.ItemNotice:
		var lines []string
		for _, l := range wrap(strings.TrimSpace(it.Text), width-2) {
			lines = append(lines, m.st.Faint.Render("· "+l))
		}
		return lines
	case model.ItemError:
		lines := wrap(strings.TrimSpace(it.Text), width-2)
		for i, l := range lines {
			lead := "  "
			if i == 0 {
				lead = "✗ "
			}
			lines[i] = m.st.Red.Render(lead + l)
		}
		return lines
	}
	if it.Text != "" {
		return wrap(it.Text, width)
	}
	return nil
}

// renderPrompt draws what the user sent: the text beside a bar, then its
// images. A queued one says how to send it now, steer it into the turn
// where the agent can take it, or cancel it.
func (m *Model) renderPrompt(text string, images []model.ImageRef, width int, status model.ItemStatus, steer bool) []string {
	bar, style := m.st.Accent, m.st.Text
	if status == model.ItemQueued || status == model.ItemCancelled {
		bar, style = m.st.Faint, m.st.Faint
	}
	var lines []string
	for _, l := range wrap(strings.TrimRight(text, "\n"), width-2) {
		lines = append(lines, bar.Render("▎ ")+style.Render(l))
	}
	for _, im := range images {
		for _, l := range m.images.thumb(m, im, min(40, width-2), 12) {
			lines = append(lines, bar.Render("▎ ")+l)
		}
	}
	switch status {
	case model.ItemQueued:
		line := m.st.Faint.Render("  Queued · ") + m.st.Key.Render("s") + m.st.Faint.Render(" sends it now · ")
		if steer {
			line += m.st.Key.Render("t") + m.st.Faint.Render(" steers it in · ")
		}
		lines = append(lines, line+m.st.Key.Render("x")+m.st.Faint.Render(" cancels it"))
	case model.ItemCancelled:
		lines = append(lines, m.st.Faint.Render("  Not sent"))
	}
	return lines
}

// renderMarkdownWithImages draws a reply, its images between its texts.
func (m *Model) renderMarkdownWithImages(text string, images []model.ImageRef, width int) []string {
	refs := map[string]model.ImageRef{}
	for _, im := range images {
		refs[im.ID] = im
	}
	var lines []string
	for i, b := range renderMarkdown(text, width, m.pal) {
		if i > 0 {
			lines = append(lines, "")
		}
		if b.imageID != "" {
			ref, ok := refs[b.imageID]
			if !ok {
				ref = model.ImageRef{ID: b.imageID}
			}
			lines = append(lines, m.images.thumb(m, ref, min(48, width), 16)...)
			if b.alt != "" {
				lines = append(lines, m.st.Faint.Render(truncate(b.alt, width)))
			}
			continue
		}
		lines = append(lines, b.lines...)
	}
	return lines
}

func (m *Model) renderTool(v *sessionView, it *model.Item, width int) []string {
	tool := it.Tool
	if tool == nil {
		tool = &model.ToolCall{Name: "Tool"}
	}
	title := tool.Title
	if title == "" {
		title = tool.Name
	}
	if title == "" {
		title = "Tool"
	}
	children := v.store.ChildrenOf(it)
	state := v.expansion(it.ID)
	glyph := m.st.Subtext.Render(m.toolGlyph(tool.Kind) + " ")
	status := m.itemStatus(it)
	titleLines := wrap(firstLine(title), width-6)
	if len(titleLines) > 2 {
		titleLines = append(titleLines[:1], truncate(titleLines[1]+"…", width-6))
	}
	var lines []string
	for i, l := range titleLines {
		line := "  " + m.st.Text.Render(l)
		if i == 0 {
			line = glyph + m.st.Text.Render(l)
			if status != "" {
				line += " " + status
			}
		}
		lines = append(lines, line)
	}
	if state == shut && len(children) > 0 {
		lines = append(lines, m.st.Faint.Render(fmt.Sprintf("  %d sub-agent steps", len(children))))
	}
	// Images show even when shut.
	for _, im := range it.Images {
		lines = append(lines, indent(m.images.thumb(m, im, min(40, width-2), 10), "  ")...)
	}
	if state == shut {
		return lines
	}
	inner := width - 4
	gutter := m.st.Rule.Render("  │ ")
	var body []string
	body = append(body, m.toolInput(tool, inner, state == full)...)
	if out := strings.TrimRight(tool.Output, "\n"); out != "" {
		label := "Output"
		if tool.ExitCode != nil {
			label = fmt.Sprintf("Output (exit %d)", *tool.ExitCode)
		}
		body = append(body, m.st.Header.Render(label))
		body = append(body, m.codeLines(out, inner, state == full)...)
	}
	for _, l := range body {
		lines = append(lines, gutter+l)
	}
	for _, c := range children {
		for _, l := range m.renderItem(v, c, width-4) {
			lines = append(lines, gutter+l)
		}
	}
	return lines
}

// codeLines draws code or output, wrapped, at most 30 lines unless all.
func (m *Model) codeLines(text string, width int, all bool) []string {
	lines := wrap(text, width)
	if !all && len(lines) > 30 {
		more := len(lines) - 30
		lines = append(lines[:30:30], m.st.Faint.Render(fmt.Sprintf("… %d more lines: enter shows all", more)))
		for i := range lines[:30] {
			lines[i] = m.st.Subtext.Render(lines[i])
		}
		return lines
	}
	for i := range lines {
		lines[i] = m.st.Subtext.Render(lines[i])
	}
	return lines
}

// toolInput previews what a tool was called with: a command, a change, a
// file's new content, or its JSON.
func (m *Model) toolInput(tool *model.ToolCall, width int, all bool) []string {
	if len(tool.Input) == 0 {
		return nil
	}
	var in map[string]any
	if json.Unmarshal(tool.Input, &in) != nil {
		return m.codeLines(string(tool.Input), width, all)
	}
	var lines []string
	switch cmd := in["command"].(type) {
	case string:
		lines = append(lines, m.st.Header.Render("Command"))
		for _, l := range wrap("$ "+cmd, width) {
			lines = append(lines, m.st.Peach.Render(l))
		}
		return lines
	case []any:
		var parts []string
		for _, p := range cmd {
			parts = append(parts, fmt.Sprint(p))
		}
		lines = append(lines, m.st.Header.Render("Command"))
		for _, l := range wrap("$ "+strings.Join(parts, " "), width) {
			lines = append(lines, m.st.Peach.Render(l))
		}
		return lines
	}
	type change struct{ old, new string }
	var changes []change
	if o, ok := in["old_string"].(string); ok {
		n, _ := in["new_string"].(string)
		changes = append(changes, change{o, n})
	}
	if edits, ok := in["edits"].([]any); ok {
		for _, e := range edits {
			if em, ok := e.(map[string]any); ok {
				o, _ := em["old_string"].(string)
				n, _ := em["new_string"].(string)
				changes = append(changes, change{o, n})
			}
		}
	}
	if len(changes) > 0 {
		lines = append(lines, m.st.Header.Render("Change"))
		var diff []string
		for _, c := range changes {
			for _, l := range strings.Split(strings.TrimRight(c.old, "\n"), "\n") {
				if c.old != "" {
					diff = append(diff, m.st.Del.Render(truncate("- "+l, width)))
				}
			}
			for _, l := range strings.Split(strings.TrimRight(c.new, "\n"), "\n") {
				if c.new != "" {
					diff = append(diff, m.st.Add.Render(truncate("+ "+l, width)))
				}
			}
		}
		if !all && len(diff) > 60 {
			diff = append(diff[:60:60], m.st.Faint.Render(fmt.Sprintf("… %d more lines: enter shows all", len(diff)-60)))
		}
		return append(lines, diff...)
	}
	if content, ok := in["content"].(string); ok {
		lines = append(lines, m.st.Header.Render("New content"))
		return append(lines, m.codeLines(strings.TrimRight(content, "\n"), width, all)...)
	}
	var b bytes.Buffer
	if json.Indent(&b, tool.Input, "", "  ") != nil {
		b.Reset()
		b.Write(tool.Input)
	}
	lines = append(lines, m.st.Header.Render("Input"))
	return append(lines, m.codeLines(b.String(), width, all)...)
}

func (m *Model) renderApproval(v *sessionView, it *model.Item, width int) []string {
	ap := it.Approval
	if ap == nil {
		return nil
	}
	title := ap.Title
	switch it.Status {
	case model.ItemPending:
		lines := []string{m.st.Yellow.Render("◆ ") + m.st.Bold.Render(truncate(title, width-2))}
		if ap.Detail != "" {
			for _, l := range wrap(ap.Detail, width-2) {
				lines = append(lines, "  "+m.st.Subtext.Render(l))
			}
		}
		return append(lines, m.st.Faint.Render("  Waiting for your answer below"))
	case model.ItemExpired:
		return []string{m.st.Faint.Render(truncate("Expired: "+title, width))}
	case model.ItemCancelled:
		return []string{m.st.Faint.Render(truncate("Cancelled: "+title, width))}
	}
	var lines []string
	if d := ap.Decision; d != nil {
		label, kind := d.OptionID, model.OptionAllowOnce
		for _, o := range ap.Options {
			if o.ID == d.OptionID {
				label, kind = o.Label, o.Kind
			}
		}
		if kind == model.OptionDeny {
			lines = append(lines, m.st.Red.Render(truncate("✗ "+label+": "+title, width)))
		} else {
			lines = append(lines, m.st.Green.Render(truncate("✓ "+label+": "+title, width)))
		}
		qs := make([]string, 0, len(d.Answers))
		for q := range d.Answers {
			qs = append(qs, q)
		}
		slices.Sort(qs)
		for _, q := range qs {
			for _, l := range wrap(q+" → "+d.Answers[q], width-2) {
				lines = append(lines, "  "+m.st.Subtext.Render(l))
			}
		}
		if d.Message != "" {
			for _, l := range wrap("“"+d.Message+"”", width-2) {
				lines = append(lines, "  "+m.st.Subtext.Render(l))
			}
		}
	} else {
		lines = append(lines, m.st.Green.Render(truncate("✓ "+title, width)))
	}
	if ap.Special == model.SpecialPlan && ap.PlanText != "" && v.expansion(it.ID) != shut {
		lines = append(lines, m.renderMarkdownWithImages(ap.PlanText, nil, width-2)...)
	}
	return lines
}

func (m *Model) renderPlan(p *model.Plan, width int) []string {
	lines := []string{m.st.AccentBold.Render("Plan")}
	if p == nil {
		return lines
	}
	if strings.TrimSpace(p.Text) != "" {
		lines = append(lines, renderMarkdownLines(m, p.Text, width)...)
	}
	for _, e := range p.Entries {
		mark, style := "• ", m.st.Text
		switch e.Status {
		case "completed":
			mark, style = "✓ ", m.st.Green
		case "in_progress":
			mark, style = "◌ ", m.st.Accent
		}
		for i, l := range wrap(e.Content, width-2) {
			lead := "  "
			if i == 0 {
				lead = mark
			}
			lines = append(lines, style.Render(lead)+m.st.Text.Render(l))
		}
	}
	return lines
}

func renderMarkdownLines(m *Model, text string, width int) []string {
	var lines []string
	for _, b := range renderMarkdown(text, width, m.pal) {
		lines = append(lines, b.lines...)
	}
	return lines
}

// contextGauge is how full the agent's context is, as the apps' ring.
func (m *Model) contextGauge(s *model.Session) string {
	if s.Context == nil || s.Context.Window <= 0 {
		return ""
	}
	pct := int(s.Context.Used * 100 / s.Context.Window)
	style := m.st.Faint
	switch {
	case pct >= 90:
		style = m.st.Red
	case pct >= 70:
		style = m.st.Yellow
	}
	marks := []string{"○", "◔", "◑", "◕", "●"}
	return style.Render(fmt.Sprintf("%s %d%%", marks[min(4, pct*5/100)], pct))
}

// itemText is what y copies of an item.
func itemText(it *model.Item) string {
	switch {
	case it.Kind == model.ItemToolCall && it.Tool != nil:
		var parts []string
		if it.Tool.Title != "" {
			parts = append(parts, it.Tool.Title)
		}
		if len(it.Tool.Input) > 0 {
			parts = append(parts, string(it.Tool.Input))
		}
		if it.Tool.Output != "" {
			parts = append(parts, it.Tool.Output)
		}
		return strings.Join(parts, "\n\n")
	case it.Kind == model.ItemApproval && it.Approval != nil:
		if it.Approval.PlanText != "" {
			return it.Approval.PlanText
		}
		return it.Approval.Title
	case it.Kind == model.ItemPlan && it.Plan != nil:
		var b strings.Builder
		b.WriteString(it.Plan.Text)
		for _, e := range it.Plan.Entries {
			fmt.Fprintf(&b, "\n- %s", e.Content)
		}
		return strings.TrimSpace(b.String())
	}
	return it.Text
}

// harnessName names a session's agent.
func (m *Model) harnessName(s *model.Session) string {
	if h := m.conn.Harness(s.Harness); h != nil && h.Name != "" {
		return h.Name
	}
	return s.Harness
}

func (m *Model) modeName(s *model.Session) string {
	if s.Mode == "" {
		return ""
	}
	if h := m.conn.Harness(s.Harness); h != nil {
		for _, c := range h.Modes {
			if c.ID == s.Mode {
				return c.Name
			}
		}
	}
	return s.Mode
}
