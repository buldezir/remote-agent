package dev.remoteagent.app.ui

import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.unit.dp
import dev.remoteagent.core.store.ConnectionState
import dev.remoteagent.core.store.SavedServer
import java.net.URI

/** The paired servers. Mirrors apple/iOS/ServersView.swift. */
@Composable
fun ServersView(
    onOpen: (SavedServer) -> Unit,
    onStartedSession: (SessionKey, empty: Boolean) -> Unit,
    onAdd: () -> Unit,
    onSettings: () -> Unit,
) {
    val servers = serverStates()
    var removing by remember { mutableStateOf<SavedServer?>(null) }
    var newSessionOn by rememberSaveable { mutableStateOf<String?>(null) }
    var settingsOf by rememberSaveable { mutableStateOf<String?>(null) }
    val actions = remember {
        SessionActions(
            startSession = { c, _ -> newSessionOn = c.server.value.id },
            remove = { removing = it },
            serverSettings = { settingsOf = it.server.value.id },
        )
    }

    // Every server connects, so that its menu can open its settings and its
    // dot says how it is; one that was given up on waits for a tap. Keyed on
    // the connections, since pairing a server again replaces its own.
    val connections = servers.map { it.connection }
    LaunchedEffect(connections) {
        for (c in connections) if (c.state.value == ConnectionState.Idle) c.start()
    }

    Screen(
        title = { Text("Servers", style = Type.title) },
        actions = {
            IconButton(onSettings) { Icon(painterResource(Icons.settings), "Settings") }
            IconButton(onAdd, Modifier.testTag("add-server")) { Icon(painterResource(Icons.add), "Add") }
        },
    ) { padding ->
        if (servers.isEmpty()) {
            EmptyState(
                Icons.server, "No servers", description = "Run `rad serve` on your computer, then `rad pair` and scan the QR code.",
                action = "Pair a server" to onAdd, modifier = Modifier.padding(padding),
            )
        } else {
            CompositionLocalProvider(LocalSessionActions provides actions) {
                LazyColumn(Modifier.fillMaxSize(), contentPadding = padding) {
                    items(servers, key = { it.id }) { s ->
                        SwipeAction("Remove", Icons.delete, palette.red, onSwipe = { removing = s.server }) {
                            ServerRow(s, onClick = { onOpen(s.server) })
                        }
                    }
                }
            }
        }
    }
    RemoveServerDialog(removing, onDismiss = { removing = null })
    servers.firstOrNull { it.id == settingsOf }?.let { s ->
        ServerSettingsDialog(s.connection, onDismiss = { settingsOf = null })
    }
    servers.firstOrNull { it.id == newSessionOn }?.let { s ->
        NewSessionDialog(s.connection, onDismiss = { newSessionOn = null }, onCreated = { session, empty ->
            newSessionOn = null
            onStartedSession(SessionKey(s.id, session.id), empty)
        })
    }
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun ServerRow(s: ServerState, onClick: () -> Unit) {
    var menu by remember { mutableStateOf(false) }
    Box {
        Row(
            Modifier.fillMaxWidth().background(palette.base).combinedClickable(onClick = onClick, onLongClick = { menu = true })
                .padding(Metrics.rowInsets).testTag("server-row"),
            horizontalArrangement = Arrangement.spacedBy(12.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Icon(painterResource(Icons.server), null, Modifier.size(28.dp), tint = palette.accent)
            Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
                Text(s.name, style = Type.headline, color = palette.text)
                Text(s.server.urls.firstOrNull()?.let { runCatching { URI(it).host }.getOrNull() } ?: "", style = Type.caption, color = palette.subtext)
            }
            ConnectionDot(s.state)
        }
        DropdownMenu(menu, { menu = false }, containerColor = palette.mantle) { ServerMenuItems(s) { menu = false } }
    }
}
