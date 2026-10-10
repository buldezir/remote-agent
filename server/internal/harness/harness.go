// Package harness defines the adapter interface that normalizes coding-agent
// CLIs (Claude Code, Codex, ACP agents) into a common event model.
package harness

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"remote-agent/internal/model"
)

type OpenOptions struct {
	SessionID string
	Cwd       string
	Model     string
	Effort    string // reasoning effort; empty for the harness default
	Mode      string
	ResumeID  string // native session/thread id to resume; empty for a fresh session
	Log       *slog.Logger
	DiagPath  string     // file to append raw protocol frames to (NDJSON); empty disables
	Images    ImageStore // keeps images from agent output; nil drops them
	// Instructions are added to the agent's system prompt, or put before the
	// first prompt of a session where there is none to add to.
	Instructions string
}

// ImageStore keeps images that agents return (internal/images).
type ImageStore interface {
	PutBase64(data string) (model.ImageRef, error)
	PutFile(path string) (model.ImageRef, error)
}

// Input is a prompt: text and the images attached to it.
type Input struct {
	Text   string
	Images []Image
}

// Image is an attached image, as a file rad keeps.
type Image struct {
	Path     string
	MimeType string
}

// Base64 reads the image, for agents that take images inline.
func (i Image) Base64() (string, error) {
	b, err := os.ReadFile(i.Path)
	if err != nil {
		return "", fmt.Errorf("read attached image: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// WithPaths is the text with the images' paths added, for agents that can't
// take images in a prompt but can open files.
func (in Input) WithPaths() string {
	if len(in.Images) == 0 {
		return in.Text
	}
	var b strings.Builder
	b.WriteString(in.Text)
	if in.Text != "" {
		b.WriteString("\n\n")
	}
	b.WriteString("Attached images (open them from disk):")
	for _, img := range in.Images {
		b.WriteString("\n- " + img.Path)
	}
	return b.String()
}

// Event is emitted by a Runtime. Implementations: ItemEvent, ApprovalEvent,
// ApprovalCancelled, TurnEnded, NativeID, ModeChanged, ContextUsage,
// ModelInfo, Exited.
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

// ModelInfo reports the model and reasoning effort the agent runs with, in
// full each time; empty fields are unknown.
type ModelInfo struct{ ID, Name, Effort string }

// Exited is the last event; the runtime is unusable afterwards.
type Exited struct{ Err error }

func (ItemEvent) isEvent()         {}
func (ApprovalEvent) isEvent()     {}
func (ApprovalCancelled) isEvent() {}
func (TurnEnded) isEvent()         {}
func (NativeID) isEvent()          {}
func (ModeChanged) isEvent()       {}
func (ContextUsage) isEvent()      {}
func (ModelInfo) isEvent()         {}
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
	Prompt(ctx context.Context, in Input) error
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

// Installed is Infos without the harnesses missing from this machine.
func (r *Registry) Installed(ctx context.Context, refresh bool) []model.HarnessInfo {
	return slices.DeleteFunc(r.Infos(ctx, refresh), func(i model.HarnessInfo) bool { return !i.Installed })
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

var effortNames = map[string]string{"xhigh": "Extra high"}

// EffortChoice names a reasoning effort level for pickers.
func EffortChoice(id, description string) model.Choice {
	name := effortNames[id]
	if name == "" && id != "" {
		name = strings.ToUpper(id[:1]) + id[1:]
	}
	return model.Choice{ID: id, Name: name, Description: description}
}
