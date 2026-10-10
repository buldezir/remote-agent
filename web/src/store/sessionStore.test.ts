// As apple/RAKit/Tests/RAKitTests/StoreTests.swift.
import { describe, expect, it, vi } from "vitest";
import type { Event, Item } from "../protocol/models";
import { SessionStore, type SessionHost } from "./sessionStore";

function event(seq: number, id: string, order: number, text: string, kind = "assistant_message", parent?: string): Event {
  const item: Item = {
    id,
    sessionId: "s",
    order,
    kind,
    status: "completed",
    text,
    parentItemId: parent,
    createdAt: "2026-10-09T12:00:00.123456789Z",
    updatedAt: "2026-10-09T12:00:00Z",
  };
  return { stream: "session:s", seq, type: "item.upserted", item };
}

function host(): SessionHost & { subscribed: number; calls: [string, unknown][] } {
  const h = {
    subscribed: 0,
    calls: [] as [string, unknown][],
    isConnected: true,
    activate: vi.fn(),
    deactivate: vi.fn(),
    async subscribe() {
      h.subscribed++;
    },
    async call<R>(method: string, params?: unknown): Promise<R> {
      h.calls.push([method, params]);
      return {} as R;
    },
  };
  return h;
}

describe("SessionStore", () => {
  it("replays, then applies live events", () => {
    const s = new SessionStore("s", undefined);
    // Replay (non-contiguous seqs are fine before synchronized).
    s.apply([event(3, "b", 2, "second"), event(7, "a", 1, "first")]);
    expect(s.transcript).toEqual([]); // not rebuilt until synchronized
    s.synchronized(7, true);
    expect(s.transcript.map((i) => i.id)).toEqual(["a", "b"]);
    // Live, contiguous.
    s.apply([event(8, "b", 2, "second, edited")]);
    expect(s.transcript.at(-1)?.text).toBe("second, edited");
    // A duplicate seq is ignored.
    s.apply([event(8, "b", 2, "stale")]);
    expect(s.transcript.at(-1)?.text).toBe("second, edited");
  });

  it("drops stale state on reset but keeps the replay", () => {
    const s = new SessionStore("s", undefined);
    s.synchronized(1, true);
    s.apply([event(2, "old", 1, "old")]);
    expect(s.transcript.map((i) => i.id)).toEqual(["old"]);
    // Resubscribe from scratch: the server replays only what exists now.
    s.beginReplay();
    s.apply([event(9, "new", 1, "new")]);
    s.synchronized(9, true);
    expect(s.transcript.map((i) => i.id)).toEqual(["new"]);
  });

  it("resubscribes after a gap", () => {
    const h = host();
    const s = new SessionStore("s", h);
    s.synchronized(1, true);
    s.apply([event(5, "x", 1, "after a gap")]);
    expect(s.items.has("x")).toBe(false);
    expect(s.live).toBe(false);
    expect(h.subscribed).toBe(1);
  });

  it("nests children under their parent", () => {
    const s = new SessionStore("s", undefined);
    s.apply([event(1, "tool", 1, "", "notice"), event(2, "child", 2, "sub", "assistant_message", "tool")]);
    s.synchronized(2, true);
    expect(s.transcript.map((i) => i.id)).toEqual(["tool"]);
    expect(s.childrenOf(s.transcript[0]).map((i) => i.id)).toEqual(["child"]);
  });

  it("stays active while any view shows it", () => {
    const h = host();
    const s = new SessionStore("s", h);
    s.activate();
    s.activate();
    s.deactivate();
    expect(s.isActive).toBe(true);
    s.deactivate();
    expect(s.isActive).toBe(false);
    s.deactivate();
    s.activate();
    expect(s.isActive).toBe(true);
    expect(h.activate).toHaveBeenCalledTimes(2);
    expect(h.deactivate).toHaveBeenCalledTimes(1);
  });

  it("keeps a prompt in the outbox until the server confirms it", async () => {
    const h = host();
    const s = new SessionStore("s", h);
    s.synchronized(0, true);
    await s.send("hello");
    expect([...s.outbox.values()]).toEqual([{ text: "hello", images: [] }]);
    expect(h.calls[0][0]).toBe("session.prompt");
    s.apply([event(1, "u", 1, "hello", "user_message")]);
    expect(s.outbox.size).toBe(0);
  });

  it("drops the outbox entry when sending fails", async () => {
    const h = host();
    h.call = async () => {
      throw new Error("nope");
    };
    const s = new SessionStore("s", h);
    await expect(s.send("hello")).rejects.toThrow("nope");
    expect(s.outbox.size).toBe(0);
  });
});
