package dev.remoteagent.core

import dev.remoteagent.core.FakeRad.Companion.events
import dev.remoteagent.core.FakeRad.Companion.project
import dev.remoteagent.core.FakeRad.Companion.session
import dev.remoteagent.core.FakeRad.Companion.upserted
import dev.remoteagent.core.net.PING_INTERVAL
import dev.remoteagent.core.net.RpcError
import dev.remoteagent.core.net.params
import dev.remoteagent.core.protocol.ImageRef
import dev.remoteagent.core.protocol.WireCoding
import dev.remoteagent.core.protocol.Workspace
import dev.remoteagent.core.store.ConnectionState
import dev.remoteagent.core.store.ForkSession
import dev.remoteagent.core.store.ImageCache
import dev.remoteagent.core.store.ImportSession
import dev.remoteagent.core.store.NewSession
import dev.remoteagent.core.store.SavedServer
import dev.remoteagent.core.store.ServerConnection
import dev.remoteagent.core.store.activeSessions
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.boolean
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.long
import kotlinx.serialization.json.put
import mockwebserver3.MockResponse
import mockwebserver3.MockWebServer
import java.net.InetSocketAddress
import java.time.Instant
import java.util.concurrent.Executors
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertFalse
import kotlin.test.assertIs
import kotlin.test.assertNull
import kotlin.test.assertTrue
import kotlin.time.Duration
import kotlin.time.Duration.Companion.milliseconds
import kotlin.time.Duration.Companion.seconds

/** The index stream's sync rules (protocol/PROTOCOL.md, "Streams and sync") against a fake rad. */
class ServerConnectionTests {
    private val server = MockWebServer().apply { start() }
    private val rad = FakeRad()
    /** The rad a reconnect reaches, in the tests that need two. */
    private val next = FakeRad()
    private val relay = lazy { Relay(InetSocketAddress(server.hostName, server.port)) }
    // The app changes a connection's state on the main thread; here, on this one.
    private val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
    private val scope = CoroutineScope(SupervisorJob() + dispatcher)

    @AfterTest
    fun tearDown() {
        rad.shutDown()
        next.shutDown()
        scope.cancel()
        dispatcher.close()
        if (relay.isInitialized()) relay.value.close()
        server.close()
    }

    private suspend fun connect(
        firstRetryDelay: Duration = 1.seconds,
        base: String = server.base(),
        pingInterval: Duration = PING_INTERVAL,
    ): ServerConnection {
        val saved = SavedServer("srv", "Test", listOf(base), "dev1", Instant.now())
        val c = ServerConnection(saved, "tok", scope, ImageCache(null), {}, pingInterval = pingInterval)
        c.firstRetryDelay = firstRetryDelay
        withContext(dispatcher) { c.start() }
        return c
    }

    /** Polls until `condition` holds, or fails after 10 s. */
    private suspend fun waitUntil(condition: () -> Boolean) = withTimeout(10.seconds) {
        while (!condition()) delay(5)
    }

    private fun synchronized(seq: Long, reset: Boolean) = buildJsonObject {
        put("stream", "index")
        put("seq", seq)
        put("reset", reset)
    }

    private fun subscribed(seq: Long) = buildJsonObject {
        put("stream", "index")
        put("seq", seq)
    }

