package orchestrator

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"remote-agent/internal/gitx"
	"remote-agent/internal/harness"
	"remote-agent/internal/model"
	"remote-agent/internal/store"
)

// A fork is a new session that continues another from one of its turns: it
// starts with that session's transcript up to and including the turn, and,
// in a worktree, with the files as they were after it. The same agent, where
// it can, copies its own conversation (harness.Forker) when the fork first
// opens it; any other agent gets the conversation as text with the fork's
// first prompt (handoff).

type ForkParams struct {
	CommandID string `json:"commandId"`
	SessionID string `json:"sessionId"`
	Turn      int    `json:"turn,omitempty"` // 0: the last turn that ended
	// Harness defaults to the session's, and Mode too with its harness, or
	// else to the harness's default. Model and Effort default to the
	// harness's: clients start them at the session's, and send them.
	Harness string `json:"harness,omitempty"`
	Model   string `json:"model,omitempty"`
	Effort  string `json:"effort,omitempty"`
	Mode    string `json:"mode,omitempty"`
	// Workspace defaults to the session's kind. A worktree starts with the
	// files as they were after the turn; the project folder is as it is.
	Workspace *WorkspaceParams `json:"workspace,omitempty"`
	Prompt    string           `json:"prompt,omitempty"`
	Images    []string         `json:"images,omitempty"`
	Title     string           `json:"title,omitempty"`
	Draft     bool             `json:"draft,omitempty"`
	// StartedBy is the session whose agent made the fork with rad's tools.
	StartedBy string `json:"-"`
}

const forkSuffix = " (fork)"

