package dev.remoteagent.app.ui

import android.content.Intent
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
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
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.core.net.toUri
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import dev.remoteagent.app.platform.AppSettings.Keys
import dev.remoteagent.app.platform.Platform
import dev.remoteagent.core.protocol.CommitPreview
import dev.remoteagent.core.protocol.FileStat
import dev.remoteagent.core.protocol.ServerTask
import dev.remoteagent.core.store.ServerConnection
import dev.remoteagent.core.store.ServerTaskProgress
import dev.remoteagent.core.store.ServerTaskProgress.Phase
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch

/** How far a commit goes, as the sheet offers it; the last one chosen is remembered. */
internal enum class CommitScope(val label: String) {
    COMMIT("Commit only"),
    PUSH("Commit & push"),
    PULL_REQUEST("Commit, push & open PR");

    val pushes: Boolean get() = this != COMMIT

    /** Whether the sheet offers it: a commit needs changes, a push a remote. */
    fun enabled(preview: CommitPreview): Boolean = if (pushes) preview.remote != null else preview.files.isNotEmpty()

    /** The button that starts it: without changes, it only pushes. */
    fun action(hasChanges: Boolean): String = when (this) {
        COMMIT -> "Commit"
        PUSH -> if (hasChanges) "Commit & Push" else "Push"
        PULL_REQUEST -> if (hasChanges) "Commit, Push & Open PR" else "Push & Open PR"
    }

    companion object {
        /**
         * The choice the sheet starts at: the one used last, else a push when
         * there's a remote, else a commit, as long as the preview allows it.
         */
        fun initial(stored: String?, preview: CommitPreview): CommitScope =
            listOfNotNull(entries.firstOrNull { it.name == stored }, PUSH, COMMIT).firstOrNull { it.enabled(preview) } ?: COMMIT
    }
}

/** "ra/fix-login → origin", or the branch alone without a remote. */
internal fun commitTarget(preview: CommitPreview): String {
    val branch = preview.branch ?: return "Detached HEAD"
    return preview.remote?.let { "$branch → $it" } ?: branch
}

internal fun plural(n: Int, one: String, many: String = one + "s"): String = "$n ${if (n == 1) one else many}"

/**
 * "Commit": commits the session's changes with a message the system agent
 * wrote (which the user can edit), then pushes the branch and opens a pull
 * request, as far as the user chose, and shows each step as it goes. It
 * runs on the server as a task: closing the sheet leaves it running, and
 * opening it again while it runs shows it. Mirrors apple/Shared/Views/CommitView.swift.
 */
