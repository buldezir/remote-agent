import type { Event, ImageRef, ServerInfo } from "./models";

export class RPCError extends Error {
  constructor(
    public code: string,
    message: string,
  ) {
    super(message);
  }

  static disconnected = () => new RPCError("disconnected", "Not connected to the server");
  static timeout = () => new RPCError("timeout", "The server did not respond in time");
  static unauthorized = () => new RPCError("unauthorized", "This device is no longer authorized; pair again.");
}

/** Server-initiated messages, given to `RPCClient.onMessage`. */
export type ServerMessage =
  | { type: "events"; stream: string; events: Event[] }
  | { type: "synchronized"; stream: string; seq: number; reset: boolean }
  | { type: "resync"; stream: string }
  | { type: "closed"; reason?: string };

/** Close code rad uses for a revoked device. */
const revokedCode = 4001;

interface Pending {
  resolve: (v: unknown) => void;
  reject: (e: Error) => void;
  timer: ReturnType<typeof setTimeout>;
}

/** One WebSocket connection to rad, speaking the JSON-RPC-shaped protocol.
 *  Reconnection is the owner's job: when the socket drops, `onMessage` gets
 *  `closed` and pending calls fail with `RPCError.disconnected`. */
export class RPCClient {
  onMessage: (m: ServerMessage) => void = () => {};
  connectedURL: string | null = null;

  private ws: WebSocket | null = null;
  private pending = new Map<number, Pending>();
  private nextID = 0;

  constructor(private token: string) {}

  get isConnected(): boolean {
    return this.ws !== null;
  }

  /** Connects to the first base URL that accepts our token. Returns server info. */
  async connect(baseURLs: string[]): Promise<ServerInfo> {
    let lastError: Error = RPCError.disconnected();
    for (const base of baseURLs) {
      try {
        return await this.open(base);
      } catch (e) {
        lastError = e as Error;
        if (lastError instanceof RPCError && lastError.code === "unauthorized") break;
      }
    }
    throw lastError;
  }

  private async open(base: string): Promise<ServerInfo> {
    this.close(undefined, false);
    const url = base.replace(/^http/, "ws") + "/v1/ws";
    // Browsers can't set the Authorization header on a WebSocket, so the
    // token goes as a subprotocol (protocol/PROTOCOL.md, Transport).
    const ws = new WebSocket(url, ["rad.v1", "rad.token." + this.token]);
    this.ws = ws;
    const opened = new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => reject(RPCError.timeout()), 10_000);
      ws.onopen = () => {
        clearTimeout(timer);
        resolve();
      };
      ws.onclose = (e) => {
        clearTimeout(timer);
        reject(e.code === revokedCode ? RPCError.unauthorized() : new RPCError("unreachable", `Could not connect to ${base}`));
      };
    });
    try {
      await opened;
    } catch (e) {
      if (this.ws === ws) this.ws = null;
      // A browser hides why an upgrade failed. Ask over HTTP whether it was the token.
      if (e instanceof RPCError && e.code === "unreachable" && (await this.tokenRejected(base))) {
        throw RPCError.unauthorized();
      }
      throw e;
    }
    if (this.ws !== ws) throw RPCError.disconnected();
    ws.onmessage = (e) => {
      if (this.ws === ws && typeof e.data === "string") this.dispatch(e.data);
    };
    ws.onclose = (e) => {
      if (this.ws !== ws) return;
      this.close(e.code === revokedCode ? RPCError.unauthorized().message : e.reason || undefined, true);
    };
    try {
      const info = await this.call<ServerInfo>("server.info", undefined, 8);
      this.connectedURL = base;
      return info;
    } catch (e) {
      this.close(undefined, false);
      throw e;
    }
  }

  /** Whether rad turns our token away, rather than being unreachable: an
   *  image that doesn't exist is 404 to a paired device and 401 otherwise. */
  private async tokenRejected(base: string): Promise<boolean> {
    try {
      const r = await fetch(base + "/v1/images/check", { headers: { Authorization: "Bearer " + this.token } });
      return r.status === 401;
    } catch {
      return false;
    }
  }

  disconnect() {
    this.close("closed by client", true);
  }

  private close(reason: string | undefined, notify: boolean) {
    const ws = this.ws;
    if (!ws) return;
    this.ws = null;
    this.connectedURL = null;
    ws.onopen = ws.onmessage = ws.onclose = ws.onerror = null;
    try {
      ws.close(1000);
    } catch {
      // already closing
    }
    const waiting = [...this.pending.values()];
    this.pending.clear();
    for (const p of waiting) {
      clearTimeout(p.timer);
      p.reject(RPCError.disconnected());
    }
    if (notify) this.onMessage({ type: "closed", reason });
  }

  private dispatch(data: string) {
    let msg: { id?: number; method?: string; params?: any; result?: unknown; error?: { code: string; message: string } };
    try {
      msg = JSON.parse(data);
    } catch {
      return;
    }
    if (msg.id !== undefined && msg.method === undefined) {
      const p = this.pending.get(msg.id);
      if (!p) return;
      this.pending.delete(msg.id);
      clearTimeout(p.timer);
      if (msg.error) p.reject(new RPCError(msg.error.code, msg.error.message));
      else p.resolve(msg.result ?? {});
      return;
    }
    const params = msg.params ?? {};
    switch (msg.method) {
      case "events":
        this.onMessage({ type: "events", stream: params.stream, events: params.events ?? [] });
        break;
      case "synchronized":
        this.onMessage({ type: "synchronized", stream: params.stream, seq: params.seq, reset: params.reset ?? false });
        break;
      case "resync":
        this.onMessage({ type: "resync", stream: params.stream });
        break;
    }
  }

  call<R>(method: string, params?: unknown, timeout = 30): Promise<R> {
    const ws = this.ws;
    if (!ws || ws.readyState !== WebSocket.OPEN) return Promise.reject(RPCError.disconnected());
    const id = ++this.nextID;
    return new Promise<R>((resolve, reject) => {
      const timer = setTimeout(() => {
        if (this.pending.delete(id)) reject(RPCError.timeout());
      }, timeout * 1000);
      this.pending.set(id, { resolve: resolve as (v: unknown) => void, reject, timer });
      ws.send(JSON.stringify({ id, method, params: params ?? {} }));
    });
  }

  // HTTP, beside the socket.

  /** Uploads an image to attach to a prompt (`POST /v1/images`). */
  async uploadImage(data: Blob): Promise<ImageRef> {
    const r = await this.http("/v1/images", { method: "POST", body: data, headers: { "Content-Type": "application/octet-stream" } });
    return r.json();
  }

  /** An image's bytes (`GET /v1/images/<id>`). The browser caches them:
   *  rad marks them immutable. */
  async imageBlob(id: string): Promise<Blob> {
    const r = await this.http("/v1/images/" + encodeURIComponent(id), {});
    return r.blob();
  }

  /** A request to the server the socket is connected to, with our token. */
  private async http(path: string, init: RequestInit): Promise<Response> {
    const base = this.connectedURL;
    if (!base) throw RPCError.disconnected();
    const headers = new Headers(init.headers);
    headers.set("Authorization", "Bearer " + this.token);
    let r: Response;
    try {
      r = await fetch(base + path, { ...init, headers, signal: AbortSignal.timeout(60_000) });
    } catch (e) {
      throw new RPCError("http", (e as Error).message);
    }
    if (r.status !== 200) {
      const body = await r.json().catch(() => null);
      throw body?.code ? new RPCError(body.code, body.message ?? body.code) : new RPCError("http", `The server answered ${r.status}`);
    }
    return r;
  }
}
