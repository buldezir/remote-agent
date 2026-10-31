import {
  ArrowUp,
  AudioLines,
  ChevronDown,
  ChevronLeft,
  ChevronUp,
  CircleDashed,
  Copy,
  Ellipsis,
  FileDiff,
  FoldVertical,
  Folder,
  GitBranch,
  GitFork,
  GitPullRequestArrow,
  Mic,
  OctagonX,
  OctagonAlert,
  Pencil,
  Save,
  Search,
  Square,
  SquareSlash,
  SquareTerminal,
  Users,
} from "lucide-react";
import { lazy, Suspense, useEffect, useImperativeHandle, useLayoutEffect, useMemo, useRef, useState } from "react";
import { commandQuery, commandSuggestions } from "../protocol/commandSuggestions";
import type { Draft, SlashCommand } from "../protocol/models";
import { contextFraction, modelName, type BackgroundTask, type HarnessInfo, type Session } from "../protocol/models";
import { Attachments } from "../store/attachments";
import { useObserved } from "../store/observable";
import type { SessionStore } from "../store/sessionStore";
import { runContaining, transcriptRows } from "../store/toolRuns";
import { findMatches } from "../store/transcriptFind";
import { CommitSheet } from "./CommitSheet";
import { ContextGauge, HarnessIcon } from "./components";
import { useConnection } from "./context";
import { DiffView } from "./DiffView";
import { useDictation } from "./dictation";
import { highlightRegistry, occurrenceRanges } from "./findHighlight";
import { AttachButton, AttachmentStrip, imageFiles, useImageDrop } from "./images";
import { DraftBubble, ItemView, ToolRunRow, TurnFooter, UserBubble } from "./items/ItemView";
import { ErrorAlert, Menu, Spinner, type MenuEntry } from "./primitives";
import { focusDone, useFocusItem, usePageInFront, useWide } from "./router";
import { RenameSession } from "./SessionRow";
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
  // An item to show, as a search result asks.
  const focus = useFocusItem(connection.server.id, sessionID);
  const [error, setError] = useState<string>();
  const [showDiff, setShowDiff] = useState(false);
  const [showCommit, setShowCommit] = useState(false);
  const [showTerminal, setShowTerminal] = useState(false);
  const [renaming, setRenaming] = useState(false);
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
  // An item kept in view for a moment after it was scrolled to, while what
  // is above it settles, such as images loading; scrolling lets it go.
  const anchor = useRef<{ id: string; until: number }>(undefined);
  const scrollToEnd = () => {
    const el = scroller.current;
    if (el) el.scrollTop = el.scrollHeight;
  };
  const itemElement = (id: string) => content.current?.querySelector<HTMLElement>(`[data-item-id="${CSS.escape(id)}"]`) ?? null;
  // The item in the middle of the transcript, or its top at the top when
  // it's taller; false when it isn't shown.
  const centre = (id: string): boolean => {
    const el = itemElement(id);
    const sc = scroller.current;
    if (!el || !sc) return false;
    pinned.current = false;
    const item = el.getBoundingClientRect();
    const view = sc.getBoundingClientRect();
    sc.scrollTop += item.height > view.height - 16 ? item.top - view.top - 8 : item.top - view.top - (view.height - item.height) / 2;
    return true;
  };
  const reveal = (id: string): boolean => {
    if (!centre(id)) return false;
    anchor.current = { id, until: performance.now() + 1000 };
    return true;
  };
  useLayoutEffect(() => {
    const ro = new ResizeObserver(() => {
      const a = anchor.current;
      if (a && performance.now() < a.until) centre(a.id);
      else if (pinned.current) scrollToEnd();
    });
    if (content.current) ro.observe(content.current);
    return () => ro.disconnect();
  }, []);

  // Find in the session: the items whose text has the query, the current one
  // the newest at first. Closing it, or another session, forgets the tints.
  const [findOpen, setFindOpen] = useState(false);
  const [findQuery, setFindQuery] = useState("");
  const [findCurrent, setFindCurrent] = useState<string>();
  // Bumped to scroll to the current match again, though it's the same.
  const [findTick, setFindTick] = useState(0);
  const findField = useRef<HTMLInputElement>(null);
  const matches = useMemo(() => (findOpen ? findMatches(store.transcript, findQuery) : []), [findOpen, findQuery, store.transcript]);
  const matchSet = useMemo(() => new Set(matches), [matches]);
  const matchIndex = findCurrent !== undefined && matchSet.has(findCurrent) ? matches.indexOf(findCurrent) : matches.length - 1;
  const currentMatch = matchIndex >= 0 ? matches[matchIndex] : undefined;

  // Runs of tool calls fold (Settings › Transcript): those the user opened
  // or shut, by id, else open while the agent works in them. The run that
  // holds find's match shows open while find is on it.
  const { groupToolCalls } = useSettings();
  const [expandedRuns, setExpandedRuns] = useState<ReadonlyMap<string, boolean>>(new Map());
  const findRun = currentMatch ? runContaining(currentMatch, store.transcript, store.turns)?.id : undefined;
  const rows = useMemo(
    () => transcriptRows(store.transcript, store.turns, findRun ? new Map(expandedRuns).set(findRun, true) : expandedRuns, groupToolCalls),
    [store.transcript, store.turns, store.getVersion(), expandedRuns, findRun, groupToolCalls],
  );
  const toggleRun = (id: string, expanded: boolean) => setExpandedRuns((m) => new Map(m).set(id, !expanded));
  // Opens the run an item is folded into, as the user would: for a search
  // result, and what find was on as it closes. True when it was shut.
  const openRun = (id: string): boolean => {
    if (!groupToolCalls) return false;
    const run = runContaining(id, store.transcript, store.turns);
    if (!run || (expandedRuns.get(run.id) ?? run.live)) return false;
    setExpandedRuns((m) => new Map(m).set(run.id, true));
    return true;
  };

  // Opened at an item, as from a search result: once it shows, it's
  // scrolled to, before it's painted, and flashed. Until then the session
  // shows its end, as usual, and it stays there if the transcript, synced,
  // hasn't got the item. One in a folded run of tool calls opens it, and
  // shows once its rows are there.
  useLayoutEffect(() => {
    if (!focus) return;
    if (reveal(focus.itemID)) {
      flash(itemElement(focus.itemID)!);
      focusDone(focus);
    } else if (!openRun(focus.itemID) && store.live) {
      focusDone(focus);
    }
  }, [focus, rows]);

  const setFind = (query: string) => {
    setFindQuery(query);
    setFindCurrent(findMatches(store.transcript, query).at(-1));
    setFindTick((n) => n + 1);
  };
  const stepFind = (by: number) => {
    if (matches.length === 0) return;
    setFindCurrent(matches[(matchIndex + by + matches.length) % matches.length]);
    setFindTick((n) => n + 1);
  };
  const openFind = () => {
    if (findOpen) {
      findField.current?.focus();
      findField.current?.select();
      return;
    }
    setFindOpen(true);
    setFind(findQuery);
  };
  // What it found stays in view.
  const closeFind = () => {
    if (currentMatch) openRun(currentMatch);
    setFindOpen(false);
  };
  useEffect(() => {
    if (!findOpen) return;
    findField.current?.focus();
    findField.current?.select();
  }, [findOpen]);
  useLayoutEffect(() => {
    if (findOpen && currentMatch) reveal(currentMatch);
  }, [findOpen, currentMatch, findTick]);

  // ⌘F or Ctrl+F finds in the session, rather than the browser in the page;
  // ⌘G and ⇧⌘G go to the next and previous match while it's open. In the
  // terminal, Ctrl+F and Ctrl+G are the shell's; a sheet keeps them.
  const findKeys = useRef<(e: KeyboardEvent) => void>(undefined);
  findKeys.current = (e) => {
    const k = e.key.toLowerCase();
    if ((k !== "f" && k !== "g") || e.altKey) return;
    const target = e.target as Element | null;
    if (target?.closest?.("dialog")) return;
    const inTerminal = !!target?.closest?.(".terminal");
    if (!e.metaKey && !(e.ctrlKey && !inTerminal)) return;
    if (k === "f") {
      if (e.shiftKey) return; // the sessions' search
      e.preventDefault();
      openFind();
    } else if (findOpen) {
      e.preventDefault();
      stepFind(e.shiftKey ? -1 : 1);
    }
  };
  useEffect(() => {
    const key = (e: KeyboardEvent) => findKeys.current?.(e);
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, []);

  // The query's occurrences in the matching items' text, where the browser
  // can mark them, kept as the transcript changes.
  useEffect(() => {
    const registry = highlightRegistry();
    const root = content.current;
    if (!findOpen || !registry || !root) return;
    const paint = () => {
      const others: Range[] = [];
      const current: Range[] = [];
      for (const id of matches) {
        const el = root.querySelector(`[data-item-id="${CSS.escape(id)}"]`);
        if (el) (id === currentMatch ? current : others).push(...occurrenceRanges(el, findQuery));
      }
      registry.set("find-match", new Highlight(...others));
      registry.set("find-current", new Highlight(...current));
    };
    paint();
    let frame = 0;
    const mo = new MutationObserver(() => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(paint);
    });
    mo.observe(root, { subtree: true, childList: true, characterData: true });
    return () => {
      mo.disconnect();
      cancelAnimationFrame(frame);
      registry.delete("find-match");
      registry.delete("find-current");
    };
  }, [findOpen, findQuery, matches, currentMatch]);
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
  // rad commits in git projects, while the agent isn't working.
  const canCommit = !!connection.info?.commit && !!project?.isGitRepo && !!session && !session.archived;
  const openCommit = () => {
    setShowDiff(false);
    setShowCommit(true);
  };
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
    { label: "Find in Session", icon: Search, onSelect: openFind },
    !!connection.info?.rename && !!session && { label: "Rename…", icon: Pencil, onSelect: () => setRenaming(true) },
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
    canCommit && { label: "Commit…", icon: GitPullRequestArrow, disabled: store.isRunning, onSelect: openCommit },
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
        ...(project?.isGitRepo
          ? [
              // The branch opens the commit sheet, as Commit… does.
              canCommit
                ? { label: session.workspace.branch ?? "Detached HEAD", icon: GitBranch, detail: "Commit…", disabled: store.isRunning, onSelect: openCommit }
                : { label: session.workspace.branch ?? "Detached HEAD", icon: GitBranch },
            ]
          : []),
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
        {findOpen && (
          <FindBar
            field={findField}
            query={findQuery}
            onQuery={setFind}
            count={matches.length}
            index={matchIndex}
            onNext={() => stepFind(1)}
            onPrevious={() => stepFind(-1)}
            onClose={closeFind}
          />
        )}
        {/* In place of the messages, they stay underneath, hidden, so they keep their place. */}
        <div className={"session-main" + (terminal === "over" ? " covered" : "")}>
          <div
            className="transcript-scroll"
            ref={scroller}
            onScroll={(e) => {
              const el = e.currentTarget;
              pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
            }}
            onWheel={() => (anchor.current = undefined)}
            onTouchStart={() => (anchor.current = undefined)}
            onPointerDown={() => (anchor.current = undefined)}
            onKeyDown={() => (anchor.current = undefined)}
          >
            <div className="transcript" ref={content}>
              {!store.synced && (
                <div className="center pad">
                  <Spinner size={22} />
                </div>
              )}
              {rows.map((r) => {
                if (r.kind === "turn") return <TurnFooter key={"turn-" + r.turn.id} turn={r.turn} />;
                if (r.kind === "run") {
                  // Shut, it's tinted for the matches inside it.
                  const found = !r.expanded && r.run.items.some((it) => matchSet.has(it.id));
                  const current = found && r.run.items.some((it) => it.id === currentMatch);
                  return (
                    <div key={r.run.id} className={"item-row" + (found ? (current ? " find-current" : " find-match") : "")} data-item-id={r.run.id}>
                      <ToolRunRow run={r.run} expanded={r.expanded} onToggle={() => toggleRun(r.run.id, r.expanded)} />
                    </div>
                  );
                }
                return (
                  <div
                    key={r.item.id}
                    className={"item-row" + (matchSet.has(r.item.id) ? (r.item.id === currentMatch ? " find-current" : " find-match") : "")}
                    data-item-id={r.item.id}
                  >
                    <ItemView item={r.item} store={store} harness={harness} />
                  </div>
                );
              })}
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
            harness={harness}
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
        {showDiff && (
          <DiffView
            store={store}
            canRevert={session?.workspace.kind === "worktree"}
            onCommit={canCommit ? openCommit : undefined}
            onClose={() => setShowDiff(false)}
          />
        )}
        {showCommit && session && <CommitSheet session={session} onClose={() => setShowCommit(false)} />}
        {renaming && session && <RenameSession connection={connection} session={session} onClose={() => setRenaming(false)} onError={setError} />}
        <ErrorAlert error={error ?? attachments.error} onClose={() => (error ? setError(undefined) : attachments.clearError())} />
      </div>
      {(terminal === "below" || terminal === "right") && <DockedTerminal placement={terminal} panel={panel} />}
    </div>
  );
}

