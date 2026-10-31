package dev.remoteagent.app.ui

import android.content.Intent
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.core.net.toUri
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import dev.remoteagent.core.net.RpcError
import dev.remoteagent.core.protocol.AgentSpec
import dev.remoteagent.core.protocol.HarnessInfo
import dev.remoteagent.core.protocol.ServerSettings
import dev.remoteagent.core.protocol.ServerTask
import dev.remoteagent.core.protocol.UpdateInfo
import dev.remoteagent.core.store.ServerConnection
import dev.remoteagent.core.store.ServerTaskProgress
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch

/**
 * A server's settings, as a sheet: rad's version and its updates, the agents
 * on the server, which it updates and signs in, the agent new sessions start
 * with, and the system agent. Opened
 * from the server's menus ("Server Settings…"). The tasks it starts, or
 * finds running, are followed while it is open (ServerTaskProgress) and shown
 * in TaskSheet. Mirrors apple/Shared/Views/ServerSettingsView.swift.
 */
@Composable
fun ServerSettingsDialog(connection: ServerConnection, onDismiss: () -> Unit) {
    val scope = rememberCoroutineScope()
    val server by connection.server.collectAsStateWithLifecycle()
    val info by connection.info.collectAsStateWithLifecycle()
    val harnesses by connection.harnesses.collectAsStateWithLifecycle()

    var update by remember { mutableStateOf<UpdateInfo?>(null) }
    var checking by remember { mutableStateOf(false) }
    var radError by remember { mutableStateOf<String?>(null) }
    // An older rad, without the methods for managing it (`method_not_found`).
    var radTooOld by remember { mutableStateOf(false) }
    var settingsUnsupported by remember { mutableStateOf(false) }
    // The server's settings, and the ones shown: another while a change is on its way.
    var saved by remember { mutableStateOf<ServerSettings?>(null) }
    var chosen by remember { mutableStateOf<ServerSettings?>(null) }
    // Why the settings couldn't be loaded, and a change that failed, beside the section it was made in.
    var settingsError by remember { mutableStateOf<String?>(null) }
    var saveError by remember { mutableStateOf<Pair<AgentField, String?>?>(null) }
    var agentsError by remember { mutableStateOf<String?>(null) }
    // What is starting a task: RAD, or an agent's id.
    var starting by remember { mutableStateOf<String?>(null) }
    val tracked = remember { mutableStateListOf<ServerTaskProgress>() }
    var shown by remember { mutableStateOf<ServerTaskProgress?>(null) }
    var confirmingUpdate by remember { mutableStateOf(false) }

    // Polling ends with the screen; the tasks go on on the server.
    DisposableEffect(Unit) { onDispose { tracked.forEach { it.stop() } } }

    fun track(t: ServerTask): ServerTaskProgress {
        tracked.firstOrNull { it.id == t.id }?.let { return it }
        // An earlier one of the same kind and agent has ended: this one takes its row.
        for (old in tracked.filter { it.kind == t.kind && it.harness == t.harness }) {
            old.stop()
            tracked.remove(old)
        }
        return ServerTaskProgress(connection, t).also { tracked += it }
    }

    suspend fun check(refresh: Boolean) {
        checking = true
        radError = null
        try {
            update = connection.checkUpdate(refresh)
            radTooOld = false
        } catch (e: CancellationException) {
            throw e
        } catch (e: RpcError) {
            if (e.code == METHOD_NOT_FOUND) {
                radTooOld = true
                update = null
            } else {
                radError = "Couldn't check for updates: ${e.message}"
            }
        } catch (e: Exception) {
            radError = "Couldn't check for updates: ${e.message}"
        } finally {
            checking = false
        }
    }

    suspend fun loadSettings() {
        settingsError = null
        try {
            saved = connection.settings()
            chosen = saved
            settingsUnsupported = false
        } catch (e: CancellationException) {
            throw e
        } catch (e: RpcError) {
            if (e.code == METHOD_NOT_FOUND) settingsUnsupported = true else settingsError = "Couldn't load the settings: ${e.message}"
        } catch (e: Exception) {
            settingsError = "Couldn't load the settings: ${e.message}"
        }
    }

    // On open, and again once rad has come back as another version, which
    // may manage itself differently.
    LaunchedEffect(info?.version) {
        launch { check(refresh = false) }
        launch { loadSettings() }
    }
    LaunchedEffect(Unit) {
        // Tasks still running, as one started from another device or before this was closed.
        launch { runCatching { connection.tasks() }.getOrNull()?.filter { it.running }?.forEach { track(it) } }
        // From rad's cache, but newer than the connection's: an agent may have
        // been signed out by a failed turn since (its hint says so).
        connection.refreshHarnesses()
    }

    fun start(key: String, onError: (String?) -> Unit, call: suspend () -> ServerTask) {
        starting = key
        onError(null)
        scope.launch {
            try {
                shown = track(call())
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                onError(e.message)
            } finally {
                if (starting == key) starting = null
            }
        }
    }

    fun updateRad() = start(RAD, { radError = it }) { connection.updateServer() }

    fun save(field: AgentField, new: ServerSettings) {
        chosen = new
        saveError = null
        scope.launch {
            try {
                val kept = connection.setSettings(new)
                saved = kept
                chosen = kept
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                saveError = field to e.message
                chosen = saved
            }
        }
    }

    // The tasks followed here that haven't ended.
    val running = buildList {
        for (p in tracked) key(p.id) { if (!p.phase.collectAsStateWithLifecycle().value.ended) add(p) }
    }

    FullScreenDialog(onDismiss) {
        Screen(
            title = { ScreenTitle(server.name) { Text("Server", style = Type.caption, color = palette.subtext) } },
            onBack = onDismiss, backIcon = Icons.close, backLabel = "Done", background = palette.mantle,
        ) { padding ->
            Column(Modifier.fillMaxSize().padding(padding).verticalScroll(rememberScrollState()).formWidth().testTag("server-settings")) {
                val u = update
                FormSection("rad", footer = if (radTooOld) null else radFooter(u)) {
                    // As server.info has it: current, and fresher while a check of the new version is on its way.
                    ValueRow("Version", info?.version ?: u?.current ?: "")
                    FormDivider()
                    if (radTooOld) {
                        Text(
                            inlineCode("This rad is too old to update from here. Update it on the computer with `rad update`, or from the Remote Agent Server app's menu."),
                            style = Type.subheadline, color = palette.subtext, modifier = Modifier.padding(12.dp).testTag("rad-too-old"),
                        )
                        return@FormSection
                    }
                    val radTask = running.firstOrNull { it.kind == ServerTask.UPDATE_SERVER }
                    when {
                        radTask != null -> ProgressRow("Updating…", Modifier.clickable { shown = radTask })
                        starting == RAD -> ProgressRow("Updating…")
                        checking -> ProgressRow("Checking for updates…")
                        u == null -> {}
                        u.available && u.via != null -> Column(Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                            Text("rad ${u.latest} is available.", style = Type.body, color = palette.text)
                            Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                                Button(
                                    { if (u.busy.isEmpty()) updateRad() else confirmingUpdate = true },
                                    colors = ButtonDefaults.buttonColors(containerColor = palette.accent, contentColor = palette.onAccent),
                                    shape = RoundedCornerShape(Metrics.Corner.card),
                                    modifier = Modifier.testTag("update-rad"),
                                ) { Text("Update to ${u.latest}") }
                                webURL(u.url)?.let { url -> LinkButton("What's New", url) }
                            }
                        }
                        u.available -> Column(Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(2.dp)) {
                            Text("rad ${u.latest} is available.", style = Type.body, color = palette.text)
                            u.reason?.let { Text(it, style = Type.subheadline, color = palette.subtext) }
                        }
                        u.latest != null && u.reason == null -> Text("Up to date.", style = Type.body, color = palette.text, modifier = Modifier.padding(12.dp))
                        else -> u.reason?.let { Text(it, style = Type.subheadline, color = palette.subtext, modifier = Modifier.padding(12.dp)) }
                    }
                    radError?.let { ErrorLine(it) }
                    FormDivider()
                    ActionRow("Check Again", enabled = !checking, Modifier.testTag("check-again")) { scope.launch { check(refresh = true) } }
                }

                // An older rad offers neither, so there's nothing to explain.
                val maintained = harnesses?.any { it.caps.update || it.caps.login } == true
                FormSection(
                    "Agents",
                    footer = if (maintained) {
                        footer(
                            "Update runs the agent's own updater on the server. Sign In shows the agent's sign-in page here, and takes the code it gives you.",
                            agentsError,
                        )
                    } else {
                        agentsError?.let { footer("", it) }
                    },
                ) {
                    val list = harnesses
                    when {
                        list == null -> ProgressRow("Checking installed agents…")
                        list.isEmpty() -> Text("No agents are installed on the server.", style = Type.body, color = palette.subtext, modifier = Modifier.padding(12.dp))
                        else -> list.forEachIndexed { i, h ->
                            if (i > 0) FormDivider()
                            AgentRow(
                                h,
                                task = running.firstOrNull { it.harness == h.id && it.kind != ServerTask.UPDATE_SERVER },
                                starting = starting == h.id,
                                onUpdate = { start(h.id, { agentsError = it }) { connection.updateHarness(h.id) } },
                                onSignIn = { start(h.id, { agentsError = it }) { connection.loginHarness(h.id) } },
                                onOpen = { shown = it },
                            )
                        }
                    }
                }

                // An older rad has no settings: no default agent, and no system agent.
                if (!settingsUnsupported) {
                    val all = harnesses ?: emptyList()
                    fun sectionError(field: AgentField) = settingsError ?: saveError?.takeIf { it.first == field }?.second
                    FormSection(
                        "New Sessions",
                        footer = footer(
                            "New sessions start with this agent, model and effort, on every device, and so do the sessions agents start " +
                                "with rad's tools unless they choose their own. With Last Used, each device starts with the agent it used last.",
                            sectionError(AgentField.DEFAULT),
                        ),
                    ) {
                        AgentSpecRows(chosen, settingsError, AgentField.DEFAULT, all, offered = all, none = "Last Used", save = ::save)
                    }
                    FormSection(
                        "System Agent",
                        footer = footer(
                            "Names new sessions and their worktree branches from the first prompt, in a call without tools that leaves no history " +
                                "with the agent. A small, fast model is best. With None, a session's title is its prompt's first line.",
                            sectionError(AgentField.SYSTEM),
                        ),
                    ) {
                        AgentSpecRows(chosen, settingsError, AgentField.SYSTEM, all, offered = all.filter { it.caps.oneShot }, none = "None", save = ::save)
                    }
                }
            }
        }
    }

    if (confirmingUpdate) {
        val busy = update?.busy ?: emptyList()
        ConfirmDialog(
            title = "Interrupt ${count(busy.size, "session")}?",
            message = "Updating restarts rad, which interrupts ${busy.joinToString(", ") { "“$it”" }}.",
            confirm = "Update Anyway",
            onConfirm = ::updateRad,
            onDismiss = { confirmingUpdate = false },
        )
    }
    shown?.let { p ->
        TaskSheet(p, agentName = harnesses?.firstOrNull { it.id == p.harness }?.name, onDismiss = { shown = null })
    }
}

