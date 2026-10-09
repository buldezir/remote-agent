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
	Context   *ContextUsage `json:"context,omitempty"`
	ModelInfo *ModelInfo    `json:"modelInfo,omitempty"`
	ItemCount int64         `json:"-"`
	CreatedAt time.Time     `json:"createdAt"`
	UpdatedAt time.Time     `json:"updatedAt"`
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
	StartedAt        time.Time  `json:"startedAt"`
	EndedAt          *time.Time `json:"endedAt,omitempty"`
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
// fields (Text, Tool, Approval, Plan) is meaningful for a given Kind.
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
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
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

type HarnessCaps struct {
	Resume      bool `json:"resume"`
	Interrupt   bool `json:"interrupt"`
	SetMode     bool `json:"setMode"`
	FreeModel   bool `json:"freeModel"` // client may type an arbitrary model id
	ModelSelect bool `json:"modelSelect"`
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
	Caps        HarnessCaps `json:"caps"`
}
