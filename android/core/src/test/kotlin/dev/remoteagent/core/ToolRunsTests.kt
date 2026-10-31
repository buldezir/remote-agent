package dev.remoteagent.core

import dev.remoteagent.core.protocol.ImageRef
import dev.remoteagent.core.protocol.Item
import dev.remoteagent.core.protocol.ItemKind
import dev.remoteagent.core.protocol.ItemStatus
import dev.remoteagent.core.protocol.ToolCall
import dev.remoteagent.core.protocol.ToolKind
import dev.remoteagent.core.protocol.Turn
import dev.remoteagent.core.protocol.TurnStatus
import dev.remoteagent.core.store.ToolRuns
import java.time.Instant
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

/** The vectors every client's ToolRuns shares (as RAKit's and web/src/store/toolRuns.test.ts). */
class ToolRunsTests {
    private val now = Instant.parse("2026-10-09T12:00:00Z")

    private fun item(id: String, kind: ItemKind, turn: String? = "t1", status: ItemStatus = ItemStatus.COMPLETED) =
        Item(id = id, sessionId = "s", turnId = turn, order = 0, kind = kind, status = status, createdAt = now, updatedAt = now)

    private fun tool(id: String, kind: ToolKind, turn: String? = "t1", status: ItemStatus = ItemStatus.COMPLETED, paths: List<String>? = null) =
        item(id, ItemKind.TOOL_CALL, turn, status).copy(tool = ToolCall(name = id, kind = kind, title = id, paths = paths))

    private fun turn(id: String, status: TurnStatus) = Turn(id = id, sessionId = "s", n = 1, status = status, startedAt = now)

    /** A finished turn with a run, a failed command, a lone edit and a reply, and a running one with a run still going. */
    private val transcript = listOf(
        item("u1", ItemKind.USER_MESSAGE),
        item("r1", ItemKind.REASONING),
        tool("a", ToolKind.EXECUTE),
        tool("b", ToolKind.READ, paths = listOf("README.md")),
        item("r2", ItemKind.REASONING),
        tool("c", ToolKind.SEARCH),
        tool("d", ToolKind.READ, paths = listOf("README.md")),
        tool("e", ToolKind.EDIT, paths = listOf("main.go")),
        tool("f", ToolKind.EDIT, paths = listOf("main.go", "main_test.go")),
        tool("g", ToolKind.EXECUTE, status = ItemStatus.FAILED),
        tool("h", ToolKind.EDIT, paths = listOf("main.go")),
        item("m1", ItemKind.ASSISTANT_MESSAGE),
        item("u2", ItemKind.USER_MESSAGE, turn = "t2"),
        tool("i", ToolKind.FETCH, turn = "t2"),
        tool("j", ToolKind.THINK, turn = "t2"),
        tool("k", ToolKind.OTHER, turn = "t2", status = ItemStatus.IN_PROGRESS),
    )

    private val turns = mapOf("t1" to turn("t1", TurnStatus.COMPLETED), "t2" to turn("t2", TurnStatus.RUNNING))

    private fun ids(rows: List<ToolRuns.Row>) = rows.map { row ->
        when (row) {
            is ToolRuns.Row.ItemRow -> row.item.id
            is ToolRuns.Row.RunRow -> row.run.id + if (row.expanded) " open" else " shut"
            is ToolRuns.Row.TurnEnd -> "end " + row.turn.id
        }
    }

