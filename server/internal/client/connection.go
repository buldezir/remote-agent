package client

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"remote-agent/internal/model"
)

type StateKind int

const (
	Idle StateKind = iota
	Connecting
	Connected
	Failed
)

// ConnState is where the connection is. Failed carries why, and Code, the
// error's (unauthorized, unreachable…).
type ConnState struct {
	Kind    StateKind
	Message string
	Code    string
}

// The connection as goroutines see it, for Deliver.
const (
	phaseStopped int32 = iota // not wanted, or given up
	phaseWaiting
	phaseConnected
)

// ServerConnection is a live connection to rad. It keeps the index
// (projects and sessions) in sync, owns the session stores, and reconnects
// with backoff: it stays Connecting while it tries, and gives up after
// MaxAttempts failures in a row, Failed until Start or Retry. A socket that
// has been silent for Heartbeat is pinged, and dropped if it doesn't
// answer, so one that died while the computer slept doesn't pass for
// connected.
//
// Its exported fields belong to the dispatcher, as do the methods without
// a context; the methods with one block, and run off it.
type ServerConnection struct {
	MaxAttempts     int
	FirstRetryDelay time.Duration
	Heartbeat       time.Duration

	// Reauthorize pairs again when rad turns the token away, and returns
	// the new token and URLs. It runs off the dispatcher.
	Reauthorize func(ctx context.Context) (token string, urls []string, err error)
	// OnURLWorked hears, off the dispatcher, of the URL that connected.
	OnURLWorked func(url string)
	// OnSessionChange hears of a session in the index that changed: live,
	// or while ra was away, in the replay after reconnecting. Not of those
	// the first sync brings.
	OnSessionChange func(old, next *model.Session)

	State       ConnState
	Info        *ServerInfo
	Projects    map[string]*model.Project
	Sessions    map[string]*model.Session
	Harnesses   []model.HarnessInfo // nil until loaded
	IndexSynced bool
	Images      *ImageCache
	// Version counts changes to the index, for views that cache what they drew.
	Version uint64

	d         Dispatcher
	rpc       *RPCClient
	draftsRef *Drafts

	urlsMu sync.Mutex
	urls   []string

	indexSeq         int64
	indexLive        bool
	indexOutstanding int // as SessionStore's, for the index
	indexReplay      []model.Event
	stores           map[string]*SessionStore
	wanted           bool
	looping          bool
	cancel           context.CancelFunc
	wake             chan struct{}

	phase         atomic.Int32
	serverVersion atomic.Value // string
}

// NewServerConnection makes a connection to the server at urls, the one
// that worked last first. drafts may be nil.
func NewServerConnection(d Dispatcher, urls []string, token string, drafts *Drafts) *ServerConnection {
	c := &ServerConnection{
		MaxAttempts:     5,
		FirstRetryDelay: time.Second,
		Heartbeat:       15 * time.Second,
		Projects:        map[string]*model.Project{},
		Sessions:        map[string]*model.Session{},
		d:               d,
		rpc:             NewRPCClient(token),
		draftsRef:       drafts,
		urls:            slices.Clone(urls),
		stores:          map[string]*SessionStore{},
		wake:            make(chan struct{}, 1),
	}
	c.rpc.OnMessage = func(m ServerMessage) { d.Post(func() { c.handle(m) }) }
	c.serverVersion.Store("")
	return c
}

// RPC is the connection's socket, for its HTTP calls and its settings.
func (c *ServerConnection) RPC() *RPCClient { return c.rpc }

func (c *ServerConnection) IsConnected() bool { return c.State.Kind == Connected }

// SetCredentials replaces the token and URLs, as after pairing again.
func (c *ServerConnection) SetCredentials(token string, urls []string) {
	c.rpc.SetToken(token)
	c.urlsMu.Lock()
	c.urls = slices.Clone(urls)
	c.urlsMu.Unlock()
}

func (c *ServerConnection) URLs() []string {
	c.urlsMu.Lock()
	defer c.urlsMu.Unlock()
	return slices.Clone(c.urls)
}

func (c *ServerConnection) promote(url string) {
	c.urlsMu.Lock()
	c.urls = append([]string{url}, slices.DeleteFunc(c.urls, func(u string) bool { return u == url })...)
	c.urlsMu.Unlock()
	if c.OnURLWorked != nil {
		c.OnURLWorked(url)
	}
}