    @Test
    fun replaysThenAppliesLiveEventsAndResubscribesOnGaps() = runBlocking {
        rad.answer = { r, method, params ->
            when (method) {
                "server.info" -> FakeRad.serverInfo
                "subscribe" -> {
                    val after = params.getValue("afterSeq").jsonPrimitive.long
                    if (after == 0L) {
                        // The replay isn't contiguous: older versions were compacted away.
                        r.notify("events", events("index", 3L to upserted("project", project("p1")), 5L to upserted("session", session("s1"))))
                        r.notify("synchronized", synchronized(5, reset = true))
                        subscribed(5)
                    } else {
                        r.notify("synchronized", synchronized(after, reset = false))
                        subscribed(after)
                    }
                }
                else -> JsonObject(emptyMap())
            }
        }
        server.upgrade(rad)
        val c = connect()
        withTimeout(5.seconds) { c.indexSynced.first { it } }
        assertEquals(ConnectionState.Connected, c.state.value)
        assertEquals(setOf("p1"), c.projects.value.keys)
        assertEquals(setOf("s1"), c.sessions.value.keys)
        rad.received.clear()

        // Live events are contiguous.
        rad.notify("events", events("index", 6L to upserted("session", session("s2", updatedAt = "2026-10-09T13:00:00Z"))))
        withTimeout(5.seconds) { c.sessions.first { "s2" in it } }
        assertEquals(listOf("s2", "s1"), c.sessions.value.activeSessions.map { it.id })

        // A gap: resubscribe from the last applied seq, and skip what came after it.
        rad.notify("events", events("index", 9L to upserted("session", session("s3"))))
        assertEquals(6, rad.nextCall("subscribe")["params"]!!.let { (it as JsonObject).getValue("afterSeq").jsonPrimitive.long })
        assertFalse("s3" in c.sessions.value)

        // The server says this client fell behind.
        rad.notify("resync", buildJsonObject { put("stream", "index") })
        assertEquals(6, (rad.nextCall("subscribe")["params"] as JsonObject).getValue("afterSeq").jsonPrimitive.long)

        // The socket drops: reconnect, and ask only for what changed since.
        server.upgrade(rad)
        rad.socket!!.close(1001, "going away")
        rad.nextCall("server.info")
        assertEquals(6, (rad.nextCall("subscribe")["params"] as JsonObject).getValue("afterSeq").jsonPrimitive.long)
        withTimeout(5.seconds) { c.state.first { it == ConnectionState.Connected } }
        withContext(dispatcher) { c.stop() }
    }

    @Test
    fun revokedDeviceFails() = runBlocking {
        repeat(2) { server.enqueue(MockResponse.Builder().code(401).body("""{"code":"unauthorized","message":"bad token"}""").build()) }
        val c = connect(firstRetryDelay = 10.milliseconds)
        val failed = withTimeout(5.seconds) { c.state.first { it is ConnectionState.Failed } }
        assertEquals(ConnectionState.Failed(RpcError.unauthorized().message), failed)
        // Trying again won't bring back a revoked token, so it doesn't.
        delay(200)
        assertEquals(1, server.requestCount)
        withContext(dispatcher) { c.stop() }
    }

    @Test
    fun givesUpUntilStartedAgain() = runBlocking {
        // Each attempt's upgrade is turned away; one spare shows any attempt too many.
        val rounds = 3
        repeat(rounds * ServerConnection.MAX_ATTEMPTS + 1) { server.enqueue(MockResponse.Builder().code(503).build()) }
        val saved = SavedServer("srv", "Test", listOf(server.base()), "dev1", Instant.now())
        val c = ServerConnection(saved, "tok", scope, ImageCache(null), {})
        c.firstRetryDelay = 10.milliseconds
        val seen = mutableListOf<ConnectionState>() // only touched on the dispatcher
        withContext(dispatcher) {
            scope.launch { c.state.collect { seen += it } }
            c.start()
        }
        suspend fun seen() = withContext(dispatcher) { seen.toList() }
        val failed = ConnectionState.Failed("Could not connect to ${server.base()}")
        waitUntil { c.state.value is ConnectionState.Failed }
        assertEquals(failed, c.state.value)
        // Connecting between attempts, failed only once it gave up.
        assertEquals(listOf(ConnectionState.Idle, ConnectionState.Connecting, failed), seen())
        assertEquals(ServerConnection.MAX_ATTEMPTS, server.requestCount)

        // Given up: no more attempts, so it stays failed.
        delay(200)
        assertEquals(failed, c.state.value)
        assertEquals(ServerConnection.MAX_ATTEMPTS, server.requestCount)

        // Opening the server, or coming back to the app, tries again.
        withContext(dispatcher) { c.start() }
        waitUntil { server.requestCount == 2 * ServerConnection.MAX_ATTEMPTS && c.state.value is ConnectionState.Failed }
        // So does Retry.
        withContext(dispatcher) { c.retry() }
        waitUntil { server.requestCount == rounds * ServerConnection.MAX_ATTEMPTS && c.state.value is ConnectionState.Failed }
        delay(200)
        assertEquals(rounds * ServerConnection.MAX_ATTEMPTS, server.requestCount)
        val connecting = listOf(ConnectionState.Connecting, failed)
        assertEquals(listOf(ConnectionState.Idle) + List(rounds) { connecting }.flatten(), seen())
        withContext(dispatcher) { c.stop() }
    }

