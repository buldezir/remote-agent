package dev.remoteagent.core.store

import dev.remoteagent.core.net.RpcError
import dev.remoteagent.core.net.params
import dev.remoteagent.core.protocol.ApprovalOption
import dev.remoteagent.core.protocol.Diff
import dev.remoteagent.core.protocol.Event
import dev.remoteagent.core.protocol.FileStat
import dev.remoteagent.core.protocol.ImageRef
import dev.remoteagent.core.protocol.Item
import dev.remoteagent.core.protocol.ItemKind
import dev.remoteagent.core.protocol.ItemStatus
import dev.remoteagent.core.protocol.Session
import dev.remoteagent.core.protocol.SlashCommand
import dev.remoteagent.core.protocol.SessionStatus
import dev.remoteagent.core.protocol.Turn
import dev.remoteagent.core.protocol.WireCoding
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.decodeFromJsonElement
import java.util.UUID
import kotlin.time.Duration
import kotlin.time.Duration.Companion.seconds

/** What a session store needs from its connection (web/src/store/sessionStore.ts's SessionHost). */
interface SessionHost {
    val scope: CoroutineScope
    val isConnected: Boolean
    suspend fun callRaw(method: String, params: JsonElement, timeout: Duration = 30.seconds): JsonElement
    /** Calls a command that carries a commandId, sending it again after a dropped connection (ServerConnection's). */
    suspend fun deliver(method: String, params: JsonElement, timeout: Duration = 30.seconds): JsonElement
    fun activate(store: SessionStore)
    fun deactivate(store: SessionStore)
    suspend fun subscribe(store: SessionStore)
    /** Where sessions' drafts are kept, if anywhere. */
    val drafts: Drafts? get() = null
}

/** A prompt on its way to the server. */
data class Outgoing(val text: String, val images: List<ImageRef>)

/**
 * The live state of one session: its turns and transcript items.
 * Mirrors apple/RAKit/Sources/RAKit/Store/SessionStore.swift.
 */
class SessionStore(val sessionID: String, private val host: SessionHost?) {
    private val _session = MutableStateFlow<Session?>(null)
    val session: StateFlow<Session?> = _session.asStateFlow()
    private val _turns = MutableStateFlow<Map<String, Turn>>(emptyMap())
    val turns: StateFlow<Map<String, Turn>> = _turns.asStateFlow()
    /** Top-level transcript items in order (children of tool calls are excluded). */
    private val _transcript = MutableStateFlow<List<Item>>(emptyList())
    val transcript: StateFlow<List<Item>> = _transcript.asStateFlow()
    /** Items nested under a tool call (a sub-agent's steps), by the parent's id. */
    private val _children = MutableStateFlow<Map<String, List<Item>>>(emptyMap())
    val children: StateFlow<Map<String, List<Item>>> = _children.asStateFlow()
    private val _synced = MutableStateFlow(false)
    val synced: StateFlow<Boolean> = _synced.asStateFlow()
    /** The session was deleted on the server: its stream says so. */
    private val _removed = MutableStateFlow(false)
    val removed: StateFlow<Boolean> = _removed.asStateFlow()
    /** Optimistic user messages not yet confirmed by the server, keyed by commandId, in the order sent. */
    private val _outbox = MutableStateFlow<Map<String, Outgoing>>(emptyMap())
    val outbox: StateFlow<Map<String, Outgoing>> = _outbox.asStateFlow()

    private val turnMap = HashMap<String, Turn>()
    private val itemMap = HashMap<String, Item>()
    val items: Map<String, Item> get() = itemMap

    internal var lastSeq = 0L
    internal var live = false
    // Views showing the session. Two panes can show the same one.
    private var viewers = 0
    val isActive: Boolean get() = viewers > 0

    /**
     * The prompt being written and not sent yet. It outlives the session's
     * screen, and the app, but stays on this device. Setting it doesn't
     * notify: the composer keeps its own copy as it is typed.
     */
    var draft: String
        get() = host?.drafts?.get(sessionID) ?: ""
        set(value) {
            host?.drafts?.set(sessionID, value)
        }

    /**
     * Start receiving updates (when a view appears). Updates stop when every
     * activate() has had its deactivate().
     */
    fun activate() {
        viewers += 1
        if (viewers == 1) host?.activate(this)
    }