// Lifecycle

func (c *ServerConnection) Start() {
	c.wanted = true
	c.updatePhase()
	if c.looping {
		return
	}
	c.looping = true
	c.spawn()
}

// Stop stops for good, until Start.
func (c *ServerConnection) Stop() {
	c.wanted = false
	c.Pause()
}

// Pause disconnects until Start or Retry.
func (c *ServerConnection) Pause() {
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	c.looping = false
	c.wakeUp()
	c.rpc.Disconnect("closed by ra", true)
	c.setState(ConnState{Kind: Idle})
}

// Retry connects now, as after a failure.
func (c *ServerConnection) Retry() {
	c.wanted = true
	c.looping = true
	c.wakeUp()
	c.spawn()
}

// Check pings now rather than at the next heartbeat, as when ra comes back
// to the front: the computer may have slept.
func (c *ServerConnection) Check() {
	if c.IsConnected() {
		go c.rpc.Ping()
	}
}

// ReviveConnection connects again if the connection gave up, as it may
// while rad restarts. Safe from any goroutine.
func (c *ServerConnection) ReviveConnection() {
	c.d.Post(func() {
		if c.State.Kind == Failed {
			c.Retry()
		}
	})
}

func (c *ServerConnection) spawn() {
	if c.cancel != nil {
		c.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	go c.runLoop(ctx)
}

// postIn posts f to run only if ctx, the run loop's, is still the
// current one.
func (c *ServerConnection) postIn(ctx context.Context, f func()) {
	c.d.Post(func() {
		if ctx.Err() == nil {
			f()
		}
	})
}

func (c *ServerConnection) runLoop(ctx context.Context) {
	defer c.postIn(ctx, func() { c.looping = false })
	delay := c.FirstRetryDelay
	failures := 0
	reauthorized := false
	for ctx.Err() == nil {
		if c.rpc.IsConnected() {
			c.sleep(ctx, min(2*time.Second, c.Heartbeat))
			// After a sleep the silence is at least as long, so the first
			// check after waking pings.
			if ctx.Err() == nil && c.rpc.Silence() >= c.Heartbeat {
				c.rpc.Ping()
			}
			continue
		}
		c.postIn(ctx, func() {
			if c.State.Kind != Connecting {
				c.setState(ConnState{Kind: Connecting})
			}
		})
		info, err := c.rpc.Connect(ctx, c.URLs())
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			if u := c.rpc.ConnectedURL(); u != "" {
				c.promote(u)
			}
			c.postIn(ctx, func() { c.connected(info) })
			delay, failures, reauthorized = c.FirstRetryDelay, 0, false
			continue
		}
		unauthorized := ErrorCode(err) == "unauthorized"
		if unauthorized && c.Reauthorize != nil && !reauthorized {
			reauthorized = true
			c.postIn(ctx, func() { c.setState(ConnState{Kind: Connecting, Message: "Pairing with rad…"}) })
			token, urls, rerr := c.Reauthorize(ctx)
			if rerr == nil {
				c.SetCredentials(token, urls)
				continue
			}
			err = rerr
		}
		failures++
		// Trying again won't bring back a revoked token.
		if unauthorized || failures >= c.MaxAttempts {
			failed := ConnState{Kind: Failed, Message: err.Error(), Code: ErrorCode(err)}
			c.postIn(ctx, func() { c.setState(failed) })
			return
		}
		c.sleep(ctx, delay)
		delay *= 2
	}
}

// sleep waits, or until Retry, Pause or the socket closing cuts it short.
func (c *ServerConnection) sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	case <-c.wake:
	}
}

func (c *ServerConnection) wakeUp() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *ServerConnection) setState(s ConnState) {
	c.State = s
	c.Version++
	c.updatePhase()
}

func (c *ServerConnection) updatePhase() {
	switch {
	case !c.wanted || c.State.Kind == Failed:
		c.phase.Store(phaseStopped)
	case c.State.Kind == Connected:
		c.phase.Store(phaseConnected)
	default:
		c.phase.Store(phaseWaiting)
	}
}

