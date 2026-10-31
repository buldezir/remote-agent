import {
  ArrowUp,
  AudioLines,
  ChevronLeft,
  CircleDashed,
  Copy,
  Ellipsis,
  FileDiff,
  FoldVertical,
  Folder,
  GitBranch,
  GitFork,
  Mic,
  OctagonX,
  OctagonAlert,
  Save,
  Square,
  SquareSlash,
  SquareTerminal,
  Users,
} from "lucide-react";
import { lazy, Suspense, useEffect, useImperativeHandle, useLayoutEffect, useMemo, useRef, useState } from "react";
import { commandQuery, commandSuggestions } from "../protocol/commandSuggestions";
import type { Draft, SlashCommand } from "../protocol/models";
import { contextFraction, modelName, type BackgroundTask, type HarnessInfo, type Item, type Session, type Turn } from "../protocol/models";
import { Attachments } from "../store/attachments";
import { useObserved } from "../store/observable";
import type { SessionStore } from "../store/sessionStore";
import { ContextGauge, HarnessIcon } from "./components";
import { useConnection } from "./context";
import { DiffView } from "./DiffView";
import { useDictation } from "./dictation";
import { AttachButton, AttachmentStrip, imageFiles, useImageDrop } from "./images";
import { DraftBubble, ItemView, TurnFooter, UserBubble } from "./items/ItemView";
import { ErrorAlert, Menu, Spinner, type MenuEntry } from "./primitives";
import { usePageInFront, useWide } from "./router";
import { useSettings } from "./settings";
import { storage } from "../store/storage";

// xterm.js loads with the first terminal opened.
const TerminalPanel = lazy(() => import("./TerminalPanel"));

/** A session's messages and prompt. `onDeleted` is called once the session
 *  is deleted, here or on another device. With `focusPrompt`, a session just
 *  started empty, the prompt takes the keyboard. `onFork` asks for a fork of
 *  the session. */
export function SessionView({
  sessionID,
  focusPrompt = false,
  onBack,
  onDeleted,
  onFork,
}: {
  sessionID: string;
  focusPrompt?: boolean;
  onBack?: () => void;
  onDeleted?: () => void;
  onFork?: (s: Session) => void;
}) {
  const connection = useConnection();
  useObserved(connection);
  const store = useMemo(() => connection.sessionStore(sessionID), [connection, sessionID]);
  useObserved(store);
  const [error, setError] = useState<string>();
  const [showDiff, setShowDiff] = useState(false);
  const [showTerminal, setShowTerminal] = useState(false);
  const placement = useTerminalPlacement();
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

  // Shown in a page in front of the user, the session is read, on every
  // device: at once, when a turn ends while it shows, and when the page
  // comes back to the front.
  const inFront = usePageInFront();
  const unread = !!connection.sessions.get(sessionID)?.unread;
  useEffect(() => {
    if (inFront && unread) void connection.markRead(sessionID);
  }, [inFront, unread, connection, sessionID]);

  // Its stream says so, or the index, once synced: before it, every
  // session looks missing.
  const deleted = store.removed || (connection.indexSynced && !connection.sessions.has(sessionID));
  useEffect(() => {
    if (deleted) onDeleted?.();
  }, [deleted]);

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
  const composer = useRef<ComposerHandle>(null);

  // The draft at the foot of the transcript, until it's sent. A session with
  // nothing in it yet can keep the prompt being written as one.
  const draft = store.unsentDraft;
  const draftable =
    !!connection.info?.drafts &&
    !!session &&
    !session.archived &&
    !session.draft &&
    store.synced &&
    store.transcript.length === 0 &&
    store.outbox.size === 0;
  const sendDraft = () => {
    pinned.current = true;
    scrollToEnd();
    return run(() => store.sendDraft());
  };
  // Taken off the server, then into the composer.
  const editDraft = (d: Draft) =>
    run(async () => {
      await connection.saveDraft(sessionID, "", []);
      composer.current?.edit(d);
    });

  // ⌘J or ⌘` shows and hides the terminal, and Ctrl does too outside it:
  // in the terminal, Ctrl+J and Ctrl+` are the shell's. Hiding it gives
  // the keyboard back to the prompt.
  const canTerminal = !!connection.info?.terminal && !!session && !session.archived;
  // A turn has to have ended for a fork to go on from it.
  const canFork = !!connection.info?.forks && !!session && !!onFork && store.sortedTurns.some((t) => t.status !== "running");
  const composerField = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    if (!canTerminal) return;
    const key = (e: KeyboardEvent) => {
      const k = e.key.toLowerCase();
      if ((k !== "j" && k !== "`") || e.shiftKey || e.altKey) return;
      const inTerminal = !!(e.target as Element | null)?.closest?.(".terminal");
      if (!e.metaKey && !(e.ctrlKey && !inTerminal)) return;
      // Capturing, so that xterm.js doesn't take it first.
      e.preventDefault();
      e.stopPropagation();
      setShowTerminal(!showTerminal);
      // Once the prompt is visible again.
      if (showTerminal) setTimeout(() => composerField.current?.focus());
    };
    window.addEventListener("keydown", key, true);
    return () => window.removeEventListener("keydown", key, true);
  }, [canTerminal, showTerminal]);

  // A session just started empty: its prompt takes the keyboard.
  useEffect(() => {
    if (focusPrompt) composerField.current?.focus();
  }, []);

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
    canTerminal && { label: showTerminal ? "Hide Terminal" : "Terminal", icon: SquareTerminal, onSelect: () => setShowTerminal(!showTerminal) },
    canFork && { label: "Fork…", icon: GitFork, onSelect: () => onFork(session) },
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

  const terminal = showTerminal && session && !session.archived ? placement : undefined;
  const panel = (style?: React.CSSProperties) =>
    session && (
      <Suspense fallback={<div className="terminal" style={style} />}>
        <TerminalPanel sessionID={session.id} folder={session.workspace.path} onHide={() => setShowTerminal(false)} style={style} />
      </Suspense>
    );

  return (
    <div className={"session-frame" + (terminal ? " terminal-" + terminal : "")}>
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
        {/* In place of the messages, they stay underneath, hidden, so they keep their place. */}
        <div className={"session-main" + (terminal === "over" ? " covered" : "")}>
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
              {draft && <DraftBubble draft={draft} archived={!!session?.archived} onSend={sendDraft} onEdit={() => editDraft(draft)} />}
              {store.synced && store.transcript.length === 0 && outbox.length === 0 && !session?.draft && (
                <div className="hint center pad">No prompts yet. Write the first one below.</div>
              )}
              {session?.status === "running" && (
                <div className="working">
                  <Spinner size={14} /> Working…
                </div>
              )}
              {!!session?.backgroundTasks?.length && <BackgroundTasks tasks={session.backgroundTasks} />}
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
            commands={harness?.commands ?? []}
            field={composerField}
            handle={composer}
            draftable={draftable}
            onSend={() => {
              pinned.current = true;
              scrollToEnd();
            }}
            onError={setError}
          />
          {terminal === "over" && panel()}
        </div>
        {showDiff && <DiffView store={store} canRevert={session?.workspace.kind === "worktree"} onClose={() => setShowDiff(false)} />}
        <ErrorAlert error={error ?? attachments.error} onClose={() => (error ? setError(undefined) : attachments.clearError())} />
      </div>
      {(terminal === "below" || terminal === "right") && <DockedTerminal placement={terminal} panel={panel} />}
    </div>
  );
}

