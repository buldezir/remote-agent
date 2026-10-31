import { Archive, GitBranch, Pencil, Trash2 } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import type { Session } from "../protocol/models";
import type { ServerConnection } from "../store/serverConnection";
import { HarnessBadge, RelativeTime, StatusIcon } from "./components";
import { useConnection } from "./context";
import { Confirm, useContextMenu, type MenuEntry } from "./primitives";
import { needsYou } from "./sidebarLayout";

/** A session in a list: its status, title, agent, project and branch. Its
 *  context menu renames, archives or deletes it. */
export function SessionRow({
  session,
  selected,
  onOpen,
  onRename,
  onArchive,
  onDelete,
}: {
  session: Session;
  selected: boolean;
  onOpen: () => void;
  onRename?: () => void;
  onArchive?: () => void;
  onDelete?: () => void;
}) {
  const connection = useConnection();
  const project = connection.projects.get(session.projectId);
  const harness = connection.harness(session.harness);
  const { onContextMenu, menu } = useContextMenu(sessionRowMenu(session, connection, { onRename, onArchive, onDelete }));
  return (
    <>
      <div className={"session-row" + (selected ? " selected" : "")} onContextMenu={onContextMenu}>
        <button className="session-row-main" onClick={onOpen} aria-current={selected ? "page" : undefined}>
          <StatusIcon status={session.status} background={!!session.backgroundTasks?.length} unread={!!session.unread} draft={!!session.draft} />
          <span className="session-row-text">
            <span className={"session-title" + (needsYou(session) || session.unread ? " strong" : "")}>{session.title}</span>
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
        {onDelete && <DeleteButton onDelete={onDelete} />}
      </div>
      {menu}
    </>
  );
}

/** A session row's context menu, as the apps': Rename… where its server
 *  renames, Archive… unless it is archived, and Delete…. */
export function sessionRowMenu(
  session: Session,
  connection: ServerConnection,
  { onRename, onArchive, onDelete }: { onRename?: () => void; onArchive?: () => void; onDelete?: () => void },
): MenuEntry[] {
  return [
    !!connection.info?.rename && onRename && { label: "Rename…", icon: Pencil, onSelect: onRename },
    !session.archived && onArchive && { label: "Archive…", icon: Archive, onSelect: onArchive },
    onDelete && { label: "Delete…", icon: Trash2, destructive: true, onSelect: onDelete },
  ];
}

/** A row's button that asks to delete its session. */
export function DeleteButton({ onDelete }: { onDelete: () => void }) {
  return (
    <button className="session-row-action icon-button" onClick={onDelete} title="Delete…" aria-label="Delete">
      <Trash2 size={15} />
    </button>
  );
}

/** Asks before archiving a session, and whether to delete its worktree. */
export function ArchiveConfirm({ connection, session, onClose }: { connection: ServerConnection; session: Session; onClose: () => void }) {
  const worktree = session.workspace.kind === "worktree";
  return (
    <Confirm
      title="Archive session?"
      message={worktree ? `The branch ${session.workspace.branch ?? ""} is kept either way.` : "The agent process is stopped."}
      actions={[
        { label: "Archive", onSelect: () => connection.archive(session.id, false).catch(() => {}) },
        ...(worktree ? [{ label: "Archive and delete worktree", destructive: true, onSelect: () => connection.archive(session.id, true).catch(() => {}) }] : []),
      ]}
      onClose={onClose}
    />
  );
}

/** Asks before deleting a session for good, and whether to delete its worktree too. */
export function DeleteConfirm({ connection, session, onClose }: { connection: ServerConnection; session: Session; onClose: () => void }) {
  const worktree = session.workspace.kind === "worktree";
  return (
    <Confirm
      title="Delete session?"
      message={worktree ? `This can't be undone. The branch ${session.workspace.branch ?? ""} is kept either way.` : "This can't be undone."}
      actions={[
        { label: "Delete", destructive: true, onSelect: () => connection.deleteSession(session.id, false).catch(() => {}) },
        ...(worktree ? [{ label: "Delete with worktree", destructive: true, onSelect: () => connection.deleteSession(session.id, true).catch(() => {}) }] : []),
      ]}
      onClose={onClose}
    />
  );
}

/** Asks for a session's title, the current one chosen so that typing
 *  replaces it; an empty one gives the session back to the server's naming.
 *  Return renames and Esc cancels. It closes at once, and a rename that
 *  fails goes to `onError`. */
export function RenameSession({
  connection,
  session,
  onClose,
  onError,
}: {
  connection: ServerConnection;
  session: Session;
  onClose: () => void;
  onError: (message: string) => void;
}) {
  const [title, setTitle] = useState(session.title);
  const field = useRef<HTMLInputElement>(null);
  // After the dialog's own effect has focused it.
  useEffect(() => field.current?.select(), []);
  const rename = () => {
    if (title.trim() === session.title) return;
    connection.renameSession(session.id, title).catch((e) => onError((e as Error).message));
  };
  return (
    <Confirm title="Rename Session" actions={[{ label: "Rename", onSelect: rename }]} onClose={onClose}>
      <input
        ref={field}
        className="text-field"
        data-autofocus
        aria-label="Title"
        value={title}
        onChange={(e) => setTitle(e.target.value)}
        onKeyDown={(e) => {
          if (e.key !== "Enter" || e.nativeEvent.isComposing) return;
          e.preventDefault();
          onClose();
          rename();
        }}
      />
      <p className="small">Leave it empty to name it automatically.</p>
    </Confirm>
  );
}
