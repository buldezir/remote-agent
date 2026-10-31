package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

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
	if got := strings.Join(names, " "); got != "list_projects list_agents list_sessions start_session send_prompt read_session wait_for_session stop_session rename_session fork_session" {
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
