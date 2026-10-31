import type { SlashCommand, Diff, Draft, Event, FileStat, HarnessInfo, ImageRef, Item, Session, Turn } from "../protocol/models";
import { RPCError } from "../protocol/rpc";
import type { Drafts } from "./drafts";
import { Observable } from "./observable";

/** What a session store needs from its connection. */
export interface SessionHost {
  call<R>(method: string, params?: unknown, timeout?: number): Promise<R>;
  /** Calls a command with a commandId, sending it again after a drop. */
  deliver<R>(method: string, params: unknown, timeout?: number): Promise<R>;
  activate(store: SessionStore): void;
  deactivate(store: SessionStore): void;
  subscribe(store: SessionStore): Promise<void>;
  readonly isConnected: boolean;
  readonly drafts?: Drafts;
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
  /** The session was deleted on the server: its stream says so. */
  removed = false;
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
  /** Subscriptions asked for and not yet synchronized. Two can overlap (a
   *  view subscribes as the connection resubscribes everything): only the
   *  last one's `synchronized` makes the store live, so a newer replay is
   *  buffered rather than skipped, and its reset can't drop what it replays. */
  private outstanding = 0;

  constructor(
    readonly sessionID: string,
    private host: SessionHost | undefined,
  ) {
    super();
  }

  get isActive(): boolean {
    return this.viewers > 0;
  }

  /** The prompt being written and not sent yet. It outlives the session's
   *  view, and the page, but stays in this browser. Setting it doesn't
   *  notify: the composer keeps its own copy as it is typed. */
  get draft(): string {
    return this.host?.drafts?.get(this.sessionID) ?? "";
  }

  set draft(text: string) {
    this.host?.drafts?.set(this.sessionID, text);
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
    this.outstanding++;
    this.live = false;
    this.replay = [];
  }

  /** A subscribe call failed: its `synchronized` won't come. */
  subscriptionFailed() {
    this.outstanding = Math.max(0, this.outstanding - 1);
  }

  /** The socket closed: no subscription is outstanding any more. */
  closed() {
    this.outstanding = 0;
    this.live = false;
  }

  synchronized(seq: number, reset: boolean) {
    this.outstanding = Math.max(0, this.outstanding - 1);
    if (reset) {
      this.turns.clear();
      this.items.clear();
    }
    const buffered = this.replay;
    this.replay = [];
    for (const ev of buffered) this.upsert(ev);
    this.lastSeq = seq;
    this.live = this.outstanding === 0;
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
      case "session.removed":
        this.session = undefined;
        this.turns.clear();
        this.items.clear();
        this.outbox.clear();
        this.removed = true;
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

  /** The session's draft, unless it is on its way: `sendDraft` put it in
   *  the outbox, where it shows until the transcript has it. */
  get unsentDraft(): Draft | undefined {
    const d = this.session?.draft;
    if (!d) return undefined;
    const key = outgoingKey(d.text, d.images ?? []);
    for (const out of this.outbox.values()) if (outgoingKey(out.text, out.images) === key) return undefined;
    return d;
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

  /** Whether a prompt can join the running turn (steer): its agent takes
   *  one, and it isn't a slash command, which agents run only as a prompt of
   *  its own. */
  canSteer(text: string, harness: HarnessInfo | undefined): boolean {
    return this.isRunning && !!harness?.caps.steer && !text.trim().startsWith("/");
  }

  /** Sends a prompt. It shows in `outbox` until the transcript has it, and
   *  waits out a dropped connection; it throws once the connection gives up.
   *  With `steer`, a prompt sent while the agent works joins its turn where
   *  the agent can take it, and is queued where it can't. */
  async send(text: string, images: ImageRef[] = [], steer = false) {
    const c = this.c;
    const commandId = crypto.randomUUID();
    this.outbox.set(commandId, { text, images });
    this.changed();
    try {
      await c.deliver<Item>("session.prompt", {
        commandId,
        sessionId: this.sessionID,
        text,
        images: images.length ? images.map((i) => i.id) : undefined,
        steer: steer || undefined,
      });
    } catch (e) {
      this.outbox.delete(commandId);
      this.changed();
      throw e;
    }
  }

  /** Sends the session's draft as `send` sends a prompt: it shows in the
   *  outbox until the transcript has it, and the event that adds it there
   *  takes the draft away. A retry after a drop has the same commandId, so
   *  it goes once. It throws when the session has no draft. */
  async sendDraft() {
    const c = this.c;
    const draft = this.session?.draft;
    if (!draft) throw new RPCError("conflict", "This session has no draft to send.");
    const commandId = crypto.randomUUID();
    this.outbox.set(commandId, { text: draft.text, images: draft.images ?? [] });
    this.changed();
    try {
      await c.deliver<Item>("session.sendDraft", { commandId, sessionId: this.sessionID });
    } catch (e) {
      this.outbox.delete(commandId);
      this.changed();
      throw e;
    }
  }

  async commands(): Promise<SlashCommand[]> {
    try {
      return (await this.c.call<{ commands: SlashCommand[] }>("session.commands", { sessionId: this.sessionID }, 80_000)).commands;
    } catch (error) {
      if (error instanceof RPCError && error.code === "method_not_found") return [];
      throw error;
    }
  }

  async interrupt(force = false) {
    await this.c.call("session.interrupt", { sessionId: this.sessionID, force });
  }

  /** Interrupts the running turn and starts the next one with this queued
   *  prompt, ahead of the others. */
  async sendNow(itemId: string) {
    try {
      await this.c.call("session.sendNow", { sessionId: this.sessionID, itemId });
    } catch (e) {
      if (e instanceof RPCError && e.code === "method_not_found") {
        throw new RPCError("method_not_found", "This server's rad is too old to send a queued prompt now. Update rad on it.");
      }
      throw e;
    }
  }

  /** Adds this queued prompt to the running turn, without interrupting it,
   *  for agents that take one (`canSteer`). */
  async steer(itemId: string) {
    try {
      await this.c.call("session.steer", { sessionId: this.sessionID, itemId });
    } catch (e) {
      if (e instanceof RPCError && e.code === "method_not_found") {
        throw new RPCError("method_not_found", "This server's rad is too old to steer a prompt into a turn. Update rad on it.");
      }
      throw e;
    }
  }

  /** Takes this queued prompt out of the queue, unsent. */
  async cancelQueued(itemId: string) {
    try {
      await this.c.call("session.cancelQueued", { sessionId: this.sessionID, itemId });
    } catch (e) {
      if (e instanceof RPCError && e.code === "method_not_found") {
        throw new RPCError("method_not_found", "This server's rad is too old to cancel a queued prompt. Update rad on it.");
      }
      throw e;
    }
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
