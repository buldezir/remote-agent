import type {
  Event,
  FSListing,
  HarnessInfo,
  ImageRef,
  Project,
  ServerInfo,
  ServerSettings,
  ServerTask,
  Session,
  UpdateInfo,
} from "../protocol/models";
import { parseDate } from "../protocol/models";
import { RPCClient, RPCError, type ServerMessage } from "../protocol/rpc";
import { Drafts } from "./drafts";
import { imageCache } from "./imageCache";
import { Observable } from "./observable";
import type { SavedServer } from "./serverStore";
import type { TaskSource } from "./serverTask";
import { SessionStore, type SessionHost } from "./sessionStore";
import { TerminalSession } from "./terminalSession";

type SessionChange = [old: Session, next: Session];

export type ConnectionState =
  | { kind: "idle" }
  | { kind: "connecting" }
  | { kind: "connected" }
  | { kind: "failed"; message: string };

export interface NewSession {
  projectId: string;
  harness: string;
  model?: string;
  effort?: string;
  mode?: string;
  workspace: { kind: "root" | "worktree"; branch?: string; baseRef?: string };
  prompt: string;
  /** Ids of uploaded images that go with the prompt. */
  images?: string[];
  /** Keep the prompt and images as the session's draft rather than send
   *  them. Only when the server has drafts (`info.drafts`): an older one
   *  would send them. */
  draft?: boolean;
}

/** `session.fork`'s request: what is left out is the session's, or with
 *  another harness, that harness's defaults. */
export interface ForkParams {
  sessionId: string;
  /** The turn to continue after; absent: the last one that ended. */
  turn?: number;
  harness?: string;
  model?: string;
  effort?: string;
  mode?: string;
  workspace?: { kind: "root" | "worktree"; branch?: string };
  prompt?: string;
}

/** A live connection to one server: keeps the index (projects and sessions)
 *  in sync, owns per-session stores, and reconnects with backoff. It stays
 *  `connecting` while it tries, and gives up after `maxAttempts` failures in
 *  a row: `failed` until `start()` or `retry()`. A socket that has been
 *  silent for `heartbeat` is pinged, and dropped if it doesn't answer, so
 *  one that died while the computer slept doesn't pass for connected. */
export class ServerConnection extends Observable implements SessionHost, TaskSource {
  /** Failed attempts in a row before the connection gives up. */
  static readonly maxAttempts = 5;
  state: ConnectionState = { kind: "idle" };
  info: ServerInfo | undefined;
  projects = new Map<string, Project>();
  sessions = new Map<string, Session>();
  /** The agents installed on the server; undefined until first loaded. */
  harnesses: HarnessInfo[] | undefined;
  indexSynced = false;
  readonly drafts: Drafts;

  private client: RPCClient;
  private indexSeq = 0;
  private indexLive = false;
  /** As SessionStore's: index subscriptions not yet synchronized. */
  private indexOutstanding = 0;
  private indexReplay: Event[] = [];
  private sessionStores = new Map<string, SessionStore>();
  private wanted = false;
  /** Bumped to stop the current run loop. */
  private run = 0;
  private wake: (() => void) | undefined;
  /** The wait after the first failed attempt, in seconds, doubling after each one. */
  firstRetryDelay = 1;
  /** Seconds a socket may stay silent before it's pinged. */
  heartbeat = 15;
  /** Called with a session in the index that changed, before and after:
   *  live, or while this device was away, in the replay after reconnecting.
   *  Not for the sessions the first sync brings. */
  onSessionChange: ((old: Session, next: Session) => void) | undefined;

  constructor(
    public server: SavedServer,
    token: string,
    private onURLWorked: (url: string) => void,
  ) {
    super();
    this.client = new RPCClient(token);
    this.client.onMessage = (m) => this.handle(m);
    this.drafts = new Drafts(server.id);
  }

  get isConnected(): boolean {
    return this.state.kind === "connected";
  }

  /** Seconds to wait for a ping's answer. */
  get pingTimeout(): number {
    return this.client.pingTimeout;
  }

  set pingTimeout(seconds: number) {
    this.client.pingTimeout = seconds;
  }

