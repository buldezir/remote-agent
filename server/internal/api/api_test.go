package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"remote-agent/internal/api"
	"remote-agent/internal/events"
	"remote-agent/internal/fsbrowse"
	"remote-agent/internal/harness"
	"remote-agent/internal/harness/fake"
	"remote-agent/internal/images"
	"remote-agent/internal/model"
	"remote-agent/internal/orchestrator"
	"remote-agent/internal/store"
	"remote-agent/internal/tasks"
	"remote-agent/internal/terminal"
)

type testServer struct {
	url  string
	st   *store.Store
	orch *orchestrator.Orchestrator
	root string
}

func startServer(t *testing.T) *testServer {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "rad.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hub := events.NewHub()
	st.OnCommit(hub.Publish)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	imgs := images.New(t.TempDir())
	orch := orchestrator.New(st, harness.NewRegistry(fake.Harness{}), orchestrator.Options{WorktreesDir: t.TempDir(), Images: imgs, Log: log})
	t.Cleanup(orch.Shutdown)
	root := t.TempDir()
	srv := api.NewServer(st, hub, orch, fsbrowse.New([]string{root}), imgs, "srv-1", "test-host", log)
	srv.WebOrigins = []string{"localhost:*", "https://web.example"}
	terms := terminal.NewManager(terminal.Options{Shell: "/bin/sh", Env: []string{"HOME=" + t.TempDir(), "PS1=$ ", "ENV="}, Log: log})
	srv.Terminals = terms
	maint := tasks.NewManager(log)
	srv.Tasks = maint
	t.Cleanup(maint.Shutdown)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(terms.Shutdown) // first: httptest doesn't wait for the terminal sockets' handlers
	return &testServer{url: ts.URL, st: st, orch: orch, root: root}
}

func (s *testServer) pair(t *testing.T, code string) (*http.Response, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"code": code, "deviceName": "test phone"})
	resp, err := http.Post(s.url+"/v1/pair", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func (s *testServer) dial(t *testing.T, token string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h := http.Header{}
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(s.url, "http")+"/v1/ws", &websocket.DialOptions{HTTPHeader: h})
}

// Client side of the RPC protocol.

type rpcError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type frame struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type eventsParams struct {
	Stream string        `json:"stream"`
	Events []model.Event `json:"events"`
}

type syncParams struct {
	Stream string `json:"stream"`
	Seq    int64  `json:"seq"`
	Reset  bool   `json:"reset"`
}

type client struct {
	t      *testing.T
	ws     *websocket.Conn
	frames chan frame
	notes  []frame // notifications received while waiting for a response
	nextID int
}

func newClient(t *testing.T, ws *websocket.Conn) *client {
	ws.SetReadLimit(16 << 20)
	c := &client{t: t, ws: ws, frames: make(chan frame, 4096)}
	go func() {
		defer close(c.frames)
		for {
			_, data, err := ws.Read(context.Background())
			if err != nil {
				return
			}
			var f frame
			if err := json.Unmarshal(data, &f); err != nil {
				t.Errorf("bad frame %s: %v", data, err)
				return
			}
			c.frames <- f
		}
	}()
	t.Cleanup(func() { ws.Close(websocket.StatusNormalClosure, "") })
	return c
}

func (c *client) read() frame {
	c.t.Helper()
	select {
	case f, ok := <-c.frames:
		if !ok {
			c.t.Fatal("connection closed")
		}
		return f
	case <-time.After(10 * time.Second):
		c.t.Fatal("timed out waiting for a frame")
	}
	panic("unreachable")
}

// call sends a request and waits for its response, queueing notifications.
func (c *client) call(method string, params any) (json.RawMessage, *rpcError) {
	c.t.Helper()
	c.nextID++
	id := c.nextID
	data, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err := c.ws.Write(context.Background(), websocket.MessageText, data); err != nil {
		c.t.Fatal(err)
	}
	for {
		f := c.read()
		if f.Method != "" {
			c.notes = append(c.notes, f)
			continue
		}
		if string(f.ID) != fmt.Sprint(id) {
			c.t.Fatalf("response for unexpected id %s", f.ID)
		}
		return f.Result, f.Error
	}
}

