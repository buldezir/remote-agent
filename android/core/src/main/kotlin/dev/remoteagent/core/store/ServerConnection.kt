package dev.remoteagent.core.store

import dev.remoteagent.core.net.Http
import dev.remoteagent.core.net.PING_INTERVAL
import dev.remoteagent.core.net.REVOKED_CODE
import dev.remoteagent.core.net.RpcClient
import dev.remoteagent.core.net.RpcError
import dev.remoteagent.core.net.ServerMessage
import dev.remoteagent.core.net.params
import dev.remoteagent.core.protocol.CommitPreview
import dev.remoteagent.core.protocol.Event
import dev.remoteagent.core.protocol.FSListing
import dev.remoteagent.core.protocol.HarnessInfo
import dev.remoteagent.core.protocol.ImageRef
import dev.remoteagent.core.protocol.ImportableSession
import dev.remoteagent.core.protocol.Project
import dev.remoteagent.core.protocol.SearchResults
import dev.remoteagent.core.protocol.ServerInfo
import dev.remoteagent.core.protocol.ServerSettings
import dev.remoteagent.core.protocol.ServerTask
import dev.remoteagent.core.protocol.Session
import dev.remoteagent.core.protocol.UpdateInfo
import dev.remoteagent.core.protocol.WireCoding
import dev.remoteagent.core.protocol.Workspace
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeoutOrNull
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.encodeToJsonElement
import okhttp3.OkHttpClient
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import okio.ByteString.Companion.toByteString
import java.io.File
import java.util.UUID
import kotlin.time.Duration
import kotlin.time.Duration.Companion.seconds

sealed interface ConnectionState {
    data object Idle : ConnectionState
    data object Connecting : ConnectionState
    data object Connected : ConnectionState
    data class Failed(val message: String) : ConnectionState
}

/**
 * A live connection to one server: keeps the index (projects + sessions)
 * in sync, owns per-session stores, and reconnects with backoff. It stays
 * `Connecting` while it tries, and gives up after `MAX_ATTEMPTS` failures in
 * a row: `Failed` until `start()` or `retry()`.
 * Mirrors apple/RAKit/Sources/RAKit/Store/ServerConnection.swift. Its state
 * changes on `scope` (the app: the main thread).
 */
