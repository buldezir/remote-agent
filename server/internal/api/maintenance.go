package api

import (
	"context"
	"errors"
	"strings"

	"remote-agent/internal/harness"
	"remote-agent/internal/model"
	"remote-agent/internal/orchestrator"
	"remote-agent/internal/tasks"
)

// Updater updates rad itself, for server.checkUpdate and server.update
// (cmd/rad's selfUpdater).
type Updater interface {
	Check(ctx context.Context, refresh bool) model.UpdateInfo
	// Install installs the latest release, reporting to t. Once it returns
	// nil, rad restarts as the new version.
	Install(ctx context.Context, t *tasks.Task) error
}

type checkUpdateParams struct {
	Refresh bool `json:"refresh"` // look again rather than use the last answer
}

type harnessParams struct {
	Harness string `json:"harness"`
}

type taskInputParams struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

func (s *Server) checkUpdate(ctx context.Context, p checkUpdateParams) (any, error) {
	if s.Updater == nil {
		return model.UpdateInfo{Current: Version, Reason: "This rad doesn't update itself.", Busy: []string{}}, nil
	}
	return s.Updater.Check(ctx, p.Refresh), nil
}

func (s *Server) updateServer(ctx context.Context) (any, error) {
	if s.Updater == nil || s.Tasks == nil {
		return nil, &methodError{"unavailable", "this rad doesn't update itself"}
	}
	info := s.Updater.Check(ctx, true)
	switch {
	case info.Via == "":
		return nil, &methodError{"unavailable", info.Reason}
	case !info.Available:
		return nil, &methodError{"conflict", "rad " + info.Current + " is up to date"}
	}
	return s.Tasks.Start(model.TaskUpdateServer, "", s.Updater.Install), nil
}

// maintainer finds an installed agent's update or login command.
func (s *Server) maintainer(ctx context.Context, id string, login bool) (harness.Harness, model.HarnessInfo, harness.Command, error) {
	reg := s.orch.Registry()
	h, ok := reg.Get(id)
	if !ok {
		return nil, model.HarnessInfo{}, harness.Command{}, invalid("unknown harness %q", id)
	}
	info := reg.Info(ctx, h, false)
	if !info.Installed {
		return nil, info, harness.Command{}, &methodError{"unavailable", info.Name + " is not installed on this machine"}
	}
	var cmd harness.Command
	if m, ok := h.(harness.Maintainer); ok {
		if login {
			cmd, ok = m.LoginCommand()
		} else {
			cmd, ok = m.UpdateCommand()
		}
		if ok {
			return h, info, cmd, nil
		}
	}
	what := "update itself"
	if login {
		what = "sign in from a command"
	}
	return nil, info, cmd, &methodError{"unavailable", info.Name + " can't " + what + "; set it in rad's config"}
}

func (s *Server) updateAgent(ctx context.Context, id string) (any, error) {
	h, info, cmd, err := s.maintainer(ctx, id, false)
	if err != nil || s.Tasks == nil {
		return nil, err
	}
	reg := s.orch.Registry()
	return s.Tasks.Start(model.TaskUpdateAgent, id, func(ctx context.Context, t *tasks.Task) error {
		t.Println("$ %s", strings.Join(cmd.Args, " "))
		if err := t.Run(ctx, cmd); err != nil {
			return err
		}
		after := reg.Info(ctx, h, true)
		t.SetVersion(after.Version)
		switch {
		case after.Version == "":
		case after.Version != info.Version && info.Version != "":
			t.Println("%s %s → %s", after.Name, info.Version, after.Version)
		default:
			t.Println("%s %s", after.Name, after.Version)
		}
		return nil
	}), nil
}

func (s *Server) loginAgent(ctx context.Context, id string) (any, error) {
	h, _, cmd, err := s.maintainer(ctx, id, true)
	if err != nil || s.Tasks == nil {
		return nil, err
	}
	reg := s.orch.Registry()
	return s.Tasks.Start(model.TaskLogin, id, func(ctx context.Context, t *tasks.Task) error {
		t.Println("$ %s", strings.Join(cmd.Args, " "))
		if err := t.Run(ctx, cmd); err != nil {
			return err
		}
		reg.SignedIn(id) // the probe tells otherwise
		after := reg.Info(ctx, h, true)
		t.SetVersion(after.Version)
		if !after.AuthOK {
			msg := after.Hint
			if msg == "" {
				msg = after.Name + " is still not signed in"
			}
			return errors.New(msg)
		}
		t.Println("%s is signed in.", after.Name)
		return nil
	}), nil
}

// commit starts a session's commit as a task, unless the session can't
// commit now. A commit that runs for the session already is the answer.
func (s *Server) commit(ctx context.Context, p orchestrator.CommitParams) (any, error) {
	if s.Tasks == nil {
		return nil, &methodError{"unavailable", "this rad doesn't run tasks"}
	}
	if err := s.orch.CheckCommit(ctx, p.SessionID); err != nil {
		return nil, err
	}
	return s.Tasks.StartForSession(model.TaskCommit, p.SessionID, func(ctx context.Context, t *tasks.Task) error {
		return s.orch.Commit(ctx, p, t)
	}), nil
}

func (s *Server) taskError(err error) error {
	if errors.Is(err, tasks.ErrNotFound) {
		return &methodError{"not_found", err.Error()}
	}
	if err != nil {
		return &methodError{"conflict", err.Error()}
	}
	return nil
}