// Fork makes a session that continues p.SessionID after turn p.Turn.
func (o *Orchestrator) Fork(ctx context.Context, p ForkParams) (*model.Session, error) {
	defer o.lockCommand(p.CommandID)()
	if res, ok, err := o.receipt(ctx, p.CommandID); err != nil {
		return nil, err
	} else if ok {
		var s model.Session
		return &s, json.Unmarshal(res, &s)
	}

	src, err := o.st.Session(ctx, p.SessionID)
	if err != nil {
		return nil, errorf(CodeNotFound, "session %s not found", p.SessionID)
	}
	proj, err := o.st.Project(ctx, src.ProjectID)
	if err != nil {
		return nil, errorf(CodeNotFound, "project %s not found", src.ProjectID)
	}
	turns, err := o.st.Turns(ctx, src.ID)
	if err != nil {
		return nil, err
	}
	at, err := forkTurn(turns, p.Turn)
	if err != nil {
		return nil, err
	}

	harnessID := p.Harness
	if harnessID == "" {
		harnessID = src.Harness
	}
	h, ok := o.reg.Get(harnessID)
	if !ok {
		return nil, errorf(CodeInvalid, "unknown harness %q", harnessID)
	}
	info := o.reg.Info(ctx, h, false)
	if !info.Installed {
		return nil, errorf(CodeUnavailable, "%s is not installed on this machine", info.Name)
	}
	m, effort, mode := p.Model, p.Effort, p.Mode
	if mode == "" && harnessID == src.Harness {
		mode = src.Mode
	}
	if mode == "" {
		mode = info.DefaultMode
	}
	hasPrompt := strings.TrimSpace(p.Prompt) != "" || len(p.Images) > 0
	var draft *model.Draft
	if p.Draft && hasPrompt {
		refs, _, err := o.images(p.Images)
		if err != nil {
			return nil, err
		}
		draft = &model.Draft{Text: p.Prompt, Images: refs}
	}

	s := &model.Session{
		ID: store.NewID(), ProjectID: proj.ID, Harness: harnessID, Model: m, Effort: effort, Mode: mode,
		Workspace: model.Workspace{Kind: model.WorkspaceRoot, Path: proj.Path},
		Status:    model.SessionIdle, Title: p.Title, Draft: draft, CreatedAt: time.Now().UTC(),
		ForkedFrom: &model.ForkedFrom{SessionID: src.ID, Turn: at.N},
	}
	if s.Title == "" {
		s.Title = strings.TrimSuffix(src.Title, forkSuffix) + forkSuffix
	}
	kind := src.Workspace.Kind
	if p.Workspace != nil && p.Workspace.Kind != "" {
		kind = p.Workspace.Kind
	}
	branch := ""
	if p.Workspace != nil {
		branch = p.Workspace.Branch
	}
	named := branch != ""
	if kind == model.WorkspaceWorktree {
		if !proj.IsGitRepo {
			return nil, errorf(CodeInvalid, "worktrees need a git repository")
		}
		if branch == "" {
			branch = provisionalBranch(strings.TrimSuffix(s.Title, forkSuffix), s.ID)
		}
		dir := filepath.Join(o.opt.WorktreesDir, proj.ID, s.ID)
		if err := forkWorktree(ctx, proj.Path, dir, branch, at.CheckpointAfter); err != nil {
			return nil, err
		}
		s.Workspace = model.Workspace{Kind: model.WorkspaceWorktree, Path: dir, Branch: branch}
	} else if proj.IsGitRepo {
		s.Workspace.Branch = gitx.CurrentBranch(ctx, proj.Path)
	}

	items, err := o.st.Items(ctx, src.ID)
	if err != nil {
		return nil, err
	}
	copied, copiedTurns := copyTranscript(s.ID, items, turns, at.N, harnessID == src.Harness)
	notice := &model.Item{ID: store.NewID(), SessionID: s.ID, Order: int64(len(copied)) + 1, Kind: model.ItemNotice,
		Status: model.ItemCompleted, Text: fmt.Sprintf("Forked from “%s” after turn %d.", src.Title, at.N)}
	s.ItemCount = notice.Order
	if proj.IsGitRepo {
		for _, t := range copiedTurns {
			for phase, sha := range map[string]string{"before": t.CheckpointBefore, "after": t.CheckpointAfter} {
				if sha == "" {
					continue
				}
				if err := gitx.SetRef(ctx, proj.Path, checkpointRef(s.ID, t.N, phase), sha); err != nil {
					o.log.Warn("copying a checkpoint into the fork failed", "session", s.ID, "turn", t.N, "err", err)
				}
			}
		}
	}

	_, err = o.st.Write(ctx, func(w *store.W) error {
		if err := w.PutSession(s); err != nil {
			return err
		}
		for _, t := range copiedTurns {
			if err := w.PutTurn(t); err != nil {
				return err
			}
		}
		for _, it := range append(copied, notice) {
			if err := w.PutItem(it); err != nil {
				return err
			}
		}
		if err := w.SetItemCount(s.ID, s.ItemCount); err != nil {
			return err
		}
		return w.PutReceipt(p.CommandID, s)
	})
	if err != nil {
		return nil, err
	}
	if p.StartedBy != "" {
		o.mu.Lock()
		o.parents[s.ID] = p.StartedBy
		o.mu.Unlock()
	}
	o.log.Info("session forked", "session", s.ID, "from", src.ID, "turn", at.N, "harness", harnessID, "items", len(copied),
		"worktree", s.Workspace.Kind == model.WorkspaceWorktree, "device", clientOf(ctx))
	if hasPrompt && draft == nil {
		cmd := ""
		if p.CommandID != "" {
			cmd = p.CommandID + "/prompt"
		}
		if _, err := o.Prompt(ctx, s.ID, p.Prompt, p.Images, cmd, false); err != nil {
			o.log.Warn("initial prompt failed", "session", s.ID, "err", err)
		}
	}
	if hasPrompt {
		o.nameSession(s.ID, proj.Name, p.Prompt, p.Title == "", s.Workspace.Kind == model.WorkspaceWorktree && !named)
	}
	return s, nil
}

// forkTurn finds turn n, or with 0 the last turn that ended.
func forkTurn(turns []*model.Turn, n int) (*model.Turn, error) {
	for i := len(turns) - 1; i >= 0; i-- {
		t := turns[i]
		if n != 0 && t.N != n {
			continue
		}
		if t.Status == model.TurnRunning {
			if n == 0 {
				continue
			}
			return nil, errorf(CodeConflict, "turn %d is still running", n)
		}
		return t, nil
	}
	if n == 0 {
		return nil, errorf(CodeInvalid, "the session has no turn to fork from yet")
	}
	return nil, errorf(CodeNotFound, "no turn %d", n)
}

// forkWorktree checks out a new branch in dir with the files of checkpoint:
// the branch starts at the commit the checkpoint was taken on, and the
// changes the checkpoint has on top of it are in the working tree.
func forkWorktree(ctx context.Context, repo, dir, branch, checkpoint string) error {
	if checkpoint == "" {
		return errorf(CodeInvalid, "the turn has no checkpoint of its files")
	}
	base, err := gitx.Resolve(ctx, repo, checkpoint+"^")
	if err != nil {
		return errorf(CodeInvalid, "the checkpoint's commit: %v", err)
	}
	if err := gitx.AddWorktree(ctx, repo, dir, branch, base); err != nil {
		return errorf(CodeInvalid, "create worktree: %v", err)
	}
	if _, err := gitx.Revert(ctx, dir, checkpoint, base); err != nil {
		gitx.RemoveWorktree(ctx, repo, dir)
		return fmt.Errorf("restore the turn's files: %w", err)
	}
	return nil
}

