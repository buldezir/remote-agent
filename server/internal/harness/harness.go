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
	"unicode"

	"remote-agent/internal/model"
)

type OpenOptions struct {
	SessionID string
	Cwd       string
	Model     string
	Effort    string // reasoning effort; empty for the harness default
	Mode      string
	ResumeID  string // native session/thread id to resume; empty for a fresh session
	// Fork opens a copy of ResumeID's conversation, up to and including the
	// turn that ended at ForkAt (its TurnEnded.ForkPoint), as a conversation
	// of its own with a new native id (Forker).
	Fork     bool
	ForkAt   string
	Log      *slog.Logger
	DiagPath string     // file to append raw protocol frames to (NDJSON); empty disables
	Images   ImageStore // keeps images from agent output; nil drops them
	// Instructions are added to the agent's system prompt, or put before the
	// first prompt of a session where there is none to add to.
	Instructions string
	// MCP is rad's MCP server, whose tools start and drive other sessions;
	// nil: the agent doesn't get it.
	MCP *MCPServer
}

// MCPServer is how an agent reaches rad's MCP server: over HTTP at URL with
// the session's Token, which adapters pass in the agent's environment as
// MCPTokenEnv so that it stays out of its command line, or through Command,
// a stdio server (`rad mcp`) that reads MCPURLEnv and MCPTokenEnv.
type MCPServer struct {
	URL     string
	Token   string
	Command []string
}

// MCPName is the name agents know rad's MCP server by.
const (
	MCPName     = "rad"
	MCPURLEnv   = "RAD_MCP_URL"
	MCPTokenEnv = "RAD_MCP_TOKEN"
)

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

// Compact summarizes the conversation so far, to make room in the context
// window. Claude Code runs it from the prompt; other adapters that list it
// run it with their agent's own request.
var Compact = model.Command{Name: "compact", Description: "Free up context by summarizing the conversation so far"}

