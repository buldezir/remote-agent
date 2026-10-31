package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"remote-agent/internal/client/radtest"
	"remote-agent/internal/model"
	"remote-agent/internal/store"
)

// Tests against a rad running in the test, with the fake harness.

type testRad struct {
	*radtest.Rad
	url string
	st  *store.Store
	srv *httptest.Server
}

func startRad(t *testing.T) *testRad {
	r := radtest.Start(t)
	return &testRad{Rad: r, url: r.URL, st: r.Store, srv: r.Server}
}

// link makes a pairing link, as `rad pair --print-url` prints it.
func (r *testRad) link(t *testing.T) PairingLink {
	return PairingLink{Name: "test-host", Code: r.Code(t), URLs: []string{r.url}}
}

func (r *testRad) pair(t *testing.T) (SavedServer, string, *ServerStore) {
	t.Helper()
	ss := NewServerStore(t.TempDir())
	saved, token, err := PairWith(context.Background(), ss, r.link(t))
	if err != nil {
		t.Fatal(err)
	}
	return saved, token, ss
}

// connect pairs and starts a connection, and waits for its index.
func (r *testRad) connect(t *testing.T) (*ServerConnection, *Queue, *ServerStore) {
	t.Helper()
	saved, token, ss := r.pair(t)
	q := NewQueue()
	c := NewServerConnection(q, saved.URLs, token, ss.Drafts())
	c.FirstRetryDelay = 50 * time.Millisecond
	c.Start()
	t.Cleanup(func() { c.Stop(); q.Drain() })
	eventually(t, q, "the index", func() bool { return c.IsConnected() && c.IndexSynced && c.Harnesses != nil })
	return c, q, ss
}

