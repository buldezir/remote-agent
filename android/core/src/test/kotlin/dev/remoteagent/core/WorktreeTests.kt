package dev.remoteagent.core

import dev.remoteagent.core.protocol.Branches
import dev.remoteagent.core.protocol.WireCoding
import dev.remoteagent.core.protocol.WorktreeInfo
import dev.remoteagent.core.protocol.availableBranches
import dev.remoteagent.core.protocol.worktreeLabel
import kotlin.test.Test
import kotlin.test.assertEquals

/** What New Session's Workspace pickers offer, as web/src/protocol's tests check it. */
class WorktreeTests {
    @Test
    fun availableBranchesLeaveOutTheOnesACheckoutHas() {
        val b = Branches(
            branches = listOf("main", "ra/fix-tests", "feature/login", "hand", "spike"),
            current = "main",
            worktrees = listOf(WorktreeInfo("/wt/s1", "ra/fix-tests"), WorktreeInfo("/repo/.claude/worktrees/x"), WorktreeInfo("/wt/hand", "hand")),
        )
        // In the server's order.
        assertEquals(listOf("feature/login", "spike"), availableBranches(b))
        assertEquals(listOf("a", "b"), availableBranches(Branches(listOf("a", "b"), current = "")))
    }

    @Test
    fun worktreeLabelIsItsBranchElseItsFolder() {
        assertEquals("ra/fix-tests", worktreeLabel(WorktreeInfo("/wt/s1", "ra/fix-tests")))
        assertEquals("spike (detached)", worktreeLabel(WorktreeInfo("/repo/.claude/worktrees/spike")))
        assertEquals("spike (detached)", worktreeLabel(WorktreeInfo("/repo/.claude/worktrees/spike/")))
    }

    @Test
    fun anOlderServerListsNoWorktrees() {
        val b = WireCoding.json.decodeFromString(Branches.serializer(), """{"branches":["main","x"],"current":"main"}""")
        assertEquals(emptyList(), b.worktrees)
        assertEquals(listOf("x"), availableBranches(b))
    }
}
