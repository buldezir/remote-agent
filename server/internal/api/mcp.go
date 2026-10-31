package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"remote-agent/internal/harness"
	"remote-agent/internal/mcp"
	"remote-agent/internal/model"
	"remote-agent/internal/orchestrator"
	"remote-agent/internal/store"
)

// rad's MCP server: tools for agents to start and drive other sessions on
// this machine. The agents rad runs reach it with their session's token
// (orchestrator.AgentToken), others, such as `rad mcp` for an agent in a
// terminal, with a device's. It speaks Streamable HTTP without streams: each
// POST is one JSON-RPC message, and the reply is its body.

// maxDepth is how many sessions deep agents may start sessions that start
// sessions.
const maxDepth = 3

// Waiting on a session lasts this long unless the agent asks for another.
const (
	defaultWait = 10 * time.Minute
	maxWait     = time.Hour
	waitPoll    = 250 * time.Millisecond
)

const mcpInstructions = "rad runs coding agents on this machine for the user, who follows them in the Remote Agent app. " +
	"These tools start and drive other sessions, each with an agent of its own, in the projects on this machine. " +
	"The user sees every session in the app, and answers their approvals and questions there: you can't. " +
	"Use them when the user asks you to hand work to other agents, or to run work in parallel: start_session, then wait_for_session and read_session."

// caller is who calls a tool: the agent of a session, or a device.
type caller struct {
	session string // the calling agent's session; "" for a device
	device  string
}

type callerKey struct{}

func callerOf(ctx context.Context) caller {
	c, _ := ctx.Value(callerKey{}).(caller)
	return c
}

