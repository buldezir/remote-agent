package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"remote-agent/internal/harness/fake"
	"remote-agent/internal/model"
)

// mcpClient calls the server's MCP tools with a token: a device's, or a
// session's agent's.
type mcpClient struct {
	t     *testing.T
	s     *testServer
	token string
	next  atomic.Int64
}

func (s *testServer) mcp(t *testing.T, token string) *mcpClient {
	return &mcpClient{t: t, s: s, token: token}
}

func (c *mcpClient) rpc(method string, params any) map[string]any {
	c.t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.next.Add(1), "method": method, "params": params})
	resp, data := c.s.request(c.t, "POST", "/v1/mcp", c.token, body)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
		c.t.Fatalf("%s = %d %s", method, resp.StatusCode, data)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil || m["error"] != nil {
		c.t.Fatalf("%s = %s", method, data)
	}
	return m["result"].(map[string]any)
}

// tool calls a tool, and returns its text, and whether it failed.
func (c *mcpClient) tool(name string, args map[string]any) (string, bool) {
	c.t.Helper()
	res := c.rpc("tools/call", map[string]any{"name": name, "arguments": args})
	return res["content"].([]any)[0].(map[string]any)["text"].(string), res["isError"] == true
}

// ok calls a tool that has to work, and decodes its JSON result into out.
func (c *mcpClient) ok(name string, args map[string]any, out any) string {
	c.t.Helper()
	text, failed := c.tool(name, args)
	if failed {
		c.t.Fatalf("%s(%v) failed: %s", name, args, text)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(text), out); err != nil {
			c.t.Fatalf("%s: %v\n%s", name, err, text)
		}
	}
	return text
}

func (c *mcpClient) fails(name string, args map[string]any, want string) {
	c.t.Helper()
	text, failed := c.tool(name, args)
	if !failed || !strings.Contains(text, want) {
		c.t.Errorf("%s(%v) = %q (failed %v), want an error with %q", name, args, text, failed, want)
	}
}

type mcpSession struct {
	ID         string            `json:"id"`
	Title      string            `json:"title"`
	Agent      string            `json:"agent"`
	Model      string            `json:"model"`
	Mode       string            `json:"mode"`
	Status     string            `json:"status"`
	Worktree   bool              `json:"worktree"`
	ForkedFrom *model.ForkedFrom `json:"forkedFrom"`
	Draft      string            `json:"draft"`
	Archived   bool              `json:"archived"`
}

func TestMCPTransport(t *testing.T) {
	s := startServer(t)
	tok := s.token(t)
	init := s.mcp(t, tok).rpc("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}})
	if init["protocolVersion"] != "2025-06-18" || !strings.Contains(init["instructions"].(string), "start_session") {
		t.Errorf("initialize = %v", init)
	}
	tools := s.mcp(t, tok).rpc("tools/list", nil)["tools"].([]any)
	var names []string
	for _, tl := range tools {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	if got := strings.Join(names, " "); got != "list_projects list_agents list_sessions start_session send_prompt read_session wait_for_session stop_session rename_session fork_session "+
		"save_draft archive_session unarchive_session search_sessions read_diff list_branches" {
		t.Errorf("tools = %s", got)
	}

	resp, _ := s.request(t, "POST", "/v1/mcp", tok, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("notification = %d", resp.StatusCode)
	}
	if resp, _ := s.request(t, "GET", "/v1/mcp", tok, nil); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d, want no stream", resp.StatusCode)
	}
	for _, token := range []string{"", "nope"} {
		if resp, _ := s.request(t, "POST", "/v1/mcp", token, []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("token %q = %d", token, resp.StatusCode)
		}
	}
	req, _ := http.NewRequest("POST", s.url+"/v1/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Origin", "https://evil.example")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("a page elsewhere = %v, %v", resp, err)
	}
}

