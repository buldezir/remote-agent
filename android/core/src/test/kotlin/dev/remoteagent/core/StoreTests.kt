package dev.remoteagent.core

import dev.remoteagent.core.net.PairingLink
import dev.remoteagent.core.net.RpcError
import dev.remoteagent.core.net.params
import dev.remoteagent.core.protocol.Event
import dev.remoteagent.core.protocol.Item
import dev.remoteagent.core.protocol.ItemKind
import dev.remoteagent.core.protocol.ItemStatus
import dev.remoteagent.core.protocol.Session
import dev.remoteagent.core.protocol.Turn
import dev.remoteagent.core.protocol.TurnStatus
import dev.remoteagent.core.protocol.WireCoding
import dev.remoteagent.core.store.Drafts
import dev.remoteagent.core.store.ImageCache
import dev.remoteagent.core.store.Outgoing
import dev.remoteagent.core.store.SavedServer
import dev.remoteagent.core.store.ServerConnection
import dev.remoteagent.core.store.SessionHost
import dev.remoteagent.core.store.SessionStore
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.decodeFromJsonElement
import java.time.Instant
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertFalse
import kotlin.test.assertNotNull
import kotlin.test.assertNull
import kotlin.test.assertTrue
import kotlin.time.Duration

/** As apple/RAKit/Tests/RAKitTests/StoreTests.swift and web/src/store/sessionStore.test.ts. */
class SessionStoreTests {
    private fun event(seq: Long, id: String, order: Long, text: String, kind: ItemKind = ItemKind.ASSISTANT_MESSAGE, parent: String? = null) =
        Event(
            stream = "session:s", seq = seq, type = "item.upserted",
            item = Item(
                id = id, sessionId = "s", parentItemId = parent, order = order, kind = kind, status = ItemStatus.COMPLETED, text = text,
                createdAt = Instant.parse("2026-10-09T12:00:00.123456789Z"), updatedAt = Instant.parse("2026-10-09T12:00:00Z"),
            ),
        )

    private class Host(override val scope: CoroutineScope) : SessionHost {
        var subscribed = 0
        var activated = 0
        var deactivated = 0
        val calls = mutableListOf<Pair<String, JsonElement>>()
        var fail: Exception? = null
        override val isConnected = true
        override suspend fun callRaw(method: String, params: JsonElement, timeout: Duration): JsonElement {
            calls += method to params
            fail?.let { throw it }
            return JsonObject(emptyMap())
        }
        override suspend fun deliver(method: String, params: JsonElement, timeout: Duration) = callRaw(method, params, timeout)
        override fun activate(store: SessionStore) {
            activated++
        }
        override fun deactivate(store: SessionStore) {
            deactivated++
        }
        override suspend fun subscribe(store: SessionStore) {
            subscribed++
        }
    }

    @Test fun oldServerCommandFallback() = runTest {
        val host = Host(this)
        host.fail = RpcError("method_not_found", "old server")
        val store = SessionStore("s", host)
        assertEquals(emptyList(), store.commands())
        host.fail = RpcError("conflict", "runtime replaced")
        assertFailsWith<RpcError> { store.commands() }
    }

    @Test
    fun replayThenLive() {
        val s = SessionStore("s", null)
        // Replay (non-contiguous seqs are fine before synchronized).
        s.apply(listOf(event(3, "b", 2, "second"), event(7, "a", 1, "first")))
        assertTrue(s.transcript.value.isEmpty()) // not rebuilt until synchronized
        s.synchronized(7, true)
        assertEquals(listOf("a", "b"), s.transcript.value.map { it.id })
        // Live, contiguous.
        s.apply(listOf(event(8, "b", 2, "second, edited")))
        assertEquals("second, edited", s.transcript.value.last().text)
        // A duplicate seq is ignored.
        s.apply(listOf(event(8, "b", 2, "stale")))
        assertEquals("second, edited", s.transcript.value.last().text)
    }

    @Test
    fun resetDropsStaleStateButKeepsReplay() {
        val s = SessionStore("s", null)
        s.synchronized(1, true)
        s.apply(listOf(event(2, "old", 1, "old")))
        assertEquals(listOf("old"), s.transcript.value.map { it.id })
        // Resubscribe from scratch: the server replays only what exists now.
        s.beginReplay()
        s.apply(listOf(event(9, "new", 1, "new")))
        s.synchronized(9, true)
        assertEquals(listOf("new"), s.transcript.value.map { it.id })
    }

