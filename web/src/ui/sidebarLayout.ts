import { parseDate, type Project, type Session } from "../protocol/models";
import type { ServerConnection } from "../store/serverConnection";
import type { ServerStore } from "../store/serverStore";

/** How a wide window lays out sessions, as on iPad and the Mac; chosen in Settings. */
export type SidebarLayout = "inbox" | "projects" | "columns";

export const sidebarLayouts: { id: SidebarLayout; name: string; summary: string }[] = [
  { id: "inbox", name: "Inbox", summary: "Sessions from every server, those that need you first." },
  { id: "projects", name: "Projects", summary: "Each server's projects, with their sessions under them." },
  { id: "columns", name: "Columns", summary: "Needs You, Running and each project in the sidebar, their sessions beside it." },
];

/** A session and the server it is on, for the lists that mix servers. */
export interface ServerSession {
  connection: ServerConnection;
  session: Session;
}

export function sessionKey(item: ServerSession): string {
  return item.connection.server.id + " " + item.session.id;
}

export function projectOf(item: ServerSession): Project | undefined {
  return item.connection.projects.get(item.session.projectId);
}

/** Waiting for an answer, or failed: what a person has to look at. */
export function needsYou(s: Session): boolean {
  return s.status === "awaiting_approval" || s.status === "error";
}

/** Needs you or is unread: its title is bold in the lists. */
export function standsOut(s: Session): boolean {
  return needsYou(s) || !!s.unread;
}

/** Working, or done but with tasks left running in the background. */
export function isBusy(s: Session): boolean {
  return s.status === "running" || (s.status === "idle" && !!s.backgroundTasks?.length);
}

/** How much the session asks for a look: an answer it waits for, then a
 *  failure, then what ended since it was last read, then work, then work
 *  left in the background. Other idle and stopped sessions ask nothing. */
export function urgency(s: Session): number {
  if (s.status === "awaiting_approval") return 5;
  if (s.status === "error") return 4;
  if (s.unread) return 3;
  if (s.status === "running") return 2;
  if (s.status === "idle" && s.backgroundTasks?.length) return 1;
  return 0;
}

/** The session whose state a project's row shows for all of them: the one
 *  that asks most, if any does. */
export function mostPressing(sessions: Session[]): Session | undefined {
  let found: Session | undefined;
  for (const s of sessions) if (urgency(s) > (found ? urgency(found) : 0)) found = s;
  return found;
}

/** Every server's sessions, the latest first. */
export function allSessions(store: ServerStore, archived: boolean): ServerSession[] {
  return store.servers
    .map((s) => store.connection(s))
    .flatMap((c) => (archived ? c.archivedSessions : c.activeSessions).map((session) => ({ connection: c, session })))
    .sort((a, b) => parseDate(b.session.updatedAt) - parseDate(a.session.updatedAt));
}

/** The open session, by `sessionKey`, and whether it stood out when it was opened. */
export interface OpenedSession {
  key?: string;
  stoodOut: boolean;
}

/** The Inbox's groups, each in the order given: what needs you or is unread,
 *  then the rest. The open session counts as unread if it was when it was
 *  opened: reading it leaves its row where it is, and so does a turn that
 *  ends in view. */
export function inboxGroups(active: ServerSession[], opened?: OpenedSession): { needsYou: ServerSession[]; recent: ServerSession[] } {
  const stands = (i: ServerSession) =>
    needsYou(i.session) || (opened?.key !== undefined && sessionKey(i) === opened.key ? opened.stoodOut : !!i.session.unread);
  return { needsYou: active.filter(stands), recent: active.filter((i) => !stands(i)) };
}

/** What needs you first, then the rest, each in the order given. */
export function needsYouFirst(items: ServerSession[]): ServerSession[] {
  return [...items.filter((i) => needsYou(i.session)), ...items.filter((i) => !needsYou(i.session))];
}

/** What the sidebar of the three-column layout shows the sessions of. */
export type SessionCollection =
  | { kind: "needsYou" }
  | { kind: "running" }
  | { kind: "all" }
  | { kind: "archived" }
  | { kind: "project"; serverID: string; projectID: string };

/** As stored: "needsYou", or "project <server id> <project id>". */
export function parseCollection(stored: string | null): SessionCollection | undefined {
  const parts = (stored ?? "").split(" ").filter(Boolean);
  switch (parts[0]) {
    case "needsYou":
    case "running":
    case "all":
    case "archived":
      return { kind: parts[0] };
    case "project":
      return parts.length === 3 ? { kind: "project", serverID: parts[1], projectID: parts[2] } : undefined;
    default:
      return undefined;
  }
}

export function storedCollection(c: SessionCollection): string {
  return c.kind === "project" ? `project ${c.serverID} ${c.projectID}` : c.kind;
}

export function sameCollection(a: SessionCollection, b: SessionCollection): boolean {
  return storedCollection(a) === storedCollection(b);
}

export function collectionSessions(c: SessionCollection, store: ServerStore): ServerSession[] {
  const active = () => allSessions(store, false);
  switch (c.kind) {
    case "needsYou":
      return active().filter((i) => needsYou(i.session));
    case "running":
      return active().filter((i) => isBusy(i.session));
    case "all":
      // What needs you first, as in the other layouts.
      return needsYouFirst(active());
    case "archived":
      return allSessions(store, true);
    case "project":
      return active().filter((i) => i.connection.server.id === c.serverID && i.session.projectId === c.projectID);
  }
}

/** Where the Columns layout shows a session when the chosen collection
 *  doesn't have it: in its project, or in the archive. */
export function revealCollection(item: ServerSession): SessionCollection {
  if (item.session.archived) return { kind: "archived" };
  return projectOf(item) ? { kind: "project", serverID: item.connection.server.id, projectID: item.session.projectId } : { kind: "all" };
}

/** The session to open on choosing a collection: of its sessions, the one
 *  opened last, else its first. `recent` holds `sessionKey`s, the latest first. */
export function sessionToOpen(items: ServerSession[], recent: string[]): ServerSession | undefined {
  const byKey = new Map(items.map((i) => [sessionKey(i), i]));
  for (const key of recent) {
    const item = byKey.get(key);
    if (item) return item;
  }
  return items[0];
}

const recentLimit = 100;

/** The sessions opened lately, kept as `sessionKey`s on lines, the latest first. */
export function recentSessions(stored: string | null): string[] {
  return (stored ?? "").split("\n").filter(Boolean);
}

/** The stored list with `key` first. */
export function addingRecent(stored: string | null, key: string): string {
  return [key, ...recentSessions(stored).filter((k) => k !== key)].slice(0, recentLimit).join("\n");
}

export function collectionTitle(c: SessionCollection, store: ServerStore): string {
  switch (c.kind) {
    case "needsYou":
      return "Needs You";
    case "running":
      return "Running";
    case "all":
      return "All Sessions";
    case "archived":
      return "Archived";
    case "project": {
      const server = store.server(c.serverID);
      return (server && store.connection(server).projects.get(c.projectID)?.name) ?? "Project";
    }
  }
}

/** "now", "4m", "3h", "2d", then the date: a session's age where a row has little room. */
export function shortAge(ms: number, now = Date.now()): string {
  const s = (now - ms) / 1000;
  if (s < 60) return "now";
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86_400) return `${Math.floor(s / 3600)}h`;
  if (s < 7 * 86_400) return `${Math.floor(s / 86_400)}d`;
  return new Date(ms).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}