    private fun commandId(call: JsonObject) = (call["params"] as JsonObject).getValue("commandId").jsonPrimitive.content

    @Test
    fun promptSentOnADeadSocketIsDeliveredAfterReconnecting() = runBlocking {
        server.upgrade(rad)
        server.upgrade(next)
        val c = connect(base = relay.value.base, pingInterval = 100.milliseconds)
        withTimeout(5.seconds) { c.state.first { it == ConnectionState.Connected } }
        val store = withContext(dispatcher) { c.sessionStore("s1") }

        // Gone quiet, as after a sleep: the prompt gets there, but its answer
        // doesn't, and nor does the pong, so the socket is dropped.
        relay.value.quiet()
        withContext(dispatcher) { store.send("hello") }

        // Sent again on the new socket, with the same commandId.
        assertEquals(2, relay.value.connectionCount)
        assertEquals(commandId(rad.nextCall("session.prompt")), commandId(next.nextCall("session.prompt")))
        // Shown until the transcript has it.
        assertEquals(listOf("hello"), store.outbox.value.values.map { it.text })
        withContext(dispatcher) { c.stop() }
    }

    @Test
    fun promptFailsOnceTheConnectionGivesUp() = runBlocking {
        // The socket drops as the prompt goes, and rad doesn't come back.
        rad.answer = { r, method, _ ->
            when (method) {
                "server.info" -> FakeRad.serverInfo
                "session.prompt" -> {
                    r.socket?.close(1001, "going away")
                    null
                }
                else -> JsonObject(emptyMap())
            }
        }
        server.upgrade(rad)
        repeat(ServerConnection.MAX_ATTEMPTS) { server.enqueue(MockResponse.Builder().code(503).build()) }
        val c = connect(firstRetryDelay = 10.milliseconds)
        withTimeout(5.seconds) { c.state.first { it == ConnectionState.Connected } }
        val store = withContext(dispatcher) { c.sessionStore("s1") }

        val e = assertFailsWith<RpcError> { withContext(dispatcher) { store.send("hello") } }
        assertEquals("disconnected", e.code)
        assertIs<ConnectionState.Failed>(c.state.value)
        assertTrue(store.outbox.value.isEmpty())
        withContext(dispatcher) { c.stop() }
    }

    @Test
    fun draftSentOnADeadSocketIsDeliveredAfterReconnecting() = runBlocking {
        server.upgrade(rad)
        server.upgrade(next)
        val c = connect(base = relay.value.base, pingInterval = 100.milliseconds)
        withTimeout(5.seconds) { c.state.first { it == ConnectionState.Connected } }
        val store = withContext(dispatcher) {
            c.sessionStore("s1").also { it.setSession(WireCoding.json.decodeFromJsonElement(session("s1", draft = "later"))) }
        }

        relay.value.quiet()
        withContext(dispatcher) { store.sendDraft() }

        // Sent again on the new socket, with the same commandId.
        assertEquals(2, relay.value.connectionCount)
        assertEquals(commandId(rad.nextCall("session.sendDraft")), commandId(next.nextCall("session.sendDraft")))
        assertEquals(listOf("later"), store.outbox.value.values.map { it.text })
        withContext(dispatcher) { c.stop() }
    }

    /** rad's answers for an index of one session, s1, that takes drafts. */
    private fun answerDrafts(r: FakeRad, method: String, params: JsonObject) = when (method) {
        "server.info" -> FakeRad.serverInfo
        "subscribe" -> {
            r.notify("events", events("index", 1L to upserted("project", project("p1")), 2L to upserted("session", session("s1"))))
            r.notify("synchronized", synchronized(2, reset = true))
            subscribed(2)
        }
        "session.create" -> session("s2", draft = params["draft"]?.let { params.getValue("prompt").jsonPrimitive.content })
        "session.fork" -> session("s3")
        "session.saveDraft" -> session("s1", updatedAt = "2026-10-09T13:00:00Z", draft = params.getValue("text").jsonPrimitive.content.ifEmpty { null })
        else -> JsonObject(emptyMap())
    }