    fun deactivate() {
        if (viewers == 0) return
        viewers -= 1
        if (viewers > 0) return
        live = false
        host?.deactivate(this)
    }

    // Sync

    /**
     * Replayed events are buffered until `synchronized`, then applied in one
     * step (after dropping cached state if the server says reset).
     */
    private var replay = mutableListOf<Event>()

    /**
     * Subscriptions asked for and not yet synchronized. Two can overlap (a
     * view subscribes as the connection resubscribes everything): only the
     * last one's `synchronized` makes the store live, so a newer replay is
     * buffered rather than skipped, and its reset can't drop what it replays.
     */
    private var outstanding = 0

    internal fun beginReplay() {
        outstanding += 1
        live = false
        replay = mutableListOf()
    }

    /** A subscribe call failed: its `synchronized` won't come. */
    internal fun subscriptionFailed() {
        outstanding = maxOf(0, outstanding - 1)
    }

    /** The socket closed: no subscription is outstanding any more. */
    internal fun closed() {
        outstanding = 0
        live = false
    }

    internal fun synchronized(seq: Long, reset: Boolean) {
        Log.d { "session $sessionID synchronized seq=$seq reset=$reset replayed=${replay.size} lastSeq=$lastSeq outstanding=$outstanding" }
        outstanding = maxOf(0, outstanding - 1)
        if (reset) {
            turnMap.clear()
            itemMap.clear()
        }
        val buffered = replay
        replay = mutableListOf()
        for (ev in buffered) upsert(ev)
        lastSeq = seq
        live = outstanding == 0
        _synced.value = true
        _turns.value = turnMap.toMap()
        rebuild()
    }

    internal fun apply(events: List<Event>) {
        Log.d { "session $sessionID events ${events.firstOrNull()?.seq}..${events.lastOrNull()?.seq} (${events.size}) live=$live lastSeq=$lastSeq" }
        if (!live) {
            replay.addAll(events)
            return
        }
        var changed = false
        for (ev in events) {
            if (ev.seq <= lastSeq) continue
            if (ev.seq != lastSeq + 1) {
                Log.d { "session $sessionID gap: got ${ev.seq} after $lastSeq" }
                // Gap: we missed something. Resubscribe from what we have.
                live = false
                host?.let { h -> h.scope.launch { h.subscribe(this@SessionStore) } }
                break
            }
            lastSeq = ev.seq
            changed = upsert(ev) || changed
        }
        _turns.value = turnMap.toMap()
        if (changed) rebuild()
    }

    /** The connection's copy of the session, from the index. */
    internal fun setSession(s: Session?) {
        _session.value = s
    }

    private fun upsert(ev: Event): Boolean {
        when (ev.type) {
            "session.upserted" -> ev.session?.let { _session.value = it }
            "turn.upserted" -> ev.turn?.let { turnMap[it.id] = it }
            "item.upserted" -> ev.item?.let {
                itemMap[it.id] = it
                return true
            }
            "session.removed" -> {
                _session.value = null
                turnMap.clear()
                itemMap.clear()
                _outbox.value = emptyMap()
                _removed.value = true
                return true
            }
        }
        return false
    }

    private fun rebuild() {
        val top = ArrayList<Item>()
        val kids = HashMap<String, MutableList<Item>>()
        for (it in itemMap.values) {
            val p = it.parentItemId
            if (p != null && itemMap.containsKey(p)) kids.getOrPut(p) { ArrayList() }.add(it) else top.add(it)
        }
        top.sortBy { it.order }
        for (list in kids.values) list.sortBy { it.order }
        // Drop optimistic messages the server has confirmed.
        val confirmed = top.filter { it.kind == ItemKind.USER_MESSAGE }.map { key(it.text ?: "", it.images ?: emptyList()) }.toSet()
        _outbox.value = _outbox.value.filterValues { key(it.text, it.images) !in confirmed }
        _transcript.value = top
        _children.value = kids
    }

    private fun key(text: String, images: List<ImageRef>) = text to images.map { it.id }

    fun children(of: Item): List<Item> = _children.value[of.id] ?: emptyList()

    val sortedTurns: List<Turn> get() = _turns.value.values.sortedBy { it.n }

    fun turn(id: String?): Turn? = id?.let { _turns.value[it] }

    val pendingApprovals: List<Item>
        get() = _transcript.value.filter { it.kind == ItemKind.APPROVAL && it.status == ItemStatus.PENDING }

