package dev.remoteagent.app.ui

import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import dev.remoteagent.core.protocol.Session
import dev.remoteagent.core.protocol.SessionStatus
import dev.remoteagent.core.store.ConnectionState
import dev.remoteagent.core.store.SavedServer
import dev.remoteagent.core.store.ServerConnection
import dev.remoteagent.core.store.activeSessions
import dev.remoteagent.core.store.archivedSessions
import kotlinx.coroutines.launch

/**
 * A server's sessions on a phone, pushed from the server list in the Servers
 * tab. It pushes sessions in turn. A tablet has every server's in its sidebar
 * (SessionsSidebar) instead. Mirrors apple/iOS/ServerHomeView.swift.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ServerHomeView(
    connection: ServerConnection,
    onOpenSession: (String) -> Unit,
    onStartedSession: (String, empty: Boolean) -> Unit,
    onBack: () -> Unit,
    onSettings: () -> Unit,
    onRemoved: () -> Unit,
) {
    val server by connection.server.collectAsStateWithLifecycle()
    val state by connection.state.collectAsStateWithLifecycle()
    val indexSynced by connection.indexSynced.collectAsStateWithLifecycle()
    val sessions by connection.sessions.collectAsStateWithLifecycle()
    val projects by connection.projects.collectAsStateWithLifecycle()
    val harnesses by connection.harnesses.collectAsStateWithLifecycle()
    val scope = rememberCoroutineScope()
    var menu by remember { mutableStateOf(false) }
    var removing by remember { mutableStateOf<SavedServer?>(null) }
    var showNew by rememberSaveable { mutableStateOf(false) }
    var archiving by remember { mutableStateOf<SessionRequest?>(null) }
    var deleting by remember { mutableStateOf<SessionRequest?>(null) }
    var refreshing by remember { mutableStateOf(false) }
    var showArchived by rememberSaveable { mutableStateOf(false) }
    var showSettings by rememberSaveable { mutableStateOf(false) }

    // Opening a server it gave up on tries again.
    LaunchedEffect(connection) { connection.start() }

    Screen(
        title = { Text(server.name, style = Type.title, maxLines = 1, overflow = TextOverflow.Ellipsis) },
        onBack = onBack,
        actions = {
            IconButton({ showNew = true }, enabled = state == ConnectionState.Connected, modifier = Modifier.testTag("new-session")) {
                Icon(painterResource(Icons.newSession), "New session")
            }
            Box {
                IconButton({ menu = true }) { Icon(painterResource(Icons.more), "More") }
                DropdownMenu(menu, { menu = false }, containerColor = palette.mantle) {
                    MenuItem("Reconnect", Icons.reconnect) { menu = false; connection.retry() }
                    MenuItem("Server Settings…", Icons.serverSettings, enabled = state == ConnectionState.Connected) {
                        menu = false
                        showSettings = true
                    }
                    MenuItem("Settings", Icons.settings) { menu = false; onSettings() }
                    MenuItem("Remove server", Icons.delete, color = palette.red) { menu = false; removing = server }
                }
            }
        },
        bottomBar = LocalTabBar.current,
    ) { padding ->
        PullToRefreshBox(
            isRefreshing = refreshing,
            onRefresh = {
                scope.launch {
                    refreshing = true
                    if (state != ConnectionState.Connected) connection.retry()
                    connection.refreshHarnesses(force = true)
                    refreshing = false
                }
            },
            modifier = Modifier.fillMaxSize().padding(padding),
        ) {
            val active = sessions.activeSessions
            val archived = sessions.archivedSessions
            LazyColumn(Modifier.fillMaxSize()) {
                banner(state, indexSynced, onRetry = { connection.retry() })
                val attention = active.filter { it.status == SessionStatus.AWAITING_APPROVAL }
                fun rows(list: List<Session>) = items(list, key = { it.id }) { s ->
                    SessionRowItem(
                        s, connection, projects[s.projectId], harnesses?.firstOrNull { it.id == s.harness }, background = palette.base,
                        onClick = { onOpenSession(s.id) },
                        onArchive = { archiving = SessionRequest(s, connection) },
                        onDelete = { deleting = SessionRequest(s, connection) },
                    )
                }
                if (attention.isNotEmpty()) {
                    item(key = "h-attention") { SectionHeader("Needs you") }
                    rows(attention)
                }
                if (active.isNotEmpty() && attention.size < active.size) {
                    item(key = "h-sessions") { SectionHeader("Sessions") }
                    rows(active.filter { it.status != SessionStatus.AWAITING_APPROVAL })
                }
                if (archived.isNotEmpty()) {
                    item(key = "h-archived") {
                        val angle by animateFloatAsState(if (showArchived) 90f else 0f, label = "chevron")
                        Row(
                            Modifier.fillMaxWidth().clickable { showArchived = !showArchived }.padding(start = 16.dp, end = 16.dp, top = 18.dp, bottom = 6.dp),
                            verticalAlignment = Alignment.CenterVertically,
                        ) {
                            Text("Archived", style = Type.footnote.copy(fontWeight = FontWeight.SemiBold), color = palette.subtext)
                            Text("  ${archived.size}", style = Type.footnote, color = palette.overlay)
                            Spacer(Modifier.weight(1f))
                            Icon(painterResource(Icons.chevronRight), if (showArchived) "Hide archived" else "Show archived", Modifier.size(18.dp).rotate(angle), tint = palette.subtext)
                        }
                    }
                    if (showArchived) rows(archived)
                }
            }
            if (indexSynced && sessions.isEmpty()) {
                EmptyState(Icons.newSession, "No sessions yet", description = "Start an agent in one of your projects.", action = "New session" to { showNew = true })
            } else if (!indexSynced && state == ConnectionState.Connecting) {
                Loading("Connecting…")
            }
        }
    }

    ArchiveSessionDialog(archiving, onDismiss = { archiving = null })
    DeleteSessionDialog(deleting, onDismiss = { deleting = null })
    RemoveServerDialog(removing, onDismiss = { removing = null }, onRemove = onRemoved)
    if (showSettings) ServerSettingsDialog(connection, onDismiss = { showSettings = false })
    if (showNew) {
        NewSessionDialog(connection, onDismiss = { showNew = false }, onCreated = { s, empty ->
            showNew = false
            onStartedSession(s.id, empty)
        })
    }
}

private fun LazyListScope.banner(state: ConnectionState, indexSynced: Boolean, onRetry: () -> Unit) {
    when {
        state is ConnectionState.Failed -> item(key = "banner") {
            Column(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 8.dp)) {
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                    Icon(painterResource(Icons.offline), null, tint = palette.red, modifier = Modifier.size(18.dp))
                    Text(state.message, style = Type.body, color = palette.red, modifier = Modifier.testTag("connection-error"))
                }
                TextButton(onRetry) { Text("Try again", color = palette.accent) }
            }
        }
        state == ConnectionState.Connecting && indexSynced -> item(key = "banner") {
            Row(Modifier.padding(horizontal = 16.dp, vertical = 10.dp), horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                Icon(painterResource(Icons.reconnect), null, tint = palette.subtext, modifier = Modifier.size(18.dp))
                Text("Reconnecting…", style = Type.body, color = palette.subtext)
            }
        }
    }
}

@Composable
private fun SectionHeader(text: String) {
    Text(
        text, style = Type.footnote.copy(fontWeight = FontWeight.SemiBold), color = palette.subtext,
        modifier = Modifier.padding(start = 16.dp, end = 16.dp, top = 18.dp, bottom = 6.dp),
    )
}
