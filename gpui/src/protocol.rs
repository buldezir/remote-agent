//! Wire entities. Mirrors server/internal/model in the Go server,
//! apple/RAKit/Sources/RAKit/Protocol/Models.swift and
//! web/src/protocol/models.ts; see protocol/PROTOCOL.md.
//!
//! Enums are open: a newer server may send values this client doesn't know,
//! which decode as `Unknown`. Unknown fields are ignored.

use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use serde_json::Value;

pub const INDEX_STREAM: &str = "index";

pub fn session_stream(id: &str) -> String {
    format!("session:{id}")
}

/// The session id in a `session:<id>` stream's name.
pub fn session_id_of_stream(stream: &str) -> Option<&str> {
    stream.strip_prefix("session:")
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct Project {
    pub id: String,
    pub path: String,
    pub name: String,
    #[serde(default)]
    pub is_git_repo: bool,
    #[serde(default)]
    pub created_at: String,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Eq, Default)]
#[serde(rename_all = "snake_case")]
pub enum WorkspaceKind {
    #[default]
    Root,
    Worktree,
    #[serde(other)]
    Unknown,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Default)]
#[serde(rename_all = "camelCase")]
pub struct Workspace {
    #[serde(default)]
    pub kind: WorkspaceKind,
    #[serde(default)]
    pub path: String,
    pub branch: Option<String>,
    pub base_ref: Option<String>,
    pub agent_path: Option<String>,
}

