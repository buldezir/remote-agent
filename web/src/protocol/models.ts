// Wire entities. Mirrors server/internal/model in the Go server and
// apple/RAKit/Sources/RAKit/Protocol/Models.swift; see protocol/PROTOCOL.md.
// Enums are open: a newer server may send values this client doesn't know,
// and views treat those like the apps' `.unknown`.

type Open<T extends string> = T | (string & {});

export interface Project {
  id: string;
  path: string;
  name: string;
  isGitRepo: boolean;
  createdAt: string;
}

export interface Workspace {
  kind: Open<"root" | "worktree">;
  path: string;
  branch?: string;
  baseRef?: string;
  /** The folder the agent moved to and works in, such as a worktree it made itself; branch is that folder's. */
  agentPath?: string;
}

export type SessionStatus = Open<"idle" | "running" | "awaiting_approval" | "stopped" | "error">;

export interface Session {
  id: string;
  projectId: string;
  harness: string;
  model?: string;
  effort?: string;
  mode?: string;
  workspace: Workspace;
  status: SessionStatus;
  nativeId?: string;
  title: string;
  error?: string;
  archived: boolean;
  /** The title is the user's (`session.rename`, or one given at create, fork
   *  or import), which rad's naming leaves alone. Absent: false. */
  customTitle?: boolean;
  /** A turn ended, or the agent asked for an answer, since a client last
   *  showed the session (`session.markRead`). Absent: false. */
  unread?: boolean;
  /** A prompt saved to send later, from any device (`session.saveDraft`).
   *  Absent: none. */
  draft?: Draft;
  context?: ContextUsage;
  modelInfo?: ModelInfo;
  backgroundTasks?: BackgroundTask[];
  /** The session this one is a fork of (`session.fork`). */
  forkedFrom?: ForkedFrom;
  createdAt: string;
  updatedAt: string;
}

/** The session and turn a fork continues from: it has that session's
 *  transcript up to and including the turn. */
export interface ForkedFrom {
  sessionId: string;
  turn: number;
}

/** A prompt kept in a session, unsent: the transcript shows it at its foot,
 *  and `session.sendDraft` sends it. */
export interface Draft {
  text: string;
  images?: ImageRef[];
}

export type BackgroundTaskKind = Open<"command" | "agent" | "other">;

/** What the agent left running after its turn (a command it watches, a
 *  subagent), while its process runs. */
export interface BackgroundTask {
  id: string;
  kind: BackgroundTaskKind;
  description: string;
}

/** The model and reasoning effort the agent reported it runs with. */
export interface ModelInfo {
  id?: string;
  name?: string;
  effort?: string;
}

/** How full the agent's context window was at its last model call. */
export interface ContextUsage {
  used: number;
  window?: number;
}

/** 0...1, or undefined when the window is unknown. */
export function contextFraction(c: ContextUsage | undefined): number | undefined {
  if (!c?.window || c.window <= 0) return undefined;
  return Math.min(1, c.used / c.window);
}

/** The model to show: the one the agent reported, else the requested one.
 *  Ids are named from the harness's model list when the agent gave no name. */
export function modelName(s: Session, harness: HarnessInfo | undefined): string | undefined {
  if (s.modelInfo?.name) return s.modelInfo.name;
  const id = s.modelInfo?.id ?? s.model;
  if (!id) return undefined;
  return harness?.models?.find((m) => m.id === id)?.name ?? id;
}

export type TurnStatus = Open<"running" | "completed" | "interrupted" | "failed">;

export interface Usage {
  inputTokens?: number;
  outputTokens?: number;
  cacheReadTokens?: number;
  cacheWriteTokens?: number;
  costUsd?: number;
}

export interface Turn {
  id: string;
  sessionId: string;
  n: number;
  status: TurnStatus;
  checkpointBefore?: string;
  checkpointAfter?: string;
  usage?: Usage;
  error?: string;
  startedAt: string;
  endedAt?: string;
}