func (c *ServerConnection) connected(info ServerInfo) {
	c.Info = &info
	c.serverVersion.Store(info.Version)
	c.setState(ConnState{Kind: Connected})
	// The index first, then each store a view shows.
	c.indexOutstanding++
	c.indexLive = false
	c.indexReplay = nil
	type sub struct {
		store *SessionStore
		after int64
	}
	var subs []sub
	for _, s := range c.stores {
		if s.IsActive() {
			s.beginReplay()
			subs = append(subs, sub{s, s.LastSeq})
		}
	}
	after := c.indexSeq
	go func() {
		ctx := context.Background()
		if c.rpc.Call(ctx, "subscribe", map[string]any{"stream": model.IndexStream, "afterSeq": after}, nil) != nil {
			c.d.Post(func() { c.indexOutstanding = max(0, c.indexOutstanding-1) })
		}
		for _, s := range subs {
			if c.rpc.Call(ctx, "subscribe", map[string]any{"stream": model.SessionStream(s.store.ID), "afterSeq": s.after}, nil) != nil {
				c.d.Post(s.store.subscriptionFailed)
			}
		}
		c.RefreshHarnesses(ctx, false)
	}()
}

// Messages

func (c *ServerConnection) handle(m ServerMessage) {
	switch m.Kind {
	case MessageEvents:
		if m.Stream == model.IndexStream {
			c.applyIndex(m.Events)
		} else if s := c.storeForStream(m.Stream); s != nil {
			s.apply(m.Events)
		}
	case MessageSynchronized:
		if m.Stream == model.IndexStream {
			c.indexSynchronized(m.Seq, m.Reset)
		} else if s := c.storeForStream(m.Stream); s != nil {
			s.synchronized(m.Seq, m.Reset)
		}
	case MessageResync:
		if m.Stream == model.IndexStream {
			c.subscribeIndex()
		} else if s := c.storeForStream(m.Stream); s != nil {
			c.subscribe(s)
		}
	case MessageClosed:
		c.indexLive = false
		c.indexOutstanding = 0
		for _, s := range c.stores {
			s.closed()
		}
		// Not after Pause: the socket closed because ra closed it.
		if c.wanted && c.looping {
			msg := ""
			if m.Reason == errUnauthorized().Message {
				msg = m.Reason
			}
			c.setState(ConnState{Kind: Connecting, Message: msg})
			c.wakeUp()
		}
	}
}

type sessionChange struct{ old, next *model.Session }

func (c *ServerConnection) indexSynchronized(seq int64, reset bool) {
	// A reset drops what was cached, but the replay is still compared with it.
	before := c.Sessions
	if reset {
		c.Projects = map[string]*model.Project{}
		c.Sessions = map[string]*model.Session{}
	} else {
		before = nil
	}
	buffered := c.indexReplay
	c.indexReplay = nil
	var changes []sessionChange
	for i := range buffered {
		if ch, ok := c.upsertIndex(&buffered[i], before); ok {
			changes = append(changes, ch)
		}
	}
	c.indexSeq = seq
	c.indexOutstanding = max(0, c.indexOutstanding-1)
	c.indexLive = c.indexOutstanding == 0
	c.IndexSynced = true
	for id, s := range c.Sessions {
		if st := c.stores[id]; st != nil && st.Session == nil {
			st.setSession(s)
		}
	}
	c.draftsRef.Keep(func(id string) bool { return c.Sessions[id] != nil })
	c.Version++
	c.report(changes)
}

func (c *ServerConnection) applyIndex(events []model.Event) {
	if !c.indexLive {
		c.indexReplay = append(c.indexReplay, events...)
		return
	}
	var changes []sessionChange
	for i := range events {
		ev := &events[i]
		if ev.Seq <= c.indexSeq {
			continue
		}
		if ev.Seq != c.indexSeq+1 {
			c.indexLive = false
			c.subscribeIndex()
			break
		}
		c.indexSeq = ev.Seq
		if ch, ok := c.upsertIndex(ev, nil); ok {
			changes = append(changes, ch)
		}
	}
	c.Version++
	c.report(changes)
}

