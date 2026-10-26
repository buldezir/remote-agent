package dev.remoteagent.app.ui

import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxScope
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.Text
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.remoteagent.app.platform.AppSettings
import dev.remoteagent.app.platform.AppSettings.Keys
import dev.remoteagent.app.platform.state
import dev.remoteagent.core.store.ConnectionState
import kotlinx.coroutines.launch

/**
 * The tablet's list pane beside a session: the Inbox or the Projects layout.
 * The Columns layout has its own (ColumnsSidebar.kt). The split view around
 * it shows the dialogs its rows ask for. Mirrors apple/Shared/Views/SessionsSidebar.swift.
 */
@Composable
fun SessionsSidebar(
    layout: SidebarLayout,
    servers: List<ServerState>,
    selection: SessionKey?,
    onOpenSession: (SessionKey) -> Unit,
    onAdd: () -> Unit,
    onSettings: () -> Unit,
) {
    Screen(
        title = { Text("Sessions", style = Type.title) },
        background = palette.mantle,
        actions = { SidebarToolbar(servers, onAdd, onSettings) },
    ) { padding ->
        SidebarRefresh(servers, Modifier.padding(padding)) {
            when (layout) {
                SidebarLayout.PROJECTS -> ProjectsSidebar(servers, selection, onOpenSession)
                else -> InboxSidebar(servers, selection, onOpenSession)
            }
        }
    }
}

/**
 * The sidebar's top bar: a new session, and in a menu, pairing and Settings.
 * In three columns the session list has the new session button instead.
 */
@Composable
fun RowScope.SidebarToolbar(servers: List<ServerState>, onAdd: () -> Unit, onSettings: () -> Unit, newSession: Boolean = true) {
    var menu by remember { mutableStateOf(false) }
    if (newSession) NewSessionButton(servers)
    // In a menu, as on iPad: a + here would read as a new session, as each server's does.
    Box {
        IconButton({ menu = true }) { Icon(painterResource(Icons.more), "More") }
        DropdownMenu(menu, { menu = false }, containerColor = palette.mantle) {
            MenuItem("Pair a server", Icons.add) { menu = false; onAdd() }
            MenuItem("Settings", Icons.settings) { menu = false; onSettings() }
        }
    }
}

/**
 * Starts a session on the server connected; with more than one, a menu offers
 * each. In the Columns layout, a project's collection starts it in that
 * project, on its server.
 */
@Composable
fun NewSessionButton(servers: List<ServerState>, collection: SessionCollection? = null) {
    val actions = LocalSessionActions.current
    val connected = servers.filter { it.state == ConnectionState.Connected }
    var menu by remember { mutableStateOf(false) }
    // Its server alone, if the project was forgotten while chosen.
    fun projectOn(s: ServerState) = (collection as? SessionCollection.Project)?.takeIf { it.serverId == s.id && it.projectId in s.projects }?.projectId
    Box {
        IconButton(
            { if (connected.size == 1) actions.newSession(connected[0].connection, projectOn(connected[0])) else menu = true },
            enabled = connected.isNotEmpty(),
            modifier = Modifier.testTag("new-session"),
        ) {
            Icon(painterResource(Icons.newSession), "New session")
        }
        DropdownMenu(menu, { menu = false }, containerColor = palette.mantle) {
            for (s in connected) MenuItem("On ${s.name}", Icons.server) { menu = false; actions.newSession(s.connection, projectOn(s)) }
        }
    }
}

/** A sidebar list that pulls to refresh every server's connection. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SidebarRefresh(servers: List<ServerState>, modifier: Modifier = Modifier, content: @Composable BoxScope.() -> Unit) {
    val scope = rememberCoroutineScope()
    var refreshing by remember { mutableStateOf(false) }
    PullToRefreshBox(
        isRefreshing = refreshing,
        onRefresh = {
            scope.launch {
                refreshing = true
                refreshConnections(servers)
                refreshing = false
            }
        },
        modifier = modifier.fillMaxSize(),
        content = content,
    )
}

/**
 * While a server isn't connected, why; once it has given up, a way to try
 * again; and once it has no sessions, a way to start one.
 */
