// The search across servers, as RAKit's SessionSearch and its ports in android/core and ra.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { SearchResult, SearchResults, ServerInfo, Session } from "../protocol/models";
import { RPCError } from "../protocol/rpc";
import { mergeSearchResults, SessionSearch, type SearchSource } from "./sessionSearch";

const at = "2026-10-09T12:00:00Z";

function result(id: string, patch: Partial<SearchResult> = {}): SearchResult {
  const session: Session = { id, projectId: "p", harness: "fake", workspace: { kind: "root", path: "/p" }, status: "idle", title: id, archived: false, createdAt: at, updatedAt: at };
  return { session, itemId: "i-" + id, itemKind: "assistant_message", snippet: [{ text: id, match: true }], matches: 1, ...patch };
}

/** A server that answers each search when the test says so. */
class FakeServer implements SearchSource {
  readonly server: { id: string; name: string };
  isConnected = true;
  info: ServerInfo | undefined;
  calls: { query: string; archived: boolean; limit?: number }[] = [];
  private pending: { resolve: (r: SearchResults) => void; reject: (e: Error) => void }[] = [];

  constructor(id: string, search = true) {
    this.server = { id, name: id.toUpperCase() };
    this.info = { serverId: id, name: id, protocolVersion: 1, version: "1", search: search || undefined };
  }

  search(query: string, archived: boolean, limit?: number): Promise<SearchResults> {
    this.calls.push({ query, archived, limit });
    return new Promise((resolve, reject) => this.pending.push({ resolve, reject }));
  }

  /** Answers the oldest search not answered yet. */
  answer(results: SearchResult[], extra: Partial<SearchResults> = {}) {
    this.pending.shift()!.resolve({ results, ...extra });
  }

  fail(message: string) {
    this.pending.shift()!.reject(new RPCError("internal", message));
  }

  get waiting(): number {
    return this.pending.length;
  }
}

/** Lets the promises settled so far run their callbacks. */
const settle = () => vi.advanceTimersByTimeAsync(0);

describe("mergeSearchResults", () => {
  it("takes every server's first, then every server's second", () => {
    expect(mergeSearchResults([["a1", "a2", "a3"], ["b1"], [], ["c1", "c2"]])).toEqual(["a1", "b1", "c1", "a2", "c2", "a3"]);
    expect(mergeSearchResults([])).toEqual([]);
    expect(mergeSearchResults([[], []])).toEqual([]);
    expect(mergeSearchResults([["a1", "a2"]])).toEqual(["a1", "a2"]);
  });
});