class ServerConnection(
    server: SavedServer,
    token: String,
    override val scope: CoroutineScope,
    private val imageCache: ImageCache,
    private val onURLWorked: (String) -> Unit,
    http: OkHttpClient = Http.client,
    storage: Storage? = null,
    pingInterval: Duration = PING_INTERVAL,
) : SessionHost {
    private val _server = MutableStateFlow(server)
    val server: StateFlow<SavedServer> = _server.asStateFlow()
    private val _state = MutableStateFlow<ConnectionState>(ConnectionState.Idle)
    val state: StateFlow<ConnectionState> = _state.asStateFlow()
    private val _info = MutableStateFlow<ServerInfo?>(null)
    val info: StateFlow<ServerInfo?> = _info.asStateFlow()
    private val _projects = MutableStateFlow<Map<String, Project>>(emptyMap())
    val projects: StateFlow<Map<String, Project>> = _projects.asStateFlow()
    private val _sessions = MutableStateFlow<Map<String, Session>>(emptyMap())
    val sessions: StateFlow<Map<String, Session>> = _sessions.asStateFlow()
    /** The agents installed on the server; null until first loaded. */
    private val _harnesses = MutableStateFlow<List<HarnessInfo>?>(null)
    val harnesses: StateFlow<List<HarnessInfo>?> = _harnesses.asStateFlow()
    private val _indexSynced = MutableStateFlow(false)
    val indexSynced: StateFlow<Boolean> = _indexSynced.asStateFlow()
    override val drafts = Drafts(storage, server.id)

    private val client = RpcClient(token, http, pingInterval)
    private var indexSeq = 0L
    private var indexLive = false
    /** As SessionStore's: index subscriptions not yet synchronized. */
    private var indexOutstanding = 0
    private var indexReplay = mutableListOf<Event>()
    private val sessionStores = HashMap<String, SessionStore>()
    private val wanted = MutableStateFlow(false)
    private var runJob: Job? = null
    /** The wait after the first failed attempt, doubling after each one. */
    internal var firstRetryDelay: Duration = 1.seconds
    /** Cuts the run loop's wait short: the socket closed, so reconnect now. */
    private val wake = Channel<Unit>(Channel.CONFLATED)
    /**
     * Called with a session in the index that changed, before and after:
     * live, or while this device was away, in the replay after reconnecting.
     * Not for the sessions the first sync brings.
     */
    var onSessionChange: ((old: Session, new: Session) -> Unit)? = null

    init {
        scope.launch { for (msg in client.messages) handle(msg) }
    }

    internal fun update(server: SavedServer) {
        _server.value = server
    }

    override val isConnected: Boolean get() = _state.value == ConnectionState.Connected

    // Lifecycle

    fun start() {
        wanted.value = true
        if (runJob?.isActive == true) return
        runJob = scope.launch { runLoop() }
    }

    fun stop() {
        wanted.value = false
        pause()
    }

    /** Stops for good, revoking this device on the server first if reachable. */
    internal fun unpairAndStop() {
        wanted.value = false
        runJob?.cancel()
        runJob = null
        _state.value = ConnectionState.Idle
        scope.launch {
            attempt {
                if (!client.isConnected) client.connect(_server.value.urls)
                client.callRaw("device.unpair", timeout = 5.seconds)
            }
            client.disconnect()
        }
    }

    internal fun pause() {
        runJob?.cancel()
        runJob = null
        client.disconnect()
        _state.value = ConnectionState.Idle
    }

    internal fun resumeIfWanted() {
        if (wanted.value) start()
    }

    /** Reconnect now (e.g. pull-to-refresh after a failure). */
    fun retry() {
        runJob?.cancel()
        runJob = null
        start()
    }

    private suspend fun runLoop() {
        var delay = firstRetryDelay
        var failures = 0
        while (currentCoroutineContext().isActive && wanted.value) {
            if (client.isConnected) {
                sleep(2.seconds)
                continue
            }
            _state.value = ConnectionState.Connecting
            try {
                _info.value = client.connect(_server.value.urls)
                client.connectedURL?.let(onURLWorked)
                _state.value = ConnectionState.Connected
                delay = firstRetryDelay
                failures = 0
                resubscribeAll()
                scope.launch { refreshHarnesses() }
            } catch (e: RpcError) {
                failures += 1
                // Trying again won't bring back a revoked token.
                if (e.code == "unauthorized" || failures >= MAX_ATTEMPTS) {
                    _state.value = ConnectionState.Failed(e.message)
                    runJob = null
                    return
                }
                sleep(delay)
                delay *= 2
            }
        }
    }

    /** Waits, or until the socket closes. */
    private suspend fun sleep(d: Duration) {
        withTimeoutOrNull(d) { wake.receive() }
    }

    private suspend fun resubscribeAll() {
        subscribeIndex()
        for (store in sessionStores.values.toList()) {
            if (store.isActive) subscribe(store)
        }
    }

    // Messages

    internal fun handle(msg: ServerMessage) {
        when (msg) {
            is ServerMessage.Events ->
                if (msg.stream == "index") applyIndex(msg.events) else store(msg.stream)?.apply(msg.events)
            is ServerMessage.Synchronized ->
                if (msg.stream == "index") {
                    // A reset drops what was cached, but the replay is still compared with it.
                    val before = _sessions.value
                    val projects = if (msg.reset) HashMap() else HashMap(_projects.value)
                    val sessions = if (msg.reset) HashMap() else HashMap(before)
                    val buffered = indexReplay
                    indexReplay = mutableListOf()
                    val changes = buffered.mapNotNull { upsertIndex(it, projects, sessions, before) }
                    _projects.value = projects
                    _sessions.value = sessions
                    indexSeq = msg.seq
                    indexOutstanding = maxOf(0, indexOutstanding - 1)
                    indexLive = indexOutstanding == 0
                    _indexSynced.value = true
                    for ((id, s) in sessions) {
                        val store = sessionStores[id] ?: continue
                        if (store.session.value == null) store.setSession(s)
                    }
                    drafts.keep { it in sessions }
                    report(changes)
                } else {
                    store(msg.stream)?.synchronized(msg.seq, msg.reset)
                }
            is ServerMessage.Resync -> {
                Log.d { "resync ${msg.stream}" }
                scope.launch { resubscribe(msg.stream) }
            }
            is ServerMessage.Closed -> {
                Log.d { "closed: ${msg.reason}" }
                indexLive = false
                indexOutstanding = 0
                for (s in sessionStores.values) s.closed()
                // Not after pause(): the socket closed because we closed it.
                if (wanted.value && runJob != null) {
                    _state.value = ConnectionState.Connecting
                    wake.trySend(Unit)
                }
            }
        }
    }

    private fun store(stream: String): SessionStore? {
        if (!stream.startsWith("session:")) return null
        return sessionStores[stream.removePrefix("session:")]
    }

    private fun applyIndex(events: List<Event>) {
        if (!indexLive) {
            indexReplay.addAll(events)
            return
        }
        val projects = HashMap(_projects.value)
        val sessions = HashMap(_sessions.value)
        val changes = mutableListOf<Pair<Session, Session>>()
        for (ev in events) {
            if (ev.seq <= indexSeq) continue
            if (ev.seq != indexSeq + 1) {
                indexLive = false
                scope.launch { subscribeIndex() }
                break
            }
            indexSeq = ev.seq
            upsertIndex(ev, projects, sessions)?.let { changes += it }
        }
        _projects.value = projects
        _sessions.value = sessions
        report(changes)
    }

    /**
     * Applies an index event, and returns the session it changed, before and
     * after. A session is compared with the one in `sessions`, else with the
     * one in `before`.
     */
    private fun upsertIndex(
        ev: Event,
        projects: MutableMap<String, Project>,
        sessions: MutableMap<String, Session>,
        before: Map<String, Session> = emptyMap(),
    ): Pair<Session, Session>? {
        when (ev.type) {
            "project.upserted" -> ev.project?.let { projects[it.id] = it }
            "project.removed" -> ev.id?.let { projects.remove(it) }
            "session.upserted" -> ev.session?.let {
                val old = sessions.put(it.id, it) ?: before[it.id]
                if (old != null && old != it) return old to it
            }
            "session.removed" -> ev.id?.let {
                sessions.remove(it)
                drafts[it] = ""
            }
        }
        return null
    }

    /** Called once the batch they came in is applied, so the index is whole. */
    private fun report(changes: List<Pair<Session, Session>>) {
        for ((old, new) in changes) onSessionChange?.invoke(old, new)
    }

    private suspend fun subscribeIndex() {
        indexOutstanding += 1
        indexLive = false
        indexReplay = mutableListOf()
        attempt { client.callRaw("subscribe", params("stream" to "index", "afterSeq" to indexSeq)) }
            ?: run { indexOutstanding = maxOf(0, indexOutstanding - 1) }
    }

    override suspend fun subscribe(store: SessionStore) {
        Log.d { "session ${store.sessionID} subscribe afterSeq=${store.lastSeq}" }
        store.beginReplay()
        attempt { client.callRaw("subscribe", params("stream" to "session:${store.sessionID}", "afterSeq" to store.lastSeq)) }
            ?: store.subscriptionFailed()
    }

    private suspend fun resubscribe(stream: String) {
        if (stream == "index") subscribeIndex() else store(stream)?.let { subscribe(it) }
    }

    // Session stores

    /** Returns the store for a session, subscribing while it's in use. */
    fun sessionStore(id: String): SessionStore = sessionStores.getOrPut(id) {
        SessionStore(id, this).also { it.setSession(_sessions.value[id]) }
    }

    override fun activate(store: SessionStore) {
        if (!isConnected) return
        scope.launch { subscribe(store) }
    }

    override fun deactivate(store: SessionStore) {
        scope.launch { attempt { client.callRaw("unsubscribe", params("stream" to "session:${store.sessionID}")) } }
    }

    // API

    override suspend fun callRaw(method: String, params: JsonElement, timeout: Duration): JsonElement =
        client.callRaw(method, params, timeout)

    /**
     * Calls a command that carries a commandId until it gets through: when the
     * socket drops or the server doesn't answer, it waits for the connection
     * to come back and sends it again. rad applies a commandId once, so a
     * repeat of one that got there returns the first result. It throws once
     * the connection gives up or stops.
     */
    override suspend fun deliver(method: String, params: JsonElement, timeout: Duration): JsonElement {
        var attempts = 0
        while (true) {
            waitUntilConnected()
            attempts += 1
            try {
                return client.callRaw(method, params, timeout)
            } catch (e: RpcError) {
                if ((e.code != "disconnected" && e.code != "timeout") || attempts >= MAX_ATTEMPTS) throw e
            }
        }
    }

    /**
     * Returns once connected. While paused (the app is in the background, or
     * a picker is open) it waits for the app to come back; it throws once the
     * connection is stopped or gives up.
     */
    private suspend fun waitUntilConnected() {
        val (s, w) = combine(_state, wanted, ::Pair).first { (s, w) ->
            // The socket goes before `state` hears that it closed.
            !w || s is ConnectionState.Failed || (s == ConnectionState.Connected && client.isConnected)
        }
        if (!w || s is ConnectionState.Failed) throw RpcError.disconnected()
    }

    suspend fun refreshHarnesses(force: Boolean = false) {
        attempt { client.call<HarnessList>("harness.list", params("refresh" to force), 45.seconds) }?.let {
            _harnesses.value = it.harnesses
        }
    }

    fun harness(id: String): HarnessInfo? = _harnesses.value?.firstOrNull { it.id == id }

    /**
     * Uploads an image to attach to a prompt. Opening a picker stops the app
     * (and so the socket) for a moment, so this waits for the connection.
     */
    suspend fun uploadImage(data: ByteArray): ImageRef {
        withTimeoutOrNull(10.seconds) { waitUntilConnected() }
        val ref = client.uploadImage(data)
        imageCache.store(data, ref.id)
        return ref
    }

    /** An image's bytes, from the cache or the server. */
    suspend fun imageData(id: String): ByteArray = imageCache.data(id) { client.imageData(id) }

    /** An image as a file, for sharing. */
    suspend fun imageFile(id: String): File = imageCache.namedFile(id) { client.imageData(id) }

    /** The session's shell on the server, for one view to show. */
    fun terminal(sessionID: String): TerminalSession = TerminalSession(TerminalSocketHost(), sessionID, scope)

    /** Terminal sockets over OkHttp, their callbacks moved onto `scope`. */
    private inner class TerminalSocketHost : TerminalHost {
        override fun connected() = isConnected

        override fun open(query: String, listener: TerminalSocket.Listener): TerminalSocket {
            val ws = client.openTerminal(query, object : WebSocketListener() {
                override fun onMessage(webSocket: WebSocket, text: String) {
                    scope.launch { listener.onText(text) }
                }

                override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
                    val data = bytes.toByteArray()
                    scope.launch { listener.onBinary(data) }
                }

                override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                    webSocket.close(1000, null)
                    scope.launch { listener.onClosed(code) }
                }

                override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                    val code = if (response?.code == 401) REVOKED_CODE else 1006
                    scope.launch { listener.onClosed(code) }
                }
            })
            return object : TerminalSocket {
                override fun send(text: String) {
                    ws.send(text)
                }

                override fun send(bytes: ByteArray) {
                    ws.send(bytes.toByteString())
                }

                override fun close() {
                    ws.close(1000, null)
                }
            }
        }
    }

    suspend fun listDirectory(path: String?): FSListing = client.call("fs.list", params("path" to (path ?: "")))

    suspend fun addProject(path: String): Project {
        val p: Project = client.call("project.add", params("path" to path))
        _projects.value += p.id to p
        return p
    }

    suspend fun removeProject(id: String) {
        client.callRaw("project.remove", params("id" to id))
    }

    suspend fun branches(projectID: String): Branches = client.call("git.branches", params("projectId" to projectID))

    suspend fun createSession(p: NewSession): Session {
        val created = deliver("session.create", WireCoding.json.encodeToJsonElement(p), 90.seconds)
        val s: Session = WireCoding.json.decodeFromJsonElement(created)
        _sessions.value += s.id to s
        return s
    }

    /**
     * A new session that continues `p.sessionId` after one of its turns, with
     * its transcript up to there. Only for a server whose `info.forks` is true.
     */
    suspend fun fork(p: ForkSession): Session {
        val forked = deliver("session.fork", WireCoding.json.encodeToJsonElement(p), 90.seconds)
        val s: Session = WireCoding.json.decodeFromJsonElement(forked)
        _sessions.value += s.id to s
        return s
    }

    // Picking up a session from the terminal (protocol/PROTOCOL.md); only for an agent whose `caps.import` is true.

    /**
     * The sessions the agent ran in the project's folder outside rad, such as
     * in a terminal, newest first. rad reads the agent's files for them, which
     * may take a moment.
     */
    suspend fun importable(projectID: String, harness: String): List<ImportableSession> {
        // The list may open as the app comes back, before the socket does.
        withTimeoutOrNull(10.seconds) { waitUntilConnected() }
        return client.call<ImportableList>("session.importable", params("projectId" to projectID, "harness" to harness), 60.seconds).sessions
    }

    /**
     * Picks up one of the agent's own sessions (`importable`) as a session of
     * rad's, whose next prompt goes on with it. Sent again after a drop with
     * the same commandId, as `createSession` is.
     */
    suspend fun importSession(p: ImportSession): Session {
        val imported = deliver("session.import", WireCoding.json.encodeToJsonElement(p), 90.seconds)
        val s: Session = WireCoding.json.decodeFromJsonElement(imported)
        _sessions.value += s.id to s
        return s
    }

    // Search (protocol/PROTOCOL.md, "Search"); only for a server whose `info.search` is true.

    /**
     * The sessions that mention `query` in what was said in them, or in their
     * title or branch, the best first, with the item that matches best. With
     * `archived`, archived sessions too; `limit`: at most so many (rad's
     * default: 30). SessionSearch asks every server at once.
     */
    suspend fun search(query: String, archived: Boolean = false, limit: Int? = null): SearchResults =
        client.call(
            "session.search",
            params("query" to query, "archived" to archived.takeIf { it }, "limit" to limit),
            15.seconds,
        )

    /**
     * Keeps a prompt in the session, unsent, for any device to send later
     * (`SessionStore.sendDraft`), replacing the one it had; no text and no
     * images removes it. The session changes here at once, without waiting
     * for its event. Only for a server whose `info.drafts` is true.
     */
    suspend fun saveDraft(sessionID: String, text: String, images: List<ImageRef>): Session {
        val saved = deliver("session.saveDraft", params("sessionId" to sessionID, "text" to text, "images" to imageIds(images)))
        val s: Session = WireCoding.json.decodeFromJsonElement(saved)
        // Unless an event brought a later change first, such as its new name.
        if (_sessions.value[s.id]?.updatedAt?.isAfter(s.updatedAt) != true) _sessions.value += s.id to s
        sessionStores[s.id]?.let { store ->
            if (store.session.value?.updatedAt?.isAfter(s.updatedAt) != true) store.setSession(s)
        }
        return s
    }

    suspend fun archive(sessionID: String, removeWorktree: Boolean) {
        client.callRaw("session.archive", params("sessionId" to sessionID, "removeWorktree" to removeWorktree))
    }

    /**
     * Clears the session's unread mark, on every device: called while it is
     * shown. The row loses its dot at once, without waiting for the server.
     * An older server doesn't have the method, so errors are ignored; an index
     * update that still says unread just calls this again.
     */
    fun markRead(sessionID: String) {
        val s = _sessions.value[sessionID]?.takeIf { it.unread } ?: return
        _sessions.value += sessionID to s.copy(unread = false)
        scope.launch { attempt { client.callRaw("session.markRead", params("sessionId" to sessionID)) } }
    }

    /** Deletes a session and its transcript for good; the index then drops it. */
    suspend fun delete(sessionID: String, removeWorktree: Boolean) {
        client.callRaw("session.delete", params("sessionId" to sessionID, "removeWorktree" to removeWorktree))
    }

    // Managing the server (protocol/PROTOCOL.md, "Managing the server")

    suspend fun settings(): ServerSettings = client.call("server.settings")

    /** Replaces the settings, all of them: a null `systemAgent` unsets it. Returns what the server kept. */
    suspend fun setSettings(settings: ServerSettings): ServerSettings =
        client.call("server.setSettings", WireCoding.json.encodeToJsonElement(settings))

    /** rad's latest release, and whether it can install it; `refresh` looks again rather than use the last answer. */
    suspend fun checkUpdate(refresh: Boolean): UpdateInfo = client.call("server.checkUpdate", params("refresh" to refresh), 45.seconds)

    /** Installs rad's latest release, then rad restarts as it (ServerTaskProgress waits for it). */
    suspend fun updateServer(): ServerTask = client.call("server.update", timeout = 45.seconds)

    /** Runs the agent's own updater on the server. */
    suspend fun updateHarness(id: String): ServerTask = client.call("harness.update", params("harness" to id), 45.seconds)

    /** Signs the agent in again: the task's `url` is the page to open here. */
    suspend fun loginHarness(id: String): ServerTask = client.call("harness.login", params("harness" to id), 45.seconds)

    /** A task; `not_found` once rad has forgotten it, as after a restart. */
    suspend fun task(id: String): ServerTask = client.call("task.get", params("id" to id))

    /**
     * The tasks running and lately ended, oldest first: the server's own
     * (updates and sign-ins), or with `sessionID` that session's (commits).
     */
    suspend fun tasks(sessionID: String? = null): List<ServerTask> =
        client.call<TaskList>("task.list", params("sessionId" to sessionID)).tasks

    // Committing (protocol/PROTOCOL.md, "Committing"); only for a server whose `info.commit` is true.

    /**
     * What a commit would take in the session, with a message the system
     * agent writes for it, which may take up to a minute.
     */
    suspend fun commitPreview(sessionID: String): CommitPreview {
        // The sheet may open as the app comes back, before the socket does.
        withTimeoutOrNull(10.seconds) { waitUntilConnected() }
        return client.call("git.commitPreview", params("sessionId" to sessionID), 90.seconds)
    }

    /**
     * Commits the session's changes with `message` (blank: rad writes one),
     * then pushes the branch, and opens a pull request, as far as asked. The
     * task reports each step; follow it with a ServerTaskProgress. Unlike
     * `deliver`, it isn't sent again after a dropped connection: a commit
     * that went would push again.
     */
    suspend fun commit(sessionID: String, message: String, push: Boolean, pullRequest: Boolean): ServerTask {
        withTimeoutOrNull(10.seconds) { waitUntilConnected() }
        return client.call(
            "git.commit",
            params(
                "sessionId" to sessionID,
                "message" to message.trim().ifEmpty { null },
                "push" to (push || pullRequest).takeIf { it },
                "pullRequest" to pullRequest.takeIf { it },
            ),
            45.seconds,
        )
    }

    /**
     * Types `text` and Return into the task's command, such as a code to
     * paste. The sign-in page opens in the browser, which stops the app (and
     * so the socket), so this waits for the connection, as uploadImage does.
     * Unlike `deliver`, it isn't sent again after a dropped connection: the
     * line would be typed twice.
     */
    suspend fun taskInput(id: String, text: String) {
        withTimeoutOrNull(10.seconds) { waitUntilConnected() }
        client.callRaw("task.input", params("id" to id, "text" to text))
    }

    suspend fun cancelTask(id: String) {
        withTimeoutOrNull(10.seconds) { waitUntilConnected() }
        client.callRaw("task.cancel", params("id" to id))
    }

    companion object {
        /** Failed attempts in a row before the connection gives up. */
        const val MAX_ATTEMPTS = 5
    }
}