func (s *Server) mcpHandler(w http.ResponseWriter, r *http.Request) {
	// A page on another site mustn't reach it through the user's browser.
	if origin := r.Header.Get("Origin"); origin != "" && !s.webOrigin(origin) {
		writeJSON(w, http.StatusForbidden, rpcError{Code: "forbidden", Message: "origin not allowed"})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	who := caller{}
	if id, ok := s.orch.TokenSession(bearer(r)); ok {
		who.session = id
	} else if dev, ok := s.device(w, r); ok {
		who.device = dev.Name
	} else {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, rpcError{Code: "invalid", Message: "could not read the request"})
		return
	}
	ctx := orchestrator.WithClient(context.WithValue(r.Context(), callerKey{}, who), cmpOr(who.device, "agent of "+who.session))
	out := s.mcpServer().Handle(ctx, body)
	if out == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *Server) mcpServer() *mcp.Server {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	object := func(required []string, props map[string]any) map[string]any {
		o := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
		if len(required) > 0 {
			o["required"] = required
		}
		return o
	}
	session := str("A session's id, from list_sessions (a unique start of it will do).")
	agentProps := map[string]any{
		"agent":    str("An agent's id from list_agents. Default: your own agent."),
		"model":    str("A model id of the agent, from list_agents. Default: the agent's."),
		"effort":   str("A reasoning effort of the model, from list_agents. Default: the model's."),
		"mode":     str("A permission mode of the agent, from list_agents. Default: your own mode with your own agent, else the agent's default; those are the only ones you can choose."),
		"worktree": map[string]any{"type": "boolean", "description": "In a git project, work on a branch of its own in a separate checkout, so as not to touch the project folder. Default: no; a fork works where the session it forks does."},
		"branch":   str("With worktree, the branch's name. Default: one named after the request."),
	}
	with := func(props map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range agentProps {
			out[k] = v
		}
		for k, v := range props {
			out[k] = v
		}
		return out
	}
	return &mcp.Server{Name: harness.MCPName, Version: Version, Instructions: mcpInstructions, Tools: []mcp.Tool{
		{Name: "list_projects", Title: "List projects", ReadOnly: true,
			Description: "Lists the projects on this machine that sessions work in: their ids, names and folders.",
			Input:       object(nil, map[string]any{}), Call: s.toolListProjects},
		{Name: "list_agents", Title: "List agents", ReadOnly: true,
			Description: "Lists the coding agents installed and signed in on this machine, with their models, reasoning efforts and permission modes.",
			Input:       object(nil, map[string]any{}), Call: s.toolListAgents},
		{Name: "list_sessions", Title: "List sessions", ReadOnly: true,
			Description: "Lists the sessions on this machine, newest first: their ids, titles, agents and statuses.",
			Input: object(nil, map[string]any{
				"project":  str("Only this project's sessions: its id or folder."),
				"archived": map[string]any{"type": "boolean", "description": "Include archived sessions."},
			}), Call: s.toolListSessions},
		{Name: "start_session", Title: "Start a session",
			Description: "Starts a session in a project, with an agent of its own, and sends it the first request. It returns at once; " +
				"follow it with wait_for_session and read_session. The user sees it in the app and answers its approvals there.",
			Input: object([]string{"project", "prompt"}, with(map[string]any{
				"project": str("The project's id or folder, from list_projects. A folder under rad's roots becomes a project."),
				"prompt":  str("The request for the session's agent. It knows nothing of your conversation: say everything it needs."),
				"title":   str("The session's title. Default: one made from the request."),
			})), Call: s.toolStartSession},
		{Name: "send_prompt", Title: "Send a prompt",
			Description: "Sends a request to a session's agent. While the agent works, it waits its turn, unless it steers. It returns at once.",
			Input: object([]string{"session", "prompt"}, map[string]any{
				"session": session, "prompt": str("The request."),
				"steer": map[string]any{"type": "boolean", "description": "While the agent works, add the request to what it's doing, for it to read at its next step, rather than wait for it to finish. Agents that can't (steers in list_agents) get it once they finish."},
			}), Call: s.toolSendPrompt},
		{Name: "read_session", Title: "Read a session", ReadOnly: true,
			Description: "Reads a session: its status, and its transcript (requests, replies, tool calls and questions), of its last turns or all of them.",
			Input: object([]string{"session"}, map[string]any{
				"session": session,
				"turns":   map[string]any{"type": "integer", "minimum": 0, "description": "How many of the last turns to read; 0 for all. Default: 1."},
			}), Call: s.toolReadSession},
		{Name: "wait_for_session", Title: "Wait for a session", ReadOnly: true,
			Description: "Waits until a session's agent stops working: it finished, failed, or needs the user to answer it. Returns its status and last reply.",
			Input: object([]string{"session"}, map[string]any{
				"session":        session,
				"timeoutSeconds": map[string]any{"type": "integer", "minimum": 1, "maximum": int(maxWait / time.Second), "description": "How long to wait at most. Default: 600."},
			}), Call: s.toolWaitForSession},
		{Name: "stop_session", Title: "Stop a session", Destructive: true,
			Description: "Interrupts what a session's agent is doing, and drops the requests waiting for it.",
			Input:       object([]string{"session"}, map[string]any{"session": session}), Call: s.toolStopSession},
		{Name: "rename_session", Title: "Rename a session",
			Description: "Gives a session a title, your own session's too: the user sees it in the app's lists, and rad no longer names the session itself. " +
				"An empty title gives the session back to rad, which names it from its first request.",
			Input: object([]string{"session", "title"}, map[string]any{
				"session": session, "title": str("The title, a few words on one line."),
			}), Call: s.toolRenameSession},
		{Name: "fork_session", Title: "Fork a session",
			Description: "Starts a new session that goes on from one of a session's turns, with its conversation, and in a worktree with its files as they were then. " +
				"With another agent, that agent gets the conversation as a transcript.",
			Input: object([]string{"session"}, with(map[string]any{
				"session": session,
				"turn":    map[string]any{"type": "integer", "minimum": 1, "description": "The turn to go on after. Default: the last one that ended."},
				"prompt":  str("A request to send the fork at once. Default: none."),
			})), Call: s.toolForkSession},
	}}
}

type projectSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	IsGitRepo bool   `json:"isGitRepo"`
}

func (s *Server) toolListProjects(ctx context.Context, _ json.RawMessage) (any, error) {
	ps, err := s.st.Projects(ctx)
	out := []projectSummary{}
	for _, p := range ps {
		out = append(out, projectSummary{p.ID, p.Name, p.Path, p.IsGitRepo})
	}
	return map[string]any{"projects": out}, err
}