/** What the agent left running after its turn; it reports back in a turn of
 *  its own when one ends. */
function BackgroundTasks({ tasks }: { tasks: BackgroundTask[] }) {
  return (
    <div className="working background-tasks" role="status" aria-label="Running in the background">
      <Spinner size={14} />
      <div className="background-tasks-list">
        <span>Running in the background</span>
        {tasks.map((t) => {
          const Icon = t.kind === "command" ? SquareTerminal : t.kind === "agent" ? Users : CircleDashed;
          return (
            <div key={t.id} className="background-task">
              <Icon size={13} aria-hidden />
              <span className="clamp2">{t.description}</span>
            </div>
          );
        })}
      </div>
    </div>
  );
}

/** Where the terminal shows: in a narrow window always in place of the
 *  messages; a sidebar needs room beside them, or it goes below. */
function useTerminalPlacement() {
  const settings = useSettings();
  const wide = useWide();
  const roomy = useMedia("(min-width: 1200px)");
  if (!wide) return "over";
  return settings.terminalPlacement === "right" && !roomy ? "below" : settings.terminalPlacement;
}

function useMedia(query: string): boolean {
  const [matches, setMatches] = useState(() => matchMedia(query).matches);
  useEffect(() => {
    const m = matchMedia(query);
    const l = () => setMatches(m.matches);
    m.addEventListener("change", l);
    return () => m.removeEventListener("change", l);
  }, [query]);
  return matches;
}

/** The terminal below the messages or beside them, with a bar to resize it. */
function DockedTerminal({ placement, panel }: { placement: "below" | "right"; panel: (style: React.CSSProperties) => React.ReactNode }) {
  const below = placement === "below";
  const key = below ? "terminalHeight" : "terminalWidth";
  const [size, setSize] = useState(() => Number(storage.get(key)) || (below ? 280 : 520));
  const max = (below ? window.innerHeight : window.innerWidth) - (below ? 200 : 360);
  const shown = Math.max(Math.min(size, max), 120);
  return (
    <>
      <PanelHandle
        vertical={below}
        size={shown}
        min={120}
        max={max}
        onChange={(n) => {
          setSize(n);
          storage.set(key, String(Math.round(n)));
        }}
      />
      {panel(below ? { height: shown } : { width: shown })}
    </>
  );
}