// copyTranscript copies the items and turns of a session up to and including
// turn n into session id, with new ids and orders, and what was still
// running, such as a background subagent, ended. Turnless items go too where
// they come before turn n+1: notices, but not prompts that never ran. With
// another agent, the turns' fork points are dropped, since they are the
// first agent's.
func copyTranscript(id string, items []*model.Item, turns []*model.Turn, n int, sameAgent bool) ([]*model.Item, []*model.Turn) {
	turnIDs := map[string]string{}
	var copiedTurns []*model.Turn
	later := map[string]bool{}
	for _, t := range turns {
		if t.N > n {
			later[t.ID] = true
			continue
		}
		c := *t
		c.ID, c.SessionID = store.NewID(), id
		if !sameAgent {
			c.ForkPoint = ""
		}
		turnIDs[t.ID] = c.ID
		copiedTurns = append(copiedTurns, &c)
	}
	cutoff := int64(-1)
	for _, it := range items {
		if later[it.TurnID] && (cutoff < 0 || it.Order < cutoff) {
			cutoff = it.Order
		}
	}
	var kept []*model.Item
	for _, it := range items {
		switch {
		case it.TurnID != "":
			if _, ok := turnIDs[it.TurnID]; !ok {
				continue
			}
		case it.Kind == model.ItemUserMessage, cutoff >= 0 && it.Order > cutoff:
			continue // a prompt cancelled in the queue, or what came after turn n
		}
		kept = append(kept, it)
	}
	slices.SortFunc(kept, func(a, b *model.Item) int { return int(a.Order - b.Order) })
	itemIDs := map[string]string{}
	for _, it := range kept {
		itemIDs[it.ID] = store.NewID()
	}
	out := make([]*model.Item, len(kept))
	for i, it := range kept {
		c := *it
		c.ID, c.SessionID, c.Order, c.TurnID = itemIDs[it.ID], id, int64(i+1), turnIDs[it.TurnID]
		c.ParentItemID = itemIDs[it.ParentItemID]
		if it.Approval != nil && it.Approval.ToolItemID != "" {
			ap := *it.Approval
			ap.ToolItemID = itemIDs[ap.ToolItemID]
			c.Approval = &ap
		}
		switch c.Status {
		case model.ItemInProgress, model.ItemQueued:
			c.Status = model.ItemCompleted
			if c.Kind == model.ItemToolCall {
				c.Status = model.ItemFailed
			}
		case model.ItemPending:
			c.Status = model.ItemExpired
			if c.Kind != model.ItemApproval {
				c.Status = model.ItemFailed
			}
		}
		out[i] = &c
	}
	return out, copiedTurns
}

// pendingFork reports whether the session is a fork whose agent hasn't taken
// a prompt yet, so it has yet to get the conversation it continues.
func (a *actor) pendingFork() bool { return a.sess.ForkedFrom != nil && a.sess.NativeID == "" }

// forkOptions has the agent copy the conversation of the session this one
// forks, when it can: it is the same agent, which marked where the turn
// ended. Otherwise it starts afresh, and the conversation goes to it as
// text with the first prompt (startTurn).
func (a *actor) forkOptions(opts *harness.OpenOptions) {
	h, _ := a.o.reg.Get(a.sess.Harness)
	if f, ok := h.(harness.Forker); !ok || !f.SupportsFork() {
		return
	}
	ctx := context.Background()
	src, err := a.o.st.Session(ctx, a.sess.ForkedFrom.SessionID)
	if err != nil || src.Harness != a.sess.Harness || src.NativeID == "" {
		return
	}
	turns, err := a.o.st.Turns(ctx, a.sess.ID)
	if err != nil {
		return
	}
	for _, t := range turns {
		if t.N == a.sess.ForkedFrom.Turn && t.ForkPoint != "" {
			opts.ResumeID, opts.Fork, opts.ForkAt = src.NativeID, true, t.ForkPoint
		}
	}
}

// handoffBudget is about how much of the conversation a fork's agent gets
// as text, in characters; handoffEntry is the most of one entry.
const (
	handoffBudget = 80_000
	handoffEntry  = 6_000
)