    @Test
    fun savedDraftChangesTheSessionAtOnce() = runBlocking {
        rad.answer = ::answerDrafts
        server.upgrade(rad)
        val c = connect()
        withTimeout(5.seconds) { c.indexSynced.first { it } }
        val store = withContext(dispatcher) { c.sessionStore("s1") }
        val ref = ImageRef("a1b2.png", "image/png", size = 100)

        val s = withContext(dispatcher) { c.saveDraft("s1", "then the changelog", listOf(ref)) }
        assertEquals(
            params("sessionId" to "s1", "text" to "then the changelog", "images" to JsonArray(listOf(JsonPrimitive("a1b2.png")))),
            rad.nextCall("session.saveDraft")["params"],
        )
        assertEquals("then the changelog", s.draft?.text)
        // In the lists and the open session, without waiting for the event.
        assertEquals("then the changelog", c.sessions.value.getValue("s1").draft?.text)
        assertEquals("then the changelog", store.session.value?.draft?.text)

        // Nothing removes it.
        withContext(dispatcher) { c.saveDraft("s1", "", emptyList()) }
        assertEquals(params("sessionId" to "s1", "text" to ""), rad.nextCall("session.saveDraft")["params"])
        assertNull(c.sessions.value.getValue("s1").draft)
        assertNull(store.session.value?.draft)
        withContext(dispatcher) { c.stop() }
    }

    @Test
    fun newSessionAsksForADraftOnlyWhenSaving() = runBlocking {
        rad.answer = ::answerDrafts
        server.upgrade(rad)
        val c = connect()
        withTimeout(5.seconds) { c.indexSynced.first { it } }
        fun new(prompt: String, draft: Boolean?) = NewSession(
            projectId = "p1", harness = "fake", workspace = NewSession.WorkspaceParams(Workspace.Kind.ROOT), prompt = prompt, draft = draft,
        )

        val s = withContext(dispatcher) { c.createSession(new("later", draft = true)) }
        assertEquals(true, (rad.nextCall("session.create")["params"] as JsonObject).getValue("draft").jsonPrimitive.boolean)
        assertEquals("later", s.draft?.text)
        assertEquals("later", c.sessions.value.getValue("s2").draft?.text)

        // An older rad would start the session with it, so it's left out.
        withContext(dispatcher) { c.createSession(new("now", draft = null)) }
        assertFalse("draft" in (rad.nextCall("session.create")["params"] as JsonObject))
        withContext(dispatcher) { c.stop() }
    }

    @Test
    fun forkSendsOnlyWhatWasChosen() = runBlocking {
        rad.answer = ::answerDrafts
        server.upgrade(rad)
        val c = connect()
        withTimeout(5.seconds) { c.indexSynced.first { it } }

        // Nothing chosen: rad takes the source's turn, agent, options and workspace.
        val s = withContext(dispatcher) { c.fork(ForkSession(commandId = "c1", sessionId = "s1")) }
        assertEquals(params("commandId" to "c1", "sessionId" to "s1"), rad.nextCall("session.fork")["params"])
        // In the lists at once, without waiting for the event.
        assertEquals("s3", s.id)
        assertTrue("s3" in c.sessions.value)

        withContext(dispatcher) {
            c.fork(
                ForkSession(
                    commandId = "c2", sessionId = "s1", turn = 2, harness = "fake", mode = "ask",
                    workspace = NewSession.WorkspaceParams(Workspace.Kind.WORKTREE, branch = "try-again"), prompt = "again",
                ),
            )
        }
        assertEquals(
            params(
                "commandId" to "c2", "sessionId" to "s1", "turn" to 2, "harness" to "fake", "mode" to "ask",
                "workspace" to params("kind" to "worktree", "branch" to "try-again"), "prompt" to "again",
            ),
            rad.nextCall("session.fork")["params"],
        )
        withContext(dispatcher) { c.stop() }
    }

