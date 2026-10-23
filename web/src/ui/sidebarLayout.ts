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

/** Working, or done but with tasks left running in the background. */
export function isBusy(s: Session): boolean {
  return s.status === "running" || (s.status === "idle" && !!s.backgroundTasks?.length);
}

/** How much the session asks for a look: an answer it waits for, then a
 *  failure, then work, then work left in the background. Idle and stopped
 *  sessions ask nothing. */
export function urgency(s: Session): number {
  switch (s.status) {
    case "awaiting_approval":
      return 4;
    case "error":
      return 3;
    case "running":
      return 2;
    case "idle":
      return s.backgroundTasks?.length ? 1 : 0;
    default:
      return 0;
  }
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