func (c *client) mustCall(method string, params any, out any) {
	c.t.Helper()
	res, e := c.call(method, params)
	if e != nil {
		c.t.Fatalf("%s: %s: %s", method, e.Code, e.Message)
	}
	if out != nil {
		if err := json.Unmarshal(res, out); err != nil {
			c.t.Fatalf("%s result %s: %v", method, res, err)
		}
	}
}

// note returns the next notification.
func (c *client) note() frame {
	c.t.Helper()
	if len(c.notes) > 0 {
		f := c.notes[0]
		c.notes = c.notes[1:]
		return f
	}
	for {
		if f := c.read(); f.Method != "" {
			return f
		}
	}
}

// subscription is what a subscribe call delivered before its synchronized.
type subscription struct {
	replay []model.Event
	sync   syncParams
	seq    int64 // from the response
}

// subscribe subscribes and consumes the stream's replay up to synchronized.
// Live events that already arrived stay queued.
func (c *client) subscribe(stream string, afterSeq int64) subscription {
	c.t.Helper()
	var res struct {
		Stream string `json:"stream"`
		Seq    int64  `json:"seq"`
	}
	c.notes = slices.DeleteFunc(c.notes, func(f frame) bool { return streamOf(f) == stream }) // stale
	c.mustCall("subscribe", map[string]any{"stream": stream, "afterSeq": afterSeq}, &res)
	sub := subscription{seq: res.Seq}
	var rest []frame
	synced := false
	for _, f := range c.notes {
		switch {
		case synced || streamOf(f) != stream:
			rest = append(rest, f)
		case f.Method == "events":
			var ep eventsParams
			json.Unmarshal(f.Params, &ep)
			sub.replay = append(sub.replay, ep.Events...)
		case f.Method == "synchronized":
			json.Unmarshal(f.Params, &sub.sync)
			synced = true
		default:
			c.t.Fatalf("unexpected %s before synchronized", f.Method)
		}
	}
	if !synced {
		c.t.Fatalf("no synchronized for %s before the subscribe response", stream)
	}
	c.notes = rest
	if res.Stream != stream || res.Seq != sub.sync.Seq {
		c.t.Errorf("subscribe result %+v, synchronized %+v", res, sub.sync)
	}
	return sub
}

func streamOf(f frame) string {
	var p struct {
		Stream string `json:"stream"`
	}
	json.Unmarshal(f.Params, &p)
	return p.Stream
}

func TestHealthAndPairing(t *testing.T) {
	s := startServer(t)
	resp, err := http.Get(s.url + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	var health map[string]any
	json.NewDecoder(resp.Body).Decode(&health)
	resp.Body.Close()
	if resp.StatusCode != 200 || health["serverId"] != "srv-1" || health["name"] != "test-host" || health["protocolVersion"] != float64(1) {
		t.Errorf("health = %d %v", resp.StatusCode, health)
	}

	if resp, out := s.pair(t, "bogus"); resp.StatusCode != http.StatusUnauthorized || out["code"] != "unauthorized" {
		t.Errorf("bad code = %d %v", resp.StatusCode, out)
	}
	code, err := s.st.CreatePairingCode(context.Background(), 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	resp, out := s.pair(t, code)
	if resp.StatusCode != 200 || out["serverId"] != "srv-1" || out["token"] == "" || out["deviceId"] == "" {
		t.Fatalf("pair = %d %v", resp.StatusCode, out)
	}
	if resp, _ := s.pair(t, code); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("code reused: %d", resp.StatusCode)
	}

	// The WebSocket needs the device token.
	for _, tok := range []string{"", "wrong"} {
		if _, resp, err := s.dial(t, tok); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("dial with %q: err=%v resp=%v", tok, err, resp)
		}
	}
	ws, _, err := s.dial(t, out["token"].(string))
	if err != nil {
		t.Fatal(err)
	}
	c := newClient(t, ws)
	var info map[string]any
	c.mustCall("server.info", nil, &info)
	if info["deviceId"] != out["deviceId"] || info["serverId"] != "srv-1" {
		t.Errorf("server.info = %v", info)
	}

	// A revoked token no longer connects.
	if n, err := s.st.DeleteDevice(context.Background(), out["deviceId"].(string)); n != 1 || err != nil {
		t.Fatalf("revoke = %d, %v", n, err)
	}
	if _, resp, err := s.dial(t, out["token"].(string)); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("revoked token: err=%v", err)
	}
}

