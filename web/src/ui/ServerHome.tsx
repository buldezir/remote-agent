import { Archive, ChevronLeft, ChevronRight, GitBranch, MessagesSquare, RefreshCw, SquarePen, WifiOff } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import type { Session } from "../protocol/models";
import { useObserved } from "../store/observable";
import { ConnectionDot, HarnessBadge, RelativeTime, StatusIcon } from "./components";
import { useConnection } from "./context";
import { Confirm, Empty, Spinner } from "./primitives";

/** A server's sessions: the sidebar in a wide window, the page on a phone. */
export function ServerHome({
  selected,
  onOpen,
  onNew,
  onBack,
  toolbar,
  footer,
}: {
  selected?: string;
  onOpen: (id: string) => void;
  onNew: () => void;
  onBack?: () => void;
  /** More buttons for the top bar. */
  toolbar?: ReactNode;
  footer?: ReactNode;
}) {
  const connection = useConnection();
  useObserved(connection);
  const [showArchived, setShowArchived] = useState(false);
  const [archiveTarget, setArchiveTarget] = useState<Session>();

  useEffect(() => {
    connection.start();
  }, [connection]);

  const active = connection.activeSessions;
  const attention = active.filter((s) => s.status === "awaiting_approval");
  const rest = active.filter((s) => s.status !== "awaiting_approval");
  const archived = connection.archivedSessions;
  const state = connection.state;

  const row = (s: Session) => (
    <li key={s.id}>
      <SessionRow session={s} selected={s.id === selected} onOpen={() => onOpen(s.id)} onArchive={s.archived ? undefined : () => setArchiveTarget(s)} />
    </li>
  );

  return (
    <div className="server-home">
      <header className="bar">
        {onBack && (
          <button className="icon-button" onClick={onBack} aria-label="Servers">
            <ChevronLeft />
          </button>
        )}
        <h1 className="bar-title">
          {!onBack && <ConnectionDot state={state} />} {connection.server.name}
        </h1>
        {toolbar}
        <button className="icon-button" onClick={onNew} disabled={state.kind !== "connected"} title="New session" aria-label="New session">
          <SquarePen size={19} />
        </button>
      </header>
      <div className="list-scroll">
        {state.kind === "failed" && (
          <div className="banner">
            <div className="c-red">
              <WifiOff size={15} /> {state.message}
            </div>
            <button className="link" onClick={() => connection.retry()}>
              Retry now
            </button>
          </div>
        )}
        {state.kind === "connecting" && connection.indexSynced && (
          <div className="banner hint">
            <RefreshCw size={15} /> Reconnecting…
          </div>
        )}
        {attention.length > 0 && (
          <>
            <h4 className="list-header">Needs you</h4>
            <ul className="session-list">{attention.map(row)}</ul>
          </>
        )}
        {(rest.length > 0 || attention.length > 0) && (
          <>
            <h4 className="list-header">Sessions</h4>
            <ul className="session-list">{rest.map(row)}</ul>
          </>
        )}
        {archived.length > 0 && (
          <>
            <button className="list-header disclosure-header" onClick={() => setShowArchived(!showArchived)} aria-expanded={showArchived}>
              Archived <span className="c-overlay">{archived.length}</span>
              <ChevronRight size={14} className={"chevron" + (showArchived ? " open" : "")} />
            </button>
            {showArchived && <ul className="session-list">{archived.map(row)}</ul>}
          </>
        )}
        {connection.indexSynced && connection.sessions.size === 0 && (
          <Empty icon={MessagesSquare} title="No sessions yet">
            <p>Start an agent in one of your projects.</p>
            <button className="button prominent" onClick={onNew} disabled={state.kind !== "connected"}>
              New session
            </button>
          </Empty>
        )}
        {!connection.indexSynced && state.kind === "connecting" && (
          <div className="center pad hint">
            <Spinner /> Connecting…
          </div>
        )}
      </div>
      {footer}
      {archiveTarget && (
        <Confirm
          title="Archive session?"
          message={
            archiveTarget.workspace.kind === "worktree" ? `The branch ${archiveTarget.workspace.branch ?? ""} is kept either way.` : "The agent process is stopped."
          }
          actions={[
            { label: "Archive", onSelect: () => connection.archive(archiveTarget.id, false).catch(() => {}) },
            ...(archiveTarget.workspace.kind === "worktree"
              ? [{ label: "Archive and delete worktree", destructive: true, onSelect: () => connection.archive(archiveTarget.id, true).catch(() => {}) }]
              : []),
          ]}
          onClose={() => setArchiveTarget(undefined)}
        />
      )}
    </div>
  );
}

function SessionRow({ session, selected, onOpen, onArchive }: { session: Session; selected: boolean; onOpen: () => void; onArchive?: () => void }) {
  const connection = useConnection();
  const project = connection.projects.get(session.projectId);
  const harness = connection.harness(session.harness);
  return (
    <div className={"session-row" + (selected ? " selected" : "")}>
      <button className="session-row-main" onClick={onOpen} aria-current={selected ? "page" : undefined}>
        <StatusIcon status={session.status} />
        <span className="session-row-text">
          <span className="session-title">{session.title}</span>
          <span className="session-meta">
            <HarnessBadge id={session.harness} name={harness?.name} />
            {project && <span className="nowrap">{project.name}</span>}
            {session.workspace.kind === "worktree" && session.workspace.branch && (
              <span className="branch">
                <GitBranch size={11} /> <span className="branch-name">{session.workspace.branch}</span>
              </span>
            )}
            <span className="spacer" />
            <RelativeTime date={session.updatedAt} />
          </span>
        </span>
      </button>
      {onArchive && (
        <button className="session-row-action icon-button" onClick={onArchive} title="Archive…" aria-label="Archive">
          <Archive size={15} />
        </button>
      )}
    </div>
  );
}