func TestMCPDrivesSessions(t *testing.T) {
	s := startServer(t)
	c := s.mcp(t, s.token(t))
	dir := filepath.Join(s.root, "app")
	os.MkdirAll(dir, 0o755)

	var agents struct {
		Agents []struct {
			ID          string `json:"id"`
			DefaultMode string `json:"defaultMode"`
		} `json:"agents"`
	}
	if c.ok("list_agents", nil, &agents); len(agents.Agents) != 1 || agents.Agents[0].ID != "fake" || agents.Agents[0].DefaultMode != "ask" {
		t.Errorf("agents = %+v", agents)
	}
	c.fails("start_session", map[string]any{"project": "/elsewhere", "prompt": "hi"}, "no project")
	c.fails("start_session", map[string]any{"project": dir, "prompt": " "}, "empty")
	c.fails("start_session", map[string]any{"project": dir, "prompt": "hi", "agent": "nope"}, "no agent")

	// A folder under the roots becomes a project; a device picks any mode.
	var started mcpSession
	c.ok("start_session", map[string]any{"project": dir, "prompt": "hello there", "mode": "auto", "title": "Helper"}, &started)
	if started.Agent != "fake" || started.Mode != "auto" || started.Title != "Helper" || started.Status != "running" {
		t.Errorf("started = %+v", started)
	}
	var projects struct {
		Projects []struct{ ID, Path string } `json:"projects"`
	}
	real, _ := filepath.EvalSymlinks(dir)
	if c.ok("list_projects", nil, &projects); len(projects.Projects) != 1 || projects.Projects[0].Path != real {
		t.Errorf("projects = %+v", projects)
	}

	var done struct {
		Done      bool   `json:"done"`
		Status    string `json:"status"`
		LastReply string `json:"lastReply"`
	}
	c.ok("wait_for_session", map[string]any{"session": started.ID[:8]}, &done)
	if !done.Done || done.Status != "idle" || done.LastReply != "Echo: hello there" {
		t.Errorf("wait = %+v", done)
	}
	if text := c.ok("send_prompt", map[string]any{"session": started.ID, "prompt": "and again"}, nil); !strings.HasPrefix(text, "Sent") {
		t.Errorf("send = %q", text)
	}
	c.ok("wait_for_session", map[string]any{"session": started.ID, "timeoutSeconds": 30}, &done)
	text := c.ok("read_session", map[string]any{"session": started.ID}, nil)
	if !strings.Contains(text, "“Helper”: idle, 2 turn(s)") || !strings.Contains(text, "User:\nand again") || strings.Contains(text, "hello there") {
		t.Errorf("read the last turn = %s", text)
	}
	if text := c.ok("read_session", map[string]any{"session": started.ID, "turns": 0}, nil); !strings.Contains(text, "User:\nhello there") {
		t.Errorf("read all = %s", text)
	}
	var sessions struct {
		Sessions []mcpSession `json:"sessions"`
	}
	if c.ok("list_sessions", map[string]any{"project": dir}, &sessions); len(sessions.Sessions) != 1 || sessions.Sessions[0].ID != started.ID {
		t.Errorf("sessions = %+v", sessions)
	}

	var renamed mcpSession
	if c.ok("rename_session", map[string]any{"session": started.ID[:8], "title": "  The helper\n "}, &renamed); renamed.Title != "The helper" {
		t.Errorf("renamed = %+v", renamed)
	}
	if got, _ := s.st.Session(context.Background(), started.ID); got.Title != "The helper" || !got.CustomTitle {
		t.Errorf("renamed session = %q, custom %v", got.Title, got.CustomTitle)
	}
	c.fails("rename_session", map[string]any{"session": "zzzzzzzz", "title": "x"}, "no session")

	var fork mcpSession
	c.ok("fork_session", map[string]any{"session": started.ID, "turn": 1, "prompt": "differently"}, &fork)
	if fork.ForkedFrom == nil || fork.ForkedFrom.Turn != 1 || fork.Agent != "fake" || fork.Mode != "auto" {
		t.Errorf("fork = %+v", fork)
	}
	c.ok("wait_for_session", map[string]any{"session": fork.ID}, &done)
	if !strings.HasPrefix(done.LastReply, "Forked fake-") {
		t.Errorf("fork's reply = %q", done.LastReply)
	}

	c.ok("send_prompt", map[string]any{"session": started.ID, "prompt": "slow and long"}, nil)
	if text := c.ok("send_prompt", map[string]any{"session": started.ID, "prompt": "and this too", "steer": true}, nil); !strings.HasPrefix(text, "Steered") {
		t.Errorf("steer = %q", text)
	}
	if text := c.ok("stop_session", map[string]any{"session": started.ID}, nil); text != "Stopped." {
		t.Errorf("stop = %q", text)
	}
	c.ok("wait_for_session", map[string]any{"session": started.ID}, &done)
	turns, _ := s.st.Turns(context.Background(), started.ID)
	if len(turns) != 3 || turns[2].Status != model.TurnInterrupted {
		t.Errorf("turns = %+v", turns)
	}
	c.fails("read_session", map[string]any{"session": "zzzzzzzz"}, "no session")
}

