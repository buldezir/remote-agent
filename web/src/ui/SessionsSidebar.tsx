import { CircleX, Ellipsis, FolderMinus, Hand, Pencil, Plus, RefreshCw, Search, ServerCog, Settings, SquarePen, Trash2, WifiOff } from "lucide-react";
import { useEffect, useRef, useState, type ReactNode } from "react";
import type { Session } from "../protocol/models";
import { useObserved, useObservedAll } from "../store/observable";
import type { ServerConnection } from "../store/serverConnection";
import type { SavedServer } from "../store/serverStore";
import { storage } from "../store/storage";
import { ConnectionDot, connectionLabel } from "./components";
import { useServerStore, useSessionSearch } from "./context";
import { InboxSidebar } from "./InboxSidebar";
import { Menu, Spinner, type MenuEntry } from "./primitives";
import { ProjectsSidebar } from "./ProjectsSidebar";
import { openSession, type Route } from "./router";
import { needsYou, type SessionCollection, type SidebarLayout } from "./sidebarLayout";
import { SearchResultRow, SessionLink } from "./SidebarRows";

export interface ServerActions {
  pair: () => void;
  settings: () => void;
  /** A new session on the server, in one of its projects when given. */
  newSession: (c: ServerConnection, projectID?: string) => void;
  archive: (c: ServerConnection, s: Session) => void;
  delete: (c: ServerConnection, s: Session) => void;
  /** A new session that goes on from one of the session's turns. */
  fork: (c: ServerConnection, s: Session) => void;
  rename: (s: SavedServer) => void;
  remove: (s: SavedServer) => void;
  /** The server's own settings: rad's updates, its agents, the system agent. */
  serverSettings: (c: ServerConnection) => void;
}

/** Tries to connect again, and, as pulling to refresh in the apps, looks for newly installed agents. */
export function reconnect(c: ServerConnection) {
  c.retry();
  void c.refreshHarnesses(true);
}

/** The sidebar beside a session in a wide window, as on iPad and the Mac:
 *  the Inbox or the Projects layout. The Columns layout has its own
 *  (`CollectionsSidebar`). As a narrow window's Inbox tab (`tab`), it is
 *  the Inbox without the servers, which the Servers tab lists. */
export function SessionsSidebar({
  layout,
  route,
  actions,
  tab = false,
  placeholder,
}: {
  layout: SidebarLayout;
  route: Route;
  actions: ServerActions;
  tab?: boolean;
  /** In place of the list, as a phone's with no servers. */
  placeholder?: ReactNode;
}) {
  const store = useServerStore();
  const search = useSessionSearch();
  useObserved(search);
  return (
    <div className="server-home">
      <SidebarBar title={tab ? "Inbox" : "Sessions"} actions={actions}>
        <NewSessionButton onNew={actions.newSession} />
      </SidebarBar>
      {!placeholder && store.servers.length > 0 && <SidebarSearchField />}
      <div className="list-scroll">
        {placeholder ||
          (search.isActive && store.servers.length > 0 ? (
            <SearchResultsList route={route} tab={tab} />
          ) : layout === "projects" && !tab ? (
            <ProjectsSidebar route={route} actions={actions} />
          ) : (
            <InboxSidebar route={route} actions={actions} tab={tab} />
          ))}
      </div>
      <SidebarFoot actions={actions} />
    </div>
  );
}

/** Whether a key is the one that focuses the search of sessions: ⇧⌘F, or
 *  Ctrl+Shift+F outside the terminal, where it's the shell's. */
export function isSearchShortcut(e: KeyboardEvent): boolean {
  if (e.key.toLowerCase() !== "f" || !e.shiftKey || e.altKey) return false;
  const target = e.target as Element | null;
  if (target?.closest?.("dialog")) return false;
  return e.metaKey || (e.ctrlKey && !target?.closest?.(".terminal"));
}

let focusFieldOnMount = false;

/** The search field that shows next takes the keyboard, as the Inbox tab's
 *  does when the shortcut brings it up. */
export function focusSearchFieldOnMount() {
  focusFieldOnMount = true;
}

/** The search of every server's sessions, atop a sidebar (in the Columns
 *  layout, atop the list of sessions): ⇧⌘F or Ctrl+Shift+F focuses it, and
 *  Esc in it clears it. While it has a query, or the keyboard, Include
 *  archived shows under it. */
