import { Archive, CircleCheck, CirclePlay, Folder, Hand, Inbox, MessagesSquare, type LucideIcon } from "lucide-react";
import { Fragment, type ReactNode } from "react";
import type { Session } from "../protocol/models";
import { useObserved, useObservedAll } from "../store/observable";
import type { ServerConnection } from "../store/serverConnection";
import { useServerStore, useSessionSearch } from "./context";
import { Empty } from "./primitives";
import { navigate, type Route } from "./router";
import {
  ConnectionStatus,
  NewSessionButton,
  ProjectActions,
  SearchResultsList,
  ServerHeader,
  SidebarBar,
  SidebarFoot,
  SidebarSearchField,
  useCollapsed,
  type ServerActions,
} from "./SessionsSidebar";
import {
  allSessions,
  collectionSessions,
  collectionTitle,
  isBusy,
  mostPressing,
  needsYou,
  sameCollection,
  sessionKey,
  type SessionCollection,
} from "./sidebarLayout";
import { SessionLink, SessionListRow, StatusGlyph } from "./SidebarRows";

/** The Columns layout's sidebar, as Mail's: the collections a session can
 *  be in. What needs you, what is working and every session, then each
 *  server's projects, then the archive. The sessions are
 *  a column of their own beside it (`SessionListColumn`), which keeps the
 *  sidebar to two levels, as the HIG asks. */
export function CollectionsSidebar({
  collection,
  setCollection,
  actions,
}: {
  collection: SessionCollection;
  setCollection: (c: SessionCollection) => void;
  actions: ServerActions;
}) {
  const store = useServerStore();
  const connections = store.servers.map((s) => store.connection(s));
  useObservedAll(connections);
  const [collapsed, setCollapsed] = useCollapsed(store.servers);
  const active = allSessions(store, false);
  const archived = allSessions(store, true);

  const link = (c: SessionCollection, title: string, Icon: LucideIcon, count: number, tint = "c-accent", plus?: ReactNode, status?: Session) => {
    const selected = sameCollection(c, collection);
    return (
      <li className={plus ? "row-plus-host" : undefined}>
        <button className={"collection-row" + (selected ? " selected" : "")} onClick={() => setCollection(c)} aria-current={selected ? "page" : undefined}>
          <Icon size={15} strokeWidth={2} className={tint} />
          <span className="collection-row-name">{title}</span>
          {/* A project's: the state of its session that asks most for a look. */}
          {status && <StatusGlyph session={status} />}
          <span className="spacer" />
          {/* A project shows that it has none; the others, as Mail's, nothing. */}
          {(count > 0 || c.kind === "project") && <span className="c-overlay small row-plus-count">{count}</span>}
        </button>
        {plus}
      </li>
    );
  };

  return (
    <div className="server-home">
      <SidebarBar title="Sessions" actions={actions} />
      <div className="list-scroll">
        <ul className="session-list">
          {/* A fixed colour where it means something, as Mail's VIP. */}
          {link({ kind: "needsYou" }, "Needs You", Hand, active.filter((i) => needsYou(i.session)).length, "c-yellow")}
          {link({ kind: "running" }, "Running", CirclePlay, active.filter((i) => isBusy(i.session)).length)}
          {link({ kind: "all" }, "All Sessions", Inbox, active.length)}
        </ul>
        {connections.map((c) => (
          <ServerCollections
            key={c.server.id}
            connection={c}
            expanded={!collapsed.has(c.server.id)}
            setExpanded={(shown) => setCollapsed(c.server.id, !shown)}
            link={link}
            actions={actions}
          />
        ))}
        {archived.length > 0 && <ul className="session-list group-gap">{link({ kind: "archived" }, "Archived", Archive, archived.length)}</ul>}
      </div>
      <SidebarFoot actions={actions} />
    </div>
  );
}