type agentSummary struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Models      []model.Choice `json:"models,omitempty"`
	Efforts     []model.Choice `json:"efforts,omitempty"`
	Modes       []model.Choice `json:"modes,omitempty"`
	DefaultMode string         `json:"defaultMode,omitempty"`
	Steers      bool           `json:"steers,omitempty"` // takes send_prompt's steer
}

func (s *Server) toolListAgents(ctx context.Context, _ json.RawMessage) (any, error) {
	out := []agentSummary{}
	for _, h := range s.orch.Registry().Installed(ctx, false) {
		if h.AuthOK {
			out = append(out, agentSummary{h.ID, h.Name, h.Models, h.Efforts, h.Modes, h.DefaultMode, h.Caps.Steer})
		}
	}
	return map[string]any{"agents": out}, nil
}

type sessionSummary struct {
	ID         string            `json:"id"`
	Title      string            `json:"title"`
	Project    string            `json:"project"`
	Agent      string            `json:"agent"`
	Model      string            `json:"model,omitempty"`
	Mode       string            `json:"mode,omitempty"`
	Status     string            `json:"status"`
	Folder     string            `json:"folder"`
	Branch     string            `json:"branch,omitempty"`
	Worktree   bool              `json:"worktree,omitempty"`
	Archived   bool              `json:"archived,omitempty"`
	ForkedFrom *model.ForkedFrom `json:"forkedFrom,omitempty"`
	Updated    time.Time         `json:"updated"`
}

func summarize(sess *model.Session, projects map[string]*model.Project) sessionSummary {
	sum := sessionSummary{ID: sess.ID, Title: sess.Title, Agent: sess.Harness, Model: sess.Model, Mode: sess.Mode,
		Status: string(sess.Status), Folder: orchestrator.Workdir(sess.Workspace), Branch: sess.Workspace.Branch,
		Worktree: sess.Workspace.Kind == model.WorkspaceWorktree, Archived: sess.Archived, ForkedFrom: sess.ForkedFrom, Updated: sess.UpdatedAt}
	if sess.ModelInfo != nil && sess.ModelInfo.ID != "" {
		sum.Model = sess.ModelInfo.ID
	}
	if p := projects[sess.ProjectID]; p != nil {
		sum.Project = p.Name
	}
	return sum
}

func (s *Server) projectMap(ctx context.Context) (map[string]*model.Project, error) {
	ps, err := s.st.Projects(ctx)
	m := map[string]*model.Project{}
	for _, p := range ps {
		m[p.ID] = p
	}
	return m, err
}

func (s *Server) toolListSessions(ctx context.Context, raw json.RawMessage) (any, error) {
	p, err := mcp.Args[struct {
		Project  string `json:"project"`
		Archived bool   `json:"archived"`
	}](raw)
	if err != nil {
		return nil, err
	}
	projects, err := s.projectMap(ctx)
	if err != nil {
		return nil, err
	}
	projectID := ""
	if p.Project != "" {
		proj, err := s.findProject(ctx, p.Project, false)
		if err != nil {
			return nil, err
		}
		projectID = proj.ID
	}
	ss, err := s.st.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	out := []sessionSummary{}
	for _, sess := range ss {
		if (projectID == "" || sess.ProjectID == projectID) && (p.Archived || !sess.Archived) {
			out = append(out, summarize(sess, projects))
		}
	}
	return map[string]any{"sessions": out}, nil
}

// findProject finds a project by its id or folder. With add, a folder under
// rad's roots that isn't one yet becomes one.
func (s *Server) findProject(ctx context.Context, ref string, add bool) (*model.Project, error) {
	if p, err := s.st.Project(ctx, ref); err == nil {
		return p, nil
	}
	if p, err := s.st.ProjectByPath(ctx, strings.TrimSuffix(ref, "/")); err == nil {
		return p, nil
	}
	path, err := s.fs.Resolve(ref) // through symbolic links, as projects are added
	if err == nil {
		if p, err := s.st.ProjectByPath(ctx, path); err == nil {
			return p, nil
		}
	}
	switch {
	case !add:
		return nil, fmt.Errorf("no project %q: list_projects lists them", ref)
	case err != nil:
		return nil, fmt.Errorf("no project %q, and it can't be one: %v", ref, err)
	}
	return s.orch.AddProject(ctx, path)
}

