import { Archive, ChevronRight, Folder } from "lucide-react";
import { useEffect, useState } from "react";
import { useObserved } from "../store/observable";
import type { ServerConnection } from "../store/serverConnection";
import { storage } from "../store/storage";
import { useServerStore } from "./context";
import { navigate, type Route } from "./router";
import { ConnectionStatus, ProjectActions, ServerHeader, useCollapsed, type ServerActions } from "./SessionsSidebar";
import { mostPressing, needsYouFirst, type ServerSession } from "./sidebarLayout";
import { SessionLink, StatusGlyph, TreeRow } from "./SidebarRows";

/** The Projects layout: each server a section, its projects folding open to
 *  their sessions, a line each, what needs you first. The projects come in
 *  order of their latest session, those without any by name after them; a
 *  server's archived sessions fold up last. */
export function ProjectsSidebar({ route, actions }: { route: Route; actions: ServerActions }) {
  const store = useServerStore();
  useObserved(store);
  const [collapsed, setCollapsed] = useCollapsed(store.servers);
  const [folded, setFolded] = useFoldedProjects();
  return (
    <>
      {store.servers.map((s) => (
        <ServerProjects
          key={s.id}
          connection={store.connection(s)}
          route={route}
          expanded={!collapsed.has(s.id)}
          setExpanded={(shown) => setCollapsed(s.id, !shown)}
          folded={folded}
          setFolded={setFolded}
          actions={actions}
        />
      ))}
    </>
  );
}

const foldedKey = "collapsedProjects";

/** The projects folded shut, as "<server id>/<project id>", kept across visits. */
function useFoldedProjects() {
  const [keys, setKeys] = useState(() => new Set((storage.get(foldedKey) ?? "").split(" ").filter(Boolean)));
  const set = (key: string, folded: boolean) => {
    const next = new Set([...keys].filter((k) => k !== key));
    if (folded) next.add(key);
    storage.set(foldedKey, [...next].join(" "));
    setKeys(next);
  };
  return [keys, set] as const;
}

interface ProjectGroup {
  projectID: string;
  name: string;
  items: ServerSession[];
}

/** The server's projects: those with sessions, the one worked in last
 *  first, then the others by name. */
function groups(connection: ServerConnection): ProjectGroup[] {
  const byProject = new Map<string, ServerSession[]>();
  for (const session of connection.activeSessions) {
    const list = byProject.get(session.projectId) ?? [];
    list.push({ connection, session });
    byProject.set(session.projectId, list);
  }
  const worked = [...byProject].map(([id, items]) => ({
    projectID: id,
    name: connection.projects.get(id)?.name ?? "Project",
    items: needsYouFirst(items),
  }));
  const idle = connection.sortedProjects.filter((p) => !byProject.has(p.id)).map((p) => ({ projectID: p.id, name: p.name, items: [] }));
  return [...worked, ...idle];
}

function ServerProjects({
  connection,
  route,
  expanded,
  setExpanded,
  folded,
  setFolded,
  actions,
}: {
  connection: ServerConnection;
  route: Route;
  expanded: boolean;
  setExpanded: (shown: boolean) => void;
  folded: Set<string>;
  setFolded: (key: string, folded: boolean) => void;
  actions: ServerActions;
}) {
  useObserved(connection);
  const [showArchived, setShowArchived] = useState(false);
  const id = connection.server.id;
  const archived = connection.archivedSessions.map((session) => ({ connection, session }));

  // Landing on a session, such as one from a link, unfolds its server, its
  // project or the archive to show its row.
  const selected = route.serverID === id && route.sessionID ? connection.sessions.get(route.sessionID) : undefined;
  const place = selected && `${selected.projectId} ${selected.archived}`;
  useEffect(() => {
    if (!selected) return;
    if (!expanded) setExpanded(true);
    if (selected.archived) setShowArchived(true);
    else if (folded.has(`${id}/${selected.projectId}`)) setFolded(`${id}/${selected.projectId}`, false);
  }, [place]);

  const rows = (items: ServerSession[]) => (
    <ul className="session-list">
      {items.map((item) => (
        <li key={item.session.id}>
          <SessionLink
            item={item}
            className="tree"
            selected={route.serverID === id && route.sessionID === item.session.id}
            onOpen={() => navigate({ serverID: id, sessionID: item.session.id })}
            onRename={() => actions.renameSession(connection, item.session)}
            onArchive={() => actions.archive(connection, item.session)}
            onDelete={() => actions.delete(connection, item.session)}
          >
            <TreeRow item={item} />
          </SessionLink>
        </li>
      ))}
    </ul>
  );

  return (
    <section className="server-section" aria-label={connection.server.name}>
      <ServerHeader connection={connection} expanded={expanded} setExpanded={setExpanded} actions={actions} />
      {expanded && (
        <>
          <ConnectionStatus connection={connection} onNew={() => actions.newSession(connection)} />
          {groups(connection).map((g) => {
            const key = `${id}/${g.projectID}`;
            const open = !folded.has(key);
            // Without sessions, it has nothing to fold open.
            const empty = g.items.length === 0;
            const status = mostPressing(g.items.map((i) => i.session));
            return (
              <div key={g.projectID}>
                <div className="row-plus-host project-row-host">
                  <button className="project-row" onClick={() => !empty && setFolded(key, open)} aria-expanded={empty ? undefined : open}>
                    <ChevronRight size={13} className={"chevron c-overlay" + (open ? " open" : "") + (empty ? " invisible" : "")} />
                    <Folder size={14} className="c-accent" />
                    <span className="project-row-name">{g.name}</span>
                    {status && <StatusGlyph session={status} />}
                    <span className="spacer" />
                    <span className="c-overlay small row-plus-count">{g.items.length}</span>
                  </button>
                  <ProjectActions connection={connection} projectID={g.projectID} name={g.name} actions={actions} />
                </div>
                {open && rows(g.items)}
              </div>
            );
          })}
          {archived.length > 0 && (
            <>
              <button className="project-row c-overlay" onClick={() => setShowArchived(!showArchived)} aria-expanded={showArchived}>
                <ChevronRight size={13} className={"chevron" + (showArchived ? " open" : "")} />
                <Archive size={14} />
                <span className="project-row-name">Archived</span>
                <span className="spacer" />
                <span className="small">{archived.length}</span>
              </button>
              {showArchived && rows(archived)}
            </>
          )}
        </>
      )}
    </section>
  );
}
