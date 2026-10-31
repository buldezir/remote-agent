// As apple/RAKit/Tests/RAKitTests/ConnectionTests.swift.
import { createHash } from "node:crypto";
import { createServer, type IncomingMessage } from "node:http";
import type { AddressInfo } from "node:net";
import type { Duplex } from "node:stream";
import { afterEach, describe, expect, it, vi } from "vitest";
import { RPCClient, RPCError } from "../protocol/rpc";
import type { ServerTask } from "../protocol/models";
import { ServerConnection } from "./serverConnection";
import type { SavedServer } from "./serverStore";
import { ServerTaskProgress } from "./serverTask";

function saved(url: string): SavedServer {
  return { id: "srv", name: "Test", urls: [url], deviceId: "dev1", addedAt: "2026-10-09T12:00:00Z" };
}

/** Counts the sockets opened, one per attempt. */
function countAttempts(): () => number {
  let n = 0;
  vi.stubGlobal(
    "WebSocket",
    class extends WebSocket {
      constructor(url: string | URL, protocols?: string | string[]) {
        n++;
        super(url, protocols);
      }
    },
  );
  return () => n;
}

/** The kinds of state the connection went through, without repeats. */
function states(c: ServerConnection): string[] {
  const seen = [c.state.kind];
  c.watch(() => {
    if (seen.at(-1) !== c.state.kind) seen.push(c.state.kind);
  });
  return seen;
}

/** Polls until `condition` holds, or fails after `timeout` ms. */
async function waitFor(condition: () => boolean, timeout = 10_000) {
  const deadline = Date.now() + timeout;
  while (!condition()) {
    if (Date.now() > deadline) throw new Error("timed out");
    await new Promise((r) => setTimeout(r, 5));
  }
}

const pause = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** A WebSocket server on loopback that stands in for rad: it answers
 *  `server.info` and `session.prompt`, ignores `slow`, and records the calls.
 *  `quiet()` makes the sockets open so far ignore everything, as a socket
 *  that died while the computer slept does: nothing comes back, and nothing
 *  says it closed. As apple/RAKit/Tests/RAKitTests/TestServer.swift. */
class TestServer {
  /** Every call that arrived, answered or not. */
  calls: { connection: number; method: string; commandId?: string; params?: any }[] = [];
  /** The version `server.info` reports. */
  version = "0.1";
  /** Answers for other methods: a result, or an error to send. */
  handlers: Record<string, (params: any) => object | RPCError> = {};
  private http = createServer((_, res) => res.writeHead(404).end());
  private sockets: Duplex[] = [];
  private quietCount = 0;

  /** Starts listening; returns the base URL. */
  async start(): Promise<string> {
    this.http.on("upgrade", (req, socket) => this.accept(req, socket));
    await new Promise<void>((r) => this.http.listen(0, "127.0.0.1", r));
    return `http://127.0.0.1:${(this.http.address() as AddressInfo).port}`;
  }

  async stop() {
    for (const s of this.sockets) s.destroy();
    this.http.closeAllConnections();
    await new Promise((r) => this.http.close(r));
  }

  /** Closes every socket, as rad does when it restarts. */
  drop() {
    for (const s of this.sockets) s.destroy();
  }

  /** The sockets open now stop answering; later ones answer. */
  quiet() {
    this.quietCount = this.sockets.length;
  }

  get connectionCount(): number {
    return this.sockets.length;
  }