private const val RAD = "rad"
private const val METHOD_NOT_FOUND = "method_not_found"

/** How rad installs an update, and which sessions updating now would interrupt. */
internal fun radFooter(u: UpdateInfo?): AnnotatedString? {
    val how = when (u?.via) {
        UpdateInfo.VIA_APP -> "The Remote Agent Server app on the Mac installs the update and restarts, with rad."
        UpdateInfo.VIA_RAD -> "rad installs the update and restarts. Sessions reconnect by themselves."
        else -> return null
    }
    val busy = if (u.available && u.busy.isNotEmpty()) {
        " Updating now interrupts ${count(u.busy.size, "busy session")}: ${u.busy.joinToString(", ")}."
    } else {
        ""
    }
    return AnnotatedString(how + busy)
}

/** "1 session", "2 sessions". */
internal fun count(n: Int, noun: String) = if (n == 1) "1 $noun" else "$n ${noun}s"

/** A section's footer, after an error in red. */
@Composable
private fun footer(text: String, error: String?): AnnotatedString {
    val red = palette.red
    return buildAnnotatedString {
        if (error != null) {
            withStyle(SpanStyle(color = red)) { append(error) }
            if (text.isNotEmpty()) append("\n")
        }
        append(text)
    }
}

/**
 * The settings that name an agent, with its model and effort: how to read one,
 * and to set it in the settings with the others as they are, since
 * `server.setSettings` replaces them all. `tag` starts its rows' test tags.
 */
