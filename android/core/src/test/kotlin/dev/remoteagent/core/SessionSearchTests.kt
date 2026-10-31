package dev.remoteagent.core

import dev.remoteagent.core.FakeRad.Companion.session
import dev.remoteagent.core.net.params
import dev.remoteagent.core.store.ConnectionState
import dev.remoteagent.core.store.ImageCache
import dev.remoteagent.core.store.SavedServer
import dev.remoteagent.core.store.ServerConnection
import dev.remoteagent.core.store.SessionSearch
import dev.remoteagent.core.store.mergeSearchResults
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import mockwebserver3.MockWebServer
import java.time.Instant
import java.util.concurrent.Executors
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue
import kotlin.time.Duration
import kotlin.time.Duration.Companion.milliseconds
import kotlin.time.Duration.Companion.seconds

/** Search across servers, against fake rads (as RAKit's SessionSearchTests and web/src/store/sessionSearch.test.ts). */
class SessionSearchTests {
    private val mocks = mutableListOf<MockWebServer>()
    private val rads = mutableListOf<FakeRad>()
    // The app changes the search's state on the main thread; here, on this one.
    private val dispatcher = Executors.newSingleThreadExecutor().asCoroutineDispatcher()
    private val scope = CoroutineScope(SupervisorJob() + dispatcher)

    @AfterTest
    fun tearDown() {
        rads.forEach { it.shutDown() }
        scope.cancel()
        dispatcher.close()
        mocks.forEach { it.close() }
    }

    private fun info(id: String, name: String, search: Boolean) = buildJsonObject {
        put("serverId", id)
        put("name", name)
        put("protocolVersion", 1)
        put("version", "0.1")
        if (search) put("search", true)
    }

    /** What a server found: sessions by id, each with an item. */
    private fun found(vararg ids: String, more: Boolean = false, indexing: Boolean = false) = buildJsonObject {
        put("results", JsonArray(ids.map { id ->
            buildJsonObject {
                put("session", session(id))
                put("itemId", "$id-item")
                put("itemKind", "assistant_message")
                put("snippet", JsonArray(listOf(buildJsonObject { put("text", "the "); }, buildJsonObject { put("text", "match"); put("match", true) })))
                put("matches", 1)
            }
        }))
        if (more) put("more", true)
        if (indexing) put("indexing", true)
    }

    /** A connected server with a fake rad; `answer` gets session.search's params (null: no answer). */
    private suspend fun server(
        id: String, search: Boolean = true, answer: (JsonObject) -> JsonElement? = { found() },
    ): Pair<ServerConnection, FakeRad> {
        val mock = MockWebServer().apply { start() }
        mocks += mock
        val rad = FakeRad()
        rads += rad
        rad.answer = { _, method, params ->
            when (method) {
                "server.info" -> info(id, "Server $id", search)
                "session.search" -> answer(params)
                else -> JsonObject(emptyMap())
            }
        }
        mock.upgrade(rad)
        val c = ServerConnection(SavedServer(id, "Server $id", listOf(mock.base()), "dev1", Instant.now()), "tok", scope, ImageCache(null), {})
        withContext(dispatcher) { c.start() }
        withTimeout(5.seconds) { c.state.first { it == ConnectionState.Connected } }
        return c to rad
    }

    private fun search(vararg connections: ServerConnection, debounce: Duration = 10.milliseconds) =
        SessionSearch(scope, debounce) { connections.toList() }

    /** Polls until `condition` holds, or fails after 10 s. */
    private suspend fun waitUntil(condition: () -> Boolean) = withTimeout(10.seconds) {
        while (!withContext(dispatcher) { condition() }) delay(5)
    }

    private suspend fun SessionSearch.type(query: String) = withContext(dispatcher) { setQuery(query) }

    private fun SessionSearch.hitIds() = hits.value.map { it.id }

    @Test
    fun mergesRoundRobinInTheServersOrder() {
        assertEquals(listOf("a1", "b1", "c1", "a2", "c2", "a3"), mergeSearchResults(listOf(listOf("a1", "a2", "a3"), listOf("b1"), listOf("c1", "c2"))))
        assertEquals(listOf("b1", "b2"), mergeSearchResults(listOf(emptyList(), listOf("b1", "b2"))))
        assertEquals(emptyList(), mergeSearchResults(emptyList<List<String>>()))
    }

