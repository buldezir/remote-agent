import { Archive, ChevronRight, Ellipsis, Monitor, Plus, WifiOff } from "lucide-react";
import { useEffect, useState } from "react";
import { useObservedAll } from "../store/observable";
import type { ServerConnection } from "../store/serverConnection";
import { ConnectionDot } from "./components";
import { useServerStore } from "./context";
import { Menu, Spinner } from "./primitives";
import { navigate, type Route } from "./router";
import { serverMenuEntries, type ServerActions } from "./SessionsSidebar";
import { allSessions, inboxGroups, sessionKey, standsOut, type OpenedSession, type ServerSession } from "./sidebarLayout";
import { InboxRow, SessionLink } from "./SidebarRows";

/** The Inbox layout: every server's sessions by state. What needs you or is
 *  unread comes first, with any server that gave up connecting; then the
 *  rest, working or not, the latest first in each. The servers are a section
 *  at the end, one line each, rather than at the foot, where a window can
 *  hide them. */
export function InboxSidebar({ route, actions }: { route: Route; actions: ServerActions }) {
  const store = useServerStore();
  const connections = store.servers.map((s) => store.connection(s));
  useObservedAll(connections);
  const [showArchived, setShowArchived] = useState(false);
  const [opened, setOpened] = useState<OpenedSession>({ stoodOut: false });

  const active = allSessions(store, false);
  const offline = connections.filter((c) => c.state.kind === "failed");
  // Whether the open session stood out when it was opened, worked out as it
  // opens, before its view reads it.
  const openKey = route.serverID && route.sessionID ? route.serverID + " " + route.sessionID : undefined;
  let open = opened;
  if (open.key !== openKey) {
    const item = active.find((i) => sessionKey(i) === openKey);
    open = { key: openKey, stoodOut: !!item && standsOut(item.session) };
    setOpened(open);
  }
  const { needsYou: waiting, recent } = inboxGroups(active, open);
  const archived = allSessions(store, true);
  const showsServer = store.servers.length > 1;

  // Landing on an archived session, such as one from a link, shows its row.
  const selected = route.serverID && route.sessionID ? store.server(route.serverID) : undefined;
  const selectedArchived = !!selected && store.connection(selected).sessions.get(route.sessionID!)?.archived === true;
  useEffect(() => {
    if (selectedArchived) setShowArchived(true);
  }, [selectedArchived]);

  const rows = (items: ServerSession[]) => (
    <ul className="session-list">
      {items.map((item) => (
        <li key={sessionKey(item)}>
          <SessionLink
            item={item}
            className="compact"
            selected={route.serverID === item.connection.server.id && route.sessionID === item.session.id}
            onOpen={() => navigate({ serverID: item.connection.server.id, sessionID: item.session.id })}
            onArchive={() => actions.archive(item.connection, item.session)}
            onDelete={() => actions.delete(item.connection, item.session)}
          >
            <InboxRow item={item} showsServer={showsServer} />
          </SessionLink>
        </li>
      ))}
    </ul>
  );

  return (
    <>
      {(waiting.length > 0 || offline.length > 0) && (
        <section aria-label="Needs You">
          <GroupHeader title="Needs You" count={waiting.length + offline.length} tint />
          {offline.map((c) => (
            <OfflineServerRow key={c.server.id} connection={c} />
          ))}
          {rows(waiting)}
        </section>
      )}
      {recent.length > 0 && (
        <section aria-label="Recent">
          <GroupHeader title="Recent" count={recent.length} />
          {rows(recent)}
        </section>
      )}
      {active.length === 0 && offline.length === 0 && connections.some((c) => c.state.kind === "connected" && c.indexSynced) && (
        <StartSession connections={connections} onNew={actions.newSession} />
      )}
      {archived.length > 0 && (
        <section aria-label="Archived" className="group-gap">
          <button className="sidebar-status" onClick={() => setShowArchived(!showArchived)} aria-expanded={showArchived}>
            <span className="status-glyph">
              <Archive size={13} className="c-overlay" />
            </span>
            <span className="c-overlay">Archived</span>
            <span className="spacer" />
            <span className="c-overlay">{archived.length}</span>
            <ChevronRight size={14} className={"chevron c-overlay" + (showArchived ? " open" : "")} />
          </button>
          {showArchived && rows(archived)}
        </section>
      )}
      {connections.length > 0 && (
        <section aria-label="Servers">
          <GroupHeader title="Servers" />
          <ul className="session-list">
            {connections.map((c) => (
              <li key={c.server.id}>
                <InboxServerRow connection={c} actions={actions} />
              </li>
            ))}
          </ul>
        </section>
      )}
    </>
  );
}

/** A section's title, with a count; tinted for what needs you. */
function GroupHeader({ title, count, tint = false }: { title: string; count?: number; tint?: boolean }) {
  return (
    <h2 className={"list-header" + (tint ? " c-yellow" : "")}>
      {title}
      {count !== undefined && <span className={tint ? "count-pill" : "c-overlay"}>{count}</span>}
    </h2>
  );
}

/** With no sessions anywhere, a way to start one. */
function StartSession({ connections, onNew }: { connections: ServerConnection[]; onNew: (c: ServerConnection) => void }) {
  const first = connections.find((c) => c.state.kind === "connected");
  if (!first) return null;
  return (
    <button className="sidebar-status" onClick={() => onNew(first)}>
      <span className="status-glyph">
        <Plus size={13} className="c-overlay" />
      </span>
      <span className="c-subtext">Start a session</span>
    </button>
  );
}

/** A server that gave up connecting, among what needs you. A click tries again. */
function OfflineServerRow({ connection }: { connection: ServerConnection }) {
  const state = connection.state;
  return (
    <button className="sidebar-status" onClick={() => connection.retry()}>
      <span className="status-glyph">
        <WifiOff size={13} className="c-red" />
      </span>
      <span className="sidebar-status-text">
        <span>{connection.server.name} is offline</span>
        {state.kind === "failed" && <span className="c-subtext small clamp2">{state.message}</span>}
        <span className="c-accent small">Try again</span>
      </span>
    </button>
  );
}

/** A server in the Inbox's list of them: its name, its sessions or how its
 *  connection is doing, and its actions in a ⋯ menu; on hover, a + in the
 *  count's place starts a session on it. */
function InboxServerRow({ connection, actions }: { connection: ServerConnection; actions: ServerActions }) {
  const server = connection.server;
  const state = connection.state;
  return (
    <div className="server-row">
      <span className="status-glyph">
        <Monitor size={13} className="c-overlay" />
      </span>
      <span className="server-row-name">{server.name}</span>
      {state.kind === "connected" && (
        <button
          className="icon-button server-row-plus"
          onClick={() => actions.newSession(connection)}
          title={`New session on ${server.name}`}
          aria-label={`New session on ${server.name}`}
        >
          <Plus size={15} />
        </button>
      )}
      {state.kind === "connected" && <span className="c-overlay server-row-count">{connection.activeSessions.length}</span>}
      {state.kind === "connecting" && <Spinner size={12} />}
      {state.kind === "failed" && (
        <button className="link" onClick={() => connection.retry()}>
          Try again
        </button>
      )}
      <ConnectionDot state={state} />
      <Menu className="icon-button server-row-action" title={`${server.name} actions`} label={<Ellipsis size={15} />} entries={serverMenuEntries(connection, actions)} />
    </div>
  );
}
