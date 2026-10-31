// Package model holds the domain entities shared by the store, orchestrator,
// harness adapters and the wire protocol. JSON tags define the wire format
// documented in protocol/PROTOCOL.md.
package model

import (
	"encoding/json"
	"time"
)

const ProtocolVersion = 1

type Project struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	IsGitRepo bool      `json:"isGitRepo"`
	CreatedAt time.Time `json:"createdAt"`
}

type WorkspaceKind string

const (
	WorkspaceRoot     WorkspaceKind = "root"
	WorkspaceWorktree WorkspaceKind = "worktree"
)

type Workspace struct {
	Kind    WorkspaceKind `json:"kind"`
	Path    string        `json:"path"` // effective cwd for the harness
	Branch  string        `json:"branch,omitempty"`
	BaseRef string        `json:"baseRef,omitempty"`
	// AgentPath is the folder the agent moved to and works in, such as a
	// worktree it made itself; the branch is that folder's.
	AgentPath string `json:"agentPath,omitempty"`
}

// ContextUsage is how full the agent's context window was at its last model
// call. Window is 0 when the harness has not reported it (yet).
type ContextUsage struct {
	Used   int64 `json:"used"`
	Window int64 `json:"window,omitempty"`
}

// ModelInfo is the model and reasoning effort the agent reported it runs
// with, which may differ from the requested Session.Model (e.g. a default or
// an alias). Empty fields are unknown.
type ModelInfo struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`   // display name, when the agent gives one
	Effort string `json:"effort,omitempty"` // e.g. low, medium, high
}

// BackgroundTask is work the agent left running, such as a command it
// watches or a subagent. The agent reports back when it ends, in a turn of
// its own if none is running.
type BackgroundTask struct {
	ID          string             `json:"id"`
	Kind        BackgroundTaskKind `json:"kind"`
	Description string             `json:"description"`
}

type BackgroundTaskKind string

const (
	BackgroundCommand BackgroundTaskKind = "command"
	BackgroundAgent   BackgroundTaskKind = "agent"
	BackgroundOther   BackgroundTaskKind = "other"
)

type SessionStatus string

const (
	SessionIdle             SessionStatus = "idle"
	SessionRunning          SessionStatus = "running"
	SessionAwaitingApproval SessionStatus = "awaiting_approval"
	SessionStopped          SessionStatus = "stopped"
	SessionError            SessionStatus = "error"
)

type Session struct {
	ID        string        `json:"id"`
	ProjectID string        `json:"projectId"`
	Harness   string        `json:"harness"`
	Model     string        `json:"model,omitempty"`
	Effort    string        `json:"effort,omitempty"` // requested reasoning effort; empty for the default
	Mode      string        `json:"mode,omitempty"`
	Workspace Workspace     `json:"workspace"`
	Status    SessionStatus `json:"status"`
	NativeID  string        `json:"nativeId,omitempty"`
	Title     string        `json:"title"`
	Error     string        `json:"error,omitempty"`
	Archived  bool          `json:"archived"`
	// Unread: a turn ended, or the agent asked for an answer, since a client
	// last showed the session (session.markRead).
	Unread    bool          `json:"unread,omitempty"`
	Draft     *Draft        `json:"draft,omitempty"` // a prompt saved to send later (session.saveDraft)
	Context   *ContextUsage `json:"context,omitempty"`
	ModelInfo *ModelInfo    `json:"modelInfo,omitempty"`
	// BackgroundTasks lists what the agent left running, while its process runs.
	BackgroundTasks []BackgroundTask `json:"backgroundTasks,omitempty"`
	// ForkedFrom: the session this one is a fork of (session.fork).
	ForkedFrom *ForkedFrom `json:"forkedFrom,omitempty"`
	ItemCount  int64       `json:"-"`
	CreatedAt  time.Time   `json:"createdAt"`
	UpdatedAt  time.Time   `json:"updatedAt"`
}

// ForkedFrom names the session and turn a fork continues from: it has that
// session's transcript up to and including the turn.
type ForkedFrom struct {
	SessionID string `json:"sessionId"`
	Turn      int    `json:"turn"`
}

// Draft is a prompt the user saved in a session to send later: from any
// device, with session.sendDraft.
type Draft struct {
	Text   string     `json:"text"`
	Images []ImageRef `json:"images,omitempty"`
}

type TurnStatus string

const (
	TurnRunning     TurnStatus = "running"
	TurnCompleted   TurnStatus = "completed"
	TurnInterrupted TurnStatus = "interrupted"
	TurnFailed      TurnStatus = "failed"
)

type Usage struct {
	InputTokens      int64   `json:"inputTokens,omitempty"`
	OutputTokens     int64   `json:"outputTokens,omitempty"`
	CacheReadTokens  int64   `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens int64   `json:"cacheWriteTokens,omitempty"`
	CostUSD          float64 `json:"costUsd,omitempty"`
}

