import { Archive, GitBranch, Trash2 } from "lucide-react";
import type { Session } from "../protocol/models";
import type { ServerConnection } from "../store/serverConnection";
import { HarnessBadge, RelativeTime, StatusIcon } from "./components";
import { useConnection } from "./context";
import { Confirm } from "./primitives";
import { needsYou } from "./sidebarLayout";

/** A session in a list: its status, title, agent, project and branch. */
export function SessionRow({
  session,
  selected,
  onOpen,
  onArchive,
  onDelete,
}: {
  session: Session;
  selected: boolean;
  onOpen: () => void;
  onArchive?: () => void;
  onDelete?: () => void;
}) {
  const connection = useConnection();
  const project = connection.projects.get(session.projectId);
  const harness = connection.harness(session.harness);
  return (
    <div className={"session-row" + (selected ? " selected" : "")}>
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
  );
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
