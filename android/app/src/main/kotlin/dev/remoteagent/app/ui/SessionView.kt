package dev.remoteagent.app.ui

import android.Manifest
import android.content.pm.PackageManager
import androidx.activity.compose.BackHandler
import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.tween
import androidx.compose.foundation.gestures.scrollBy
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.TextButton
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.input.ImeAction
import dev.remoteagent.core.store.TranscriptFind
import kotlinx.coroutines.flow.filterNotNull
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.withTimeoutOrNull
import kotlin.time.Duration.Companion.seconds
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.heightIn
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import dev.remoteagent.core.protocol.commandQuery
import dev.remoteagent.core.protocol.commandSuggestions
import androidx.compose.foundation.background
import dev.remoteagent.app.platform.Dictation
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.material3.VerticalDivider
import androidx.compose.material3.adaptive.currentWindowAdaptiveInfoV2
import androidx.window.core.layout.WindowSizeClass
import dev.remoteagent.app.platform.AppSettings.Keys
import dev.remoteagent.app.platform.state
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.isImeVisible
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.ime
import androidx.compose.foundation.layout.navigationBars
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.union
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.IconButtonDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.MutableState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.focus.onFocusChanged
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.KeyEventType
import androidx.compose.ui.input.key.isCtrlPressed
import androidx.compose.ui.input.key.isShiftPressed
import androidx.compose.ui.input.key.key
import androidx.compose.ui.input.key.onPreviewKeyEvent
import androidx.compose.ui.input.key.type
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.TextFieldValue
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.compose.currentStateAsState
import dev.remoteagent.app.platform.Platform
import dev.remoteagent.core.protocol.BackgroundTask
import dev.remoteagent.core.protocol.HarnessInfo
import dev.remoteagent.core.protocol.Item
import dev.remoteagent.core.protocol.Session
import dev.remoteagent.core.protocol.SessionStatus
import dev.remoteagent.core.protocol.SlashCommand
import dev.remoteagent.core.protocol.Turn
import dev.remoteagent.core.protocol.TurnStatus
import dev.remoteagent.core.protocol.Workspace
import dev.remoteagent.core.store.Outgoing
import dev.remoteagent.core.store.ServerConnection
import dev.remoteagent.core.store.SessionStore
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch

/** The connection the session on screen belongs to, for its images and commands. */
val LocalConnection = staticCompositionLocalOf<ServerConnection> { error("no ServerConnection") }

/**
 * A session's transcript and composer. With `focusPrompt`, a session just
 * started empty, the prompt takes the keyboard. With `focus`, as from a
 * search result, it opens at that item, which it flashes, then calls
 * `onFocused`; at the foot if the item never comes. `onForked` opens a fork
 * made from its menu, as a new session opens. Its find bar (Find in Session
 * in its menu, or Ctrl+F) marks the items that match and steps through
 * them. Mirrors apple/Shared/Views/SessionView.swift.
 */
@Composable
fun SessionView(
    connection: ServerConnection,
    store: SessionStore,
    focusPrompt: Boolean = false,
    focus: FocusRequest? = null,
    onFocused: (FocusRequest) -> Unit = {},
    onBack: () -> Unit,
    onForked: (Session, empty: Boolean) -> Unit,
) {
    CompositionLocalProvider(LocalConnection provides connection) {
        SessionScreen(connection, store, focusPrompt, focus, onFocused, onBack, onForked)
    }
}

/** An item that flashes as it opens: one each time, so that the same item can flash again. */
private class Flash(val itemId: String)

private sealed interface Row {
    val key: String

    data class ItemRow(val item: Item) : Row {
        override val key get() = item.id
    }

    data class Footer(val turn: Turn) : Row {
        override val key get() = "turn-" + turn.id
    }
}