    @Test
    fun importListsTheAgentsSessionsAndPicksOneUp() = runBlocking {
        rad.answer = { r, method, params ->
            when (method) {
                "session.importable" -> buildJsonObject {
                    put(
                        "sessions",
                        JsonArray(
                            listOf(
                                buildJsonObject {
                                    put("id", "n2")
                                    put("title", "Add a dark theme")
                                    put("messages", 6)
                                    put("updatedAt", "2026-10-09T12:00:30Z")
                                    put("active", true)
                                },
                                buildJsonObject {
                                    put("id", "n1")
                                    put("title", "Fix the login redirect")
                                    put("messages", 4)
                                    put("updatedAt", "2026-10-09T10:00:00Z")
                                },
                            ),
                        ),
                    )
                }
                "session.import" -> session("s4")
                else -> answerDrafts(r, method, params)
            }
        }
        server.upgrade(rad)
        val c = connect()
        withTimeout(5.seconds) { c.indexSynced.first { it } }

        val list = withContext(dispatcher) { c.importable("p1", "fake") }
        assertEquals(params("projectId" to "p1", "harness" to "fake"), rad.nextCall("session.importable")["params"])
        assertEquals(listOf("n2", "n1"), list.map { it.id })
        assertEquals(listOf(true, false), list.map { it.active })
        assertEquals(listOf(6, 4), list.map { it.messages })

        // Nothing chosen: rad takes the agent's defaults.
        val s = withContext(dispatcher) { c.importSession(ImportSession(commandId = "c1", projectId = "p1", harness = "fake", nativeId = "n1")) }
        assertEquals(
            params("commandId" to "c1", "projectId" to "p1", "harness" to "fake", "nativeId" to "n1"),
            rad.nextCall("session.import")["params"],
        )
        // In the lists at once, without waiting for the event.
        assertEquals("s4", s.id)
        assertTrue("s4" in c.sessions.value)

        // The form's options, and a commandId of its own each time.
        withContext(dispatcher) {
            c.importSession(ImportSession(projectId = "p1", harness = "fake", nativeId = "n2", model = "m", effort = "high", mode = "ask"))
        }
        val sent = rad.nextCall("session.import")["params"] as JsonObject
        assertEquals(
            params("projectId" to "p1", "harness" to "fake", "nativeId" to "n2", "model" to "m", "effort" to "high", "mode" to "ask"),
            JsonObject(sent - "commandId"),
        )
        assertTrue(sent.getValue("commandId").jsonPrimitive.content.let { it.isNotEmpty() && it != "c1" })
        withContext(dispatcher) { c.stop() }
    }

    @Test
    fun importSentOnADeadSocketIsDeliveredAfterReconnecting() = runBlocking {
        next.answer = { _, method, _ ->
            when (method) {
                "server.info" -> FakeRad.serverInfo
                "session.import" -> session("s4")
                else -> JsonObject(emptyMap())
            }
        }
        server.upgrade(rad)
        server.upgrade(next)
        val c = connect(base = relay.value.base, pingInterval = 100.milliseconds)
        withTimeout(5.seconds) { c.state.first { it == ConnectionState.Connected } }

        relay.value.quiet()
        val s = withContext(dispatcher) { c.importSession(ImportSession(projectId = "p1", harness = "fake", nativeId = "n1")) }

        // Sent again on the new socket, with the same commandId, so rad picks it up once.
        assertEquals(2, relay.value.connectionCount)
        assertEquals(commandId(rad.nextCall("session.import")), commandId(next.nextCall("session.import")))
        assertEquals("s4", s.id)
        assertTrue("s4" in c.sessions.value)
        withContext(dispatcher) { c.stop() }
    }