  /** The session's shell on the server, for one view to show. */
  terminal(sessionID: string): TerminalSession {
    return new TerminalSession(
      { connected: () => this.isConnected, open: (query) => this.client.openSocket("/v1/terminal?" + query, "rad.terminal.v1") },
      sessionID,
    );
  }

  // Lifecycle

  start() {
    this.wanted = true;
    if (this.looping) return;
    this.looping = true;
    void this.runLoop(++this.run);
  }

  stop() {
    this.wanted = false;
    this.pause();
  }

  /** Stops for good, revoking this device on the server first if reachable. */
  unpairAndStop() {
    this.wanted = false;
    this.run++;
    this.looping = false;
    this.wake?.();
    this.setState({ kind: "idle" });
    void (async () => {
      try {
        if (!this.client.isConnected) await this.client.connect(this.server.urls);
        await this.client.call("device.unpair", undefined, 5);
      } catch {
        // best effort
      }
      this.client.disconnect();
    })();
  }

  pause() {
    this.run++;
    this.looping = false;
    this.wake?.();
    this.client.disconnect();
    this.setState({ kind: "idle" });
  }

  /** Reconnect now (after a failure, or when the page comes back). */
  retry() {
    this.wanted = true;
    this.looping = true;
    this.wake?.();
    void this.runLoop(++this.run);
  }

  /** Pings the socket now rather than at the next heartbeat, as when the
   *  page comes back: the computer may have slept. */
  check() {
    if (this.isConnected) void this.client.ping();
  }

  /** A run loop is going. */
  private looping = false;

  private async runLoop(run: number) {
    let delay = this.firstRetryDelay;
    let failures = 0;
    try {
      while (run === this.run && this.wanted) {
        if (this.client.isConnected) {
          await this.sleep(Math.min(2, this.heartbeat));
          // After a sleep the silence is at least as long, so the first
          // check after waking pings.
          if (run === this.run && this.client.silence >= this.heartbeat) await this.client.ping();
          continue;
        }
        this.setState({ kind: "connecting" });
        try {
          const info = await this.client.connect(this.server.urls);
          if (run !== this.run) return;
          this.info = info;
          if (this.client.connectedURL) this.onURLWorked(this.client.connectedURL);
          this.setState({ kind: "connected" });
          delay = this.firstRetryDelay;
          failures = 0;
          await this.resubscribeAll();
          void this.refreshHarnesses();
        } catch (e) {
          if (run !== this.run) return;
          failures++;
          // Trying again won't bring back a revoked token.
          const unauthorized = e instanceof RPCError && e.code === "unauthorized";
          if (unauthorized || failures >= ServerConnection.maxAttempts) {
            this.setState({ kind: "failed", message: (e as Error).message });
            return;
          }
          await this.sleep(delay);
          delay *= 2;
        }
      }
    } finally {
      if (run === this.run) this.looping = false;
    }
  }

  /** Waits, or until `retry` or `pause` cuts it short. */
  private sleep(seconds: number): Promise<void> {
    return new Promise((resolve) => {
      const t = setTimeout(done, seconds * 1000);
      const self = this;
      function done() {
        clearTimeout(t);
        if (self.wake === done) self.wake = undefined;
        resolve();
      }
      this.wake = done;
    });
  }

  private setState(s: ConnectionState) {
    this.state = s;
    this.changed();
  }

  private async resubscribeAll() {
    await this.subscribeIndex();
    for (const store of this.sessionStores.values()) {
      if (store.isActive) await this.subscribe(store);
    }
  }

  // Messages