// handoff is the conversation so far, for an agent that starts a fork afresh:
// its first request, and as much of the rest as the budget holds, latest
// first. except is the prompt it goes with.
func (a *actor) handoff(ctx context.Context, except string) string {
	items, err := a.o.st.Items(ctx, a.sess.ID)
	if err != nil {
		a.o.log.Warn("reading the conversation for the fork failed", "session", a.sess.ID, "err", err)
	}
	var entries []string
	first := -1
	for _, it := range items {
		if it.ID == except {
			continue
		}
		if e := TranscriptLine(it); e != "" {
			if first < 0 && it.Kind == model.ItemUserMessage {
				first = len(entries)
			}
			entries = append(entries, e)
		}
	}
	budget := handoffBudget
	var head []string
	if first >= 0 {
		head = entries[:first+1]
		for _, e := range head {
			budget -= len(e)
		}
		entries = entries[first+1:]
	}
	start := len(entries)
	for start > 0 && budget-len(entries[start-1]) >= 0 {
		start--
		budget -= len(entries[start])
	}
	var b strings.Builder
	b.WriteString("[Note from the user's tooling: this session is a fork of an earlier conversation, after its turn ")
	fmt.Fprintf(&b, "%d, which you didn't take part in. Here it is as a transcript, so that you can carry on from it. ", a.sess.ForkedFrom.Turn)
	b.WriteString("Re-read files before editing them. The user's new request follows the transcript.]\n\n<transcript>\n")
	for _, e := range head {
		b.WriteString(e + "\n\n")
	}
	if start > 0 {
		fmt.Fprintf(&b, "[… %d entries left out …]\n\n", start)
	}
	for _, e := range entries[start:] {
		b.WriteString(e + "\n\n")
	}
	b.WriteString("</transcript>")
	return b.String()
}

// TranscriptLine is an item as the transcript of a handoff, or of an MCP
// tool's read, shows it to an agent, or "" for what it leaves out: reasoning,
// a subagent's work, and prompts that never ran.
func TranscriptLine(it *model.Item) string {
	switch it.Kind {
	case model.ItemUserMessage:
		if it.TurnID == "" {
			return ""
		}
		text := clip(it.Text)
		if n := len(it.Images); n > 0 {
			text += fmt.Sprintf(" [%d image(s)]", n)
		}
		return "User:\n" + text
	case model.ItemAssistantMessage:
		if it.ParentItemID != "" {
			return "" // a subagent's
		}
		return "Assistant:\n" + clip(it.Text)
	case model.ItemToolCall:
		if it.Tool == nil || it.ParentItemID != "" {
			return ""
		}
		line := "Tool: " + firstLine(cmp.Or(it.Tool.Title, it.Tool.Name))
		if it.Tool.ExitCode != nil {
			line += fmt.Sprintf(" (exit %d)", *it.Tool.ExitCode)
		} else if it.Status == model.ItemFailed {
			line += " (failed)"
		}
		return line
	case model.ItemPlan:
		if it.Plan == nil {
			return ""
		}
		var b strings.Builder
		b.WriteString("Plan:")
		if it.Plan.Text != "" {
			b.WriteString("\n" + clip(it.Plan.Text))
		}
		for _, e := range it.Plan.Entries {
			fmt.Fprintf(&b, "\n- [%s] %s", e.Status, e.Content)
		}
		return b.String()
	case model.ItemApproval:
		ap := it.Approval
		if ap == nil || ap.Decision == nil {
			return ""
		}
		line := "Asked: " + firstLine(ap.Title)
		if ap.Special == model.SpecialPlan && ap.PlanText != "" {
			line += "\n" + clip(ap.PlanText)
		}
		for _, q := range ap.Questions {
			if answer, ok := ap.Decision.Answers[q.Question]; ok {
				line += fmt.Sprintf("\n%s → %s", q.Question, answer)
			}
		}
		for _, opt := range ap.Options {
			if opt.ID == ap.Decision.OptionID {
				line += " — " + opt.Label
			}
		}
		if ap.Decision.Message != "" {
			line += ": " + firstLine(ap.Decision.Message)
		}
		return line
	case model.ItemNotice, model.ItemError:
		if it.Text == "" {
			return ""
		}
		return "[" + firstLine(it.Text) + "]"
	}
	return ""
}

func clip(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= handoffEntry {
		return s
	}
	return string([]rune(s)[:handoffEntry]) + " […]"
}

func firstLine(s string) string {
	line, _, more := strings.Cut(strings.TrimSpace(s), "\n")
	if more {
		line += " …"
	}
	return line
}