func TestMCPAgentLimits(t *testing.T) {
	s := startServer(t)
	dev := s.mcp(t, s.token(t))
	dir := filepath.Join(s.root, "app")
	os.MkdirAll(dir, 0o755)
	var parent mcpSession
	dev.ok("start_session", map[string]any{"project": dir, "prompt": "hello", "mode": "auto"}, &parent)
	dev.ok("wait_for_session", map[string]any{"session": parent.ID}, nil)

	// The agent of a session in auto mode: its own mode or the agent's default.
	agent := s.mcp(t, s.orch.AgentToken(parent.ID))
	var child mcpSession
	agent.ok("start_session", map[string]any{"project": dir, "prompt": "help me"}, &child)
	if child.Agent != "fake" || child.Mode != "auto" {
		t.Errorf("child = %+v", child)
	}
	agent.ok("wait_for_session", map[string]any{"session": child.ID}, nil)
	items, _ := s.st.Items(context.Background(), child.ID)
	if len(items) == 0 || items[0].Kind != model.ItemNotice || items[0].Text != fmt.Sprintf("Started by the agent of “%s”.", parent.Title) {
		t.Errorf("child's first item = %+v", items[0])
	}
	agent.fails("start_session", map[string]any{"project": dir, "prompt": "x", "mode": "yolo"}, "choose ask or auto")
	agent.fails("send_prompt", map[string]any{"session": parent.ID, "prompt": "loop"}, "your own session")
	agent.fails("wait_for_session", map[string]any{"session": parent.ID}, "your own session")
	// But it may name its own.
	var own mcpSession
	if agent.ok("rename_session", map[string]any{"session": parent.ID, "title": "Parent"}, &own); own.Title != "Parent" {
		t.Errorf("rename own = %+v", own)
	}

	// The child's agent, in ask mode, can't start one in auto.
	var asking mcpSession
	agent.ok("start_session", map[string]any{"project": dir, "prompt": "ask first", "mode": "ask"}, &asking)
	if asking.Mode != "ask" {
		t.Errorf("asking = %+v", asking)
	}
	agent.ok("wait_for_session", map[string]any{"session": asking.ID}, nil)
	s.mcp(t, s.orch.AgentToken(asking.ID)).fails("start_session", map[string]any{"project": dir, "prompt": "x", "mode": "auto"}, "choose ask")

	// Three deep at most.
	grand := s.mcp(t, s.orch.AgentToken(child.ID))
	var g mcpSession
	grand.ok("start_session", map[string]any{"project": dir, "prompt": "deeper"}, &g)
	s.mcp(t, s.orch.AgentToken(g.ID)).fails("start_session", map[string]any{"project": dir, "prompt": "deepest"}, "only 3 deep")

	// An archived session's agent has no tools.
	tok := s.orch.AgentToken(child.ID)
	if err := s.orch.Archive(context.Background(), child.ID, false); err != nil {
		t.Fatal(err)
	}
	if resp, _ := s.request(t, "POST", "/v1/mcp", tok, []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("archived session's token = %d", resp.StatusCode)
	}
}