/** Find in the session, atop the transcript: Return goes to the next match
 *  and Shift+Return to the previous one, and Esc closes it. */
function FindBar({
  field,
  query,
  onQuery,
  count,
  index,
  onNext,
  onPrevious,
  onClose,
}: {
  field: React.RefObject<HTMLInputElement | null>;
  query: string;
  onQuery: (q: string) => void;
  count: number;
  index: number;
  onNext: () => void;
  onPrevious: () => void;
  onClose: () => void;
}) {
  return (
    <div className="find-bar" role="search">
      <div className="search-field">
        <Search size={14} className="c-overlay" aria-hidden />
        <input
          ref={field}
          type="search"
          placeholder="Find in session"
          aria-label="Find in session"
          value={query}
          onChange={(e) => onQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.nativeEvent.isComposing) return;
            if (e.key === "Enter") {
              e.preventDefault();
              if (e.shiftKey) onPrevious();
              else onNext();
            } else if (e.key === "Escape") {
              e.preventDefault();
              onClose();
            }
          }}
        />
      </div>
      <span className="find-count" aria-live="polite">
        {query.trim() === "" ? "" : count === 0 ? "No matches" : `${index + 1} of ${count}`}
      </span>
      <button className="icon-button" onClick={onPrevious} disabled={count === 0} title="Previous (⇧⌘G)" aria-label="Previous match">
        <ChevronUp size={18} />
      </button>
      <button className="icon-button" onClick={onNext} disabled={count === 0} title="Next (⌘G)" aria-label="Next match">
        <ChevronDown size={18} />
      </button>
      <button className="link find-done" onClick={onClose}>
        Done
      </button>
    </div>
  );
}