    @Test
    fun overlappingSubscriptionsKeepTheLastReplay() {
        val s = SessionStore("s", null)
        // A view subscribes while the connection resubscribes everything.
        s.beginReplay()
        s.beginReplay()
        val replay = listOf(event(3, "b", 2, "second"), event(7, "a", 1, "first"))
        s.apply(replay)
        s.synchronized(7, true)
        assertFalse(s.live) // the second subscription hasn't synchronized yet
        s.apply(replay)
        s.synchronized(7, true)
        assertTrue(s.live)
        assertEquals(listOf("a", "b"), s.transcript.value.map { it.id })
    }

    @Test
    fun gapResubscribes() = runTest {
        val h = Host(this)
        val s = SessionStore("s", h)
        s.synchronized(1, true)
        s.apply(listOf(event(5, "x", 1, "after a gap")))
        assertNull(s.items["x"])
        assertFalse(s.live)
        runCurrent()
        assertEquals(1, h.subscribed)
    }

    @Test
    fun childrenNestUnderParent() {
        val s = SessionStore("s", null)
        s.apply(listOf(event(1, "tool", 1, "", ItemKind.NOTICE), event(2, "child", 2, "sub", parent = "tool")))
        s.synchronized(2, true)
        assertEquals(listOf("tool"), s.transcript.value.map { it.id })
        assertEquals(listOf("child"), s.children(s.transcript.value.first()).map { it.id })
    }

    @Test
    fun staysActiveWhileAnyViewShowsIt() = runTest {
        val h = Host(this)
        val s = SessionStore("s", h)
        s.activate()
        s.activate()
        s.deactivate()
        assertTrue(s.isActive)
        s.deactivate()
        assertFalse(s.isActive)
        s.deactivate()
        s.activate()
        assertTrue(s.isActive)
        assertEquals(2, h.activated)
        assertEquals(1, h.deactivated)
    }

    @Test
    fun outboxKeepsPromptUntilConfirmed() = runTest {
        val h = Host(this)
        val s = SessionStore("s", h)
        s.synchronized(0, true)
        s.send("hello")
        assertEquals(listOf(Outgoing("hello", emptyList())), s.outbox.value.values.toList())
        assertEquals("session.prompt", h.calls[0].first)
        s.apply(listOf(event(1, "u", 1, "hello", ItemKind.USER_MESSAGE)))
        assertTrue(s.outbox.value.isEmpty())
    }

    @Test
    fun outboxDropsEntryWhenSendingFails() = runTest {
        val h = Host(this).apply { fail = IllegalStateException("nope") }
        val s = SessionStore("s", h)
        assertFailsWith<IllegalStateException> { s.send("hello") }
        assertTrue(s.outbox.value.isEmpty())
    }

    @Test
    fun sendNowNamesTheQueuedPrompt() = runTest {
        val h = Host(this)
        val s = SessionStore("s", h)
        s.sendNow("q")
        assertEquals("session.sendNow", h.calls[0].first)
        assertEquals(params("sessionId" to "s", "itemId" to "q"), h.calls[0].second)
    }

    @Test
    fun sendNowOnAnOlderRadSaysToUpdateIt() = runTest {
        val h = Host(this).apply { fail = RpcError("method_not_found", "unknown method session.sendNow") }
        val s = SessionStore("s", h)
        val e = assertFailsWith<RpcError> { s.sendNow("q") }
        assertEquals("method_not_found", e.code)
        assertEquals("This server's rad is too old to send a queued prompt now. Update rad on it.", e.message)
        // Other errors are the server's own.
        h.fail = RpcError("not_found", "no such session")
        assertEquals("no such session", assertFailsWith<RpcError> { s.sendNow("q") }.message)
    }

    private fun turn(seq: Long, id: String) = Event(
        stream = "session:s", seq = seq, type = "turn.upserted",
        turn = Turn(id = id, sessionId = "s", n = 1, status = TurnStatus.COMPLETED, startedAt = Instant.parse("2026-10-09T12:00:00Z")),
    )

    private fun removed(seq: Long) = Event(stream = "session:s", seq = seq, type = "session.removed", id = "s")