type Turn struct {
	ID               string     `json:"id"`
	SessionID        string     `json:"sessionId"`
	N                int        `json:"n"`
	Status           TurnStatus `json:"status"`
	CheckpointBefore string     `json:"checkpointBefore,omitempty"`
	CheckpointAfter  string     `json:"checkpointAfter,omitempty"`
	Usage            *Usage     `json:"usage,omitempty"`
	Error            string     `json:"error,omitempty"`
	// ForkPoint is where the agent's own conversation stood when the turn
	// ended, for a fork made after it (rad's; clients needn't read it).
	ForkPoint string     `json:"forkPoint,omitempty"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
}

type ItemKind string

const (
	ItemUserMessage      ItemKind = "user_message"
	ItemAssistantMessage ItemKind = "assistant_message"
	ItemReasoning        ItemKind = "reasoning"
	ItemToolCall         ItemKind = "tool_call"
	ItemApproval         ItemKind = "approval"
	ItemPlan             ItemKind = "plan"
	ItemNotice           ItemKind = "notice"
	ItemError            ItemKind = "error"
)

type ItemStatus string

const (
	ItemQueued     ItemStatus = "queued"
	ItemInProgress ItemStatus = "in_progress"
	ItemCompleted  ItemStatus = "completed"
	ItemFailed     ItemStatus = "failed"
	// Approval-only statuses.
	ItemPending   ItemStatus = "pending"
	ItemResolved  ItemStatus = "resolved"
	ItemCancelled ItemStatus = "cancelled"
	ItemExpired   ItemStatus = "expired"
)

// Item is one entry of a session transcript. Exactly one of the payload
// fields (Text, Tool, Approval, Plan) is meaningful for a given Kind. Images
// come with a user message (attached to the prompt) or a tool call (in its
// output, e.g. a screenshot).
type Item struct {
	ID           string     `json:"id"`
	SessionID    string     `json:"sessionId"`
	TurnID       string     `json:"turnId,omitempty"`
	ParentItemID string     `json:"parentItemId,omitempty"`
	Order        int64      `json:"order"`
	Kind         ItemKind   `json:"kind"`
	Status       ItemStatus `json:"status"`
	Text         string     `json:"text,omitempty"`
	Tool         *ToolCall  `json:"tool,omitempty"`
	Approval     *Approval  `json:"approval,omitempty"`
	Plan         *Plan      `json:"plan,omitempty"`
	Images       []ImageRef `json:"images,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

// ImageRef points to an image rad keeps; clients fetch the bytes with
// GET /v1/images/<id>. Width and Height are 0 when unknown (WebP).
type ImageRef struct {
	ID       string `json:"id"`       // SHA-256 of the bytes and an extension: "<hex>.png"
	MimeType string `json:"mimeType"` // image/png, image/jpeg, image/gif or image/webp
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Size     int64  `json:"size"`
}

// ToolKind is a coarse category used by the client to pick an icon.
type ToolKind string

const (
	ToolRead    ToolKind = "read"
	ToolEdit    ToolKind = "edit"
	ToolExecute ToolKind = "execute"
	ToolSearch  ToolKind = "search"
	ToolFetch   ToolKind = "fetch"
	ToolThink   ToolKind = "think"
	ToolOther   ToolKind = "other"
)

type ToolCall struct {
	Name     string          `json:"name"`
	Kind     ToolKind        `json:"kind"`
	Title    string          `json:"title,omitempty"` // one-line human summary, e.g. "Bash: go test ./..."
	Input    json.RawMessage `json:"input,omitempty"`
	Output   string          `json:"output,omitempty"` // truncated to MaxToolOutput
	ExitCode *int            `json:"exitCode,omitempty"`
	Paths    []string        `json:"paths,omitempty"`
}

const MaxToolOutput = 32 << 10

type OptionKind string

const (
	OptionAllowOnce    OptionKind = "allow_once"
	OptionAllowSession OptionKind = "allow_session"
	OptionDeny         OptionKind = "deny"
)

type ApprovalOption struct {
	ID    string     `json:"id"`
	Label string     `json:"label"`
	Kind  OptionKind `json:"kind"`
}

type ApprovalSpecial string

const (
	SpecialNone     ApprovalSpecial = ""
	SpecialPlan     ApprovalSpecial = "plan"     // approve a proposed plan (Claude ExitPlanMode)
	SpecialQuestion ApprovalSpecial = "question" // answer multiple-choice questions (Claude AskUserQuestion)
)

type Question struct {
	Question    string           `json:"question"`
	Header      string           `json:"header,omitempty"`
	MultiSelect bool             `json:"multiSelect,omitempty"`
	Options     []QuestionOption `json:"options"`
	// OptionsOnly: the agent takes only one of the options, so clients
	// don't offer an answer in the user's own words, as they otherwise do.
	OptionsOnly bool `json:"optionsOnly,omitempty"`
}

type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type Approval struct {
	ToolItemID string           `json:"toolItemId,omitempty"`
	ToolName   string           `json:"toolName,omitempty"`
	Title      string           `json:"title"`
	Detail     string           `json:"detail,omitempty"`
	Input      json.RawMessage  `json:"input,omitempty"`
	Options    []ApprovalOption `json:"options"`
	Special    ApprovalSpecial  `json:"special,omitempty"`
	PlanText   string           `json:"planText,omitempty"`
	Questions  []Question       `json:"questions,omitempty"`
	Decision   *Decision        `json:"decision,omitempty"`
}

type Decision struct {
	OptionID string            `json:"optionId"`
	Message  string            `json:"message,omitempty"`
	Answers  map[string]string `json:"answers,omitempty"`
	At       time.Time         `json:"at"`
}

type PlanEntry struct {
	Content string `json:"content"`
	Status  string `json:"status"` // pending | in_progress | completed
}

type Plan struct {
	Entries []PlanEntry `json:"entries,omitempty"`
	Text    string      `json:"text,omitempty"`
}

// Event types.
const (
	EvProjectUpserted = "project.upserted"
	EvProjectRemoved  = "project.removed"
	EvSessionUpserted = "session.upserted"
	EvSessionRemoved  = "session.removed"
	EvTurnUpserted    = "turn.upserted"
	EvItemUpserted    = "item.upserted"
)

// Streams.
const IndexStream = "index"

func SessionStream(id string) string { return "session:" + id }

// Event is one entry of a stream's change feed. The feed keeps only the latest
// event per entity, so replaying a stream from seq 0 yields a full snapshot.
type Event struct {
	Stream  string    `json:"stream"`
	Seq     int64     `json:"seq"`
	Type    string    `json:"type"`
	Project *Project  `json:"project,omitempty"`
	Session *Session  `json:"session,omitempty"`
	Turn    *Turn     `json:"turn,omitempty"`
	Item    *Item     `json:"item,omitempty"`
	ID      string    `json:"id,omitempty"` // entity id for *.removed
	TS      time.Time `json:"ts"`
}

// EntityKey identifies the entity an event is about; the feed keeps one row per key.
func (e *Event) EntityKey() string {
	switch {
	case e.Project != nil:
		return "project:" + e.Project.ID
	case e.Session != nil:
		return "session:" + e.Session.ID
	case e.Turn != nil:
		return "turn:" + e.Turn.ID
	case e.Item != nil:
		return "item:" + e.Item.ID
	case e.Type == EvProjectRemoved:
		return "project:" + e.ID
	case e.Type == EvSessionRemoved:
		return "session:" + e.ID
	}
	return ""
}

// Harness descriptions returned by harness.list.

type Choice struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Efforts     []Choice `json:"efforts,omitempty"` // for a model: the reasoning efforts it supports
}

