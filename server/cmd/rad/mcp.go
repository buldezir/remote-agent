package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"remote-agent/internal/config"
	"remote-agent/internal/harness"
	"remote-agent/internal/netinfo"
)

const mcpUsage = `rad mcp — rad's tools for an agent, as an MCP server on stdio

Agents that rad runs get the tools by themselves. To give them to an agent
you run yourself, add this command as one of its MCP servers, such as
  claude mcp add rad -- rad mcp
  codex mcp add rad -- rad mcp
The tools start and drive sessions on the rad that serves this config, which
must be running. The first run adds a device, "rad mcp", which rad devices
lists and revokes.
`

// mcpURL is rad's MCP server on its loopback address, or the address it
// listens on.
func mcpURL(cfg *config.Config) string {
	urls := netinfo.BaseURLs(cfg)
	if len(urls) == 0 {
		return fmt.Sprintf("http://127.0.0.1:%d/v1/mcp", cfg.Port)
	}
	return urls[len(urls)-1] + "/v1/mcp"
}

// mcpBridge passes MCP's messages between stdio and rad's MCP server over
// HTTP. An agent that rad runs starts it with the URL and its session's
// token in the environment; otherwise it is a device of its own.
func mcpBridge(args []string) error {
	if len(args) > 0 {
		fmt.Fprint(os.Stderr, mcpUsage)
		if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
			return nil
		}
		os.Exit(2)
	}
	url, token := os.Getenv(harness.MCPURLEnv), os.Getenv(harness.MCPTokenEnv)
	if url == "" || token == "" {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if url == "" {
			url = mcpURL(cfg)
		}
		if token == "" {
			if token, err = mcpToken(cfg); err != nil {
				return err
			}
		}
	}
	return bridge(context.Background(), os.Stdin, os.Stdout, url, token, http.DefaultClient)
}

// mcpToken is the token of the device `rad mcp` is, which it adds the first
// time, as `rad debug` does.
func mcpToken(cfg *config.Config) (string, error) {
	path := filepath.Join(cfg.DataDir, "mcp-token")
	st, err := openStore(cfg)
	if err != nil {
		return "", err
	}
	defer st.Close()
	if b, err := os.ReadFile(path); err == nil {
		tok := strings.TrimSpace(string(b))
		if _, err := st.DeviceByToken(context.Background(), tok); err == nil {
			return tok, nil
		}
	}
	_, tok, err := st.AddDevice(context.Background(), "rad mcp")
	if err != nil {
		return "", err
	}
	return tok, os.WriteFile(path, []byte(tok), 0o600)
}

// bridge posts each line of in, a JSON-RPC message, to url, and writes the
// replies to out, a line each. Requests run side by side, since waiting on a
// session can take long.
func bridge(ctx context.Context, in io.Reader, out io.Writer, url, token string, hc *http.Client) error {
	var mu sync.Mutex
	write := func(b []byte) {
		mu.Lock()
		defer mu.Unlock()
		out.Write(append(bytes.TrimSpace(b), '\n'))
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		msg := bytes.Clone(line)
		wg.Go(func() {
			reply, err := post(ctx, hc, url, token, msg)
			if err != nil {
				reply = errorReply(msg, err)
			}
			if len(reply) > 0 {
				write(reply)
			}
		})
	}
	return sc.Err()
}

func post(ctx context.Context, hc *http.Client, url, token string, msg []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(msg))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rad isn't reachable at %s (is `rad serve` running?): %w", url, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	switch {
	case res.StatusCode == http.StatusAccepted:
		return nil, nil
	case res.StatusCode != http.StatusOK:
		var e struct {
			Message string `json:"message"`
		}
		json.Unmarshal(body, &e)
		return nil, fmt.Errorf("rad answered %s: %s", res.Status, e.Message)
	}
	return body, nil
}

// errorReply is a JSON-RPC error for msg, if it is a request.
func errorReply(msg []byte, err error) []byte {
	var m struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(msg, &m) != nil || len(m.ID) == 0 || string(m.ID) == "null" {
		return nil
	}
	var reply struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	reply.JSONRPC, reply.ID = "2.0", m.ID
	reply.Error.Code, reply.Error.Message = -32603, err.Error()
	if errors.Is(err, context.Canceled) {
		reply.Error.Message = "cancelled"
	}
	out, _ := json.Marshal(reply)
	return out
}
