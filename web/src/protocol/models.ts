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
  context?: ContextUsage;
  modelInfo?: ModelInfo;
  createdAt: string;
  updatedAt: string;
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
}

export interface HarnessCaps {
  resume: boolean;
  interrupt: boolean;
  setMode: boolean;
  freeModel: boolean;
  modelSelect: boolean;
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
  /** The slash commands to offer in a menu, such as /compact. */
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

export interface ServerInfo {
  serverId: string;
  name: string;
  protocolVersion: number;
  version: string;
  roots?: string[];
  deviceId?: string;
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