export type ItemKind = Open<
  "user_message" | "assistant_message" | "reasoning" | "tool_call" | "approval" | "plan" | "notice" | "error"
>;

export type ItemStatus = Open<
  "queued" | "in_progress" | "completed" | "failed" | "pending" | "resolved" | "cancelled" | "expired"
>;

export type ToolKind = Open<"read" | "edit" | "execute" | "search" | "fetch" | "think" | "other">;

export type JSONValue = null | boolean | number | string | JSONValue[] | { [key: string]: JSONValue };

export interface ToolCall {
  name: string;
  kind: ToolKind;
  title?: string;
  input?: JSONValue;
  output?: string;
  exitCode?: number;
  paths?: string[];
}

export type OptionKind = Open<"allow_once" | "allow_session" | "deny">;

export interface ApprovalOption {
  id: string;
  label: string;
  kind: OptionKind;
}

export interface QuestionOption {
  label: string;
  description?: string;
}

export interface Question {
  question: string;
  header?: string;
  multiSelect?: boolean;
  options: QuestionOption[];
  /** The agent takes only one of the options: no answer in the user's own words. */
  optionsOnly?: boolean;
}

export interface Decision {
  optionId: string;
  message?: string;
  answers?: Record<string, string>;
  at: string;
}

export interface Approval {
  toolItemId?: string;
  toolName?: string;
  title: string;
  detail?: string;
  input?: JSONValue;
  options?: ApprovalOption[];
  special?: Open<"plan" | "question">;
  planText?: string;
  questions?: Question[];
  decision?: Decision;
}

export interface PlanEntry {
  content: string;
  status: Open<"pending" | "in_progress" | "completed">;
}

export interface Plan {
  entries?: PlanEntry[];
  text?: string;
}

/** An image rad keeps: attached to a prompt, in a tool's output, or shown in
 *  a reply. The id names the content. */
export interface ImageRef {
  id: string;
  mimeType: string;
  width?: number;
  height?: number;
  size: number;
}

/** Width over height, when the server knows both. */
export function aspectRatio(r: ImageRef): number | undefined {
  return r.width && r.height ? r.width / r.height : undefined;
}

/** The id in a reply's link to one of rad's images: rad points the Markdown
 *  images in agents' replies at its copies, `rad-image:<id>`. */
export function imageIdLinkedBy(url: string): string | undefined {
  if (!url.startsWith("rad-image:")) return undefined;
  const id = url.slice("rad-image:".length);
  return id || undefined;
}

export interface Item {
  id: string;
  sessionId: string;
  turnId?: string;
  parentItemId?: string;
  order: number;
  kind: ItemKind;
  status: ItemStatus;
  text?: string;
  tool?: ToolCall;
  approval?: Approval;
  plan?: Plan;
  /** User messages: the attached images. Tool calls: images in the output.
   *  Agent messages: the images the text links to. */
  images?: ImageRef[];
  createdAt: string;
  updatedAt: string;
}

export interface Event {
  stream: string;
  seq: number;
  type: string;
  ts?: string;
  project?: Project;
  session?: Session;
  turn?: Turn;
  item?: Item;
  id?: string;
}

export interface Choice {
  id: string;
  name: string;
  description?: string;
  /** For a model: the reasoning efforts it supports. */
  efforts?: Choice[];
}

/** A slash command: a prompt of "/<name>" runs it rather than going to the model. */
export interface SlashCommand {
  name: string;
  description?: string;
  kind?: string;
  scope?: string;
}

export interface HarnessCaps {
  resume: boolean;
  interrupt: boolean;
  setMode: boolean;
  freeModel: boolean;
  modelSelect: boolean;
  /** `harness.update` runs the agent's own updater. Older servers leave it out. */
  update?: boolean;
  /** `harness.login` signs the agent in again. */
  login?: boolean;
  /** The agent can be the system agent (`ServerSettings`). */
  oneShot?: boolean;
  /** The agent takes a prompt into the running turn (`session.steer`). */
  steer?: boolean;
  /** The agent picks up its own sessions, such as the ones run in a
   *  terminal (`session.importable`, `session.import`). */
  import?: boolean;
}

