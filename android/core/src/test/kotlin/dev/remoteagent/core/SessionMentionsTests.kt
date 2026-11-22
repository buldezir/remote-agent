package dev.remoteagent.core

import dev.remoteagent.core.protocol.Item
import dev.remoteagent.core.protocol.ItemKind
import dev.remoteagent.core.protocol.ItemStatus
import dev.remoteagent.core.protocol.WireCoding
import dev.remoteagent.core.protocol.completeMention
import dev.remoteagent.core.protocol.mentionDisplay
import dev.remoteagent.core.protocol.mentionQuery
import dev.remoteagent.core.protocol.mentionSuggestions
import dev.remoteagent.core.protocol.mentionText
import dev.remoteagent.core.protocol.mentions
import dev.remoteagent.core.store.TranscriptFind
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import java.time.Instant
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNotNull
import kotlin.test.assertTrue

/**
 * The vectors every client's @ session mentions share
 * (protocol/vectors/session_mentions.json), as gpui's, RAKit's and the
 * web's tests check them.
 */
class SessionMentionsTests {
    @Serializable
    private data class Vectors(
        @SerialName("self") val selfId: String,
        val sessions: List<S>,
        val query: List<QueryCase>,
        val suggestions: List<SuggestCase>,
        val complete: List<CompleteCase>,
        val mentionText: List<TextCase>,
        val display: List<DisplayCase>,
    )

    @Serializable
    private data class S(val id: String, val title: String, val updatedAt: String, val archived: Boolean = false)

    @Serializable
    private data class QueryCase(val text: String, val query: String?)

    @Serializable
    private data class SuggestCase(val query: String, val ids: List<String>)

    @Serializable
    private data class CompleteCase(val text: String, val id: String, val result: String)

    @Serializable
    private data class TextCase(val title: String, val id: String, val text: String)

    @Serializable
    private data class DisplayCase(val text: String, val display: String)

    private val v = WireCoding.json.decodeFromString(Vectors.serializer(), vector("session_mentions.json"))

    /** The text without its `|`, and the cursor where it was. */
    private fun cursor(marked: String): Pair<String, Int> {
        val at = marked.indexOf('|')
        return marked.removeRange(at, at + 1) to at
    }

    @Test
    fun queries() {
        assertTrue(v.query.isNotEmpty())
        for (c in v.query) {
            val (text, at) = cursor(c.text)
            assertEquals(c.query, mentionQuery(text, at)?.query, c.text)
        }
    }

    @Test
    fun suggestions() {
        assertTrue(v.suggestions.isNotEmpty())
        for (c in v.suggestions) {
            val ids = mentionSuggestions(c.query, v.sessions, v.selfId, { it.id }, { it.title }, { Instant.parse(it.updatedAt) })
                .map { it.id }
            assertEquals(c.ids, ids, c.query)
        }
    }

    @Test
    fun completions() {
        assertTrue(v.complete.isNotEmpty())
        for (c in v.complete) {
            val (text, at) = cursor(c.text)
            val q = assertNotNull(mentionQuery(text, at), c.text)
            val s = v.sessions.first { it.id == c.id }
            val done = completeMention(text, at, q, s.title, s.id)
            assertEquals(c.result, done.text.substring(0, done.cursor) + "|" + done.text.substring(done.cursor), c.text)
        }
    }

    @Test
    fun mentionTexts() {
        assertTrue(v.mentionText.isNotEmpty())
        for (c in v.mentionText) assertEquals(c.text, mentionText(c.title, c.id))
    }

    @Test
    fun displays() {
        assertTrue(v.display.isNotEmpty())
        for (c in v.display) assertEquals(c.display, mentionDisplay(c.text), c.text)
    }

    @Test
    fun findsMentions() {
        val text = "a @[Fix](session:s-1) b @[x](other:1) @[y](session:) @[Ünï](session:s-2)"
        val m = mentions(text)
        assertEquals(2, m.size)
        assertEquals("@[Fix](session:s-1)", text.substring(m[0].range))
        assertEquals("Ünï" to "s-2", m[1].title to m[1].id)
    }

    @Test
    fun findSearchesAPromptsMentionsAsShown() {
        val item = Item(
            id = "u", sessionId = "s", order = 1, kind = ItemKind.USER_MESSAGE, status = ItemStatus.COMPLETED,
            text = "see @[Fix the login bug](session:s-login)", createdAt = Instant.EPOCH, updatedAt = Instant.EPOCH,
        )
        assertEquals("see @Fix the login bug", TranscriptFind.searchText(item))
    }
}