    @Test
    fun searchesOnceTheQueryIsLongEnoughAndSettles() = runBlocking {
        val (a, rad) = server("a") { found("s1") }
        val s = search(a, debounce = 150.milliseconds)

        // One character isn't searched.
        s.type("t")
        delay(300)
        assertFalse(s.active.value)
        assertFalse(s.isSearching.value)
        assertTrue(rad.received.none { it.getValue("method").jsonPrimitive.content == "session.search" })

        // Typing on waits for the last change; archived sessions are searched unless turned off.
        s.type("te")
        assertTrue(s.active.value)
        assertTrue(s.isSearching.value)
        s.type("tests")
        waitUntil { !s.isSearching.value }
        assertEquals(params("query" to "tests", "archived" to true, "limit" to 30), rad.nextCall("session.search")["params"])
        assertTrue(rad.received.none { it.getValue("method").jsonPrimitive.content == "session.search" })
        assertEquals(listOf("a/s1"), s.hitIds())
        assertEquals("s1-item", s.hits.value[0].result.itemId)

        withContext(dispatcher) { s.setIncludeArchived(false) }
        waitUntil { !s.isSearching.value && rad.received.isNotEmpty() }
        assertEquals(params("query" to "tests", "limit" to 30), rad.nextCall("session.search")["params"])

        // Shorter again: the results go, without asking.
        s.type(" t ")
        assertFalse(s.active.value)
        assertEquals(emptyList(), s.hits.value)
        withContext(dispatcher) { a.stop() }
    }

    @Test
    fun dropsAnAnswerToAnOlderQuery() = runBlocking {
        // The first query's answer comes only after the second's.
        val (a, rad) = server("a") { p -> if (p.getValue("query").jsonPrimitive.content == "old") null else found("s2") }
        val s = search(a)
        s.type("old")
        val old = rad.nextCall("session.search")
        s.type("new")
        waitUntil { !s.isSearching.value }
        assertEquals(listOf("a/s2"), s.hitIds())

        rad.socket!!.send(buildJsonObject {
            put("id", old.getValue("id"))
            put("result", found("s1"))
        }.toString())
        delay(200)
        assertEquals(listOf("a/s2"), s.hitIds())
        withContext(dispatcher) { a.stop() }
    }

    @Test
    fun mergesTheServersAnswersAndReportsTheOnesThatFailOrCant() = runBlocking {
        val (a, _) = server("a") { found("a1", "a2", "a3", indexing = true) }
        val (b, _) = server("b") { found("b1", more = true) }
        val (c, _) = server("c") { FakeRad.error("internal", "the index is locked") }
        val (old, oldRad) = server("old", search = false)
        val s = search(a, b, c, old)
        s.type("tests")
        waitUntil { !s.isSearching.value }

        // Every server's first, then the seconds…
        assertEquals(listOf("a/a1", "b/b1", "a/a2", "a/a3"), s.hitIds())
        assertEquals(listOf(SessionSearch.Failure("c", "Server c", "the index is locked")), s.failures.value)
        assertTrue(s.more.value)
        assertEquals(listOf("Server a"), s.indexing.value)
        // A rad without search isn't asked: it needs an update.
        assertEquals(listOf("Server old"), s.unsupported.value)
        assertTrue(oldRad.received.none { it.getValue("method").jsonPrimitive.content == "session.search" })
        assertEquals(2, s.answered.value)
        assertFalse(s.noServers.value)
        withContext(dispatcher) { listOf(a, b, c, old).forEach { it.stop() } }
    }

    @Test
    fun saysWhenNoServerCouldBeSearched() = runBlocking {
        // None connected: no answer, so not "no sessions match".
        val s = search()
        s.setQuery("tests")
        waitUntil { !s.isSearching.value }
        assertTrue(s.noServers.value)
        assertEquals(0, s.answered.value)
        assertTrue(s.hits.value.isEmpty())
        s.setQuery("")
        assertFalse(s.noServers.value)
    }

    @Test
    fun showsTheAnswersSoFarWhileAServerIsSlow() = runBlocking {
        val (a, _) = server("a") { found("a1") }
        val (slow, _) = server("slow") { null }
        val s = search(slow, a)
        s.type("tests")
        waitUntil { s.hits.value.isNotEmpty() }
        assertEquals(listOf("a/a1"), s.hitIds())
        assertTrue(s.isSearching.value)
        withContext(dispatcher) {
            a.stop()
            slow.stop()
        }
    }
}