export function SidebarSearchField() {
  const search = useSessionSearch();
  useObserved(search);
  const field = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (focusFieldOnMount) {
      focusFieldOnMount = false;
      field.current?.focus();
    }
    const key = (e: KeyboardEvent) => {
      if (!isSearchShortcut(e)) return;
      e.preventDefault();
      field.current?.focus();
      field.current?.select();
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, []);
  return (
    <div className={"sidebar-search" + (search.query ? " has-query" : "")} role="search">
      <div className="search-field">
        <Search size={14} className="c-overlay" aria-hidden />
        <input
          ref={field}
          type="search"
          placeholder="Search sessions"
          aria-label="Search sessions"
          value={search.query}
          onChange={(e) => search.setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key !== "Escape") return;
            e.preventDefault();
            search.setQuery("");
            e.currentTarget.blur();
          }}
        />
        {search.query && (
          <button
            type="button"
            className="search-clear"
            onClick={() => {
              search.setQuery("");
              field.current?.focus();
            }}
            title="Clear"
            aria-label="Clear search"
          >
            <CircleX size={14} />
          </button>
        )}
      </div>
      {/* It keeps the field's keyboard, so that it stays to be clicked. */}
      <label className="search-option" onMouseDown={(e) => e.preventDefault()}>
        <input type="checkbox" checked={search.includeArchived} onChange={(e) => search.setIncludeArchived(e.target.checked)} />
        Include archived
      </label>
    </div>
  );
}

/** The sessions the search found, in place of a sidebar's rows, then how
 *  the search went. A result opens its session at the item that matched,
 *  in a narrow window in the Inbox tab (`tab`), and the results stay. */
export function SearchResultsList({ route, tab = false }: { route: Route; tab?: boolean }) {
  const store = useServerStore();
  const search = useSessionSearch();
  useObserved(search);
  const connections = store.servers.map((s) => store.connection(s));
  useObservedAll(connections);
  // A server that connects, or goes, is searched or left out.
  const serversKey = search.serversKey();
  useEffect(() => search.serversChanged(), [search, serversKey]);
  const byID = new Map(connections.map((c) => [c.server.id, c]));
  const showsServer = store.servers.length > 1;
  const rows = search.hits.flatMap((hit) => {
    const connection = byID.get(hit.serverId);
    if (!connection) return [];
    // The index's copy is the newer one, read or renamed since; one the
    // synced index no longer has was deleted.
    const session = connection.sessions.get(hit.result.session.id) ?? (connection.indexSynced ? undefined : hit.result.session);
    return session ? [{ hit, item: { connection, session } }] : [];
  });
  return (
    <>
      {rows.length > 0 && (
        <ul className="session-list" aria-label="Search results">
          {rows.map(({ hit, item }) => (
            <li key={hit.id}>
              <SessionLink
                item={item}
                className="search-result"
                selected={route.serverID === hit.serverId && route.sessionID === item.session.id}
                onOpen={() => openSession({ serverID: hit.serverId, sessionID: item.session.id, tab: tab ? "inbox" : undefined }, hit.result.itemId)}
              >
                <SearchResultRow item={item} result={hit.result} showsServer={showsServer} />
              </SessionLink>
            </li>
          ))}
        </ul>
      )}
      <div className="search-status" aria-live="polite">
        {search.isSearching && (
          <span className="search-progress">
            <Spinner size={13} />
          </span>
        )}
        {/* Only once a server answered: failures say why instead. */}
        {!search.isSearching && rows.length === 0 && search.answered > 0 && <p>No sessions match “{search.query.trim()}”.</p>}
        {search.noServers && <p>No server is connected.</p>}
        {search.more && <p>More sessions match. Add words to narrow it down.</p>}
        {search.indexing.map((name) => (
          <p key={"indexing " + name}>{name} is still indexing older sessions, so some may be missing.</p>
        ))}
        {search.failures.map((f) => (
          <p key={"failed " + f.server}>
            Couldn't search {f.server}: {f.message}
          </p>
        ))}
        {search.unsupported.map((name) => (
          <p key={"old " + name}>Update rad on {name} to search its sessions.</p>
        ))}
      </div>
    </>
  );
}