// findSession finds a session by its id, or a unique start of it.
func (s *Server) findSession(ctx context.Context, ref string) (*model.Session, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, errors.New("which session? list_sessions lists them")
	}
	if sess, err := s.st.Session(ctx, ref); err == nil {
		return sess, nil
	}
	ss, err := s.st.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	var found *model.Session
	for _, sess := range ss {
		if len(ref) >= 6 && strings.HasPrefix(sess.ID, ref) {
			if found != nil {
				return nil, fmt.Errorf("more than one session starts with %q", ref)
			}
			found = sess
		}
	}
	if found == nil {
		return nil, fmt.Errorf("no session %q: list_sessions lists them", ref)
	}
	return found, nil
}

// other finds a session that isn't the caller's own.
func (s *Server) other(ctx context.Context, ref string) (*model.Session, error) {
	sess, err := s.findSession(ctx, ref)
	if err == nil && sess.ID == callerOf(ctx).session {
		return nil, errors.New("that is your own session")
	}
	return sess, err
}

type agentArgs struct {
	Agent    string `json:"agent"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
	Mode     string `json:"mode"`
	Worktree *bool  `json:"worktree"`
	Branch   string `json:"branch"`
}

// agent resolves the agent, model and mode a session started by the caller
// gets. An agent can't give one a mode that asks less than its own: with its
// own agent, its own mode, else the agent's default. A device chooses any.
func (s *Server) agent(ctx context.Context, a agentArgs) (string, string, error) {
	who := callerOf(ctx)
	var own *model.Session
	if who.session != "" {
		sess, err := s.st.Session(ctx, who.session)
		if err != nil {
			return "", "", err
		}
		own = sess
		if depth := len(s.orch.Lineage(own.ID)); depth >= maxDepth-1 {
			return "", "", fmt.Errorf("sessions started by agents can start others only %d deep", maxDepth)
		}
	}
	id := a.Agent
	if id == "" && own != nil {
		id = own.Harness
	}
	infos := s.orch.Registry().Installed(ctx, false)
	if id == "" {
		for _, h := range infos {
			if h.AuthOK && (id == "" || h.ID == "claude") {
				id = h.ID
			}
		}
	}
	i := slices.IndexFunc(infos, func(h model.HarnessInfo) bool { return h.ID == id })
	if i < 0 {
		return "", "", fmt.Errorf("no agent %q: list_agents lists them", id)
	}
	info := infos[i]
	if !info.AuthOK {
		return "", "", fmt.Errorf("%s isn't signed in on this machine", info.Name)
	}
	if own == nil {
		return id, a.Mode, nil
	}
	allowed := []string{info.DefaultMode}
	if id == own.Harness && own.Mode != "" {
		allowed = append(allowed, own.Mode)
	}
	mode := a.Mode
	if mode == "" {
		mode = allowed[len(allowed)-1]
	}
	if !slices.Contains(allowed, mode) {
		return "", "", fmt.Errorf("mode %q asks the user less than you may: choose %s", mode, strings.Join(slices.Compact(allowed), " or "))
	}
	return id, mode, nil
}

func (s *Server) toolStartSession(ctx context.Context, raw json.RawMessage) (any, error) {
	p, err := mcp.Args[struct {
		agentArgs
		Project string `json:"project"`
		Prompt  string `json:"prompt"`
		Title   string `json:"title"`
	}](raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.Prompt) == "" {
		return nil, errors.New("the prompt is empty")
	}
	proj, err := s.findProject(ctx, p.Project, true)
	if err != nil {
		return nil, err
	}
	agent, mode, err := s.agent(ctx, p.agentArgs)
	if err != nil {
		return nil, err
	}
	ws := orchestrator.WorkspaceParams{Kind: model.WorkspaceRoot}
	if p.Worktree != nil && *p.Worktree {
		ws = orchestrator.WorkspaceParams{Kind: model.WorkspaceWorktree, Branch: p.Branch}
	}
	sess, err := s.orch.CreateSession(ctx, orchestrator.CreateSessionParams{CommandID: store.NewID(), ProjectID: proj.ID,
		Harness: agent, Model: p.Model, Effort: p.Effort, Mode: mode, Workspace: ws, Prompt: p.Prompt, Title: p.Title,
		StartedBy: callerOf(ctx).session})
	if err != nil {
		return nil, err
	}
	return s.started(ctx, sess)
}

// started describes a session just made: as the store has it, with its
// first turn running.
func (s *Server) started(ctx context.Context, sess *model.Session) (any, error) {
	if now, err := s.st.Session(ctx, sess.ID); err == nil {
		sess = now
	}
	projects, err := s.projectMap(ctx)
	return summarize(sess, projects), err
}

func (s *Server) toolSendPrompt(ctx context.Context, raw json.RawMessage) (any, error) {
	p, err := mcp.Args[struct {
		Session string `json:"session"`
		Prompt  string `json:"prompt"`
		Steer   bool   `json:"steer"`
	}](raw)
	if err != nil {
		return nil, err
	}
	sess, err := s.other(ctx, p.Session)
	if err != nil {
		return nil, err
	}
	busy := sess.Status == model.SessionRunning || sess.Status == model.SessionAwaitingApproval
	it, err := s.orch.Prompt(ctx, sess.ID, p.Prompt, nil, store.NewID(), p.Steer)
	if err != nil {
		return nil, err
	}
	switch {
	case it.Status == model.ItemQueued:
		return "Queued: the agent gets it once its turn ends.", nil
	case p.Steer && busy:
		return "Steered: the agent reads it at its next step, in the turn it's working on.", nil
	}
	return "Sent: the agent is working on it.", nil
}

func (s *Server) toolReadSession(ctx context.Context, raw json.RawMessage) (any, error) {
	p, err := mcp.Args[struct {
		Session string `json:"session"`
		Turns   *int   `json:"turns"`
	}](raw)
	if err != nil {
		return nil, err
	}
	sess, err := s.findSession(ctx, p.Session)
	if err != nil {
		return nil, err
	}
	last := 1
	if p.Turns != nil {
		last = *p.Turns
	}
	turns, err := s.st.Turns(ctx, sess.ID)
	if err != nil {
		return nil, err
	}
	items, err := s.st.Items(ctx, sess.ID)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Session %s, “%s”: %s, %d turn(s).\n", sess.ID, sess.Title, statusText(sess), len(turns))
	from := int64(0) // the order the transcript starts from
	if last > 0 && last < len(turns) {
		first := turns[len(turns)-last].ID
		for _, it := range items {
			if it.TurnID == first {
				from = it.Order
				break
			}
		}
		fmt.Fprintf(&b, "Its last %d turn(s):\n", last)
	}
	b.WriteString("\n")
	for _, it := range items {
		if it.Order < from {
			continue
		}
		if line := orchestrator.TranscriptLine(it); line != "" {
			b.WriteString(line + "\n\n")
		} else if it.Kind == model.ItemApproval && it.Status == model.ItemPending && it.Approval != nil {
			b.WriteString("Waiting for the user to answer: " + it.Approval.Title + "\n\n")
		}
	}
	return strings.TrimSpace(b.String()), nil
}

// statusText says what a session's agent is doing, for an agent to read.
func statusText(sess *model.Session) string {
	switch sess.Status {
	case model.SessionRunning:
		return "working"
	case model.SessionAwaitingApproval:
		return "waiting for the user to answer it in the app"
	case model.SessionError:
		return "failed: " + sess.Error
	}
	if sess.Archived {
		return "archived"
	}
	if n := len(sess.BackgroundTasks); n > 0 {
		return fmt.Sprintf("idle, with %d task(s) running in the background", n)
	}
	return "idle"
}

func (s *Server) toolWaitForSession(ctx context.Context, raw json.RawMessage) (any, error) {
	p, err := mcp.Args[struct {
		Session string `json:"session"`
		Timeout int    `json:"timeoutSeconds"`
	}](raw)
	if err != nil {
		return nil, err
	}
	sess, err := s.other(ctx, p.Session)
	if err != nil {
		return nil, err
	}
	wait := defaultWait
	if p.Timeout > 0 {
		wait = min(time.Duration(p.Timeout)*time.Second, maxWait)
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	tick := time.NewTicker(waitPoll)
	defer tick.Stop()
	for sess.Status == model.SessionRunning {
		select {
		case <-ctx.Done():
			return map[string]any{"session": sess.ID, "status": statusText(sess), "done": false,
				"note": "Still working; wait again, or read_session to see how far it got."}, nil
		case <-tick.C:
		}
		if sess, err = s.st.Session(ctx, sess.ID); err != nil {
			return nil, err
		}
	}
	res := map[string]any{"session": sess.ID, "status": statusText(sess), "done": true}
	items, err := s.st.Items(ctx, sess.ID)
	if err != nil {
		return nil, err
	}
	for _, it := range slices.Backward(items) {
		if it.Kind == model.ItemAssistantMessage && it.ParentItemID == "" {
			res["lastReply"] = it.Text
			break
		}
	}
	return res, nil
}

func (s *Server) toolStopSession(ctx context.Context, raw json.RawMessage) (any, error) {
	p, err := mcp.Args[struct {
		Session string `json:"session"`
	}](raw)
	if err != nil {
		return nil, err
	}
	sess, err := s.other(ctx, p.Session)
	if err != nil {
		return nil, err
	}
	if err := s.orch.Interrupt(ctx, sess.ID, false); err != nil {
		return nil, err
	}
	return "Stopped.", nil
}

func (s *Server) toolRenameSession(ctx context.Context, raw json.RawMessage) (any, error) {
	p, err := mcp.Args[struct {
		Session string `json:"session"`
		Title   string `json:"title"`
	}](raw)
	if err != nil {
		return nil, err
	}
	sess, err := s.findSession(ctx, p.Session)
	if err != nil {
		return nil, err
	}
	if sess, err = s.orch.Rename(ctx, sess.ID, p.Title); err != nil {
		return nil, err
	}
	projects, err := s.projectMap(ctx)
	return summarize(sess, projects), err
}

func (s *Server) toolForkSession(ctx context.Context, raw json.RawMessage) (any, error) {
	p, err := mcp.Args[struct {
		agentArgs
		Session string `json:"session"`
		Turn    int    `json:"turn"`
		Prompt  string `json:"prompt"`
	}](raw)
	if err != nil {
		return nil, err
	}
	src, err := s.findSession(ctx, p.Session)
	if err != nil {
		return nil, err
	}
	args := p.agentArgs
	if args.Agent == "" && callerOf(ctx).session == "" {
		args.Agent = src.Harness
	}
	agent, mode, err := s.agent(ctx, args)
	if err != nil {
		return nil, err
	}
	if agent == src.Harness && p.Model == "" && p.Effort == "" {
		p.Model, p.Effort = src.Model, src.Effort // as the session it forks
	}
	fp := orchestrator.ForkParams{CommandID: store.NewID(), SessionID: src.ID, Turn: p.Turn, Harness: agent, Model: p.Model,
		Effort: p.Effort, Mode: mode, Prompt: p.Prompt, StartedBy: callerOf(ctx).session}
	switch {
	case p.Worktree == nil:
		// As the session it forks.
	case *p.Worktree:
		fp.Workspace = &orchestrator.WorkspaceParams{Kind: model.WorkspaceWorktree, Branch: p.Branch}
	default:
		fp.Workspace = &orchestrator.WorkspaceParams{Kind: model.WorkspaceRoot}
	}
	sess, err := s.orch.Fork(ctx, fp)
	if err != nil {
		return nil, err
	}
	return s.started(ctx, sess)
}
