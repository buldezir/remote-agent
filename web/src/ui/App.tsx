import { ChevronRight, ChevronsUpDown, MessagesSquare, Monitor, Pencil, Plus, RefreshCw, Settings, Trash2 } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import type { Session } from "../protocol/models";
import { pairingLinkInHash } from "../protocol/pairing";
import { useObserved, useObservedAll } from "../store/observable";
import { storage } from "../store/storage";
import type { ServerConnection } from "../store/serverConnection";
import type { SavedServer, ServerStore } from "../store/serverStore";
import { AddServer } from "./AddServer";
import { ConnectionDot } from "./components";
import { ConnectionContext, ServerStoreContext, useServerStore } from "./context";
import { NewSession } from "./NewSession";
import { Confirm, Empty, Menu } from "./primitives";
import { navigate, useRoute, useWide, type Route } from "./router";
import { ServerHome } from "./ServerHome";
import { CollectionsSidebar, SessionListColumn } from "./ColumnsSidebar";
import { reconnect, serverSettingsEntry, SessionsSidebar, type ServerActions } from "./SessionsSidebar";
import { ServerSettingsView } from "./ServerSettings";
import { ArchiveConfirm, DeleteConfirm } from "./SessionRow";
import { useSettings } from "./settings";
import {
  addingRecent,
  collectionSessions,
  parseCollection,
  recentSessions,
  revealCollection,
  sameCollection,
  sessionToOpen,
  storedCollection,
  type SessionCollection,
} from "./sidebarLayout";
import { SessionView } from "./SessionView";
import { SettingsView } from "./SettingsView";