describe("SessionSearch", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("searches every server once the query is long enough, after a pause", async () => {
    const a = new FakeServer("a");
    const b = new FakeServer("b");
    const search = new SessionSearch(() => [a, b]);
    let changes = 0;
    search.watch(() => changes++);

    // One character isn't enough.
    search.setQuery("t");
    expect(search.isActive).toBe(false);
    await vi.advanceTimersByTimeAsync(1000);
    expect(a.calls).toEqual([]);
    expect(search.isSearching).toBe(false);

    // Typing on waits until it stops.
    search.setQuery("te");
    search.setQuery("tes");
    expect(search.isActive).toBe(true);
    expect(search.isSearching).toBe(true);
    await vi.advanceTimersByTimeAsync(200);
    search.setQuery("test ");
    await vi.advanceTimersByTimeAsync(200);
    expect(a.calls).toEqual([]);
    await vi.advanceTimersByTimeAsync(100);
    // Trimmed, with the archive by default, and the limit.
    expect(a.calls).toEqual([{ query: "test", archived: true, limit: 30 }]);
    expect(b.calls).toEqual([{ query: "test", archived: true, limit: 30 }]);

    // Merged as they come, in the servers' order.
    b.answer([result("b1"), result("b2")], { indexing: true });
    await settle();
    expect(search.hits.map((h) => h.id)).toEqual(["b/b1", "b/b2"]);
    expect(search.isSearching).toBe(true);
    expect(search.indexing).toEqual(["B"]);
    a.answer([result("a1"), result("a2"), result("a3")], { more: true });
    await settle();
    expect(search.hits.map((h) => h.id)).toEqual(["a/a1", "b/b1", "a/a2", "b/b2", "a/a3"]);
    expect(search.hits[1]).toMatchObject({ serverId: "b", result: { session: { id: "b1" }, itemId: "i-b1" } });
    expect(search.isSearching).toBe(false);
    expect(search.more).toBe(true);
    expect(search.failures).toEqual([]);
    expect(changes).toBeGreaterThan(0);

    // The archive left out searches again.
    search.setIncludeArchived(false);
    await vi.advanceTimersByTimeAsync(250);
    expect(a.calls.at(-1)).toEqual({ query: "test", archived: false, limit: 30 });

    // Too short again: nothing, and no search.
    search.setQuery("t");
    expect(search.hits).toEqual([]);
    expect(search.isSearching).toBe(false);
    expect(search.more).toBe(false);
    expect(search.indexing).toEqual([]);
    await vi.advanceTimersByTimeAsync(1000);
    expect(a.calls).toHaveLength(2);
  });

  it("drops an answer to an earlier query", async () => {
    const a = new FakeServer("a");
    const search = new SessionSearch(() => [a]);
    search.setQuery("old");
    await vi.advanceTimersByTimeAsync(250);
    search.setQuery("new");
    await vi.advanceTimersByTimeAsync(250);
    expect(a.calls.map((c) => c.query)).toEqual(["old", "new"]);

    // The old answer comes last-but-one, and is dropped.
    a.answer([result("old")]);
    await settle();
    expect(search.hits).toEqual([]);
    expect(search.isSearching).toBe(true);
    a.answer([result("new")]);
    await settle();
    expect(search.hits.map((h) => h.result.session.id)).toEqual(["new"]);
    expect(search.isSearching).toBe(false);

    // So is one that comes after the query was cleared.
    search.setQuery("again");
    await vi.advanceTimersByTimeAsync(250);
    search.setQuery("");
    a.answer([result("again")]);
    await settle();
    expect(search.hits).toEqual([]);
    expect(search.isSearching).toBe(false);
  });

  it("reports a server that failed, with the others' results", async () => {
    const a = new FakeServer("a");
    const b = new FakeServer("b");
    const search = new SessionSearch(() => [a, b]);
    search.setQuery("tests");
    await vi.advanceTimersByTimeAsync(250);
    a.fail("The server did not respond in time");
    b.answer([result("b1")]);
    await settle();
    expect(search.failures).toEqual([{ server: "A", message: "The server did not respond in time" }]);
    expect(search.hits.map((h) => h.id)).toEqual(["b/b1"]);
    expect(search.isSearching).toBe(false);
    expect(search.answered).toBe(1);

    // A search that works there forgets the failure.
    search.setQuery("tests again");
    await vi.advanceTimersByTimeAsync(250);
    a.answer([]);
    b.answer([]);
    await settle();
    expect(search.failures).toEqual([]);
    expect(search.hits).toEqual([]);
  });

  it("skips servers that don't search or aren't connected", async () => {
    const a = new FakeServer("a");
    const old = new FakeServer("old", false);
    const away = new FakeServer("away");
    away.isConnected = false;
    const sources = [a, old, away];
    const search = new SessionSearch(() => sources);
    expect(search.serversKey()).toBe("a !old");
    search.setQuery("tests");
    // Not while it waits to search.
    search.serversChanged();
    expect(a.calls).toEqual([]);
    await vi.advanceTimersByTimeAsync(250);
    expect(old.calls).toEqual([]);
    expect(away.calls).toEqual([]);
    expect(search.unsupported).toEqual(["OLD"]);
    a.answer([result("a1")]);
    await settle();
    expect(search.hits.map((h) => h.id)).toEqual(["a/a1"]);

    // Nothing changed: no search.
    search.serversChanged();
    await settle();
    expect(a.calls).toHaveLength(1);

    // Once it connects, it's asked too, at once.
    away.isConnected = true;
    expect(search.serversKey()).toBe("a !old away");
    search.serversChanged();
    await settle();
    expect(away.calls).toHaveLength(1);
    expect(a.calls).toHaveLength(2);
    a.answer([result("a1")]);
    away.answer([result("w1")]);
    await settle();
    expect(search.hits.map((h) => h.id)).toEqual(["a/a1", "away/w1"]);

    // With no server to ask, it's done at once, with nothing.
    const none = new SessionSearch(() => [old]);
    none.setQuery("tests");
    await vi.advanceTimersByTimeAsync(250);
    expect(none.isSearching).toBe(false);
    expect(none.hits).toEqual([]);
    expect(none.unsupported).toEqual(["OLD"]);
    expect(none.noServers).toBe(false);
    expect(a.waiting + away.waiting).toBe(0);

    // None connected: it says so rather than that nothing matched.
    const off = new FakeServer("off");
    off.isConnected = false;
    const offline = new SessionSearch(() => [off]);
    offline.setQuery("tests");
    await vi.advanceTimersByTimeAsync(250);
    expect(offline.noServers).toBe(true);
    expect(offline.answered).toBe(0);
  });
});
