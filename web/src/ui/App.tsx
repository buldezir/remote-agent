import { ChevronRight, ChevronsUpDown, MessagesSquare, Monitor, Pencil, Plus, RefreshCw, Settings, Trash2, MessageCircleQuestion } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { pairingLinkInHash } from "../protocol/pairing";
import { useObserved } from "../store/observable";
import type { ServerConnection } from "../store/serverConnection";
import type { SavedServer, ServerStore } from "../store/serverStore";
import { AddServer } from "./AddServer";
import { ConnectionDot, connectionLabel } from "./components";
import { ConnectionContext, ServerStoreContext, useServerStore } from "./context";
import { NewSession } from "./NewSession";
import { Confirm, Empty, Menu } from "./primitives";
import { navigate, useRoute, useWide, type Route } from "./router";
import { ServerHome } from "./ServerHome";
import { SessionView } from "./SessionView";
import { SettingsView } from "./SettingsView";

/** Sheets over the page. */
type Sheet = { kind: "pair"; link?: string } | { kind: "new" } | { kind: "settings" } | { kind: "remove"; server: SavedServer } | { kind: "rename"; server: SavedServer };

export function App({ store }: { store: ServerStore }) {
  return (
    <ServerStoreContext.Provider value={store}>
      <Root />
    </ServerStoreContext.Provider>
  );
}

function Root() {
  const store = useServerStore();
  useObserved(store);
  const route = useRoute();
  const wide = useWide();
  const [sheet, setSheet] = useState<Sheet>();

  // A link from `rad pair --web` opens the pairing sheet.
  useEffect(() => {
    const open = () => {
      const link = pairingLinkInHash(location.hash);
      if (link === undefined) return;
      navigate({}, true);
      setSheet({ kind: "pair", link });
    };
    open();
    window.addEventListener("hashchange", open);
    return () => window.removeEventListener("hashchange", open);
  }, []);

  // Back on the page: reconnect at once rather than at the next retry.
  useEffect(() => {
    const visible = () => document.visibilityState === "visible" && store.wake();
    document.addEventListener("visibilitychange", visible);
    window.addEventListener("online", visible);
    return () => {
      document.removeEventListener("visibilitychange", visible);
      window.removeEventListener("online", visible);
    };
  }, [store]);

  const server = store.server(route.serverID);
  const connection = server ? store.connection(server) : undefined;

  // A wide window always shows a server; the first, if none was chosen.
  useEffect(() => {
    if (wide && !server && store.servers.length > 0) navigate({ serverID: store.servers[0].id }, true);
  }, [wide, server, store.servers.length]);

  // A phone with one server opens it at launch, as the iPhone app does.
  const openedOnce = useRef(false);
  useEffect(() => {
    if (!openedOnce.current && !wide && !route.serverID && store.servers.length === 1) navigate({ serverID: store.servers[0].id });
    openedOnce.current = true;
  }, []);

  const actions: ServerActions = {
    pair: () => setSheet({ kind: "pair" }),
    settings: () => setSheet({ kind: "settings" }),
    remove: (s) => setSheet({ kind: "remove", server: s }),
    rename: (s) => setSheet({ kind: "rename", server: s }),
    newSession: () => setSheet({ kind: "new" }),
  };

  let page;
  if (wide) {
    page = <Split route={route} connection={connection} actions={actions} />;
  } else if (connection && route.sessionID) {
    page = (
      <ConnectionContext.Provider value={connection}>
        <SessionView key={route.sessionID} sessionID={route.sessionID} onBack={() => navigate({ serverID: route.serverID })} />
      </ConnectionContext.Provider>
    );
  } else if (connection) {
    page = (
      <ConnectionContext.Provider value={connection}>
        <ServerHome
          onOpen={(id) => navigate({ serverID: connection.server.id, sessionID: id })}
          onNew={actions.newSession}
          onBack={() => navigate({})}
          toolbar={<ServerMenuButton connection={connection} actions={actions} compact />}
        />
      </ConnectionContext.Provider>
    );
  } else {
    page = <ServerList actions={actions} />;
  }

  return (
    <>
      {page}
      {sheet?.kind === "pair" && (
        <AddServer initialLink={sheet.link} onClose={() => setSheet(undefined)} onPaired={(s) => navigate({ serverID: s.id })} />
      )}
      {sheet?.kind === "new" && connection && (
        <ConnectionContext.Provider value={connection}>
          <NewSession onClose={() => setSheet(undefined)} onCreated={(s) => navigate({ serverID: connection.server.id, sessionID: s.id })} />
        </ConnectionContext.Provider>
      )}
      {sheet?.kind === "settings" && <SettingsView onClose={() => setSheet(undefined)} />}
      {sheet?.kind === "remove" && (
        <Confirm
          title={`Remove ${sheet.server.name}?`}
          message={
            <>
              This device is unpaired from the server. Run <code>rad pair</code> on the computer to add it again.
            </>
          }
          actions={[
            {
              label: "Remove",
              destructive: true,
              onSelect: () => {
                if (route.serverID === sheet.server.id) navigate({}, true);
                store.remove(sheet.server);
              },
            },
          ]}
          onClose={() => setSheet(undefined)}
        />
      )}
      {sheet?.kind === "rename" && <RenameServer server={sheet.server} onClose={() => setSheet(undefined)} />}
    </>
  );
}