  private accept(req: IncomingMessage, socket: Duplex) {
    const n = this.sockets.length;
    this.sockets.push(socket);
    socket.on("error", () => {});
    const accept = createHash("sha1")
      .update(req.headers["sec-websocket-key"] + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11")
      .digest("base64");
    socket.write(
      "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
        `Sec-WebSocket-Accept: ${accept}\r\nSec-WebSocket-Protocol: rad.v1\r\n\r\n`,
    );
    let buffered = Buffer.alloc(0);
    socket.on("data", (chunk: Buffer) => {
      buffered = Buffer.concat([buffered, chunk]);
      for (let f = readFrame(buffered); f; f = readFrame(buffered)) {
        buffered = buffered.subarray(f.size);
        const quiet = n < this.quietCount;
        if (f.opcode === 1) this.answer(socket, n, f.payload.toString(), quiet);
        else if (f.opcode === 8 && !quiet) socket.end();
      }
    });
  }

  private answer(socket: Duplex, n: number, text: string, quiet: boolean) {
    const req = JSON.parse(text);
    this.calls.push({ connection: n, method: req.method, commandId: req.params?.commandId, params: req.params });
    if (quiet || req.method === "slow") return;
    let result: object = {};
    const handler = this.handlers[req.method];
    if (handler) {
      const r = handler(req.params);
      const reply = r instanceof RPCError ? { id: req.id, error: { code: r.code, message: r.message } } : { id: req.id, result: r };
      socket.write(textFrame(JSON.stringify(reply)));
      return;
    }
    if (req.method === "server.info") result = { serverId: "srv", name: "Test", protocolVersion: 1, version: this.version };
    if (req.method === "session.prompt") {
      const at = "2026-01-01T00:00:00Z";
      result = { id: "i" + req.id, sessionId: req.params.sessionId, order: 1, kind: "user_message", status: "queued", createdAt: at, updatedAt: at };
    }
    socket.write(textFrame(JSON.stringify({ id: req.id, result })));
  }
}

/** The client frame at the start of `data`, unmasked, if it has all arrived. */
function readFrame(data: Buffer): { opcode: number; payload: Buffer; size: number } | undefined {
  if (data.length < 2) return undefined;
  let length = data[1] & 0x7f;
  let at = 2;
  if (length === 126) {
    if (data.length < 4) return undefined;
    length = data.readUInt16BE(2);
    at = 4;
  } else if (length === 127) {
    if (data.length < 10) return undefined;
    length = Number(data.readBigUInt64BE(2));
    at = 10;
  }
  const size = at + 4 + length;
  if (data.length < size) return undefined;
  const mask = data.subarray(at, at + 4);
  const payload = Buffer.from(data.subarray(at + 4, size));
  for (let i = 0; i < payload.length; i++) payload[i] ^= mask[i % 4];
  return { opcode: data[0] & 0x0f, payload, size };
}

/** A server's text frame, which isn't masked. */
function textFrame(text: string): Buffer {
  const payload = Buffer.from(text);
  const head = payload.length < 126 ? Buffer.from([0x81, payload.length]) : Buffer.from([0x81, 126, payload.length >> 8, payload.length & 0xff]);
  return Buffer.concat([head, payload]);
}

/** A connection to `server`, connected, with short timings. */
async function connect(server: TestServer): Promise<ServerConnection> {
  const c = new ServerConnection(saved(await server.start()), "tok", () => {});
  c.firstRetryDelay = 0.01;
  c.heartbeat = 0.05;
  c.pingTimeout = 0.3;
  c.start();
  await waitFor(() => c.state.kind === "connected");
  return c;
}

describe("ServerConnection", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("gives up after maxAttempts failures until started again", async () => {
    const attempts = countAttempts();
    // Nothing listens on port 1, so each attempt fails at once.
    const c = new ServerConnection(saved("http://127.0.0.1:1"), "tok", () => {});
    c.firstRetryDelay = 0.01;
    const seen = states(c);
    c.start();
    expect(c.state.kind).toBe("connecting");
    await waitFor(() => c.state.kind === "failed");
    // Connecting between attempts, failed only once it gave up.
    expect(seen).toEqual(["idle", "connecting", "failed"]);
    expect(attempts()).toBe(ServerConnection.maxAttempts);

    // Given up: no more attempts, so it stays failed.
    await pause(200);
    expect(c.state.kind).toBe("failed");
    expect(attempts()).toBe(ServerConnection.maxAttempts);

    // Opening the server, or coming back to the page, tries again.
    c.start();
    expect(c.state.kind).toBe("connecting");
    await waitFor(() => c.state.kind === "failed");
    expect(attempts()).toBe(2 * ServerConnection.maxAttempts);

    // So does Retry.
    c.retry();
    expect(c.state.kind).toBe("connecting");
    await waitFor(() => c.state.kind === "failed");
    expect(attempts()).toBe(3 * ServerConnection.maxAttempts);
    c.stop();
  });

  it("gives up at once when the device is no longer authorized", async () => {
    // A server that turns the token away: the upgrade and the HTTP check alike.
    const server = createServer((_, res) => {
      res.writeHead(401).end();
    });
    server.on("upgrade", (_, socket) => {
      socket.end("HTTP/1.1 401 Unauthorized\r\nContent-Length: 0\r\nConnection: close\r\n\r\n");
    });
    await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
    try {
      const attempts = countAttempts();
      const { port } = server.address() as AddressInfo;
      const c = new ServerConnection(saved(`http://127.0.0.1:${port}`), "tok", () => {});
      c.firstRetryDelay = 0.01;
      c.start();
      await waitFor(() => c.state.kind === "failed");
      expect(c.state).toEqual({ kind: "failed", message: RPCError.unauthorized().message });
      await pause(100);
      expect(attempts()).toBe(1);
      c.stop();
    } finally {
      server.closeAllConnections();
      await new Promise((r) => server.close(r));
    }
  });

  it("drops a socket that stops answering", async () => {
    const server = new TestServer();
    const c = await connect(server);
    try {
      // While it answers, the pings keep it.
      await pause(300);
      expect(server.connectionCount).toBe(1);

      // Gone quiet, as after a sleep: the ping goes unanswered, so it
      // reconnects rather than passing for connected.
      server.quiet();
      await waitFor(() => server.connectionCount === 2);
      await waitFor(() => c.state.kind === "connected");
    } finally {
      c.stop();
      await server.stop();
    }
  });

  it("checks the socket at once when the page comes back", async () => {
    const server = new TestServer();
    const c = await connect(server);
    try {
      // No heartbeat for a minute, so only the check finds the dead socket.
      c.heartbeat = 60;
      server.quiet();
      c.check();
      await waitFor(() => server.connectionCount === 2);
      await waitFor(() => c.state.kind === "connected");
    } finally {
      c.stop();
      await server.stop();
    }
  });

  it("delivers a prompt sent on a dead socket after reconnecting", async () => {
    const server = new TestServer();
    const c = await connect(server);
    try {
      const store = c.sessionStore("s1");
      server.quiet();
      await store.send("hello");

      // Sent again on the new socket, with the same commandId.
      const prompts = server.calls.filter((m) => m.method === "session.prompt");
      expect(prompts.map((p) => p.connection)).toEqual([0, 1]);
      expect(new Set(prompts.map((p) => p.commandId)).size).toBe(1);
      // Shown until the transcript has it.
      expect([...store.outbox.values()].map((o) => o.text)).toEqual(["hello"]);
    } finally {
      c.stop();
      await server.stop();
    }
  });

  it("marks an unread session read once", async () => {
    const server = new TestServer();
    const c = await connect(server);
    try {
      const at = "2026-10-09T12:00:00Z";
      const session = { id: "s1", projectId: "p1", harness: "fake", workspace: { kind: "root", path: "/p" }, status: "idle", title: "t", archived: false, createdAt: at, updatedAt: at };
      c.sessions.set("s1", { ...session, unread: true });
      // Once the agents have loaded, nothing else changes it.
      await waitFor(() => c.harnesses !== undefined);
      let changes = 0;
      c.watch(() => changes++);

      // The row loses its mark before the server answers.
      const done = c.markRead("s1");
      expect(c.sessions.get("s1")?.unread).toBeUndefined();
      expect(changes).toBe(1);
      await done;
      expect(server.calls.filter((m) => m.method === "session.markRead").map((m) => m.params)).toEqual([{ sessionId: "s1" }]);

      // Read already: nothing to send.
      await c.markRead("s1");
      c.sessions.set("s2", { ...session, id: "s2" });
      await c.markRead("s2");
      await c.markRead("missing");
      expect(server.calls.filter((m) => m.method === "session.markRead")).toHaveLength(1);
      expect(changes).toBe(1);

      // An older server doesn't know the method, which is no error.
      server.handlers["session.markRead"] = () => new RPCError("method_not_found", "unknown method");
      c.sessions.set("s3", { ...session, id: "s3", unread: true });
      await c.markRead("s3");
      expect(c.sessions.get("s3")?.unread).toBeUndefined();
      expect(server.calls.filter((m) => m.method === "session.markRead")).toHaveLength(2);
    } finally {
      c.stop();
      await server.stop();
    }
  });

  it("creates a session as a draft and saves its draft", async () => {
    const server = new TestServer();
    const at = "2026-10-09T12:00:00Z";
    const session = { id: "s1", projectId: "p1", harness: "fake", workspace: { kind: "root", path: "/p" }, status: "idle", title: "New session", archived: false, createdAt: at, updatedAt: at };
    const refs = (ids?: string[]) => ids?.map((id) => ({ id, mimeType: "image/png", size: 1 }));
    server.handlers["session.create"] = (p) => ({ ...session, title: p.prompt, draft: p.draft ? { text: p.prompt, images: refs(p.images) } : undefined });
    server.handlers["session.saveDraft"] = (p) => ({ ...session, draft: p.text || p.images ? { text: p.text, images: refs(p.images) } : undefined });
    const c = await connect(server);
    try {
      // Only a draft says so: an older server would send the prompt either way.
      const workspace = { kind: "root" as const };
      await c.createSession({ projectId: "p1", harness: "fake", workspace, prompt: "now" });
      await c.createSession({ projectId: "p1", harness: "fake", workspace, prompt: "now", draft: false });
      await c.createSession({ projectId: "p1", harness: "fake", workspace, prompt: "later", draft: true });
      expect(server.calls.filter((m) => m.method === "session.create").map((m) => m.params.draft)).toEqual([undefined, undefined, true]);
      expect(c.sessions.get("s1")?.draft).toEqual({ text: "later" });

      // The session changes at once, in the index and in its store, before
      // its events come.
      const store = c.sessionStore("s1");
      let changes = 0;
      store.watch(() => changes++);
      const image = { id: "img.png", mimeType: "image/png", size: 1 };
      expect((await c.saveDraft("s1", "again", [image])).draft).toEqual({ text: "again", images: [image] });
      expect(c.sessions.get("s1")?.draft?.text).toBe("again");
      expect(store.session?.draft?.text).toBe("again");
      expect(changes).toBe(1);

      // No text and no images remove it.
      await c.saveDraft("s1", "", []);
      expect(c.sessions.get("s1")?.draft).toBeUndefined();
      expect(store.unsentDraft).toBeUndefined();
      expect(server.calls.filter((m) => m.method === "session.saveDraft").map((m) => m.params)).toEqual([
        { sessionId: "s1", text: "again", images: ["img.png"] },
        { sessionId: "s1", text: "" },
      ]);
    } finally {
      c.stop();
      await server.stop();
    }
  });

  it("delivers a draft saved or sent on a dead socket after reconnecting", async () => {
    const server = new TestServer();
    const at = "2026-10-09T12:00:00Z";
    const session = { id: "s1", projectId: "p1", harness: "fake", workspace: { kind: "root", path: "/p" }, status: "idle", title: "t", archived: false, createdAt: at, updatedAt: at };
    server.handlers["session.saveDraft"] = (p) => ({ ...session, draft: { text: p.text } });
    server.handlers["session.sendDraft"] = (p) => ({ id: "u1", sessionId: p.sessionId, order: 1, kind: "user_message", status: "queued", createdAt: at, updatedAt: at });
    const c = await connect(server);
    try {
      const store = c.sessionStore("s1");
      server.quiet();
      await c.saveDraft("s1", "later");
      const saves = server.calls.filter((m) => m.method === "session.saveDraft");
      expect(saves.map((m) => m.connection)).toEqual([0, 1]);
      expect(store.session?.draft?.text).toBe("later");

      server.quiet();
      await store.sendDraft();
      // Sent again on the new socket, with the same commandId.
      const sends = server.calls.filter((m) => m.method === "session.sendDraft");
      expect(sends.map((m) => m.connection)).toEqual([1, 2]);
      expect(new Set(sends.map((m) => m.commandId)).size).toBe(1);
      // Shown as a prompt on its way until the transcript has it.
      expect([...store.outbox.values()].map((o) => o.text)).toEqual(["later"]);
      expect(store.unsentDraft).toBeUndefined();
    } finally {
      c.stop();
      await server.stop();
    }
  });

  it("fails a prompt once the connection gives up", async () => {
    const server = new TestServer();
    const c = await connect(server);
    try {
      const store = c.sessionStore("s1");
      await server.stop();
      await expect(store.send("hello")).rejects.toBeInstanceOf(RPCError);
      expect(c.state.kind).toBe("failed");
      expect(store.outbox.size).toBe(0);
    } finally {
      c.stop();
    }
  });
});