    @Test
    fun commitSendsWhatWasChosenAndListsTheSessionsTasks() = runBlocking {
        val task = buildJsonObject {
            put("id", "k1")
            put("kind", "commit")
            put("sessionId", "s1")
            put("status", "running")
            put("output", "")
            put("startedAt", "2026-10-09T12:00:00Z")
        }
        rad.answer = { r, method, params ->
            when (method) {
                "git.commitPreview" -> buildJsonObject {
                    put("branch", "ra/x")
                    put("files", JsonArray(emptyList()))
                    put("message", "")
                    put("ahead", 2)
                }
                "git.commit" -> task
                "task.list" -> buildJsonObject { put("tasks", JsonArray(if ("sessionId" in params) listOf(task) else emptyList())) }
                else -> answerDrafts(r, method, params)
            }
        }
        server.upgrade(rad)
        val c = connect()
        withTimeout(5.seconds) { c.indexSynced.first { it } }

        val preview = withContext(dispatcher) { c.commitPreview("s1") }
        assertEquals(params("sessionId" to "s1"), rad.nextCall("git.commitPreview")["params"])
        assertEquals("ra/x", preview.branch)
        assertEquals(2, preview.ahead)
        assertNull(preview.remote)

        // A message as typed, trimmed; a pull request pushes too.
        val t = withContext(dispatcher) { c.commit("s1", "  Fix it\n\nBecause.\n", push = false, pullRequest = true) }
        assertEquals(
            params("sessionId" to "s1", "message" to "Fix it\n\nBecause.", "push" to true, "pullRequest" to true),
            rad.nextCall("git.commit")["params"],
        )
        assertEquals("commit", t.kind)
        assertEquals("s1", t.sessionId)
        // No message: rad writes one. Commit only: nothing more is said.
        withContext(dispatcher) { c.commit("s1", " \n", push = false, pullRequest = false) }
        assertEquals(params("sessionId" to "s1"), rad.nextCall("git.commit")["params"])
        withContext(dispatcher) { c.commit("s1", "x", push = true, pullRequest = false) }
        assertEquals(params("sessionId" to "s1", "message" to "x", "push" to true), rad.nextCall("git.commit")["params"])

        // The session's tasks, apart from the server's own.
        assertEquals(listOf("k1"), withContext(dispatcher) { c.tasks("s1") }.map { it.id })
        assertEquals(params("sessionId" to "s1"), rad.nextCall("task.list")["params"])
        assertEquals(emptyList(), withContext(dispatcher) { c.tasks() })
        withContext(dispatcher) { c.stop() }
    }

    @Test
    fun renameSendsTheTitleAsTypedAndTheEventRetitlesTheSession() = runBlocking {
        var seq = 2L
        rad.answer = { r, method, params ->
            when (method) {
                "server.info" -> FakeRad.serverInfo
                "subscribe" -> {
                    r.notify("events", events("index", 1L to upserted("project", project("p1")), 2L to upserted("session", session("s1"))))
                    r.notify("synchronized", synchronized(2, reset = true))
                    subscribed(2)
                }
                "session.rename" -> {
                    // As rad: the session keeps its updatedAt, and its event brings the title.
                    val title = params.getValue("title").jsonPrimitive.content
                    val s = session("s1", title = title.ifEmpty { "Session s1" }, customTitle = title.isNotEmpty())
                    seq += 1
                    r.notify("events", events("index", seq to upserted("session", s)))
                    s
                }
                else -> JsonObject(emptyMap())
            }
        }
        server.upgrade(rad)
        val c = connect()
        withTimeout(5.seconds) { c.indexSynced.first { it } }

        val s = withContext(dispatcher) { c.rename("s1", "Ship the release") }
        assertEquals(params("sessionId" to "s1", "title" to "Ship the release"), rad.nextCall("session.rename")["params"])
        assertEquals("Ship the release", s.title)
        assertTrue(s.customTitle)
        withTimeout(5.seconds) { c.sessions.first { it["s1"]?.title == "Ship the release" } }
        assertTrue(c.sessions.value.getValue("s1").customTitle)

        // Empty gives it back to rad's naming, so it goes as it is.
        val reset = withContext(dispatcher) { c.rename("s1", "") }
        assertEquals(params("sessionId" to "s1", "title" to ""), rad.nextCall("session.rename")["params"])
        assertFalse(reset.customTitle)
        withTimeout(5.seconds) { c.sessions.first { it["s1"]?.title == "Session s1" } }
        assertFalse(c.sessions.value.getValue("s1").customTitle)
        withContext(dispatcher) { c.stop() }
    }

