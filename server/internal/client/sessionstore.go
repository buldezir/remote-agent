package client

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"remote-agent/internal/model"
)

// sessionHost is what a session store needs from its connection.
type sessionHost interface {
	callTimeout(ctx context.Context, method string, params, out any, timeout time.Duration) error
	// deliver calls a command with a commandId, sending it again after a drop.
	deliver(ctx context.Context, method string, params, out any, timeout time.Duration) error
	activate(*SessionStore)
	deactivate(*SessionStore)
	subscribe(*SessionStore)
	post(func())
	drafts() *Drafts
}

// Job is a command's network part, to run off the dispatcher. It changes
// state only by posting.
type Job func(ctx context.Context) error

// Outgoing is a prompt on its way to rad.
type Outgoing struct {
	CommandID string
	Text      string
	Images    []model.ImageRef
}

// SessionStore is the live state of one session: its turns and transcript
// items. Its fields belong to the dispatcher.
type SessionStore struct {
	ID      string
	Session *model.Session
	Turns   map[string]*model.Turn
	Items   map[string]*model.Item
	// Transcript is the top-level items in order (a tool call's children
	// are under it, in ChildrenOf).
	Transcript []*model.Item
	Synced     bool
	// Removed: the session was deleted on rad; its stream says so.
	Removed bool
	// Outbox holds prompts sent and not yet in the transcript, in the order
	// sent.
	Outbox []Outgoing
	// Version counts changes, for views that cache what they drew.
	Version uint64

	LastSeq int64
	Live    bool

	host sessionHost
	// Views showing the session; two can show the same one.
	viewers  int
	children map[string][]*model.Item
	// Replayed events wait for synchronized, then apply in one step (after
	// dropping the cache if rad says reset).
	replay []model.Event
	// Subscriptions asked for and not yet synchronized. Two can overlap (a
	// view subscribes as the connection resubscribes everything): only the
	// last one's synchronized makes the store live, so a newer replay is
	// buffered rather than skipped, and its reset can't drop what it
	// replays.
	outstanding int
}

func newSessionStore(id string, host sessionHost) *SessionStore {
	return &SessionStore{ID: id, host: host, Turns: map[string]*model.Turn{}, Items: map[string]*model.Item{}, children: map[string][]*model.Item{}}
}

func (s *SessionStore) IsActive() bool { return s.viewers > 0 }

// Draft is the prompt being written and not sent yet. It outlives the
// session's view, and ra.
func (s *SessionStore) Draft() string {
	if s.host == nil {
		return ""
	}
	return s.host.drafts().Get(s.ID)
}

func (s *SessionStore) SetDraft(text string) {
	if s.host != nil {
		s.host.drafts().Set(s.ID, text)
	}
}

// Activate starts updates, when a view shows the session. They stop when
// every Activate has had its Deactivate.
func (s *SessionStore) Activate() {
	s.viewers++
	if s.viewers == 1 && s.host != nil {
		s.host.activate(s)
	}
}

func (s *SessionStore) Deactivate() {
	if s.viewers == 0 {
		return
	}
	s.viewers--
	if s.viewers > 0 {
		return
	}
	s.Live = false
	if s.host != nil {
		s.host.deactivate(s)
	}
}

// Sync

func (s *SessionStore) beginReplay() {
	s.outstanding++
	s.Live = false
	s.replay = nil
}

// subscriptionFailed: a subscribe call failed, so its synchronized won't come.
func (s *SessionStore) subscriptionFailed() {
	s.outstanding = max(0, s.outstanding-1)
}

// closed: the socket closed, so no subscription is outstanding any more.
func (s *SessionStore) closed() {
	s.outstanding = 0
	s.Live = false
}

func (s *SessionStore) synchronized(seq int64, reset bool) {
	s.outstanding = max(0, s.outstanding-1)
	if reset {
		clear(s.Turns)
		clear(s.Items)
	}
	buffered := s.replay
	s.replay = nil
	for i := range buffered {
		s.upsert(&buffered[i])
	}
	s.LastSeq = seq
	s.Live = s.outstanding == 0
	s.Synced = true
	s.rebuild()
}

