package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"remote-agent/internal/config"
	"remote-agent/internal/model"
	"remote-agent/internal/netinfo"
	"remote-agent/internal/store"
)

const debugUsage = `rad debug — test client

  rad debug token                         print a device token for this machine
  rad debug call <method> [json-params]   send one RPC and print the result
  rad debug run [flags] <prompt>          create a session, stream it, answer approvals
      --harness ID      harness id (default "fake")
      --cwd DIR         project directory (default ".")
      --mode MODE       permission mode
      --model MODEL
      --worktree        run in a new git worktree
      --approve         auto-approve (first allow option)
      --deny            auto-deny
      --diff            print the turn diff at the end
  rad debug prompt <sessionId> <prompt>   send a follow-up prompt and stream until idle
`

type client struct {
	ws      *websocket.Conn
	next    atomic.Int64
	mu      sync.Mutex
	pending map[int64]chan rpcResp
	notes   chan rpcNote
}

type rpcResp struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type rpcNote struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func debugToken(cfg *config.Config) (string, error) {
	path := filepath.Join(cfg.DataDir, "debug-token")
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
	_, tok, err := st.AddDevice(context.Background(), "rad debug")
	if err != nil {
		return "", err
	}
	return tok, os.WriteFile(path, []byte(tok), 0o600)
}

func dial(ctx context.Context, cfg *config.Config) (*client, error) {
	tok, err := debugToken(cfg)
	if err != nil {
		return nil, err
	}
	urls := netinfo.BaseURLs(netinfo.ListenAddrs(cfg))
	base := urls[len(urls)-1] // prefer loopback
	if u := os.Getenv("RAD_URL"); u != "" {
		base = u
	}
	ws, _, err := websocket.Dial(ctx, strings.Replace(base, "http", "ws", 1)+"/v1/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + tok}},
	})
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w (is `rad serve` running?)", base, err)
	}
	ws.SetReadLimit(64 << 20)
	c := &client{ws: ws, pending: map[int64]chan rpcResp{}, notes: make(chan rpcNote, 1024)}
	go c.read()
	return c, nil
}

func (c *client) read() {
	defer close(c.notes)
	for {
		_, data, err := c.ws.Read(context.Background())
		if err != nil {
			return
		}
		var f struct {
			ID *int64 `json:"id"`
			rpcResp
			rpcNote
		}
		if json.Unmarshal(data, &f) != nil {
			continue
		}
		if f.ID != nil {
			c.mu.Lock()
			ch := c.pending[*f.ID]
			delete(c.pending, *f.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- f.rpcResp
			}
			continue
		}
		c.notes <- f.rpcNote
	}
}