    @Test
    fun deletedSessionEmptiesTheStore() = runTest {
        val h = Host(this)
        val s = SessionStore("s", h)
        s.setSession(WireCoding.json.decodeFromJsonElement<Session>(FakeRad.session("s")))
        s.apply(listOf(turn(1, "t"), event(2, "a", 1, "first")))
        s.synchronized(2, true)
        s.send("unconfirmed")
        assertFalse(s.removed.value)
        // Deleted, here or on another device: the stream's next event says so.
        s.apply(listOf(removed(3)))
        assertTrue(s.removed.value)
        assertNull(s.session.value)
        assertTrue(s.turns.value.isEmpty())
        assertTrue(s.transcript.value.isEmpty())
        assertTrue(s.items.isEmpty())
        assertTrue(s.outbox.value.isEmpty())
    }

    @Test
    fun subscribingToADeletedSessionReplaysItsRemoval() {
        val s = SessionStore("s", null)
        // Its stream holds only the removal, the next in its seq.
        s.apply(listOf(removed(12)))
        assertFalse(s.removed.value) // not applied until synchronized
        s.synchronized(12, true)
        assertTrue(s.removed.value)
        assertTrue(s.transcript.value.isEmpty())
    }
}

/** As PairingTests in StoreTests.swift and web/src/protocol/pairing.test.ts. */
/** Drafts stay on the device, by server and session. As DraftTests in apple/RAKit/Tests/RAKitTests/StoreTests.swift. */
class DraftTests {
    private val storage = MemoryStorage()
    private val server = SavedServer("srv", "Test", listOf("http://127.0.0.1:1"), "d", Instant.now())

    @Test
    fun outliveTheStoreAndTheApp() = runTest {
        fun connection() = ServerConnection(server, "", backgroundScope, ImageCache(null), {}, storage = storage)
        val c = connection()
        c.sessionStore("a").draft = "half a thought"
        assertEquals("", c.sessionStore("b").draft)
        // The next launch reads it back.
        assertEquals("half a thought", connection().sessionStore("a").draft)
        // Sent: forgotten.
        c.sessionStore("a").draft = ""
        assertNull(storage["drafts.srv"])
    }

    @Test
    fun forgetSessionsGoneFromTheServer() {
        val d = Drafts(storage, "srv")
        d["a"] = "kept"
        d["b"] = "gone"
        d.keep { it == "a" }
        val reread = Drafts(storage, "srv")
        assertEquals("kept", reread["a"])
        assertEquals("", reread["b"])
        d.removeAll()
        assertNull(storage["drafts.srv"])
    }
}

class PairingTests {
    @Test
    fun parsesLink() {
        val l = assertNotNull(PairingLink.parse("remoteagent://pair?v=1&name=studio&code=abc&url=http%3A%2F%2F100.64.1.2%3A7421&url=http%3A%2F%2F127.0.0.1%3A7421"))
        assertEquals(PairingLink("studio", "abc", listOf("http://100.64.1.2:7421", "http://127.0.0.1:7421")), l)
        assertNull(PairingLink.parse("https://example.com"))
        assertNull(PairingLink.parse("remoteagent://pair?code=abc"))
    }

    @Test
    fun decodesNameWithSpaces() {
        // As written by netinfo.PairingLink for `name = "Studio Mac + iPad"`.
        val l = PairingLink.parse("remoteagent://pair?code=abc&name=Studio%20Mac%20%2B%20iPad&url=http%3A%2F%2F127.0.0.1%3A7421&v=1")
        assertEquals("Studio Mac + iPad", l?.name)
    }

    @Test
    fun keepsOnlyServerAddresses() {
        // Trailing slashes go; paths and other schemes aren't server addresses.
        val l = PairingLink.parse("  remoteagent://pair?code=abc&url=http%3A%2F%2Fh%3A1%2F&url=http%3A%2F%2Fh%3A2%2Fx&url=ftp%3A%2F%2Fh  ")
        assertEquals(listOf("http://h:1"), l?.urls)
        assertEquals("Server", l?.name)
    }

    @Test
    fun roundTrips() {
        val l = PairingLink("Studio Mac + iPad", "abc", listOf("http://127.0.0.1:7421"))
        assertEquals(l, PairingLink.parse(l.toLinkString()))
    }
}