#[derive(Debug, Clone, Copy, Deserialize, PartialEq, Eq, Default)]
#[serde(rename_all = "snake_case")]
pub enum SessionStatus {
    #[default]
    Idle,
    Running,
    AwaitingApproval,
    Stopped,
    Error,
    #[serde(other)]
    Unknown,
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct Draft {
    #[serde(default)]
    pub text: String,
    #[serde(default)]
    pub images: Vec<ImageRef>,
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct ContextUsage {
    pub used: i64,
    pub window: Option<i64>,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Default)]
#[serde(rename_all = "camelCase")]
pub struct ModelInfo {
    pub id: Option<String>,
    pub name: Option<String>,
    pub effort: Option<String>,
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct BackgroundTask {
    pub id: String,
    #[serde(default)]
    pub kind: String,
    #[serde(default)]
    pub description: String,
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct ForkedFrom {
    pub session_id: String,
    pub turn: i64,
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct Session {
    pub id: String,
    pub project_id: String,
    pub harness: String,
    pub model: Option<String>,
    pub effort: Option<String>,
    pub mode: Option<String>,
    #[serde(default)]
    pub workspace: Workspace,
    #[serde(default)]
    pub status: SessionStatus,
    pub native_id: Option<String>,
    #[serde(default)]
    pub title: String,
    pub error: Option<String>,
    #[serde(default)]
    pub archived: bool,
    #[serde(default)]
    pub custom_title: bool,
    #[serde(default)]
    pub unread: bool,
    pub draft: Option<Draft>,
    pub context: Option<ContextUsage>,
    pub model_info: Option<ModelInfo>,
    #[serde(default)]
    pub background_tasks: Vec<BackgroundTask>,
    pub forked_from: Option<ForkedFrom>,
    pub pull_request: Option<PullRequest>,
    #[serde(default)]
    pub created_at: String,
    #[serde(default)]
    pub updated_at: String,
}

/// The pull request from a session's branch, as rad last found it on the
/// forge (GitHub).
#[derive(Debug, Clone, Deserialize, PartialEq, Default)]
#[serde(rename_all = "camelCase")]
pub struct PullRequest {
    #[serde(default)]
    pub forge: String,
    #[serde(default)]
    pub number: i64,
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub url: String,
    #[serde(default)]
    pub state: PullRequestState,
    #[serde(default)]
    pub head: String,
    pub base: Option<String>,
    pub checks: Option<PullRequestChecks>,
    pub review: Option<PullRequestReview>,
}

#[derive(Debug, Clone, Copy, Deserialize, PartialEq, Eq, Default)]
#[serde(rename_all = "snake_case")]
pub enum PullRequestState {
    #[default]
    Open,
    Draft,
    Merged,
    Closed,
    #[serde(other)]
    Unknown,
}

#[derive(Debug, Clone, Copy, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum PullRequestChecks {
    Passing,
    Failing,
    Pending,
    #[serde(other)]
    Unknown,
}

#[derive(Debug, Clone, Copy, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum PullRequestReview {
    Approved,
    ChangesRequested,
    ReviewRequired,
    #[serde(other)]
    Unknown,
}

#[derive(Debug, Clone, Copy, Deserialize, PartialEq, Eq, Default)]
#[serde(rename_all = "snake_case")]
pub enum TurnStatus {
    #[default]
    Running,
    Completed,
    Interrupted,
    Failed,
    #[serde(other)]
    Unknown,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Default)]
#[serde(rename_all = "camelCase")]
pub struct Usage {
    pub input_tokens: Option<i64>,
    pub output_tokens: Option<i64>,
    pub cache_read_tokens: Option<i64>,
    pub cache_write_tokens: Option<i64>,
    pub cost_usd: Option<f64>,
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct Turn {
    pub id: String,
    pub session_id: String,
    pub n: i64,
    #[serde(default)]
    pub status: TurnStatus,
    pub checkpoint_before: Option<String>,
    pub checkpoint_after: Option<String>,
    pub usage: Option<Usage>,
    pub error: Option<String>,
    #[serde(default)]
    pub started_at: String,
    pub ended_at: Option<String>,
}

#[derive(Debug, Clone, Copy, Deserialize, PartialEq, Eq, Hash, Default)]
#[serde(rename_all = "snake_case")]
pub enum ItemKind {
    UserMessage,
    AssistantMessage,
    Reasoning,
    ToolCall,
    Approval,
    Plan,
    Notice,
    Error,
    #[default]
    #[serde(other)]
    Unknown,
}

#[derive(Debug, Clone, Copy, Deserialize, PartialEq, Eq, Default)]
#[serde(rename_all = "snake_case")]
pub enum ItemStatus {
    Queued,
    InProgress,
    #[default]
    Completed,
    Failed,
    Pending,
    Resolved,
    Cancelled,
    Expired,
    #[serde(other)]
    Unknown,
}

#[derive(Debug, Clone, Copy, Deserialize, PartialEq, Eq, Hash, Default)]
#[serde(rename_all = "snake_case")]
pub enum ToolKind {
    Read,
    Edit,
    Execute,
    Search,
    Fetch,
    Think,
    #[default]
    #[serde(other)]
    Other,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Default)]
#[serde(rename_all = "camelCase")]
pub struct ToolCall {
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub kind: ToolKind,
    pub title: Option<String>,
    pub input: Option<Value>,
    pub output: Option<String>,
    pub exit_code: Option<i64>,
    #[serde(default)]
    pub paths: Vec<String>,
}

#[derive(Debug, Clone, Copy, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum OptionKind {
    AllowOnce,
    AllowSession,
    Deny,
    #[serde(other)]
    Unknown,
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct ApprovalOption {
    pub id: String,
    pub label: String,
    pub kind: OptionKind,
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct QuestionOption {
    pub label: String,
    pub description: Option<String>,
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct Question {
    pub question: String,
    pub header: Option<String>,
    #[serde(default)]
    pub multi_select: bool,
    #[serde(default)]
    pub options: Vec<QuestionOption>,
    #[serde(default)]
    pub options_only: bool,
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct Decision {
    pub option_id: String,
    pub message: Option<String>,
    #[serde(default)]
    pub answers: BTreeMap<String, String>,
    #[serde(default)]
    pub at: String,
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct Approval {
    pub tool_item_id: Option<String>,
    pub tool_name: Option<String>,
    #[serde(default)]
    pub title: String,
    pub detail: Option<String>,
    pub input: Option<Value>,
    #[serde(default)]
    pub options: Vec<ApprovalOption>,
    pub special: Option<String>,
    pub plan_text: Option<String>,
    #[serde(default)]
    pub questions: Vec<Question>,
    pub decision: Option<Decision>,
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct PlanEntry {
    pub content: String,
    #[serde(default)]
    pub status: String,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Default)]
#[serde(rename_all = "camelCase")]
pub struct Plan {
    #[serde(default)]
    pub entries: Vec<PlanEntry>,
    pub text: Option<String>,
}

#[derive(Debug, Clone, Deserialize, Serialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct ImageRef {
    pub id: String,
    #[serde(default)]
    pub mime_type: String,
    pub width: Option<i64>,
    pub height: Option<i64>,
    #[serde(default)]
    pub size: i64,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Default)]
#[serde(rename_all = "camelCase")]
pub struct Item {
    pub id: String,
    #[serde(default)]
    pub session_id: String,
    pub turn_id: Option<String>,
    pub parent_item_id: Option<String>,
    #[serde(default)]
    pub order: i64,
    #[serde(default)]
    pub kind: ItemKind,
    #[serde(default)]
    pub status: ItemStatus,
    pub text: Option<String>,
    pub tool: Option<ToolCall>,
    pub approval: Option<Approval>,
    pub plan: Option<Plan>,
    #[serde(default)]
    pub images: Vec<ImageRef>,
    pub limit: Option<UsageWindow>,
    #[serde(default)]
    pub created_at: String,
    #[serde(default)]
    pub updated_at: String,
}

impl Item {
    pub fn turn(&self) -> &str {
        self.turn_id.as_deref().unwrap_or("")
    }

    /// The text rad's search indexes of the item (`model.SearchText`), which
    /// find in a session searches too: none for sub-agents' items.
    pub fn search_text(&self) -> String {
        if self.parent_item_id.as_deref().is_some_and(|p| !p.is_empty()) {
            return String::new();
        }
        match self.kind {
            ItemKind::UserMessage | ItemKind::AssistantMessage | ItemKind::Notice | ItemKind::Error => {
                self.text.clone().unwrap_or_default()
            }
            ItemKind::ToolCall => {
                let Some(tool) = &self.tool else { return String::new() };
                let mut s = tool.title.clone().unwrap_or_default();
                if let Some(cmd) = tool.input.as_ref().and_then(tool_command)
                    && !cmd.is_empty()
                    && !s.contains(&cmd)
                {
                    s.push('\n');
                    s.push_str(&cmd);
                }
                s
            }
            ItemKind::Plan => {
                let Some(plan) = &self.plan else { return String::new() };
                let mut parts: Vec<&str> = plan.entries.iter().map(|e| e.content.as_str()).collect();
                if let Some(t) = plan.text.as_deref().filter(|t| !t.is_empty()) {
                    parts.push(t);
                }
                parts.join("\n")
            }
            ItemKind::Approval => {
                let Some(a) = &self.approval else { return String::new() };
                match a.special.as_deref() {
                    Some("plan") => a.plan_text.clone().unwrap_or_default(),
                    Some("question") => a
                        .questions
                        .iter()
                        .map(|q| q.question.as_str())
                        .collect::<Vec<_>>()
                        .join("\n"),
                    _ => String::new(),
                }
            }
            _ => String::new(),
        }
    }
}

/// The shell command in a tool's input, as most agents put it: a string, or
/// its words.
pub fn tool_command(input: &Value) -> Option<String> {
    match input.get("command")? {
        Value::String(s) => Some(s.clone()),
        Value::Array(parts) => Some(parts.iter().filter_map(Value::as_str).collect::<Vec<_>>().join(" ")),
        _ => None,
    }
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct UsageLimits {
    pub harness: String,
    #[serde(default)]
    pub windows: Vec<UsageWindow>,
    #[serde(default)]
    pub limited: bool,
    pub resets_at: Option<String>,
    pub plan: Option<String>,
    #[serde(default)]
    pub overage: bool,
    #[serde(default)]
    pub updated_at: String,
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct UsageWindow {
    pub id: String,
    #[serde(default)]
    pub label: String,
    #[serde(default)]
    pub used_percent: i64,
    pub resets_at: Option<String>,
    pub window_minutes: Option<i64>,
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct Event {
    #[serde(default)]
    pub stream: String,
    pub seq: i64,
    #[serde(rename = "type")]
    pub kind: String,
    pub project: Option<Project>,
    pub session: Option<Session>,
    pub turn: Option<Turn>,
    pub item: Option<Item>,
    pub limits: Option<UsageLimits>,
    pub id: Option<String>,
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct Choice {
    pub id: String,
    pub name: String,
    pub description: Option<String>,
    #[serde(default)]
    pub efforts: Vec<Choice>,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Default)]
#[serde(rename_all = "camelCase")]
pub struct HarnessCaps {
    #[serde(default)]
    pub resume: bool,
    #[serde(default)]
    pub interrupt: bool,
    #[serde(default)]
    pub set_mode: bool,
    #[serde(default)]
    pub free_model: bool,
    #[serde(default)]
    pub model_select: bool,
    #[serde(default)]
    pub update: bool,
    #[serde(default)]
    pub login: bool,
    #[serde(default)]
    pub one_shot: bool,
    #[serde(default)]
    pub steer: bool,
    #[serde(default)]
    pub import: bool,
}

/// A slash command: the harness's, or one `session.commands` found.
#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct Command {
    pub name: String,
    pub description: Option<String>,
    pub kind: Option<String>,
    pub scope: Option<String>,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Default)]
pub struct CommandList {
    #[serde(default)]
    pub commands: Vec<Command>,
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct HarnessInfo {
    pub id: String,
    pub name: String,
    #[serde(default)]
    pub installed: bool,
    pub version: Option<String>,
    #[serde(default)]
    pub auth_ok: bool,
    pub hint: Option<String>,
    #[serde(default)]
    pub models: Vec<Choice>,
    #[serde(default)]
    pub efforts: Vec<Choice>,
    #[serde(default)]
    pub modes: Vec<Choice>,
    pub default_mode: Option<String>,
    #[serde(default)]
    pub commands: Vec<Command>,
    #[serde(default)]
    pub caps: HarnessCaps,
}

impl HarnessInfo {
    pub fn usable(&self) -> bool {
        self.installed && self.auth_ok
    }

    /// The model's efforts, else the harness's.
    pub fn efforts_for(&self, model: Option<&str>) -> &[Choice] {
        model
            .and_then(|m| self.models.iter().find(|c| c.id == m))
            .filter(|c| !c.efforts.is_empty())
            .map(|c| c.efforts.as_slice())
            .unwrap_or(&self.efforts)
    }

    /// A model's name, else its id.
    pub fn model_name(&self, id: &str) -> String {
        self.models
            .iter()
            .find(|c| c.id == id)
            .map(|c| c.name.clone())
            .unwrap_or_else(|| id.to_string())
    }
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct HarnessList {
    #[serde(default)]
    pub harnesses: Vec<HarnessInfo>,
}

#[derive(Debug, Clone, Deserialize, Serialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct AgentSpec {
    pub harness: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub model: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub effort: Option<String>,
}

#[derive(Debug, Clone, Deserialize, Serialize, PartialEq, Default)]
#[serde(rename_all = "camelCase")]
pub struct ServerSettings {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub system_agent: Option<AgentSpec>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub default_agent: Option<AgentSpec>,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Default)]
#[serde(rename_all = "camelCase")]
pub struct ServerInfo {
    #[serde(default)]
    pub server_id: String,
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub protocol_version: i64,
    #[serde(default)]
    pub version: String,
    #[serde(default)]
    pub roots: Vec<String>,
    #[serde(default)]
    pub device_id: String,
    #[serde(default)]
    pub terminal: bool,
    #[serde(default)]
    pub drafts: bool,
    #[serde(default)]
    pub forks: bool,
    #[serde(default)]
    pub fork_in_worktree: bool,
    #[serde(default)]
    pub commit: bool,
    #[serde(default)]
    pub search: bool,
    #[serde(default)]
    pub rename: bool,
    #[serde(default)]
    pub unarchive: bool,
}

/// What `git.branches` answers.
#[derive(Debug, Clone, Deserialize, PartialEq, Default)]
#[serde(rename_all = "camelCase")]
pub struct Branches {
    #[serde(default)]
    pub branches: Vec<String>,
    #[serde(default)]
    pub current: String,
    #[serde(default)]
    pub worktrees: Vec<WorktreeInfo>,
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct WorktreeInfo {
    pub path: String,
    pub branch: Option<String>,
}

impl WorktreeInfo {
    /// Its branch, else its folder's name, detached.
    pub fn label(&self) -> String {
        match &self.branch {
            Some(b) if !b.is_empty() => b.clone(),
            _ => format!(
                "{} (detached)",
                std::path::Path::new(&self.path)
                    .file_name()
                    .map(|n| n.to_string_lossy().into_owned())
                    .unwrap_or_default()
            ),
        }
    }
}

impl Branches {
    /// The branches a new worktree can check out: those no checkout has.
    pub fn available(&self) -> Vec<String> {
        let mut taken: Vec<&str> = vec![self.current.as_str()];
        taken.extend(self.worktrees.iter().filter_map(|w| w.branch.as_deref()));
        self.branches
            .iter()
            .filter(|b| !taken.contains(&b.as_str()))
            .cloned()
            .collect()
    }
}

#[derive(Debug, Clone, Copy, Deserialize, PartialEq, Eq, Default)]
#[serde(rename_all = "snake_case")]
pub enum FileStatus {
    Added,
    #[default]
    Modified,
    Deleted,
    Renamed,
    #[serde(other)]
    Unknown,
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct FileStat {
    pub path: String,
    pub old_path: Option<String>,
    #[serde(default)]
    pub status: FileStatus,
    #[serde(default)]
    pub additions: i64,
    #[serde(default)]
    pub deletions: i64,
    #[serde(default)]
    pub binary: bool,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Default)]
#[serde(rename_all = "camelCase")]
pub struct Diff {
    #[serde(default)]
    pub from: String,
    #[serde(default)]
    pub to: String,
    #[serde(default)]
    pub files: Vec<FileStat>,
    #[serde(default)]
    pub patch: String,
    #[serde(default)]
    pub truncated: bool,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Default)]
#[serde(rename_all = "camelCase")]
pub struct RevertResult {
    #[serde(default)]
    pub files: Vec<FileStat>,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Default)]
#[serde(rename_all = "camelCase")]
pub struct CommitPreview {
    pub branch: Option<String>,
    #[serde(default)]
    pub files: Vec<FileStat>,
    #[serde(default)]
    pub message: String,
    pub remote: Option<String>,
    pub base: Option<String>,
    #[serde(default)]
    pub ahead: i64,
    pub no_pull_request: Option<String>,
}

#[derive(Debug, Clone, Copy, Deserialize, PartialEq, Eq, Default)]
#[serde(rename_all = "snake_case")]
pub enum TaskStatus {
    #[default]
    Running,
    Succeeded,
    Failed,
    Cancelled,
    #[serde(other)]
    Unknown,
}

#[derive(Debug, Clone, Copy, Deserialize, PartialEq, Eq, Default)]
#[serde(rename_all = "snake_case")]
pub enum StepStatus {
    #[default]
    Pending,
    Running,
    Succeeded,
    Skipped,
    Failed,
    #[serde(other)]
    Unknown,
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct TaskStep {
    pub id: String,
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub status: StepStatus,
    pub detail: Option<String>,
}

/// A long job on the server: an update, a sign-in, a commit.
#[derive(Debug, Clone, Deserialize, PartialEq, Default)]
#[serde(rename_all = "camelCase")]
pub struct ServerTask {
    pub id: String,
    #[serde(default)]
    pub kind: String,
    pub harness: Option<String>,
    pub session_id: Option<String>,
    #[serde(default)]
    pub status: TaskStatus,
    #[serde(default)]
    pub steps: Vec<TaskStep>,
    #[serde(default)]
    pub output: String,
    pub url: Option<String>,
    #[serde(default)]
    pub input: bool,
    pub error: Option<String>,
    pub version: Option<String>,
    #[serde(default)]
    pub started_at: String,
    pub ended_at: Option<String>,
}

impl ServerTask {
    pub fn running(&self) -> bool {
        self.status == TaskStatus::Running
    }
}

#[derive(Debug, Clone, Deserialize, PartialEq, Default)]
pub struct TaskList {
    #[serde(default)]
    pub tasks: Vec<ServerTask>,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Default)]
#[serde(rename_all = "camelCase")]
pub struct UpdateInfo {
    #[serde(default)]
    pub current: String,
    pub latest: Option<String>,
    pub url: Option<String>,
    #[serde(default)]
    pub available: bool,
    pub via: Option<String>,
    pub reason: Option<String>,
    #[serde(default)]
    pub busy: Vec<String>,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Default)]
#[serde(rename_all = "camelCase")]
pub struct FsEntry {
    pub name: String,
    pub path: String,
    #[serde(default)]
    pub is_git_repo: bool,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Default)]
#[serde(rename_all = "camelCase")]
pub struct FsList {
    #[serde(default)]
    pub path: String,
    pub parent: Option<String>,
    #[serde(default)]
    pub entries: Vec<FsEntry>,
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct ImportableSession {
    pub id: String,
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub messages: i64,
    #[serde(default)]
    pub updated_at: String,
    #[serde(default)]
    pub active: bool,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Default)]
pub struct ImportableList {
    #[serde(default)]
    pub sessions: Vec<ImportableSession>,
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
pub struct SnippetPart {
    #[serde(default)]
    pub text: String,
    #[serde(default, rename = "match")]
    pub is_match: bool,
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct SearchResult {
    pub session: Option<Session>,
    pub item_id: Option<String>,
    pub item_kind: Option<ItemKind>,
    #[serde(default)]
    pub snippet: Vec<SnippetPart>,
    #[serde(default)]
    pub matches: i64,
}

#[derive(Debug, Clone, Deserialize, PartialEq, Default)]
#[serde(rename_all = "camelCase")]
pub struct SearchResults {
    #[serde(default)]
    pub results: Vec<SearchResult>,
    #[serde(default)]
    pub more: bool,
    #[serde(default)]
    pub indexing: bool,
}

/// A text frame on the terminal's socket.
#[derive(Debug, Clone, Deserialize, Serialize, PartialEq, Default)]
#[serde(rename_all = "camelCase")]
pub struct TerminalMessage {
    #[serde(rename = "type")]
    pub kind: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub offset: Option<u64>,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub reset: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cols: Option<u16>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub rows: Option<u16>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub exit_code: Option<i64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub code: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub message: Option<String>,
}

#[derive(Debug, Clone, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct PairResult {
    pub server_id: String,
    pub name: String,
    #[serde(default)]
    pub protocol_version: i64,
    #[serde(default)]
    pub version: String,
    pub token: String,
    pub device_id: String,
}

/// An error rad answered with, or one of the client's own (`unreachable`,
/// `disconnected`, `timeout`).
#[derive(Debug, Clone, Deserialize, PartialEq)]
pub struct RpcError {
    pub code: String,
    #[serde(default)]
    pub message: String,
}

impl RpcError {
    pub fn new(code: &str, message: impl Into<String>) -> Self {
        Self {
            code: code.into(),
            message: message.into(),
        }
    }
}

impl std::fmt::Display for RpcError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        if self.message.is_empty() {
            write!(f, "{}", self.code)
        } else {
            write!(f, "{}", self.message)
        }
    }
}