fun LazyListScope.connectionStatus(s: ServerState) {
    val key = "${s.id}/status"
    val state = s.state
    when {
        state is ConnectionState.Failed -> item(key = key) {
            SidebarRow(Modifier.animateItem().clickable { s.connection.retry() }, Alignment.Top) {
                Icon(painterResource(Icons.offline), null, Modifier.size(16.dp), tint = palette.red)
                Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
                    Text(
                        state.message, style = Type.subheadline, color = palette.subtext, maxLines = 3, overflow = TextOverflow.Ellipsis,
                        modifier = Modifier.testTag("connection-error"),
                    )
                    Text("Try again", style = Type.subheadline, color = palette.accent)
                }
            }
        }
        state == ConnectionState.Connecting -> item(key = key) {
            SidebarRow(Modifier.animateItem()) {
                Box(Modifier.size(16.dp), contentAlignment = Alignment.Center) {
                    CircularProgressIndicator(Modifier.size(12.dp), color = palette.subtext, strokeWidth = 1.5.dp)
                }
                Text(if (s.indexSynced) "Reconnecting…" else "Connecting…", style = Type.subheadline, color = palette.subtext)
            }
        }
        state == ConnectionState.Connected && s.indexSynced && s.sessions.isEmpty() -> item(key = key) {
            val actions = LocalSessionActions.current
            SidebarRow(Modifier.animateItem().clickable { actions.newSession(s.connection) }) {
                Icon(painterResource(Icons.newSession), null, Modifier.size(16.dp), tint = palette.overlay)
                Text("Start a session", style = Type.subheadline, color = palette.subtext)
            }
        }
    }
}

/** A row of the sidebar that isn't a session, its icon in line with the sessions' status glyphs. */
@Composable
fun SidebarRow(
    modifier: Modifier = Modifier,
    alignment: Alignment.Vertical = Alignment.CenterVertically,
    content: @Composable RowScope.() -> Unit,
) {
    Row(
        modifier.fillMaxWidth().background(palette.mantle).padding(horizontal = 16.dp, vertical = 10.dp),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
        verticalAlignment = alignment,
        content = content,
    )
}

/** Shows or hides the archived sessions below it. */
@Composable
fun ArchivedToggle(count: Int, shown: Boolean, onToggle: (Boolean) -> Unit, modifier: Modifier = Modifier) {
    val angle by animateFloatAsState(if (shown) 90f else 0f, label = "chevron")
    SidebarRow(modifier.clickable { onToggle(!shown) }.semantics { stateDescription = if (shown) "Shown" else "Hidden" }) {
        Icon(painterResource(Icons.archive), null, Modifier.size(16.dp), tint = palette.overlay)
        Text("Archived", style = Type.body, color = palette.overlay, modifier = Modifier.weight(1f))
        Text("$count", style = Type.body.copy(fontFeatureSettings = "tnum"), color = palette.overlay)
        Icon(painterResource(Icons.chevronRight), null, Modifier.size(14.dp).rotate(angle), tint = palette.overlay)
    }
}

