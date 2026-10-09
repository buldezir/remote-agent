// Package harness defines the adapter interface that normalizes coding-agent
// CLIs (Claude Code, Codex, ACP agents) into a common event model.
package harness

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"remote-agent/internal/model"
)

type OpenOptions struct {
	SessionID string
	Cwd       string
	Model     string
	Mode      string
	ResumeID  string // native session/thread id to resume; empty for a fresh session
	Log       *slog.Logger
	DiagPath  string // file to append raw protocol frames to (NDJSON); empty disables
}

// Event is emitted by a Runtime. Implementations: ItemEvent, ApprovalEvent,
// ApprovalCancelled, TurnEnded, NativeID, ModeChanged, ContextUsage, Exited.
type Event interface{ isEvent() }

// ItemEvent carries the full current state of a transcript item. Item.ID and
// Item.ParentItemID are adapter-local keys, stable within a session; the
// orchestrator maps them to global ids and fills session/turn/order fields.
type ItemEvent struct{ Item model.Item }

// ApprovalEvent asks the user to decide on something. ID is adapter-local and
// is passed back to Runtime.Respond. Approval.ToolItemID is an adapter key.
type ApprovalEvent struct {
	ID       string
	Approval model.Approval
}

// ApprovalCancelled withdraws a pending approval (e.g. the turn was interrupted).
type ApprovalCancelled struct{ ID string }

type TurnEnded struct {
	Status model.TurnStatus
	Usage  *model.Usage
	Error  string
}

// NativeID reports the harness's own session/thread id once known.
type NativeID struct{ ID string }

type ModeChanged struct{ Mode string }

// ContextUsage reports how many tokens the conversation occupies in the
// model's context window. Window is 0 if unknown; the last known one is kept.
type ContextUsage struct{ Used, Window int64 }

// Exited is the last event; the runtime is unusable afterwards.
type Exited struct{ Err error }

func (ItemEvent) isEvent()         {}
func (ApprovalEvent) isEvent()     {}
func (ApprovalCancelled) isEvent() {}
func (TurnEnded) isEvent()         {}
func (NativeID) isEvent()          {}
func (ModeChanged) isEvent()       {}
func (ContextUsage) isEvent()      {}
func (Exited) isEvent()            {}

type Response struct {
	OptionID string
	Message  string
	Answers  map[string]string
}

type Runtime interface {
	NativeID() string
	Events() <-chan Event
	// Prompt starts a turn and returns once the harness accepted it. The turn
	// finishes with a TurnEnded event. Callers must not overlap turns.
	Prompt(ctx context.Context, text string) error
	Interrupt(ctx context.Context) error
	Respond(ctx context.Context, approvalID string, r Response) error
	SetMode(ctx context.Context, mode string) error
	Close() error
}

type Harness interface {
	ID() string
	Probe(ctx context.Context) model.HarnessInfo
	Open(ctx context.Context, o OpenOptions) (Runtime, error)
}

// Overlayer is implemented by harnesses that learn details (modes, models)
// from live sessions; the registry applies it on top of cached probes.
type Overlayer interface {
	Overlay(info *model.HarnessInfo)
}

var ErrUnsupported = errors.New("not supported by this harness")

// Registry holds the configured harnesses and caches probe results.
type Registry struct {
	list []Harness
	mu   sync.Mutex
	info map[string]cachedInfo
}

type cachedInfo struct {
	info model.HarnessInfo
	at   time.Time
}

func NewRegistry(hs ...Harness) *Registry {
	return &Registry{list: hs, info: map[string]cachedInfo{}}
}

func (r *Registry) Get(id string) (Harness, bool) {
	for _, h := range r.list {
		if h.ID() == id {
			return h, true
		}
	}
	return nil, false
}

const probeTTL = 2 * time.Minute

// Infos probes all harnesses concurrently, using cached results when fresh.
func (r *Registry) Infos(ctx context.Context, refresh bool) []model.HarnessInfo {
	out := make([]model.HarnessInfo, len(r.list))
	var wg sync.WaitGroup
	for i, h := range r.list {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = r.Info(ctx, h, refresh)
		}()
	}
	wg.Wait()
	return out
}

func (r *Registry) Info(ctx context.Context, h Harness, refresh bool) model.HarnessInfo {
	r.mu.Lock()
	c, ok := r.info[h.ID()]
	r.mu.Unlock()
	if ok && !refresh && time.Since(c.at) < probeTTL {
		if o, ok := h.(Overlayer); ok {
			o.Overlay(&c.info)
		}
		return c.info
	}
	pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	info := h.Probe(pctx)
	r.mu.Lock()
	r.info[h.ID()] = cachedInfo{info, time.Now()}
	r.mu.Unlock()
	return info
}