// Command reports whether the prompt runs the slash command name, as
// "/compact" or "/compact <text for it>", and returns the text after it.
func (in Input) Command(name string) (args string, ok bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(in.Text), "/"+name)
	if !ok || len(in.Images) > 0 || rest != "" && !unicode.IsSpace(rune(rest[0])) {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// Event is emitted by a Runtime. Implementations: ItemEvent, ApprovalEvent,
// ApprovalCancelled, TurnStarted, TurnEnded, NativeID, ModeChanged,
// ContextUsage, ModelInfo, BackgroundTasks, Exited.
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

// TurnStarted reports a turn the agent began on its own, such as to report
// on a background task that ended. Turns that Prompt starts don't send it.
// Notice, if set, says why, for the transcript.
type TurnStarted struct{ Notice string }

type TurnEnded struct {
	Status model.TurnStatus
	Usage  *model.Usage
	Error  string
	// ForkPoint is where the conversation stands after the turn, in the
	// agent's own terms, for OpenOptions.ForkAt; empty where it can't fork.
	ForkPoint string
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

// BackgroundTasks lists the tasks the agent runs in the background, in full
// each time. They end with the process, so it should not be stopped as idle.
type BackgroundTasks struct{ Tasks []model.BackgroundTask }

// WorkdirChanged reports the folder the agent moved to, such as a worktree
// it made itself, or back to.
type WorkdirChanged struct{ Dir string }

// Exited is the last event; the runtime is unusable afterwards.
type Exited struct{ Err error }

func (ItemEvent) isEvent()         {}
func (ApprovalEvent) isEvent()     {}
func (ApprovalCancelled) isEvent() {}
func (TurnStarted) isEvent()       {}
func (TurnEnded) isEvent()         {}
func (NativeID) isEvent()          {}
func (ModeChanged) isEvent()       {}
func (ContextUsage) isEvent()      {}
func (ModelInfo) isEvent()         {}
func (BackgroundTasks) isEvent()   {}
func (WorkdirChanged) isEvent()    {}
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
	// finishes with a TurnEnded event. Callers must not overlap turns,
	// including those the agent starts itself (TurnStarted).
	Prompt(ctx context.Context, in Input) error
	Interrupt(ctx context.Context) error
	Respond(ctx context.Context, approvalID string, r Response) error
	SetMode(ctx context.Context, mode string) error
	Close() error
}

// Steerer is implemented by runtimes whose agent takes a prompt into the
// turn it is running, without interrupting it (HarnessCaps.Steer): it reads
// it at its next step, such as after the tool call it is running, and goes
// on with it in the same turn, which ends with one TurnEnded.
type Steerer interface {
	// Steer returns once the agent took the prompt, or ErrNoTurn when no turn
	// of its runs to take it, such as one that ended meanwhile.
	Steer(ctx context.Context, in Input) error
}

// ErrNoTurn is Steer's answer when the agent has no turn running.
var ErrNoTurn = errors.New("the agent has no turn running")

// CommandDiscovery advertises listing support before an agent is opened.
type CommandDiscovery interface {
	SupportsCommands() bool
}

// CommandLister discovers commands without a turn, concurrently with Prompt, Interrupt and Close.
type CommandLister interface {
	Commands(context.Context) ([]model.Command, error)
}

type Harness interface {
	ID() string
	Probe(ctx context.Context) model.HarnessInfo
	Open(ctx context.Context, o OpenOptions) (Runtime, error)
}

// Forker is implemented by harnesses whose agent copies a conversation into
// a new one, cut after a turn: OpenOptions.Fork. Agents of other harnesses
// get a fork's conversation as text.
type Forker interface {
	SupportsFork() bool
}

// Importer is implemented by harnesses whose agent keeps its sessions in
// files that rad can read, such as the ones the user ran in a terminal, so
// that rad can pick one up (session.import): it lists those that ran in a
// folder, and reads one's conversation. The session's next prompt resumes it
// as rad resumes its own (OpenOptions.ResumeID).
type Importer interface {
	// ListNative lists the agent's sessions that ran in dir, newest first, at
	// most ImportLimit of them. It leaves out the ones in known (rad's own,
	// by their native ids), any other that rad started, sub-agents' and
	// sessions too short to go on with. Active is left to the caller.
	ListNative(ctx context.Context, dir string, known map[string]bool) ([]model.ImportableSession, error)
	// ReadNative reads the conversation of the session id that ran in dir,
	// keeping the images in it. It returns ErrNotFound if there's none.
	ReadNative(ctx context.Context, dir, id string, images ImageStore) (*Transcript, error)
}

// ImportLimit is how many sessions ListNative lists at most.
const ImportLimit = 30

// ErrNotFound is ReadNative's answer for a session that isn't there.
var ErrNotFound = errors.New("no such session")

// Transcript is an agent's own record of a session: its title, if it gave
// one, the model it last ran with, and its turns.
type Transcript struct {
	Title     string // the agent's or the user's name for it; empty: none
	Model     string
	Turns     []TranscriptTurn
	UpdatedAt time.Time // when the agent last wrote to it
}

// TranscriptTurn is one turn of a Transcript: a prompt and what the agent
// did with it, or a turn the agent started itself. Items' IDs and
// ParentItemIDs are the adapter's keys, as in ItemEvent; Approval.ToolItemID
// too.
type TranscriptTurn struct {
	Items     []model.Item
	Status    model.TurnStatus
	Error     string
	StartedAt time.Time
	EndedAt   time.Time
	ForkPoint string // as TurnEnded.ForkPoint
}

// OneShotter is implemented by harnesses that can be the system agent,
// which names sessions: they answer one prompt without tools, in an empty
// folder, and keep nothing of the conversation, so it shows up in neither
// the agent's history nor its sessions.
type OneShotter interface {
	OneShot(ctx context.Context, o OneShotOptions) (string, error)
}

type OneShotOptions struct {
	Cwd    string // an empty folder, removed afterwards
	Model  string // empty: the harness's default
	Effort string
	Prompt string
}

// Command is a command line, with the variables to add to and remove from
// rad's environment for it.
type Command struct {
	Args  []string
	Env   map[string]string
	Unset []string
}

// Maintainer is implemented by harnesses whose CLI can update itself, or
// sign in again, from a command. rad runs them in a pseudo-terminal, so the
// user can answer them, such as by pasting a code. ok is false for one the
// CLI doesn't have.
type Maintainer interface {
	UpdateCommand() (cmd Command, ok bool)
	LoginCommand() (cmd Command, ok bool)
}

// Configured returns the command a config sets, if it sets one: an empty
// list turns it off. Otherwise it returns def, which may be nil (none).
func Configured(configured, def []string) ([]string, bool) {
	if configured != nil {
		return configured, len(configured) > 0
	}
	return def, len(def) > 0
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
	// signedOut holds why an agent's turn failed for want of a login, which
	// a probe may not see, such as an expired token. It lasts until the
	// agent signs in, or a turn of its completes.
	signedOut map[string]string
}

type cachedInfo struct {
	info model.HarnessInfo
	at   time.Time
}

func NewRegistry(hs ...Harness) *Registry {
	return &Registry{list: hs, info: map[string]cachedInfo{}, signedOut: map[string]string{}}
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
		return r.withLogin(c.info)
	}
	pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	info := h.Probe(pctx)
	setCaps(h, &info)
	r.mu.Lock()
	r.info[h.ID()] = cachedInfo{info, time.Now()}
	r.mu.Unlock()
	return r.withLogin(info)
}

// withLogin marks an agent whose turn failed for want of a login.
func (r *Registry) withLogin(info model.HarnessInfo) model.HarnessInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	if why := r.signedOut[info.ID]; why != "" && info.Installed && info.AuthOK {
		info.AuthOK, info.Hint = false, why
	}
	return info
}

// SignedOut records that the agent needs to sign in again, as a turn's
// error said.
func (r *Registry) SignedOut(id, why string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.signedOut[id] = why
}

// SignedIn forgets SignedOut: the agent signed in, or worked.
func (r *Registry) SignedIn(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.signedOut, id)
}

// authErrors are what agents say, in lower case, when they have no login
// or it expired.
var authErrors = []string{
	"/login", "authentication_error", "oauth token has expired", "token has expired", "not logged in",
	"invalid api key", "401 unauthorized", "api error: 401", "status 401", "could not be refreshed",
	"codex login", "log in again", "sign in again", "please log in", "please login", "authentication required",
}

// IsAuthError reports whether an agent's error says it has to sign in
// (again).
func IsAuthError(msg string) bool {
	msg = strings.ToLower(msg)
	for _, p := range authErrors {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// setCaps adds what the harness implements beyond sessions, once it is
// installed.
func setCaps(h Harness, info *model.HarnessInfo) {
	if !info.Installed {
		return
	}
	if m, ok := h.(Maintainer); ok {
		_, info.Caps.Update = m.UpdateCommand()
		_, info.Caps.Login = m.LoginCommand()
	}
	_, info.Caps.OneShot = h.(OneShotter)
	_, info.Caps.Import = h.(Importer)
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