func (s *SessionStore) apply(events []model.Event) {
	if !s.Live {
		s.replay = append(s.replay, events...)
		return
	}
	for i := range events {
		ev := &events[i]
		if ev.Seq <= s.LastSeq {
			continue
		}
		if ev.Seq != s.LastSeq+1 {
			// A gap: something was missed. Resubscribe from what we have.
			s.Live = false
			if s.host != nil {
				s.host.subscribe(s)
			}
			break
		}
		s.LastSeq = ev.Seq
		s.upsert(ev)
	}
	s.rebuild()
}

// setSession sets the connection's copy of the session, from the index.
func (s *SessionStore) setSession(session *model.Session) {
	s.Session = session
	s.Version++
}

func (s *SessionStore) upsert(ev *model.Event) {
	switch ev.Type {
	case model.EvSessionUpserted:
		if ev.Session != nil {
			s.Session = ev.Session
		}
	case model.EvTurnUpserted:
		if ev.Turn != nil {
			s.Turns[ev.Turn.ID] = ev.Turn
		}
	case model.EvItemUpserted:
		if ev.Item != nil {
			s.Items[ev.Item.ID] = ev.Item
		}
	case model.EvSessionRemoved:
		s.Session = nil
		clear(s.Turns)
		clear(s.Items)
		s.Outbox = nil
		s.Removed = true
	}
}

func (s *SessionStore) rebuild() {
	var top []*model.Item
	kids := map[string][]*model.Item{}
	for _, it := range s.Items {
		if it.ParentItemID != "" && s.Items[it.ParentItemID] != nil {
			kids[it.ParentItemID] = append(kids[it.ParentItemID], it)
		} else {
			top = append(top, it)
		}
	}
	byOrder := func(a, b *model.Item) int {
		return cmp.Or(cmp.Compare(a.Order, b.Order), strings.Compare(a.ID, b.ID))
	}
	slices.SortFunc(top, byOrder)
	for _, list := range kids {
		slices.SortFunc(list, byOrder)
	}
	// Drop the prompts rad has confirmed.
	confirmed := map[string]bool{}
	for _, it := range top {
		if it.Kind == model.ItemUserMessage {
			confirmed[outgoingKey(it.Text, it.Images)] = true
		}
	}
	s.Outbox = slices.DeleteFunc(s.Outbox, func(o Outgoing) bool { return confirmed[outgoingKey(o.Text, o.Images)] })
	s.Transcript = top
	s.children = kids
	s.Version++
}

// ChildrenOf returns the items under a tool call, such as a sub-agent's.
func (s *SessionStore) ChildrenOf(it *model.Item) []*model.Item {
	return s.children[it.ID]
}

func (s *SessionStore) SortedTurns() []*model.Turn {
	turns := make([]*model.Turn, 0, len(s.Turns))
	for _, t := range s.Turns {
		turns = append(turns, t)
	}
	slices.SortFunc(turns, func(a, b *model.Turn) int { return a.N - b.N })
	return turns
}

func (s *SessionStore) PendingApprovals() []*model.Item {
	var out []*model.Item
	for _, it := range s.Transcript {
		if it.Kind == model.ItemApproval && it.Status == model.ItemPending {
			out = append(out, it)
		}
	}
	return out
}

// UnsentDraft is the session's draft, unless it is on its way: SendDraft
// put it in the outbox, where it shows until the transcript has it.
func (s *SessionStore) UnsentDraft() *model.Draft {
	if s.Session == nil || s.Session.Draft == nil {
		return nil
	}
	d := s.Session.Draft
	key := outgoingKey(d.Text, d.Images)
	for _, o := range s.Outbox {
		if outgoingKey(o.Text, o.Images) == key {
			return nil
		}
	}
	return d
}

func (s *SessionStore) IsRunning() bool {
	return s.Session != nil && (s.Session.Status == model.SessionRunning || s.Session.Status == model.SessionAwaitingApproval)
}

// Commands

// Send puts a prompt in the outbox, where it shows until the transcript
// has it, and returns the job that sends it. The job waits out a dropped
// connection, and fails once the connection gives up, taking the prompt
// out of the outbox again.
func (s *SessionStore) Send(text string, images []model.ImageRef) Job {
	commandID := uuid.NewString()
	s.Outbox = append(s.Outbox, Outgoing{CommandID: commandID, Text: text, Images: images})
	s.Version++
	return s.deliverOutgoing(commandID, "session.prompt", map[string]any{
		"commandId": commandID, "sessionId": s.ID, "text": text, "images": imageIDs(images),
	})
}