interface ServerActions {
  pair: () => void;
  settings: () => void;
  remove: (s: SavedServer) => void;
  rename: (s: SavedServer) => void;
  newSession: () => void;
}

/** The iPad and Mac layout: one server's sessions in the sidebar, the chosen
 *  session beside them. The menu at the foot of the sidebar switches servers. */
function Split({ route, connection, actions }: { route: Route; connection: ServerConnection | undefined; actions: ServerActions }) {
  const store = useServerStore();
  useObserved(connection);
  return (
    <div className="split">
      <aside className="sidebar">
        {connection ? (
          <ConnectionContext.Provider value={connection}>
            <ServerHome
              key={connection.server.id}
              selected={route.sessionID}
              onOpen={(id) => navigate({ serverID: connection.server.id, sessionID: id })}
              onNew={actions.newSession}
              footer={<ServerMenuButton connection={connection} actions={actions} />}
            />
          </ConnectionContext.Provider>
        ) : (
          <div className="server-home">
            <header className="bar">
              <h1 className="bar-title">Servers</h1>
              <button className="icon-button" onClick={actions.settings} title="Settings" aria-label="Settings">
                <Settings size={18} />
              </button>
              <button className="icon-button" onClick={actions.pair} title="Pair a server" aria-label="Pair a server">
                <Plus />
              </button>
            </header>
          </div>
        )}
      </aside>
      <main className="detail">
        {connection && route.sessionID ? (
          connection.indexSynced && !connection.sessions.has(route.sessionID) ? (
            <Empty icon={MessageCircleQuestion} title="Session not found">
              <p>It is no longer on {connection.server.name}.</p>
            </Empty>
          ) : (
            <ConnectionContext.Provider value={connection}>
              {/* A new identity per session, so the draft and the subscription don't carry over. */}
              <SessionView key={connection.server.id + route.sessionID} sessionID={route.sessionID} />
            </ConnectionContext.Provider>
          )
        ) : store.servers.length === 0 ? (
          <NoServers onPair={actions.pair} />
        ) : (
          <Empty icon={MessagesSquare} title="No session selected">
            <p>Choose a session in the sidebar, or start a new one.</p>
          </Empty>
        )}
      </main>
    </div>
  );
}

function NoServers({ onPair }: { onPair: () => void }) {
  return (
    <Empty icon={Monitor} title="No servers">
      <p>
        Run <code>rad serve</code> on your computer, then <code>rad pair --web {location.origin}</code>, and open the link it prints.
      </p>
      <button className="button prominent" onClick={onPair}>
        Pair a server
      </button>
    </Empty>
  );
}

