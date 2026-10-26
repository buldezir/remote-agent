package dev.remoteagent.app.ui

import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.remoteagent.core.protocol.HarnessInfo
import dev.remoteagent.core.protocol.Project
import dev.remoteagent.core.protocol.Session
import dev.remoteagent.core.protocol.Workspace
import dev.remoteagent.core.store.SavedServer
import dev.remoteagent.core.store.ServerConnection
import kotlinx.coroutines.launch

// A session's row, for the phone's list of a server's sessions, and the
// dialogs that go with it, which the tablet's sidebars (SidebarRows.kt) use
// too. Mirrors apple/Shared/Views/SessionRow.swift.

@Composable
fun SessionRow(session: Session, project: Project?, harness: HarnessInfo?, modifier: Modifier = Modifier) {
    Row(modifier, horizontalArrangement = Arrangement.spacedBy(10.dp), verticalAlignment = Alignment.Top) {
        Box(Modifier.padding(top = 2.dp)) { StatusIcon(session.status, background = session.backgroundTasks.isNotEmpty(), unread = session.unread) }
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            Text(
                session.title, style = Type.body.copy(fontWeight = if (session.standsOut) FontWeight.SemiBold else FontWeight.Medium),
                color = palette.text, maxLines = 2, overflow = TextOverflow.Ellipsis,
            )
            // The time sits on this line, so the title gets the full width.
            Row(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.CenterVertically) {
                HarnessBadge(session.harness, harness?.name)
                project?.let { Text(it.name, style = Type.caption, color = palette.subtext, maxLines = 1) }
                if (session.workspace.kind == Workspace.Kind.WORKTREE) {
                    session.workspace.branch?.let { b ->
                        Row(Modifier.weight(1f, fill = false), horizontalArrangement = Arrangement.spacedBy(2.dp), verticalAlignment = Alignment.CenterVertically) {
                            Icon(painterResource(Icons.branch), null, Modifier.size(12.dp), tint = palette.subtext)
                            Text(b, style = Type.caption, color = palette.subtext, maxLines = 1, overflow = TextOverflow.MiddleEllipsis)
                        }
                    }
                }
                Spacer(Modifier.weight(1f).width(4.dp))
                RelativeTime(session.updatedAt)
            }
        }
    }
}

/**
 * A session in a list: a tap opens it, a long press offers to archive or
 * delete it, and a swipe archives it, or deletes one already archived.
 * The selected one is drawn as a surface inset from the edges, on the list's
 * [background], which also covers the swipe's colour behind the row.
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
fun SessionRowItem(
    session: Session,
    project: Project?,
    harness: HarnessInfo?,
    background: Color,
    onClick: () -> Unit,
    onArchive: () -> Unit,
    onDelete: () -> Unit,
    modifier: Modifier = Modifier,
    selected: Boolean = false,
) {
    var menu by remember { mutableStateOf(false) }
    Box(modifier) {
        SessionSwipe(session.archived, onArchive, onDelete) {
            Box(Modifier.background(background)) {
                Box(
                    Modifier.fillMaxWidth().padding(horizontal = 8.dp)
                        .background(if (selected) palette.surface0 else Color.Transparent, RoundedCornerShape(Metrics.Corner.selection))
                        .combinedClickable(onClick = onClick, onLongClick = { menu = true })
                        .testTag("session-row"),
                ) {
                    SessionRow(session, project, harness, Modifier.padding(horizontal = 8.dp, vertical = 10.dp))
                }
                SessionRowMenu(menu, { menu = false }, session.archived, onArchive, onDelete)
            }
        }
    }
}

/** A row's swipe: Archive, or for a session already archived, Delete. */
@Composable
fun SessionSwipe(archived: Boolean, onArchive: () -> Unit, onDelete: () -> Unit, content: @Composable () -> Unit) {
    if (archived) {
        SwipeAction("Delete", Icons.delete, palette.red, onSwipe = onDelete, content = content)
    } else {
        SwipeAction("Archive", Icons.archive, palette.blue, onSwipe = onArchive, content = content)
    }
}

/** A row's long-press menu: Archive while it isn't archived, and Delete. */
@Composable
fun SessionRowMenu(expanded: Boolean, onDismiss: () -> Unit, archived: Boolean, onArchive: () -> Unit, onDelete: () -> Unit) {
    DropdownMenu(expanded, onDismiss, containerColor = palette.mantle) {
        if (!archived) MenuItem("Archive…", Icons.archive) { onDismiss(); onArchive() }
        MenuItem("Delete…", Icons.delete, color = palette.red) { onDismiss(); onDelete() }
    }
}

/** A session to archive or delete, and the server it is on. */
data class SessionRequest(val session: Session, val connection: ServerConnection)

/** Confirmation before archiving a session, which can also delete its worktree. */
@Composable
fun ArchiveSessionDialog(request: SessionRequest?, onDismiss: () -> Unit) {
    request ?: return
    val (s, connection) = request
    // On the connection's scope: the dialog leaves the composition as it confirms.
    fun archive(removeWorktree: Boolean) {
        connection.scope.launch { runCatching { connection.archive(s.id, removeWorktree) } }
    }
    val worktree = s.workspace.kind == Workspace.Kind.WORKTREE
    ConfirmDialog(
        title = "Archive session?",
        message = if (worktree) "The branch ${s.workspace.branch ?: ""} is kept either way." else "The agent process is stopped.",
        confirm = "Archive",
        destructive = false,
        onConfirm = { archive(removeWorktree = false) },
        onDismiss = onDismiss,
        extra = if (worktree) "Archive and delete worktree" to { archive(removeWorktree = true) } else null,
    )
}

/** Confirmation before deleting a session for good, which can also delete its worktree. */
@Composable
fun DeleteSessionDialog(request: SessionRequest?, onDismiss: () -> Unit) {
    request ?: return
    val (s, connection) = request
    // On the connection's scope: the dialog leaves the composition as it confirms.
    fun delete(removeWorktree: Boolean) {
        connection.scope.launch { runCatching { connection.delete(s.id, removeWorktree) } }
    }
    val worktree = s.workspace.kind == Workspace.Kind.WORKTREE
    ConfirmDialog(
        title = "Delete session?",
        message = if (worktree) "This can't be undone. The branch ${s.workspace.branch ?: ""} is kept either way." else "This can't be undone.",
        confirm = "Delete",
        onConfirm = { delete(removeWorktree = false) },
        onDismiss = onDismiss,
        extra = if (worktree) "Delete with worktree" to { delete(removeWorktree = true) } else null,
    )
}

/** Confirmation before forgetting a server. */
@Composable
fun RemoveServerDialog(server: SavedServer?, onDismiss: () -> Unit, onRemove: () -> Unit = {}) {
    val store = LocalStore.current
    server ?: return
    ConfirmDialog(
        title = "Remove ${server.name}?",
        message = "This device is unpaired from the server. Run `rad pair` on the computer to add it again.",
        confirm = "Remove",
        onConfirm = {
            onRemove()
            store.remove(server)
        },
        onDismiss = onDismiss,
    )
}
