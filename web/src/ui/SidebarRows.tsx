import { Archive, Clock, GitBranch, Hand, Square, TriangleAlert } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { contextFraction, modelName, parseDate, type Session } from "../protocol/models";
import { Spinner } from "./primitives";
import { DeleteButton } from "./SessionRow";
import { needsYou, projectOf, shortAge, type ServerSession } from "./sidebarLayout";

/** A session's row in a sidebar list: a click opens it, and on hover
 *  buttons archive or delete it. */
export function SessionLink({
  item,
  selected,
  onOpen,
  onArchive,
  onDelete,
  className = "",
  children,
}: {
  item: ServerSession;
  selected: boolean;
  onOpen: () => void;
  onArchive?: () => void;
  onDelete?: () => void;
  className?: string;
  children: ReactNode;
}) {
  return (
    <div className={`session-row ${className}` + (selected ? " selected" : "")}>
      <button className="session-row-main" onClick={onOpen} aria-current={selected ? "page" : undefined}>
        {children}
      </button>
      {onArchive && !item.session.archived && (
        <button className="session-row-action icon-button" onClick={onArchive} title="Archive…" aria-label="Archive">
          <Archive size={15} />
        </button>
      )}
      {onDelete && <DeleteButton onDelete={onDelete} />}
    </div>
  );
}

/** A session in the Inbox: its state, title and age, and where it runs. */
export function InboxRow({ item, showsServer }: { item: ServerSession; showsServer: boolean }) {
  const s = item.session;
  return (
    <>
      <StatusGlyph session={s} />
      <span className="session-row-text">
        <span className="row-line">
          <span className={"row-title" + (needsYou(s) || s.unread ? " strong" : "")}>{s.title}</span>
          <ShortAge date={s.updatedAt} />
        </span>
        <SessionPlace item={item} server={showsServer} />
      </span>
    </>
  );
}

/** A session in the Projects layout, under its project: one line. */
export function TreeRow({ item }: { item: ServerSession }) {
  const s = item.session;
  return (
    <>
      <StatusGlyph session={s} />
      <span className={"row-title" + (needsYou(s) || s.unread ? " strong" : "")}>{s.title}</span>
      {s.workspace.kind === "worktree" && (
        <span className="row-branch-glyph" title={s.workspace.branch ?? "Worktree"} aria-label={`Worktree ${s.workspace.branch ?? ""}`}>
          <GitBranch size={11} />
        </span>
      )}
      <ShortAge date={s.updatedAt} />
    </>
  );
}

/** A session in the Columns layout's list, as Mail shows a message: the
 *  title and age, where it runs, and what the agent is doing or last did. */
export function SessionListRow({ item, showsServer }: { item: ServerSession; showsServer: boolean }) {
  const s = item.session;
  return (
    <>
      <StatusGlyph session={s} />
      <span className="session-row-text">
        <span className="row-line">
          <span className={"row-title " + (needsYou(s) || s.unread ? "strong" : "medium")}>{s.title}</span>
          <ShortAge date={s.updatedAt} />
        </span>
        <SessionPlace item={item} server={showsServer} />
        <span className={"row-activity clamp2" + (s.status === "error" ? " c-red" : "")}>{activity(item)}</span>
      </span>
    </>
  );
}

/** What the agent is doing: from the session's state, its error or its
 *  background task, else the agent, model and context it has used. */
function activity({ connection, session: s }: ServerSession): string {
  switch (s.status) {
    case "awaiting_approval":
      return "Waiting for your answer";
    case "error":
      return s.error ?? "Failed";
    case "running":
      return "Working…";
    case "stopped":
      return "Stopped";
    default: {
      const task = s.backgroundTasks?.[0];
      if (task) return `In the background: ${task.description}`;
      const harness = connection.harness(s.harness);
      const fraction = contextFraction(s.context);
      const context = fraction === undefined ? undefined : `${Math.round(fraction * 100)}% context`;
      return [modelName(s, harness) ?? harness?.name ?? s.harness, context].filter(Boolean).join(" · ");
    }
  }
}

/** "Mac Studio › weather-api ⑂ ra/fix-cache": the server (when there is
 *  more than one), the project, and a worktree's branch, which a narrow
 *  line cuts short. */
export function SessionPlace({ item, server }: { item: ServerSession; server: boolean }) {
  const project = projectOf(item);
  const ws = item.session.workspace;
  return (
    <span className="row-place">
      {server && (
        <>
          <span className="nowrap">{item.connection.server.name}</span>
          <span className="c-overlay">›</span>
        </>
      )}
      {project && <span className="nowrap">{project.name}</span>}
      {ws.kind === "worktree" && ws.branch && (
        <span className="branch">
          <GitBranch size={11} /> <span className="branch-name">{ws.branch}</span>
        </span>
      )}
    </span>
  );
}

/** A session's state in the sidebar's denser rows: a ring while idle, a
 *  dot once it is unread, a spinner while it works, and the symbols that
 *  ask for the user. */
export function StatusGlyph({ session }: { session: Session }) {
  let icon;
  let label;
  switch (session.status) {
    case "running":
      icon = <Spinner size={12} />;
      label = "Working";
      break;
    case "awaiting_approval":
      icon = <Hand size={13} strokeWidth={2.25} className="c-yellow" />;
      label = "Needs you";
      break;
    case "error":
      icon = <TriangleAlert size={13} className="c-red" />;
      label = "Failed";
      break;
    case "stopped":
      icon = <Square size={8} fill="currentColor" className="c-overlay" />;
      label = "Stopped";
      break;
    default:
      if (session.backgroundTasks?.length) {
        icon = <Clock size={13} className="c-subtext" />;
        label = "Running in the background";
      } else if (session.unread) {
        icon = <span className="unread-dot" />;
        label = "Unread";
      } else {
        icon = <span className="idle-ring" />;
        label = "Idle";
      }
  }
  return (
    <span className="status-glyph" title={label} role="img" aria-label={label}>
      {icon}
    </span>
  );
}

/** "now", "4m", "3h", "2d", then the date. It keeps itself current. */
export function ShortAge({ date }: { date: string }) {
  const [, tick] = useState(0);
  useEffect(() => {
    const t = setInterval(() => tick((n) => n + 1), 60_000);
    return () => clearInterval(t);
  }, []);
  const ms = parseDate(date);
  return (
    <time className="short-age" dateTime={date} title={new Date(ms).toLocaleString()}>
      {shortAge(ms)}
    </time>
  );
}
