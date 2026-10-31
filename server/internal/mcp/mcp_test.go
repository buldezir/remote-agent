package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func testServer() *Server {
	return &Server{Name: "rad", Version: "1.2.3", Instructions: "Use the tools.", Tools: []Tool{
		{Name: "echo", Description: "Echoes", ReadOnly: true, Input: map[string]any{"type": "object"},
			Call: func(_ context.Context, args json.RawMessage) (any, error) {
				p, err := Args[struct {
					Text string `json:"text"`
				}](args)
				return p.Text, err
			}},
		{Name: "obj", Description: "An object", Input: map[string]any{"type": "object"},
			Call: func(context.Context, json.RawMessage) (any, error) { return map[string]int{"n": 1}, nil }},
		{Name: "list", Description: "A list", Input: map[string]any{"type": "object"},
			Call: func(context.Context, json.RawMessage) (any, error) { return []int{1, 2}, nil }},
		{Name: "fail", Description: "Fails", Input: map[string]any{"type": "object"}, Destructive: true,
			Call: func(context.Context, json.RawMessage) (any, error) { return nil, errors.New("no such session") }},
	}}
}

func call(t *testing.T, s *Server, msg string) map[string]any {
	t.Helper()
	out := s.Handle(context.Background(), []byte(msg))
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("reply %q: %v", out, err)
	}
	if m["jsonrpc"] != "2.0" {
		t.Errorf("reply %s has no jsonrpc", out)
	}
	return m
}

func TestInitialize(t *testing.T) {
	s := testServer()
	m := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{}}}`)
	res := m["result"].(map[string]any)
	if res["protocolVersion"] != "2025-06-18" || res["instructions"] != "Use the tools." ||
		res["serverInfo"].(map[string]any)["version"] != "1.2.3" || res["capabilities"].(map[string]any)["tools"] == nil {
		t.Errorf("initialize = %v", res)
	}
	// A version it doesn't know gets its newest.
	m = call(t, s, `{"jsonrpc":"2.0","id":"a","method":"initialize","params":{"protocolVersion":"2099-01-01"}}`)
	if m["id"] != "a" || m["result"].(map[string]any)["protocolVersion"] != Versions[0] {
		t.Errorf("initialize = %v", m)
	}
	if out := s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); out != nil {
		t.Errorf("a notification got %s", out)
	}
	if m := call(t, s, `{"jsonrpc":"2.0","id":2,"method":"ping"}`); m["result"] == nil || m["error"] != nil {
		t.Errorf("ping = %v", m)
	}
}

func TestTools(t *testing.T) {
	s := testServer()
	tools := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 4 {
		t.Fatalf("tools = %v", tools)
	}
	echo := tools[0].(map[string]any)
	if echo["name"] != "echo" || echo["annotations"].(map[string]any)["readOnlyHint"] != true || echo["inputSchema"] == nil {
		t.Errorf("echo = %v", echo)
	}

	res := call(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`)["result"].(map[string]any)
	if text := res["content"].([]any)[0].(map[string]any)["text"]; text != "hi" || res["isError"] != nil {
		t.Errorf("echo = %v", res)
	}
	res = call(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"obj"}}`)["result"].(map[string]any)
	if res["structuredContent"].(map[string]any)["n"] != 1.0 || !strings.Contains(res["content"].([]any)[0].(map[string]any)["text"].(string), `"n": 1`) {
		t.Errorf("obj = %v", res)
	}
	if res = call(t, s, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"list"}}`)["result"].(map[string]any); res["structuredContent"] != nil {
		t.Errorf("a list went as structured content: %v", res)
	}
	res = call(t, s, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"fail"}}`)["result"].(map[string]any)
	if res["isError"] != true || res["content"].([]any)[0].(map[string]any)["text"] != "no such session" {
		t.Errorf("fail = %v", res)
	}
	res = call(t, s, `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"echo","arguments":{"text":5}}}`)["result"].(map[string]any)
	if res["isError"] != true || res["content"].([]any)[0].(map[string]any)["text"] != "argument text: want string" {
		t.Errorf("bad arguments = %v", res)
	}
}

func TestErrors(t *testing.T) {
	s := testServer()
	for msg, code := range map[string]float64{
		`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`:                      codeMethodNotFound,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"nope"}}`: codeInvalidParams,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":["x"]}}`:  codeInvalidParams,
		`{not json`: codeParse,
	} {
		if m := call(t, s, msg); m["error"] == nil || m["error"].(map[string]any)["code"] != code {
			t.Errorf("%s = %v, want code %v", msg, m, code)
		}
	}
	if out := s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":9,"result":{}}`)); out != nil {
		t.Errorf("a response got %s", out)
	}
}