/** A session the agent ran outside rad, such as in a terminal, which
 *  `session.import` picks up. */
export interface ImportableSession {
  /** The agent's own id for it: the session's `nativeId` once imported. */
  id: string;
  /** The agent's or the user's name for it, else its first prompt's first line. */
  title: string;
  /** The prompts and the agent's replies. */
  messages: number;
  /** When the agent last wrote to it. */
  updatedAt: string;
  /** Written in the last minute: it may still run in a terminal. */
  active?: boolean;
}

export interface HarnessInfo {
  id: string;
  name: string;
  protocol: string;
  installed: boolean;
  version?: string;
  authOk: boolean;
  hint?: string;
  models?: Choice[];
  efforts?: Choice[];
  modes?: Choice[];
  defaultMode?: string;
  /** Slash commands the composer suggests besides the session's own, such as /compact. */
  commands?: SlashCommand[];
  caps: HarnessCaps;
}

export function usable(h: HarnessInfo): boolean {
  return h.installed && h.authOk;
}

/** The reasoning efforts to offer for a model (undefined or unlisted: the default model). */
export function effortsForModel(h: HarnessInfo, model: string | undefined): Choice[] {
  return h.models?.find((m) => m.id === model)?.efforts ?? h.efforts ?? [];
}

/** An agent with a model and an effort: the system agent, or the one new
 *  sessions start with. Model and effort absent: the agent's, the model's default. */
export interface AgentSpec {
  harness: string;
  model?: string;
  effort?: string;
}

/** The settings clients change on a server (`server.settings`). */
export interface ServerSettings {
  /** Names sessions and their worktree branches. Absent: none, and a
   *  session's title is its prompt's first line. */
  systemAgent?: AgentSpec;
  /** The agent, model and effort new sessions start with. Absent: each
   *  client starts with the agent it used last. */
  defaultAgent?: AgentSpec;
}

/** What `server.checkUpdate` found out about rad's releases. */
export interface UpdateInfo {
  current: string;
  /** Absent: the lookup failed, and `reason` says why. */
  latest?: string;
  /** The latest release's page. */
  url?: string;
  /** `latest` is newer than `current`. */
  available: boolean;
  /** How an update installs; absent: it can't, and `reason` says why. */
  via?: Open<"rad" | "app">;
  reason?: string;
  /** The titles of the sessions a restart would interrupt. */
  busy: string[];
}

/** Whether the client can have rad install `latest`. */
export function installable(u: UpdateInfo): boolean {
  return u.available && !!u.via;
}

export type TaskKind = Open<"update_server" | "update_agent" | "login" | "commit">;
export type TaskStatus = Open<"running" | "succeeded" | "failed" | "cancelled">;
/** Unknown counts as pending. */
export type TaskStepStatus = Open<"pending" | "running" | "succeeded" | "skipped" | "failed">;

/** One step of a task, as it gets there: a commit's "commit", "push" and
 *  "pull_request". */
export interface TaskStep {
  id: string;
  title: string;
  status: TaskStepStatus;
  /** What it did, or does now, or why it failed. */
  detail?: string;
}

/** A job on the server: updating rad or an agent, signing an agent in, or
 *  committing a session's changes. Clients poll it (`task.get`) while it
 *  runs. */
export interface ServerTask {
  id: string;
  kind: TaskKind;
  /** The agent, for update_agent and login. */
  harness?: string;
  /** The session, for commit. */
  sessionId?: string;
  /** Anything but running has ended; an unknown status counts as failed. */
  status: TaskStatus;
  /** What it does, in order. */
  steps?: TaskStep[];
  /** What the command printed, as a terminal shows it, without colours. */
  output: string;
  /** The last web address in the output, such as a sign-in page; for a
   *  commit, the pull request. */
  url?: string;
  /** It takes typed lines now (`task.input`), such as a code to paste. */
  input?: boolean;
  error?: string;
  /** For an update, the version installed; for a login, the agent's. */
  version?: string;
  startedAt: string;
  endedAt?: string;
}