internal enum class AgentField(val tag: String) {
    /** The one new sessions start with. */
    DEFAULT("default"),
    /** The system agent. */
    SYSTEM("system");

    fun get(s: ServerSettings): AgentSpec? = when (this) {
        DEFAULT -> s.defaultAgent
        SYSTEM -> s.systemAgent
    }

    fun set(s: ServerSettings, agent: AgentSpec?): ServerSettings = when (this) {
        DEFAULT -> s.copy(defaultAgent = agent)
        SYSTEM -> s.copy(systemAgent = agent)
    }
}

/** A saved choice the server no longer offers, to show as it is. */
private fun unlisted(id: String?, offered: List<Pair<String, String>>, label: (String) -> String) =
    if (id != null && offered.none { it.first == id }) listOf(id to label(id)) else emptyList()

/**
 * The agents to choose from for a setting, as ids and names: `none` first,
 * with the id "", then the ones `offered`, then the one `chosen` if they
 * don't include it.
 */
internal fun agentOptions(none: String, offered: List<HarnessInfo>, chosen: String?, harnesses: List<HarnessInfo>): List<Pair<String, String>> {
    val listed = offered.map { it.id to it.name }
    return listOf("" to none) + listed + unlisted(chosen, listed) { id -> harnesses.firstOrNull { it.id == id }?.name ?: id }
}

