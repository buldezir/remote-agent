package fake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"remote-agent/internal/harness"
	"remote-agent/internal/model"
)

// The fake's prompt "delegate" starts a session with rad's MCP tools, as an
// agent hands work to another: it calls start_session at rad's MCP server,
// and shows the call as Claude Code does, mcp__rad__start_session. The new
// session reports back when its turn ends (orchestrator/reports.go).

// delegatePrompt is the new session's request: it holds no keyword, or its
// report, which quotes the reply, would start one session after another.
const delegatePrompt = "Look into the logs, please, and say what you find."

// With wait, it waits for the session with wait_for_session, which then
// tells it how the turn ended, rather than a report.
func (r *runtime) delegate(ctx context.Context, key func(string) string, wait bool) {
	out, ok := r.radTool(ctx, key("delegate"), "start_session",
		map[string]any{"project": r.opts.Cwd, "prompt": delegatePrompt, "title": "Look into the logs"})
	var started struct {
		ID string `json:"id"`
	}
	if !ok || !wait || json.Unmarshal([]byte(out), &started) != nil {
		return
	}
	r.radTool(ctx, key("wait"), "wait_for_session", map[string]any{"session": started.ID})
}

// radTool calls one of rad's tools, shown as a tool call as Claude Code
// shows it, and returns its answer.
func (r *runtime) radTool(ctx context.Context, id, tool string, args map[string]any) (string, bool) {
	input, _ := json.Marshal(args)
	name := "mcp__" + harness.MCPName + "__" + tool
	it := model.Item{ID: id, Kind: model.ItemToolCall, Status: model.ItemInProgress,
		Tool: &model.ToolCall{Name: name, Kind: model.ToolOther, Title: name, Input: input}}
	r.emitItem(it)
	out, err := callMCP(ctx, r.opts.MCP, tool, args)
	it.Status, it.Tool.Output = model.ItemCompleted, out
	if err != nil {
		it.Status, it.Tool.Output = model.ItemFailed, err.Error()
	}
	r.emitItem(it)
	return out, err == nil
}

// callMCP calls one of the tools of rad's MCP server, and returns its text.
func callMCP(ctx context.Context, m *harness.MCPServer, tool string, args any) (string, error) {
	if m == nil || m.URL == "" {
		return "", errors.New("rad's MCP server isn't reachable here")
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args}})
	req, err := http.NewRequestWithContext(ctx, "POST", m.URL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var res struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	if res.Error != nil {
		return "", errors.New(res.Error.Message)
	}
	text := ""
	if len(res.Result.Content) > 0 {
		text = res.Result.Content[0].Text
	}
	if res.Result.IsError {
		return "", errors.New(text)
	}
	return text, nil
}