type Row = { kind: "item"; item: Item } | { kind: "turn"; turn: Turn };

/** Items in order, with a footer after the last item of each interrupted or failed turn.
 *  A prompt queued during a turn sits among its items, so the last is looked up. */
function transcriptRows(items: Item[], turns: Map<string, Turn>): Row[] {
  const out: Row[] = [];
  const last = new Map<string, number>();
  items.forEach((item, i) => {
    if (item.turnId) last.set(item.turnId, i);
  });
  items.forEach((item, i) => {
    out.push({ kind: "item", item });
    const t = item.turnId ? turns.get(item.turnId) : undefined;
    if (t && last.get(t.id) === i && (t.status === "interrupted" || t.status === "failed")) out.push({ kind: "turn", turn: t });
  });
  return out;
}

/** "Opus 5.5 · high · Ask": the model (the harness until the agent reports
 *  one), its reasoning effort and the permission mode. */
function subtitle(s: Session, harness: HarnessInfo | undefined): string {
  const mode = s.mode ? (harness?.modes?.find((m) => m.id === s.mode)?.name ?? s.mode) : undefined;
  return [modelName(s, harness) ?? harness?.name ?? s.harness, s.modelInfo?.effort ?? s.effort, mode].filter(Boolean).join(" · ");
}

/** What the session view asks of its composer. */
interface ComposerHandle {
  /** Takes in a draft taken back to edit: its text ahead of what is
   *  written, its images ahead of those attached, and the keyboard. */
  edit(d: Draft): void;
}

/** With `draftable`, a session with nothing in it yet, the prompt can be
 *  saved as the session's draft rather than sent. */
