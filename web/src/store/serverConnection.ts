import type { Event, FSListing, HarnessInfo, ImageRef, Project, ServerInfo, Session } from "../protocol/models";
import { parseDate } from "../protocol/models";
import { RPCClient, RPCError, type ServerMessage } from "../protocol/rpc";
import { imageCache } from "./imageCache";
import { Observable } from "./observable";
import type { SavedServer } from "./serverStore";
import { SessionStore, type SessionHost } from "./sessionStore";

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
}

/** A live connection to one server: keeps the index (projects and sessions)
 *  in sync, owns per-session stores, and reconnects with backoff. */
export class ServerConnection extends Observable implements SessionHost {
  state: ConnectionState = { kind: "idle" };
  info: ServerInfo | undefined;
  projects = new Map<string, Project>();
  sessions = new Map<string, Session>();
  /** The agents installed on the server; undefined until first loaded. */
  harnesses: HarnessInfo[] | undefined;
  indexSynced = false;

  private client: RPCClient;
  private indexSeq = 0;
  private indexLive = false;
  private indexReplay: Event[] = [];
  private sessionStores = new Map<string, SessionStore>();
  private wanted = false;
  /** Bumped to stop the current run loop. */
  private run = 0;
  private wake: (() => void) | undefined;

  constructor(
    public server: SavedServer,
    token: string,
    private onURLWorked: (url: string) => void,
  ) {
    super();
    this.client = new RPCClient(token);
    this.client.onMessage = (m) => this.handle(m);
  }

  get isConnected(): boolean {
    return this.state.kind === "connected";
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

  /** A run loop is going. */
  private looping = false;

  private async runLoop(run: number) {
    let delay = 1;
    try {
      while (run === this.run && this.wanted) {
        if (this.client.isConnected) {
          await this.sleep(2);
          continue;
        }
        this.setState({ kind: "connecting" });
        try {
          const info = await this.client.connect(this.server.urls);
          if (run !== this.run) return;
          this.info = info;
          if (this.client.connectedURL) this.onURLWorked(this.client.connectedURL);
          this.setState({ kind: "connected" });
          delay = 1;
          await this.resubscribeAll();
          void this.refreshHarnesses();
        } catch (e) {
          if (run !== this.run) return;
          this.setState({ kind: "failed", message: (e as Error).message });
          await this.sleep(delay);
          delay = Math.min(delay * 2, 30);
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
          if (msg.reset) {
            this.projects.clear();
            this.sessions.clear();
          }
          const buffered = this.indexReplay;
          this.indexReplay = [];
          for (const ev of buffered) this.upsertIndex(ev);
          this.indexSeq = msg.seq;
          this.indexLive = true;
          this.indexSynced = true;
          for (const [id, s] of this.sessions) {
            const store = this.sessionStores.get(id);
            if (store && !store.session) store.setSession(s);
          }
          this.changed();
        } else {
          this.storeForStream(msg.stream)?.synchronized(msg.seq, msg.reset);
        }
        break;
      case "resync":
        void this.resubscribe(msg.stream);
        break;
      case "closed":
        this.indexLive = false;
        for (const s of this.sessionStores.values()) s.live = false;
        if (this.wanted) {
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
    for (const ev of events) {
      if (ev.seq <= this.indexSeq) continue;
      if (ev.seq !== this.indexSeq + 1) {
        this.indexLive = false;
        void this.subscribeIndex();
        break;
      }
      this.indexSeq = ev.seq;
      this.upsertIndex(ev);
    }
    this.changed();
  }

  private upsertIndex(ev: Event) {
    switch (ev.type) {
      case "project.upserted":
        if (ev.project) this.projects.set(ev.project.id, ev.project);
        break;
      case "project.removed":
        if (ev.id) this.projects.delete(ev.id);
        break;
      case "session.upserted":
        if (ev.session) this.sessions.set(ev.session.id, ev.session);
        break;
      case "session.removed":
        if (ev.id) this.sessions.delete(ev.id);
        break;
    }
  }

  private async subscribeIndex() {
    this.indexLive = false;
    this.indexReplay = [];
    await this.client.call("subscribe", { stream: "index", afterSeq: this.indexSeq }).catch(() => {});
  }

  async subscribe(store: SessionStore) {
    store.beginReplay();
    await this.client.call("subscribe", { stream: "session:" + store.sessionID, afterSeq: store.lastSeq }).catch(() => {});
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
    const s = await this.client.call<Session>("session.create", { commandId: crypto.randomUUID(), ...p }, 90);
    this.sessions.set(s.id, s);
    this.changed();
    return s;
  }

  async archive(sessionId: string, removeWorktree: boolean) {
    await this.client.call("session.archive", { sessionId, removeWorktree });
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

function byUpdated(a: Session, b: Session): number {
  return parseDate(b.updatedAt) - parseDate(a.updatedAt);
}

export { RPCError };
