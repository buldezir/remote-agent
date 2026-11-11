package dev.remoteagent.app

import dev.remoteagent.app.ui.NEW_WORKTREE
import dev.remoteagent.app.ui.startingHarness
import dev.remoteagent.app.ui.workspaceParams
import dev.remoteagent.core.protocol.AgentSpec
import dev.remoteagent.core.protocol.HarnessInfo
import dev.remoteagent.core.protocol.Project
import dev.remoteagent.core.protocol.Workspace
import dev.remoteagent.core.store.NewSession.WorkspaceParams
import java.time.Instant
import kotlin.test.Test
import kotlin.test.assertEquals

/** The agent New Session starts with, as the web client's tests have it. */
class NewSessionTests {
    private fun harness(id: String, authOk: Boolean = true) = HarnessInfo(id = id, name = id, protocol = "acp", installed = true, authOk = authOk)

    @Test
    fun startsWithTheServersDefaultElseTheLastUsed() {
        val list = listOf(harness("claude"), harness("codex"), harness("pi", authOk = false), harness("fake"))
        // The default, while it's signed in.
        assertEquals("codex", startingHarness(list, AgentSpec("codex"), "fake"))
        assertEquals("fake", startingHarness(list, AgentSpec("pi"), "fake"))
        // Not installed: the last used.
        assertEquals("fake", startingHarness(list, AgentSpec("opencode"), "fake"))
        // Without a default, the last used; Claude Code before any.
        assertEquals("fake", startingHarness(list, null, "fake"))
        assertEquals("claude", startingHarness(list, null, null))
        // Neither usable: the first that is.
        assertEquals("claude", startingHarness(list, null, "pi"))
        assertEquals("codex", startingHarness(list.drop(1), null, null))
        assertEquals("", startingHarness(listOf(harness("pi", authOk = false)), AgentSpec("pi"), "pi"))
        assertEquals("", startingHarness(emptyList(), null, null))
        // The agents haven't come.
        assertEquals("", startingHarness(null, AgentSpec("codex"), "fake"))
    }

    @Test
    fun workspaceFollowsTheWorkInAndBranchPickers() {
        val git = Project("p1", "/repo", "repo", isGitRepo = true, createdAt = Instant.EPOCH)
        val root = WorkspaceParams(Workspace.Kind.ROOT)
        assertEquals(root, workspaceParams(git, "", "", "named", "main"))
        // A new worktree on a new branch, named by rad unless named here.
        assertEquals(WorkspaceParams(Workspace.Kind.WORKTREE), workspaceParams(git, NEW_WORKTREE, "", "", ""))
        assertEquals(
            WorkspaceParams(Workspace.Kind.WORKTREE, branch = "fix", baseRef = "main"),
            workspaceParams(git, NEW_WORKTREE, "", "fix", "main"),
        )
        // On a branch that exists: no name or base.
        assertEquals(WorkspaceParams(Workspace.Kind.WORKTREE, branch = "feature/login"), workspaceParams(git, NEW_WORKTREE, "feature/login", "fix", "main"))
        // An existing worktree goes by its path alone.
        assertEquals(WorkspaceParams(Workspace.Kind.WORKTREE, path = "/wt/hand"), workspaceParams(git, "/wt/hand", "feature/login", "fix", "main"))
        // Outside git, always the folder.
        assertEquals(root, workspaceParams(git.copy(isGitRepo = false), NEW_WORKTREE, "", "", ""))
    }
}