function Composer({
  commands: builtins,
  store,
  session,
  attachments,
  field,
  handle,
  draftable,
  onSend,
  onError,
}: {
  commands: SlashCommand[];
  store: SessionStore;
  session: Session | undefined;
  attachments: Attachments;
  field: React.RefObject<HTMLTextAreaElement | null>;
  handle: React.Ref<ComposerHandle>;
  draftable: boolean;
  onSend: () => void;
  onError: (e: string) => void;
}) {
  const connection = useConnection();
  // The store keeps the draft for when the session is open again.
  const [draft, setDraftState] = useState(() => store.draft);
  const setDraft = (text: string) => {
    setDraftState(text);
    store.draft = text;
  };
  useImperativeHandle(handle, () => ({
    edit(d: Draft) {
      // As a prompt that couldn't be sent comes back.
      setDraft([d.text, store.draft].filter((t) => t !== "").join("\n"));
      attachments.restoreUploaded(d.images ?? []);
      field.current?.focus();
    },
  }));
  const [focused, setFocused] = useState(false);
  const dictation = useDictation();
  const archived = session?.archived === true;
  const [commands, setCommands] = useState<SlashCommand[]>([]);
  const [loadingCommands, setLoadingCommands] = useState(false);
  const [commandsError, setCommandsError] = useState(false);
  const [commandIndex, setCommandIndex] = useState(0);
  const [commandsDismissed, setCommandsDismissed] = useState(false);
  const wantsCommands = focused && commandQuery(draft) !== undefined && !archived;
  const connected = store.isConnected;
  const suggestions = focused && !archived && !commandsDismissed ? commandSuggestions(draft, [...commands, ...builtins]) : [];
  const complete = (command: SlashCommand) => {
    setDraft("/" + command.name + " ");
    field.current?.focus();
  };
  useEffect(() => { setCommandIndex(0); setCommandsDismissed(false); }, [draft]);
  useEffect(() => {
    if (!wantsCommands || !connected) return;
    let cancelled = false;
    setCommands([]);
    setLoadingCommands(true);
    setCommandsError(false);
    store.commands().then((result) => { if (!cancelled) setCommands(result); })
      .catch(() => { if (!cancelled) setCommandsError(true); })
      .finally(() => { if (!cancelled) setLoadingCommands(false); });
    return () => { cancelled = true; setLoadingCommands(false); };
  }, [store, wantsCommands, connected]);
  useEffect(() => {
    document.getElementById(`command-option-${commandIndex}`)?.scrollIntoView({ block: "nearest" });
  }, [draft, commands, commandIndex]);

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
      // In the terminal, Ctrl+. is the shell's.
      if ((e.target as Element | null)?.closest?.(".terminal")) return;
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

  // Kept in the session rather than sent; it then shows in the transcript.
  const [savingDraft, setSavingDraft] = useState(false);
  const offersDraft = draftable && !nothingToSend;
  const saveDraft = () => {
    if (!offersDraft || !canSend || savingDraft) return;
    setSavingDraft(true);
    connection
      .saveDraft(store.sessionID, draft, attachments.refs)
      .then(
        () => {
          setDraft("");
          attachments.clear();
        },
        (e) => onError(e.message),
      )
      .finally(() => setSavingDraft(false));
  };

  // ⌘S or Ctrl+S saves it, while the button shows, but not from a sheet
  // over the session. In the terminal, Ctrl+S is the shell's.
  const saveDraftKey = useRef(saveDraft);
  saveDraftKey.current = saveDraft;
  useEffect(() => {
    if (!offersDraft) return;
    const key = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() !== "s" || e.shiftKey || e.altKey) return;
      if ((e.target as Element | null)?.closest?.("dialog")) return;
      const inTerminal = !!(e.target as Element | null)?.closest?.(".terminal");
      if (!e.metaKey && !(e.ctrlKey && !inTerminal)) return;
      e.preventDefault();
      saveDraftKey.current();
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, [offersDraft]);

  const send = () => {
    if (!canSend || archived) return;
    const text = draft;
    const sent = attachments.entries;
    const images = attachments.refs;
    setDraft("");
    attachments.clear();
    onSend();
    store.send(text, images).catch((e) => {
      // It didn't go: back into the composer, ahead of anything written since.
      setDraft([text, store.draft].filter((t) => t !== "").join("\n"));
      attachments.restore(sent);
      onError(e.message);
    });
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
        {suggestions.length > 0 && (
          <div className="command-suggestions" role="listbox" id="command-suggestions" aria-label="Command suggestions">
            {suggestions.map((command, index) => (
              <button key={command.name} id={`command-option-${index}`} type="button" role="option"
                aria-selected={index === commandIndex}
                onMouseDown={(e) => e.preventDefault()} onClick={() => complete(command)}>
                <span><span className="mono">/{command.name}</span>
                  {command.description && <small>{command.description}</small>}</span>
                {command.scope && <small className="command-scope">{command.scope}</small>}
              </button>
            ))}
          </div>
        )}
        {focused && wantsCommands && !commandsDismissed && (loadingCommands || commandsError) && (
          <small className="command-discovery-status">{loadingCommands ? "Loading skills…" : "Skills unavailable. Try typing / again."}</small>
        )}
        <div className="composer-row">
          <AttachButton attachments={attachments} disabled={archived} />
          <textarea
            aria-controls={suggestions.length ? "command-suggestions" : undefined}
            aria-expanded={suggestions.length > 0}
            aria-autocomplete="list"
            aria-activedescendant={suggestions.length ? `command-option-${Math.min(commandIndex, suggestions.length - 1)}` : undefined}
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
              if (e.nativeEvent.isComposing) return;
              if (suggestions.length && !e.shiftKey) {
                if (e.key === "ArrowDown" || e.key === "ArrowUp") {
                  e.preventDefault();
                  setCommandIndex(Math.max(0, Math.min(suggestions.length - 1, commandIndex + (e.key === "ArrowDown" ? 1 : -1))));
                  return;
                }
                if (e.key === "Escape") { e.preventDefault(); setCommandsDismissed(true); return; }
                if (e.key === "Enter" || e.key === "Tab") {
                  const selected = suggestions[Math.min(commandIndex, suggestions.length - 1)];
                  if (e.key !== "Enter" || draft !== "/" + selected.name) {
                    e.preventDefault(); complete(selected); return;
                  }
                }
              }
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
          {offersDraft && (
            <button
              className="round-button c-subtext"
              onClick={saveDraft}
              disabled={!canSend || savingDraft}
              title="Save as Draft (⌘S)"
              aria-label="Save as Draft"
            >
              {savingDraft ? <Spinner size={15} /> : <Save size={18} />}
            </button>
          )}
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

/** The bar between the messages and the terminal: dragging it resizes the
 *  terminal, which is `size` pixels tall below them or wide beside them. */
function PanelHandle({ vertical, size, onChange, min, max }: { vertical: boolean; size: number; onChange: (n: number) => void; min: number; max: number }) {
  return (
    <div
      className="panel-handle"
      onPointerDown={(e) => {
        e.preventDefault();
        const start = vertical ? e.clientY : e.clientX;
        const from = size;
        const move = (m: PointerEvent) => {
          const d = start - (vertical ? m.clientY : m.clientX);
          onChange(Math.min(Math.max(from + d, min), max));
        };
        const up = () => {
          window.removeEventListener("pointermove", move);
          window.removeEventListener("pointerup", up);
        };
        window.addEventListener("pointermove", move);
        window.addEventListener("pointerup", up);
      }}
    />
  );
}