// Command is a slash command: a prompt of "/<name>", optionally followed by
// text for it, runs it rather than going to the model.
type Command struct {
	Name        string `json:"name"` // without the slash
	Description string `json:"description,omitempty"`
	Kind        string `json:"kind,omitempty"`  // skill, prompt, extension, when reported
	Scope       string `json:"scope,omitempty"` // user, project, system, etc., when reported
}

type HarnessCaps struct {
	Resume      bool `json:"resume"`
	Interrupt   bool `json:"interrupt"`
	SetMode     bool `json:"setMode"`
	FreeModel   bool `json:"freeModel"` // client may type an arbitrary model id
	ModelSelect bool `json:"modelSelect"`
	Update      bool `json:"update,omitempty"`  // harness.update runs the CLI's own update
	Login       bool `json:"login,omitempty"`   // harness.login signs the CLI in again
	OneShot     bool `json:"oneShot,omitempty"` // can be the system agent (ServerSettings)
	Steer       bool `json:"steer,omitempty"`   // takes a prompt into the running turn (session.steer)
	Import      bool `json:"import,omitempty"`  // picks up the agent's own sessions (session.importable, session.import)
}

// ImportableSession is a session the agent ran outside rad, such as in a
// terminal, which session.import picks up.
type ImportableSession struct {
	ID        string    `json:"id"`               // the agent's own id for it, the session's nativeId once imported
	Title     string    `json:"title"`            // the agent's or the user's name for it, else its first prompt
	Messages  int       `json:"messages"`         // the prompts and the agent's replies
	UpdatedAt time.Time `json:"updatedAt"`        // when the agent last wrote to it
	Active    bool      `json:"active,omitempty"` // written in the last minute: it may still run in a terminal
}

