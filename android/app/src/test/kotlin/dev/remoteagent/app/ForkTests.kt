package dev.remoteagent.app

import dev.remoteagent.app.ui.AgentChoices
import dev.remoteagent.app.ui.ForkPoint
import dev.remoteagent.app.ui.forkPoints
import dev.remoteagent.core.protocol.Choice
import dev.remoteagent.core.protocol.HarnessCaps
import dev.remoteagent.core.protocol.HarnessInfo
import dev.remoteagent.core.protocol.Item
import dev.remoteagent.core.protocol.ItemKind
import dev.remoteagent.core.protocol.ItemStatus
import dev.remoteagent.core.protocol.Turn
import dev.remoteagent.core.protocol.TurnStatus
import java.time.Instant
import kotlin.test.Test
import kotlin.test.assertEquals

/** Fork Session's From picker and the options it starts at. */
class ForkTests {
    private val at = Instant.parse("2026-10-09T12:00:00Z")

    private fun turn(n: Int, status: TurnStatus = TurnStatus.COMPLETED) = Turn("t$n", "s1", n, status, startedAt = at)

    private fun item(order: Long, turn: Int?, kind: ItemKind, text: String?) =
        Item("i$order", "s1", turn?.let { "t$it" }, order = order, kind = kind, status = ItemStatus.COMPLETED, text = text, createdAt = at, updatedAt = at)

    @Test
    fun endedTurnsNewestFirstWithTheirPrompts() {
        val turns = listOf(turn(1), turn(2, TurnStatus.FAILED), turn(3), turn(4, TurnStatus.RUNNING))
        val transcript = listOf(
            item(1, 1, ItemKind.USER_MESSAGE, "\n  Fix the tests  \nthen the docs"),
            item(2, 1, ItemKind.ASSISTANT_MESSAGE, "Done."),
            // Images only: no text to show.
            item(3, 2, ItemKind.USER_MESSAGE, null),
            item(4, 3, ItemKind.USER_MESSAGE, "Rename every handler in the server's API package to match the protocol"),
            // A follow-up queued during the turn isn't its prompt.
            item(5, 3, ItemKind.USER_MESSAGE, "and the tests"),
            item(6, 4, ItemKind.USER_MESSAGE, "still running"),
        )
        assertEquals(
            listOf(
                ForkPoint(3, "Turn 3 · Rename every handler in the server's API packag…"),
                ForkPoint(2, "Turn 2"),
                ForkPoint(1, "Turn 1 · Fix the tests"),
            ),
            forkPoints(turns, transcript),
        )
        assertEquals(emptyList(), forkPoints(listOf(turn(1, TurnStatus.RUNNING)), transcript))
    }

    @Test
    fun choicesKeepWhatTheAgentStillOffers() {
        val h = HarnessInfo(
            id = "claude", name = "Claude Code", protocol = "stream-json", installed = true, authOk = true,
            models = listOf(Choice("opus", "Opus", efforts = listOf(Choice("high", "High")))),
            efforts = listOf(Choice("low", "Low")),
            modes = listOf(Choice("default", "Ask"), Choice("plan", "Plan")), defaultMode = "default",
            caps = HarnessCaps(freeModel = true),
        )
        assertEquals(AgentChoices("opus", "", "high", "plan"), AgentChoices.of(h, "opus", "high", "plan"))
        // A model it doesn't list is typed in; an effort the model hasn't, and a mode it hasn't, go back to the defaults.
        assertEquals(AgentChoices("__custom", "sonnet-9", "low", "default"), AgentChoices.of(h, "sonnet-9", "low", "yolo"))
        assertEquals(AgentChoices(mode = "default"), AgentChoices.of(h, null, "high", null))
        assertEquals(AgentChoices(mode = "default"), AgentChoices.of(h.copy(caps = HarnessCaps()), "sonnet-9", null, null))
    }
}