// otherAgent is a second scripted agent, the fake one under another id.
type otherAgent struct{ fake.Harness }

func (otherAgent) ID() string { return "other" }

func (o otherAgent) Probe(ctx context.Context) model.HarnessInfo {
	info := o.Harness.Probe(ctx)
	info.ID, info.Name = "other", "Other"
	return info
}

// yoloAgent is the fake agent with a default that doesn't ask, as Claude
// Code's and Codex's Yolo, and an AgentMode that does.
type yoloAgent struct{ fake.Harness }

func (yoloAgent) ID() string { return "yolo" }

func (o yoloAgent) Probe(ctx context.Context) model.HarnessInfo {
	info := o.Harness.Probe(ctx)
	info.ID, info.Name, info.DefaultMode, info.AgentMode = "yolo", "Yolo", "auto", "ask"
	return info
}

// The sessions an agent starts take its own mode or the agent's AgentMode,
// not a default that asks less.
func TestMCPAgentMode(t *testing.T) {
	s := startServer(t, yoloAgent{})
	dev := s.mcp(t, s.token(t))
	dir := filepath.Join(s.root, "app")
	os.MkdirAll(dir, 0o755)
	var agents struct {
		Agents []struct{ ID, DefaultMode string } `json:"agents"`
	}
	dev.ok("list_agents", nil, &agents)
	for _, a := range agents.Agents {
		if a.ID == "yolo" && a.DefaultMode != "ask" {
			t.Errorf("list_agents' default mode = %s, want the one agents may choose", a.DefaultMode)
		}
	}
	start := func(c *mcpClient, args map[string]any) mcpSession {
		t.Helper()
		args["project"], args["agent"] = dir, "yolo"
		var sess mcpSession
		c.ok("start_session", args, &sess)
		c.ok("wait_for_session", map[string]any{"session": sess.ID}, nil)
		return sess
	}
	// A device's session gets the default.
	if top := start(dev, map[string]any{"prompt": "hello"}); top.Mode != "auto" {
		t.Errorf("device's session = %+v", top)
	} else if child := start(s.mcp(t, s.orch.AgentToken(top.ID)), map[string]any{"prompt": "help"}); child.Mode != "auto" {
		t.Errorf("an agent in the default gets its own mode: %+v", child)
	}
	asking := start(dev, map[string]any{"prompt": "hello", "mode": "ask"})
	agent := s.mcp(t, s.orch.AgentToken(asking.ID))
	if child := start(agent, map[string]any{"prompt": "help"}); child.Mode != "ask" {
		t.Errorf("an asking agent's child = %+v", child)
	}
	agent.fails("start_session", map[string]any{"project": dir, "agent": "yolo", "prompt": "x", "mode": "auto"}, "choose ask")
}