  private handle(msg: ServerMessage) {
    switch (msg.type) {
      case "events":
        if (msg.stream === "index") this.applyIndex(msg.events);
        else this.storeForStream(msg.stream)?.apply(msg.events);
        break;
      case "synchronized":
        if (msg.stream === "index") {
          // A reset drops what was cached, but the replay is still compared with it.
          const before = new Map(this.sessions);
          if (msg.reset) {
            this.projects.clear();
            this.sessions.clear();
          }
          const buffered = this.indexReplay;
          this.indexReplay = [];
          const changes: SessionChange[] = [];
          for (const ev of buffered) {
            const c = this.upsertIndex(ev, before);
            if (c) changes.push(c);
          }
          this.indexSeq = msg.seq;
          this.indexOutstanding = Math.max(0, this.indexOutstanding - 1);
          this.indexLive = this.indexOutstanding === 0;
          this.indexSynced = true;
          for (const [id, s] of this.sessions) {
            const store = this.sessionStores.get(id);
            if (store && !store.session) store.setSession(s);
          }
          this.drafts.keep((id) => this.sessions.has(id));
          this.changed();
          this.report(changes);
        } else {
          this.storeForStream(msg.stream)?.synchronized(msg.seq, msg.reset);
        }
        break;
      case "resync":
        void this.resubscribe(msg.stream);
        break;
      case "closed":
        this.indexLive = false;
        this.indexOutstanding = 0;
        for (const s of this.sessionStores.values()) s.closed();
        // Not after pause(): the socket closed because we closed it.
        if (this.wanted && this.looping) {
          this.setState({ kind: "connecting" });
          this.wake?.();
        }
        break;
    }
  }

  private storeForStream(stream: string): SessionStore | undefined {
    if (!stream.startsWith("session:")) return undefined;
    return this.sessionStores.get(stream.slice("session:".length));
  }

  private applyIndex(events: Event[]) {
    if (!this.indexLive) {
      this.indexReplay.push(...events);
      return;
    }
    const changes: SessionChange[] = [];
    for (const ev of events) {
      if (ev.seq <= this.indexSeq) continue;
      if (ev.seq !== this.indexSeq + 1) {
        this.indexLive = false;
        void this.subscribeIndex();
        break;
      }
      this.indexSeq = ev.seq;
      const c = this.upsertIndex(ev);
      if (c) changes.push(c);
    }
    this.changed();
    this.report(changes);
  }

  /** Applies an index event, and returns the session it changed. A session
   *  is compared with the one cached, else with the one in `before`. */
  private upsertIndex(ev: Event, before?: Map<string, Session>): SessionChange | undefined {
    switch (ev.type) {
      case "project.upserted":
        if (ev.project) this.projects.set(ev.project.id, ev.project);
        break;
      case "project.removed":
        if (ev.id) this.projects.delete(ev.id);
        break;
      case "session.upserted": {
        const s = ev.session;
        if (!s) break;
        const old = this.sessions.get(s.id) ?? before?.get(s.id);
        this.sessions.set(s.id, s);
        if (old && JSON.stringify(old) !== JSON.stringify(s)) return [old, s];
        break;
      }
      case "session.removed":
        if (ev.id) {
          this.sessions.delete(ev.id);
          this.drafts.set(ev.id, "");
        }
        break;
    }
    return undefined;
  }

  /** Called once the batch they came in is applied, so the index is whole. */
  private report(changes: SessionChange[]) {
    for (const [old, next] of changes) this.onSessionChange?.(old, next);
  }

  private async subscribeIndex() {
    this.indexOutstanding++;
    this.indexLive = false;
    this.indexReplay = [];
    await this.client.call("subscribe", { stream: "index", afterSeq: this.indexSeq }).catch(() => {
      this.indexOutstanding = Math.max(0, this.indexOutstanding - 1);
    });
  }

  async subscribe(store: SessionStore) {
    store.beginReplay();
    await this.client
      .call("subscribe", { stream: "session:" + store.sessionID, afterSeq: store.lastSeq })
      .catch(() => store.subscriptionFailed());
  }

  private async resubscribe(stream: string) {
    if (stream === "index") await this.subscribeIndex();
    else {
      const s = this.storeForStream(stream);
      if (s) await this.subscribe(s);
    }
  }

  // Session stores

  /** Returns the store for a session, subscribing while it's in use. */
  sessionStore(id: string): SessionStore {
    let s = this.sessionStores.get(id);
    if (!s) {
      s = new SessionStore(id, this);
      s.session = this.sessions.get(id);
      this.sessionStores.set(id, s);
    }
    return s;
  }

