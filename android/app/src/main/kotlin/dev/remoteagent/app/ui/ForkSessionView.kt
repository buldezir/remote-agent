package dev.remoteagent.app.ui

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import dev.remoteagent.core.protocol.Item
import dev.remoteagent.core.protocol.ItemKind
import dev.remoteagent.core.protocol.Session
import dev.remoteagent.core.protocol.Turn
import dev.remoteagent.core.protocol.TurnStatus
import dev.remoteagent.core.protocol.Workspace
import dev.remoteagent.core.store.ForkSession
import dev.remoteagent.core.store.NewSession
import dev.remoteagent.core.store.ServerConnection
import dev.remoteagent.core.store.SessionStore
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch

/** A turn a fork can continue after, as the From picker shows it. */
internal data class ForkPoint(val turn: Int, val label: String)

/**
 * The turns that ended, newest first, each labelled with its number and the
 * first line of its prompt, cut short.
 */
internal fun forkPoints(turns: Collection<Turn>, transcript: List<Item>): List<ForkPoint> {
    val prompts = HashMap<String, String>()
    for (item in transcript) {
        val id = item.turnId ?: continue
        if (item.kind == ItemKind.USER_MESSAGE && id !in prompts) prompts[id] = item.text.orEmpty()
    }
    return turns.filter { it.status != TurnStatus.RUNNING }.sortedByDescending { it.n }.map { t ->
        val line = prompts[t.id]?.lineSequence()?.map { it.trim() }?.firstOrNull { it.isNotEmpty() }
        ForkPoint(t.n, listOfNotNull("Turn ${t.n}", line?.let(::shortened)).joinToString(" · "))
    }
}

private fun shortened(line: String, max: Int = 48): String =
    if (line.length <= max) line else line.take(max - 1).trimEnd() + "…"

/**
 * "Fork session": a new session that continues the one in `store` after one
 * of its turns, with its transcript up to there. The agent and its options
 * start as the session's, and aren't remembered for New Session. `onForked`
 * gets the new session, and whether it opens without a prompt.
 * Mirrors apple/Shared/Views/ForkSessionView.swift.
 */
