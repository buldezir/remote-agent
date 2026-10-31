// Package mcp is a small Model Context Protocol server: it answers MCP's
// JSON-RPC messages with tools, and nothing else (no resources, prompts or
// sampling). It knows no transport: Handle takes one message and returns the
// reply, which internal/api serves over Streamable HTTP and `rad mcp` over
// stdio.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// Versions are the protocol versions the server speaks, newest first.
var Versions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// Tool is one tool. Call gets its arguments as JSON; a string result is the
// tool's text, anything else goes as JSON text and structured content. An
// error is the tool's (isError), which the agent reads, not a protocol error.
type Tool struct {
	Name        string
	Title       string
	Description string
	Input       map[string]any // a JSON schema of the arguments, an object
	ReadOnly    bool
	Destructive bool
	Call        func(ctx context.Context, args json.RawMessage) (any, error)
}

type Server struct {
	Name, Version string
	Instructions  string // for the agent, with the initialize result
	Tools         []Tool
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSON-RPC's error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// Handle answers one JSON-RPC message. It returns nil for notifications and
// for responses, which need no answer.
func (s *Server) Handle(ctx context.Context, raw []byte) []byte {
	var m message
	if err := json.Unmarshal(raw, &m); err != nil {
		return reply(json.RawMessage("null"), nil, &rpcError{codeParse, "parse error: " + err.Error()})
	}
	if len(m.ID) == 0 || string(m.ID) == "null" {
		return nil // a notification, such as notifications/initialized
	}
	if m.Method == "" {
		return nil // a response to a request of ours; we send none
	}
	result, err := s.call(ctx, m.Method, m.Params)
	return reply(m.ID, result, err)
}

func reply(id json.RawMessage, result any, err *rpcError) []byte {
	m := message{JSONRPC: "2.0", ID: id, Result: result, Error: err}
	if err == nil && result == nil {
		m.Result = struct{}{}
	}
	out, _ := json.Marshal(m)
	return out
}

func (s *Server) call(ctx context.Context, method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(params, &p)
		version := Versions[0]
		if slices.Contains(Versions, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		res := map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": s.Name, "version": s.Version},
		}
		if s.Instructions != "" {
			res["instructions"] = s.Instructions
		}
		return res, nil
	case "ping":
		return nil, nil
	case "tools/list":
		tools := make([]map[string]any, 0, len(s.Tools))
		for _, t := range s.Tools {
			d := map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.Input,
				"annotations": map[string]any{"readOnlyHint": t.ReadOnly, "destructiveHint": t.Destructive, "openWorldHint": false}}
			if t.Title != "" {
				d["title"] = t.Title
			}
			tools = append(tools, d)
		}
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &rpcError{codeInvalidParams, "bad params: " + err.Error()}
		}
		i := slices.IndexFunc(s.Tools, func(t Tool) bool { return t.Name == p.Name })
		if i < 0 {
			return nil, &rpcError{codeInvalidParams, fmt.Sprintf("unknown tool %q", p.Name)}
		}
		if len(p.Arguments) == 0 || string(p.Arguments) == "null" {
			p.Arguments = json.RawMessage("{}")
		}
		return toolResult(s.Tools[i].Call(ctx, p.Arguments)), nil
	}
	if method == "" {
		return nil, &rpcError{codeInvalidRequest, "no method"}
	}
	return nil, &rpcError{codeMethodNotFound, "unknown method " + method}
}

func toolResult(v any, err error) map[string]any {
	if err != nil {
		return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": err.Error()}}}
	}
	if text, ok := v.(string); ok {
		return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return toolResult(nil, err)
	}
	res := map[string]any{"content": []map[string]any{{"type": "text", "text": string(data)}}}
	// Structured content has to be an object.
	var obj map[string]any
	if json.Unmarshal(data, &obj) == nil {
		res["structuredContent"] = obj
	}
	return res
}

// Args decodes a tool's arguments into T, as a tool error the agent can
// correct when they don't fit.
func Args[T any](raw json.RawMessage) (T, error) {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) && te.Field != "" {
			return v, fmt.Errorf("argument %s: want %s", te.Field, te.Type)
		}
		return v, fmt.Errorf("bad arguments: %w", err)
	}
	return v, nil
}