type HarnessInfo struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Protocol    string      `json:"protocol"` // claude | codex | pi | acp | fake
	Installed   bool        `json:"installed"`
	Version     string      `json:"version,omitempty"`
	AuthOK      bool        `json:"authOk"`
	Hint        string      `json:"hint,omitempty"`
	Models      []Choice    `json:"models,omitempty"`
	Efforts     []Choice    `json:"efforts,omitempty"` // for the default model; a listed model's own efforts win
	Modes       []Choice    `json:"modes,omitempty"`
	DefaultMode string      `json:"defaultMode,omitempty"`
	Commands    []Command   `json:"commands,omitempty"` // slash commands the apps offer in a menu
	Caps        HarnessCaps `json:"caps"`
}

// SystemAgent is the agent, and its model, that does rad's own small jobs:
// naming sessions and their worktree branches. Its calls run without tools
// and leave no conversation behind.
type SystemAgent struct {
	Harness string `json:"harness"`
	Model   string `json:"model,omitempty"`  // empty: the harness's default
	Effort  string `json:"effort,omitempty"` // empty: the model's default
}

// ServerSettings are the settings clients change (server.setSettings), as
// opposed to the config file's.
type ServerSettings struct {
	// SystemAgent names sessions and worktree branches from the first
	// prompt; nil: the title is the prompt's first line.
	SystemAgent *SystemAgent `json:"systemAgent,omitempty"`
}

