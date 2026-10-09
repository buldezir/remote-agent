package api

import (
	"context"
	"encoding/json"
	"errors"

	"remote-agent/internal/fsbrowse"
	"remote-agent/internal/gitx"
	"remote-agent/internal/harness"
	"remote-agent/internal/model"
	"remote-agent/internal/orchestrator"
)

func decode[T any](raw json.RawMessage) (T, error) {
	var v T
	if len(raw) == 0 || string(raw) == "null" {
		return v, nil
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, invalid("bad params: %v", err)
	}
	return v, nil
}

// call decodes params into P and invokes f.
func call[P any](raw json.RawMessage, f func(P) (any, error)) (any, error) {
	p, err := decode[P](raw)
	if err != nil {
		return nil, err
	}
	return f(p)
}

type idParams struct {
	ID string `json:"id"`
}

type sessionParams struct {
	SessionID string `json:"sessionId"`
}

type pathParams struct {
	Path string `json:"path"`
}

type harnessListParams struct {
	Refresh bool `json:"refresh"`
}

type promptParams struct {
	CommandID string `json:"commandId"`
	SessionID string `json:"sessionId"`
	Text      string `json:"text"`
}

type interruptParams struct {
	SessionID string `json:"sessionId"`
	Force     bool   `json:"force"`
}

type setModeParams struct {
	SessionID string `json:"sessionId"`
	Mode      string `json:"mode"`
}

type archiveParams struct {
	SessionID      string `json:"sessionId"`
	RemoveWorktree bool   `json:"removeWorktree"`
}

type respondParams struct {
	CommandID  string            `json:"commandId"`
	SessionID  string            `json:"sessionId"`
	ApprovalID string            `json:"approvalId"`
	OptionID   string            `json:"optionId"`
	Message    string            `json:"message,omitempty"`
	Answers    map[string]string `json:"answers,omitempty"`
}

type turnDiffParams struct {
	TurnID string   `json:"turnId"`
	Paths  []string `json:"paths,omitempty"`
}

type sessionDiffParams struct {
	SessionID string   `json:"sessionId"`
	Paths     []string `json:"paths,omitempty"`
}

type revertParams struct {
	SessionID string `json:"sessionId"`
	Turn      int    `json:"turn"`
}

type projectIDParams struct {
	ProjectID string `json:"projectId"`
}

func (c *conn) dispatch(method string, raw json.RawMessage) (any, error) {
	// Commands outlive a dropped connection: a half-applied create or prompt
	// is worse than finishing it. Clients retry with the same commandId.
	ctx := orchestrator.WithClient(context.WithoutCancel(c.ctx), c.device.Name)
	o := c.s.orch
	switch method {
	case "server.info":
		info := c.s.info()
		return map[string]any{
			"serverId": info.ServerID, "name": info.Name, "protocolVersion": info.ProtocolVersion,
			"version": info.Version, "roots": c.s.fs.Roots(), "deviceId": c.device.ID,
		}, nil

	case "device.unpair":
		// The phone forgets this server: revoke its own token. The socket is
		// closed by the periodic revalidation (or by the client).
		_, err := c.s.st.DeleteDevice(ctx, c.device.ID)
		return nil, err

	case "harness.list":
		return call(raw, func(p harnessListParams) (any, error) {
			return map[string]any{"harnesses": o.Registry().Infos(ctx, p.Refresh)}, nil
		})

	case "fs.list":
		return call(raw, func(p pathParams) (any, error) {
			l, err := c.s.fs.List(p.Path)
			if errors.Is(err, fsbrowse.ErrOutsideRoots) {
				return nil, invalid("%v", err)
			}
			return l, err
		})

	case "project.list":
		ps, err := c.s.st.Projects(ctx)
		if ps == nil {
			ps = []*model.Project{}
		}
		return map[string]any{"projects": ps}, err

	case "project.add":
		return call(raw, func(p pathParams) (any, error) {
			path, err := c.s.fs.Resolve(p.Path)
			if err != nil {
				return nil, invalid("%v", err)
			}
			return o.AddProject(ctx, path)
		})

	case "project.remove":
		return call(raw, func(p idParams) (any, error) { return nil, o.RemoveProject(ctx, p.ID) })

	case "session.list":
		ss, err := c.s.st.Sessions(ctx)
		if ss == nil {
			ss = []*model.Session{}
		}
		return map[string]any{"sessions": ss}, err

	case "session.create":
		return call(raw, func(p orchestrator.CreateSessionParams) (any, error) { return o.CreateSession(ctx, p) })

	case "session.prompt":
		return call(raw, func(p promptParams) (any, error) { return o.Prompt(ctx, p.SessionID, p.Text, p.CommandID) })

	case "session.interrupt":
		return call(raw, func(p interruptParams) (any, error) { return nil, o.Interrupt(ctx, p.SessionID, p.Force) })

	case "session.setMode":
		return call(raw, func(p setModeParams) (any, error) { return nil, o.SetMode(ctx, p.SessionID, p.Mode) })

	case "session.archive":
		return call(raw, func(p archiveParams) (any, error) { return nil, o.Archive(ctx, p.SessionID, p.RemoveWorktree) })

	case "approval.respond":
		return call(raw, func(p respondParams) (any, error) {
			return nil, o.Respond(ctx, p.SessionID, p.ApprovalID,
				harness.Response{OptionID: p.OptionID, Message: p.Message, Answers: p.Answers}, p.CommandID)
		})

	case "subscribe":
		return call(raw, func(p subscribeParams) (any, error) { return c.subscribe(p) })

	case "unsubscribe":
		return call(raw, func(p streamParams) (any, error) { c.unsubscribe(p.Stream); return nil, nil })

	case "git.turnDiff":
		return call(raw, func(p turnDiffParams) (any, error) { return o.TurnDiff(ctx, p.TurnID, p.Paths) })

	case "git.sessionDiff":
		return call(raw, func(p sessionDiffParams) (any, error) { return o.SessionDiff(ctx, p.SessionID, p.Paths) })

	case "git.revert":
		return call(raw, func(p revertParams) (any, error) {
			files, err := o.Revert(ctx, p.SessionID, p.Turn)
			if files == nil {
				files = []gitx.FileStat{}
			}
			return map[string]any{"files": files}, err
		})

	case "git.branches":
		return call(raw, func(p projectIDParams) (any, error) {
			proj, err := c.s.st.Project(ctx, p.ProjectID)
			if err != nil {
				return nil, &methodError{"not_found", "project not found"}
			}
			if !proj.IsGitRepo {
				return map[string]any{"branches": []string{}, "current": ""}, nil
			}
			bs, err := gitx.Branches(ctx, proj.Path)
			return map[string]any{"branches": bs, "current": gitx.CurrentBranch(ctx, proj.Path)}, err
		})
	}
	return nil, &methodError{"method_not_found", "unknown method " + method}
}
