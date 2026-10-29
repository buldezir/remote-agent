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
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.remoteagent.core.store.ConnectionState

/**
 * The Inbox layout: every server's sessions by state. What needs you or is
 * unread comes first, with any server that gave up connecting; then the
 * rest, working or not, the latest first in each. The servers are a section
 * at the end, one line each. Mirrors apple/Shared/Views/InboxSidebar.swift.
 */
@Composable
fun InboxSidebar(servers: List<ServerState>, selection: SessionKey?, onOpenSession: (SessionKey) -> Unit) {
    val active = servers.sessions(archived = false)
    // Whether the open session stood out when it was opened, worked out as it
    // opens, before its view reads it.
    val openedStoodOut = remember(selection) { active.firstOrNull { it.key == selection }?.session?.standsOut == true }
    val groups = inboxGroups(active, selection, openedStoodOut)
    val offline = servers.filter { it.offline }
    val archived = servers.sessions(archived = true)
    val showsServer = servers.size > 1
    var showArchived by rememberSaveable { mutableStateOf(false) }

    // Landing on an archived session, such as one opened before the window
    // widened, shows its row. Only the synced index can tell.
    val selectionArchived = selection != null &&
        servers.firstOrNull { it.id == selection.serverId }?.sessions?.get(selection.sessionId)?.archived == true
    LaunchedEffect(selectionArchived) {
        if (selectionArchived) showArchived = true
    }

    LazyColumn(Modifier.fillMaxSize().testTag("sidebar")) {
        fun rows(list: List<ServerSession>) = items(list, key = { it.id }) { item ->
            SessionLink(item, selected = item.key == selection, onOpen = onOpenSession, modifier = Modifier.animateItem()) {
                InboxRow(item, showsServer)
            }
        }
        if (groups.needsYou.isNotEmpty() || offline.isNotEmpty()) {
            item(key = "h/needsYou") {
                GroupHeader("Needs You", Modifier.animateItem(), groups.needsYou.size + offline.size, palette.yellow)
            }
            items(offline, key = { "offline/${it.id}" }) { OfflineServerRow(it, Modifier.animateItem()) }
            rows(groups.needsYou)
        }
        if (groups.recent.isNotEmpty()) {
            item(key = "h/recent") { GroupHeader("Recent", Modifier.animateItem(), groups.recent.size) }
            rows(groups.recent)
        }
        if (archived.isNotEmpty()) {
            item(key = "archived") {
                ArchivedToggle(archived.size, showArchived, { showArchived = it }, Modifier.animateItem().padding(top = 8.dp))
            }
            if (showArchived) rows(archived)
        }
        if (servers.isNotEmpty()) {
            item(key = "h/servers") { GroupHeader("Servers", Modifier.animateItem()) }
            items(servers, key = { "server/${it.id}" }) { InboxServerRow(it, Modifier.animateItem()) }
        }
    }
}

/** A section's title, with a count; tinted for what needs you. */
@Composable
private fun GroupHeader(title: String, modifier: Modifier = Modifier, count: Int? = null, tint: Color? = null) {
    val style = Type.subheadline.copy(fontWeight = FontWeight.SemiBold)
    Row(
        modifier.fillMaxWidth().background(palette.mantle).padding(start = 16.dp, end = 16.dp, top = 16.dp, bottom = 4.dp)
            .semantics(mergeDescendants = true) {},
        horizontalArrangement = Arrangement.spacedBy(6.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(title, style = style, color = tint ?: palette.subtext)
        if (count != null) {
            val number = style.copy(fontFeatureSettings = "tnum")
            if (tint != null) {
                Text(
                    "$count", style = number, color = palette.onAccent,
                    modifier = Modifier.background(tint, RoundedCornerShape(50)).padding(horizontal = 6.dp),
                )
            } else {
                Text("$count", style = number, color = palette.overlay)
            }
        }
    }
}

/** A server that gave up connecting, among what needs you. A tap tries again; a long press, its actions. */
@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun OfflineServerRow(s: ServerState, modifier: Modifier = Modifier) {
    var menu by remember { mutableStateOf(false) }
    Box(modifier) {
        SidebarRow(Modifier.combinedClickable(onClick = { s.connection.retry() }, onLongClick = { menu = true }), Alignment.Top) {
            Box(Modifier.size(16.dp, 22.dp), contentAlignment = Alignment.Center) {
                Icon(painterResource(Icons.offline), null, Modifier.size(14.dp), tint = palette.red)
            }
            Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
                Text("${s.name} is offline", style = Type.body, color = palette.text, maxLines = 1, overflow = TextOverflow.Ellipsis)
                (s.state as? ConnectionState.Failed)?.let {
                    Text(it.message, style = Type.caption, color = palette.subtext, maxLines = 2, overflow = TextOverflow.Ellipsis)
                }
                Text("Try again", style = Type.caption, color = palette.accent)
            }
        }
        DropdownMenu(menu, { menu = false }, containerColor = palette.mantle) { ServerMenuItems(s) { menu = false } }
    }
}

/**
 * A server in the Inbox's list of them: its name, its sessions or how its
 * connection is doing, and its actions in a ⋯ menu, which a tap or a long
 * press on the row opens too.
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun InboxServerRow(s: ServerState, modifier: Modifier = Modifier) {
    var menu by remember { mutableStateOf(false) }
    Row(
        modifier.fillMaxWidth().background(palette.mantle)
            .combinedClickable(onClick = { menu = true }, onLongClick = { menu = true })
            .padding(start = 16.dp, end = 4.dp),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Icon(painterResource(Icons.server), null, Modifier.size(16.dp), tint = palette.overlay)
        Text(s.name, style = Type.body, color = palette.text, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
        when (s.state) {
            ConnectionState.Connected -> Text("${s.active.size}", style = Type.body.copy(fontFeatureSettings = "tnum"), color = palette.overlay)
            ConnectionState.Connecting -> CircularProgressIndicator(Modifier.size(12.dp), color = palette.subtext, strokeWidth = 1.5.dp)
            is ConnectionState.Failed -> Text(
                "Try again", style = Type.subheadline, color = palette.accent,
                modifier = Modifier.combinedClickable(onClick = { s.connection.retry() }).padding(vertical = 4.dp),
            )
            ConnectionState.Idle -> {}
        }
        ConnectionDot(s.state, size = 7.dp)
        Box {
            IconButton({ menu = true }) { Icon(painterResource(Icons.more), "${s.name} actions", Modifier.size(20.dp), tint = palette.subtext) }
            DropdownMenu(menu, { menu = false }, containerColor = palette.mantle) { ServerMenuItems(s) { menu = false } }
        }
    }
}
