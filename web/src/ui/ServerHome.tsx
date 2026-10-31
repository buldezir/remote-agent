import { ChevronLeft, ChevronRight, MessagesSquare, RefreshCw, SquarePen, WifiOff } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import type { Session } from "../protocol/models";
import { useObserved } from "../store/observable";
import { useConnection } from "./context";
import { Empty, ErrorAlert, Spinner } from "./primitives";
import { ArchiveConfirm, DeleteConfirm, RenameSession, SessionRow } from "./SessionRow";

/** A server's sessions on a phone, as on iPhone. A wide window has every
 *  server's in its sidebar instead (SessionsSidebar). */
export function ServerHome({
  onOpen,
  onNew,
  onBack,
  toolbar,
}: {
  onOpen: (id: string) => void;
  onNew: () => void;
  onBack: () => void;
  /** More buttons for the top bar. */
  toolbar?: ReactNode;
}) {
  const connection = useConnection();
  useObserved(connection);
  const [showArchived, setShowArchived] = useState(false);
  const [archiveTarget, setArchiveTarget] = useState<Session>();
  const [deleteTarget, setDeleteTarget] = useState<Session>();
  const [renameTarget, setRenameTarget] = useState<Session>();
  const [error, setError] = useState<string>();

  // Opening a server it gave up on tries again.
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
      <SessionRow
        session={s}
        selected={false}
        onOpen={() => onOpen(s.id)}
        onRename={() => setRenameTarget(s)}
        onArchive={s.archived ? undefined : () => setArchiveTarget(s)}
        onDelete={() => setDeleteTarget(s)}
      />
    </li>
  );

  return (
    <div className="server-home">
      <header className="bar">
        <button className="icon-button" onClick={onBack} aria-label="Servers">
          <ChevronLeft />
        </button>
        <h1 className="bar-title">{connection.server.name}</h1>
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
              Try again
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
      {archiveTarget && <ArchiveConfirm connection={connection} session={archiveTarget} onClose={() => setArchiveTarget(undefined)} />}
      {deleteTarget && <DeleteConfirm connection={connection} session={deleteTarget} onClose={() => setDeleteTarget(undefined)} />}
      {renameTarget && <RenameSession connection={connection} session={renameTarget} onClose={() => setRenameTarget(undefined)} onError={setError} />}
      <ErrorAlert error={error} onClose={() => setError(undefined)} />
    </div>
  );
}
