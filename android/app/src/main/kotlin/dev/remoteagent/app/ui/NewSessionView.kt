package dev.remoteagent.app.ui

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
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import dev.remoteagent.app.platform.AppSettings.Keys
import dev.remoteagent.core.protocol.FSListing
import dev.remoteagent.core.protocol.HarnessInfo
import dev.remoteagent.core.protocol.Project
import dev.remoteagent.core.protocol.Session
import dev.remoteagent.core.protocol.Workspace
import dev.remoteagent.core.store.NewSession
import dev.remoteagent.core.store.ServerConnection
import dev.remoteagent.core.store.sortedProjects
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import java.io.File

private const val CUSTOM = "__custom"

/**
 * "New session", in `projectId` when given, else in the project used last.
 * `onCreated` gets the new session, or one picked up from the agent's own
 * (Continue a terminal session…), and whether it started empty, without a
 * prompt. Mirrors apple/Shared/Views/NewSessionView.swift.
 */
@Composable
fun NewSessionDialog(connection: ServerConnection, projectId: String? = null, onDismiss: () -> Unit, onCreated: (Session, empty: Boolean) -> Unit) {
    val settings = LocalSettings.current
    val scope = rememberCoroutineScope()
    val projects by connection.projects.collectAsStateWithLifecycle()
    val harnesses by connection.harnesses.collectAsStateWithLifecycle()
    val info by connection.info.collectAsStateWithLifecycle()
    var projectID by rememberSaveable { mutableStateOf(projectId ?: "") }
    var harnessID by rememberSaveable { mutableStateOf("") }
    var model by rememberSaveable { mutableStateOf("") }
    var customModel by rememberSaveable { mutableStateOf("") }
    var effort by rememberSaveable { mutableStateOf("") }
    var mode by rememberSaveable { mutableStateOf("") }
    var useWorktree by rememberSaveable { mutableStateOf(false) }
    var branch by rememberSaveable { mutableStateOf("") }
    var baseRef by rememberSaveable { mutableStateOf("") }
    var branches by remember { mutableStateOf(emptyList<String>()) }
    var prompt by rememberSaveable { mutableStateOf("") }
    var creating by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    // Why the chosen project wasn't forgotten, beside its Forget button.
    var forgetError by remember { mutableStateOf<String?>(null) }
    var browsing by remember { mutableStateOf(false) }
    // The agent's sessions from outside Remote Agent, to pick one up instead.
    var importing by rememberSaveable { mutableStateOf(false) }
    val attachments = rememberAttachments(connection) { error = it }

    val project = projects[projectID]
    val harness = harnesses?.firstOrNull { it.id == harnessID }
    val chosenModel = chosenModel(harness, model, customModel)

    // Starts from the model, effort and permission mode last used with this
    // agent, if it still offers them.
    suspend fun restoreChoices(h: HarnessInfo) {
        val c = AgentChoices.of(h, settings.get(Keys.lastModel(h.id)), settings.get(Keys.lastEffort(h.id)), settings.get(Keys.lastMode(h.id)))
        model = c.model
        customModel = c.customModel
        effort = c.effort
        mode = c.mode
    }

    LaunchedEffect(Unit) {
        if (connection.harnesses.value == null) connection.refreshHarnesses()
        val lastProject = settings.get(Keys.lastProject) ?: ""
        if (projectID.isEmpty() && connection.projects.value[lastProject] != null) projectID = lastProject
        if (harnessID.isEmpty()) {
            val last = settings.get(Keys.lastHarness) ?: "claude"
            val list = connection.harnesses.value
            harnessID = if (list?.firstOrNull { it.id == last }?.usable == true) last else list?.firstOrNull { it.usable }?.id ?: ""
        }
    }
    var restoredFor by rememberSaveable { mutableStateOf<String?>(null) }
    LaunchedEffect(harnessID, harness != null) {
        if (harness != null && restoredFor != harnessID) {
            restoreChoices(harness)
            restoredFor = harnessID
        }
    }
    // Models support different effort levels.
    LaunchedEffect(chosenModel) {
        if (harness != null && harness.efforts(chosenModel).none { it.id == effort }) effort = ""
    }
    LaunchedEffect(projectID) {
        forgetError = null
        branches = emptyList()
        if (project?.isGitRepo == true) branches = runCatching { connection.branches(projectID).branches }.getOrDefault(emptyList())
    }

    // The next form starts from them.
    suspend fun rememberChoices(p: Project, h: HarnessInfo) {
        settings.put(Keys.lastHarness, h.id)
        settings.put(Keys.lastProject, p.id)
        settings.put(Keys.lastModel(h.id), chosenModel ?: "")
        settings.put(Keys.lastEffort(h.id), effort)
        settings.put(Keys.lastMode(h.id), mode)
    }

    // As a draft, the prompt waits in the new session, unsent, for any device to send.
    fun create(asDraft: Boolean = false) {
        val p = project ?: return
        val h = harness ?: return
        creating = true
        error = null
        scope.launch {
            try {
                val s = connection.createSession(
                    NewSession(
                        projectId = p.id, harness = h.id, model = chosenModel, effort = effort.ifEmpty { null },
                        mode = mode.ifEmpty { null },
                        workspace = NewSession.WorkspaceParams(
                            kind = if (useWorktree) Workspace.Kind.WORKTREE else Workspace.Kind.ROOT,
                            branch = branch.takeIf { useWorktree && it.isNotEmpty() },
                            baseRef = baseRef.takeIf { useWorktree && it.isNotEmpty() },
                        ),
                        prompt = prompt,
                        images = attachments.refs.map { it.id }.ifEmpty { null },
                        draft = true.takeIf { asDraft },
                    ),
                )
                rememberChoices(p, h)
                // As rad decides whether to start a turn. A draft is there to
                // read, so the prompt doesn't take the keyboard.
                onCreated(s, !asDraft && prompt.isBlank() && attachments.refs.isEmpty())
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                error = e.message
            } finally {
                creating = false
            }
        }
    }

    // Takes the project off the server's list, which it refuses while the
    // project has sessions that aren't archived.
    fun forget(id: String) {
        forgetError = null
        scope.launch {
            try {
                connection.removeProject(id)
                if (projectID == id) projectID = ""
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                forgetError = e.message
            }
        }
    }

    FullScreenDialog(onDismiss) {
        Screen(
            title = { Text("New session", style = Type.headline) },
            onBack = onDismiss, backIcon = Icons.close, backLabel = "Cancel", background = palette.mantle,
            actions = {
                if (creating) {
                    CircularProgressIndicator(Modifier.size(24.dp).padding(end = 4.dp), color = palette.subtext, strokeWidth = 2.dp)
                } else {
                    val enabled = project != null && harness != null && attachments.ready
                    TextButton({ create() }, enabled = enabled, modifier = Modifier.testTag("start")) {
                        Text("Start", color = if (enabled) palette.accent else palette.overlay, style = Type.headline)
                    }
                }
            },
        ) { padding ->
            Column(Modifier.fillMaxSize().padding(padding).imePadding().verticalScroll(rememberScrollState()).formWidth()) {
                FormSection("Project") {
                    PickerRow(
                        "Project", project?.name ?: "Choose…",
                        listOf("" to "Choose…") + projects.sortedProjects.map { it.id to it.name },
                        tag = "project-picker",
                    ) { projectID = it; useWorktree = false }
                    FormDivider()
                    Row(
                        Modifier.fillMaxWidth().clickable { browsing = true }.padding(12.dp),
                        horizontalArrangement = Arrangement.spacedBy(10.dp), verticalAlignment = Alignment.CenterVertically,
                    ) {
                        Icon(painterResource(Icons.folder), null, tint = palette.accent)
                        Text("Add a folder…", color = palette.accent, style = Type.body)
                    }
                    project?.let {
                        FormDivider()
                        Row(Modifier.padding(start = 12.dp, end = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                            Text(
                                it.path, style = Type.caption.copy(fontFamily = FontFamily.Monospace), color = palette.subtext,
                                modifier = Modifier.weight(1f).padding(vertical = 12.dp),
                            )
                            TextButton({ forget(it.id) }, modifier = Modifier.testTag("forget-project")) {
                                Text("Forget", color = palette.accent, style = Type.body)
                            }
                        }
                        forgetError?.let { e ->
                            Row(
                                Modifier.padding(start = 12.dp, end = 12.dp, bottom = 12.dp),
                                horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically,
                            ) {
                                Icon(painterResource(Icons.warning), null, tint = palette.red, modifier = Modifier.size(16.dp))
                                Text(e, color = palette.red, style = Type.caption)
                            }
                        }
                    }
                }

                AgentSection(harnesses, harnessID) { harnessID = it }

                if (harness != null) {
                    OptionsSection(
                        harness, model = model, customModel = customModel, effort = effort, mode = mode,
                        onModel = { model = it }, onCustomModel = { customModel = it }, onEffort = { effort = it }, onMode = { mode = it },
                    )
                }

                // Only an agent that can pick up its own sessions offers them.
                if (project != null && harness?.caps?.import == true) {
                    FormSection {
                        Row(
                            Modifier.fillMaxWidth().clickable(enabled = !creating, role = Role.Button) { importing = true }.padding(12.dp)
                                .testTag("continue-terminal-session"),
                            horizontalArrangement = Arrangement.spacedBy(10.dp), verticalAlignment = Alignment.CenterVertically,
                        ) {
                            Icon(painterResource(Icons.terminal), null, tint = palette.accent)
                            Text("Continue a terminal session…", color = palette.accent, style = Type.body, modifier = Modifier.weight(1f))
                            Icon(painterResource(Icons.chevronRight), null, tint = palette.overlay)
                        }
                    }
                }

                if (project?.isGitRepo == true) {
                    FormSection(
                        "Workspace",
                        footer = AnnotatedString(
                            if (useWorktree) "The agent works on its own branch in a separate checkout. You can revert its turns."
                            else "The agent works directly in the project folder.",
                        ),
                    ) {
                        SwitchRow("Isolated git worktree", useWorktree) { useWorktree = it }
                        if (useWorktree) {
                            FormDivider()
                            FormTextField(branch, { branch = it }, "Branch (auto)",
                                keyboardOptions = KeyboardOptions(autoCorrectEnabled = false, capitalization = KeyboardCapitalization.None))
                            FormDivider()
                            val options = listOf("" to "Current HEAD") + branches.map { it to it }
                            PickerRow("Based on", options.firstOrNull { it.first == baseRef }?.second ?: baseRef, options) { baseRef = it }
                        }
                    }
                }

                // An older server would start the session with the prompt.
                val drafts = info?.drafts == true
                val footer = "Optional. Without one, the session starts empty and you write the first prompt in it." +
                    if (drafts) " Save as Draft keeps the prompt in the session, unsent, to send later from any device." else ""
                FormSection("Prompt", footer = AnnotatedString(footer)) {
                    FormTextField(prompt, { prompt = it }, "What should the agent do?", style = messageStyle(), maxLines = 12, minLines = 4, modifier = Modifier.testTag("new-prompt"))
                    if (!attachments.isEmpty) {
                        FormDivider()
                        AttachmentStrip(attachments, Modifier.padding(12.dp))
                    }
                    FormDivider()
                    AttachMenu(attachments, title = "Add Images")
                    if (drafts) {
                        FormDivider()
                        val enabled = project != null && harness != null && attachments.ready && !creating &&
                            (prompt.isNotBlank() || !attachments.isEmpty)
                        val color = if (enabled) palette.accent else palette.overlay
                        Row(
                            Modifier.fillMaxWidth().clickable(enabled = enabled, role = Role.Button) { create(asDraft = true) }.padding(12.dp)
                                .testTag("save-as-draft"),
                            horizontalArrangement = Arrangement.spacedBy(10.dp),
                            verticalAlignment = Alignment.CenterVertically,
                        ) {
                            Icon(painterResource(Icons.saveDraft), null, tint = color)
                            Text("Save as Draft", color = color, style = Type.body)
                        }
                    }
                }

                error?.let { FormError(it) }
            }
        }
    }

    val p = project
    val h = harness
    if (importing && p != null && h != null && h.caps.import) {
        ImportSessionDialog(
            connection, p, h, model = chosenModel, effort = effort.ifEmpty { null }, mode = mode.ifEmpty { null },
            onDismiss = { importing = false },
            onImported = { s ->
                rememberChoices(p, h)
                importing = false
                // It opens at the conversation so far, ready for the next prompt.
                onCreated(s, true)
            },
        )
    }

    if (browsing) {
        DirectoryBrowserDialog(connection, onDismiss = { browsing = false }) { picked ->
            browsing = false
            scope.launch {
                try {
                    projectID = connection.addProject(picked).id
                } catch (e: CancellationException) {
                    throw e
                } catch (e: Exception) {
                    error = e.message
                }
            }
        }
    }
}

/** The model id to request: the one picked, or the one typed for Custom…; null for the agent's default. */
internal fun chosenModel(harness: HarnessInfo?, model: String, customModel: String): String? =
    (if (model == CUSTOM || harness?.models.isNullOrEmpty()) customModel else model).ifEmpty { null }

/**
 * The model, effort and permission mode to start from with an agent, as the
 * Options section holds them: the ones given where the agent still offers
 * them, else its defaults. New Session gives the ones last used with the
 * agent, Fork Session the source session's.
 */
internal data class AgentChoices(val model: String = "", val customModel: String = "", val effort: String = "", val mode: String = "") {
    companion object {
        fun of(h: HarnessInfo, model: String?, effort: String?, mode: String?): AgentChoices {
            val models = h.models ?: emptyList()
            var picked = ""
            var custom = ""
            model?.takeIf { it.isNotEmpty() }?.let { m ->
                if (models.any { it.id == m }) picked = m
                else if (h.caps.freeModel) {
                    picked = if (models.isEmpty()) "" else CUSTOM
                    custom = m
                }
            }
            val chosen = chosenModel(h, picked, custom)
            return AgentChoices(
                model = picked,
                customModel = custom,
                effort = effort?.takeIf { e -> h.efforts(chosen).any { it.id == e } } ?: "",
                mode = mode?.takeIf { m -> h.modes?.any { it.id == m } == true } ?: h.defaultMode ?: h.modes?.firstOrNull()?.id ?: "",
            )
        }
    }
}

/** The agents on the server, to choose one; the ones not installed or signed in can't be. */
@Composable
internal fun AgentSection(harnesses: List<HarnessInfo>?, selected: String, onSelect: (String) -> Unit) {
    FormSection("Agent") {
        when {
            harnesses == null -> Row(Modifier.padding(12.dp), horizontalArrangement = Arrangement.spacedBy(10.dp), verticalAlignment = Alignment.CenterVertically) {
                CircularProgressIndicator(Modifier.size(18.dp), color = palette.subtext, strokeWidth = 2.dp)
                Text("Checking installed agents…", color = palette.subtext, style = Type.body)
            }
            harnesses.isEmpty() -> Text(
                "No agents are installed on the server. Install Claude Code, Codex, Pi or an ACP agent there.",
                color = palette.subtext, style = Type.body, modifier = Modifier.padding(12.dp),
            )
            else -> harnesses.forEachIndexed { i, h ->
                if (i > 0) FormDivider()
                HarnessChoiceRow(h, h.id == selected) { onSelect(h.id) }
            }
        }
    }
}

/** The agent's model, effort and permission mode, as far as it offers them. */
@Composable
internal fun OptionsSection(
    harness: HarnessInfo,
    model: String,
    customModel: String,
    effort: String,
    mode: String,
    onModel: (String) -> Unit,
    onCustomModel: (String) -> Unit,
    onEffort: (String) -> Unit,
    onMode: (String) -> Unit,
) {
    FormSection("Options") {
        var first = true
        fun divider() = if (first) { first = false; false } else true
        val models = harness.models ?: emptyList()
        if (models.isNotEmpty()) {
            divider()
            val options = listOf("" to "Default") + models.map { it.id to it.name } + (if (harness.caps.freeModel) listOf(CUSTOM to "Custom…") else emptyList())
            PickerRow("Model", options.firstOrNull { it.first == model }?.second ?: "Default", options, onSelect = onModel)
        }
        if (model == CUSTOM || (harness.caps.freeModel && models.isEmpty())) {
            if (divider()) FormDivider()
            FormTextField(customModel, onCustomModel, "Model id",
                keyboardOptions = KeyboardOptions(autoCorrectEnabled = false, capitalization = KeyboardCapitalization.None))
        }
        val efforts = harness.efforts(chosenModel(harness, model, customModel))
        if (efforts.isNotEmpty()) {
            if (divider()) FormDivider()
            val options = listOf("" to "Default") + efforts.map { it.id to it.name }
            PickerRow("Effort", options.firstOrNull { it.first == effort }?.second ?: "Default", options, onSelect = onEffort)
            efforts.firstOrNull { it.id == effort }?.description?.takeIf { it.isNotEmpty() }?.let { FormNote(it) }
        }
        val modes = harness.modes ?: emptyList()
        if (modes.isNotEmpty()) {
            if (divider()) FormDivider()
            PickerRow("Permissions", modes.firstOrNull { it.id == mode }?.name ?: mode, modes.map { it.id to it.name }, onSelect = onMode)
            modes.firstOrNull { it.id == mode }?.description?.takeIf { it.isNotEmpty() }?.let { FormNote(it) }
        }
    }
}

/** Why the form's request failed, at its foot. */
@Composable
internal fun FormError(message: String) {
    FormSection {
        Row(Modifier.padding(12.dp), horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
            Icon(painterResource(Icons.warning), null, tint = palette.red, modifier = Modifier.size(18.dp))
            Text(message, color = palette.red, style = Type.subheadline)
        }
    }
}

/** A row that opens a menu of choices, as a Picker in a Form. A long value is cut short. */
@Composable
fun PickerRow(title: String, value: String, options: List<Pair<String, String>>, tag: String? = null, onSelect: (String) -> Unit) {
    var open by remember { mutableStateOf(false) }
    Box {
        Row(
            Modifier.fillMaxWidth().clickable { open = true }.padding(horizontal = 12.dp, vertical = 12.dp)
                .then(if (tag != null) Modifier.testTag(tag) else Modifier),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(title, style = Type.body, color = palette.text)
            Text(
                value, style = Type.body, color = palette.subtext, maxLines = 1, overflow = TextOverflow.Ellipsis, textAlign = TextAlign.End,
                modifier = Modifier.weight(1f).padding(start = 12.dp),
            )
            Icon(painterResource(Icons.switcher), null, Modifier.size(18.dp).padding(start = 2.dp), tint = palette.subtext)
        }
        DropdownMenu(open, { open = false }, containerColor = palette.mantle, modifier = Modifier.align(Alignment.TopEnd)) {
            for ((id, label) in options) {
                MenuItem(label, if (label == value) Icons.check else null) { open = false; onSelect(id) }
            }
        }
    }
}

@Composable
private fun FormNote(text: String) {
    Text(text, style = Type.caption, color = palette.subtext, modifier = Modifier.padding(start = 12.dp, end = 12.dp, bottom = 10.dp))
}

@Composable
private fun HarnessChoiceRow(harness: HarnessInfo, selected: Boolean, onClick: () -> Unit) {
    Row(
        Modifier.fillMaxWidth().clickable(enabled = harness.usable, onClick = onClick).alpha(if (harness.usable) 1f else 0.5f)
            .padding(horizontal = 12.dp, vertical = 10.dp).testTag("harness-${harness.id}"),
        horizontalArrangement = Arrangement.spacedBy(12.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Box(Modifier.width(28.dp), contentAlignment = Alignment.Center) { HarnessIcon(harness.id, Modifier.size(22.dp)) }
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            Row(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.CenterVertically) {
                Text(harness.name, style = Type.body, color = palette.text)
                harness.version?.let { Text(it, style = Type.caption2.copy(fontFamily = FontFamily.Monospace), color = palette.overlay, maxLines = 1) }
            }
            if (!harness.usable) harness.hint?.let { Text(it, style = Type.caption, color = palette.yellow) }
        }
        if (selected) Icon(painterResource(Icons.check), "Selected", tint = palette.accent)
    }
}

/** Picks a folder on the server to add as a project, starting from its roots (`fs.list`). */
@Composable
private fun DirectoryBrowserDialog(connection: ServerConnection, onDismiss: () -> Unit, onPick: (String) -> Unit) {
    // The folders opened, from the roots down; empty shows the roots.
    val path = remember { mutableStateListOf<String>() }
    var listing by remember { mutableStateOf<FSListing?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    val current = path.lastOrNull()
    LaunchedEffect(current) {
        listing = null
        error = null
        try {
            listing = connection.listDirectory(current)
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            error = e.message
        }
    }
    FullScreenDialog({ if (path.isEmpty()) onDismiss() else path.removeAt(path.lastIndex) }) {
        Screen(
            title = { Text(current?.let { File(it).name } ?: "Folders", style = Type.headline) },
            onBack = { if (path.isEmpty()) onDismiss() else path.removeAt(path.lastIndex) },
            backIcon = if (path.isEmpty()) Icons.close else Icons.back,
            backLabel = if (path.isEmpty()) "Cancel" else "Back",
            background = palette.mantle,
            actions = {
                if (current != null) {
                    TextButton({ onPick(current) }, modifier = Modifier.testTag("use-folder")) {
                        Text("Use folder", color = palette.accent, style = Type.headline)
                    }
                }
            },
        ) { padding ->
            val l = listing
            when {
                error != null -> Text(error!!, color = palette.red, style = Type.body, modifier = Modifier.padding(padding).padding(16.dp))
                l == null -> Loading("Loading…", Modifier.padding(padding))
                l.entries.isEmpty() -> Text("No subfolders", color = palette.subtext, style = Type.body, modifier = Modifier.padding(padding).padding(16.dp))
                else -> LazyColumn(Modifier.fillMaxSize().padding(padding)) {
                    items(l.entries, key = { it.path }) { e ->
                        Row(
                            Modifier.fillMaxWidth().background(palette.base).clickable { path.add(e.path) }.padding(horizontal = 16.dp, vertical = 12.dp),
                            horizontalArrangement = Arrangement.spacedBy(12.dp),
                            verticalAlignment = Alignment.CenterVertically,
                        ) {
                            Icon(painterResource(Icons.folder), null, tint = palette.accent)
                            Text(if (current == null) e.path else e.name, style = Type.body, color = palette.text, modifier = Modifier.weight(1f, fill = false))
                            if (e.isGitRepo) {
                                Text(
                                    "git", style = Type.caption2.copy(fontWeight = FontWeight.SemiBold), color = palette.accent,
                                    modifier = Modifier.background(palette.accent.copy(alpha = 0.15f), RoundedCornerShape(50)).padding(horizontal = 5.dp, vertical = 1.dp),
                                )
                            }
                            Box(Modifier.weight(1f))
                            Icon(painterResource(Icons.chevronRight), null, tint = palette.overlay)
                        }
                    }
                }
            }
        }
    }
}