    private fun removed(id: String) = buildJsonObject {
        put("type", "session.removed")
        put("id", id)
        put("ts", "2026-10-09T12:00:00Z")
    }

    @Test
    fun markReadClearsTheMarkAtOnceAndTellsTheServerOnce() = runBlocking {
        rad.answer = { r, method, _ ->
            when (method) {
                "server.info" -> FakeRad.serverInfo
                "subscribe" -> {
                    r.notify(
                        "events",
                        events(
                            "index", 1L to upserted("project", project("p1")),
                            2L to upserted("session", session("s1", unread = true)), 3L to upserted("session", session("s2")),
                        ),
                    )
                    r.notify("synchronized", synchronized(3, reset = true))
                    subscribed(3)
                }
                // As an older server, which the client ignores.
                "session.markRead" -> FakeRad.error("method_not_found", "unknown method")
                else -> JsonObject(emptyMap())
            }
        }
        server.upgrade(rad)
        val c = connect()
        withTimeout(5.seconds) { c.indexSynced.first { it } }
        assertTrue(c.sessions.value.getValue("s1").unread)
        assertFalse(c.sessions.value.getValue("s2").unread)

        // The row loses its mark before the server answers.
        withContext(dispatcher) {
            c.markRead("s1")
            assertFalse(c.sessions.value.getValue("s1").unread)
        }
        assertEquals(params("sessionId" to "s1"), rad.nextCall("session.markRead")["params"])

        // Read already, so there is nothing to tell the server.
        withContext(dispatcher) {
            c.markRead("s1")
            c.markRead("s2")
            c.markRead("gone")
        }
        delay(200)
        assertTrue(rad.received.none { it.getValue("method").jsonPrimitive.content == "session.markRead" })
        assertFalse(c.sessions.value.getValue("s1").unread)
        withContext(dispatcher) { c.stop() }
    }

    @Test
    fun deletedSessionLeavesTheIndexAndItsStore() = runBlocking {
        rad.answer = { r, method, params ->
            when (method) {
                "server.info" -> FakeRad.serverInfo
                "subscribe" -> {
                    val stream = params.getValue("stream").jsonPrimitive.content
                    if (stream == "index") {
                        r.notify("events", events("index", 1L to upserted("project", project("p1")), 2L to upserted("session", session("s1"))))
                        r.notify("synchronized", synchronized(2, reset = true))
                        subscribed(2)
                    } else {
                        r.notify("events", events(stream, 1L to upserted("session", session("s1"))))
                        r.notify("synchronized", buildJsonObject {
                            put("stream", stream)
                            put("seq", 1)
                            put("reset", true)
                        })
                        buildJsonObject {
                            put("stream", stream)
                            put("seq", 1)
                        }
                    }
                }
                "session.delete" -> {
                    // As rad: the removal goes to the index and to the session's own stream.
                    r.notify("events", events("index", 3L to removed("s1")))
                    r.notify("events", events("session:s1", 2L to removed("s1")))
                    JsonObject(emptyMap())
                }
                else -> JsonObject(emptyMap())
            }
        }
        server.upgrade(rad)
        val c = connect()
        withTimeout(5.seconds) { c.indexSynced.first { it } }
        val store = withContext(dispatcher) {
            c.sessionStore("s1").also {
                it.draft = "half a thought"
                it.activate()
            }
        }
        withTimeout(5.seconds) { store.synced.first { it } }

        withContext(dispatcher) { c.delete("s1", removeWorktree = true) }
        assertEquals(params("sessionId" to "s1", "removeWorktree" to true), rad.nextCall("session.delete")["params"])
        withTimeout(5.seconds) { store.removed.first { it } }
        withTimeout(5.seconds) { c.sessions.first { "s1" !in it } }
        assertEquals("", withContext(dispatcher) { store.draft })
        withContext(dispatcher) { c.stop() }
    }
}