@Composable
fun ForkSessionDialog(connection: ServerConnection, store: SessionStore, onDismiss: () -> Unit, onForked: (Session, empty: Boolean) -> Unit) {
    val scope = rememberCoroutineScope()
    val storeSession by store.session.collectAsStateWithLifecycle()
    val sessions by connection.sessions.collectAsStateWithLifecycle()
    val projects by connection.projects.collectAsStateWithLifecycle()
    val harnesses by connection.harnesses.collectAsStateWithLifecycle()
    val turns by store.turns.collectAsStateWithLifecycle()
    val transcript by store.transcript.collectAsStateWithLifecycle()
    val source = storeSession ?: sessions[store.sessionID]
    val project = source?.let { projects[it.projectId] }
    val points = remember(turns, transcript) { forkPoints(turns.values, transcript) }

    // Null: the newest turn that ended.
    var turn by rememberSaveable { mutableStateOf<Int?>(null) }
    var harnessID by rememberSaveable { mutableStateOf("") }
    var model by rememberSaveable { mutableStateOf("") }
    var customModel by rememberSaveable { mutableStateOf("") }
    var effort by rememberSaveable { mutableStateOf("") }
    var mode by rememberSaveable { mutableStateOf("") }
    var useWorktree by rememberSaveable { mutableStateOf(source?.workspace?.kind == Workspace.Kind.WORKTREE) }
    var branch by rememberSaveable { mutableStateOf("") }
    var prompt by rememberSaveable { mutableStateOf("") }
    var forking by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }

    val point = points.firstOrNull { it.turn == turn } ?: points.firstOrNull()
    val harness = harnesses?.firstOrNull { it.id == harnessID }
    val chosenModel = chosenModel(harness, model, customModel)
    // With the source's agent, its model, effort and mode as the pickers
    // start with them. Left so, they go unsaid, and the fork keeps the
    // source's own.
    val kept = source?.let { s -> harness?.takeIf { it.id == s.harness }?.let { AgentChoices.of(it, s.model, s.effort, s.mode) } }

    // The source's agent, unless it can't be used now.
    LaunchedEffect(Unit) {
        if (connection.harnesses.value == null) connection.refreshHarnesses()
        if (harnessID.isEmpty()) {
            val list = connection.harnesses.value
            harnessID = source?.harness?.takeIf { id -> list?.firstOrNull { it.id == id }?.usable == true }
                ?: list?.firstOrNull { it.usable }?.id ?: ""
        }
    }
    // Another agent starts at its defaults.
    var startedFor by rememberSaveable { mutableStateOf<String?>(null) }
    LaunchedEffect(harnessID, harness != null) {
        if (harness != null && startedFor != harnessID) {
            val c = kept ?: AgentChoices.of(harness, null, null, null)
            model = c.model
            customModel = c.customModel
            effort = c.effort
            mode = c.mode
            startedFor = harnessID
        }
    }
    // Models support different effort levels.
    LaunchedEffect(chosenModel) {
        if (harness != null && harness.efforts(chosenModel).none { it.id == effort }) effort = ""
    }

    fun fork() {
        val s = source ?: return
        val at = point ?: return
        val h = harness ?: return
        // The source's kind of workspace goes unsaid too.
        val named = if (useWorktree) branch.trim() else ""
        val workspace = NewSession.WorkspaceParams(if (useWorktree) Workspace.Kind.WORKTREE else Workspace.Kind.ROOT, branch = named.ifEmpty { null })
            .takeIf { project?.isGitRepo == true && (useWorktree != (s.workspace.kind == Workspace.Kind.WORKTREE) || named.isNotEmpty()) }
        forking = true
        error = null
        scope.launch {
            try {
                val forked = connection.fork(
                    ForkSession(
                        sessionId = s.id, turn = at.turn,
                        harness = h.id.takeIf { it != s.harness },
                        // As the pickers show them, which start at the source's.
                        model = chosenModel,
                        effort = effort.ifEmpty { null },
                        mode = mode.ifEmpty { null },
                        workspace = workspace,
                        prompt = prompt.takeIf { it.isNotBlank() },
                    ),
                )
                onForked(forked, prompt.isBlank())
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                error = e.message
            } finally {
                forking = false
            }
        }
    }

    FullScreenDialog(onDismiss) {
        Screen(
            title = { Text("Fork session", style = Type.headline) },
            onBack = onDismiss, backIcon = Icons.close, backLabel = "Cancel", background = palette.mantle,
            actions = {
                if (forking) {
                    CircularProgressIndicator(Modifier.size(24.dp).padding(end = 4.dp), color = palette.subtext, strokeWidth = 2.dp)
                } else {
                    val enabled = source != null && point != null && harness != null
                    TextButton({ fork() }, enabled = enabled, modifier = Modifier.testTag("fork")) {
                        Text("Fork", color = if (enabled) palette.accent else palette.overlay, style = Type.headline)
                    }
                }
            },
        ) { padding ->
            Column(Modifier.fillMaxSize().padding(padding).imePadding().verticalScroll(rememberScrollState()).formWidth()) {
                FormSection("From") {
                    PickerRow(
                        "After turn", point?.label ?: "None yet", points.map { it.turn.toString() to it.label }, tag = "fork-turn",
                    ) { turn = it.toInt() }
                }

                AgentSection(harnesses, harnessID) { harnessID = it }

                if (harness != null) {
                    OptionsSection(
                        harness, model = model, customModel = customModel, effort = effort, mode = mode,
                        onModel = { model = it }, onCustomModel = { customModel = it }, onEffort = { effort = it }, onMode = { mode = it },
                    )
                }

                if (project?.isGitRepo == true) {
                    FormSection(
                        "Workspace",
                        footer = AnnotatedString(
                            if (useWorktree) "The fork gets its own branch, with the files as they were after that turn."
                            else "The fork works in the project folder, as it is now.",
                        ),
                    ) {
                        SwitchRow("Isolated git worktree", useWorktree) { useWorktree = it }
                        if (useWorktree) {
                            FormDivider()
                            FormTextField(branch, { branch = it }, "Branch (auto)",
                                keyboardOptions = KeyboardOptions(autoCorrectEnabled = false, capitalization = KeyboardCapitalization.None))
                        }
                    }
                }

                FormSection(
                    "Prompt",
                    footer = AnnotatedString(
                        "Optional. Without one, the fork opens with the conversation so far, and you write the next prompt in it. " +
                            "The same agent picks up its own conversation; another agent gets it as a transcript with the first prompt.",
                    ),
                ) {
                    FormTextField(prompt, { prompt = it }, "What should the agent do next?", style = messageStyle(), maxLines = 12, minLines = 4, modifier = Modifier.testTag("fork-prompt"))
                }

                error?.let { FormError(it) }
            }
        }
    }
}
