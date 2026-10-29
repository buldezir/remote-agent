package dev.remoteagent.app

import dev.remoteagent.app.ui.InboxKey
import dev.remoteagent.app.ui.ServerHomeKey
import dev.remoteagent.app.ui.ServersKey
import dev.remoteagent.app.ui.SessionKey
import dev.remoteagent.app.ui.SettingsKey
import dev.remoteagent.app.ui.normalizedBackStack
import kotlin.test.Test
import kotlin.test.assertEquals

/** A tab's back stack as the window turns wide (the sidebar) or narrow (a phone's tabs). */
class BackStackTests {
    private val home = ServerHomeKey("a")
    private val session = SessionKey("a", "s1")

    @Test
    fun wideDropsServerHomes() {
        assertEquals(listOf(ServersKey, session), normalizedBackStack(listOf(ServersKey, home, session), wide = true))
        assertEquals(listOf(ServersKey), normalizedBackStack(listOf(ServersKey, home), wide = true))
        assertEquals(listOf(ServersKey, SettingsKey), normalizedBackStack(listOf(ServersKey, home, SettingsKey), wide = true))
    }

    @Test
    fun narrowPutsASessionOnItsServer() {
        assertEquals(listOf(ServersKey, home, session), normalizedBackStack(listOf(ServersKey, session), wide = false))
        // Settings opened beside a session.
        assertEquals(
            listOf(ServersKey, home, session, SettingsKey),
            normalizedBackStack(listOf(ServersKey, session, SettingsKey), wide = false),
        )
        // Another server's sessions aren't this session's.
        assertEquals(
            listOf(ServersKey, ServerHomeKey("b"), home, session),
            normalizedBackStack(listOf(ServersKey, ServerHomeKey("b"), session), wide = false),
        )
    }

    @Test
    fun aStackThatFitsIsKept() {
        val phone = listOf(ServersKey, home, session)
        assertEquals(phone, normalizedBackStack(phone, wide = false))
        val tablet = listOf(ServersKey, session)
        assertEquals(tablet, normalizedBackStack(tablet, wide = true))
        for (wide in listOf(true, false)) {
            assertEquals(listOf(ServersKey), normalizedBackStack(listOf(ServersKey), wide))
            assertEquals(listOf(ServersKey, SettingsKey), normalizedBackStack(listOf(ServersKey, SettingsKey), wide))
        }
    }

    @Test
    fun roundTripReturnsThePhoneStack() {
        val phone = listOf(ServersKey, home, session)
        assertEquals(phone, normalizedBackStack(normalizedBackStack(phone, wide = true), wide = false))
    }

    @Test
    fun theInboxsSessionsSitOnTheInbox() {
        // Wide or narrow, a session opened from the Inbox stays open on it, with no server home under it.
        for (wide in listOf(true, false)) {
            assertEquals(listOf(InboxKey, session), normalizedBackStack(listOf(InboxKey, session), wide))
            assertEquals(listOf(InboxKey, session, SettingsKey), normalizedBackStack(listOf(InboxKey, session, SettingsKey), wide))
            assertEquals(listOf(InboxKey), normalizedBackStack(listOf(InboxKey), wide))
        }
        val phone = listOf(InboxKey, session)
        assertEquals(phone, normalizedBackStack(normalizedBackStack(phone, wide = true), wide = false))
    }
}