  activate(store: SessionStore) {
    if (this.state.kind !== "connected") return;
    void this.subscribe(store);
  }

  deactivate(store: SessionStore) {
    this.client.call("unsubscribe", { stream: "session:" + store.sessionID }).catch(() => {});
  }

  // API

  call<R>(method: string, params?: unknown, timeout?: number): Promise<R> {
    return this.client.call<R>(method, params, timeout);
  }

  /** Calls a command that carries a commandId until it gets through: when
   *  the socket drops or the server doesn't answer, it waits for the
   *  connection to come back and sends it again. rad applies a commandId
   *  once, so a repeat of one that got there returns the first result. It
   *  throws once the connection gives up or stops. */
  async deliver<R>(method: string, params: unknown, timeout = 30): Promise<R> {
    for (let attempts = 1; ; attempts++) {
      await this.waitUntilConnected();
      try {
        return await this.client.call<R>(method, params, timeout);
      } catch (e) {
        const lost = e instanceof RPCError && (e.code === "disconnected" || e.code === "timeout");
        if (!lost || attempts >= ServerConnection.maxAttempts) throw e;
      }
    }
  }

  /** Returns once connected, waiting out a reconnect. It throws once the
   *  connection is stopped or gives up. */
  private async waitUntilConnected() {
    for (;;) {
      if (!this.wanted || this.state.kind === "failed") throw RPCError.disconnected();
      // The socket goes before `state` hears that it closed.
      if (this.isConnected && this.client.isConnected) return;
      await new Promise((r) => setTimeout(r, 100));
    }
  }

  async refreshHarnesses(force = false) {
    try {
      const r = await this.client.call<{ harnesses?: HarnessInfo[] }>("harness.list", { refresh: force }, 45);
      this.harnesses = r.harnesses ?? [];
      this.changed();
    } catch {
      // keep what we had
    }
  }

  harness(id: string): HarnessInfo | undefined {
    return this.harnesses?.find((h) => h.id === id);
  }

  /** Uploads an image to attach to a prompt. */
  async uploadImage(data: Blob): Promise<ImageRef> {
    const ref = await this.client.uploadImage(data);
    imageCache.store(ref.id, data);
    return ref;
  }

  /** An object URL for an image, from the cache or the server. */
  imageURL(id: string): Promise<string> {
    return imageCache.url(id, () => this.client.imageBlob(id));
  }

  listDirectory(path: string | undefined): Promise<FSListing> {
    return this.client.call("fs.list", { path: path ?? "" });
  }

  async addProject(path: string): Promise<Project> {
    const p = await this.client.call<Project>("project.add", { path });
    this.projects.set(p.id, p);
    this.changed();
    return p;
  }

  async removeProject(id: string) {
    await this.client.call("project.remove", { id });
  }

  async branches(projectId: string): Promise<{ branches: string[]; current: string }> {
    const r = await this.client.call<{ branches?: string[]; current?: string }>("git.branches", { projectId });
    return { branches: r.branches ?? [], current: r.current ?? "" };
  }

  async createSession(p: NewSession): Promise<Session> {
    const s = await this.deliver<Session>("session.create", { commandId: crypto.randomUUID(), ...p, draft: p.draft || undefined }, 90);
    this.sessions.set(s.id, s);
    this.changed();
    return s;
  }

  /** A new session that continues another after one of its turns, as
   *  `createSession` makes one. */
  async fork(p: ForkParams): Promise<Session> {
    const s = await this.deliver<Session>("session.fork", { commandId: crypto.randomUUID(), ...p }, 90);
    this.sessions.set(s.id, s);
    this.changed();
    return s;
  }

  /** Keeps a prompt in the session, unsent, for any device to send later,
   *  in place of the draft it had; no text and no images remove it. The
   *  session changes here at once, as the server returns it. Saving it
   *  twice does no harm, so it is sent again after a drop, as commands are. */
  async saveDraft(sessionId: string, text: string, images: ImageRef[] = []): Promise<Session> {
    const s = await this.deliver<Session>("session.saveDraft", {
      sessionId,
      text,
      images: images.length ? images.map((i) => i.id) : undefined,
    });
    this.sessions.set(s.id, s);
    this.sessionStores.get(s.id)?.setSession(s);
    this.changed();
    return s;
  }