/** The server's name and status, and a menu with the server actions. At
 *  the foot of the sidebar, or (compact) a button in a phone's top bar. */
function ServerMenuButton({ connection, actions, compact = false }: { connection: ServerConnection; actions: ServerActions; compact?: boolean }) {
  const store = useServerStore();
  useObserved(connection);
  const server = connection.server;
  const entries = [
    ...(compact
      ? []
      : [
          {
            section: "Servers",
            items: store.servers.map((s) => ({ label: s.name, checked: s.id === server.id, onSelect: () => navigate({ serverID: s.id }) })),
          },
          { label: "Pair a server", icon: Plus, onSelect: actions.pair },
          "divider" as const,
        ]),
    {
      label: "Reconnect",
      icon: RefreshCw,
      onSelect: () => {
        // As pulling to refresh in the apps: also look for newly installed agents.
        connection.retry();
        void connection.refreshHarnesses(true);
      },
    },
    { label: "Rename…", icon: Pencil, onSelect: () => actions.rename(server) },
    { label: `Remove ${server.name}…`, icon: Trash2, destructive: true, onSelect: () => actions.remove(server) },
    "divider" as const,
    { label: "Settings", icon: Settings, onSelect: actions.settings },
  ];
  if (compact) return <Menu label={<ChevronsUpDown size={18} />} title="Server" entries={entries} />;
  return (
    <div className="sidebar-foot" title={connectionLabel(connection.state)}>
      <Menu
        className="server-menu-button"
        align="start"
        title={`${server.name}, ${connectionLabel(connection.state)}`}
        label={
          <>
            <ConnectionDot state={connection.state} />
            <span className="server-menu-name">{server.name}</span>
            <ChevronsUpDown size={14} className="c-subtext" />
          </>
        }
        entries={entries}
      />
    </div>
  );
}

/** The iPhone's server list: each server with its status. */
function ServerList({ actions }: { actions: ServerActions }) {
  const store = useServerStore();
  return (
    <div className="server-home">
      <header className="bar">
        <h1 className="bar-title">Servers</h1>
        <button className="icon-button" onClick={actions.settings} title="Settings" aria-label="Settings">
          <Settings size={18} />
        </button>
        <button className="icon-button" onClick={actions.pair} title="Pair a server" aria-label="Pair a server">
          <Plus />
        </button>
      </header>
      <div className="list-scroll">
        {store.servers.length === 0 ? (
          <NoServers onPair={actions.pair} />
        ) : (
          <ul className="session-list">
            {store.servers.map((s) => (
              <li key={s.id}>
                <ServerRow server={s} onRemove={() => actions.remove(s)} />
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function ServerRow({ server, onRemove }: { server: SavedServer; onRemove: () => void }) {
  const store = useServerStore();
  const connection = store.connection(server);
  useObserved(connection);
  useEffect(() => connection.start(), [connection]);
  return (
    <div className="session-row">
      <button className="session-row-main" onClick={() => navigate({ serverID: server.id })}>
        <ConnectionDot state={connection.state} />
        <span className="session-row-text">
          <span className="session-title">{server.name}</span>
          <span className="session-meta mono">{server.urls[0]?.replace(/^https?:\/\//, "")}</span>
        </span>
        <ChevronRight size={16} className="c-overlay" />
      </button>
      <button className="session-row-action icon-button" onClick={onRemove} title="Remove server" aria-label="Remove server">
        <Trash2 size={15} />
      </button>
    </div>
  );
}

function RenameServer({ server, onClose }: { server: SavedServer; onClose: () => void }) {
  const store = useServerStore();
  const [name, setName] = useState(server.name);
  return (
    <Confirm
      title="Rename server"
      actions={[{ label: "Rename", onSelect: () => name.trim() && store.rename(server, name.trim()) }]}
      onClose={onClose}
    >
      <input className="text-field" data-autofocus value={name} onChange={(e) => setName(e.target.value)} />
    </Confirm>
  );
}