func TestDeviceUnpair(t *testing.T) {
	s := startServer(t)
	code, err := s.st.CreatePairingCode(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, out := s.pair(t, code)
	token := out["token"].(string)
	ws, _, err := s.dial(t, token)
	if err != nil {
		t.Fatal(err)
	}
	c := newClient(t, ws)
	c.mustCall("device.unpair", nil, nil)
	if devs, _ := s.st.Devices(context.Background()); len(devs) != 0 {
		t.Errorf("devices after unpair = %v", devs)
	}
	if _, resp, err := s.dial(t, token); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unpaired token still connects: err=%v", err)
	}
}

// token pairs a device and returns its token.
func (s *testServer) token(t *testing.T) string {
	t.Helper()
	code, err := s.st.CreatePairingCode(context.Background(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	resp, out := s.pair(t, code)
	if resp.StatusCode != 200 {
		t.Fatalf("pair = %d %v", resp.StatusCode, out)
	}
	return out["token"].(string)
}

func connect(t *testing.T, s *testServer) *client {
	t.Helper()
	ws, _, err := s.dial(t, s.token(t))
	if err != nil {
		t.Fatal(err)
	}
	return newClient(t, ws)
}

// request makes an HTTP request with a device token.
func (s *testServer) request(t *testing.T, method, path, token string, body []byte) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, s.url+path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func TestImages(t *testing.T) {
	s := startServer(t)
	token := s.token(t)
	var b bytes.Buffer
	png.Encode(&b, image.NewGray(image.Rect(0, 0, 12, 7)))
	shot := b.Bytes()

	if resp, _ := s.request(t, "POST", "/v1/images", "", shot); resp.StatusCode != 401 {
		t.Errorf("upload without a token = %d", resp.StatusCode)
	}
	if resp, body := s.request(t, "POST", "/v1/images", token, []byte("plain text")); resp.StatusCode != 400 {
		t.Errorf("upload of text = %d %s", resp.StatusCode, body)
	}
	resp, body := s.request(t, "POST", "/v1/images", token, shot)
	var ref model.ImageRef
	json.Unmarshal(body, &ref)
	if resp.StatusCode != 200 || ref.MimeType != "image/png" || ref.Width != 12 || ref.Height != 7 || ref.Size != int64(len(shot)) {
		t.Fatalf("upload = %d %s", resp.StatusCode, body)
	}

	if resp, _ := s.request(t, "GET", "/v1/images/"+ref.ID, "", nil); resp.StatusCode != 401 {
		t.Errorf("download without a token = %d", resp.StatusCode)
	}
	resp, body = s.request(t, "GET", "/v1/images/"+ref.ID, token, nil)
	if resp.StatusCode != 200 || !bytes.Equal(body, shot) || resp.Header.Get("Content-Type") != "image/png" ||
		!strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("download = %d %v (%d bytes)", resp.StatusCode, resp.Header, len(body))
	}
	if resp, _ := s.request(t, "GET", "/v1/images/"+strings.Repeat("0", 64)+".png", token, nil); resp.StatusCode != 404 {
		t.Errorf("missing image = %d", resp.StatusCode)
	}

	// The image goes with a prompt.
	c := connect(t, s)
	dir := filepath.Join(s.root, "proj")
	os.Mkdir(dir, 0o755)
	var proj model.Project
	c.mustCall("project.add", map[string]any{"path": dir}, &proj)
	var sess model.Session
	c.mustCall("session.create", map[string]any{"commandId": "c1", "projectId": proj.ID, "harness": "fake",
		"workspace": map[string]any{"kind": "root"}, "prompt": "what is this?", "images": []string{ref.ID}}, &sess)
	var user *model.Item
	for _, ev := range c.subscribe(model.SessionStream(sess.ID), 0).replay {
		if ev.Item != nil && ev.Item.Kind == model.ItemUserMessage {
			user = ev.Item
		}
	}
	if user == nil || user.Text != "what is this?" || len(user.Images) != 1 || user.Images[0] != ref {
		t.Errorf("user message = %+v", user)
	}
	if _, e := c.call("session.prompt", map[string]any{"commandId": "p1", "sessionId": sess.ID, "images": []string{"x.png"}}); e == nil || e.Code != "invalid" {
		t.Errorf("prompt with an unknown image: %+v", e)
	}
}

func TestSessionOverWebSocket(t *testing.T) {
	s := startServer(t)
	c := connect(t, s)

	dir := filepath.Join(s.root, "proj")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var proj model.Project
	c.mustCall("project.add", map[string]any{"path": dir}, &proj)
	if proj.Name != "proj" || proj.IsGitRepo {
		t.Errorf("project = %+v", proj)
	}
	if _, e := c.call("project.add", map[string]any{"path": os.TempDir()}); e == nil || e.Code != "invalid" {
		t.Errorf("project outside roots: %+v", e)
	}

	// The index replays the project, then synchronizes with reset.
	idx := c.subscribe(model.IndexStream, 0)
	if !idx.sync.Reset || idx.sync.Seq != 1 || len(idx.replay) != 1 || idx.replay[0].Type != model.EvProjectUpserted || idx.replay[0].Project.ID != proj.ID {
		t.Errorf("index subscription = %+v", idx)
	}

	var sess model.Session
	c.mustCall("session.create", map[string]any{
		"commandId": "create-1", "projectId": proj.ID, "harness": "fake",
		"workspace": map[string]any{"kind": "root"}, "prompt": "hello api",
	}, &sess)
	if sess.ID == "" || sess.Title != "hello api" || sess.Harness != "fake" {
		t.Fatalf("session = %+v", sess)
	}
	// Retrying the create returns the same session.
	var again model.Session
	c.mustCall("session.create", map[string]any{"commandId": "create-1", "projectId": proj.ID, "harness": "fake", "prompt": "hello api"}, &again)
	if again.ID != sess.ID {
		t.Errorf("retried create = %s, want %s", again.ID, sess.ID)
	}

	stream := model.SessionStream(sess.ID)
	sub := c.subscribe(stream, 0)
	if !sub.sync.Reset {
		t.Error("first subscription is not a reset")
	}
	for _, ev := range sub.replay {
		if ev.Seq > sub.sync.Seq || ev.Stream != stream {
			t.Errorf("replayed event %d %s beyond synchronized %d", ev.Seq, ev.Stream, sub.sync.Seq)
		}
	}

	// Live events follow gap-free until the turn completes.
	items := map[string]*model.Item{}
	seqOf := map[string]int64{}
	done := false
	for _, ev := range sub.replay {
		if ev.Item != nil {
			items[ev.Item.ID], seqOf[ev.Item.ID] = ev.Item, ev.Seq
		}
		done = done || (ev.Turn != nil && ev.Turn.Status == model.TurnCompleted)
	}
	cursor := sub.sync.Seq
	for !done {
		f := c.note()
		var p eventsParams
		json.Unmarshal(f.Params, &p)
		if f.Method != "events" || p.Stream != stream {
			continue // index updates
		}
		for _, ev := range p.Events {
			if ev.Seq != cursor+1 {
				t.Fatalf("live seq %d after %d", ev.Seq, cursor)
			}
			cursor = ev.Seq
			if ev.Item != nil {
				items[ev.Item.ID], seqOf[ev.Item.ID] = ev.Item, ev.Seq
			}
			if ev.Turn != nil && ev.Turn.Status == model.TurnCompleted {
				done = true
			}
		}
	}
	var texts []string
	var user, reply string
	for _, it := range items {
		switch it.Kind {
		case model.ItemUserMessage:
			user = it.ID
		case model.ItemAssistantMessage:
			reply = it.ID
		default:
			continue
		}
		texts = append(texts, string(it.Kind)+"="+it.Text)
	}
	if !contains(texts, "user_message=hello api") || !contains(texts, "assistant_message=Echo: hello api") {
		t.Errorf("transcript = %q", texts)
	}

	// Let trailing session updates land, then resubscribe from just after the
	// user message: only entities changed later are replayed, without a reset.
	time.Sleep(200 * time.Millisecond)
	c.mustCall("unsubscribe", map[string]any{"stream": stream}, nil)
	full := c.subscribe(stream, 0)
	mid := seqOf[user]
	if mid == 0 || mid >= full.sync.Seq || seqOf[reply] <= mid {
		t.Fatalf("user message seq %d, reply seq %d, last %d", mid, seqOf[reply], full.sync.Seq)
	}
	re := c.subscribe(stream, mid)
	if re.sync.Reset || re.sync.Seq != full.sync.Seq {
		t.Errorf("resubscribe sync = %+v", re.sync)
	}
	if len(re.replay) == 0 || len(re.replay) >= len(full.replay) {
		t.Errorf("resubscribe replayed %d of %d events", len(re.replay), len(full.replay))
	}
	sawReply := false
	for _, ev := range re.replay {
		if ev.Seq <= mid || (ev.Item != nil && ev.Item.ID == user) {
			t.Errorf("resubscribe replayed seq %d (cursor %d)", ev.Seq, mid)
		}
		if ev.Item != nil && ev.Item.ID == reply {
			sawReply = ev.Item.Text == "Echo: hello api"
		}
	}
	if !sawReply {
		t.Error("resubscribe did not replay the final reply")
	}
	// Nothing newer: an empty replay at the current seq.
	if up := c.subscribe(stream, full.sync.Seq); len(up.replay) != 0 || up.sync.Reset {
		t.Errorf("up-to-date resubscribe = %+v", up)
	}
	// A cursor from the future (e.g. a reset database) gets a full reset.
	if ahead := c.subscribe(stream, full.sync.Seq+100); !ahead.sync.Reset || len(ahead.replay) != len(full.replay) {
		t.Errorf("ahead resubscribe = reset %v, %d events", ahead.sync.Reset, len(ahead.replay))
	}

	// Errors carry protocol codes.
	if _, e := c.call("nope", nil); e == nil || e.Code != "method_not_found" {
		t.Errorf("unknown method: %+v", e)
	}
	if _, e := c.call("session.prompt", map[string]any{"sessionId": "missing", "text": "hi"}); e == nil || e.Code != "not_found" {
		t.Errorf("unknown session: %+v", e)
	}
	if _, e := c.call("session.sendNow", map[string]any{"sessionId": "missing", "itemId": "x"}); e == nil || e.Code != "not_found" {
		t.Errorf("send now in an unknown session: %+v", e)
	}
	if _, e := c.call("session.cancelQueued", map[string]any{"sessionId": "missing", "itemId": "x"}); e == nil || e.Code != "not_found" {
		t.Errorf("cancel in an unknown session: %+v", e)
	}
	// A prompt no longer queued is sent already.
	c.mustCall("session.sendNow", map[string]any{"sessionId": sess.ID, "itemId": user}, nil)
	c.mustCall("session.cancelQueued", map[string]any{"sessionId": sess.ID, "itemId": user}, nil)
	if _, e := c.call("session.prompt", "not an object"); e == nil || e.Code != "invalid" {
		t.Errorf("bad params: %+v", e)
	}
	if _, e := c.call("subscribe", map[string]any{"stream": "bogus"}); e == nil || e.Code != "invalid" {
		t.Errorf("bad stream: %+v", e)
	}

	// The finished turn left the session unread, until a client shows it.
	var read struct{ Sessions []model.Session }
	c.mustCall("session.list", nil, &read)
	if len(read.Sessions) != 1 || !read.Sessions[0].Unread {
		t.Errorf("sessions after the turn = %+v", read.Sessions)
	}
	c.mustCall("session.markRead", map[string]any{"sessionId": sess.ID}, nil)
	var after struct{ Sessions []model.Session } // unread is left out once false
	c.mustCall("session.list", nil, &after)
	if len(after.Sessions) != 1 || after.Sessions[0].Unread {
		t.Errorf("read session = %+v", after.Sessions)
	}
	if _, e := c.call("session.markRead", map[string]any{"sessionId": "missing"}); e == nil || e.Code != "not_found" {
		t.Errorf("read an unknown session: %+v", e)
	}

	// A draft waits in the session until a device sends it.
	var drafted model.Session
	c.mustCall("session.saveDraft", map[string]any{"sessionId": sess.ID, "text": "next"}, &drafted)
	if drafted.Draft == nil || drafted.Draft.Text != "next" {
		t.Errorf("saved draft = %+v", drafted.Draft)
	}
	var undrafted model.Session // the draft is left out once removed
	c.mustCall("session.saveDraft", map[string]any{"sessionId": sess.ID, "text": ""}, &undrafted)
	if undrafted.Draft != nil || undrafted.Title != "hello api" {
		t.Errorf("removed draft: %q, %+v", undrafted.Title, undrafted.Draft)
	}
	if _, e := c.call("session.sendDraft", map[string]any{"commandId": "draft-1", "sessionId": sess.ID}); e == nil || e.Code != "conflict" {
		t.Errorf("send without a draft: %+v", e)
	}
	var draftOnly model.Session
	c.mustCall("session.create", map[string]any{"commandId": "create-draft", "projectId": proj.ID, "harness": "fake",
		"workspace": map[string]any{"kind": "root"}, "prompt": "later", "draft": true}, &draftOnly)
	if draftOnly.Draft == nil || draftOnly.Draft.Text != "later" || draftOnly.Title != "later" || draftOnly.Status != model.SessionIdle {
		t.Errorf("created with a draft: %+v", draftOnly)
	}
	c.mustCall("session.delete", map[string]any{"sessionId": draftOnly.ID}, nil)

	// Deleting the session removes it from the index, and its subscribers
	// hear so on its own stream, next in seq.
	cursor = c.subscribe(stream, 0).sync.Seq
	c.mustCall("session.delete", map[string]any{"sessionId": sess.ID}, nil)
	removed := map[string]bool{}
	for len(removed) < 2 {
		f := c.note()
		var p eventsParams
		json.Unmarshal(f.Params, &p)
		for _, ev := range p.Events {
			if ev.Type != model.EvSessionRemoved || ev.ID != sess.ID {
				continue
			}
			if p.Stream == stream && ev.Seq != cursor+1 {
				t.Errorf("removal seq %d after %d", ev.Seq, cursor)
			}
			removed[p.Stream] = true
		}
	}
	if !removed[stream] || !removed[model.IndexStream] {
		t.Errorf("removal on %v", removed)
	}
	if gone := c.subscribe(stream, 0); len(gone.replay) != 1 || gone.replay[0].Type != model.EvSessionRemoved {
		t.Errorf("deleted session's stream replays %+v", gone.replay)
	}
	var list struct{ Sessions []model.Session }
	c.mustCall("session.list", nil, &list)
	if len(list.Sessions) != 0 {
		t.Errorf("sessions after delete = %+v", list.Sessions)
	}
	if _, e := c.call("session.delete", map[string]any{"sessionId": sess.ID}); e == nil || e.Code != "not_found" {
		t.Errorf("delete twice: %+v", e)
	}
}

// A view that takes another's place unsubscribes and subscribes again at
// once, without waiting for the answers: rad must apply the two in order, or
// the session stops updating.
func TestResubscribeInOrder(t *testing.T) {
	s := startServer(t)
	c := connect(t, s)
	dir := filepath.Join(s.root, "proj")
	os.Mkdir(dir, 0o755)
	var proj model.Project
	c.mustCall("project.add", map[string]any{"path": dir}, &proj)
	var sess model.Session
	c.mustCall("session.create", map[string]any{"commandId": "c1", "projectId": proj.ID, "harness": "fake", "workspace": map[string]any{"kind": "root"}}, &sess)
	stream := model.SessionStream(sess.ID)
	c.subscribe(stream, 0)

	for i := range 30 {
		for _, method := range []string{"unsubscribe", "subscribe"} {
			c.nextID++
			data, _ := json.Marshal(map[string]any{"id": c.nextID, "method": method, "params": map[string]any{"stream": stream}})
			if err := c.ws.Write(context.Background(), websocket.MessageText, data); err != nil {
				t.Fatal(err)
			}
		}
		for answered := 0; answered < 2; {
			if f := c.read(); f.Method == "" {
				answered++
			}
		}
		c.mustCall("session.saveDraft", map[string]any{"sessionId": sess.ID, "text": fmt.Sprint("draft ", i)}, nil)
		for heard := false; !heard; {
			f := c.note()
			var p eventsParams
			json.Unmarshal(f.Params, &p)
			for _, ev := range p.Events {
				heard = heard || p.Stream == stream && ev.Session != nil && ev.Session.Draft != nil && ev.Session.Draft.Text == fmt.Sprint("draft ", i)
			}
		}
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// Browsers can't set headers on a WebSocket, so a web page offers its token
// as a subprotocol, and only pages on the allowed origins may connect.
func TestBrowserWebSocket(t *testing.T) {
	s := startServer(t)
	token := s.token(t)
	dial := func(origin string, protocols ...string) (*websocket.Conn, *http.Response, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h := http.Header{}
		if origin != "" {
			h.Set("Origin", origin)
		}
		return websocket.Dial(ctx, "ws"+strings.TrimPrefix(s.url, "http")+"/v1/ws",
			&websocket.DialOptions{HTTPHeader: h, Subprotocols: protocols})
	}

	ws, _, err := dial("http://localhost:5173", "rad.v1", "rad.token."+token)
	if err != nil {
		t.Fatal(err)
	}
	if ws.Subprotocol() != "rad.v1" {
		t.Errorf("subprotocol = %q", ws.Subprotocol())
	}
	var info map[string]any
	newClient(t, ws).mustCall("server.info", nil, &info)
	if info["serverId"] != "srv-1" {
		t.Errorf("server.info = %v", info)
	}

	if ws, _, err := dial("https://web.example", "rad.v1", "rad.token."+token); err != nil {
		t.Errorf("origin with scheme: %v", err)
	} else {
		ws.CloseNow()
	}
	if _, resp, err := dial("http://localhost:5173", "rad.v1", "rad.token.wrong"); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong token: err=%v", err)
	}
	if _, resp, err := dial("https://evil.example", "rad.v1", "rad.token."+token); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("other origin: err=%v", err)
	}
	if _, resp, err := dial("http://web.example", "rad.v1", "rad.token."+token); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("allowed host on another scheme: err=%v", err)
	}
}