/**
 * A server as a section header: its name, how its connection is doing, and
 * while its rows are hidden, how many sessions it has and whether one needs
 * you, else whether one is unread. A tap shows or hides the rows, or tries
 * again once the connection has given up. A + starts a session on it; its
 * other actions are in a ⋯ menu, which a long press on the name opens too.
 * [compact]: a narrow column, where the name gets the +'s room and the menu
 * has the action.
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
fun ServerHeader(s: ServerState, expanded: Boolean, onExpand: (Boolean) -> Unit, modifier: Modifier = Modifier, compact: Boolean = false) {
    val actions = LocalSessionActions.current
    val name = s.name
    val active = s.active
    val waiting = active.any { it.needsYou }
    val unread = !waiting && active.any { it.unread }
    val connected = s.state == ConnectionState.Connected
    var menu by remember { mutableStateOf(false) }
    val description = buildList {
        add(connectionLabel(s.state))
        add(if (expanded) "Expanded" else "Collapsed, ${active.size} sessions")
        if (!expanded && waiting) add("Needs you")
        if (!expanded && unread) add("Unread")
    }.joinToString(", ")

    Row(modifier.fillMaxWidth().background(palette.mantle).padding(start = 8.dp, end = 4.dp), verticalAlignment = Alignment.CenterVertically) {
        Box(
            Modifier.weight(1f).clip(RoundedCornerShape(Metrics.Corner.selection))
                .combinedClickable(
                    role = Role.Button,
                    onClickLabel = when {
                        s.offline -> "Try to connect again"
                        expanded -> "Hide sessions"
                        else -> "Show sessions"
                    },
                    onLongClickLabel = "Actions",
                    onLongClick = { menu = true },
                    onClick = {
                        // Once the connection has given up, a tap tries again.
                        if (s.offline) {
                            s.connection.retry()
                            onExpand(true)
                        } else {
                            onExpand(!expanded)
                        }
                    },
                )
                .semantics {
                    contentDescription = name
                    stateDescription = description
                }
                .testTag("server-header"),
        ) {
            // One element to TalkBack, which reads the name and the state above.
            Row(
                Modifier.clearAndSetSemantics {}.padding(horizontal = 8.dp, vertical = 10.dp),
                horizontalArrangement = Arrangement.spacedBy(6.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                val style = Type.subheadline.copy(fontWeight = FontWeight.SemiBold)
                Text(name, style = style, color = palette.subtext, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
                ConnectionDot(s.state, size = 7.dp)
                if (!expanded && active.isNotEmpty()) {
                    Text("${active.size}", style = style.copy(fontFeatureSettings = "tnum"), color = palette.overlay)
                }
                if (!expanded && waiting) {
                    Icon(painterResource(Icons.approval), null, Modifier.size(13.dp), tint = palette.yellow)
                } else if (!expanded && unread) {
                    UnreadDot(7.dp)
                }
            }
        }
        if (connected && !compact) {
            IconButton({ actions.newSession(s.connection) }) {
                Icon(painterResource(Icons.add), "New session on $name", Modifier.size(20.dp), tint = palette.subtext)
            }
        }
        Box {
            IconButton({ menu = true }) { Icon(painterResource(Icons.more), "$name actions", Modifier.size(20.dp), tint = palette.subtext) }
            DropdownMenu(menu, { menu = false }, containerColor = palette.mantle) { ServerMenuItems(s) { menu = false } }
        }
    }
}

/** A project's actions, for the menu a long press on its row opens: a new session in it, and forgetting it. */
@Composable
fun ProjectMenu(expanded: Boolean, onDismiss: () -> Unit, s: ServerState, projectId: String) {
    val actions = LocalSessionActions.current
    val connected = s.state == ConnectionState.Connected
    DropdownMenu(expanded, onDismiss, containerColor = palette.mantle) {
        MenuItem("New session", Icons.add, enabled = connected) {
            onDismiss()
            actions.newSession(s.connection, projectId)
        }
        HorizontalDivider(color = palette.surface0)
        // The server keeps a project while it has sessions that aren't archived.
        MenuItem("Forget project", Icons.forget, enabled = connected && s.active.none { it.projectId == projectId }) {
            onDismiss()
            s.connection.scope.launch { runCatching { s.connection.removeProject(projectId) } }
        }
    }
}

/** A server's actions, for its menus: a new session, reconnecting, its settings, removing it. */
@Composable
fun ServerMenuItems(s: ServerState, dismiss: () -> Unit) {
    val actions = LocalSessionActions.current
    val connected = s.state == ConnectionState.Connected
    MenuItem("New session on ${s.name}", Icons.add, enabled = connected) { dismiss(); actions.newSession(s.connection) }
    MenuItem("Reconnect", Icons.reconnect) { dismiss(); s.connection.retry() }
    MenuItem("Server Settings…", Icons.serverSettings, enabled = connected) { dismiss(); actions.serverSettings(s.connection) }
    HorizontalDivider(color = palette.surface0)
    MenuItem("Remove ${s.name}…", Icons.delete, color = palette.red) { dismiss(); actions.remove(s.server) }
}

/** The servers whose rows are hidden, which the Projects and Columns sidebars share. */
class CollapsedServers(private val ids: Set<String>, private val settings: AppSettings, private val servers: List<String>) {
    fun expanded(id: String) = id !in ids

    fun setExpanded(id: String, shown: Boolean) {
        settings.update(Keys.collapsedServers, "") { stored ->
            // Forgets servers that were removed.
            toggled(stored, id, present = !shown).split(' ').filter { it in servers }.joinToString(" ")
        }
    }
}

@Composable
fun rememberCollapsedServers(servers: List<ServerState>): CollapsedServers {
    val settings = LocalSettings.current
    // By id, separated by spaces.
    val stored by settings.state(Keys.collapsedServers, "")
    return CollapsedServers(stored.split(' ').filter { it.isNotEmpty() }.toSet(), settings, servers.map { it.id })
}