@Composable
fun CommitDialog(connection: ServerConnection, sessionID: String, onDismiss: () -> Unit) {
    val scope = rememberCoroutineScope()
    val settings = LocalSettings.current
    var preview by remember { mutableStateOf<CommitPreview?>(null) }
    var loadError by remember { mutableStateOf<String?>(null) }
    var reload by remember { mutableIntStateOf(0) }
    var message by rememberSaveable { mutableStateOf("") }
    var choice by rememberSaveable { mutableStateOf<CommitScope?>(null) }
    var starting by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var progress by remember { mutableStateOf<ServerTaskProgress?>(null) }
    DisposableEffect(progress) {
        val p = progress
        onDispose { p?.stop() }
    }

    // A commit that runs already, from here or another device, shows as it goes.
    LaunchedEffect(reload) {
        if (progress != null) return@LaunchedEffect
        loadError = null
        try {
            val running = runCatching { connection.tasks(sessionID) }.getOrNull()
                ?.lastOrNull { it.kind == ServerTask.COMMIT && it.sessionId == sessionID && it.running }
            if (running != null) {
                progress = ServerTaskProgress(connection, running)
                return@LaunchedEffect
            }
            val p = connection.commitPreview(sessionID)
            if (preview == null) message = p.message
            if (choice == null) choice = CommitScope.initial(settings.get(Keys.commitScope), p)
            preview = p
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            loadError = e.message ?: "The server didn't answer"
        }
    }

    fun start() {
        val p = preview ?: return
        val c = choice?.takeIf { it.enabled(p) } ?: return
        starting = true
        error = null
        scope.launch {
            try {
                val task = connection.commit(
                    sessionID, if (p.files.isEmpty()) "" else message,
                    push = c.pushes, pullRequest = c == CommitScope.PULL_REQUEST,
                )
                settings.put(Keys.commitScope, c.name)
                progress = ServerTaskProgress(connection, task)
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                error = e.message
            } finally {
                starting = false
            }
        }
    }

    FullScreenDialog(onDismiss) {
        val p = progress
        if (p != null) {
            CommitProgressScreen(p, onDismiss)
            return@FullScreenDialog
        }
        val pv = preview
        val current = pv?.let { choice ?: CommitScope.initial(null, it) }
        Screen(
            title = { Text("Commit", style = Type.headline) },
            onBack = onDismiss, backIcon = Icons.close, backLabel = "Cancel", background = palette.mantle,
            // Where the other sheets have theirs, so that it's there without
            // scrolling past the files, saying how far it goes.
            actions = {
                if (starting) {
                    CircularProgressIndicator(Modifier.size(24.dp).padding(end = 4.dp), color = palette.subtext, strokeWidth = 2.dp)
                } else if (pv != null && current != null) {
                    val enabled = current.enabled(pv)
                    TextButton(::start, enabled = enabled, modifier = Modifier.testTag("commit-start")) {
                        Text(current.action(pv.files.isNotEmpty()), color = if (enabled) palette.accent else palette.overlay, style = Type.headline, maxLines = 1)
                    }
                }
            },
        ) { padding ->
            Column(Modifier.fillMaxSize().padding(padding).imePadding().verticalScroll(rememberScrollState()).formWidth()) {
                when {
                    pv != null && current != null -> CommitForm(pv, message, { message = it }, current, { choice = it })
                    loadError != null -> FormSection {
                        Row(Modifier.padding(12.dp), horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                            Icon(painterResource(Icons.warning), null, tint = palette.red, modifier = Modifier.size(18.dp))
                            Text(loadError ?: "", color = palette.red, style = Type.subheadline, modifier = Modifier.weight(1f))
                            TextButton({ reload++ }, modifier = Modifier.testTag("commit-retry")) { Text("Retry", color = palette.accent, style = Type.body) }
                        }
                    }
                    else -> Row(
                        Modifier.fillMaxWidth().padding(24.dp).testTag("commit-loading"),
                        horizontalArrangement = Arrangement.spacedBy(10.dp, Alignment.CenterHorizontally),
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        CircularProgressIndicator(Modifier.size(18.dp), color = palette.subtext, strokeWidth = 2.dp)
                        Text("Looking at the changes…", style = Type.body, color = palette.subtext)
                    }
                }
                error?.let { FormError(it) }
            }
        }
    }
}

