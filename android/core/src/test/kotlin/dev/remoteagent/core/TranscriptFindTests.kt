package dev.remoteagent.core

import dev.remoteagent.core.net.RpcClient
import dev.remoteagent.core.net.ServerMessage
import dev.remoteagent.core.protocol.Item
import dev.remoteagent.core.protocol.ItemKind
import dev.remoteagent.core.protocol.ItemStatus
import dev.remoteagent.core.protocol.ToolCall
import dev.remoteagent.core.protocol.WireCoding
import dev.remoteagent.core.store.TranscriptFind
import java.time.Instant
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertIs

/** The vectors every client's find shares (as RAKit's and web/src/store/transcriptFind.test.ts). */
class TranscriptFindTests {
    /** The items of protocol/fixtures/notification_events_session.json, in the transcript's order. */
    private val items: List<Item> by lazy {
        val client = RpcClient("token")
        client.dispatch(fixture("notification_events_session.json"))
        val events = assertIs<ServerMessage.Events>(client.messages.tryReceive().getOrNull()).events
        events.mapNotNull { it.item }.sortedBy { it.order }
    }

    private fun find(query: String) = TranscriptFind.findMatches(items, query)

    @Test
    fun findsTheSharedVectors() {
        // In any case: the prompt, the reply and the plan.
        assertEquals(listOf("i1", "i4", "i8"), find("TESTS"))
        // A question's text.
        assertEquals(listOf("i6"), find("which"))
        // A tool call's title, the query as one string with its space.
        assertEquals(listOf("i3"), find("go test"))
        // Reasoning isn't searched.
        assertEquals(emptyList(), find("test output"))
        // Nor are a sub-agent's items.
        assertEquals(emptyList(), find("sub-agent"))
        assertEquals(emptyList(), find(""))
        assertEquals(emptyList(), find("   "))
        // The query is trimmed.
        assertEquals(listOf("i6"), find("  which "))
    }

    private fun tool(title: String?, input: String?) = Item(
        id = "t", sessionId = "s", order = 1, kind = ItemKind.TOOL_CALL, status = ItemStatus.COMPLETED,
        tool = ToolCall(name = "Bash", title = title, input = input?.let { WireCoding.json.parseToJsonElement(it) }),
        createdAt = Instant.EPOCH, updatedAt = Instant.EPOCH,
    )

    @Test
    fun aToolCallsTextIsItsTitleAndCommand() {
        // A command given as arguments is joined with spaces.
        assertEquals("Run\ngit status", TranscriptFind.searchText(tool("Run", """{"command":["git","status"]}""")))
        assertEquals("Run\nls -la", TranscriptFind.searchText(tool("Run", """{"command":"ls -la"}""")))
        // A title that has its command is enough.
        assertEquals("Run go test ./...", TranscriptFind.searchText(tool("Run go test ./...", """{"command":"go test ./..."}""")))
        // No input, or no command in it.
        assertEquals("Run", TranscriptFind.searchText(tool("Run", null)))
        assertEquals("Read", TranscriptFind.searchText(tool("Read", """{"file_path":"a.txt"}""")))
        assertEquals("Run", TranscriptFind.searchText(tool("Run", """{"command":[1,2]}""")))
        assertEquals("Run", TranscriptFind.searchText(tool("Run", """["ls"]""")))
        // As rad's: no title, the command after a line break.
        assertEquals("\nmake", TranscriptFind.searchText(tool(null, """{"command":"make"}""")))
    }
}