func TestMCPDefaultAgent(t *testing.T) {
	s := startServer(t, otherAgent{})
	ctx := context.Background()
	dev := s.mcp(t, s.token(t))
	dir := filepath.Join(s.root, "app")
	os.MkdirAll(dir, 0o755)
	// Without a default: Claude Code if it's there, else the first agent.
	var first mcpSession
	if dev.ok("start_session", map[string]any{"project": dir, "prompt": "hello"}, &first); first.Agent != "fake" {
		t.Errorf("without a default = %+v", first)
	}

	if _, err := s.orch.SetSettings(ctx, model.ServerSettings{DefaultAgent: &model.AgentSpec{Harness: "other", Model: "echo", Effort: "high"}}); err != nil {
		t.Fatal(err)
	}
	var agents struct {
		Default struct{ Agent, Model, Effort string } `json:"default"`
	}
	if dev.ok("list_agents", nil, &agents); agents.Default.Agent != "other" || agents.Default.Model != "echo" || agents.Default.Effort != "high" {
		t.Errorf("list_agents' default = %+v", agents.Default)
	}
	effort := func(id string) string {
		sess, err := s.st.Session(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return sess.Effort
	}
	for _, c := range []struct {
		args                 map[string]any
		agent, model, effort string
	}{
		{map[string]any{}, "other", "echo", "high"},
		{map[string]any{"agent": "other", "effort": "low"}, "other", "echo", "low"},
		{map[string]any{"agent": "other", "model": "echo"}, "other", "echo", "high"},
		{map[string]any{"agent": "other", "model": "custom"}, "other", "custom", ""},
		{map[string]any{"agent": "fake"}, "fake", "", ""},
	} {
		args := map[string]any{"project": dir, "prompt": "hello"}
		for k, v := range c.args {
			args[k] = v
		}
		var got mcpSession
		dev.ok("start_session", args, &got)
		if got.Agent != c.agent || got.Model != c.model || effort(got.ID) != c.effort {
			t.Errorf("start_session(%v) = %s %q %q, want %s %q %q", c.args, got.Agent, got.Model, effort(got.ID), c.agent, c.model, c.effort)
		}
	}

	// It wins over an agent's own, with that agent's default mode.
	dev.ok("wait_for_session", map[string]any{"session": first.ID}, nil)
	var child mcpSession
	s.mcp(t, s.orch.AgentToken(first.ID)).ok("start_session", map[string]any{"project": dir, "prompt": "help me"}, &child)
	if child.Agent != "other" || child.Model != "echo" || child.Mode != "ask" || effort(child.ID) != "high" {
		t.Errorf("child = %+v, effort %q", child, effort(child.ID))
	}
	// A fork with its source's agent goes on with the source's model.
	var fork mcpSession
	dev.ok("fork_session", map[string]any{"session": first.ID}, &fork)
	if fork.Agent != "fake" || fork.Model != "" {
		t.Errorf("fork = %+v", fork)
	}
}

func TestMCPDrafts(t *testing.T) {
	s := startServer(t)
	ctx := context.Background()
	dev := s.mcp(t, s.token(t))
	dir := filepath.Join(s.root, "app")
	os.MkdirAll(dir, 0o755)

	// A session started with a draft waits for the user to send it.
	var drafted mcpSession
	dev.ok("start_session", map[string]any{"project": dir, "prompt": "look at the logs", "draft": true}, &drafted)
	if drafted.Status != "idle" || drafted.Draft != "look at the logs" {
		t.Errorf("drafted = %+v", drafted)
	}
	if turns, _ := s.st.Turns(ctx, drafted.ID); len(turns) != 0 {
		t.Errorf("a draft started %d turn(s)", len(turns))
	}

	var started mcpSession
	dev.ok("start_session", map[string]any{"project": dir, "prompt": "hello"}, &started)
	dev.ok("wait_for_session", map[string]any{"session": started.ID}, nil)
	// An agent may save one in its own session, as in any.
	agent := s.mcp(t, s.orch.AgentToken(started.ID))
	if text := agent.ok("save_draft", map[string]any{"session": started.ID, "text": "next, the tests"}, nil); !strings.HasPrefix(text, "Saved") {
		t.Errorf("save = %q", text)
	}
	if got, _ := s.st.Session(ctx, started.ID); got.Draft == nil || got.Draft.Text != "next, the tests" {
		t.Errorf("draft = %+v", got.Draft)
	}
	if text := agent.ok("save_draft", map[string]any{"session": started.ID, "text": ""}, nil); text != "Removed the draft." {
		t.Errorf("remove = %q", text)
	}
	if got, _ := s.st.Session(ctx, started.ID); got.Draft != nil {
		t.Errorf("draft after removing = %+v", got.Draft)
	}

	var fork mcpSession
	dev.ok("fork_session", map[string]any{"session": started.ID, "prompt": "try it another way", "draft": true}, &fork)
	if fork.Draft != "try it another way" || fork.Status != "idle" {
		t.Errorf("fork = %+v", fork)
	}
	dev.fails("fork_session", map[string]any{"session": started.ID, "draft": true}, "needs a prompt")
}

func TestMCPArchive(t *testing.T) {
	s := startServer(t)
	dev := s.mcp(t, s.token(t))
	dir := filepath.Join(s.root, "app")
	os.MkdirAll(dir, 0o755)
	start := func(c *mcpClient, prompt string) mcpSession {
		t.Helper()
		var sess mcpSession
		c.ok("start_session", map[string]any{"project": dir, "prompt": prompt}, &sess)
		c.ok("wait_for_session", map[string]any{"session": sess.ID}, nil)
		return sess
	}
	parent, users := start(dev, "hello"), start(dev, "the user's own")
	agent := s.mcp(t, s.orch.AgentToken(parent.ID))
	child := start(agent, "help me")
	grandchild := start(s.mcp(t, s.orch.AgentToken(child.ID)), "help more")

	// An agent archives the sessions it started, theirs, and no others.
	agent.fails("archive_session", map[string]any{"session": parent.ID}, "your own session")
	agent.fails("archive_session", map[string]any{"session": users.ID}, "you didn't start")
	for _, id := range []string{grandchild.ID, child.ID} {
		if text := agent.ok("archive_session", map[string]any{"session": id}, nil); text != "Archived." {
			t.Errorf("archive = %q", text)
		}
	}
	var sessions struct {
		Sessions []mcpSession `json:"sessions"`
	}
	if dev.ok("list_sessions", nil, &sessions); len(sessions.Sessions) != 2 {
		t.Errorf("sessions left = %+v", sessions.Sessions)
	}
	dev.fails("send_prompt", map[string]any{"session": child.ID, "prompt": "more"}, "archived")

	var back mcpSession
	if agent.ok("unarchive_session", map[string]any{"session": child.ID}, &back); back.Archived || back.ID != child.ID {
		t.Errorf("unarchived = %+v", back)
	}
	dev.ok("send_prompt", map[string]any{"session": child.ID, "prompt": "more"}, nil)

	// A device archives any.
	if text := dev.ok("archive_session", map[string]any{"session": users.ID}, nil); text != "Archived." {
		t.Errorf("device's archive = %q", text)
	}
	if text := dev.ok("archive_session", map[string]any{"session": users.ID}, nil); !strings.Contains(text, "already") {
		t.Errorf("archive again = %q", text)
	}
}

func TestMCPSearch(t *testing.T) {
	s := startServer(t)
	dev := s.mcp(t, s.token(t))
	dir := filepath.Join(s.root, "app")
	os.MkdirAll(dir, 0o755)
	var a, b mcpSession
	dev.ok("start_session", map[string]any{"project": dir, "prompt": "fix the flaky checkout test"}, &a)
	dev.ok("start_session", map[string]any{"project": dir, "prompt": "write the release notes"}, &b)
	dev.ok("wait_for_session", map[string]any{"session": a.ID}, nil)
	dev.ok("wait_for_session", map[string]any{"session": b.ID}, nil)

	var found struct {
		Results []struct {
			Session mcpSession `json:"session"`
			Snippet string     `json:"snippet"`
			Matches int        `json:"matches"`
		} `json:"results"`
	}
	if dev.ok("search_sessions", map[string]any{"query": "flak"}, &found); len(found.Results) != 1 || found.Results[0].Session.ID != a.ID ||
		!strings.Contains(found.Results[0].Snippet, "**flaky**") {
		t.Errorf("found = %+v", found)
	}
	dev.fails("search_sessions", map[string]any{"query": " "}, "empty")

	dev.ok("archive_session", map[string]any{"session": a.ID}, nil)
	if dev.ok("search_sessions", map[string]any{"query": "flaky"}, &found); len(found.Results) != 0 {
		t.Errorf("found archived = %+v", found)
	}
	if dev.ok("search_sessions", map[string]any{"query": "flaky", "archived": true}, &found); len(found.Results) != 1 {
		t.Errorf("found with archived = %+v", found)
	}
}

func TestMCPDiffAndBranches(t *testing.T) {
	s := startServer(t)
	dev := s.mcp(t, s.token(t))
	dir := filepath.Join(s.root, "app")
	for _, args := range [][]string{{"init", "-q", "-b", "main", dir}, {"-C", dir, "commit", "-q", "--allow-empty", "-m", "init"},
		{"-C", dir, "branch", "feature"}} {
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	var sess mcpSession
	dev.ok("start_session", map[string]any{"project": dir, "prompt": "write a.txt", "mode": "auto", "worktree": true, "branch": "ra/notes"}, &sess)
	dev.ok("wait_for_session", map[string]any{"session": sess.ID}, nil)
	dev.ok("send_prompt", map[string]any{"session": sess.ID, "prompt": "write b.txt"}, nil)
	dev.ok("wait_for_session", map[string]any{"session": sess.ID}, nil)

	type diff struct {
		Files []struct {
			Path      string `json:"path"`
			Status    string `json:"status"`
			Additions int    `json:"additions"`
		} `json:"files"`
		Patch string `json:"patch"`
	}
	var all, second diff
	if dev.ok("read_diff", map[string]any{"session": sess.ID}, &all); len(all.Files) != 2 || !strings.Contains(all.Patch, "+hello from fake") {
		t.Errorf("session's diff = %+v", all)
	}
	if dev.ok("read_diff", map[string]any{"session": sess.ID, "turn": 2}, &second); len(second.Files) != 1 || second.Files[0].Path != "b.txt" ||
		second.Files[0].Status != "added" {
		t.Errorf("turn 2's diff = %+v", second)
	}
	dev.fails("read_diff", map[string]any{"session": sess.ID, "turn": 3}, "has 2 turn(s)")

	var branches struct {
		Branches  []string `json:"branches"`
		Current   string   `json:"current"`
		Worktrees []struct {
			Path, Branch string
		} `json:"worktrees"`
	}
	dev.ok("list_branches", map[string]any{"project": dir}, &branches)
	if branches.Current != "main" || !slices.Contains(branches.Branches, "feature") || !slices.Contains(branches.Branches, "ra/notes") ||
		len(branches.Worktrees) != 1 || branches.Worktrees[0].Branch != "ra/notes" {
		t.Errorf("branches = %+v", branches)
	}
	dev.fails("list_branches", map[string]any{"project": "/elsewhere"}, "no project")
}

// A turn an agent gave a session reports back to the agent's session when
// it ends, unless the agent waited for it, stopped it, or asked for none.
func TestMCPReports(t *testing.T) {
	s := startServer(t)
	ctx := context.Background()
	dev := s.mcp(t, s.token(t))
	dir := filepath.Join(s.root, "app")
	os.MkdirAll(dir, 0o755)
	var parent mcpSession
	dev.ok("start_session", map[string]any{"project": dir, "prompt": "hello", "mode": "auto"}, &parent)
	dev.ok("wait_for_session", map[string]any{"session": parent.ID}, nil)
	agent := s.mcp(t, s.orch.AgentToken(parent.ID))

	// reports are the parent's report items; it waits for n of them, and for
	// the parent's turns to end.
	reports := func(n int) []*model.Item {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			items, _ := s.st.Items(ctx, parent.ID)
			var out []*model.Item
			started := true
			for _, it := range items {
				if it.Report != nil {
					out = append(out, it)
					started = started && it.TurnID != ""
				}
			}
			sess, _ := s.st.Session(ctx, parent.ID)
			if len(out) >= n && started && sess.Status != model.SessionRunning || time.Now().After(deadline) {
				return out
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	var child mcpSession
	agent.ok("start_session", map[string]any{"project": dir, "prompt": "help me", "title": "Helper"}, &child)
	got := reports(1)
	if len(got) != 1 {
		t.Fatalf("reports = %d, want 1", len(got))
	}
	rep := got[0]
	if want := fmt.Sprintf("@[Helper](session:%s) finished.", child.ID); rep.Text != want || rep.TurnID == "" || rep.Status != model.ItemCompleted {
		t.Errorf("report = %q (%s, turn %q), want %q", rep.Text, rep.Status, rep.TurnID, want)
	}
	if rep.Report.SessionID != child.ID || rep.Report.Title != "Helper" || rep.Report.Status != model.TurnCompleted {
		t.Errorf("report = %+v", rep.Report)
	}
	// The agent read the session's reply in it: the fake echoes its prompt.
	items, _ := s.st.Items(ctx, parent.ID)
	last := items[len(items)-1]
	if last.Kind != model.ItemAssistantMessage || !strings.Contains(last.Text, "<rad-report>") || !strings.Contains(last.Text, "Echo: help me") {
		t.Errorf("parent's reply = %q", last.Text)
	}
	// read_session tells it from the user's prompts.
	if text := dev.ok("read_session", map[string]any{"session": parent.ID}, nil); !strings.Contains(text, "Report (from rad, not the user):\n@[Helper]") {
		t.Errorf("read = %s", text)
	}

	// Waiting for it, the agent hears it there.
	agent.ok("send_prompt", map[string]any{"session": child.ID, "prompt": "slow, please"}, nil)
	var done struct {
		Done bool `json:"done"`
	}
	if agent.ok("wait_for_session", map[string]any{"session": child.ID}, &done); !done.Done {
		t.Fatal("not done")
	}
	// With report false, it hears nothing.
	agent.ok("send_prompt", map[string]any{"session": child.ID, "prompt": "quietly", "report": false}, nil)
	dev.ok("wait_for_session", map[string]any{"session": child.ID}, nil)
	// Stopping it, it knows.
	agent.ok("send_prompt", map[string]any{"session": child.ID, "prompt": "slow again"}, nil)
	agent.ok("stop_session", map[string]any{"session": child.ID}, nil)
	dev.ok("wait_for_session", map[string]any{"session": child.ID}, nil)
	// A prompt the user sends reports to nobody.
	dev.ok("send_prompt", map[string]any{"session": child.ID, "prompt": "from the user"}, nil)
	dev.ok("wait_for_session", map[string]any{"session": child.ID}, nil)
	time.Sleep(200 * time.Millisecond)
	if got := reports(1); len(got) != 1 {
		t.Errorf("reports = %d, want still 1", len(got))
	}

	// Stopped by someone else, or failed, it says so.
	agent.ok("send_prompt", map[string]any{"session": child.ID, "prompt": "slow once more"}, nil)
	time.Sleep(100 * time.Millisecond)
	dev.ok("stop_session", map[string]any{"session": child.ID}, nil)
	agent.ok("send_prompt", map[string]any{"session": child.ID, "prompt": "fail"}, nil)
	got = reports(3)
	if len(got) != 3 || got[1].Report.Status != model.TurnInterrupted || !strings.HasSuffix(got[1].Text, "was stopped.") ||
		got[2].Report.Status != model.TurnFailed || !strings.Contains(got[2].Text, "failed") {
		for _, r := range got {
			t.Logf("report %q %+v", r.Text, r.Report)
		}
		t.Errorf("reports = %d, want stopped and failed", len(got))
	}

	// While the agent works, the report joins its turn.
	dev.ok("send_prompt", map[string]any{"session": parent.ID, "prompt": "slow, a long one: " + strings.Repeat("word ", 40)}, nil)
	agent.ok("send_prompt", map[string]any{"session": child.ID, "prompt": "quick"}, nil)
	got = reports(4)
	turns, _ := s.st.Turns(ctx, parent.ID)
	if len(got) != 4 || got[3].TurnID != turns[len(turns)-1].ID {
		t.Errorf("the report = %+v, want it in the parent's last turn %s", got[len(got)-1], turns[len(turns)-1].ID)
	}
	items, _ = s.st.Items(ctx, parent.ID)
	if last := items[len(items)-1]; !strings.Contains(last.Text, "Steered: <rad-report>") {
		t.Errorf("parent's reply = %q", last.Text)
	}
}