/** Sheets over the page. */
type Sheet =
  | { kind: "pair"; link?: string }
  | { kind: "new"; connection: ServerConnection; projectID?: string }
  | { kind: "settings" }
  | { kind: "server"; connection: ServerConnection }
  | { kind: "archive"; connection: ServerConnection; session: Session }
  | { kind: "delete"; connection: ServerConnection; session: Session }
  | { kind: "remove"; server: SavedServer }
  | { kind: "rename"; server: SavedServer };

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

  // A session started empty, as "<server id> <session id>": its prompt takes
  // the keyboard when it opens, and only then.
  const [startedEmpty, setStartedEmpty] = useState<string>();
  const openKey = route.serverID && route.sessionID ? `${route.serverID} ${route.sessionID}` : undefined;
  useEffect(() => {
    if (openKey !== startedEmpty) setStartedEmpty(undefined);
  }, [openKey]);
  const focusPrompt = openKey !== undefined && openKey === startedEmpty;

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
    serverSettings: (c) => setSheet({ kind: "server", connection: c }),
    newSession: (c, projectID) => setSheet({ kind: "new", connection: c, projectID }),
    archive: (c, s) => setSheet({ kind: "archive", connection: c, session: s }),
    delete: (c, s) => setSheet({ kind: "delete", connection: c, session: s }),
  };

  let page;
  if (wide) {
    page = <Split route={route} connection={connection} actions={actions} focusPrompt={focusPrompt} />;
  } else if (connection && route.sessionID) {
    page = (
      <ConnectionContext.Provider value={connection}>
        <SessionView
          key={route.sessionID}
          sessionID={route.sessionID}
          focusPrompt={focusPrompt}
          onBack={() => navigate({ serverID: route.serverID })}
          onDeleted={() => navigate({ serverID: route.serverID }, true)}
        />
      </ConnectionContext.Provider>
    );
  } else if (connection) {
    page = (
      <ConnectionContext.Provider value={connection}>
        <ServerHome
          onOpen={(id) => navigate({ serverID: connection.server.id, sessionID: id })}
          onNew={() => actions.newSession(connection)}
          onBack={() => navigate({})}
          toolbar={<ServerMenu connection={connection} actions={actions} />}
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
      {sheet?.kind === "new" && (
        <ConnectionContext.Provider value={sheet.connection}>
          <NewSession
            projectID={sheet.projectID}
            onClose={() => setSheet(undefined)}
            onCreated={(s, empty) => {
              const serverID = sheet.connection.server.id;
              setStartedEmpty(empty ? `${serverID} ${s.id}` : undefined);
              navigate({ serverID, sessionID: s.id });
            }}
          />
        </ConnectionContext.Provider>
      )}
      {sheet?.kind === "settings" && <SettingsView onClose={() => setSheet(undefined)} />}
      {sheet?.kind === "server" && (
        <ConnectionContext.Provider value={sheet.connection}>
          <ServerSettingsView onClose={() => setSheet(undefined)} />
        </ConnectionContext.Provider>
      )}
      {sheet?.kind === "archive" && <ArchiveConfirm connection={sheet.connection} session={sheet.session} onClose={() => setSheet(undefined)} />}
      {sheet?.kind === "delete" && <DeleteConfirm connection={sheet.connection} session={sheet.session} onClose={() => setSheet(undefined)} />}
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

/** The iPad and Mac layout: every server's sessions in the sidebar, the
 *  chosen session beside them, or in the Columns layout, a list of sessions
 *  between the two. */
function Split({
  route,
  connection,
  actions: given,
  focusPrompt,
}: {
  route: Route;
  connection: ServerConnection | undefined;
  actions: ServerActions;
  focusPrompt: boolean;
}) {
  const store = useServerStore();
  const { sidebarLayout: layout } = useSettings();
  const connections = store.servers.map((s) => store.connection(s));
  useObservedAll(connections);
  const [collection, setCollection] = useCollection();

  // In the Columns layout, a session asked for on the chosen project's
  // server, as from its header, starts in that project.
  const actions: ServerActions =
    layout === "columns" && collection.kind === "project"
      ? {
          ...given,
          newSession: (c, projectID) =>
            given.newSession(c, projectID ?? (c.server.id === collection.serverID && c.projects.has(collection.projectID) ? collection.projectID : undefined)),
        }
      : given;

  // The sessions opened lately, kept in this browser, so that choosing a
  // collection opens the one of its sessions opened last.
  useEffect(() => {
    if (route.serverID && route.sessionID) storage.set(recentKey, addingRecent(storage.get(recentKey), `${route.serverID} ${route.sessionID}`));
  }, [route.serverID, route.sessionID]);

  // Chosen in the sidebar: the session beside it is one of the collection's,
  // the one opened last, else its first.
  const choose = (c: SessionCollection) => {
    if (sameCollection(c, collection)) return;
    setCollection(c);
    const open = sessionToOpen(collectionSessions(c, store), recentSessions(storage.get(recentKey)));
    navigate(open ? { serverID: open.connection.server.id, sessionID: open.session.id } : { serverID: route.serverID });
  };

  // Every server connects; one that was given up on waits for a click.
  useEffect(() => {
    for (const c of connections) if (c.state.kind === "idle") c.start();
  }, [store.servers]);

  // In three columns, a session opened from elsewhere, such as a link or a
  // new session, shows in the list: when the collection doesn't have it,
  // its project or Archived does.
  const open = connection && route.sessionID ? connection.sessions.get(route.sessionID) : undefined;
  useEffect(() => {
    if (layout !== "columns" || !open || !connection) return;
    const shown = collectionSessions(collection, store).some((i) => i.connection === connection && i.session.id === open.id);
    if (!shown) setCollection(revealCollection({ connection, session: open }));
  }, [layout, connection?.server.id, open?.id, open?.archived]);

  // The open session is no longer on the server, as when it was deleted,
  // here or on another device, so nothing is selected, as on iPad and the
  // Mac. Only the synced index can tell: before it, every session looks missing.
  const gone = !!connection && !!route.sessionID && connection.indexSynced && !open;
  useEffect(() => {
    if (gone) navigate({ serverID: route.serverID }, true);
  }, [gone]);

  // The Columns layout's project is no longer there: it was forgotten, here
  // or on another device, or its server was removed. All Sessions shows
  // instead. Only the synced index can tell that it was forgotten.
  const chosen = collection.kind === "project" ? connections.find((c) => c.server.id === collection.serverID) : undefined;
  const projectGone = collection.kind === "project" && (!chosen || (chosen.indexSynced && !chosen.projects.has(collection.projectID)));
  useEffect(() => {
    if (projectGone) setCollection({ kind: "all" });
  }, [projectGone]);

  const detail = (
    <main className="detail">
      {connection && route.sessionID && !gone ? (
        <ConnectionContext.Provider value={connection}>
          {/* A new identity per session, so the draft and the subscription don't carry over. */}
          <SessionView key={connection.server.id + route.sessionID} sessionID={route.sessionID} focusPrompt={focusPrompt} />
        </ConnectionContext.Provider>
      ) : store.servers.length === 0 ? (
        <NoServers onPair={actions.pair} />
      ) : (
        <Empty icon={MessagesSquare} title="No session selected">
          <p>Choose a session in the sidebar, or start a new one.</p>
        </Empty>
      )}
    </main>
  );

  if (layout === "columns") {
    return (
      <div className="split columns">
        <aside className="sidebar">
          <CollectionsSidebar collection={collection} setCollection={choose} actions={actions} />
        </aside>
        <section className="session-column" aria-label="Sessions">
          <SessionListColumn collection={collection} route={route} actions={actions} />
        </section>
        {detail}
      </div>
    );
  }
  return (
    <div className="split">
      <aside className="sidebar">
        <SessionsSidebar layout={layout} route={route} actions={actions} />
      </aside>
      {detail}
    </div>
  );
}

const collectionKey = "sessionCollection";
const recentKey = "recentSessions";

/** The Columns layout's collection, kept across visits. */
function useCollection() {
  const [c, setC] = useState<SessionCollection>(() => parseCollection(storage.get(collectionKey)) ?? { kind: "all" });
  const set = (next: SessionCollection) => {
    storage.set(collectionKey, storedCollection(next));
    setC(next);
  };
  return [c, set] as const;
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

/** The server actions, in the top bar of a server's sessions on a phone. */
function ServerMenu({ connection, actions }: { connection: ServerConnection; actions: ServerActions }) {
  useObserved(connection); // Server Settings… waits for the connection
  const server = connection.server;
  return (
    <Menu
      label={<ChevronsUpDown size={18} />}
      title="Server"
      entries={[
        { label: "Reconnect", icon: RefreshCw, onSelect: () => reconnect(connection) },
        { label: "Rename…", icon: Pencil, onSelect: () => actions.rename(server) },
        serverSettingsEntry(connection, actions),
        { label: `Remove ${server.name}…`, icon: Trash2, destructive: true, onSelect: () => actions.remove(server) },
        "divider",
        { label: "Settings", icon: Settings, onSelect: actions.settings },
      ]}
    />
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