/** A sidebar's top bar: its title, Settings, and what else it holds. */
export function SidebarBar({ title, actions, children }: { title: string; actions: ServerActions; children?: ReactNode }) {
  return (
    <header className="bar">
      <h1 className="bar-title">{title}</h1>
      <button className="icon-button" onClick={actions.settings} title="Settings" aria-label="Settings">
        <Settings size={18} />
      </button>
      {children}
    </header>
  );
}

/** Pairing a server, at the foot of the sidebar. */
export function SidebarFoot({ actions }: { actions: ServerActions }) {
  return (
    <div className="sidebar-foot">
      <button className="sidebar-foot-button" onClick={actions.pair}>
        <Plus size={16} /> Pair a server
      </button>
    </div>
  );
}

const collapsedKey = "collapsedServers";

/** The servers whose rows are hidden, by id, kept across visits; the
 *  Projects and Columns sidebars share them. */
export function useCollapsed(servers: SavedServer[]) {
  const [ids, setIds] = useState(() => new Set((storage.get(collapsedKey) ?? "").split(" ").filter(Boolean)));
  const set = (id: string, collapsed: boolean) => {
    // Forgets servers that were removed.
    const next = new Set([...ids].filter((x) => x !== id && servers.some((s) => s.id === x)));
    if (collapsed) next.add(id);
    storage.set(collapsedKey, [...next].join(" "));
    setIds(next);
  };
  return [ids, set] as const;
}

/** Starts a session on the server connected, or, with several, on the one
 *  picked from a menu. */
/** Starts a session; with more than one server connected, a menu offers
 *  each. In the Columns layout, a project's collection starts it in that
 *  project, on its server. */
export function NewSessionButton({ onNew, collection }: { onNew: (c: ServerConnection, projectID?: string) => void; collection?: SessionCollection }) {
  const store = useServerStore();
  const connections = store.servers.map((s) => store.connection(s));
  useObservedAll(connections);
  const connected = connections.filter((c) => c.state.kind === "connected");
  // Its server alone, if the project was forgotten while chosen.
  const projectOn = (c: ServerConnection) =>
    collection?.kind === "project" && collection.serverID === c.server.id && c.projects.has(collection.projectID) ? collection.projectID : undefined;
  const projectName = (c: ServerConnection) => c.projects.get(projectOn(c) ?? "")?.name;
  if (connected.length > 1) {
    const inProject = connected.find(projectName);
    return (
      <Menu
        title={inProject ? `New session in ${projectName(inProject)}` : "New session"}
        label={<SquarePen size={19} />}
        entries={connected.map((c) => ({ label: `On ${c.server.name}`, onSelect: () => onNew(c, projectOn(c)) }))}
      />
    );
  }
  const only = connected[0];
  const project = only && projectName(only);
  return (
    <button
      className="icon-button"
      onClick={() => only && onNew(only, projectOn(only))}
      disabled={!only}
      title={project ? `New session in ${project}` : only ? `New session on ${only.server.name}` : "New session"}
      aria-label="New session"
    >
      <SquarePen size={19} />
    </button>
  );
}

/** A project's + and ⋯, beside its row: a new session in it, and its
 *  actions, which forget it too. On hover they take the place of the row's
 *  count (`row-plus-count`). */
export function ProjectActions({ connection, projectID, name, actions }: { connection: ServerConnection; projectID: string; name: string; actions: ServerActions }) {
  if (connection.state.kind !== "connected") return null;
  // The server keeps a project while it has sessions that aren't archived.
  const hasSessions = connection.activeSessions.some((s) => s.projectId === projectID);
  return (
    <>
      <button
        className="icon-button row-plus"
        onClick={() => actions.newSession(connection, projectID)}
        title={`New session in ${name}`}
        aria-label={`New session in ${name}`}
      >
        <Plus size={15} />
      </button>
      <Menu
        className="icon-button row-plus"
        title={`${name} actions`}
        label={<Ellipsis size={15} />}
        entries={[
          { label: `New session in ${name}`, icon: SquarePen, onSelect: () => actions.newSession(connection, projectID) },
          "divider",
          { label: "Forget project", icon: FolderMinus, disabled: hasSessions, onSelect: () => connection.removeProject(projectID).catch(() => {}) },
        ]}
      />
    </>
  );
}