// SendDraft sends the session's draft as Send sends a prompt: it shows in
// the outbox until the transcript has it, and the event that adds it there
// takes the draft away. A retry after a drop has the same commandId, so it
// goes once.
func (s *SessionStore) SendDraft() (Job, error) {
	if s.Session == nil || s.Session.Draft == nil {
		return nil, &RPCError{"conflict", "This session has no draft to send."}
	}
	d := s.Session.Draft
	commandID := uuid.NewString()
	s.Outbox = append(s.Outbox, Outgoing{CommandID: commandID, Text: d.Text, Images: d.Images})
	s.Version++
	return s.deliverOutgoing(commandID, "session.sendDraft", map[string]any{"commandId": commandID, "sessionId": s.ID}), nil
}

func (s *SessionStore) deliverOutgoing(commandID, method string, params map[string]any) Job {
	host := s.host
	return func(ctx context.Context) error {
		if host == nil {
			return errDisconnected()
		}
		err := host.deliver(ctx, method, params, nil, 30*time.Second)
		if err != nil {
			host.post(func() {
				s.Outbox = slices.DeleteFunc(s.Outbox, func(o Outgoing) bool { return o.CommandID == commandID })
				s.Version++
			})
		}
		return err
	}
}

func imageIDs(images []model.ImageRef) []string {
	if len(images) == 0 {
		return nil
	}
	ids := make([]string, len(images))
	for i, im := range images {
		ids[i] = im.ID
	}
	return ids
}

func (s *SessionStore) call(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	if s.host == nil {
		return errDisconnected()
	}
	return s.host.callTimeout(ctx, method, params, out, timeout)
}

// Commands lists the session's slash commands and skills. An older rad
// doesn't know the method: none, then.
func (s *SessionStore) Commands(ctx context.Context) ([]model.Command, error) {
	var r commandList
	err := s.call(ctx, "session.commands", map[string]any{"sessionId": s.ID}, &r, 80*time.Second)
	if ErrorCode(err) == "method_not_found" {
		return nil, nil
	}
	return r.Commands, err
}

// Interrupt stops the turn; force kills the agent's process.
func (s *SessionStore) Interrupt(ctx context.Context, force bool) error {
	return s.call(ctx, "session.interrupt", map[string]any{"sessionId": s.ID, "force": force}, nil, 30*time.Second)
}

// SendNow interrupts the turn and starts the next one with this queued
// prompt, ahead of the others.
func (s *SessionStore) SendNow(ctx context.Context, itemID string) error {
	err := s.call(ctx, "session.sendNow", map[string]any{"sessionId": s.ID, "itemId": itemID}, nil, 30*time.Second)
	if ErrorCode(err) == "method_not_found" {
		return &RPCError{"method_not_found", "This rad is too old to send a queued prompt now. Update rad."}
	}
	return err
}

// CancelQueued takes this queued prompt out of the queue, unsent.
func (s *SessionStore) CancelQueued(ctx context.Context, itemID string) error {
	err := s.call(ctx, "session.cancelQueued", map[string]any{"sessionId": s.ID, "itemId": itemID}, nil, 30*time.Second)
	if ErrorCode(err) == "method_not_found" {
		return &RPCError{"method_not_found", "This rad is too old to cancel a queued prompt. Update rad."}
	}
	return err
}

// Respond answers an approval. answers is for a question's: question →
// chosen labels, joined with ", ".
func (s *SessionStore) Respond(ctx context.Context, approvalID, optionID, message string, answers map[string]string) error {
	params := map[string]any{"commandId": uuid.NewString(), "sessionId": s.ID, "approvalId": approvalID, "optionId": optionID}
	if message != "" {
		params["message"] = message
	}
	if len(answers) > 0 {
		params["answers"] = answers
	}
	return s.call(ctx, "approval.respond", params, nil, 30*time.Second)
}

func (s *SessionStore) SetMode(ctx context.Context, mode string) error {
	return s.call(ctx, "session.setMode", map[string]any{"sessionId": s.ID, "mode": mode}, nil, 30*time.Second)
}

func outgoingKey(text string, images []model.ImageRef) string {
	var b strings.Builder
	b.WriteString(text)
	for _, im := range images {
		b.WriteString("\x00")
		b.WriteString(im.ID)
	}
	return b.String()
}