func TestCORS(t *testing.T) {
	s := startServer(t)
	preflight := func(origin string) *http.Response {
		req, _ := http.NewRequest(http.MethodOptions, s.url+"/v1/images", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "authorization")
		req.Header.Set("Access-Control-Request-Private-Network", "true")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	resp := preflight("http://localhost:5173")
	h := resp.Header
	if resp.StatusCode != http.StatusNoContent || h.Get("Access-Control-Allow-Origin") != "http://localhost:5173" ||
		!strings.Contains(h.Get("Access-Control-Allow-Headers"), "Authorization") ||
		!strings.Contains(h.Get("Access-Control-Allow-Methods"), "POST") ||
		h.Get("Access-Control-Allow-Private-Network") != "true" {
		t.Errorf("preflight = %d %v", resp.StatusCode, h)
	}
	if resp := preflight("https://evil.example"); resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("other origin allowed: %v", resp.Header)
	}

	req, _ := http.NewRequest(http.MethodGet, s.url+"/v1/health", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Errorf("health = %d %v", resp.StatusCode, resp.Header)
	}
}

func TestServerManagement(t *testing.T) {
	s := startServer(t)
	c := connect(t, s)

	var harnesses struct{ Harnesses []model.HarnessInfo }
	c.mustCall("harness.list", nil, &harnesses)
	if caps := harnesses.Harnesses[0].Caps; !caps.Update || !caps.Login || !caps.OneShot {
		t.Fatalf("fake caps %+v", caps)
	}

	// Settings.
	var settings model.ServerSettings
	c.mustCall("server.settings", nil, &settings)
	if settings.SystemAgent != nil {
		t.Fatalf("fresh settings %+v", settings)
	}
	if _, err := c.call("server.setSettings", map[string]any{"systemAgent": map[string]string{"harness": "nope"}}); err == nil || err.Code != "invalid" {
		t.Fatalf("unknown harness: %+v", err)
	}
	c.mustCall("server.setSettings", map[string]any{"systemAgent": map[string]string{"harness": "fake", "model": "echo"}}, &settings)
	c.mustCall("server.settings", nil, &settings)
	if settings.SystemAgent == nil || settings.SystemAgent.Model != "echo" {
		t.Fatalf("settings %+v", settings)
	}

	// No updater: rad says why it can't update.
	var info model.UpdateInfo
	c.mustCall("server.checkUpdate", map[string]bool{"refresh": true}, &info)
	if info.Via != "" || info.Reason == "" || info.Current == "" {
		t.Fatalf("check %+v", info)
	}
	if _, err := c.call("server.update", nil); err == nil || err.Code != "unavailable" {
		t.Fatalf("update without an updater: %+v", err)
	}

	// Signing in: the command prints a link and waits for a code.
	var task model.Task
	c.mustCall("harness.login", map[string]string{"harness": "fake"}, &task)
	if task.Kind != model.TaskLogin || task.Harness != "fake" || task.Status != model.TaskRunning {
		t.Fatalf("login task %+v", task)
	}
	poll := func(done func(model.Task) bool) model.Task {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			var got model.Task // absent fields stay unset
			c.mustCall("task.get", map[string]string{"id": task.ID}, &got)
			if task = got; done(task) {
				return task
			}
		}
		t.Fatalf("task %+v", task)
		return task
	}
	poll(func(k model.Task) bool { return k.Input && strings.Contains(k.Output, "Paste code here") })
	if task.URL != "https://example.com/fake-login?session=42" || !strings.HasPrefix(task.Output, "$ /bin/sh -c") {
		t.Fatalf("waiting for a code: %+v", task)
	}
	c.mustCall("task.input", map[string]string{"id": task.ID, "text": "abc"}, nil)
	poll(func(k model.Task) bool { return k.Status != model.TaskRunning })
	if task.Status != model.TaskSucceeded || !strings.Contains(task.Output, "Fake (scripted) is signed in.") || task.Input {
		t.Fatalf("signed in: %+v", task)
	}

	// Updating.
	c.mustCall("harness.update", map[string]string{"harness": "fake"}, &task)
	poll(func(k model.Task) bool { return k.Status != model.TaskRunning })
	if task.Status != model.TaskSucceeded || task.Version != "1" || !strings.Contains(task.Output, "Downloading 100%\nFake is up to date.") {
		t.Fatalf("updated: %+v", task)
	}
	if strings.Contains(task.Output, "\nDownloading  50%") {
		t.Fatalf("the progress line wasn't redrawn: %q", task.Output)
	}

	// Cancelling a login that waits.
	c.mustCall("harness.login", map[string]string{"harness": "fake"}, &task)
	poll(func(k model.Task) bool { return k.Input })
	c.mustCall("task.cancel", map[string]string{"id": task.ID}, nil)
	poll(func(k model.Task) bool { return k.Status != model.TaskRunning })
	if task.Status != model.TaskCancelled {
		t.Fatalf("cancelled: %+v", task)
	}

	var list struct{ Tasks []model.Task }
	c.mustCall("task.list", nil, &list)
	if len(list.Tasks) != 3 || list.Tasks[2].ID != task.ID {
		t.Fatalf("list %+v", list.Tasks)
	}
	if _, err := c.call("task.get", map[string]string{"id": "nope"}); err == nil || err.Code != "not_found" {
		t.Fatalf("unknown task: %+v", err)
	}
	if _, err := c.call("harness.update", map[string]string{"harness": "nope"}); err == nil || err.Code != "invalid" {
		t.Fatalf("unknown harness: %+v", err)
	}
}

