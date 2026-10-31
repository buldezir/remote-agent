package dev.remoteagent.app

import dev.remoteagent.app.ui.AgentField
import dev.remoteagent.app.ui.agentOptions
import dev.remoteagent.app.ui.count
import dev.remoteagent.app.ui.radFooter
import dev.remoteagent.app.ui.webURL
import dev.remoteagent.core.protocol.AgentSpec
import dev.remoteagent.core.protocol.HarnessCaps
import dev.remoteagent.core.protocol.HarnessInfo
import dev.remoteagent.core.protocol.ServerSettings
import dev.remoteagent.core.protocol.UpdateInfo
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull

/** The server settings' wording, which the Apple and web clients share, and their agent pickers. */
class ServerSettingsTests {
    @Test
    fun radFooters() {
        val app = UpdateInfo(current = "0.11.0", latest = "0.12.0", available = true, via = UpdateInfo.VIA_APP, busy = listOf("Fix the tests", "Docs"))
        assertEquals(
            "The Remote Agent Server app on the Mac installs the update and restarts, with rad. " +
                "Updating now interrupts 2 busy sessions: Fix the tests, Docs.",
            radFooter(app)?.text,
        )
        val rad = app.copy(via = UpdateInfo.VIA_RAD, busy = listOf("Fix the tests"))
        assertEquals(
            "rad installs the update and restarts. Sessions reconnect by themselves. Updating now interrupts 1 busy session: Fix the tests.",
            radFooter(rad)?.text,
        )
        // Up to date: nothing to interrupt.
        assertEquals("rad installs the update and restarts. Sessions reconnect by themselves.", radFooter(rad.copy(available = false))?.text)
        // It can't install: the reason says why, and there's no footer.
        assertNull(radFooter(app.copy(via = null, reason = "rad dev is a development build, which doesn't update itself.")))
        assertNull(radFooter(null))
        assertEquals("1 session", count(1, "session"))
        assertEquals("3 sessions", count(3, "session"))
    }

    @Test
    fun onlyWebLinksOpen() {
        assertEquals("https://claude.com/cai/oauth/authorize?code=true", webURL("https://claude.com/cai/oauth/authorize?code=true"))
        assertEquals("HTTP://example.com", webURL("HTTP://example.com"))
        assertNull(webURL("javascript:alert(1)"))
        assertNull(webURL("file:///etc/passwd"))
        assertNull(webURL("intent://scan/#Intent;scheme=zxing;end"))
        assertNull(webURL("example.com/a:b"))
        assertNull(webURL("https:"))
        assertNull(webURL(null))
    }

    @Test
    fun agentChoices() {
        val claude = HarnessInfo(id = "claude", name = "Claude Code", protocol = "stream-json", installed = true, authOk = true, caps = HarnessCaps(oneShot = true))
        val fake = HarnessInfo(id = "fake", name = "Fake (scripted)", protocol = "fake", installed = true, authOk = false)
        val all = listOf(claude, fake)
        // New Sessions offers every agent; System Agent only those that take one-shot calls.
        assertEquals(listOf("" to "Last Used", "claude" to "Claude Code", "fake" to "Fake (scripted)"), agentOptions("Last Used", all, null, all))
        assertEquals(listOf("" to "None", "claude" to "Claude Code"), agentOptions("None", all.filter { it.caps.oneShot }, "claude", all))
        // A saved agent that isn't offered shows as it is: by its name while installed, else by its id.
        assertEquals(listOf("" to "None", "claude" to "Claude Code", "fake" to "Fake (scripted)"), agentOptions("None", listOf(claude), "fake", all))
        assertEquals(listOf("" to "None", "claude" to "Claude Code", "pi" to "pi"), agentOptions("None", listOf(claude), "pi", all))
    }

    @Test
    fun aChangeKeepsTheOtherSetting() {
        val settings = ServerSettings(systemAgent = AgentSpec("claude", "haiku"), defaultAgent = AgentSpec("codex", "gpt-5.5", "high"))
        assertEquals(AgentSpec("codex", "gpt-5.5", "high"), AgentField.DEFAULT.get(settings))
        assertEquals(AgentSpec("claude", "haiku"), AgentField.SYSTEM.get(settings))
        assertEquals(ServerSettings(systemAgent = AgentSpec("claude", "haiku")), AgentField.DEFAULT.set(settings, null))
        assertEquals(
            ServerSettings(systemAgent = AgentSpec("fake"), defaultAgent = AgentSpec("codex", "gpt-5.5", "high")),
            AgentField.SYSTEM.set(settings, AgentSpec("fake")),
        )
    }
}
