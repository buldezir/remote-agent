package dev.remoteagent.app

import dev.remoteagent.app.ui.importableDetail
import dev.remoteagent.core.protocol.ImportableSession
import java.time.Instant
import java.time.ZoneOffset
import java.util.Locale
import kotlin.test.Test
import kotlin.test.assertEquals

/** How Terminal sessions describes a session to pick up. */
class ImportTests {
    private val t0: Instant = Instant.parse("2026-10-15T12:00:00Z")

    private fun detail(messages: Int, secondsAgo: Long) =
        importableDetail(ImportableSession("n1", "Fix it", messages, t0.minusSeconds(secondsAgo)), t0, ZoneOffset.UTC, locale = Locale.US)

    @Test
    fun saysHowLongAndHowLongAgo() {
        assertEquals("1 message · just now", detail(1, 20))
        assertEquals("4 messages · 5m ago", detail(4, 5 * 60))
        assertEquals("12 messages · 2h ago", detail(12, 2 * 3600 + 59))
        assertEquals("0 messages · 3d ago", detail(0, 3 * 86_400))
        // From a week on, the date.
        assertEquals("6 messages · Oct 3", detail(6, 12 * 86_400))
    }
}