export interface FSEntry {
  name: string;
  path: string;
  isGitRepo: boolean;
}

export interface FSListing {
  path: string;
  parent?: string;
  entries: FSEntry[];
}

export interface FileStat {
  path: string;
  oldPath?: string;
  status: Open<"added" | "modified" | "deleted" | "renamed">;
  additions: number;
  deletions: number;
  binary?: boolean;
}

export interface Diff {
  from: string;
  to: string;
  files: FileStat[];
  patch: string;
  truncated: boolean;
}

/** What `git.commit` would commit in a session, and a message for it
 *  (`git.commitPreview`). */
export interface CommitPreview {
  /** Checked out where the agent works; absent: detached. */
  branch?: string;
  /** Everything uncommitted, untracked files included. */
  files: FileStat[];
  /** For the files, by the system agent or rad; "" without files. */
  message: string;
  /** Where a push goes; absent: nowhere. */
  remote?: string;
  /** The branch a pull request goes into; absent: the repository's default. */
  base?: string;
  /** The branch's commits its remote branch lacks (before the first push: that base lacks). */
  ahead: number;
  /** Why a pull request can't be opened from the branch, such as the repository's default branch; absent: it can. */
  noPullRequest?: string;
}

export interface ServerInfo {
  serverId: string;
  name: string;
  protocolVersion: number;
  version: string;
  roots?: string[];
  deviceId?: string;
  /** The server runs shells for sessions (`GET /v1/terminal`). */
  terminal?: boolean;
  /** The server keeps drafts (`session.saveDraft`, `session.sendDraft` and
   *  `session.create`'s `draft`). An older one would start a session
   *  created as a draft with its prompt, so clients offer drafts only then. */
  drafts?: boolean;
  /** The server forks sessions (`session.fork`). */
  forks?: boolean;
  /** The server commits, pushes and opens pull requests (`git.commitPreview`, `git.commit`). */
  commit?: boolean;
  /** The server finds sessions by what was said in them (`session.search`). */
  search?: boolean;
  /** The server renames sessions (`session.rename`). */
  rename?: boolean;
}

/** `session.search`'s result, best first. */
export interface SearchResults {
  results: SearchResult[];
  /** More sessions match than the limit. */
  more?: boolean;
  /** rad is still indexing sessions an older rad wrote, which may be missing. */
  indexing?: boolean;
}

/** A session that `session.search` found, with the item in it that matches
 *  best: else its title or branch matched. */
export interface SearchResult {
  session: Session;
  /** Absent: only the title or the branch matched. */
  itemId?: string;
  /** The item's kind. */
  itemKind?: ItemKind;
  /** The match in its context, on one line, its words marked. */
  snippet: SnippetPart[];
  /** How many of the session's items match. */
  matches: number;
}

/** A run of a snippet's text: `match` marks the words the query found. */
export interface SnippetPart {
  text: string;
  match?: boolean;
}

/** A text frame on a terminal socket. The shell's output and the user's
 *  input travel as binary frames. */
export interface TerminalMessage {
  /** attached, reset, exit or error from rad; resize or close from a client. */
  type: string;
  id?: string;
  offset?: number;
  reset?: boolean;
  cols?: number;
  rows?: number;
  exitCode?: number;
  code?: string;
  message?: string;
}

export interface PairResult {
  serverId: string;
  name: string;
  protocolVersion: number;
  version: string;
  token: string;
  deviceId: string;
}

/** Milliseconds since the epoch for one of Go's RFC 3339 times, which carry
 *  up to nanoseconds; some browsers parse only milliseconds. */
export function parseDate(s: string | undefined): number {
  if (!s) return NaN;
  return Date.parse(s.replace(/(\.\d{3})\d+/, "$1"));
}