    val isRunning: Boolean
        get() = _session.value?.status.let { it == SessionStatus.RUNNING || it == SessionStatus.AWAITING_APPROVAL }

    /** The connection is up, so images can load. */
    val isConnected: Boolean get() = host?.isConnected ?: false

    // Commands

    private val c: SessionHost get() = host ?: throw RpcError.disconnected()

    /**
     * Sends a prompt. It shows in `outbox` until the transcript has it, and
     * waits out a dropped connection; it throws once the connection gives up.
     */
    suspend fun send(text: String, images: List<ImageRef> = emptyList()) {
        val c = c
        val commandId = UUID.randomUUID().toString()
        _outbox.value += commandId to Outgoing(text, images)
        try {
            c.deliver(
                "session.prompt",
                params("commandId" to commandId, "sessionId" to sessionID, "text" to text, "images" to imageIds(images)),
            )
        } catch (e: Throwable) {
            _outbox.value -= commandId
            throw e
        }
    }

    /**
     * Sends the session's draft (`Session.draft`) as its prompt. As `send`,
     * it shows in `outbox` until the transcript has it, and waits out a
     * dropped connection; it throws once the connection gives up, or when the
     * session has no draft.
     */
    suspend fun sendDraft() {
        val c = c
        val saved = _session.value?.draft ?: throw RpcError("conflict", "The session has no draft.")
        val commandId = UUID.randomUUID().toString()
        _outbox.value += commandId to Outgoing(saved.text, saved.images)
        try {
            c.deliver("session.sendDraft", params("commandId" to commandId, "sessionId" to sessionID))
        } catch (e: Throwable) {
            _outbox.value -= commandId
            throw e
        }
    }

    suspend fun commands(): List<SlashCommand> {
        try {
            val result = c.callRaw("session.commands", params("sessionId" to sessionID), timeout = 80.seconds)
            return WireCoding.json.decodeFromJsonElement<CommandList>(result).commands
        } catch (error: RpcError) {
            if (error.code == "method_not_found") return emptyList()
            throw error
        }
    }

    @Serializable
    private data class CommandList(val commands: List<SlashCommand>)

    suspend fun interrupt(force: Boolean = false) {
        c.callRaw("session.interrupt", params("sessionId" to sessionID, "force" to force))
    }

    /**
     * Sends a queued prompt at once: rad interrupts the running turn and
     * starts the next one with it, ahead of the other queued prompts.
     */
    suspend fun sendNow(itemId: String) {
        try {
            c.callRaw("session.sendNow", params("sessionId" to sessionID, "itemId" to itemId))
        } catch (e: RpcError) {
            if (e.code != "method_not_found") throw e
            throw RpcError(e.code, "This server's rad is too old to send a queued prompt now. Update rad on it.")
        }
    }

    suspend fun respond(approval: Item, option: ApprovalOption, message: String? = null, answers: Map<String, String>? = null) {
        c.callRaw(
            "approval.respond",
            params(
                "commandId" to UUID.randomUUID().toString(),
                "sessionId" to sessionID,
                "approvalId" to approval.id,
                "optionId" to option.id,
                "message" to message,
                "answers" to answers?.let { a -> JsonObject(a.mapValues { JsonPrimitive(it.value) }) },
            ),
        )
    }

    suspend fun setMode(mode: String) {
        c.callRaw("session.setMode", params("sessionId" to sessionID, "mode" to mode))
    }

    suspend fun turnDiff(turnId: String): Diff =
        WireCoding.json.decodeFromJsonElement(c.callRaw("git.turnDiff", params("turnId" to turnId)))

    suspend fun sessionDiff(): Diff =
        WireCoding.json.decodeFromJsonElement(c.callRaw("git.sessionDiff", params("sessionId" to sessionID)))

    suspend fun revert(toTurn: Int): List<FileStat> =
        WireCoding.json.decodeFromJsonElement<RevertResult>(c.callRaw("git.revert", params("sessionId" to sessionID, "turn" to toTurn))).files
}

@Serializable
private data class RevertResult(val files: List<FileStat> = emptyList())

/** The ids of uploaded images, for a prompt; null for none. */
internal fun imageIds(images: List<ImageRef>): JsonArray? =
    images.takeIf { it.isNotEmpty() }?.let { list -> JsonArray(list.map { JsonPrimitive(it.id) }) }