func TestSessionCommandSuggestions(t *testing.T) {
	s := startServer(t)
	c := connect(t, s)
	dir := filepath.Join(s.root, "skills-project")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var project model.Project
	c.mustCall("project.add", map[string]any{"path": dir}, &project)
	var session model.Session
	c.mustCall("session.create", map[string]any{"commandId": "create-skills", "projectId": project.ID, "harness": "fake", "workspace": map[string]any{"kind": "root"}}, &session)
	var result struct {
		Commands []model.Command `json:"commands"`
	}
	c.mustCall("session.commands", map[string]any{"sessionId": session.ID}, &result)
	if len(result.Commands) < 3 || result.Commands[1].Name != "skill:prototype" || result.Commands[1].Scope != "user" {
		t.Fatalf("catalog = %+v", result)
	}
	for _, event := range c.subscribe(model.SessionStream(session.ID), 0).replay {
		if event.Item != nil || event.Turn != nil {
			t.Fatalf("discovery started a turn: %+v", event)
		}
	}
	if _, e := c.call("session.commands", map[string]any{"sessionId": "missing"}); e == nil {
		t.Fatal("missing session accepted")
	}
	c.mustCall("session.archive", map[string]any{"sessionId": session.ID}, nil)
	c.mustCall("session.commands", map[string]any{"sessionId": session.ID}, &result)
	if len(result.Commands) != 0 {
		t.Fatal("archived session has suggestions")
	}
}