/** The composer's text. The store keeps each change, for when the session is open again. */
@Composable
private fun rememberDraft(store: SessionStore): MutableState<String> = remember(store) {
    val text = mutableStateOf(store.draft)
    object : MutableState<String> {
        override var value: String
            get() = text.value
            set(value) {
                text.value = value
                store.draft = value
            }
        override fun component1() = value
        override fun component2(): (String) -> Unit = { value = it }
    }
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun SessionScreen(
    connection: ServerConnection,
    store: SessionStore,
    focusPrompt: Boolean,
    focus: FocusRequest?,
    onFocused: (FocusRequest) -> Unit,
    onBack: () -> Unit,
    onForked: (Session, empty: Boolean) -> Unit,
) {
    val context = LocalContext.current
    val uiScope = rememberCoroutineScope()
    val storeSession by store.session.collectAsStateWithLifecycle()
    val sessions by connection.sessions.collectAsStateWithLifecycle()
    val harnesses by connection.harnesses.collectAsStateWithLifecycle()
    val projects by connection.projects.collectAsStateWithLifecycle()
    val info by connection.info.collectAsStateWithLifecycle()
    val transcript by store.transcript.collectAsStateWithLifecycle()
    val children by store.children.collectAsStateWithLifecycle()
    val turns by store.turns.collectAsStateWithLifecycle()
    val outbox by store.outbox.collectAsStateWithLifecycle()
    val synced by store.synced.collectAsStateWithLifecycle()

    val session = storeSession ?: sessions[store.sessionID]
    val harness = session?.let { s -> harnesses?.firstOrNull { it.id == s.harness } }
    val project = session?.let { projects[it.projectId] }
    val running = session?.status == SessionStatus.RUNNING || session?.status == SessionStatus.AWAITING_APPROVAL
    val archived = session?.archived == true

    var draft by rememberDraft(store)
    var focused by remember { mutableStateOf(false) }
    val focusRequester = remember { FocusRequester() }
    val connectionState by connection.state.collectAsStateWithLifecycle()
    var commands by remember(store) { mutableStateOf<List<SlashCommand>>(emptyList()) }
    var loadingCommands by remember(store) { mutableStateOf(false) }
    var commandsError by remember(store) { mutableStateOf(false) }
    val wantsCommands = focused && commandQuery(draft) != null && !archived
    LaunchedEffect(store, wantsCommands, connectionState) {
        if (!wantsCommands || !store.isConnected) return@LaunchedEffect
        commands = emptyList()
        loadingCommands = true
        commandsError = false
        try {
            commands = store.commands()
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) {
            commandsError = true
        } finally {
            loadingCommands = false
        }
    }
    var error by remember { mutableStateOf<String?>(null) }
    var menu by remember { mutableStateOf(false) }
    var showDiff by rememberSaveable { mutableStateOf(false) }
    var showTerminal by rememberSaveable { mutableStateOf(false) }
    var showFork by rememberSaveable { mutableStateOf(false) }
    // A fork continues after a turn that ended, archived sessions' too.
    val canFork = info?.forks == true && session != null && turns.values.any { it.status != TurnStatus.RUNNING }
    var showCommit by rememberSaveable { mutableStateOf(false) }
    // Commits go in the folder the agent works in, between its turns.
    val canCommit = info?.commit == true && project?.isGitRepo == true && session != null && !archived
    val listState = rememberLazyListState()
    val settings = LocalSettings.current
    val placementSetting by settings.state(Keys.terminalPlacement, TerminalPlacement.OVER.name)
    val savedHeight by settings.state(Keys.terminalHeight, 280f)
    var terminalHeight by remember(savedHeight) { mutableFloatStateOf(savedHeight) }
    // On a phone or in a narrow window always in place of the messages, otherwise where Settings says.
    val wide = currentWindowAdaptiveInfoV2().windowSizeClass.isWidthAtLeastBreakpoint(WindowSizeClass.WIDTH_DP_EXPANDED_LOWER_BOUND)
    val placement = if (wide) TerminalPlacement.entries.firstOrNull { it.name == placementSetting } ?: TerminalPlacement.OVER else TerminalPlacement.OVER
    val attachments = rememberAttachments(connection) { error = it }
    val dictation = remember { Dictation(context.applicationContext, settings, uiScope) }
    LaunchedEffect(dictation.error) { dictation.error?.let { error = it; dictation.error = null } }
    DisposableEffect(dictation) { onDispose { dictation.stop() } }

    // New rows go in at the foot of the reversed list, where the list would keep
    // its old foot in place and push them out of sight; while the reader is at
    // the foot, follow them. Scrolling up stops following; back down resumes.
    var follow by remember { mutableStateOf(true) }
    LaunchedEffect(listState) {
        snapshotFlow { listState.isScrollInProgress to (listState.firstVisibleItemIndex == 0 && listState.firstVisibleItemScrollOffset == 0) }
            .collect { (scrolling, atFoot) -> if (scrolling) follow = atFoot }
    }
    // The session's draft, unless it is on its way: then the outbox shows it.
    val savedDraft = session?.draft?.takeIf { d -> Outgoing(d.text, d.images) !in outbox.values }
    // Drafts are for the first prompt: a server that keeps them, a session with nothing yet.
    val offersDraft = info?.drafts == true && synced && transcript.isEmpty() && outbox.isEmpty() && session != null &&
        session.draft == null && !archived
    // A draft is being saved, sent or taken back into the composer.
    var draftBusy by remember(store) { mutableStateOf(false) }

    LaunchedEffect(transcript.size, outbox.size, session?.status, session?.backgroundTasks?.size, savedDraft != null) {
        if (follow) listState.scrollToItem(0)
    }

    // A session opened straight from a restored back stack starts its server too.
    LaunchedEffect(connection) { connection.start() }
    DisposableEffect(store) {
        store.activate()
        onDispose { store.deactivate() }
    }
    // The index says the session is unread while it shows here, with the app
    // in front: read it, on every device. A turn that ends while it shows is
    // read at once, and one that ended in the background once the app is back.
    val lifecycle by LocalLifecycleOwner.current.lifecycle.currentStateAsState()
    val unreadInView = lifecycle.isAtLeast(Lifecycle.State.RESUMED) && sessions[store.sessionID]?.unread == true
    LaunchedEffect(store, unreadInView) {
        if (unreadInView) connection.markRead(store.sessionID)
    }

    fun run(block: suspend () -> Unit) {
        connection.scope.launch {
            try {
                block()
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                error = e.message ?: "The request failed"
            }
        }
    }

    fun toBottom() = uiScope.launch { listState.animateScrollToItem(0) }

    // There is something to send, and its images are on the server.
    val canSend = (draft.isNotBlank() || !attachments.isEmpty) && attachments.ready && !archived

    // Dictated text is appended to whatever was already typed.
    val micPermission = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        if (!granted) error = "Remote Agent needs the microphone to dictate. Allow it in Settings › Apps › Remote Agent › Permissions."
    }
    fun startDictation() {
        if (context.checkSelfPermission(Manifest.permission.RECORD_AUDIO) != PackageManager.PERMISSION_GRANTED) {
            micPermission.launch(Manifest.permission.RECORD_AUDIO)
            return
        }
        val prefix = draft
        val separator = if (prefix.isEmpty() || prefix.last().isWhitespace()) "" else " "
        dictation.start(listOfNotNull(project?.name, harness?.name)) { text -> draft = prefix + separator + text }
    }
    // With steer, the prompt joins the running turn, where the agent takes it.
    fun send(steer: Boolean = false) {
        if (!canSend) return
        val text = draft
        val sent = attachments.entries.toList()
        draft = ""
        attachments.clear()
        // Back to the foot of the transcript, which then follows the reply.
        toBottom()
        connection.scope.launch {
            try {
                store.send(text, sent.mapNotNull { it.ref }, steer)
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                // It didn't go: back into the composer, ahead of anything
                // written since.
                draft = listOf(text, store.draft).filter { it.isNotEmpty() }.joinToString("\n")
                attachments.restore(sent)
                error = e.message ?: "The request failed"
            }
        }
    }

    // The composer's prompt becomes the session's draft, kept on the server
    // for any device to send. It leaves the composer once it is saved.
    val canSaveDraft = offersDraft && !draftBusy && (draft.isNotBlank() || !attachments.isEmpty) && attachments.ready
    fun saveDraft() {
        if (!canSaveDraft) return
        val text = draft
        val sent = attachments.entries.toList()
        draftBusy = true
        connection.scope.launch {
            try {
                connection.saveDraft(store.sessionID, text, sent.mapNotNull { it.ref })
                draft = ""
                attachments.clear()
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                error = e.message ?: "The request failed"
            } finally {
                draftBusy = false
            }
        }
    }

    fun sendDraft() {
        toBottom()
        draftBusy = true
        connection.scope.launch {
            try {
                store.sendDraft()
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                error = e.message ?: "The request failed"
            } finally {
                draftBusy = false
            }
        }
    }

    // Takes the draft off the server and into the composer, ahead of what it
    // has, to change it there.
    fun editDraft() {
        val saved = session?.draft ?: return
        draftBusy = true
        connection.scope.launch {
            try {
                connection.saveDraft(store.sessionID, "", emptyList())
                draft = listOf(saved.text, store.draft).filter { it.isNotEmpty() }.joinToString("\n")
                attachments.insert(saved.images)
                // Not while the terminal covers the composer.
                runCatching { focusRequester.requestFocus() }
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                error = e.message ?: "The request failed"
            } finally {
                draftBusy = false
            }
        }
    }

    // Runs a slash command, as a prompt of its own: the draft stays. While a
    // turn runs, it waits for it, like a follow-up.
    fun send(command: SlashCommand) {
        toBottom()
        run { store.send(command.prompt) }
    }

    // A footer after the last item of each interrupted or failed turn. A prompt
    // queued during a turn sits among its items, so the last is looked up.
    val rows = remember(transcript, turns) {
        val last = buildMap { transcript.forEachIndexed { i, item -> item.turnId?.let { put(it, i) } } }
        buildList<Row> {
            transcript.forEachIndexed { i, item ->
                add(Row.ItemRow(item))
                val tid = item.turnId
                if (tid != null && last[tid] == i) {
                    turns[tid]?.takeIf { it.status == TurnStatus.INTERRUPTED || it.status == TurnStatus.FAILED }?.let { add(Row.Footer(it)) }
                }
            }
        }
    }

    // What the list shows below the transcript's items, bottom first; an item's
    // place in the list comes after them.
    val showsError = session?.status == SessionStatus.ERROR && session.error != null
    val background = session?.backgroundTasks.orEmpty()
    val showsWorking = session?.status == SessionStatus.RUNNING
    val showsEmpty = synced && transcript.isEmpty() && outbox.isEmpty() && session?.draft == null
    val leading = listOf(showsError, background.isNotEmpty(), showsWorking, savedDraft != null, showsEmpty).count { it } + outbox.size
    val listIndex by rememberUpdatedState { itemId: String ->
        rows.asReversed().indexOfFirst { it is Row.ItemRow && it.item.id == itemId }.takeIf { it >= 0 }?.plus(leading)
    }

    // Find in the session: the items whose text has the query, in the
    // transcript's order, and the current one, the newest until stepped from.
    var findOpen by remember(store) { mutableStateOf(false) }
    var findQuery by remember(store) { mutableStateOf("") }
    var findCurrent by remember(store) { mutableStateOf<String?>(null) }
    // Asks the field for the keyboard, each time find is asked for.
    var findAsked by remember(store) { mutableIntStateOf(0) }
    val findField = remember { FocusRequester() }
    val findMatches = remember(transcript, findQuery, findOpen) {
        if (findOpen) TranscriptFind.findMatches(transcript, findQuery) else emptyList()
    }
    val matched = remember(findMatches) { findMatches.toHashSet() }
    val current = findCurrent?.takeIf { it in matched } ?: findMatches.lastOrNull()
    fun openFind() {
        findOpen = true
        findAsked += 1
    }
    fun closeFind() {
        findOpen = false
        findQuery = ""
        findCurrent = null
    }
    // Next goes down, to newer items; Previous up. Both wrap around.
    fun step(by: Int) {
        if (findMatches.isEmpty()) return
        val i = findMatches.indexOf(current).takeIf { it >= 0 } ?: findMatches.lastIndex
        findCurrent = findMatches[(i + by).mod(findMatches.size)]
    }
    LaunchedEffect(findAsked) {
        if (!findOpen) return@LaunchedEffect
        // Once the bar is laid out, and the menu that asked for it has gone.
        withFrameNanos {}
        runCatching { findField.requestFocus() }
    }
    val shortcuts = LocalShortcuts.current
    LaunchedEffect(shortcuts) {
        shortcuts.collect { if (it == Shortcut.FIND_IN_SESSION) openFind() }
    }
    // With the keyboard up, Back hides it first.
    BackHandler(enabled = findOpen && !WindowInsets.isImeVisible) { closeFind() }
    LaunchedEffect(current, findOpen) {
        val id = current?.takeIf { findOpen } ?: return@LaunchedEffect
        listIndex(id)?.let { i ->
            follow = false
            listState.centerItem(i)
        }
    }

    // Opened at an item, as from a search result: once the item is in the
    // list, it shows in the middle and flashes. One that doesn't come with
    // the session's replay was deleted: the session opens at its foot.
    var flash by remember(store) { mutableStateOf<Flash?>(null) }
    val flashAlpha = remember { Animatable(0f) }
    LaunchedEffect(focus) {
        val request = focus ?: return@LaunchedEffect
        store.synced.first { it }
        val index = withTimeoutOrNull(5.seconds) { snapshotFlow { listIndex(request.itemId) }.filterNotNull().first() }
        if (index != null) {
            follow = false
            listState.centerItem(index)
            follow = false
            flash = Flash(request.itemId)
        }
        onFocused(request)
    }
    LaunchedEffect(flash) {
        val f = flash ?: return@LaunchedEffect
        flashAlpha.snapTo(1f)
        flashAlpha.animateTo(0f, tween(2000))
        if (flash === f) flash = null
    }
    val currentTint = palette.yellow.copy(alpha = 0.28f)
    val matchTint = palette.yellow.copy(alpha = 0.12f)
    val flashColor = palette.accent

    Screen(
        title = { SessionTitle(session, harness) },
        onBack = onBack,
        actions = {
            Box {
                IconButton({ menu = true }, Modifier.testTag("session-menu")) { Icon(painterResource(Icons.more), "More") }
                SessionMenu(
                    expanded = menu, onDismiss = { menu = false }, session = session, harness = harness,
                    projectName = project?.name, isGitRepo = project?.isGitRepo == true,
                    terminal = info?.terminal == true && !archived, showTerminal = showTerminal, running = running,
                    onMode = { m -> run { store.setMode(m) } },
                    onFind = ::openFind,
                    onChanges = { showDiff = true },
                    onTerminal = { showTerminal = !showTerminal },
                    onFork = if (canFork) ({ showFork = true }) else null,
                    onCommit = if (canCommit) ({ showCommit = true }) else null,
                    onCommand = ::send,
                    onCopyPath = { path -> Platform.copy(context, path, "Path") },
                    onForceStop = { run { store.interrupt(force = true) } },
                )
            }
        },
    ) { padding ->
        val terminalShown = showTerminal && session != null
        val covered = terminalShown && placement == TerminalPlacement.OVER
        val below = terminalShown && placement == TerminalPlacement.BELOW
        Row(Modifier.fillMaxSize().padding(top = padding.calculateTopPadding())) {
        BoxWithConstraints(Modifier.weight(1f).fillMaxHeight()) {
        val maxTerminal = (maxHeight.value - 200f).coerceAtLeast(120f)
        // Below the session, the column keeps clear of the keyboard as a whole.
        Column(Modifier.fillMaxSize().then(if (below) Modifier.windowInsetsPadding(WindowInsets.navigationBars.union(WindowInsets.ime)) else Modifier)) {
            if (findOpen && !covered) {
                FindBar(
                    query = findQuery,
                    onQuery = {
                        findQuery = it
                        // A new query starts at its newest match.
                        findCurrent = null
                    },
                    count = when {
                        findQuery.isBlank() -> null
                        findMatches.isEmpty() -> "No matches"
                        else -> "${findMatches.indexOf(current) + 1} of ${findMatches.size}"
                    },
                    canStep = findMatches.isNotEmpty(),
                    onPrevious = { step(-1) },
                    onNext = { step(1) },
                    onDone = ::closeFind,
                    focusRequester = findField,
                )
            }
            Box(Modifier.weight(1f).fillMaxWidth(), contentAlignment = Alignment.TopCenter) {
                LazyColumn(
                    Modifier.fillMaxSize().widthIn(max = Metrics.readableWidth).alpha(if (covered) 0f else 1f).testTag("transcript"),
                    state = listState,
                    reverseLayout = true,
                    contentPadding = PaddingValues(horizontal = Metrics.margin, vertical = Metrics.gap),
                    verticalArrangement = Arrangement.spacedBy(Metrics.gap, Alignment.Bottom),
                ) {
                    // Bottom first: the list grows upwards from the composer.
                    // The items before the transcript's are counted in `leading`.
                    if (showsError) {
                        item(key = "error") {
                            Row(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.CenterVertically) {
                                Icon(painterResource(Icons.forceStop), null, Modifier.size(16.dp), tint = palette.red)
                                Text(session.error ?: "", style = Type.footnote, color = palette.red)
                            }
                        }
                    }
                    if (background.isNotEmpty()) {
                        item(key = "background") { BackgroundTasks(background) }
                    }
                    if (showsWorking) {
                        item(key = "working") {
                            Row(Modifier.padding(start = 4.dp), horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                                CircularProgressIndicator(Modifier.size(14.dp), color = palette.subtext, strokeWidth = 2.dp)
                                Text("Working…", style = Type.footnote, color = palette.subtext)
                            }
                        }
                    }
                    savedDraft?.let { d ->
                        item(key = "draft") {
                            DraftBubble(d, enabled = !draftBusy && !archived, onEdit = ::editDraft, onSend = ::sendDraft)
                        }
                    }
                    if (showsEmpty) {
                        item(key = "empty") {
                            Text(
                                "No prompts yet. Write the first one below.", style = Type.footnote, color = palette.subtext,
                                modifier = Modifier.fillMaxWidth().padding(bottom = 40.dp), textAlign = androidx.compose.ui.text.style.TextAlign.Center,
                            )
                        }
                    }
                    items(outbox.entries.reversed(), key = { "out-" + it.key }) { (_, out) ->
                        UserBubble(out.text, out.images, pending = true)
                    }
                    items(rows.asReversed(), key = { it.key }, contentType = { (it as? Row.ItemRow)?.item?.kind ?: "footer" }) { row ->
                        when (row) {
                            is Row.ItemRow -> {
                                val id = row.item.id
                                val found = findOpen && id in matched
                                val tint = when {
                                    found && id == current -> currentTint
                                    found -> matchTint
                                    else -> Color.Transparent
                                }
                                val flashing = flash?.itemId == id
                                Box(
                                    Modifier.highlight { tint }
                                        .then(if (flashing) Modifier.highlight { flashColor.copy(alpha = 0.22f * flashAlpha.value) } else Modifier),
                                ) {
                                    CompositionLocalProvider(LocalFindQuery provides findQuery.takeIf { found }) {
                                        ItemView(row.item, store, children, harness)
                                    }
                                }
                            }
                            is Row.Footer -> TurnFooter(row.turn)
                        }
                    }
                    if (!synced) {
                        item(key = "loading") {
                            Box(Modifier.fillMaxWidth().padding(top = 40.dp), contentAlignment = Alignment.Center) {
                                CircularProgressIndicator(color = palette.subtext)
                            }
                        }
                    }
                }
                if (covered) {
                    TerminalPanel(connection, session!!.id, session.workspace.path, hide = { showTerminal = false }, modifier = Modifier.fillMaxSize())
                }
            }
            if (!covered) {
                Composer(
                    commands = commands + harness?.commands.orEmpty(),
                    loadingCommands = loadingCommands,
                    commandsError = commandsError,
                    insets = !below,
                    dictation = dictation.phase,
                    onDictate = ::startDictation,
                    onStopDictation = dictation::stop,
                    attachments = attachments,
                    draft = draft, onDraft = { draft = it }, placeholder = when (dictation.phase) {
                        Dictation.Phase.DOWNLOADING -> "Downloading the speech model…"
                        Dictation.Phase.STARTING, Dictation.Phase.LISTENING -> "Listening…"
                        else -> if (running) "Queue a follow-up…" else "Prompt"
                    },
                    running = running, enabled = !archived, canSend = canSend, focusPrompt = focusPrompt, focusRequester = focusRequester,
                    onSaveDraft = if (offersDraft) ::saveDraft else null, canSaveDraft = canSaveDraft,
                    // The keyboard has its own dictation key.
                    onFocus = { focused = it; if (it) dictation.stop() }, onSend = { send() }, onStop = { run { store.interrupt() } },
                    onSteer = if (canSend && store.canSteer(draft, harness)) ({ send(steer = true) }) else null,
                )
            }
            if (below) {
                val shown = terminalHeight.coerceIn(120f, maxTerminal)
                PanelResizeHandle(shown, 120f..maxTerminal, onHeight = { terminalHeight = it }, onDone = { settings.put(Keys.terminalHeight, terminalHeight) })
                TerminalPanel(connection, session!!.id, session.workspace.path, hide = { showTerminal = false }, modifier = Modifier.fillMaxWidth().height(shown.dp), insets = false)
            }
        }
        }
        if (terminalShown && placement == TerminalPlacement.RIGHT) {
            VerticalDivider(color = palette.surface1)
            TerminalPanel(connection, session!!.id, session.workspace.path, hide = { showTerminal = false }, modifier = Modifier.width(440.dp).fillMaxHeight())
        }
        }
    }

    ErrorDialog(error, title = "Error") { error = null }
    if (showDiff && session != null) {
        DiffDialog(
            store, canRevert = session.workspace.kind == Workspace.Kind.WORKTREE, onDismiss = { showDiff = false },
            onCommit = if (canCommit) ({ showDiff = false; showCommit = true }) else null,
        )
    }
    if (showCommit && session != null) {
        CommitDialog(connection, session.id, onDismiss = { showCommit = false })
    }
    if (showFork && session != null) {
        ForkSessionDialog(connection, store, onDismiss = { showFork = false }, onForked = { forked, empty ->
            showFork = false
            onForked(forked, empty)
        })
    }
}

