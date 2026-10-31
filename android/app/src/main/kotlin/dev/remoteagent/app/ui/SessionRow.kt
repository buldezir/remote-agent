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
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.Icon
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.ExperimentalComposeUiApi
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.testTagsAsResourceId
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.input.TextFieldValue
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.remoteagent.core.protocol.HarnessInfo
import dev.remoteagent.core.protocol.Project
import dev.remoteagent.core.protocol.Session
import dev.remoteagent.core.protocol.Workspace
import dev.remoteagent.core.store.SavedServer
import dev.remoteagent.core.store.ServerConnection
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch

// A session's row, for the phone's list of a server's sessions, and the
// dialogs that go with it, which the tablet's sidebars (SidebarRows.kt) use
// too. Mirrors apple/Shared/Views/SessionRow.swift.

@Composable
fun SessionRow(session: Session, project: Project?, harness: HarnessInfo?, modifier: Modifier = Modifier) {
    Row(modifier, horizontalArrangement = Arrangement.spacedBy(10.dp), verticalAlignment = Alignment.Top) {
        Box(Modifier.padding(top = 2.dp)) {
            StatusIcon(session.status, background = session.backgroundTasks.isNotEmpty(), unread = session.unread, draft = session.draft != null)
        }
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
 * A session in a list: a tap opens it, a long press offers to rename, archive
 * or delete it, and a swipe archives it at once, or deletes one already archived.
 * The selected one is drawn as a surface inset from the edges, on the list's
 * [background], which also covers the swipe's colour behind the row.
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
fun SessionRowItem(
    session: Session,
    connection: ServerConnection,
    project: Project?,
    harness: HarnessInfo?,
    background: Color,
    onClick: () -> Unit,
    onArchive: () -> Unit,
    onDelete: () -> Unit,
    // Null: the server can't rename.
    onRename: (() -> Unit)?,
    modifier: Modifier = Modifier,
    selected: Boolean = false,
) {
    var menu by remember { mutableStateOf(false) }
    Box(modifier) {
        SessionSwipe(SessionRequest(session, connection), onDelete) {
            Box(Modifier.background(background)) {
                Box(
                    Modifier.fillMaxWidth().padding(horizontal = 8.dp)
                        .background(if (selected) palette.surface0 else Color.Transparent, RoundedCornerShape(Metrics.Corner.selection))
                        .combinedClickable(onClick = onClick, onLongClick = { menu = true })
                        .testTag("session-row"),
                ) {
                    SessionRow(session, project, harness, Modifier.padding(horizontal = 8.dp, vertical = 10.dp))
                }
                SessionRowMenu(menu, { menu = false }, session.archived, onArchive, onDelete, onRename)
            }
        }
    }
}

/**
 * A row's swipe: Archive, which goes ahead without asking and keeps a
 * worktree, or for a session already archived, Delete, which asks first.
 */
@Composable
fun SessionSwipe(request: SessionRequest, onDelete: () -> Unit, content: @Composable () -> Unit) {
    if (request.session.archived) {
        SwipeAction("Delete", Icons.delete, palette.red, onSwipe = onDelete, content = content)
    } else {
        SwipeAction("Archive", Icons.archive, palette.blue, onSwipe = { request.archive() }, content = content)
    }
}

/**
 * A row's long-press menu: Rename when the server can (archived sessions
 * too), Archive while it isn't archived, and Delete.
 */
@Composable
fun SessionRowMenu(
    expanded: Boolean,
    onDismiss: () -> Unit,
    archived: Boolean,
    onArchive: () -> Unit,
    onDelete: () -> Unit,
    // Null: the server can't rename.
    onRename: (() -> Unit)?,
) {
    DropdownMenu(expanded, onDismiss, containerColor = palette.mantle) {
        if (onRename != null) MenuItem("Rename…", Icons.rename) { onDismiss(); onRename() }
        if (!archived) MenuItem("Archive…", Icons.archive) { onDismiss(); onArchive() }
        MenuItem("Delete…", Icons.delete, color = palette.red) { onDismiss(); onDelete() }
    }
}

/** A session to rename, archive or delete, and the server it is on. */
data class SessionRequest(val session: Session, val connection: ServerConnection) {
    /**
     * Archives the session without asking. On the connection's scope: the
     * row or dialog that asked leaves the composition as it goes.
     */
    fun archive(removeWorktree: Boolean = false) {
        connection.scope.launch { runCatching { connection.archive(session.id, removeWorktree) } }
    }
}

/** Confirmation before archiving a session, which can also delete its worktree. */
@Composable
fun ArchiveSessionDialog(request: SessionRequest?, onDismiss: () -> Unit) {
    request ?: return
    val s = request.session
    val worktree = s.workspace.kind == Workspace.Kind.WORKTREE
    ConfirmDialog(
        title = "Archive session?",
        message = if (worktree) "The branch ${s.workspace.branch ?: ""} is kept either way." else "The agent process is stopped.",
        confirm = "Archive",
        destructive = false,
        onConfirm = { request.archive() },
        onDismiss = onDismiss,
        extra = if (worktree) "Archive and delete worktree" to { request.archive(removeWorktree = true) } else null,
    )
}

/**
 * "Rename Session": its title, all of it selected so that typing replaces it,
 * and the keyboard up; Done on it renames. An empty title gives the session
 * back to rad, which names it from its first prompt again. The dialog closes
 * at once, and the new title comes as the session's event; a rename that fails
 * shows its error, so callers keep this in the composition as they do the
 * other dialogs.
 */
@OptIn(ExperimentalComposeUiApi::class)
@Composable
fun RenameSessionDialog(request: SessionRequest?, onDismiss: () -> Unit) {
    var error by remember { mutableStateOf<String?>(null) }
    ErrorDialog(error, title = "Couldn't rename the session") { error = null }
    request ?: return
    val (s, connection) = request
    // The title it opened with, which the session may lose meanwhile, as rad names it.
    val shown = remember(s.id) { s.title }
    var field by remember(s.id) { mutableStateOf(TextFieldValue(shown, TextRange(0, shown.length))) }
    fun rename() {
        onDismiss()
        val title = field.text
        // Unchanged: nothing to ask.
        if (title.trim() == shown) return
        // On the connection's scope: the dialog has left the composition.
        connection.scope.launch {
            try {
                connection.rename(s.id, title)
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                error = e.message ?: "The request failed"
            }
        }
    }
    AlertDialog(
        onDismissRequest = onDismiss,
        // A dialog is a window of its own: its test tags need this again (android/e2e).
        modifier = Modifier.semantics { testTagsAsResourceId = true },
        containerColor = palette.mantle,
        titleContentColor = palette.text,
        textContentColor = palette.subtext,
        title = { Text("Rename Session") },
        text = {
            // Focus brings the keyboard up. Showing it as well (the keyboard
            // controller) left it, on a tablet, to the sidebar's search field
            // once the dialog closed.
            val focus = remember { FocusRequester() }
            LaunchedEffect(Unit) { runCatching { focus.requestFocus() } }
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                OutlinedTextField(
                    field, { field = it },
                    singleLine = true,
                    textStyle = Type.body,
                    keyboardOptions = KeyboardOptions(capitalization = KeyboardCapitalization.Sentences, imeAction = ImeAction.Done),
                    keyboardActions = KeyboardActions(onDone = { rename() }),
                    modifier = Modifier.fillMaxWidth().focusRequester(focus).testTag("rename-field"),
                    colors = OutlinedTextFieldDefaults.colors(
                        focusedTextColor = palette.text, unfocusedTextColor = palette.text, focusedBorderColor = palette.accent,
                        unfocusedBorderColor = palette.surface1, cursorColor = palette.accent,
                    ),
                )
                Text("Leave it empty to name it automatically.", style = Type.footnote)
            }
        },
        confirmButton = { TextButton(::rename, Modifier.testTag("rename-confirm")) { Text("Rename", color = palette.accent) } },
        dismissButton = { TextButton(onDismiss) { Text("Cancel", color = palette.accent) } },
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