@Composable
private fun CommitForm(
    preview: CommitPreview,
    message: String,
    onMessage: (String) -> Unit,
    choice: CommitScope,
    onChoice: (CommitScope) -> Unit,
) {
    val files = preview.files
    FormSection("Branch") {
        InfoRow(Icons.branch, commitTarget(preview), mono = preview.branch != null, tag = "commit-target")
        if (preview.remote == null) {
            FormDivider()
            InfoRow(Icons.warning, "No remote to push to", color = palette.subtext)
        }
        if (choice == CommitScope.PULL_REQUEST) {
            FormDivider()
            InfoRow(Icons.commit, "Pull request into ${preview.base ?: "the default branch"}", color = palette.subtext)
        }
    }

    if (files.isEmpty()) {
        FormSection("Changes") {
            Column(Modifier.padding(12.dp).testTag("commit-nothing"), verticalArrangement = Arrangement.spacedBy(2.dp)) {
                Text("Nothing to commit.", style = Type.body, color = palette.text)
                if (preview.ahead > 0) Text("${plural(preview.ahead, "commit")} to push", style = Type.footnote, color = palette.subtext)
            }
        }
    } else {
        val adds = files.sumOf { it.additions }
        val dels = files.sumOf { it.deletions }
        FormSection("${plural(files.size, "file")} changed  +$adds −$dels") {
            files.take(MAX_FILES).forEachIndexed { i, f ->
                if (i > 0) FormDivider()
                ChangedFile(f)
            }
            if (files.size > MAX_FILES) {
                FormDivider()
                Text("and ${plural(files.size - MAX_FILES, "more file")}", style = Type.footnote, color = palette.subtext, modifier = Modifier.padding(12.dp))
            }
        }
        FormSection("Message") {
            FormTextField(message, onMessage, "Leave empty for rad to write one", style = messageStyle(), maxLines = 12, minLines = 3, modifier = Modifier.testTag("commit-message"))
        }
    }

    FormSection("Then") {
        CommitScope.entries.forEachIndexed { i, s ->
            if (i > 0) FormDivider()
            val enabled = s.enabled(preview)
            Row(
                Modifier.fillMaxWidth().clickable(enabled = enabled) { onChoice(s) }.padding(horizontal = 12.dp, vertical = 12.dp).testTag("commit-scope-${s.name.lowercase()}"),
                horizontalArrangement = Arrangement.spacedBy(10.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                val on = s == choice
                Icon(
                    painterResource(if (on) Icons.radioOn else Icons.radioOff), null, Modifier.size(20.dp),
                    tint = if (!enabled) palette.overlay else if (on) palette.accent else palette.subtext,
                )
                Text(s.label, style = Type.body, color = if (enabled) palette.text else palette.overlay)
            }
        }
    }
}

private const val MAX_FILES = 30

@Composable
private fun InfoRow(icon: Int, text: String, color: androidx.compose.ui.graphics.Color = palette.text, mono: Boolean = false, tag: String? = null) {
    Row(
        Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 12.dp).then(if (tag != null) Modifier.testTag(tag) else Modifier),
        horizontalArrangement = Arrangement.spacedBy(10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Icon(painterResource(icon), null, Modifier.size(18.dp), tint = palette.subtext)
        Text(
            text, style = if (mono) Type.body.copy(fontFamily = FontFamily.Monospace) else Type.body, color = color,
            maxLines = 1, overflow = TextOverflow.MiddleEllipsis,
        )
    }
}

/** A file the commit takes: its status as a letter, its path and its lines. */
@Composable
private fun ChangedFile(file: FileStat) {
    val (letter, color) = when (file.status) {
        FileStat.Status.ADDED -> "A" to palette.green
        FileStat.Status.DELETED -> "D" to palette.red
        FileStat.Status.RENAMED -> "R" to palette.blue
        else -> "M" to palette.yellow
    }
    Row(
        Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 8.dp).testTag("commit-file"),
        horizontalArrangement = Arrangement.spacedBy(10.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(letter, style = Type.footnote.copy(fontFamily = FontFamily.Monospace, fontWeight = FontWeight.Bold), color = color, modifier = Modifier.width(14.dp))
        Text(
            file.oldPath?.let { "$it → ${file.path}" } ?: file.path,
            style = Type.footnote.copy(fontFamily = FontFamily.Monospace), color = palette.text,
            maxLines = 1, overflow = TextOverflow.StartEllipsis, modifier = Modifier.weight(1f),
        )
        if (file.binary == true) {
            Text("binary", style = Type.caption2, color = palette.subtext)
        } else {
            Text("+${file.additions}", style = Type.caption.copy(fontFamily = FontFamily.Monospace), color = palette.green)
            Text("−${file.deletions}", style = Type.caption.copy(fontFamily = FontFamily.Monospace), color = palette.red)
        }
    }
}

/** The commit as it goes: its steps, the pull request once there is one, and what git and gh printed. */
@Composable
private fun CommitProgressScreen(progress: ServerTaskProgress, onDismiss: () -> Unit) {
    val task by progress.task.collectAsStateWithLifecycle()
    val phase by progress.phase.collectAsStateWithLifecycle()
    val scope = rememberCoroutineScope()
    val context = LocalContext.current
    var error by remember { mutableStateOf<String?>(null) }
    var showOutput by rememberSaveable { mutableStateOf(false) }
    LaunchedEffect(phase) { if (phase == Phase.FAILED) showOutput = true }

    Screen(
        title = { Text("Commit", style = Type.headline) },
        background = palette.mantle,
        actions = {
            TextButton(onDismiss, modifier = Modifier.testTag("commit-done")) { Text("Done", color = palette.accent, style = Type.headline) }
        },
    ) { padding ->
        Column(
            Modifier.fillMaxSize().padding(padding).verticalScroll(rememberScrollState()).formWidth().padding(bottom = Metrics.margin),
        ) {
            Row(Modifier.padding(horizontal = Metrics.margin + 12.dp, vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                val text = when (phase) {
                    Phase.RUNNING, Phase.RESTARTING -> "Working…"
                    Phase.DONE -> "Done."
                    Phase.FAILED -> task.error?.takeIf { it.isNotEmpty() }?.let { "Failed: $it" } ?: "Failed."
                    Phase.CANCELLED -> "Cancelled."
                }
                Text(
                    text, style = Type.body, color = if (phase == Phase.FAILED) palette.red else palette.text,
                    modifier = Modifier.weight(1f).testTag("commit-status"),
                )
                if (phase == Phase.RUNNING) {
                    TextButton(
                        {
                            error = null
                            scope.launch {
                                try {
                                    progress.cancel()
                                } catch (e: CancellationException) {
                                    throw e
                                } catch (e: Exception) {
                                    error = e.message
                                }
                            }
                        },
                        modifier = Modifier.testTag("commit-cancel"),
                    ) { Text("Cancel", color = palette.red, style = Type.body) }
                }
            }

            if (task.steps.isNotEmpty()) {
                FormSection {
                    task.steps.forEachIndexed { i, step ->
                        if (i > 0) FormDivider()
                        StepRow(step)
                    }
                }
            }

            val url = webURL(task.url)
            if (url != null) {
                Row(
                    Modifier.padding(horizontal = Metrics.margin, vertical = 8.dp),
                    horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically,
                ) {
                    Button(
                        {
                            try {
                                context.startActivity(Intent(Intent.ACTION_VIEW, url.toUri()))
                            } catch (_: android.content.ActivityNotFoundException) {
                                error = "No app here opens $url"
                            }
                        },
                        colors = ButtonDefaults.buttonColors(containerColor = palette.accent, contentColor = palette.onAccent),
                        shape = RoundedCornerShape(Metrics.Corner.card),
                        modifier = Modifier.testTag("open-pull-request"),
                    ) {
                        Icon(painterResource(Icons.openPage), null, Modifier.size(18.dp))
                        Text("Open Pull Request", modifier = Modifier.padding(start = 6.dp))
                    }
                    TextButton({ Platform.copy(context, url, "Pull request") }) {
                        Icon(painterResource(Icons.copy), null, Modifier.size(18.dp), tint = palette.accent)
                        Text("Copy Link", color = palette.accent, style = Type.body, modifier = Modifier.padding(start = 6.dp))
                    }
                }
            }

            error?.let { FormError(it) }

            if (task.output.isNotEmpty()) {
                Row(
                    Modifier.fillMaxWidth().clickable { showOutput = !showOutput }
                        .padding(horizontal = Metrics.margin + 12.dp, vertical = 10.dp).testTag("commit-output-toggle"),
                    horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.CenterVertically,
                ) {
                    Text("Output", style = Type.footnote.copy(fontWeight = FontWeight.SemiBold), color = palette.subtext)
                    Icon(painterResource(if (showOutput) Icons.expand else Icons.chevronRight), null, Modifier.size(16.dp), tint = palette.subtext)
                }
                if (showOutput) {
                    Box(Modifier.fillMaxWidth().padding(horizontal = Metrics.margin).background(palette.crust, RoundedCornerShape(Metrics.Corner.card))) {
                        SelectionContainer {
                            Text(task.output, style = codeStyle(), color = palette.text, modifier = Modifier.fillMaxWidth().padding(Metrics.padding).testTag("commit-output"))
                        }
                    }
                }
            }
        }
    }
}

/** A step and how it went: waiting, working, done, nothing to do, or failed, with what it did beneath. */
@Composable
private fun StepRow(step: ServerTask.Step) {
    Row(
        Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 10.dp).testTag("commit-step-${step.id}"),
        horizontalArrangement = Arrangement.spacedBy(10.dp),
        verticalAlignment = Alignment.Top,
    ) {
        Box(Modifier.padding(top = 1.dp).size(20.dp), contentAlignment = Alignment.Center) {
            when (step.status) {
                ServerTask.Step.Status.RUNNING -> CircularProgressIndicator(Modifier.size(16.dp), color = palette.subtext, strokeWidth = 2.dp)
                ServerTask.Step.Status.SUCCEEDED -> Icon(painterResource(Icons.completed), null, tint = palette.green)
                ServerTask.Step.Status.SKIPPED -> Icon(painterResource(Icons.skipped), null, tint = palette.subtext)
                ServerTask.Step.Status.FAILED -> Icon(painterResource(Icons.failed), null, tint = palette.red)
                ServerTask.Step.Status.PENDING, ServerTask.Step.Status.UNKNOWN -> Icon(painterResource(Icons.circle), null, tint = palette.subtext)
            }
        }
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            val pending = step.status == ServerTask.Step.Status.PENDING || step.status == ServerTask.Step.Status.UNKNOWN
            Text(step.title, style = Type.body, color = if (pending) palette.subtext else palette.text)
            step.detail?.takeIf { it.isNotEmpty() }?.let {
                Text(it, style = Type.footnote, color = if (step.status == ServerTask.Step.Status.FAILED) palette.red else palette.subtext)
            }
        }
    }
}