/**
 * The agent a setting names, from the ones `offered` (`none` labels the
 * choice of none), and its model and effort; while the settings load, or when
 * they couldn't be, a row says so. Each change saves the whole settings, with
 * only this one changed.
 */
@Composable
private fun AgentSpecRows(
    settings: ServerSettings?,
    loadError: String?,
    field: AgentField,
    harnesses: List<HarnessInfo>,
    offered: List<HarnessInfo>,
    none: String,
    save: (AgentField, ServerSettings) -> Unit,
) {
    if (settings == null) {
        if (loadError == null) ProgressRow("Loading…") else Text("Unavailable", style = Type.body, color = palette.subtext, modifier = Modifier.padding(12.dp))
        return
    }
    val agent = field.get(settings)
    fun set(new: AgentSpec?) = save(field, field.set(settings, new))

    val agents = agentOptions(none, offered, agent?.harness, harnesses)
    PickerRow("Agent", agents.firstOrNull { it.first == (agent?.harness ?: "") }?.second ?: none, agents, tag = "${field.tag}-agent") { id ->
        if (id != (agent?.harness ?: "")) set(if (id.isEmpty()) null else AgentSpec(id))
    }
    if (agent == null) return
    val h = harnesses.firstOrNull { it.id == agent.harness }

    val models = (h?.models ?: emptyList()).map { it.id to it.name }
    val modelOptions = listOf("" to "Default") + models + unlisted(agent.model, models) { it }
    FormDivider()
    PickerRow("Model", modelOptions.firstOrNull { it.first == (agent.model ?: "") }?.second ?: "Default", modelOptions, tag = "${field.tag}-model") { m ->
        val model = m.ifEmpty { null }
        // Models support different efforts.
        val effort = agent.effort?.takeIf { e -> h?.efforts(model)?.any { it.id == e } == true }
        set(agent.copy(model = model, effort = effort))
    }

    val efforts = (h?.efforts(agent.model) ?: emptyList()).map { it.id to it.name }
    if (efforts.isEmpty()) return
    val effortOptions = listOf("" to "Default") + efforts + unlisted(agent.effort, efforts) { it }
    FormDivider()
    PickerRow("Effort", effortOptions.firstOrNull { it.first == (agent.effort ?: "") }?.second ?: "Default", effortOptions, tag = "${field.tag}-effort") { e ->
        set(agent.copy(effort = e.ifEmpty { null }))
    }
}

/**
 * An agent: its version, whether it is signed in, and its actions. While a
 * task of its runs, the row says so, and a tap shows the task.
 */