// upsertIndex applies an index event, and returns the session it changed.
// A session is compared with the one cached, else with the one in before.
func (c *ServerConnection) upsertIndex(ev *model.Event, before map[string]*model.Session) (sessionChange, bool) {
	switch ev.Type {
	case model.EvProjectUpserted:
		if ev.Project != nil {
			c.Projects[ev.Project.ID] = ev.Project
		}
	case model.EvProjectRemoved:
		delete(c.Projects, ev.ID)
	case model.EvSessionUpserted:
		s := ev.Session
		if s == nil {
			break
		}
		old := c.Sessions[s.ID]
		if old == nil {
			old = before[s.ID]
		}
		c.Sessions[s.ID] = s
		if old != nil && !sameJSON(old, s) {
			return sessionChange{old, s}, true
		}
	case model.EvSessionRemoved:
		if ev.ID != "" {
			delete(c.Sessions, ev.ID)
			c.draftsRef.Set(ev.ID, "")
		}
	}
	return sessionChange{}, false
}

func sameJSON(a, b any) bool {
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(ja, jb)
}

// report tells of the changes once the batch they came in is applied, so
// the index is whole.
func (c *ServerConnection) report(changes []sessionChange) {
	if c.OnSessionChange == nil {
		return
	}
	for _, ch := range changes {
		c.OnSessionChange(ch.old, ch.next)
	}
}

func (c *ServerConnection) subscribeIndex() {
	c.indexOutstanding++
	c.indexLive = false
	c.indexReplay = nil
	after := c.indexSeq
	go func() {
		if c.rpc.Call(context.Background(), "subscribe", map[string]any{"stream": model.IndexStream, "afterSeq": after}, nil) != nil {
			c.d.Post(func() { c.indexOutstanding = max(0, c.indexOutstanding-1) })
		}
	}()
}

func (c *ServerConnection) storeForStream(stream string) *SessionStore {
	id, ok := strings.CutPrefix(stream, "session:")
	if !ok {
		return nil
	}
	return c.stores[id]
}

// Session stores (sessionHost)

// SessionStore returns the store for a session, which subscribes while a
// view shows it (Activate).
func (c *ServerConnection) SessionStore(id string) *SessionStore {
	s := c.stores[id]
	if s == nil {
		s = newSessionStore(id, c)
		s.Session = c.Sessions[id]
		c.stores[id] = s
	}
	return s
}

func (c *ServerConnection) activate(s *SessionStore) {
	if c.State.Kind == Connected {
		c.subscribe(s)
	}
}

func (c *ServerConnection) deactivate(s *SessionStore) {
	go c.rpc.Call(context.Background(), "unsubscribe", map[string]any{"stream": model.SessionStream(s.ID)}, nil)
}

func (c *ServerConnection) subscribe(s *SessionStore) {
	s.beginReplay()
	after := s.LastSeq
	go func() {
		if c.rpc.Call(context.Background(), "subscribe", map[string]any{"stream": model.SessionStream(s.ID), "afterSeq": after}, nil) != nil {
			c.d.Post(s.subscriptionFailed)
		}
	}()
}

func (c *ServerConnection) post(f func()) { c.d.Post(f) }

func (c *ServerConnection) drafts() *Drafts { return c.draftsRef }

func (c *ServerConnection) callTimeout(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	return c.rpc.CallTimeout(ctx, method, params, out, timeout)
}

func (c *ServerConnection) deliver(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	return c.Deliver(ctx, method, params, out, timeout)
}

// API

// Call sends a request; see RPCClient.Call.
func (c *ServerConnection) Call(ctx context.Context, method string, params, out any) error {
	return c.rpc.Call(ctx, method, params, out)
}

// Deliver calls a command that carries a commandId until it gets through:
// when the socket drops or rad doesn't answer, it waits for the connection
// to come back and sends it again. rad applies a commandId once, so a
// repeat of one that got there returns the first result. It fails once the
// connection is stopped or gives up.
func (c *ServerConnection) Deliver(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	for attempt := 1; ; attempt++ {
		if err := c.waitUntilConnected(ctx); err != nil {
			return err
		}
		err := c.rpc.CallTimeout(ctx, method, params, out, timeout)
		if err == nil {
			return nil
		}
		code := ErrorCode(err)
		if (code != "disconnected" && code != "timeout") || attempt >= c.MaxAttempts {
			return err
		}
	}
}