describe("ServerConnection's server management", () => {
  const at = "2026-10-09T12:00:00Z";
  const update = (status: string, version?: string): ServerTask => ({ id: "k1", kind: "update_server", status, output: "Downloading rad 0.2…", version, startedAt: at });

  it("calls the server's methods", async () => {
    const server = new TestServer();
    server.handlers["server.checkUpdate"] = () => ({ current: "0.1", reason: "rad 0.1 is a development build, which doesn't update itself." });
    server.handlers["server.settings"] = () => ({});
    server.handlers["server.setSettings"] = (p) => p;
    server.handlers["task.list"] = () => ({});
    server.handlers["harness.login"] = (p) => ({ id: "k2", kind: "login", harness: p.harness, status: "running", startedAt: at });
    const c = await connect(server);
    try {
      // Fields an older server leaves out come in as their defaults.
      expect(await c.checkUpdate(true)).toMatchObject({ current: "0.1", available: false, busy: [] });
      expect(server.calls.find((m) => m.method === "server.checkUpdate")?.params).toEqual({ refresh: true });
      expect(await c.settings()).toEqual({});
      const s = { systemAgent: { harness: "fake", model: "m1" } };
      expect(await c.setSettings(s)).toEqual(s);
      // No system agent: the whole settings, without it.
      expect(await c.setSettings({ systemAgent: undefined })).toEqual({});
      expect(await c.tasks()).toEqual([]);
      expect(await c.loginHarness("fake")).toMatchObject({ id: "k2", harness: "fake", output: "" });
      await c.taskInput("k2", "abc");
      await c.cancelTask("k2");
      expect(server.calls.filter((m) => m.method.startsWith("task.")).map((m) => [m.method, m.params])).toEqual([
        ["task.list", {}],
        ["task.input", { id: "k2", text: "abc" }],
        ["task.cancel", { id: "k2" }],
      ]);
    } finally {
      c.stop();
      await server.stop();
    }
  });

  it("commits a session's changes", async () => {
    const server = new TestServer();
    const commitTask = (status: string): ServerTask => ({
      id: "k4",
      kind: "commit",
      sessionId: "s1",
      status,
      steps: [{ id: "commit", title: "Commit", status: status === "running" ? "running" : "succeeded" }],
      output: "",
      url: status === "running" ? undefined : "https://github.com/me/app/pull/12",
      startedAt: at,
    });
    // An older field set: no files, no message, no ahead.
    server.handlers["git.commitPreview"] = () => ({ branch: "ra/x", remote: "origin" });
    server.handlers["git.commit"] = () => ({ ...commitTask("running"), output: undefined });
    server.handlers["task.list"] = (p) => (p?.sessionId === "s1" ? { tasks: [commitTask("running")] } : { tasks: [] });
    let polls = 0;
    server.handlers["task.get"] = () => commitTask(++polls < 2 ? "running" : "succeeded");
    const c = await connect(server);
    try {
      expect(await c.commitPreview("s1")).toEqual({ branch: "ra/x", remote: "origin", files: [], message: "", ahead: 0 });
      const t = await c.commit({ sessionId: "s1", message: "  Fix it\n\n", pullRequest: true });
      expect(t).toMatchObject({ id: "k4", kind: "commit", sessionId: "s1", output: "" });
      await c.commit({ sessionId: "s1", message: "   ", push: true });
      await c.commit({ sessionId: "s1" });
      expect(server.calls.filter((m) => m.method.startsWith("git.")).map((m) => [m.method, m.params])).toEqual([
        ["git.commitPreview", { sessionId: "s1" }],
        ["git.commit", { sessionId: "s1", message: "Fix it", push: true, pullRequest: true }],
        ["git.commit", { sessionId: "s1", push: true }],
        ["git.commit", { sessionId: "s1" }],
      ]);
      // A session's tasks, apart from the server's.
      expect((await c.tasks("s1")).map((k) => k.id)).toEqual(["k4"]);
      expect(await c.tasks()).toEqual([]);
      expect(server.calls.filter((m) => m.method === "task.list").map((m) => m.params)).toEqual([{ sessionId: "s1" }, {}]);

      const p = new ServerTaskProgress(t, c);
      p.interval = 0.01;
      p.start();
      await waitFor(() => p.phase.kind === "done");
      expect(p.task.url).toBe("https://github.com/me/app/pull/12");
      expect(p.task.steps?.[0].status).toBe("succeeded");
      // A commit's end isn't an agent's: the agents aren't looked at again.
      expect(server.calls.filter((m) => m.method === "harness.list" && m.params?.refresh)).toHaveLength(0);
    } finally {
      c.stop();
      await server.stop();
    }
  });

  it("follows an update of rad through its restart", async () => {
    const server = new TestServer();
    let polls = 0;
    server.handlers["server.update"] = () => update("running");
    server.handlers["task.get"] = (p) => (p.id !== "k1" ? new RPCError("not_found", "no such task") : ++polls < 2 ? update("running") : update("succeeded", "0.2"));
    const c = await connect(server);
    try {
      const p = new ServerTaskProgress(await c.updateServer(), c);
      p.interval = 0.01;
      p.start();
      await waitFor(() => p.phase.kind === "restarting");
      // rad restarts as the new version; the connection comes back to it.
      server.version = "0.2";
      server.drop();
      await waitFor(() => p.phase.kind === "done");
      expect(c.info?.version).toBe("0.2");
      expect(server.connectionCount).toBe(2);
      p.stop();
    } finally {
      c.stop();
      await server.stop();
    }
  });

  it("looks at the agents again once one signed in", async () => {
    const server = new TestServer();
    let signedIn = false;
    server.handlers["harness.list"] = () => ({ harnesses: [{ id: "fake", name: "Fake", protocol: "fake", installed: true, authOk: signedIn, caps: {} }] });
    server.handlers["harness.login"] = () => ({ id: "k3", kind: "login", harness: "fake", status: "running", output: "", startedAt: at });
    server.handlers["task.get"] = () => ({ id: "k3", kind: "login", harness: "fake", status: signedIn ? "succeeded" : "running", output: "", startedAt: at });
    const c = await connect(server);
    try {
      await waitFor(() => c.harnesses?.[0]?.authOk === false);
      const p = new ServerTaskProgress(await c.loginHarness("fake"), c);
      p.interval = 0.01;
      p.start();
      signedIn = true;
      await waitFor(() => p.phase.kind === "done");
      await waitFor(() => c.harnesses?.[0]?.authOk === true);
      expect(server.calls.filter((m) => m.method === "harness.list").map((m) => m.params)).toEqual([{ refresh: false }, { refresh: true }]);
    } finally {
      c.stop();
      await server.stop();
    }
  });
});

describe("RPCClient", () => {
  it("pings when a call goes unanswered", async () => {
    const server = new TestServer();
    const url = await server.start();
    const client = new RPCClient("tok");
    client.pingTimeout = 0.3;
    let closed = 0;
    client.onMessage = (m) => m.type === "closed" && closed++;
    try {
      await client.connect([url]);
      // The server answers the ping, so it's only slow: the call times out.
      await expect(client.call("slow", undefined, 0.05)).rejects.toMatchObject({ code: "timeout" });
      expect(client.isConnected).toBe(true);

      // It doesn't: the socket is dropped, and the call fails as disconnected.
      server.quiet();
      await expect(client.call("slow", undefined, 0.05)).rejects.toMatchObject({ code: "disconnected" });
      expect(client.isConnected).toBe(false);
      expect(closed).toBe(1);
    } finally {
      client.disconnect();
      await server.stop();
    }
  });
});