// eventually drains the queue until cond holds.
func eventually(t *testing.T, q *Queue, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		q.Drain()
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		select {
		case <-q.Ready():
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// run runs a job off the queue's goroutine, draining the queue meanwhile,
// as ra runs commands in Bubble Tea's commands.
func run(t *testing.T, q *Queue, job func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- job() }()
	for {
		select {
		case err := <-done:
			q.Drain()
			return err
		case <-q.Ready():
			q.Drain()
		case <-time.After(15 * time.Second):
			t.Fatal("a job hung")
		}
	}
}

func (r *testRad) project(t *testing.T, c *ServerConnection, q *Queue) model.Project {
	t.Helper()
	var p model.Project
	if err := run(t, q, func() (err error) { p, err = c.AddProject(context.Background(), r.Root); return }); err != nil {
		t.Fatal(err)
	}
	return p
}

func lastText(s *SessionStore, kind model.ItemKind) string {
	for i := len(s.Transcript) - 1; i >= 0; i-- {
		if s.Transcript[i].Kind == kind {
			return s.Transcript[i].Text
		}
	}
	return ""
}

func TestRadPairsConnectsAndRunsAPrompt(t *testing.T) {
	r := startRad(t)
	c, q, _ := r.connect(t)
	if c.Info == nil || c.Info.ServerID != "srv-1" || !c.Info.Drafts {
		t.Fatalf("info %+v", c.Info)
	}
	if c.Harness("fake") == nil {
		t.Fatal("no fake harness")
	}
	p := r.project(t, c, q)
	if c.Projects[p.ID] == nil {
		t.Fatal("project not in the index")
	}
	var s model.Session
	err := run(t, q, func() (err error) {
		s, err = c.CreateSession(context.Background(), NewSession{ProjectID: p.ID, Harness: "fake", Workspace: NewWorkspace{Kind: model.WorkspaceRoot}})
		return
	})
	if err != nil {
		t.Fatal(err)
	}
	store := c.SessionStore(s.ID)
	store.Activate()
	eventually(t, q, "the session to sync", func() bool { return store.Synced && store.Live })
	job := store.Send("hello there", nil, false)
	if len(store.Outbox) != 1 {
		t.Fatal("not in the outbox")
	}
	if err := run(t, q, func() error { return job(context.Background()) }); err != nil {
		t.Fatal(err)
	}
	eventually(t, q, "the reply", func() bool {
		return store.Session != nil && store.Session.Status == model.SessionIdle && lastText(store, model.ItemAssistantMessage) != ""
	})
	if len(store.Outbox) != 0 || lastText(store, model.ItemUserMessage) != "hello there" {
		t.Fatalf("outbox %+v, transcript %v", store.Outbox, ids(store.Transcript))
	}
	if turns := store.SortedTurns(); len(turns) != 1 || turns[0].Status != model.TurnCompleted {
		t.Fatalf("turns %+v", turns)
	}
	// The index has it too.
	eventually(t, q, "the index's session", func() bool { return c.Sessions[s.ID] != nil && c.Sessions[s.ID].Status == model.SessionIdle })
}

func TestRadSteersPromptsIntoATurn(t *testing.T) {
	r := startRad(t)
	c, q, _ := r.connect(t)
	p := r.project(t, c, q)
	var s model.Session
	if err := run(t, q, func() (err error) {
		s, err = c.CreateSession(context.Background(), NewSession{ProjectID: p.ID, Harness: "fake", Workspace: NewWorkspace{Kind: model.WorkspaceRoot},
			Prompt: "slow reply with a good many words in it, so that it streams for a while yet"})
		return
	}); err != nil {
		t.Fatal(err)
	}
	store := c.SessionStore(s.ID)
	store.Activate()
	eventually(t, q, "the reply to stream", func() bool { return store.IsRunning() && lastText(store, model.ItemAssistantMessage) != "" })
	if !store.CanSteer("more", c.Harness("fake")) {
		t.Fatal("can't steer the fake agent's turn")
	}
	// A prompt sent to steer joins the turn; a queued one joins it on Steer.
	send := func(text string, steer bool) error {
		job := store.Send(text, nil, steer)
		return run(t, q, func() error { return job(context.Background()) })
	}
	if err := send("queued first", false); err != nil {
		t.Fatal(err)
	}
	var queued string
	eventually(t, q, "the queued prompt", func() bool {
		for _, it := range store.Transcript {
			if it.Status == model.ItemQueued {
				queued = it.ID
			}
		}
		return queued != ""
	})
	if err := run(t, q, func() error { return store.Steer(context.Background(), queued) }); err != nil {
		t.Fatal(err)
	}
	if err := send("and this", true); err != nil {
		t.Fatal(err)
	}
	eventually(t, q, "the turn to end", func() bool { return store.Session != nil && store.Session.Status == model.SessionIdle })
	turns := store.SortedTurns()
	if len(turns) != 1 {
		t.Fatalf("turns %+v", turns)
	}
	var prompts []string
	for _, it := range store.Transcript {
		if it.Kind == model.ItemUserMessage && it.TurnID == turns[0].ID {
			prompts = append(prompts, it.Text)
		}
	}
	if len(prompts) != 3 || prompts[1] != "queued first" || prompts[2] != "and this" {
		t.Errorf("turn's prompts %q", prompts)
	}
	if got := lastText(store, model.ItemAssistantMessage); !strings.HasPrefix(got, "Steered: ") {
		t.Errorf("last reply %q", got)
	}
}

func TestRadForksASession(t *testing.T) {
	r := startRad(t)
	c, q, _ := r.connect(t)
	if !c.Info.Forks {
		t.Fatalf("info %+v", c.Info)
	}
	p := r.project(t, c, q)
	var src model.Session
	if err := run(t, q, func() (err error) {
		src, err = c.CreateSession(context.Background(), NewSession{ProjectID: p.ID, Harness: "fake", Workspace: NewWorkspace{Kind: model.WorkspaceRoot}, Prompt: "hello there"})
		return
	}); err != nil {
		t.Fatal(err)
	}
	srcStore := c.SessionStore(src.ID)
	srcStore.Activate()
	eventually(t, q, "the turn's end", func() bool {
		turns := srcStore.SortedTurns()
		return len(turns) == 1 && turns[0].Status == model.TurnCompleted
	})
	var s model.Session
	if err := run(t, q, func() (err error) {
		s, err = c.Fork(context.Background(), ForkSession{SessionID: src.ID, Turn: 1, Prompt: "and then"})
		return
	}); err != nil {
		t.Fatal(err)
	}
	if s.ForkedFrom == nil || s.ForkedFrom.SessionID != src.ID || s.ForkedFrom.Turn != 1 || s.Harness != "fake" || c.Sessions[s.ID] == nil {
		t.Fatalf("fork %+v", s)
	}
	store := c.SessionStore(s.ID)
	store.Activate()
	eventually(t, q, "the fork's reply", func() bool {
		return store.Session != nil && store.Session.Status == model.SessionIdle && strings.Contains(lastText(store, model.ItemAssistantMessage), "Echo: and then")
	})
	if turns := store.SortedTurns(); len(turns) != 2 || turns[1].N != 2 {
		t.Fatalf("turns %+v", turns)
	}
}

func TestRadApprovalsAndQuestions(t *testing.T) {
	r := startRad(t)
	c, q, _ := r.connect(t)
	p := r.project(t, c, q)
	var s model.Session
	run(t, q, func() (err error) {
		s, err = c.CreateSession(context.Background(), NewSession{ProjectID: p.ID, Harness: "fake", Workspace: NewWorkspace{Kind: model.WorkspaceRoot}, Prompt: "write a.txt then a question"})
		return
	})
	var seen []string
	c.OnSessionChange = func(old, next *model.Session) {
		if a := Alert(old, next); a.Kind == AlertNeedsYou {
			seen = append(seen, next.ID)
		}
	}
	store := c.SessionStore(s.ID)
	store.Activate()
	eventually(t, q, "the approval", func() bool { return len(store.PendingApprovals()) == 1 })
	ap := store.PendingApprovals()[0]
	if err := run(t, q, func() error { return store.Respond(context.Background(), ap.ID, "allow", "", nil) }); err != nil {
		t.Fatal(err)
	}
	eventually(t, q, "the question", func() bool {
		pending := store.PendingApprovals()
		return len(pending) == 1 && pending[0].Approval.Special == model.SpecialQuestion
	})
	qa := store.PendingApprovals()[0]
	if err := run(t, q, func() error {
		return store.Respond(context.Background(), qa.ID, "submit", "", map[string]string{"Which color?": "Blue"})
	}); err != nil {
		t.Fatal(err)
	}
	eventually(t, q, "the end of the turn", func() bool { return store.Session.Status == model.SessionIdle })
	if reply := lastText(store, model.ItemAssistantMessage); !strings.Contains(reply, "Blue") {
		t.Fatalf("reply %q", reply)
	}
	if len(seen) == 0 {
		t.Fatal("no needs-you alert")
	}
}

func TestRadDraftsAndMarkRead(t *testing.T) {
	r := startRad(t)
	c, q, _ := r.connect(t)
	p := r.project(t, c, q)
	var s model.Session
	run(t, q, func() (err error) {
		s, err = c.CreateSession(context.Background(), NewSession{ProjectID: p.ID, Harness: "fake", Workspace: NewWorkspace{Kind: model.WorkspaceRoot}, Prompt: "later please", Draft: true})
		return
	})
	if s.Draft == nil || s.Draft.Text != "later please" {
		t.Fatalf("draft %+v", s.Draft)
	}
	store := c.SessionStore(s.ID)
	store.Activate()
	eventually(t, q, "the draft", func() bool { return store.Synced && store.UnsentDraft() != nil })
	job, err := store.SendDraft()
	if err != nil {
		t.Fatal(err)
	}
	if err := run(t, q, func() error { return job(context.Background()) }); err != nil {
		t.Fatal(err)
	}
	eventually(t, q, "the turn to end unread", func() bool {
		is := c.Sessions[s.ID]
		return is != nil && is.Status == model.SessionIdle && is.Unread && store.UnsentDraft() == nil
	})
	c.MarkRead(s.ID)
	if c.Sessions[s.ID].Unread {
		t.Fatal("not read at once")
	}
	// rad agrees: a second connection sees it read.
	c2, q2, _ := r.connect(t)
	eventually(t, q2, "the session read", func() bool { return c2.Sessions[s.ID] != nil && !c2.Sessions[s.ID].Unread })

	// A draft saved and removed.
	run(t, q, func() error { _, err := c.SaveDraft(context.Background(), s.ID, "again", nil); return err })
	if store.Session.Draft == nil || store.Session.Draft.Text != "again" {
		t.Fatalf("draft %+v", store.Session.Draft)
	}
	run(t, q, func() error { _, err := c.SaveDraft(context.Background(), s.ID, "", nil); return err })
	if store.Session.Draft != nil {
		t.Fatal("draft kept")
	}
}

func TestRadResubscribesAndDeliversAfterADrop(t *testing.T) {
	r := startRad(t)
	c, q, _ := r.connect(t)
	p := r.project(t, c, q)
	var s model.Session
	run(t, q, func() (err error) {
		s, err = c.CreateSession(context.Background(), NewSession{ProjectID: p.ID, Harness: "fake", Workspace: NewWorkspace{Kind: model.WorkspaceRoot}})
		return
	})
	store := c.SessionStore(s.ID)
	store.Activate()
	eventually(t, q, "the session to sync", func() bool { return store.Live })
	// The socket drops, as when the network goes, and a prompt goes out meanwhile.
	r.srv.CloseClientConnections()
	job := store.Send("after the drop", nil, false)
	if err := run(t, q, func() error { return job(context.Background()) }); err != nil {
		t.Fatal(err)
	}
	eventually(t, q, "the reply after reconnecting", func() bool {
		return c.IsConnected() && store.Live && store.Session.Status == model.SessionIdle && lastText(store, model.ItemUserMessage) == "after the drop"
	})
	n := 0
	for _, it := range store.Transcript {
		if it.Kind == model.ItemUserMessage {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d user messages", n)
	}
}

func TestRadPairsAgainWhenRevoked(t *testing.T) {
	r := startRad(t)
	saved, token, ss := r.pair(t)
	q := NewQueue()
	c := NewServerConnection(q, saved.URLs, token, ss.Drafts())
	var paired atomic.Int32
	c.Reauthorize = func(ctx context.Context) (string, []string, error) {
		paired.Add(1)
		saved, token, err := PairWith(ctx, ss, r.link(t))
		return token, saved.URLs, err
	}
	c.Start()
	defer c.Stop()
	eventually(t, q, "connected", func() bool { return c.IsConnected() && c.IndexSynced })
	if n, err := r.st.DeleteDevice(context.Background(), saved.DeviceID); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	// rad closes a revoked device's socket within its keepalive; don't wait for it.
	c.RPC().Disconnect("", true)
	eventually(t, q, "paired again", func() bool { return paired.Load() == 1 && c.IsConnected() && c.IndexSynced })
	if _, tok, _ := ss.Load(); tok == "" {
		t.Fatal("no new token saved")
	}
}

func TestRadGivesUpAtOnceWhenUnauthorized(t *testing.T) {
	r := startRad(t)
	q := NewQueue()
	c := NewServerConnection(q, []string{r.url}, "bogus", nil)
	c.Start()
	defer c.Stop()
	eventually(t, q, "failed", func() bool { return c.State.Kind == Failed })
	if c.State.Code != "unauthorized" {
		t.Fatalf("state %+v", c.State)
	}
}

func TestRadGivesUpAfterMaxAttempts(t *testing.T) {
	r := startRad(t)
	r.srv.Close()
	q := NewQueue()
	c := NewServerConnection(q, []string{r.url}, "tok", nil)
	c.FirstRetryDelay = 10 * time.Millisecond
	c.MaxAttempts = 3
	c.Start()
	defer c.Stop()
	eventually(t, q, "failed", func() bool { return c.State.Kind == Failed })
	if c.State.Code != "unreachable" {
		t.Fatalf("state %+v", c.State)
	}
	// A prompt fails once the connection gave up.
	if err := c.Deliver(context.Background(), "session.prompt", nil, nil, time.Second); ErrorCode(err) != "disconnected" {
		t.Fatal(err)
	}
}

// A rad that accepts the socket and never answers.
func silentRad(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		for {
			typ, data, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			// Answer server.info only while connecting (id 1).
			if typ == websocket.MessageText && strings.Contains(string(data), `"id":1,`) {
				ws.Write(r.Context(), websocket.MessageText, []byte(`{"id":1,"result":{"serverId":"s","version":"1"}}`))
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestRPCPingsWhenACallGoesUnanswered(t *testing.T) {
	url := silentRad(t)
	c := NewRPCClient("tok")
	c.PingTimeout = 50 * time.Millisecond
	closed := make(chan string, 1)
	c.OnMessage = func(m ServerMessage) {
		if m.Kind == MessageClosed {
			closed <- m.Reason
		}
	}
	if _, err := c.Connect(context.Background(), []string{url}); err != nil {
		t.Fatal(err)
	}
	err := c.CallTimeout(context.Background(), "session.list", nil, nil, 50*time.Millisecond)
	if ErrorCode(err) != "disconnected" {
		t.Fatalf("err %v", err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("the socket wasn't dropped")
	}
	if c.IsConnected() {
		t.Fatal("still connected")
	}
}
