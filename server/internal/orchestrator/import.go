package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"remote-agent/internal/gitx"
	"remote-agent/internal/harness"
	"remote-agent/internal/model"
	"remote-agent/internal/store"
)

// An import picks up a session the agent ran outside rad, such as in a
// terminal, from the agent's own files (harness.Importer): rad copies its
// conversation into a session of its own in the project folder, with the
// agent's id for it as the native id, so that the next prompt resumes it as
// rad resumes its own sessions.

// activeWindow is how recently an agent wrote to a session that may still
// run in a terminal.
const activeWindow = time.Minute

// importer finds the project and the harness that imports from it.
func (o *Orchestrator) importer(ctx context.Context, projectID, harnessID string) (*model.Project, harness.Importer, model.HarnessInfo, error) {
	proj, err := o.st.Project(ctx, projectID)
	if err != nil {
		return nil, nil, model.HarnessInfo{}, errorf(CodeNotFound, "project %s not found", projectID)
	}
	h, ok := o.reg.Get(harnessID)
	if !ok {
		return nil, nil, model.HarnessInfo{}, errorf(CodeInvalid, "unknown harness %q", harnessID)
	}
	imp, ok := h.(harness.Importer)
	info := o.reg.Info(ctx, h, false)
	if !ok {
		return nil, nil, info, errorf(CodeInvalid, "%s can't pick up its sessions", info.Name)
	}
	if !info.Installed {
		return nil, nil, info, errorf(CodeUnavailable, "%s is not installed on this machine", info.Name)
	}
	return proj, imp, info, nil
}

// nativeIDs are the agent's ids of rad's sessions with harnessID, archived
// ones included.
func (o *Orchestrator) nativeIDs(ctx context.Context, harnessID string) (map[string]*model.Session, error) {
	sessions, err := o.st.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	ids := map[string]*model.Session{}
	for _, s := range sessions {
		if s.Harness == harnessID && s.NativeID != "" {
			ids[s.NativeID] = s
		}
	}
	return ids, nil
}

// Importable lists the sessions the agent harnessID ran in the project's
// folder outside rad, newest first.
func (o *Orchestrator) Importable(ctx context.Context, projectID, harnessID string) ([]model.ImportableSession, error) {
	proj, imp, _, err := o.importer(ctx, projectID, harnessID)
	if err != nil {
		return nil, err
	}
	sessions, err := o.nativeIDs(ctx, harnessID)
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(sessions))
	for id := range sessions {
		known[id] = true
	}
	list, err := imp.ListNative(ctx, proj.Path, known)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i].Active = time.Since(list[i].UpdatedAt) < activeWindow
	}
	return list, nil
}

type ImportParams struct {
	CommandID string `json:"commandId"`
	ProjectID string `json:"projectId"`
	Harness   string `json:"harness"`
	NativeID  string `json:"nativeId"` // the agent's id for it, from session.importable
	// Model, Effort and Mode are what the agent goes on with, as in
	// session.create: absent, the harness's defaults.
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	Mode   string `json:"mode,omitempty"`
	Title  string `json:"title,omitempty"`
}

