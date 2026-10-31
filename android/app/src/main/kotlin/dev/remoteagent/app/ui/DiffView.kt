package dev.remoteagent.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import dev.remoteagent.app.platform.AppSettings.Keys
import dev.remoteagent.app.platform.state
import dev.remoteagent.core.protocol.Diff
import dev.remoteagent.core.protocol.FileStat
import dev.remoteagent.core.protocol.SessionStatus
import dev.remoteagent.core.protocol.Turn
import dev.remoteagent.core.store.SessionStore
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import java.io.File

/**
 * A session's or a turn's changes, and reverting a worktree; `onCommit`, if
 * the session can commit, opens Commit. Mirrors apple/Shared/Views/DiffView.swift.
 */
@Composable
fun DiffDialog(store: SessionStore, canRevert: Boolean, onDismiss: () -> Unit, onCommit: (() -> Unit)? = null) {
    val scope = rememberCoroutineScope()
    val turns by store.turns.collectAsStateWithLifecycle()
    val session by store.session.collectAsStateWithLifecycle()
    val running = session?.status == SessionStatus.RUNNING || session?.status == SessionStatus.AWAITING_APPROVAL
    val revertible = turns.values.filter { it.checkpointBefore != null }.sortedBy { it.n }
    // "session" or a turn id.
    var scopeID by rememberSaveable { mutableStateOf("session") }
    var diff by remember { mutableStateOf<Diff?>(null) }
    var loading by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var reverted by remember { mutableStateOf<String?>(null) }
    var revertMenu by remember { mutableStateOf(false) }
    var revertTurn by remember { mutableStateOf<Turn?>(null) }
    var openFile by remember { mutableStateOf<FileStat?>(null) }
    var reload by remember { mutableIntStateOf(0) }

    LaunchedEffect(scopeID, reload) {
        loading = true
        error = null
        try {
            diff = if (scopeID == "session") store.sessionDiff() else store.turnDiff(scopeID)
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            diff = null
            error = e.message
        } finally {
            loading = false
        }
    }

    FullScreenDialog({ if (openFile != null) openFile = null else onDismiss() }) {
        val file = openFile
        if (file != null) {
            FileDiffScreen(file, diff?.patch?.let { PatchParser.split(it)[file.path] } ?: "", onBack = { openFile = null })
            return@FullScreenDialog
        }
        Screen(
            title = { Text("Changes", style = Type.headline) },
            onBack = onDismiss, backIcon = Icons.close, backLabel = "Done", background = palette.mantle,
            actions = {
                if (onCommit != null) {
                    IconButton(onCommit, enabled = !running, modifier = Modifier.testTag("diff-commit")) {
                        Icon(painterResource(Icons.commit), "Commit…", tint = if (running) palette.overlay else palette.accent)
                    }
                }
                if (canRevert) {
                    Box {
                        TextButton({ revertMenu = true }, enabled = !running && revertible.isNotEmpty(), modifier = Modifier.testTag("revert")) {
                            Icon(painterResource(Icons.revert), null, Modifier.size(18.dp), tint = if (running) palette.overlay else palette.accent)
                            Text(" Revert files…", color = if (running) palette.overlay else palette.accent)
                        }
                        DropdownMenu(revertMenu, { revertMenu = false }, containerColor = palette.mantle) {
                            for (t in revertible.reversed()) MenuItem("Before turn ${t.n}", null) { revertMenu = false; revertTurn = t }
                        }
                    }
                }
            },
        ) { padding ->
            LazyColumn(Modifier.fillMaxSize().padding(padding)) {
                item {
                    FormSection {
                        val options = listOf("session" to "Whole session") + revertible.map { it.id to "Turn ${it.n}" }
                        PickerRow("Scope", options.firstOrNull { it.first == scopeID }?.second ?: "Whole session", options, tag = "diff-scope") { scopeID = it }
                    }
                }
                error?.let { item { Text(it, color = palette.red, style = Type.body, modifier = Modifier.padding(horizontal = 24.dp, vertical = 8.dp)) } }
                reverted?.let { item { Text(it, color = palette.green, style = Type.body, modifier = Modifier.padding(horizontal = 24.dp, vertical = 8.dp).testTag("reverted")) } }
                val d = diff
                if (d != null) {
                    item {
                        val adds = d.files.sumOf { it.additions }
                        val dels = d.files.sumOf { it.deletions }
                        Text(
                            "${d.files.size} file${if (d.files.size == 1) "" else "s"}  +$adds −$dels",
                            style = Type.footnote.copy(fontWeight = FontWeight.SemiBold), color = palette.subtext,
                            modifier = Modifier.padding(start = 24.dp, top = 12.dp, bottom = 4.dp),
                        )
                    }
                    if (d.files.isEmpty()) item { Text("No changes", color = palette.subtext, style = Type.body, modifier = Modifier.padding(horizontal = 24.dp, vertical = 8.dp)) }
                    items(d.files, key = { it.path }) { f -> FileRow(f) { openFile = f } }
                    if (d.truncated) item {
                        Text("The patch was truncated; some files may show no lines.", style = Type.caption, color = palette.subtext, modifier = Modifier.padding(24.dp))
                    }
                } else if (loading) {
                    item { Box(Modifier.fillMaxWidth().padding(24.dp), contentAlignment = Alignment.Center) { CircularProgressIndicator(color = palette.subtext) } }
                }
            }
        }
        revertTurn?.let { t ->
            ConfirmDialog(
                title = "Revert files?",
                message = "Files changed in turn ${t.n} and later are restored in the worktree. The conversation is kept, and the agent is told about the revert.",
                confirm = "Revert to before turn ${t.n}",
                onConfirm = {
                    scope.launch {
                        try {
                            val files = store.revert(t.n)
                            reverted = "Reverted ${files.size} file${if (files.size == 1) "" else "s"}"
                            reload++
                        } catch (e: CancellationException) {
                            throw e
                        } catch (e: Exception) {
                            error = e.message
                        }
                    }
                },
                onDismiss = { revertTurn = null },
            )
        }
    }
}

