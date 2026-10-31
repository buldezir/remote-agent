package dev.remoteagent.app

import dev.remoteagent.app.ui.startingHarness
import dev.remoteagent.core.protocol.AgentSpec
import dev.remoteagent.core.protocol.HarnessInfo
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
}