// Import makes a session of the agent's own session p.NativeID, which ran
// in the project's folder outside rad: its transcript is the conversation
// so far, and its next prompt goes on with it.
func (o *Orchestrator) Import(ctx context.Context, p ImportParams) (*model.Session, error) {
	defer o.lockCommand(p.CommandID)()
	if res, ok, err := o.receipt(ctx, p.CommandID); err != nil {
		return nil, err
	} else if ok {
		var s model.Session
		return &s, json.Unmarshal(res, &s)
	}
	proj, imp, info, err := o.importer(ctx, p.ProjectID, p.Harness)
	if err != nil {
		return nil, err
	}
	if p.NativeID == "" {
		return nil, errorf(CodeInvalid, "which session? nativeId is missing")
	}
	// One import of a session at a time, so that two don't both find it new.
	defer o.lockCommand("import:" + p.Harness + "/" + p.NativeID)()
	sessions, err := o.nativeIDs(ctx, p.Harness)
	if err != nil {
		return nil, err
	}
	if s := sessions[p.NativeID]; s != nil {
		return nil, errorf(CodeConflict, "that session is in rad already, as “%s”", s.Title)
	}
	var images harness.ImageStore
	if o.opt.Images != nil {
		images = o.opt.Images
	}
	tr, err := imp.ReadNative(ctx, proj.Path, p.NativeID, images)
	if errors.Is(err, harness.ErrNotFound) {
		return nil, errorf(CodeNotFound, "%s has no session %s in %s", info.Name, p.NativeID, proj.Path)
	} else if err != nil {
		return nil, err
	}

	mode := p.Mode
	if mode == "" {
		mode = info.DefaultMode
	}
	now := time.Now().UTC()
	p.Title = userTitle(p.Title)
	s := &model.Session{
		ID: store.NewID(), ProjectID: proj.ID, Harness: p.Harness, Model: p.Model, Effort: p.Effort, Mode: mode,
		Workspace: model.Workspace{Kind: model.WorkspaceRoot, Path: proj.Path},
		Status:    model.SessionIdle, NativeID: p.NativeID, Title: p.Title, CustomTitle: p.Title != "", CreatedAt: now,
	}
	if proj.IsGitRepo {
		s.Workspace.Branch = gitx.CurrentBranch(ctx, proj.Path)
	}
	if tr.Model != "" {
		s.ModelInfo = &model.ModelInfo{ID: tr.Model}
	}
	turns, items, firstPrompt := importedTranscript(s.ID, tr, now)
	if len(turns) > 0 && !turns[0].StartedAt.IsZero() {
		s.CreatedAt = turns[0].StartedAt
	}
	named := s.Title != "" || tr.Title != ""
	if s.Title == "" {
		s.Title = tr.Title
	}
	if s.Title == "" {
		s.Title = titleFrom(firstPrompt)
	}
	notice := &model.Item{ID: store.NewID(), SessionID: s.ID, Order: int64(len(items)) + 1, Kind: model.ItemNotice, Status: model.ItemCompleted,
		Text: fmt.Sprintf("Picked up from %s, where this session ran outside rad. The next prompt goes on with it.", info.Name)}
	if time.Since(tr.UpdatedAt) < activeWindow {
		notice.Text += fmt.Sprintf(" %s wrote to it less than a minute ago: if it still runs in a terminal, quit it there first, or the two will write over each other.", info.Name)
	}
	s.ItemCount = notice.Order

	_, err = o.st.Write(ctx, func(w *store.W) error {
		if err := w.PutSession(s); err != nil {
			return err
		}
		for _, t := range turns {
			if err := w.PutTurn(t); err != nil {
				return err
			}
		}
		for _, it := range append(items, notice) {
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
	o.log.Info("session imported", "session", s.ID, "harness", p.Harness, "turns", len(turns), "items", len(items), "device", clientOf(ctx))
	if !named {
		o.nameSession(s.ID, proj.Name, firstPrompt, true, false)
	}
	return s, nil
}

// importedTranscript makes the turns and items of session id from an
// agent's transcript: rad's ids and orders in place of the adapter's keys,
// and tool output cut as rad cuts it. It also returns the first prompt.
func importedTranscript(id string, tr *harness.Transcript, now time.Time) ([]*model.Turn, []*model.Item, string) {
	ids := map[string]string{}
	for _, t := range tr.Turns {
		for _, it := range t.Items {
			ids[it.ID] = store.NewID()
		}
	}
	var turns []*model.Turn
	var items []*model.Item
	first := ""
	for i, t := range tr.Turns {
		turn := &model.Turn{ID: store.NewID(), SessionID: id, N: i + 1, Status: t.Status, Error: t.Error,
			StartedAt: t.StartedAt, ForkPoint: t.ForkPoint}
		if turn.Status == "" || turn.Status == model.TurnRunning {
			turn.Status = model.TurnCompleted
		}
		if turn.StartedAt.IsZero() {
			turn.StartedAt = now
		}
		ended := t.EndedAt
		if ended.Before(turn.StartedAt) {
			ended = turn.StartedAt
		}
		turn.EndedAt = &ended
		turns = append(turns, turn)
		for _, in := range t.Items {
			it := in
			it.ID, it.SessionID, it.TurnID = ids[in.ID], id, turn.ID
			it.Order = int64(len(items)) + 1
			it.ParentItemID = ids[in.ParentItemID]
			if it.CreatedAt.IsZero() {
				it.CreatedAt = turn.StartedAt
			}
			if in.Approval != nil && in.Approval.ToolItemID != "" {
				ap := *in.Approval
				ap.ToolItemID = ids[ap.ToolItemID]
				it.Approval = &ap
			}
			if in.Tool != nil && len(in.Tool.Output) > model.MaxToolOutput {
				tool := *in.Tool
				tool.Output = "…\n" + tool.Output[len(tool.Output)-model.MaxToolOutput:]
				it.Tool = &tool
			}
			if first == "" && it.Kind == model.ItemUserMessage {
				first = strings.TrimSpace(it.Text)
			}
			items = append(items, &it)
		}
	}
	return turns, items, first
}