@Serializable
private data class HarnessList(val harnesses: List<HarnessInfo> = emptyList())

@Serializable
private data class TaskList(val tasks: List<ServerTask> = emptyList())

@Serializable
private data class ImportableList(val sessions: List<ImportableSession> = emptyList())

@Serializable
data class Branches(val branches: List<String> = emptyList(), val current: String = "")

@Serializable
data class NewSession(
    val commandId: String = UUID.randomUUID().toString(),
    val projectId: String,
    val harness: String,
    val model: String? = null,
    val effort: String? = null,
    val mode: String? = null,
    val workspace: WorkspaceParams,
    val prompt: String,
    /** Ids of uploaded images (`uploadImage`) that go with the prompt. */
    val images: List<String>? = null,
    /**
     * True: the prompt and images become the session's draft, and no turn
     * starts. Only for a server whose `info.drafts` is true; null isn't sent.
     */
    val draft: Boolean? = null,
) {
    @Serializable
    data class WorkspaceParams(val kind: Workspace.Kind, val branch: String? = null, val baseRef: String? = null)
}

/**
 * `session.fork`'s params. What is null isn't sent, and rad takes the
 * source's: its agent with its model, effort and mode (another agent's
 * defaults with another agent), and its kind of workspace.
 */
@Serializable
data class ForkSession(
    val commandId: String = UUID.randomUUID().toString(),
    val sessionId: String,
    /** The turn to continue after; null: the last one that ended. */
    val turn: Int? = null,
    val harness: String? = null,
    val model: String? = null,
    val effort: String? = null,
    val mode: String? = null,
    /** A worktree starts with the files as they were after the turn; `baseRef` isn't taken. */
    val workspace: NewSession.WorkspaceParams? = null,
    val prompt: String? = null,
    val images: List<String>? = null,
    val title: String? = null,
    val draft: Boolean? = null,
)

/**
 * `session.import`'s params: the agent's own session `nativeId`, which ran in
 * the project's folder (`ServerConnection.importable` lists them). The model,
 * effort and mode are what it goes on with; null isn't sent, and rad takes
 * the agent's defaults.
 */
@Serializable
data class ImportSession(
    val commandId: String = UUID.randomUUID().toString(),
    val projectId: String,
    val harness: String,
    val nativeId: String,
    val model: String? = null,
    val effort: String? = null,
    val mode: String? = null,
)

// Derived

val Map<String, Session>.activeSessions: List<Session>
    get() = values.filter { !it.archived }.sortedByDescending { it.updatedAt }

val Map<String, Session>.archivedSessions: List<Session>
    get() = values.filter { it.archived }.sortedByDescending { it.updatedAt }

val Map<String, Project>.sortedProjects: List<Project>
    get() = values.sortedWith(compareBy(String.CASE_INSENSITIVE_ORDER) { it.name })

