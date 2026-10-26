import { describe, expect, it } from "vitest";
import type { Session } from "../protocol/models";
import type { ServerConnection } from "../store/serverConnection";
import {
  addingRecent,
  isBusy,
  mostPressing,
  needsYou,
  parseCollection,
  recentSessions,
  revealCollection,
  sessionToOpen,
  shortAge,
  storedCollection,
  urgency,
  type ServerSession,
  type SessionCollection,
} from "./sidebarLayout";

const session = (patch: Partial<Session>): Session => ({
  id: "s",
  projectId: "p",
  harness: "fake",
  workspace: { kind: "root", path: "/tmp/p" },
  status: "idle",
  title: "t",
  archived: false,
  createdAt: "2026-10-15T10:00:00Z",
  updatedAt: "2026-10-15T10:00:00Z",
  ...patch,
});

describe("sidebar layouts", () => {
  it("stores and reads collections", () => {
    const all: SessionCollection[] = [
      { kind: "needsYou" },
      { kind: "running" },
      { kind: "all" },
      { kind: "archived" },
      { kind: "project", serverID: "srv", projectID: "prj" },
    ];
    for (const c of all) expect(parseCollection(storedCollection(c))).toEqual(c);
    expect(parseCollection(null)).toBeUndefined();
    expect(parseCollection("project srv")).toBeUndefined();
    expect(parseCollection("bogus")).toBeUndefined();
  });

  it("groups sessions by what they need", () => {
    expect(needsYou(session({ status: "awaiting_approval" }))).toBe(true);
    expect(needsYou(session({ status: "error" }))).toBe(true);
    expect(needsYou(session({ status: "running" }))).toBe(false);
    expect(isBusy(session({ status: "running" }))).toBe(true);
    expect(isBusy(session({ backgroundTasks: [{ id: "b", kind: "command", description: "watch" }] }))).toBe(true);
    expect(isBusy(session({}))).toBe(false);
    expect(isBusy(session({ status: "stopped", backgroundTasks: [{ id: "b", kind: "command", description: "watch" }] }))).toBe(false);
  });

  it("shows a project's most pressing session", () => {
    const background = session({ id: "background", backgroundTasks: [{ id: "b", kind: "command", description: "watch" }] });
    const running = session({ id: "running", status: "running" });
    const failed = session({ id: "failed", status: "error" });
    const waiting = session({ id: "waiting", status: "awaiting_approval" });
    const idle = [session({ id: "idle" }), session({ id: "stopped", status: "stopped" })];
    expect(mostPressing([])).toBeUndefined();
    expect(mostPressing(idle)).toBeUndefined();
    expect(mostPressing([...idle, background])?.id).toBe("background");
    expect(mostPressing([background, running, ...idle])?.id).toBe("running");
    expect(mostPressing([running, failed])?.id).toBe("failed");
    expect(mostPressing([failed, waiting, running])?.id).toBe("waiting");

    // What ended since it was read comes after what asks for the user, before work.
    const unread = session({ id: "unread", unread: true });
    expect(mostPressing([...idle, unread])?.id).toBe("unread");
    expect(mostPressing([background, running, unread])?.id).toBe("unread");
    expect(mostPressing([unread, failed])?.id).toBe("failed");
    expect(mostPressing([waiting, unread])?.id).toBe("waiting");
  });

  it("ranks an unread session by what it asks for", () => {
    expect(urgency(session({ status: "awaiting_approval", unread: true }))).toBe(5);
    expect(urgency(session({ status: "error", unread: true }))).toBe(4);
    expect(urgency(session({ unread: true }))).toBe(3);
    expect(urgency(session({ status: "running", unread: true }))).toBe(3);
    expect(urgency(session({ status: "stopped", unread: true }))).toBe(3);
    expect(urgency(session({ status: "running" }))).toBe(2);
    expect(urgency(session({ backgroundTasks: [{ id: "b", kind: "command", description: "watch" }] }))).toBe(1);
    expect(urgency(session({ unread: false }))).toBe(0);
    expect(urgency(session({ status: "stopped" }))).toBe(0);
  });

  it("gives short ages", () => {
    const now = Date.parse("2026-10-15T12:00:00Z");
    expect(shortAge(now - 20_000, now)).toBe("now");
    expect(shortAge(now - 4 * 60_000, now)).toBe("4m");
    expect(shortAge(now - 3 * 3600_000, now)).toBe("3h");
    expect(shortAge(now - 2 * 86_400_000, now)).toBe("2d");
    expect(shortAge(now - 30 * 86_400_000, now)).not.toMatch(/d$/);
  });

  it("reveals a session in its project, or in the archive", () => {
    const connection = { server: { id: "srv" }, projects: new Map([["p", { id: "p", name: "p" }]]) } as unknown as ServerConnection;
    expect(revealCollection({ connection, session: session({}) })).toEqual({ kind: "project", serverID: "srv", projectID: "p" });
    expect(revealCollection({ connection, session: session({ archived: true }) })).toEqual({ kind: "archived" });
    expect(revealCollection({ connection, session: session({ projectId: "forgotten" }) })).toEqual({ kind: "all" });
  });

  it("opens a collection's session opened last, else its first", () => {
    const connection = { server: { id: "srv" } } as unknown as ServerConnection;
    const items: ServerSession[] = ["a", "b", "c"].map((id) => ({ connection, session: session({ id }) }));
    expect(sessionToOpen(items, [])?.session.id).toBe("a");
    expect(sessionToOpen(items, ["srv x", "srv c", "srv b"])?.session.id).toBe("c");
    expect(sessionToOpen(items, ["other c"])?.session.id).toBe("a");
    expect(sessionToOpen([], ["srv c"])).toBeUndefined();
  });

  it("keeps the sessions opened lately, the latest first", () => {
    let stored = addingRecent(null, "srv a");
    stored = addingRecent(stored, "srv b");
    stored = addingRecent(stored, "srv a");
    expect(recentSessions(stored)).toEqual(["srv a", "srv b"]);
    for (let i = 0; i < 150; i++) stored = addingRecent(stored, `srv ${i}`);
    expect(recentSessions(stored)).toHaveLength(100);
    expect(recentSessions(stored)[0]).toBe("srv 149");
    expect(recentSessions(null)).toEqual([]);
  });
});
