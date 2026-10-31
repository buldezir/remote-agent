// As apple/RAKit/Tests/RAKitTests/StoreTests.swift.
import { describe, expect, it, vi } from "vitest";
import type { Event, HarnessInfo, Item, Session } from "../protocol/models";
import { RPCError } from "../protocol/rpc";
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
    deliver<R>(method: string, params: unknown): Promise<R> {
      return h.call<R>(method, params);
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

  it("keeps the last replay when subscriptions overlap", () => {
    const s = new SessionStore("s", undefined);
    // A view subscribes while the connection resubscribes everything.
    s.beginReplay();
    s.beginReplay();
    const replay = [event(3, "b", 2, "second"), event(7, "a", 1, "first")];
    s.apply(replay);
    s.synchronized(7, true);
    expect(s.live).toBe(false); // the second subscription hasn't synchronized yet
    s.apply(replay);
    s.synchronized(7, true);
    expect(s.live).toBe(true);
    expect(s.transcript.map((i) => i.id)).toEqual(["a", "b"]);
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

  it("empties when the session is deleted", async () => {
    const s = new SessionStore("s", host());
    s.synchronized(1, true);
    s.setSession({ id: "s" } as Session);
    s.apply([event(2, "a", 1, "first")]);
    await s.send("pending");
    expect(s.removed).toBe(false);
    s.apply([{ stream: "session:s", seq: 3, type: "session.removed", id: "s", ts: "2026-10-09T12:00:00Z" }]);
    expect(s.removed).toBe(true);
    expect(s.session).toBeUndefined();
    expect(s.transcript).toEqual([]);
    expect(s.items.size).toBe(0);
    expect(s.turns.size).toBe(0);
    expect(s.outbox.size).toBe(0);
  });

  it("is removed when a later subscription replays the deletion", () => {
    const s = new SessionStore("s", undefined);
    s.synchronized(2, true);
    s.apply([event(3, "a", 1, "first")]);
    // Away while it was deleted: the stream now holds only the removal.
    s.beginReplay();
    s.apply([{ stream: "session:s", seq: 4, type: "session.removed", id: "s" }]);
    s.synchronized(4, true);
    expect(s.removed).toBe(true);
    expect(s.transcript).toEqual([]);
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

  it("sends the draft through the outbox", async () => {
    const h = host();
    const s = new SessionStore("s", h);
    s.synchronized(0, true);
    const image = { id: "img.png", mimeType: "image/png", size: 1 };
    s.setSession({ id: "s", draft: { text: "later", images: [image] } } as Session);
    expect(s.unsentDraft?.text).toBe("later");
    await s.sendDraft();
    // On its way: in the outbox, as a prompt sent, and no longer a draft to show.
    expect([...s.outbox.values()]).toEqual([{ text: "later", images: [image] }]);
    expect(s.unsentDraft).toBeUndefined();
    expect(h.calls.map(([m]) => m)).toEqual(["session.sendDraft"]);
    expect(h.calls[0][1]).toMatchObject({ sessionId: "s", commandId: expect.any(String) });
    // The user message comes with the session without its draft.
    const message = event(2, "u", 1, "later", "user_message");
    message.item!.images = [image];
    s.apply([{ stream: "session:s", seq: 1, type: "session.upserted", session: { id: "s" } as Session }, message]);
    expect(s.outbox.size).toBe(0);
    expect(s.unsentDraft).toBeUndefined();
    expect(s.transcript.map((i) => i.text)).toEqual(["later"]);
  });

  it("keeps the draft when sending it fails", async () => {
    const h = host();
    h.call = async () => {
      throw new Error("nope");
    };
    const s = new SessionStore("s", h);
    s.setSession({ id: "s", draft: { text: "later" } } as Session);
    await expect(s.sendDraft()).rejects.toThrow("nope");
    expect(s.outbox.size).toBe(0);
    expect(s.unsentDraft).toEqual({ text: "later" });
  });

  it("has no draft to send without one", async () => {
    const h = host();
    const s = new SessionStore("s", h);
    s.setSession({ id: "s" } as Session);
    await expect(s.sendDraft()).rejects.toMatchObject({ code: "conflict" });
    expect(h.calls).toEqual([]);
    expect(s.outbox.size).toBe(0);
  });

  it("sends a queued prompt now", async () => {
    const h = host();
    const s = new SessionStore("s", h);
    await s.sendNow("u");
    expect(h.calls).toEqual([["session.sendNow", { sessionId: "s", itemId: "u" }]]);
  });

  it("says when rad is too old to send a queued prompt now", async () => {
    const h = host();
    h.call = async () => {
      throw new RPCError("method_not_found", "unknown method session.sendNow");
    };
    const s = new SessionStore("s", h);
    await expect(s.sendNow("u")).rejects.toThrow("This server's rad is too old to send a queued prompt now. Update rad on it.");
    // Other errors pass through.
    h.call = async () => {
      throw new RPCError("not_found", "no such item");
    };
    await expect(s.sendNow("u")).rejects.toThrow("no such item");
  });

  it("steers a queued prompt into the turn", async () => {
    const h = host();
    const s = new SessionStore("s", h);
    await s.steer("u");
    expect(h.calls).toEqual([["session.steer", { sessionId: "s", itemId: "u" }]]);
  });

  it("says when rad is too old to steer", async () => {
    const h = host();
    h.call = async () => {
      throw new RPCError("method_not_found", "unknown method session.steer");
    };
    const s = new SessionStore("s", h);
    await expect(s.steer("u")).rejects.toThrow("This server's rad is too old to steer a prompt into a turn. Update rad on it.");
    // Other errors pass through.
    h.call = async () => {
      throw new RPCError("not_found", "no such item");
    };
    await expect(s.steer("u")).rejects.toThrow("no such item");
  });

  it("sends a prompt that steers", async () => {
    const h = host();
    const s = new SessionStore("s", h);
    await s.send("also this", [], true);
    expect(h.calls[0]).toEqual(["session.prompt", expect.objectContaining({ text: "also this", steer: true })]);
    // Without steer, the prompt says nothing of it.
    await s.send("later");
    expect((h.calls[1][1] as Record<string, unknown>).steer).toBeUndefined();
  });

  it("steers while the agent that can take it works, and not slash commands", () => {
    const s = new SessionStore("s", host());
    const steers = { id: "claude", caps: { steer: true } } as HarnessInfo;
    const plain = { id: "acp:cursor", caps: {} } as HarnessInfo;
    const status = (st: string, seq: number) =>
      s.apply([{ stream: "session:s", seq, type: "session.upserted", session: { id: "s", status: st } as Session }]);
    s.synchronized(0, true);
    status("idle", 1);
    expect(s.canSteer("more", steers)).toBe(false);
    status("running", 2);
    expect(s.canSteer("more", steers)).toBe(true);
    status("awaiting_approval", 3);
    expect(s.canSteer("more", steers)).toBe(true);
    expect(s.canSteer("more", plain)).toBe(false);
    expect(s.canSteer("more", undefined)).toBe(false);
    expect(s.canSteer(" /compact", steers)).toBe(false);
  });

  it("cancels a queued prompt", async () => {
    const h = host();
    const s = new SessionStore("s", h);
    await s.cancelQueued("u");
    expect(h.calls).toEqual([["session.cancelQueued", { sessionId: "s", itemId: "u" }]]);
  });

  it("says when rad is too old to cancel a queued prompt", async () => {
    const h = host();
    h.call = async () => {
      throw new RPCError("method_not_found", "unknown method session.cancelQueued");
    };
    const s = new SessionStore("s", h);
    await expect(s.cancelQueued("u")).rejects.toThrow("This server's rad is too old to cancel a queued prompt. Update rad on it.");
    // Other errors pass through.
    h.call = async () => {
      throw new RPCError("not_found", "no such item");
    };
    await expect(s.cancelQueued("u")).rejects.toThrow("no such item");
  });
});

it("falls back on old servers but preserves real discovery errors", async () => {
  const h = host();
  h.call = async () => { throw new RPCError("method_not_found", "old server"); };
  const store = new SessionStore("s", h);
  expect(await store.commands()).toEqual([]);
  h.call = async () => { throw new RPCError("conflict", "runtime replaced"); };
  await expect(store.commands()).rejects.toMatchObject({ code: "conflict" });
});