@Composable
private fun FileRow(file: FileStat, onClick: () -> Unit) {
    val (icon, color) = when (file.status) {
        FileStat.Status.ADDED -> Icons.add to palette.green
        FileStat.Status.DELETED -> Icons.delete to palette.red
        FileStat.Status.RENAMED -> Icons.keyRight to palette.blue
        else -> Icons.toolEdit to palette.peach
    }
    Row(
        Modifier.fillMaxWidth().background(palette.base).clickable(onClick = onClick).padding(horizontal = 16.dp, vertical = 10.dp).testTag("file-row"),
        horizontalArrangement = Arrangement.spacedBy(10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Icon(painterResource(icon), null, Modifier.size(20.dp), tint = color)
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(1.dp)) {
            Text(File(file.path).name, style = Type.footnote.copy(fontWeight = FontWeight.Medium), color = palette.text)
            val dir = File(file.path).parent
            val sub = file.oldPath?.let { "$it → ${file.path}" } ?: dir
            sub?.let { Text(it, style = Type.caption2.copy(fontFamily = FontFamily.Monospace), color = palette.subtext, maxLines = 1, overflow = TextOverflow.StartEllipsis) }
        }
        if (file.binary == true) {
            Text("binary", style = Type.caption2, color = palette.subtext)
        } else {
            Text("+${file.additions}", style = Type.caption.copy(fontFamily = FontFamily.Monospace), color = palette.green)
            Text("−${file.deletions}", style = Type.caption.copy(fontFamily = FontFamily.Monospace), color = palette.red)
        }
        Icon(painterResource(Icons.chevronRight), null, tint = palette.overlay)
    }
}

@Composable
private fun FileDiffScreen(file: FileStat, patch: String, onBack: () -> Unit) {
    val settings = LocalSettings.current
    val wrap by settings.state(Keys.diffWrap, true)
    val lines = remember(patch) { PatchParser.lines(patch) }
    Screen(
        title = { Text(File(file.path).name, style = Type.headline, maxLines = 1, overflow = TextOverflow.Ellipsis) },
        onBack = onBack,
        actions = {
            IconButton({ settings.put(Keys.diffWrap, !wrap) }, Modifier.testTag("wrap")) {
                Icon(painterResource(Icons.wrap), if (wrap) "Don't wrap lines" else "Wrap lines", tint = if (wrap) palette.accent else palette.subtext)
            }
        },
    ) { padding ->
        val p = palette
        @Composable
        fun line(l: DiffLine, modifier: Modifier) {
            val bg = when (l.kind) {
                DiffLine.Kind.ADD -> p.green.copy(alpha = 0.15f)
                DiffLine.Kind.DEL -> p.red.copy(alpha = 0.15f)
                else -> Color.Transparent
            }
            val fg = when (l.kind) {
                // As in Catppuccin's style guide.
                DiffLine.Kind.HUNK -> p.peach
                DiffLine.Kind.META -> p.subtext
                else -> p.text
            }
            Text(
                l.text.ifEmpty { " " }, color = fg, softWrap = wrap,
                style = Type.caption.copy(fontFamily = FontFamily.Monospace, fontSize = 12.sp, lineHeight = 16.sp),
                modifier = modifier.background(bg).padding(horizontal = 8.dp, vertical = 1.dp),
            )
        }
        if (lines.isEmpty()) {
            Text(if (file.binary == true) "Binary file" else "No textual changes", color = p.subtext, modifier = Modifier.padding(padding).padding(16.dp))
        } else if (wrap) {
            SelectionContainer {
                LazyColumn(Modifier.fillMaxSize().padding(padding).testTag("diff-lines")) {
                    items(lines) { line(it, Modifier.fillMaxWidth()) }
                }
            }
        } else {
            // Unwrapped, the lines scroll sideways together.
            SelectionContainer {
                Box(Modifier.fillMaxSize().padding(padding).verticalScroll(rememberScrollState()).horizontalScroll(rememberScrollState()).testTag("diff-lines")) {
                    Column(Modifier.width(IntrinsicSize.Max)) {
                        for (l in lines) line(l, Modifier.fillMaxWidth())
                    }
                }
            }
        }
    }
}