impl std::error::Error for RpcError {}

/// A notification from rad, decoded.
#[derive(Debug, Clone, PartialEq)]
pub enum Notification {
    Events { stream: String, events: Vec<Event> },
    Synchronized { stream: String, seq: i64, reset: bool },
    Resync { stream: String },
}

#[derive(Deserialize)]
struct EventsParams {
    stream: String,
    #[serde(default)]
    events: Vec<Event>,
}

#[derive(Deserialize)]
struct SynchronizedParams {
    stream: String,
    seq: i64,
    #[serde(default)]
    reset: bool,
}

#[derive(Deserialize)]
struct ResyncParams {
    stream: String,
}

/// A frame from rad: a response to a request of ours, or a notification.
#[derive(Debug, Clone, PartialEq)]
pub enum Frame {
    Response {
        id: u64,
        result: Result<Value, RpcError>,
    },
    Notification(Notification),
    /// A notification this client doesn't know, or one that didn't decode.
    Ignored,
}

#[derive(Deserialize)]
struct RawFrame {
    id: Option<Value>,
    method: Option<String>,
    params: Option<Value>,
    result: Option<Value>,
    error: Option<RpcError>,
}

impl Frame {
    pub fn parse(text: &str) -> Result<Frame, serde_json::Error> {
        let raw: RawFrame = serde_json::from_str(text)?;
        if let Some(id) = raw.id {
            // Our ids are numbers.
            let id = id.as_u64().unwrap_or(u64::MAX);
            let result = match raw.error {
                Some(e) => Err(e),
                None => Ok(raw.result.unwrap_or(Value::Null)),
            };
            return Ok(Frame::Response { id, result });
        }
        let params = raw.params.unwrap_or(Value::Null);
        Ok(match raw.method.as_deref() {
            Some("events") => match serde_json::from_value::<EventsParams>(params) {
                Ok(p) => Frame::Notification(Notification::Events {
                    stream: p.stream,
                    events: p.events,
                }),
                Err(_) => Frame::Ignored,
            },
            Some("synchronized") => match serde_json::from_value::<SynchronizedParams>(params) {
                Ok(p) => Frame::Notification(Notification::Synchronized {
                    stream: p.stream,
                    seq: p.seq,
                    reset: p.reset,
                }),
                Err(_) => Frame::Ignored,
            },
            Some("resync") => match serde_json::from_value::<ResyncParams>(params) {
                Ok(p) => Frame::Notification(Notification::Resync { stream: p.stream }),
                Err(_) => Frame::Ignored,
            },
            _ => Frame::Ignored,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    fn fixture(name: &str) -> String {
        let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("../protocol/fixtures")
            .join(name);
        std::fs::read_to_string(&path).unwrap_or_else(|e| panic!("{}: {e}", path.display()))
    }

    fn result<T: serde::de::DeserializeOwned>(name: &str) -> T {
        match Frame::parse(&fixture(name)).unwrap() {
            Frame::Response { result: Ok(v), .. } => serde_json::from_value(v).unwrap(),
            other => panic!("{name}: {other:?}"),
        }
    }

    #[test]
    fn pair_response() {
        let pair: PairResult = serde_json::from_str(&fixture("http_pair_response.json")).unwrap();
        assert_eq!(pair.server_id, "srv1");
        assert_eq!(pair.token, "0123abcd");
        assert_eq!(pair.device_id, "dev1");
    }

    #[test]
    fn server_info() {
        let info: ServerInfo = result("response_server_info.json");
        assert_eq!(info.version, "0.4.0");
        assert_eq!(info.roots, vec!["/Users/me/projects"]);
        assert!(info.drafts && info.rename && info.unarchive);
    }

    #[test]
    fn error_response() {
        match Frame::parse(&fixture("response_error.json")).unwrap() {
            Frame::Response { id: 7, result: Err(e) } => assert_eq!(e.code, "conflict"),
            other => panic!("{other:?}"),
        }
    }

    #[test]
    fn harness_list() {
        let list: HarnessList = result("response_harness_list.json");
        let claude = &list.harnesses[0];
        assert_eq!(claude.id, "claude");
        assert!(claude.usable());
        assert_eq!(claude.models[0].efforts.len(), 2);
    }

    #[test]
    fn server_settings() {
        let s: ServerSettings = result("response_server_settings.json");
        assert!(s.default_agent.is_some() || s.system_agent.is_some());
    }

    #[test]
    fn notifications() {
        match Frame::parse(&fixture("notification_synchronized.json")).unwrap() {
            Frame::Notification(Notification::Synchronized {
                stream,
                seq: 15,
                reset: true,
            }) => {
                assert_eq!(stream, "session:s1")
            }
            other => panic!("{other:?}"),
        }
        assert_eq!(
            Frame::parse(&fixture("notification_resync.json")).unwrap(),
            Frame::Notification(Notification::Resync {
                stream: "session:s1".into()
            })
        );
    }

    #[test]
    fn session_events() {
        let Frame::Notification(Notification::Events { stream, events }) =
            Frame::parse(&fixture("notification_events_session.json")).unwrap()
        else {
            panic!("not events")
        };
        assert_eq!(stream, "session:s1");
        let session = events.iter().find_map(|e| e.session.as_ref()).unwrap();
        assert_eq!(session.status, SessionStatus::AwaitingApproval);
        assert!(session.unread && session.custom_title);
        assert_eq!(session.workspace.kind, WorkspaceKind::Worktree);
        assert_eq!(session.draft.as_ref().unwrap().images.len(), 1);
        assert_eq!(session.model_info.as_ref().unwrap().name.as_deref(), Some("Sonnet 5.5"));
        let pr = session.pull_request.as_ref().unwrap();
        assert_eq!((pr.number, pr.state), (42, PullRequestState::Open));
        assert_eq!(pr.checks, Some(PullRequestChecks::Failing));
        assert_eq!(pr.review, Some(PullRequestReview::ChangesRequested));
        assert_eq!(pr.url, "https://github.com/acme/app/pull/42");
        let items: Vec<&Item> = events.iter().filter_map(|e| e.item.as_ref()).collect();
        assert!(items.iter().any(|i| i.kind == ItemKind::ToolCall && i.tool.is_some()));
        assert!(
            items
                .iter()
                .any(|i| i.kind == ItemKind::Approval && i.approval.is_some())
        );
        assert!(events.iter().any(|e| e.turn.is_some()));
    }

    #[test]
    fn pull_request() {
        let s: Session = result("response_session.json");
        let pr = s.pull_request.unwrap();
        assert_eq!(pr.forge, "github");
        assert_eq!(pr.title, "Fix the flaky tests");
        assert_eq!((pr.head.as_str(), pr.base.as_deref()), ("ra/fix-tests", Some("main")));
        // A newer server's states, checks and reviews still decode.
        let pr: PullRequest = serde_json::from_value(serde_json::json!({
            "forge": "gitlab", "number": 7, "title": "T", "url": "https://gitlab.com/a/b/-/merge_requests/7",
            "state": "queued", "head": "b", "checks": "skipped", "review": "dismissed",
        }))
        .unwrap();
        assert_eq!(pr.state, PullRequestState::Unknown);
        assert_eq!(pr.checks, Some(PullRequestChecks::Unknown));
        assert_eq!(pr.review, Some(PullRequestReview::Unknown));
    }

    #[test]
    fn index_events() {
        let Frame::Notification(Notification::Events { events, .. }) =
            Frame::parse(&fixture("notification_events_index.json")).unwrap()
        else {
            panic!("not events")
        };
        assert!(events.iter().any(|e| e.project.is_some()));
        assert!(events.iter().any(|e| e.session.is_some()));
    }

    #[test]
    fn management_results() {
        let b: Branches = result("response_branches.json");
        assert_eq!(b.available(), vec!["feature/login".to_string()]);
        assert_eq!(b.worktrees[1].label(), "spike (detached)");
        let u: UpdateInfo = result("response_check_update.json");
        assert!(u.available && u.busy.len() == 1);
        let c: CommitPreview = result("response_commit_preview.json");
        assert_eq!(c.files.len(), 2);
        assert_eq!(c.base.as_deref(), Some("main"));
        let d: CommitPreview = result("response_commit_preview_default_branch.json");
        assert!(d.no_pull_request.is_some());
        let diff: Diff = result("response_diff.json");
        assert_eq!(diff.files[1].old_path.as_deref(), Some("old.go"));
        assert!(diff.files[2].binary);
        let fs: FsList = result("response_fs_list.json");
        assert!(fs.entries[0].is_git_repo);
        let cmds: CommandList = result("response_session_commands.json");
        assert_eq!(cmds.commands[1].kind.as_deref(), Some("skill"));
        let imp: ImportableList = result("response_session_importable.json");
        assert!(imp.sessions[0].active);
        let search: SearchResults = result("response_session_search.json");
        assert!(search.results[0].session.is_some());
        let task: ServerTask = result("response_task_commit.json");
        assert_eq!(task.steps[0].status, StepStatus::Skipped);
        assert_eq!(task.status, TaskStatus::Succeeded);
        let tasks: TaskList = result("response_task_list.json");
        assert!(!tasks.tasks.is_empty());
        let t: ServerTask = result("response_task.json");
        assert!(!t.id.is_empty());
        let info: ServerInfo = result("response_server_info.json");
        assert!(info.terminal && info.forks && info.fork_in_worktree && info.commit && info.search);
    }

    #[test]
    fn terminal_messages() {
        let a: TerminalMessage = serde_json::from_str(&fixture("terminal_attached.json")).unwrap();
        assert_eq!(a.kind, "attached");
        assert!(a.reset);
        assert_eq!(a.offset, Some(262130));
        let e: TerminalMessage = serde_json::from_str(&fixture("terminal_error.json")).unwrap();
        assert_eq!(e.code.as_deref(), Some("gone"));
        let x: TerminalMessage = serde_json::from_str(&fixture("terminal_exit.json")).unwrap();
        assert!(x.exit_code.is_some());
        let r: TerminalMessage = serde_json::from_str(&fixture("terminal_resize.json")).unwrap();
        let back: Value = serde_json::to_value(&r).unwrap();
        let want: Value = serde_json::from_str(&fixture("terminal_resize.json")).unwrap();
        assert_eq!(back, want);
    }

    #[test]
    fn unknown_values_decode() {
        let item: Item = serde_json::from_str(
            r#"{"id":"i","kind":"hologram","status":"teleported","order":3,"tool":{"name":"x","kind":"warp"},"newField":1}"#,
        )
        .unwrap();
        assert_eq!(item.kind, ItemKind::Unknown);
        assert_eq!(item.status, ItemStatus::Unknown);
        assert_eq!(item.tool.unwrap().kind, ToolKind::Other);
    }
}
