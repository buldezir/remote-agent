import {
  ArrowUp,
  AudioLines,
  ChevronLeft,
  Copy,
  Ellipsis,
  FileDiff,
  FoldVertical,
  Folder,
  GitBranch,
  Mic,
  OctagonX,
  OctagonAlert,
  Square,
  SquareSlash,
} from "lucide-react";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { contextFraction, modelName, type HarnessInfo, type Item, type Session, type Turn } from "../protocol/models";
import { Attachments } from "../store/attachments";
import { useObserved } from "../store/observable";
import type { SessionStore } from "../store/sessionStore";
import { ContextGauge, HarnessIcon } from "./components";
import { useConnection } from "./context";
import { DiffView } from "./DiffView";
import { useDictation } from "./dictation";
import { AttachButton, AttachmentStrip, imageFiles, useImageDrop } from "./images";
import { ItemView, TurnFooter, UserBubble } from "./items/ItemView";
import { ErrorAlert, Menu, Spinner, type MenuEntry } from "./primitives";

export function SessionView({ sessionID, onBack }: { sessionID: string; onBack?: () => void }) {
  const connection = useConnection();
  useObserved(connection);
  const store = useMemo(() => connection.sessionStore(sessionID), [connection, sessionID]);
  useObserved(store);
  const [error, setError] = useState<string>();
  const [showDiff, setShowDiff] = useState(false);
  const attachments = useMemo(() => new Attachments(connection), [connection]);
  useObserved(attachments);

  useEffect(() => {
    store.activate();
    return () => store.deactivate();
  }, [store]);

  const session = store.session ?? connection.sessions.get(sessionID);
  const harness = session ? connection.harness(session.harness) : undefined;
  const project = session ? connection.projects.get(session.projectId) : undefined;

  useEffect(() => {
    document.title = session?.title ? `${session.title} – Remote Agent` : "Remote Agent";
  }, [session?.title]);

  const run = async (f: () => Promise<unknown>) => {
    try {
      await f();
    } catch (e) {
      setError((e as Error).message);
    }
  };

  // The transcript follows new output while it is scrolled to the end.
  const scroller = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);
  const scrollToEnd = () => {
    const el = scroller.current;
    if (el) el.scrollTop = el.scrollHeight;
  };
  useLayoutEffect(() => {
    const ro = new ResizeObserver(() => {
      if (pinned.current) scrollToEnd();
    });
    if (content.current) ro.observe(content.current);
    return () => ro.disconnect();
  }, []);

  const rows = useMemo(() => transcriptRows(store.transcript, store.turns), [store.transcript, store.turns, store.getVersion()]);
  const outbox = [...store.outbox.entries()].sort(([a], [b]) => a.localeCompare(b));
  const drop = useImageDrop(session?.archived ? undefined : attachments);

  const menu: MenuEntry[] = [
    harness?.modes?.length
      ? {
          section: "Permissions",
          items: harness.modes.map((m) => ({
            label: m.name,
            detail: m.description,
            checked: session?.mode === m.id,
            onSelect: () => run(() => store.setMode(m.id)),
          })),
        }
      : null,
    project?.isGitRepo && { label: "Changes", icon: FileDiff, onSelect: () => setShowDiff(true) },
    harness?.commands?.length
      ? {
          section: "Commands",
          items: harness.commands.map((c) => ({
            label: "/" + c.name,
            icon: c.name === "compact" ? FoldVertical : SquareSlash,
            detail: c.description,
            disabled: session?.archived,
            onSelect: () => {
              pinned.current = true;
              void run(() => store.send("/" + c.name));
            },
          })),
        }
      : null,
    session && {
      section: project?.name ?? "",
      items: [
        ...(project?.isGitRepo ? [{ label: session.workspace.branch ?? "Detached HEAD", icon: GitBranch }] : []),
        ...(session.workspace.kind === "worktree" ? [{ label: "Worktree", icon: Copy }] : []),
        {
          label: <span className="mono small">{session.workspace.path}</span>,
          icon: Folder,
          onSelect: () => void navigator.clipboard?.writeText(session.workspace.path),
          detail: "Copy path",
        },
      ],
    },
    store.isRunning && { label: "Force stop agent", icon: OctagonX, destructive: true, onSelect: () => run(() => store.interrupt(true)) },
  ];

  return (
    <div className={"session" + (drop.over ? " dropping" : "")} {...drop.handlers}>
      <header className="bar session-bar">
        {onBack && (
          <button className="icon-button" onClick={onBack} aria-label="Back">
            <ChevronLeft />
          </button>
        )}
        <div className="session-heading">
          <h1>{session?.title ?? "Session"}</h1>
          {session && (
            <div className="session-subtitle">
              <HarnessIcon id={session.harness} size={12} />
              <span>{subtitle(session, harness)}</span>
              {contextFraction(session.context) !== undefined && (
                <>
                  <span>·</span>
                  <ContextGauge fraction={contextFraction(session.context)!} />
                </>
              )}
            </div>
          )}
        </div>
        <Menu label={<Ellipsis />} title="Session" entries={menu} />
      </header>
      <div className="transcript-scroll" ref={scroller} onScroll={(e) => {
        const el = e.currentTarget;
        pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
      }}>
        <div className="transcript" ref={content}>
          {!store.synced && (
            <div className="center pad">
              <Spinner size={22} />
            </div>
          )}
          {rows.map((r) =>
            r.kind === "item" ? (
              <ItemView key={r.item.id} item={r.item} store={store} />
            ) : (
              <TurnFooter key={"turn-" + r.turn.id} turn={r.turn} />
            ),
          )}
          {outbox.map(([id, out]) => (
            <UserBubble key={id} text={out.text} images={out.images} pending />
          ))}
          {store.synced && store.transcript.length === 0 && outbox.length === 0 && (
            <div className="hint center pad">No prompts yet. Write the first one below.</div>
          )}
          {session?.status === "running" && (
            <div className="working">
              <Spinner size={14} /> Working…
            </div>
          )}
          {session?.status === "error" && session.error && (
            <div className="item-error">
              <OctagonAlert size={14} /> {session.error}
            </div>
          )}
        </div>
      </div>
      <Composer
        store={store}
        session={session}
        attachments={attachments}
        onSend={() => {
          pinned.current = true;
          scrollToEnd();
        }}
        onError={setError}
      />
      {showDiff && <DiffView store={store} canRevert={session?.workspace.kind === "worktree"} onClose={() => setShowDiff(false)} />}
      <ErrorAlert error={error ?? attachments.error} onClose={() => (error ? setError(undefined) : attachments.clearError())} />
    </div>
  );
}