/**
 * Find in the session, atop the transcript: the query, which match is shown
 * of how many, Previous and Next, and Done. In the field Return goes to the
 * next match, Shift+Return to the previous one, and Esc closes it.
 */
@Composable
private fun FindBar(
    query: String,
    onQuery: (String) -> Unit,
    count: String?,
    canStep: Boolean,
    onPrevious: () -> Unit,
    onNext: () -> Unit,
    onDone: () -> Unit,
    focusRequester: FocusRequester,
) {
    Column(Modifier.fillMaxWidth().background(palette.mantle).testTag("find-bar")) {
        Row(
            Modifier.fillMaxWidth().padding(start = Metrics.margin, end = 4.dp, top = 4.dp, bottom = 4.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Row(
                Modifier.weight(1f).background(palette.surface0, RoundedCornerShape(Metrics.Corner.field)).padding(horizontal = 10.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(6.dp),
            ) {
                Icon(painterResource(Icons.search), null, Modifier.size(16.dp), tint = palette.overlay)
                BasicTextField(
                    value = query,
                    onValueChange = onQuery,
                    singleLine = true,
                    textStyle = Type.body.copy(color = palette.text),
                    cursorBrush = SolidColor(palette.accent),
                    keyboardOptions = KeyboardOptions(imeAction = ImeAction.Search),
                    keyboardActions = KeyboardActions(onSearch = { onNext() }),
                    modifier = Modifier.weight(1f).padding(vertical = 8.dp).focusRequester(focusRequester)
                        .onPreviewKeyEvent {
                            if (it.type != KeyEventType.KeyDown) return@onPreviewKeyEvent false
                            when (it.key) {
                                Key.Enter, Key.NumPadEnter -> {
                                    if (it.isShiftPressed) onPrevious() else onNext()
                                    true
                                }
                                Key.Escape -> {
                                    onDone()
                                    true
                                }
                                else -> false
                            }
                        }
                        .testTag("find-field"),
                    decorationBox = { inner ->
                        Box {
                            if (query.isEmpty()) Text("Find in session", style = Type.body, color = palette.overlay, maxLines = 1)
                            inner()
                        }
                    },
                )
                if (count != null) {
                    Text(
                        count, style = Type.caption.copy(fontFeatureSettings = "tnum"), color = palette.subtext, maxLines = 1,
                        modifier = Modifier.testTag("find-count"),
                    )
                }
            }
            IconButton(onPrevious, enabled = canStep, modifier = Modifier.testTag("find-previous")) {
                Icon(painterResource(Icons.findPrevious), "Previous", tint = if (canStep) palette.accent else palette.overlay)
            }
            IconButton(onNext, enabled = canStep, modifier = Modifier.testTag("find-next")) {
                Icon(painterResource(Icons.findNext), "Next", tint = if (canStep) palette.accent else palette.overlay)
            }
            TextButton(onDone, Modifier.testTag("find-done")) {
                Text("Done", style = Type.body.copy(fontWeight = FontWeight.SemiBold), color = palette.accent)
            }
        }
        HorizontalDivider(color = palette.surface0)
    }
}

/**
 * A colour behind an item, a little past its edges, with rounded corners:
 * find's tints and the flash of an item opened from search. Read as it
 * draws, so that a flash that fades redraws only.
 */
private fun Modifier.highlight(color: () -> Color): Modifier = drawBehind {
    val c = color()
    if (c.alpha == 0f) return@drawBehind
    val dx = 4.dp.toPx()
    val dy = 3.dp.toPx()
    drawRoundRect(
        c, topLeft = Offset(-dx, -dy), size = Size(size.width + 2 * dx, size.height + 2 * dy),
        cornerRadius = CornerRadius(Metrics.Corner.card.toPx()),
    )
}

/**
 * Scrolls the transcript to show the item at [index] in the middle, or its
 * top where it is taller than the list. The list is reversed: its offsets
 * run from the foot up.
 */
private suspend fun LazyListState.centerItem(index: Int) {
    scrollToItem(index)
    val info = layoutInfo.visibleItemsInfo.firstOrNull { it.index == index } ?: return
    val start = layoutInfo.viewportStartOffset
    val end = layoutInfo.viewportEndOffset
    val wanted = if (info.size > end - start) end - info.size else start + (end - start - info.size) / 2
    scrollBy((info.offset - wanted).toFloat())
}

/** What the agent left running after its turn: it reports back in a turn of its own. */
@Composable
private fun BackgroundTasks(tasks: List<BackgroundTask>) {
    Row(Modifier.padding(start = 4.dp).testTag("background-tasks"), horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.Top) {
        CircularProgressIndicator(Modifier.padding(top = 2.dp).size(14.dp), color = palette.subtext, strokeWidth = 2.dp)
        Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
            Text("Running in the background", style = Type.footnote, color = palette.subtext)
            for (t in tasks) {
                Row(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.Top) {
                    val icon = when (t.kind) {
                        BackgroundTask.Kind.COMMAND -> Icons.terminal
                        BackgroundTask.Kind.AGENT -> Icons.toolThink
                        BackgroundTask.Kind.OTHER -> Icons.inProgress
                    }
                    Icon(painterResource(icon), null, Modifier.padding(top = 2.dp).size(14.dp), tint = palette.subtext)
                    Text(t.description, style = Type.footnote, color = palette.subtext, maxLines = 2, overflow = TextOverflow.Ellipsis)
                }
            }
        }
    }
}

@Composable
private fun SessionTitle(session: Session?, harness: HarnessInfo?) {
    Column {
        Text(session?.title ?: "", style = Type.subheadline.copy(fontWeight = FontWeight.SemiBold), color = palette.text, maxLines = 1, overflow = TextOverflow.Ellipsis)
        if (session != null) {
            Row(horizontalArrangement = Arrangement.spacedBy(4.dp), verticalAlignment = Alignment.CenterVertically) {
                HarnessIcon(session.harness, Modifier.size(12.dp))
                Text(subtitle(session, harness), style = Type.caption2, color = palette.subtext, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
                session.context?.fraction?.let {
                    Text("·", style = Type.caption2, color = palette.subtext)
                    ContextGauge(it, Type.caption2)
                }
            }
        }
    }
}

/**
 * "Opus 5.5 · high · Ask": the model (the harness until the agent reports
 * one), its reasoning effort and the permission mode.
 */
private fun subtitle(s: Session, harness: HarnessInfo?): String {
    val mode = s.mode?.takeIf { it.isNotEmpty() }?.let { m -> harness?.modes?.firstOrNull { it.id == m }?.name ?: m }
    return listOfNotNull(s.modelName(harness) ?: harness?.name ?: s.harness, s.modelInfo?.effort ?: s.effort, mode)
        .filter { it.isNotEmpty() }.joinToString(" · ")
}

@Composable
private fun SessionMenu(
    expanded: Boolean,
    onDismiss: () -> Unit,
    session: Session?,
    harness: HarnessInfo?,
    projectName: String?,
    isGitRepo: Boolean,
    terminal: Boolean,
    showTerminal: Boolean,
    running: Boolean,
    onMode: (String) -> Unit,
    onFind: () -> Unit,
    onChanges: () -> Unit,
    onTerminal: () -> Unit,
    // Null: the server or the session can't fork.
    onFork: (() -> Unit)?,
    // Null: the server or the session can't commit.
    onCommit: (() -> Unit)?,
    onCommand: (SlashCommand) -> Unit,
    onCopyPath: (String) -> Unit,
    onForceStop: () -> Unit,
) {
    DropdownMenu(expanded, onDismiss, containerColor = palette.mantle) {
        val modes = harness?.modes
        if (!modes.isNullOrEmpty()) {
            MenuHeader("Permissions")
            for (m in modes) {
                MenuItem(m.name, if (session?.mode == m.id) Icons.check else null) { onDismiss(); onMode(m.id) }
            }
            HorizontalDivider(color = palette.surface0)
        }
        MenuItem("Find in Session", Icons.search) { onDismiss(); onFind() }
        if (isGitRepo) MenuItem("Changes", Icons.changes) { onDismiss(); onChanges() }
        if (terminal) MenuItem(if (showTerminal) "Hide Terminal" else "Terminal", Icons.terminal) { onDismiss(); onTerminal() }
        if (onFork != null) MenuItem("Fork…", Icons.fork) { onDismiss(); onFork() }
        // Not while the agent works: it may be changing the files.
        if (onCommit != null) MenuItem("Commit…", Icons.commit, enabled = !running) { onDismiss(); onCommit() }
        val commands = harness?.commands
        if (!commands.isNullOrEmpty()) {
            HorizontalDivider(color = palette.surface0)
            MenuHeader("Commands")
            for (c in commands) {
                androidx.compose.material3.DropdownMenuItem(
                    text = {
                        Column {
                            Text(c.prompt, color = palette.text, fontFamily = FontFamily.Monospace)
                            c.description?.let { Text(it, style = Type.caption, color = palette.subtext, maxLines = 2) }
                        }
                    },
                    leadingIcon = { Icon(painterResource(c.symbol), null, tint = palette.text) },
                    enabled = session?.archived != true,
                    onClick = { onDismiss(); onCommand(c) },
                )
            }
        }
        if (session != null) {
            HorizontalDivider(color = palette.surface0)
            MenuHeader(projectName ?: "")
            // The branch commits too, from here.
            if (isGitRepo) {
                MenuItem(session.workspace.branch ?: "Detached HEAD", Icons.branch, color = palette.subtext, enabled = true) {
                    if (onCommit != null && !running) {
                        onDismiss()
                        onCommit()
                    }
                }
            }
            if (session.workspace.kind == Workspace.Kind.WORKTREE) MenuItem("Worktree", Icons.folder, color = palette.subtext) {}
            MenuItem(session.workspace.path, Icons.copy, color = palette.subtext) { onDismiss(); onCopyPath(session.workspace.path) }
        }
        if (running) {
            HorizontalDivider(color = palette.surface0)
            MenuItem("Force stop agent", Icons.forceStop, color = palette.red) { onDismiss(); onForceStop() }
        }
    }
}

@Composable
private fun MenuHeader(text: String) {
    Text(text, style = Type.caption.copy(fontWeight = FontWeight.SemiBold), color = palette.subtext, modifier = Modifier.padding(horizontal = 16.dp, vertical = 6.dp))
}

@Composable
private fun Composer(
    commands: List<SlashCommand>,
    loadingCommands: Boolean,
    commandsError: Boolean,
    insets: Boolean,
    dictation: Dictation.Phase,
    onDictate: () -> Unit,
    onStopDictation: () -> Unit,
    attachments: Attachments,
    draft: String,
    onDraft: (String) -> Unit,
    placeholder: String,
    running: Boolean,
    enabled: Boolean,
    canSend: Boolean,
    focusPrompt: Boolean,
    focusRequester: FocusRequester,
    // Null: the session can't take a draft (offersDraft).
    onSaveDraft: (() -> Unit)?,
    canSaveDraft: Boolean,
    onFocus: (Boolean) -> Unit,
    onSend: () -> Unit,
    onStop: () -> Unit,
    // Ctrl+Shift+Enter on a hardware keyboard: into the running turn; null: it can't.
    onSteer: (() -> Unit)? = null,
) {
    var focusedNow by remember { mutableStateOf(false) }
    var field by remember { mutableStateOf(TextFieldValue(draft, TextRange(draft.length))) }
    var commandIndex by remember(draft) { mutableStateOf(0) }
    var commandsDismissed by remember(draft) { mutableStateOf(false) }
    val suggestions = if (focusedNow && enabled && !commandsDismissed) commandSuggestions(draft, commands) else emptyList()
    val suggestionList = rememberLazyListState()
    LaunchedEffect(draft, suggestions, commandIndex) {
        if (suggestions.isNotEmpty()) suggestionList.scrollToItem(commandIndex.coerceAtMost(suggestions.lastIndex))
    }
    fun complete(command: SlashCommand) {
        onDraft(command.prompt + " ")
        focusRequester.requestFocus()
    }
    LaunchedEffect(Unit) { if (focusPrompt) focusRequester.requestFocus() }
    Box(
        Modifier.fillMaxWidth().background(palette.mantle)
            .then(if (insets) Modifier.windowInsetsPadding(WindowInsets.navigationBars.union(WindowInsets.ime)) else Modifier),
        contentAlignment = Alignment.Center,
    ) {
        Column(Modifier.widthIn(max = Metrics.readableWidth).fillMaxWidth().padding(horizontal = Metrics.margin, vertical = 6.dp)) {
        if (suggestions.isNotEmpty()) {
            LazyColumn(Modifier.fillMaxWidth().heightIn(max = 220.dp).testTag("command-suggestions"), state = suggestionList) {
                items(suggestions, key = { it.name }) { command ->
                    Row(
                        Modifier.fillMaxWidth()
                            .heightIn(min = 48.dp)
                            .background(if (suggestions.indexOf(command) == commandIndex) palette.surface0 else palette.mantle)
                            .clickable { complete(command) }.padding(8.dp),
                        horizontalArrangement = Arrangement.spacedBy(8.dp),
                    ) {
                        Column(Modifier.weight(1f)) {
                            Text(command.prompt, style = Type.footnote, color = palette.text, fontFamily = FontFamily.Monospace)
                            command.description?.let { Text(it, style = Type.caption2, color = palette.subtext, maxLines = 2) }
                        }
                        command.scope?.let { Text(it.replaceFirstChar { c -> c.titlecase() }, style = Type.caption2, color = palette.subtext) }
                    }
                }
            }
        }
        if (focusedNow && enabled && !commandsDismissed && commandQuery(draft) != null && (loadingCommands || commandsError)) {
            Text(if (loadingCommands) "Loading skills…" else "Skills unavailable. Try typing / again.", style = Type.caption, color = palette.subtext)
        }
        if (!attachments.isEmpty) AttachmentStrip(attachments, Modifier.padding(bottom = 4.dp))
        Row(
            Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.spacedBy(6.dp),
            verticalAlignment = Alignment.Bottom,
        ) {
            AttachMenu(attachments, enabled = enabled)
            val style = messageStyle().copy(color = palette.text)
            // Text that comes from elsewhere (a draft to edit, dictation, a
            // completed command) puts the cursor at its end.
            val value = if (field.text == draft) field else TextFieldValue(draft, TextRange(draft.length))
            BasicTextField(
                value = value,
                onValueChange = {
                    field = it
                    if (it.text != draft) onDraft(it.text)
                },
                enabled = enabled,
                textStyle = style,
                maxLines = 6,
                cursorBrush = SolidColor(palette.accent),
                modifier = Modifier.weight(1f)
                    .background(palette.surface0, RoundedCornerShape(Metrics.Corner.field))
                    .padding(horizontal = 10.dp, vertical = 8.dp)
                    .focusRequester(focusRequester)
                    .onFocusChanged { focusedNow = it.isFocused; onFocus(it.isFocused) }
                    // A hardware keyboard's Return sends; Shift-Return is a new line.
                    .onPreviewKeyEvent {
                        if (it.type == KeyEventType.KeyDown && suggestions.isNotEmpty() && !it.isShiftPressed) {
                            when (it.key) {
                                Key.DirectionDown -> { commandIndex = minOf(commandIndex + 1, suggestions.lastIndex); return@onPreviewKeyEvent true }
                                Key.DirectionUp -> { commandIndex = maxOf(commandIndex - 1, 0); return@onPreviewKeyEvent true }
                                Key.Escape -> { commandsDismissed = true; return@onPreviewKeyEvent true }
                                Key.Enter, Key.Tab -> {
                                    val selected = suggestions[commandIndex.coerceAtMost(suggestions.lastIndex)]
                                    if (it.key == Key.Enter && draft == selected.prompt) onSend() else complete(selected)
                                    return@onPreviewKeyEvent true
                                }
                            }
                        }
                        if (it.key == Key.Enter && it.type == KeyEventType.KeyDown && it.isShiftPressed && it.isCtrlPressed && onSteer != null) {
                            onSteer()
                            true
                        } else if (it.key == Key.Enter && it.type == KeyEventType.KeyDown && !it.isShiftPressed) {
                            onSend()
                            true
                        } else {
                            false
                        }
                    }
                    .testTag("composer"),
                decorationBox = { inner ->
                    Box {
                        if (draft.isEmpty()) Text(placeholder, style = style, color = palette.overlay)
                        inner()
                    }
                },
            )
            if (running) {
                IconButton(onStop, Modifier.size(40.dp), colors = IconButtonDefaults.iconButtonColors(contentColor = palette.red)) {
                    RoundIcon(Icons.stop, palette.red, "Stop")
                }
            }
            // The mic is the main button until the keyboard is up or there is
            // text to send; while listening, it stops.
            val nothingToSend = draft.isBlank() && attachments.isEmpty
            if (onSaveDraft != null && !nothingToSend) {
                IconButton(onSaveDraft, Modifier.size(40.dp).testTag("save-draft"), enabled = canSaveDraft) {
                    Icon(
                        painterResource(Icons.saveDraft), "Save as Draft", Modifier.size(Metrics.roundButton * 0.85f),
                        tint = if (canSaveDraft) palette.accent else palette.overlay,
                    )
                }
            }
            when {
                dictation != Dictation.Phase.IDLE -> IconButton(onStopDictation, Modifier.size(40.dp).testTag("dictate"), enabled = dictation != Dictation.Phase.FINISHING) {
                    RoundIcon(Icons.dictating, palette.red, "Stop dictation")
                }
                !focusedNow && nothingToSend -> IconButton(onDictate, Modifier.size(40.dp).testTag("dictate"), enabled = enabled) {
                    RoundIcon(Icons.mic, if (enabled) palette.accent else palette.overlay, "Dictate")
                }
                else -> IconButton(onSend, Modifier.size(40.dp).testTag("send"), enabled = canSend) {
                    RoundIcon(Icons.send, if (canSend) palette.accent else palette.overlay, "Send")
                }
            }
        }
        }
    }
}

/** A symbol on a filled circle, as SF Symbols' *.circle.fill buttons. */
@Composable
fun RoundIcon(icon: Int, color: androidx.compose.ui.graphics.Color, label: String) {
    Box(Modifier.size(Metrics.roundButton).background(color, androidx.compose.foundation.shape.CircleShape), contentAlignment = Alignment.Center) {
        Icon(painterResource(icon), label, Modifier.size(Metrics.roundButton * 0.62f), tint = palette.onAccent)
    }
}