function ServerCollections({
  connection,
  expanded,
  setExpanded,
  link,
  actions,
}: {
  connection: ServerConnection;
  expanded: boolean;
  setExpanded: (shown: boolean) => void;
  link: (c: SessionCollection, title: string, Icon: LucideIcon, count: number, tint?: string, plus?: ReactNode, status?: Session) => ReactNode;
  actions: ServerActions;
}) {
  useObserved(connection);
  const byProject = new Map<string, Session[]>();
  for (const s of connection.activeSessions) {
    const list = byProject.get(s.projectId) ?? [];
    list.push(s);
    byProject.set(s.projectId, list);
  }
  return (
    <section className="server-section" aria-label={connection.server.name}>
      <ServerHeader connection={connection} expanded={expanded} setExpanded={setExpanded} actions={actions} />
      {expanded && (
        <>
          <ConnectionStatus connection={connection} onNew={() => actions.newSession(connection)} />
          <ul className="session-list">
            {connection.sortedProjects.map((p) => {
              const sessions = byProject.get(p.id) ?? [];
              return (
                <Fragment key={p.id}>
                  {link(
                    { kind: "project", serverID: connection.server.id, projectID: p.id },
                    p.name,
                    Folder,
                    sessions.length,
                    undefined,
                    <ProjectActions connection={connection} projectID={p.id} name={p.name} actions={actions} />,
                    mostPressing(sessions),
                  )}
                </Fragment>
              );
            })}
          </ul>
        </>
      )}
    </section>
  );
}

/** The Columns layout's middle column: the sessions in the collection
 *  chosen in the sidebar, three lines each. As in Mail, the search field is
 *  atop it, and the search's results take its place while there are any. */
export function SessionListColumn({ collection, route, actions }: { collection: SessionCollection; route: Route; actions: ServerActions }) {
  const store = useServerStore();
  const connections = store.servers.map((s) => store.connection(s));
  useObservedAll(connections);
  const search = useSessionSearch();
  useObserved(search);
  const items = collectionSessions(collection, store);
  const showsServer = store.servers.length > 1;
  const searching = search.isActive && store.servers.length > 0;
  return (
    <div className="server-home">
      <header className="bar">
        <h1 className="bar-title">
          {collectionTitle(collection, store)}
          {!searching && <span className="bar-count">{items.length === 1 ? "1 session" : `${items.length} sessions`}</span>}
        </h1>
        <NewSessionButton onNew={actions.newSession} collection={collection} />
      </header>
      {store.servers.length > 0 && <SidebarSearchField />}
      <div className="list-scroll">
        {searching ? (
          <SearchResultsList route={route} />
        ) : items.length === 0 ? (
          <EmptyCollection collection={collection} />
        ) : (
          <ul className="session-list">
            {items.map((item) => (
              <li key={sessionKey(item)}>
                <SessionLink
                  item={item}
                  className="three-line"
                  selected={route.serverID === item.connection.server.id && route.sessionID === item.session.id}
                  onOpen={() => navigate({ serverID: item.connection.server.id, sessionID: item.session.id })}
                  onRename={() => actions.renameSession(item.connection, item.session)}
                  onArchive={() => actions.archive(item.connection, item.session)}
                  onDelete={() => actions.delete(item.connection, item.session)}
                >
                  <SessionListRow item={item} showsServer={showsServer} />
                </SessionLink>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

function EmptyCollection({ collection }: { collection: SessionCollection }) {
  switch (collection.kind) {
    case "needsYou":
      return (
        <Empty icon={CircleCheck} title="Nothing needs you">
          <p>Sessions that wait for your answer, or failed, show here.</p>
        </Empty>
      );
    case "running":
      return (
        <Empty icon={CirclePlay} title="Nothing is running">
          <p>Sessions show here while their agent works.</p>
        </Empty>
      );
    case "archived":
      return <Empty icon={Archive} title="Nothing archived" />;
    default:
      return (
        <Empty icon={MessagesSquare} title="No sessions">
          <p>Start one with the New Session button.</p>
        </Empty>
      );
  }
}