type Row = { kind: "item"; item: Item } | { kind: "turn"; turn: Turn };

/** Items in order, with a footer after the last item of each interrupted or failed turn. */
function transcriptRows(items: Item[], turns: Map<string, Turn>): Row[] {
  const out: Row[] = [];
  items.forEach((item, i) => {
    out.push({ kind: "item", item });
    const next = items[i + 1]?.turnId;
    const t = item.turnId ? turns.get(item.turnId) : undefined;
    if (t && next !== item.turnId && (t.status === "interrupted" || t.status === "failed")) out.push({ kind: "turn", turn: t });
  });
  return out;
}

/** "Opus 5.5 · high · Ask": the model (the harness until the agent reports
 *  one), its reasoning effort and the permission mode. */
function subtitle(s: Session, harness: HarnessInfo | undefined): string {
  const mode = s.mode ? (harness?.modes?.find((m) => m.id === s.mode)?.name ?? s.mode) : undefined;
  return [modelName(s, harness) ?? harness?.name ?? s.harness, s.modelInfo?.effort ?? s.effort, mode].filter(Boolean).join(" · ");
}

function Composer({
  store,
  session,
  attachments,
  onSend,
  onError,
}: {
  store: SessionStore;
  session: Session | undefined;
  attachments: Attachments;
  onSend: () => void;
  onError: (e: string) => void;
}) {
  const [draft, setDraft] = useState("");
  const [focused, setFocused] = useState(false);
  const field = useRef<HTMLTextAreaElement>(null);
  const dictation = useDictation();
  const archived = session?.archived === true;

  useEffect(() => {
    if (dictation.error) {
      onError(dictation.error);
      dictation.clearError();
    }
  }, [dictation.error]);

  // The field grows with its text, up to 12 lines.
  useLayoutEffect(() => {
    const el = field.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = el.scrollHeight + "px";
  }, [draft]);

  // ⌘. or Ctrl+. stops the agent, as in the Mac app.
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if (e.key === "." && (e.metaKey || e.ctrlKey) && store.isRunning) {
        e.preventDefault();
        store.interrupt().catch((err) => onError(err.message));
      }
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, [store]);

  const nothingToSend = draft.trim() === "" && attachments.isEmpty;
  const canSend = !nothingToSend && attachments.ready;
  const action = dictation.phase !== "idle" ? "stopDictation" : nothingToSend && dictation.supported ? "dictate" : "send";

  const send = () => {
    if (!canSend || archived) return;
    const text = draft;
    const images = attachments.refs;
    setDraft("");
    attachments.clear();
    onSend();
    store.send(text, images).catch((e) => onError(e.message));
  };

  const main = () => {
    if (action === "send") send();
    else if (action === "stopDictation") dictation.stop();
    else {
      // Dictated text is appended to whatever was already typed.
      const prefix = draft;
      const sep = prefix === "" || /\s$/.test(prefix) ? "" : " ";
      dictation.start((text) => setDraft(prefix + sep + text));
    }
  };

  const placeholder = dictation.phase === "listening" ? "Listening…" : store.isRunning ? "Queue a follow-up…" : "Prompt";

  return (
    <div className="composer-wrap">
      <div className={"composer" + (focused ? " focused" : "")} onMouseDown={(e) => {
        if (e.target === e.currentTarget) {
          e.preventDefault();
          field.current?.focus();
        }
      }}>
        <AttachmentStrip attachments={attachments} />
        <div className="composer-row">
          <AttachButton attachments={attachments} disabled={archived} />
          <textarea
            ref={field}
            className="composer-field"
            rows={1}
            value={draft}
            placeholder={placeholder}
            disabled={archived}
            onChange={(e) => setDraft(e.target.value)}
            onFocus={() => setFocused(true)}
            onBlur={() => setFocused(false)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
                e.preventDefault();
                send();
              }
            }}
            onPaste={(e) => {
              const files = imageFiles(e.clipboardData);
              if (files.length) {
                e.preventDefault();
                void attachments.add(files);
              }
            }}
          />
          {store.isRunning && (
            <button className="round-button main c-red" onClick={() => store.interrupt().catch((e) => onError(e.message))} title="Stop (⌘.)" aria-label="Stop">
              <Square size={11} fill="currentColor" />
            </button>
          )}
          <button
            className={"round-button main " + (action === "stopDictation" ? "c-red" : "c-accent")}
            onClick={main}
            disabled={archived || (action === "send" && !canSend)}
            title={action === "send" ? "Send (Return)" : action === "dictate" ? "Dictate" : "Stop dictation"}
            aria-label={action === "send" ? "Send" : action === "dictate" ? "Dictate" : "Stop dictation"}
          >
            {action === "send" ? <ArrowUp size={18} strokeWidth={2.5} /> : action === "dictate" ? <Mic size={17} /> : <AudioLines size={17} className="pulse" />}
          </button>
        </div>
      </div>
    </div>
  );
}