    @Test
    fun foldsRunsOfToolCalls() {
        val runs = ToolRuns.runs(transcript, turns)
        assertEquals(listOf("tools:r1", "tools:i"), runs.map { it.id })
        assertEquals(listOf("r1", "a", "b", "r2", "c", "d", "e", "f"), runs[0].items.map { it.id })
        assertEquals(6, runs[0].tools.size)
        assertFalse(runs[0].live)
        assertFalse(runs[0].working)
        assertEquals("Ran 1 command, edited 2 files, read 1 file, searched 1 time", runs[0].title)
        assertEquals(listOf(ToolKind.EXECUTE, ToolKind.EDIT, ToolKind.READ, ToolKind.SEARCH), runs[0].parts.map { it.kind })
        assertEquals(listOf(1, 2, 1, 1), runs[0].parts.map { it.count })
        // Its turn runs and nothing came after it.
        assertEquals(listOf("i", "j", "k"), runs[1].items.map { it.id })
        assertTrue(runs[1].live)
        assertTrue(runs[1].working)
        assertEquals("Fetched 1 page, started 1 agent, used 1 tool", runs[1].title)
    }

    @Test
    fun rowsShowARunShutUnlessLive() {
        assertEquals(listOf("u1", "tools:r1 shut", "g", "h", "m1", "u2", "tools:i open", "i", "j", "k"), ids(ToolRuns.rows(transcript, turns)))
        // The user's choice wins, either way.
        assertEquals(
            listOf("u1", "tools:r1 open", "r1", "a", "b", "r2", "c", "d", "e", "f", "g", "h", "m1", "u2", "tools:i shut"),
            ids(ToolRuns.rows(transcript, turns, mapOf("tools:r1" to true, "tools:i" to false))),
        )
        // Not grouping: every item is a row.
        assertEquals(transcript.map { it.id }, ids(ToolRuns.rows(transcript, turns, grouping = false)))
    }

    @Test
    fun aLiveRunFoldsOnceTheAgentRepliesOrTheTurnEnds() {
        val items = transcript.toMutableList()
        assertTrue(ToolRuns.runs(items, turns)[1].live)
        // A prompt queued meanwhile isn't the turn's.
        items += item("q", ItemKind.USER_MESSAGE, turn = null, status = ItemStatus.QUEUED)
        assertTrue(ToolRuns.runs(items, turns)[1].live)
        items += item("m2", ItemKind.ASSISTANT_MESSAGE, turn = "t2", status = ItemStatus.IN_PROGRESS)
        assertFalse(ToolRuns.runs(items, turns)[1].live)
        assertEquals(listOf("u2", "tools:i shut", "q", "m2"), ids(ToolRuns.rows(items, turns)).takeLast(4))
        val ended = turns + ("t2" to turn("t2", TurnStatus.COMPLETED))
        assertFalse(ToolRuns.runs(transcript, ended)[1].live)
    }

    @Test
    fun aPromptQueuedMidRunWaitsAfterIt() {
        // Sent while the agent works, before it calls more tools: the run
        // goes on past it, live, and the prompt shows after it.
        val items = transcript.toMutableList()
        items.add(items.size - 1, item("q", ItemKind.USER_MESSAGE, turn = null, status = ItemStatus.QUEUED))
        val runs = ToolRuns.runs(items, turns)
        assertEquals(listOf("i", "j", "k"), runs[1].items.map { it.id })
        assertTrue(runs[1].live)
        assertEquals(listOf("u2", "tools:i open", "i", "j", "k", "q"), ids(ToolRuns.rows(items, turns)).takeLast(6))
        assertEquals(listOf("u2", "tools:i shut", "q"), ids(ToolRuns.rows(items, turns, expanded = mapOf("tools:i" to false))).takeLast(3))
        // After one call, which doesn't fold, it stays where it is.
        val queued = item("q", ItemKind.USER_MESSAGE, turn = null, status = ItemStatus.QUEUED)
        assertEquals(listOf("a", "q", "m"), ids(ToolRuns.rows(listOf(tool("a", ToolKind.EXECUTE), queued, item("m", ItemKind.ASSISTANT_MESSAGE)), turns)))
        assertEquals(listOf("tools:a shut", "q"), ids(ToolRuns.rows(listOf(tool("a", ToolKind.EXECUTE), queued, tool("b", ToolKind.READ)), turns)))
        // Steered into the turn, it's conversation: the run ends before it.
        items[items.size - 2] = item("q", ItemKind.USER_MESSAGE, turn = "t2")
        assertEquals(listOf("u2", "tools:i shut", "q", "k"), ids(ToolRuns.rows(items, turns)).takeLast(4))
    }