func (c *client) call(ctx context.Context, method string, params any, out any) error {
	id := c.next.Add(1)
	ch := make(chan rpcResp, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err := c.ws.Write(ctx, websocket.MessageText, b); err != nil {
		return err
	}
	select {
	case r := <-ch:
		if r.Error != nil {
			return fmt.Errorf("%s: %s", r.Error.Code, r.Error.Message)
		}
		if out != nil {
			return json.Unmarshal(r.Result, out)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func debug(args []string) error {
	if len(args) == 0 {
		fmt.Print(debugUsage)
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	switch args[0] {
	case "token":
		tok, err := debugToken(cfg)
		fmt.Println(tok)
		return err
	case "call":
		if len(args) < 2 {
			return errors.New("usage: rad debug call <method> [json]")
		}
		var params any = map[string]any{}
		if len(args) > 2 {
			if err := json.Unmarshal([]byte(args[2]), &params); err != nil {
				return fmt.Errorf("params: %w", err)
			}
		}
		c, err := dial(ctx, cfg)
		if err != nil {
			return err
		}
		var out json.RawMessage
		if err := c.call(ctx, args[1], params, &out); err != nil {
			return err
		}
		pretty, _ := json.MarshalIndent(json.RawMessage(out), "", "  ")
		fmt.Println(string(pretty))
		return nil
	case "run":
		return debugRun(ctx, cfg, args[1:])
	case "prompt":
		if len(args) < 3 {
			return errors.New("usage: rad debug prompt <sessionId> <prompt>")
		}
		c, err := dial(ctx, cfg)
		if err != nil {
			return err
		}
		return streamSession(ctx, c, args[1], func() error {
			return c.call(ctx, "session.prompt", map[string]any{"sessionId": args[1], "text": strings.Join(args[2:], " "), "commandId": store.NewID()}, nil)
		}, approvalPolicy{})
	}
	fmt.Print(debugUsage)
	return nil
}

type approvalPolicy struct{ approve, deny bool }

func debugRun(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	harnessID := fs.String("harness", "fake", "")
	cwd := fs.String("cwd", ".", "")
	mode := fs.String("mode", "", "")
	modelID := fs.String("model", "", "")
	worktree := fs.Bool("worktree", false, "")
	approve := fs.Bool("approve", false, "")
	deny := fs.Bool("deny", false, "")
	diff := fs.Bool("diff", false, "")
	fs.Parse(args)
	prompt := strings.Join(fs.Args(), " ")
	if prompt == "" {
		return errors.New("missing prompt")
	}
	dir, err := filepath.Abs(*cwd)
	if err != nil {
		return err
	}
	c, err := dial(ctx, cfg)
	if err != nil {
		return err
	}
	var proj model.Project
	if err := c.call(ctx, "project.add", map[string]any{"path": dir}, &proj); err != nil {
		return err
	}
	ws := map[string]any{"kind": "root"}
	if *worktree {
		ws["kind"] = "worktree"
	}
	var sess model.Session
	if err := c.call(ctx, "session.create", map[string]any{
		"commandId": store.NewID(), "projectId": proj.ID, "harness": *harnessID, "mode": *mode, "model": *modelID, "workspace": ws,
	}, &sess); err != nil {
		return err
	}
	fmt.Printf("session %s (%s, mode %s) in %s\n", sess.ID, sess.Harness, sess.Mode, sess.Workspace.Path)
	err = streamSession(ctx, c, sess.ID, func() error {
		return c.call(ctx, "session.prompt", map[string]any{"sessionId": sess.ID, "text": prompt, "commandId": store.NewID()}, nil)
	}, approvalPolicy{*approve, *deny})
	if err != nil {
		return err
	}
	if *diff {
		var res struct {
			Files []struct {
				Path      string `json:"path"`
				Status    string `json:"status"`
				Additions int    `json:"additions"`
				Deletions int    `json:"deletions"`
			} `json:"files"`
			Patch string `json:"patch"`
		}
		if err := c.call(ctx, "git.sessionDiff", map[string]any{"sessionId": sess.ID}, &res); err != nil {
			return err
		}
		fmt.Println("── diff ──")
		for _, f := range res.Files {
			fmt.Printf("%-9s %s (+%d -%d)\n", f.Status, f.Path, f.Additions, f.Deletions)
		}
		fmt.Print(res.Patch)
	}
	return nil
}

// streamSession subscribes to a session, runs start, and prints events until
// the turn ends.
func streamSession(ctx context.Context, c *client, sessionID string, start func() error, pol approvalPolicy) error {
	stream := model.SessionStream(sessionID)
	if err := c.call(ctx, "subscribe", map[string]any{"stream": stream, "afterSeq": 0}, nil); err != nil {
		return err
	}
	// Skip the snapshot; print only what happens from now on.
	synced := false
	started := false
	sawTurn := false
	printed := map[string]string{}
	stdin := bufio.NewReader(os.Stdin)
	for n := range c.notes {
		switch n.Method {
		case "synchronized":
			if !synced {
				synced = true
				if err := start(); err != nil {
					return err
				}
				started = true
			}
		case "resync":
			return errors.New("server asked to resync")
		case "events":
			var p struct {
				Events []model.Event `json:"events"`
			}
			json.Unmarshal(n.Params, &p)
			if !started {
				continue
			}
			for _, ev := range p.Events {
				switch {
				case ev.Turn != nil:
					t := ev.Turn
					if t.Status == model.TurnRunning {
						sawTurn = true
						fmt.Printf("── turn %d started ──\n", t.N)
					} else {
						usage := ""
						if t.Usage != nil {
							usage = fmt.Sprintf(" in=%d out=%d $%.4f", t.Usage.InputTokens, t.Usage.OutputTokens, t.Usage.CostUSD)
						}
						fmt.Printf("── turn %d %s%s %s ──\n", t.N, t.Status, usage, t.Error)
					}
				case ev.Session != nil:
					s := ev.Session
					if sawTurn && (s.Status == model.SessionIdle || s.Status == model.SessionError) {
						if s.Error != "" {
							fmt.Println("session error:", s.Error)
						}
						return nil
					}
				case ev.Item != nil:
					printItem(ev.Item, printed)
					if ev.Item.Kind == model.ItemApproval && ev.Item.Status == model.ItemPending {
						if err := answer(ctx, c, sessionID, ev.Item, pol, stdin); err != nil {
							fmt.Println("respond:", err)
						}
					}
				}
			}
		}
	}
	return errors.New("connection closed")
}

func printItem(it *model.Item, printed map[string]string) {
	switch it.Kind {
	case model.ItemAssistantMessage, model.ItemReasoning:
		prev := printed[it.ID]
		if it.Text == "" {
			return
		}
		if strings.HasPrefix(it.Text, prev) {
			if prev == "" {
				label := "assistant"
				if it.Kind == model.ItemReasoning {
					label = "thinking"
				}
				fmt.Printf("[%s] ", label)
			}
			fmt.Print(it.Text[len(prev):])
		} else {
			fmt.Printf("\n[%s] %s", it.Kind, it.Text)
		}
		printed[it.ID] = it.Text
		if it.Status != model.ItemInProgress && printed[it.ID+"/done"] == "" {
			printed[it.ID+"/done"] = "1"
			fmt.Println()
		}
	case model.ItemToolCall:
		key := string(it.Status)
		if printed[it.ID] == key {
			return
		}
		printed[it.ID] = key
		title := it.Tool.Title
		if title == "" {
			title = it.Tool.Name
		}
		out := ""
		if it.Status == model.ItemCompleted || it.Status == model.ItemFailed {
			out = strings.TrimSpace(it.Tool.Output)
			if len(out) > 300 {
				out = out[:300] + "…"
			}
			if out != "" {
				out = "\n    " + strings.ReplaceAll(out, "\n", "\n    ")
			}
		}
		fmt.Printf("[tool %s] %s%s\n", it.Status, title, out)
	case model.ItemApproval:
		if printed[it.ID] == string(it.Status) {
			return
		}
		printed[it.ID] = string(it.Status)
		fmt.Printf("[approval %s] %s %s\n", it.Status, it.Approval.Title, it.Approval.Detail)
	case model.ItemUserMessage:
		if printed[it.ID] == "" {
			printed[it.ID] = "1"
			fmt.Printf("[user] %s\n", it.Text)
		}
	case model.ItemPlan:
		fmt.Printf("[plan]")
		for _, e := range it.Plan.Entries {
			fmt.Printf("\n  [%s] %s", e.Status, e.Content)
		}
		if it.Plan.Text != "" {
			fmt.Printf(" %s", it.Plan.Text)
		}
		fmt.Println()
	default:
		fmt.Printf("[%s] %s\n", it.Kind, it.Text)
	}
}

func answer(ctx context.Context, c *client, sessionID string, it *model.Item, pol approvalPolicy, stdin *bufio.Reader) error {
	ap := it.Approval
	if len(ap.Input) > 0 {
		in := string(ap.Input)
		if len(in) > 500 {
			in = in[:500] + "…"
		}
		fmt.Println("    input:", in)
	}
	pick := func(kind model.OptionKind) string {
		for _, o := range ap.Options {
			if o.Kind == kind {
				return o.ID
			}
		}
		return ""
	}
	answers := map[string]string{}
	for _, q := range ap.Questions {
		if len(q.Options) > 0 {
			answers[q.Question] = q.Options[0].Label
		}
	}
	var opt string
	switch {
	case pol.approve:
		opt = pick(model.OptionAllowOnce)
	case pol.deny:
		opt = pick(model.OptionDeny)
	default:
		for i, o := range ap.Options {
			fmt.Printf("    %d) %s\n", i+1, o.Label)
		}
		fmt.Print("    choose: ")
		line, _ := stdin.ReadString('\n')
		var i int
		fmt.Sscanf(strings.TrimSpace(line), "%d", &i)
		if i < 1 || i > len(ap.Options) {
			i = 1
		}
		opt = ap.Options[i-1].ID
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return c.call(ctx, "approval.respond", map[string]any{
		"commandId": store.NewID(), "sessionId": sessionID, "approvalId": it.ID, "optionId": opt, "answers": answers,
	}, nil)
}