@Composable
private fun AgentRow(
    h: HarnessInfo,
    task: ServerTaskProgress?,
    starting: Boolean,
    onUpdate: () -> Unit,
    onSignIn: () -> Unit,
    onOpen: (ServerTaskProgress) -> Unit,
) {
    var menu by remember { mutableStateOf(false) }
    Row(
        Modifier.fillMaxWidth().then(if (task != null) Modifier.clickable { onOpen(task) } else Modifier)
            .padding(start = 12.dp, end = 4.dp, top = 6.dp, bottom = 6.dp).testTag("agent-${h.id}"),
        horizontalArrangement = Arrangement.spacedBy(12.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Box(Modifier.width(28.dp), contentAlignment = Alignment.Center) { HarnessIcon(h.id, Modifier.size(22.dp)) }
        Column(Modifier.weight(1f).padding(vertical = 4.dp), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            Row(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.CenterVertically) {
                Text(h.name, style = Type.body, color = palette.text)
                h.version?.let { Text(it, style = Type.body, color = palette.subtext, maxLines = 1) }
            }
            when {
                task != null -> Text(if (task.kind == ServerTask.LOGIN) "Signing in…" else "Updating…", style = Type.subheadline, color = palette.subtext)
                h.authOk -> Text("Ready", style = Type.subheadline, color = palette.green)
                else -> Text(h.hint?.takeIf { it.isNotEmpty() } ?: "Not signed in", style = Type.subheadline, color = palette.yellow)
            }
        }
        if (task != null || starting) {
            CircularProgressIndicator(Modifier.padding(end = 12.dp).size(18.dp), color = palette.subtext, strokeWidth = 2.dp)
            return@Row
        }
        if (h.caps.login && !h.authOk) {
            TextButton(onSignIn, modifier = Modifier.testTag("sign-in-${h.id}")) { Text("Sign In", color = palette.accent, style = Type.body) }
        }
        val signInAgain = h.caps.login && h.authOk
        if (h.caps.update || signInAgain) {
            Box {
                IconButton({ menu = true }, Modifier.testTag("agent-menu-${h.id}")) {
                    Icon(painterResource(Icons.more), "${h.name} actions", Modifier.size(20.dp), tint = palette.subtext)
                }
                DropdownMenu(menu, { menu = false }, containerColor = palette.mantle) {
                    if (h.caps.update) MenuItem("Update", Icons.update) { menu = false; onUpdate() }
                    if (signInAgain) MenuItem("Sign In Again", Icons.signIn) { menu = false; onSignIn() }
                }
            }
        }
    }
}

@Composable
private fun ValueRow(title: String, value: String) {
    Row(Modifier.fillMaxWidth().padding(12.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(title, style = Type.body, color = palette.text, modifier = Modifier.weight(1f))
        Text(value, style = Type.body, color = palette.subtext, maxLines = 1)
    }
}

/** Something going on: a spinner and what it is. */
@Composable
private fun ProgressRow(label: String, modifier: Modifier = Modifier) {
    Row(modifier.fillMaxWidth().padding(12.dp), horizontalArrangement = Arrangement.spacedBy(10.dp), verticalAlignment = Alignment.CenterVertically) {
        CircularProgressIndicator(Modifier.size(18.dp), color = palette.subtext, strokeWidth = 2.dp)
        Text(label, style = Type.body, color = palette.subtext)
    }
}

@Composable
private fun ActionRow(label: String, enabled: Boolean, modifier: Modifier = Modifier, onClick: () -> Unit) {
    Text(
        label, style = Type.body, color = if (enabled) palette.accent else palette.overlay,
        modifier = modifier.fillMaxWidth().clickable(enabled = enabled, onClick = onClick).padding(12.dp),
    )
}

@Composable
private fun ErrorLine(text: String) {
    Row(Modifier.padding(start = 12.dp, end = 12.dp, bottom = 12.dp), horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
        Icon(painterResource(Icons.warning), null, tint = palette.red, modifier = Modifier.size(16.dp))
        Text(text, color = palette.red, style = Type.subheadline)
    }
}

/** A link that opens in the browser. */
@Composable
private fun LinkButton(title: String, url: String) {
    val context = LocalContext.current
    TextButton({ runCatching { context.startActivity(Intent(Intent.ACTION_VIEW, url.toUri())) } }) {
        Text(title, color = palette.accent, style = Type.body)
    }
}
