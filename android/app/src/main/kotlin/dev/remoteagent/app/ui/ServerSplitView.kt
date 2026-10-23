package dev.remoteagent.app.ui

import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.window.core.layout.WindowSizeClass
import dev.remoteagent.app.platform.AppSettings.Keys
import dev.remoteagent.app.platform.state
import dev.remoteagent.core.store.ConnectionState
import dev.remoteagent.core.store.SavedServer

/**
 * The tablet's list pane, for every layout: every server's sessions in the
 * sidebar, or in the Columns layout, the collections and a list of sessions
 * beside them. The open session is the detail pane beside it (AppNavigation).
 * It shows the dialogs the sidebars ask for. Mirrors apple/Shared/Views/ServerSplitView.swift.
 */
@Composable
fun ServerSplitView(
    layout: SidebarLayout,
    selection: SessionKey?,
    onOpenSession: (SessionKey) -> Unit,
    onCloseSession: () -> Unit,
    onAdd: () -> Unit,
    onSettings: () -> Unit,
    onRemoved: (SavedServer) -> Unit,
) {
    val settings = LocalSettings.current
    val servers = serverStates()
    var newSessionOn by rememberSaveable { mutableStateOf<String?>(null) }
    var newSessionIn by rememberSaveable { mutableStateOf<String?>(null) }
    var archiving by remember { mutableStateOf<SessionRequest?>(null) }
    var deleting by remember { mutableStateOf<SessionRequest?>(null) }
    var removing by remember { mutableStateOf<SavedServer?>(null) }
    var settingsOf by rememberSaveable { mutableStateOf<String?>(null) }

    // The Columns layout's collection, as `SessionCollection.stored`; null until it has loaded.
    val stored by remember { settings.flow(Keys.sessionCollection, "") }.collectAsStateWithLifecycle<String?>(null)
    // The one chosen since, at once: the stored one follows later.
    var chosen by remember { mutableStateOf<SessionCollection?>(null) }
    val collection = chosen ?: stored?.let { SessionCollection.parse(it) } ?: SessionCollection.All
    fun show(c: SessionCollection) {
        chosen = c
        settings.put(Keys.sessionCollection, c.stored)
    }
    // In the Columns layout, a session asked for on the chosen project's
    // server, as from its header, starts in that project.
    val chosenProject by rememberUpdatedState((collection as? SessionCollection.Project)?.takeIf { layout == SidebarLayout.COLUMNS })

    val actions = remember {
        SessionActions(
            startSession = { c, projectId ->
                newSessionOn = c.server.value.id
                newSessionIn = projectId ?: chosenProject?.takeIf { it.serverId == c.server.value.id && it.projectId in c.projects.value }?.projectId
            },
            archive = { archiving = it },
            delete = { deleting = it },
            remove = { removing = it },
            serverSettings = { settingsOf = it.server.value.id },
        )
    }

    // Every server connects; one that was given up on waits for a tap. Keyed
    // on the connections, since pairing a server again replaces its own.
    val connections = servers.map { it.connection }
    LaunchedEffect(connections) {
        for (c in connections) if (c.state.value == ConnectionState.Idle) c.start()
    }

    // The sessions opened lately, kept on this device, so that choosing a
    // collection opens the one of its sessions opened last.
    val recent by settings.state(Keys.recentSessions, "")
    LaunchedEffect(selection) {
        selection?.let { key -> settings.update(Keys.recentSessions, "") { RecentSessions.adding(key, it) } }
    }

    // Chosen in the sidebar: the session beside it is one of the collection's,
    // the one opened last, else its first.
    fun choose(c: SessionCollection) {
        if (c == collection) return
        show(c)
        c.sessionToOpen(servers, RecentSessions.keys(recent))?.let(onOpenSession) ?: onCloseSession()
    }

    // In three columns, a session opened from elsewhere, such as a new one or
    // one open before the window widened, shows in the list: when the
    // collection doesn't have it, its project or Archived does.
    val open = selection?.let { k -> servers.firstOrNull { it.id == k.serverId }?.let { s -> s.sessions[k.sessionId]?.let { ServerSession(s, it) } } }
    val reveal = if (layout == SidebarLayout.COLUMNS && stored != null && open != null) "$selection ${open.session.archived}" else null
    LaunchedEffect(reveal) {
        if (reveal == null || open == null) return@LaunchedEffect
        if (collection.sessions(servers).none { it.key == selection }) {
            show(open.collection)
        }
    }

    // The Columns layout's project is no longer there: it was forgotten, here
    // or on another device, or its server was removed. All Sessions shows
    // instead. Only the synced index can tell that it was forgotten.
    val projectGone = (collection as? SessionCollection.Project)?.let { p ->
        servers.firstOrNull { it.id == p.serverId }?.let { it.indexSynced && p.projectId !in it.projects } ?: true
    } == true
    LaunchedEffect(projectGone) {
        if (projectGone) show(SessionCollection.All)
    }

    CompositionLocalProvider(LocalSessionActions provides actions) {
        when (layout) {
            SidebarLayout.COLUMNS -> ColumnsSidebar(
                servers, collection, onChoose = { choose(it) },
                selection = selection, onOpenSession = onOpenSession, onAdd = onAdd, onSettings = onSettings,
            )
            else -> SessionsSidebar(layout, servers, selection, onOpenSession, onAdd, onSettings)
        }
    }

    ArchiveSessionDialog(archiving, onDismiss = { archiving = null })
    DeleteSessionDialog(deleting, onDismiss = { deleting = null })
    val removed = removing
    RemoveServerDialog(removed, onDismiss = { removing = null }, onRemove = { removed?.let(onRemoved) })
    servers.firstOrNull { it.id == settingsOf }?.let { s ->
        ServerSettingsDialog(s.connection, onDismiss = { settingsOf = null })
    }
    servers.firstOrNull { it.id == newSessionOn }?.let { s ->
        NewSessionDialog(s.connection, newSessionIn, onDismiss = { newSessionOn = null }, onCreated = { session ->
            newSessionOn = null
            onOpenSession(SessionKey(s.id, session.id))
        })
    }
}

/**
 * The list pane's width, as the directive's default pane width: the default
 * for a sidebar, wider for the Columns layout's two columns. The session
 * beside it asks for as much, so the list takes up to half the window.
 */
fun listPaneWidth(layout: SidebarLayout, window: WindowSizeClass): Dp? = when {
    layout != SidebarLayout.COLUMNS -> null
    window.isWidthAtLeastBreakpoint(WindowSizeClass.WIDTH_DP_LARGE_LOWER_BOUND) -> 640.dp
    else -> 540.dp
}