// UpdateInfo is what server.checkUpdate finds out about rad's releases.
type UpdateInfo struct {
	Current   string `json:"current"`
	Latest    string `json:"latest,omitempty"` // empty: the lookup failed (Reason says why)
	URL       string `json:"url,omitempty"`    // the latest release's page
	Available bool   `json:"available"`        // Latest is newer than Current
	// Via is how an update installs: "rad" replaces its own binary and
	// restarts, "app" has the Remote Agent Server app update itself.
	// Empty: it can't from here, and Reason says why.
	Via    string   `json:"via,omitempty"`
	Reason string   `json:"reason,omitempty"`
	Busy   []string `json:"busy"` // titles of the sessions a restart would interrupt
}

type TaskKind string

const (
	TaskUpdateServer TaskKind = "update_server"
	TaskUpdateAgent  TaskKind = "update_agent"
	TaskLogin        TaskKind = "login"
	// TaskCommit commits a session's changes, and pushes them and opens a
	// pull request when asked (git.commit).
	TaskCommit TaskKind = "commit"
)

type TaskStatus string

const (
	TaskRunning   TaskStatus = "running"
	TaskSucceeded TaskStatus = "succeeded"
	TaskFailed    TaskStatus = "failed"
	TaskCancelled TaskStatus = "cancelled"
)

// Task is a job a client started: updating rad or an agent, signing an
// agent in, or committing a session's changes. Clients poll it with
// task.get while it runs.
type Task struct {
	ID        string     `json:"id"`
	Kind      TaskKind   `json:"kind"`
	Harness   string     `json:"harness,omitempty"`   // update_agent, login
	SessionID string     `json:"sessionId,omitempty"` // commit
	Status    TaskStatus `json:"status"`
	// Steps are what the task does, in order, as it gets to them: a
	// commit's commit, push and pull request.
	Steps []TaskStep `json:"steps,omitempty"`
	// Output is what the command printed, as a terminal would show it,
	// without colours: its last lines.
	Output string `json:"output"`
	// URL is the last web address in the output, such as a sign-in page;
	// for a commit, the pull request it opened or found open.
	URL   string `json:"url,omitempty"`
	Input bool   `json:"input,omitempty"` // takes typed lines (task.input), such as a code to paste
	Error string `json:"error,omitempty"`
	// Version is, for an update, the version installed; for a login, the
	// agent's version.
	Version   string     `json:"version,omitempty"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
}

type TaskStepStatus string

const (
	StepPending   TaskStepStatus = "pending"
	StepRunning   TaskStepStatus = "running"
	StepSucceeded TaskStepStatus = "succeeded"
	StepSkipped   TaskStepStatus = "skipped" // nothing to do, such as a commit without changes
	StepFailed    TaskStepStatus = "failed"
)

// TaskStep is one step of a task, as clients list it.
type TaskStep struct {
	ID     string         `json:"id"` // commit, push, pull_request
	Title  string         `json:"title"`
	Status TaskStepStatus `json:"status"`
	Detail string         `json:"detail,omitempty"` // what it did, or does now, or why it failed
}

// TerminalMessage is a text frame on a terminal socket (GET /v1/terminal).
// The shell's output and the client's input travel as binary frames.
type TerminalMessage struct {
	Type     string `json:"type"`               // attached, reset, exit or error from rad; resize or close from a client
	ID       string `json:"id,omitempty"`       // attached: the shell, for resuming
	Offset   uint64 `json:"offset,omitempty"`   // attached, reset: the stream offset of the next output byte
	Reset    bool   `json:"reset,omitempty"`    // attached: the client clears its screen first
	Cols     int    `json:"cols,omitempty"`     // attached, resize
	Rows     int    `json:"rows,omitempty"`     // attached, resize
	ExitCode *int   `json:"exitCode,omitempty"` // exit
	Code     string `json:"code,omitempty"`     // error: not_found, archived, no_dir, gone or unavailable
	Message  string `json:"message,omitempty"`  // error
}