    @Test
    fun callsThatNeedAttentionStandAlone() {
        val shot = tool("s", ToolKind.OTHER).copy(images = listOf(ImageRef(id = "img", mimeType = "image/png", size = 1)))
        val items = listOf(
            tool("a", ToolKind.EXECUTE), tool("b", ToolKind.EDIT, status = ItemStatus.PENDING), tool("c", ToolKind.EXECUTE),
            shot, tool("d", ToolKind.READ), item("r", ItemKind.REASONING), tool("e", ToolKind.READ),
        )
        // Pending and with images, a call breaks the run; one call alone
        // doesn't fold, and reasoning doesn't count.
        assertEquals(listOf("a", "b", "c", "s", "tools:d shut"), ids(ToolRuns.rows(items, turns)))
        assertEquals("Read 2 files", ToolRuns.runs(items, turns)[0].title)
        // Nor does reasoning on its own.
        assertEquals(emptyList(), ToolRuns.runs(listOf(item("r1", ItemKind.REASONING), item("r2", ItemKind.REASONING), tool("x", ToolKind.READ)), turns))
    }

    @Test
    fun runsStayWithinATurn() {
        val items = listOf(tool("a", ToolKind.EXECUTE), tool("b", ToolKind.EXECUTE, turn = "t2"), tool("c", ToolKind.EXECUTE, turn = "t2"))
        assertEquals(listOf("a", "tools:b open", "b", "c"), ids(ToolRuns.rows(items, turns)))
    }

    @Test
    fun aTurnThatEndedBadlyEndsAfterItsRun() {
        val items = listOf(item("u", ItemKind.USER_MESSAGE), tool("a", ToolKind.EXECUTE), tool("b", ToolKind.READ))
        val failed = mapOf("t1" to turn("t1", TurnStatus.FAILED))
        assertEquals(listOf("u", "tools:a shut", "end t1"), ids(ToolRuns.rows(items, failed)))
        assertEquals(listOf("u", "tools:a open", "a", "b", "end t1"), ids(ToolRuns.rows(items, failed, mapOf("tools:a" to true))))
        assertEquals(listOf("u", "a", "b", "end t1"), ids(ToolRuns.rows(items, mapOf("t1" to turn("t1", TurnStatus.INTERRUPTED)), grouping = false)))
    }

    @Test
    fun findsTheRunAnItemIsIn() {
        assertEquals("tools:r1", ToolRuns.runContaining("c", transcript, turns)?.id)
        assertEquals("tools:r1", ToolRuns.runContaining("r2", transcript, turns)?.id)
        assertNull(ToolRuns.runContaining("g", transcript, turns))
        assertNull(ToolRuns.runContaining("m1", transcript, turns))
    }

    @Test
    fun countsFilesByPathAndTheRestByCall() {
        val items = listOf(
            tool("a", ToolKind.READ), tool("b", ToolKind.READ), tool("c", ToolKind.EDIT, paths = listOf("x")), tool("d", ToolKind.EDIT, paths = listOf("x")),
            tool("e", ToolKind.SEARCH), tool("f", ToolKind.SEARCH), tool("g", ToolKind.FETCH), tool("h", ToolKind.FETCH),
            tool("i", ToolKind.THINK), tool("j", ToolKind.THINK), tool("k", ToolKind.OTHER), tool("l", ToolKind.OTHER),
            tool("m", ToolKind.EXECUTE), tool("n", ToolKind.EXECUTE),
        )
        assertEquals(
            "Ran 2 commands, edited 1 file, read 2 files, searched 2 times, fetched 2 pages, started 2 agents, used 2 tools",
            ToolRuns.title(ToolRuns.parts(items)),
        )
        assertEquals("", ToolRuns.title(emptyList()))
    }
}