/** A moment's tint behind an item, fading out, as when a search result
 *  opens it: on its overlay (`.item-row::after`), which also tints cards. */
function flash(el: HTMLElement) {
  const color = getComputedStyle(el).getPropertyValue("--flash").trim();
  if (!color || typeof el.animate !== "function") return;
  el.animate([{ backgroundColor: color }, { backgroundColor: "transparent" }], { duration: 2000, easing: "ease-out", pseudoElement: "::after" });
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
  harness,
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
  harness: HarnessInfo | undefined;
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

  // ⇧⌘Return or Ctrl+Shift+Enter sends a prompt into the running turn, where
  // the agent takes one; Return queues it.
  const steers = store.canSteer(draft, harness);
  const send = (steer = false) => {
    if (!canSend || archived) return;
    const text = draft;
    const sent = attachments.entries;
    const images = attachments.refs;
    setDraft("");
    attachments.clear();
    onSend();
    store.send(text, images, steer && steers).catch((e) => {
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
              if (e.key === "Enter" && e.shiftKey && (e.metaKey || e.ctrlKey) && steers) {
                e.preventDefault();
                send(true);
              } else if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
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
            title={
              action === "send"
                ? steers
                  ? "Queue (Return) · Steer into the turn (⇧⌘Return)"
                  : "Send (Return)"
                : action === "dictate"
                  ? "Dictate"
                  : "Stop dictation"
            }
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