func (c *ServerConnection) waitUntilConnected(ctx context.Context) error {
	for {
		switch c.phase.Load() {
		case phaseStopped:
			return errDisconnected()
		case phaseConnected:
			// The socket goes before State hears that it closed.
			if c.rpc.IsConnected() {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// RefreshHarnesses loads the agents installed on the server; force has rad
// look again rather than answer from what it found last.
func (c *ServerConnection) RefreshHarnesses(ctx context.Context, force bool) {
	var r harnessList
	if c.rpc.CallTimeout(ctx, "harness.list", map[string]any{"refresh": force}, &r, 45*time.Second) != nil {
		return // keep what we had
	}
	if r.Harnesses == nil {
		r.Harnesses = []model.HarnessInfo{}
	}
	c.d.Post(func() {
		c.Harnesses = r.Harnesses
		c.Version++
	})
}

func (c *ServerConnection) Harness(id string) *model.HarnessInfo {
	for i := range c.Harnesses {
		if c.Harnesses[i].ID == id {
			return &c.Harnesses[i]
		}
	}
	return nil
}

// UploadImage uploads image bytes to attach to a prompt, and keeps them in
// the image cache.
func (c *ServerConnection) UploadImage(ctx context.Context, data []byte) (model.ImageRef, error) {
	ref, err := c.rpc.UploadImage(ctx, data)
	if err == nil && c.Images != nil {
		c.Images.Store(ref.ID, data)
	}
	return ref, err
}

// Image returns an image's bytes, from the cache or rad.
func (c *ServerConnection) Image(ctx context.Context, id string) ([]byte, error) {
	if c.Images != nil {
		return c.Images.Load(id, func() ([]byte, error) { return c.rpc.Image(ctx, id) })
	}
	return c.rpc.Image(ctx, id)
}

func (c *ServerConnection) ListDirectory(ctx context.Context, path string) (FSListing, error) {
	var l FSListing
	err := c.rpc.Call(ctx, "fs.list", map[string]any{"path": path}, &l)
	return l, err
}

func (c *ServerConnection) AddProject(ctx context.Context, path string) (model.Project, error) {
	var p model.Project
	if err := c.rpc.Call(ctx, "project.add", map[string]any{"path": path}, &p); err != nil {
		return p, err
	}
	c.d.Post(func() {
		c.Projects[p.ID] = &p
		c.Version++
	})
	return p, nil
}

func (c *ServerConnection) RemoveProject(ctx context.Context, id string) error {
	return c.rpc.Call(ctx, "project.remove", map[string]any{"id": id}, nil)
}

func (c *ServerConnection) Branches(ctx context.Context, projectID string) (Branches, error) {
	var b Branches
	err := c.rpc.Call(ctx, "git.branches", map[string]any{"projectId": projectID}, &b)
	return b, err
}

func (c *ServerConnection) CreateSession(ctx context.Context, p NewSession) (model.Session, error) {
	params := map[string]any{
		"commandId": uuid.NewString(), "projectId": p.ProjectID, "harness": p.Harness,
		"workspace": p.Workspace,
	}
	for k, v := range map[string]string{"model": p.Model, "effort": p.Effort, "mode": p.Mode, "prompt": p.Prompt} {
		if v != "" {
			params[k] = v
		}
	}
	if ids := imageIDs(p.Images); ids != nil {
		params["images"] = ids
	}
	if p.Draft {
		params["draft"] = true
	}
	var s model.Session
	if err := c.Deliver(ctx, "session.create", params, &s, 90*time.Second); err != nil {
		return s, err
	}
	c.d.Post(func() {
		c.Sessions[s.ID] = &s
		c.Version++
	})
	return s, nil
}

// Fork makes a session that goes on from another after one of its turns,
// as CreateSession makes one: sent again after a drop, with the same
// commandId.
func (c *ServerConnection) Fork(ctx context.Context, p ForkSession) (model.Session, error) {
	params := map[string]any{"commandId": uuid.NewString(), "sessionId": p.SessionID}
	if p.Turn > 0 {
		params["turn"] = p.Turn
	}
	for k, v := range map[string]string{"harness": p.Harness, "model": p.Model, "effort": p.Effort, "mode": p.Mode, "prompt": p.Prompt, "title": p.Title} {
		if v != "" {
			params[k] = v
		}
	}
	if p.Workspace != nil {
		params["workspace"] = p.Workspace
	}
	if ids := imageIDs(p.Images); ids != nil {
		params["images"] = ids
	}
	if p.Draft {
		params["draft"] = true
	}
	var s model.Session
	if err := c.Deliver(ctx, "session.fork", params, &s, 90*time.Second); err != nil {
		return s, err
	}
	c.d.Post(func() {
		c.Sessions[s.ID] = &s
		c.Version++
	})
	return s, nil
}

// Importable lists the sessions the agent ran in the project's folder
// outside rad, such as in a terminal, newest first, for Import.
func (c *ServerConnection) Importable(ctx context.Context, projectID, harness string) ([]model.ImportableSession, error) {
	var res importableList
	err := c.callTimeout(ctx, "session.importable", map[string]any{"projectId": projectID, "harness": harness}, &res, 60*time.Second)
	return res.Sessions, err
}

// Import picks up one of the agent's own sessions as a session of rad's,
// whose next prompt goes on with it, as CreateSession makes one: sent again
// after a drop, with the same commandId.
func (c *ServerConnection) Import(ctx context.Context, p ImportSession) (model.Session, error) {
	params := map[string]any{"commandId": uuid.NewString(), "projectId": p.ProjectID, "harness": p.Harness, "nativeId": p.NativeID}
	for k, v := range map[string]string{"model": p.Model, "effort": p.Effort, "mode": p.Mode} {
		if v != "" {
			params[k] = v
		}
	}
	var s model.Session
	if err := c.Deliver(ctx, "session.import", params, &s, 90*time.Second); err != nil {
		return s, err
	}
	c.d.Post(func() {
		c.Sessions[s.ID] = &s
		c.Version++
	})
	return s, nil
}

// SaveDraft keeps a prompt in the session, unsent, for any device to send
// later, in place of the draft it had; no text and no images remove it.
// Saving it twice does no harm, so it is sent again after a drop, as
// commands are.
func (c *ServerConnection) SaveDraft(ctx context.Context, sessionID, text string, images []model.ImageRef) (model.Session, error) {
	params := map[string]any{"sessionId": sessionID, "text": text}
	if ids := imageIDs(images); ids != nil {
		params["images"] = ids
	}
	var s model.Session
	if err := c.Deliver(ctx, "session.saveDraft", params, &s, 30*time.Second); err != nil {
		return s, err
	}
	c.d.Post(func() {
		c.Sessions[s.ID] = &s
		if st := c.stores[s.ID]; st != nil {
			st.setSession(&s)
		}
		c.Version++
	})
	return s, nil
}

func (c *ServerConnection) Archive(ctx context.Context, sessionID string, removeWorktree bool) error {
	return c.rpc.Call(ctx, "session.archive", map[string]any{"sessionId": sessionID, "removeWorktree": removeWorktree}, nil)
}

// DeleteSession deletes a session and its transcript for good; the index
// then drops it.
func (c *ServerConnection) DeleteSession(ctx context.Context, sessionID string, removeWorktree bool) error {
	return c.rpc.Call(ctx, "session.delete", map[string]any{"sessionId": sessionID, "removeWorktree": removeWorktree}, nil)
}

// MarkRead marks the session read on rad, for every device, when the index
// has it unread. Its row loses the mark at once. An older rad doesn't know
// the method; an index that still has it unread calls again.
func (c *ServerConnection) MarkRead(sessionID string) {
	s := c.Sessions[sessionID]
	if s == nil || !s.Unread {
		return
	}
	read := *s
	read.Unread = false
	c.Sessions[sessionID] = &read
	c.Version++
	go c.rpc.Call(context.Background(), "session.markRead", map[string]any{"sessionId": sessionID}, nil)
}

// Unpair revokes this device on rad, connecting first if need be. Best
// effort: the caller forgets the token either way.
func (c *ServerConnection) Unpair(ctx context.Context) error {
	if !c.rpc.IsConnected() {
		if _, err := c.rpc.Connect(ctx, c.URLs()); err != nil {
			return err
		}
	}
	return c.rpc.CallTimeout(ctx, "device.unpair", nil, nil, 5*time.Second)
}

// Managing the server

func (c *ServerConnection) Settings(ctx context.Context) (model.ServerSettings, error) {
	var s model.ServerSettings
	err := c.rpc.Call(ctx, "server.settings", nil, &s)
	return s, err
}

// SetSettings replaces the server's settings and returns them as rad keeps
// them.
func (c *ServerConnection) SetSettings(ctx context.Context, s model.ServerSettings) (model.ServerSettings, error) {
	var out model.ServerSettings
	err := c.rpc.Call(ctx, "server.setSettings", s, &out)
	return out, err
}

// CheckUpdate asks whether rad has an update; refresh looks again rather
// than use the answer of the last few minutes.
func (c *ServerConnection) CheckUpdate(ctx context.Context, refresh bool) (model.UpdateInfo, error) {
	var u model.UpdateInfo
	err := c.rpc.CallTimeout(ctx, "server.checkUpdate", map[string]any{"refresh": refresh}, &u, 45*time.Second)
	if u.Busy == nil {
		u.Busy = []string{}
	}
	return u, err
}

// UpdateServer installs rad's latest release, which restarts it.
func (c *ServerConnection) UpdateServer(ctx context.Context) (model.Task, error) {
	var t model.Task
	err := c.rpc.CallTimeout(ctx, "server.update", nil, &t, 45*time.Second)
	return t, err
}

// UpdateHarness runs the agent's own updater.
func (c *ServerConnection) UpdateHarness(ctx context.Context, id string) (model.Task, error) {
	var t model.Task
	err := c.rpc.Call(ctx, "harness.update", map[string]any{"harness": id}, &t)
	return t, err
}

// LoginHarness runs the agent's sign-in.
func (c *ServerConnection) LoginHarness(ctx context.Context, id string) (model.Task, error) {
	var t model.Task
	err := c.rpc.Call(ctx, "harness.login", map[string]any{"harness": id}, &t)
	return t, err
}

func (c *ServerConnection) Task(ctx context.Context, id string) (model.Task, error) {
	var t model.Task
	err := c.rpc.Call(ctx, "task.get", map[string]any{"id": id}, &t)
	return t, err
}

// Tasks lists the tasks that run on rad, and those that ended lately,
// oldest first.
func (c *ServerConnection) Tasks(ctx context.Context) ([]model.Task, error) {
	var r taskList
	err := c.rpc.Call(ctx, "task.list", nil, &r)
	return r.Tasks, err
}

// TaskInput types a line into the task's command, such as a code to paste.
func (c *ServerConnection) TaskInput(ctx context.Context, id, text string) error {
	return c.rpc.Call(ctx, "task.input", map[string]any{"id": id, "text": text}, nil)
}

func (c *ServerConnection) CancelTask(ctx context.Context, id string) error {
	return c.rpc.Call(ctx, "task.cancel", map[string]any{"id": id}, nil)
}

// ServerVersion is the version of rad last connected to. Safe from any
// goroutine.
func (c *ServerConnection) ServerVersion() string {
	return c.serverVersion.Load().(string)
}

// TaskEnded: an agent's update or sign-in ended, so its version and
// sign-in may have changed.
func (c *ServerConnection) TaskEnded(ctx context.Context, _ model.Task) {
	c.RefreshHarnesses(ctx, true)
}

// Derived

func (c *ServerConnection) ActiveSessions() []*model.Session {
	return c.sessions(false)
}

func (c *ServerConnection) ArchivedSessions() []*model.Session {
	return c.sessions(true)
}

func (c *ServerConnection) sessions(archived bool) []*model.Session {
	var out []*model.Session
	for _, s := range c.Sessions {
		if s.Archived == archived {
			out = append(out, s)
		}
	}
	SortByUpdated(out)
	return out
}

// SortByUpdated sorts sessions the latest first.
func SortByUpdated(sessions []*model.Session) {
	slices.SortFunc(sessions, func(a, b *model.Session) int {
		if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
}

// SortedProjects are the projects by name, ignoring case.
func (c *ServerConnection) SortedProjects() []*model.Project {
	out := make([]*model.Project, 0, len(c.Projects))
	for _, p := range c.Projects {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b *model.Project) int {
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}
