import type { Diff, Event, FileStat, ImageRef, Item, Session, Turn } from "../protocol/models";
import { RPCError } from "../protocol/rpc";
import { Observable } from "./observable";

/** What a session store needs from its connection. */
export interface SessionHost {
  call<R>(method: string, params?: unknown, timeout?: number): Promise<R>;
  activate(store: SessionStore): void;
  deactivate(store: SessionStore): void;
  subscribe(store: SessionStore): Promise<void>;
  readonly isConnected: boolean;
}

/** A prompt on its way to the server. */
export interface Outgoing {
  text: string;
  images: ImageRef[];
}

/** The live state of one session: its turns and transcript items. */
export class SessionStore extends Observable {
  session: Session | undefined;
  turns = new Map<string, Turn>();
  items = new Map<string, Item>();
  /** Top-level transcript items in order (children of tool calls are excluded). */
  transcript: Item[] = [];
  synced = false;
  /** Optimistic user messages not yet confirmed by the server, keyed by commandId. */
  outbox = new Map<string, Outgoing>();

  lastSeq = 0;
  live = false;
  // Views showing the session; two can show the same one.
  private viewers = 0;
  private children = new Map<string, Item[]>();
  /** Replayed events are buffered until `synchronized`, then applied in one
   *  step (after dropping cached state if the server says reset). */
  private replay: Event[] = [];

  constructor(
    readonly sessionID: string,
    private host: SessionHost | undefined,
  ) {
    super();
  }

  get isActive(): boolean {
    return this.viewers > 0;
  }

  /** Start receiving updates (when a view appears). Updates stop when every
   *  activate() has had its deactivate(). */
  activate() {
    this.viewers++;
    if (this.viewers === 1) this.host?.activate(this);
  }

  deactivate() {
    if (this.viewers === 0) return;
    this.viewers--;
    if (this.viewers > 0) return;
    this.live = false;
    this.host?.deactivate(this);
  }

  // Sync

  beginReplay() {
    this.live = false;
    this.replay = [];
  }

  synchronized(seq: number, reset: boolean) {
    if (reset) {
      this.turns.clear();
      this.items.clear();
    }
    const buffered = this.replay;
    this.replay = [];
    for (const ev of buffered) this.upsert(ev);
    this.lastSeq = seq;
    this.live = true;
    this.synced = true;
    this.rebuild();
  }

  apply(events: Event[]) {
    if (!this.live) {
      this.replay.push(...events);
      return;
    }
    for (const ev of events) {
      if (ev.seq <= this.lastSeq) continue;
      if (ev.seq !== this.lastSeq + 1) {
        // Gap: we missed something. Resubscribe from what we have.
        this.live = false;
        void this.host?.subscribe(this);
        break;
      }
      this.lastSeq = ev.seq;
      this.upsert(ev);
    }
    this.rebuild();
  }

  /** The connection's copy of the session, from the index. */
  setSession(s: Session) {
    this.session = s;
    this.changed();
  }

  private upsert(ev: Event) {
    switch (ev.type) {
      case "session.upserted":
        if (ev.session) this.session = ev.session;
        break;
      case "turn.upserted":
        if (ev.turn) this.turns.set(ev.turn.id, ev.turn);
        break;
      case "item.upserted":
        if (ev.item) this.items.set(ev.item.id, ev.item);
        break;
    }
  }

  private rebuild() {
    const top: Item[] = [];
    const kids = new Map<string, Item[]>();
    for (const it of this.items.values()) {
      if (it.parentItemId && this.items.has(it.parentItemId)) {
        const list = kids.get(it.parentItemId) ?? [];
        list.push(it);
        kids.set(it.parentItemId, list);
      } else {
        top.push(it);
      }
    }
    const byOrder = (a: Item, b: Item) => a.order - b.order;
    top.sort(byOrder);
    for (const list of kids.values()) list.sort(byOrder);
    // Drop optimistic messages the server has confirmed.
    const confirmed = new Set(top.filter((it) => it.kind === "user_message").map((it) => outgoingKey(it.text ?? "", it.images ?? [])));
    for (const [id, out] of this.outbox) {
      if (confirmed.has(outgoingKey(out.text, out.images))) this.outbox.delete(id);
    }
    this.transcript = top;
    this.children = kids;
    this.changed();
  }

  childrenOf(item: Item): Item[] {
    return this.children.get(item.id) ?? [];
  }

  get sortedTurns(): Turn[] {
    return [...this.turns.values()].sort((a, b) => a.n - b.n);
  }

  get pendingApprovals(): Item[] {
    return this.transcript.filter((it) => it.kind === "approval" && it.status === "pending");
  }

  get isRunning(): boolean {
    return this.session?.status === "running" || this.session?.status === "awaiting_approval";
  }

  /** The connection is up, so images can load. */
  get isConnected(): boolean {
    return this.host?.isConnected ?? false;
  }

  // Commands

  private get c(): SessionHost {
    if (!this.host) throw RPCError.disconnected();
    return this.host;
  }

  async send(text: string, images: ImageRef[] = []) {
    const c = this.c;
    const commandId = crypto.randomUUID();
    this.outbox.set(commandId, { text, images });
    this.changed();
    try {
      await c.call<Item>("session.prompt", {
        commandId,
        sessionId: this.sessionID,
        text,
        images: images.length ? images.map((i) => i.id) : undefined,
      });
    } catch (e) {
      this.outbox.delete(commandId);
      this.changed();
      throw e;
    }
  }

  async interrupt(force = false) {
    await this.c.call("session.interrupt", { sessionId: this.sessionID, force });
  }

  async respond(approval: Item, optionId: string, message?: string, answers?: Record<string, string>) {
    await this.c.call("approval.respond", {
      commandId: crypto.randomUUID(),
      sessionId: this.sessionID,
      approvalId: approval.id,
      optionId,
      message,
      answers,
    });
  }

  async setMode(mode: string) {
    await this.c.call("session.setMode", { sessionId: this.sessionID, mode });
  }

  turnDiff(turnId: string): Promise<Diff> {
    return this.c.call("git.turnDiff", { turnId });
  }

  sessionDiff(): Promise<Diff> {
    return this.c.call("git.sessionDiff", { sessionId: this.sessionID });
  }

  async revert(turn: number): Promise<FileStat[]> {
    const r = await this.c.call<{ files?: FileStat[] }>("git.revert", { sessionId: this.sessionID, turn });
    return r.files ?? [];
  }
}

function outgoingKey(text: string, images: ImageRef[]): string {
  return JSON.stringify([text, images.map((i) => i.id)]);
}