/** A server's actions, for its menus. */
export function serverMenuEntries(connection: ServerConnection, actions: ServerActions): MenuEntry[] {
  const server = connection.server;
  return [
    { label: `New session on ${server.name}`, icon: SquarePen, disabled: connection.state.kind !== "connected", onSelect: () => actions.newSession(connection) },
    { label: "Reconnect", icon: RefreshCw, onSelect: () => reconnect(connection) },
    { label: "Rename…", icon: Pencil, onSelect: () => actions.rename(server) },
    serverSettingsEntry(connection, actions),
    "divider",
    { label: `Remove ${server.name}…`, icon: Trash2, destructive: true, onSelect: () => actions.remove(server) },
  ];
}

/** Opens the server's settings, which need it connected. */
export function serverSettingsEntry(connection: ServerConnection, actions: ServerActions): MenuEntry {
  return { label: "Server Settings…", icon: ServerCog, disabled: connection.state.kind !== "connected", onSelect: () => actions.serverSettings(connection) };
}

/** A server as a section header: its name and connection, and while its
 *  rows are hidden, how many sessions it has and whether one needs you, or
 *  else whether one is unread. A click shows or hides them, or tries again
 *  once the connection has given up. On hover, a + starts a session on the
 *  server and ⋯ has its other actions. */
export function ServerHeader({
  connection,
  expanded,
  setExpanded,
  actions,
}: {
  connection: ServerConnection;
  expanded: boolean;
  setExpanded: (shown: boolean) => void;
  actions: ServerActions;
}) {
  useObserved(connection);
  const server = connection.server;
  const state = connection.state;
  const offline = state.kind === "failed";
  const connected = state.kind === "connected";
  const active = connection.activeSessions;
  const waiting = active.some(needsYou);
  const unread = !waiting && active.some((s) => s.unread);

  const activate = () => {
    if (offline) {
      connection.retry();
      setExpanded(true);
    } else {
      setExpanded(!expanded);
    }
  };
  const label = [server.name, connectionLabel(state)];
  if (!expanded) label.push(`${active.length} sessions`);
  if (!expanded && waiting) label.push("needs you");
  if (!expanded && unread) label.push("unread");

  return (
    <div className="server-header">
      <button
        className="server-header-main"
        onClick={activate}
        aria-expanded={expanded}
        aria-label={label.join(", ")}
        title={offline ? `Try to connect to ${server.name} again` : connectionLabel(state)}
      >
        <span className="server-header-name">{server.name}</span>
        <ConnectionDot state={state} />
        {!expanded && active.length > 0 && <span className="c-overlay">{active.length}</span>}
        {!expanded && waiting && <Hand size={12} strokeWidth={2.25} className="c-yellow" />}
        {!expanded && unread && <span className="unread-dot" />}
      </button>
      {connected && (
        <button
          className="icon-button server-header-action"
          onClick={() => actions.newSession(connection)}
          title={`New session on ${server.name}`}
          aria-label={`New session on ${server.name}`}
        >
          <Plus size={15} />
        </button>
      )}
      <Menu
        className="icon-button server-header-action"
        title={`${server.name} actions`}
        label={<Ellipsis size={15} />}
        entries={serverMenuEntries(connection, actions)}
      />
    </div>
  );
}

/** While the server isn't connected, why; once it has given up, a way to
 *  try again. Connected without sessions, a way to start one. */
export function ConnectionStatus({ connection, onNew }: { connection: ServerConnection; onNew: () => void }) {
  useObserved(connection);
  const state = connection.state;
  switch (state.kind) {
    case "failed":
      return (
        <button className="sidebar-status" onClick={() => connection.retry()}>
          <span className="status-glyph">
            <WifiOff size={13} className="c-red" />
          </span>
          <span className="sidebar-status-text">
            <span className="c-subtext clamp3">{state.message}</span>
            <span className="c-accent">Try again</span>
          </span>
        </button>
      );
    case "connecting":
      return (
        <div className="sidebar-status">
          <span className="status-glyph">
            <Spinner size={12} />
          </span>
          <span className="c-subtext">{connection.indexSynced ? "Reconnecting…" : "Connecting…"}</span>
        </div>
      );
    case "connected":
      if (!connection.indexSynced || connection.sessions.size > 0) return null;
      return (
        <button className="sidebar-status" onClick={onNew}>
          <span className="status-glyph">
            <SquarePen size={13} className="c-overlay" />
          </span>
          <span className="c-subtext">Start a session</span>
        </button>
      );
    default:
      return null;
  }
}