  async archive(sessionId: string, removeWorktree: boolean) {
    await this.client.call("session.archive", { sessionId, removeWorktree });
  }

  /** Marks the session read on the server, for every device, when the index
   *  has it unread. Its row loses the mark at once. An older server doesn't
   *  know the method; an index that still has it unread calls again. */
  async markRead(sessionId: string) {
    const s = this.sessions.get(sessionId);
    if (!s?.unread) return;
    // As the server sends it once read: without the field.
    const read = { ...s };
    delete read.unread;
    this.sessions.set(sessionId, read);
    this.changed();
    try {
      await this.client.call("session.markRead", { sessionId });
    } catch {
      // method_not_found, or the connection dropped
    }
  }

  /** Deletes a session and its transcript for good; the index then drops it. */
  async deleteSession(sessionId: string, removeWorktree: boolean) {
    await this.client.call("session.delete", { sessionId, removeWorktree });
  }

  // Managing the server

  settings(): Promise<ServerSettings> {
    return this.client.call("server.settings");
  }

  /** Replaces the server's settings (leave `systemAgent` out to unset it);
   *  returns them as the server keeps them. */
  setSettings(s: ServerSettings): Promise<ServerSettings> {
    return this.client.call("server.setSettings", s);
  }

  /** Whether rad has an update; `refresh` asks GitHub again rather than use
   *  the answer of the last few minutes. */
  async checkUpdate(refresh = false): Promise<UpdateInfo> {
    const r = await this.client.call<UpdateInfo>("server.checkUpdate", { refresh }, 45);
    return { ...r, available: !!r.available, busy: r.busy ?? [] };
  }

  /** Installs rad's latest release, which restarts it. It looks the release up again first. */
  async updateServer(): Promise<ServerTask> {
    return task(await this.client.call<ServerTask>("server.update", undefined, 45));
  }

  /** Runs the agent's own updater. */
  async updateHarness(id: string): Promise<ServerTask> {
    return task(await this.client.call<ServerTask>("harness.update", { harness: id }));
  }

  /** Runs the agent's sign-in command. */
  async loginHarness(id: string): Promise<ServerTask> {
    return task(await this.client.call<ServerTask>("harness.login", { harness: id }));
  }

  async task(id: string): Promise<ServerTask> {
    return task(await this.client.call<ServerTask>("task.get", { id }));
  }

  /** The tasks that run on the server, and those that ended lately, oldest first. */
  async tasks(): Promise<ServerTask[]> {
    const r = await this.client.call<{ tasks?: ServerTask[] }>("task.list");
    return (r.tasks ?? []).map(task);
  }

  /** Types a line into the task's command, such as a code to paste. */
  async taskInput(id: string, text: string) {
    await this.client.call("task.input", { id, text });
  }

  async cancelTask(id: string) {
    await this.client.call("task.cancel", { id });
  }

  /** The version of rad last connected to. */
  get serverVersion(): string | undefined {
    return this.info?.version;
  }

  /** An agent's update or sign-in ended: its version and sign-in state may have changed. */
  async taskEnded() {
    await this.refreshHarnesses(true);
  }

  /** rad may take longer to restart than the connection keeps trying. */
  reviveConnection() {
    if (this.state.kind === "failed") this.retry();
  }

  // Derived

  get activeSessions(): Session[] {
    return [...this.sessions.values()].filter((s) => !s.archived).sort(byUpdated);
  }

  get archivedSessions(): Session[] {
    return [...this.sessions.values()].filter((s) => s.archived).sort(byUpdated);
  }

  get sortedProjects(): Project[] {
    return [...this.projects.values()].sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: "base" }));
  }
}

/** A task as the server sent it, with `output` there even if it wasn't. */
function task(t: ServerTask): ServerTask {
  return { ...t, output: t.output ?? "" };
}

function byUpdated(a: Session, b: Session): number {
  return parseDate(b.updatedAt) - parseDate(a.updatedAt);
}

export { RPCError };
