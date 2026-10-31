package dev.remoteagent.core

import dev.remoteagent.core.protocol.UsageLimits
import dev.remoteagent.core.protocol.UsageWindow
import dev.remoteagent.core.protocol.WireCoding
import dev.remoteagent.core.store.UsageLimitsFormat
import kotlinx.serialization.Serializable
import java.time.Duration
import java.time.Instant
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * The vectors every client's usage-limits formatting shares
 * (protocol/vectors/usage_limits.json), as ra's internal/client/limits_test.go,
 * RAKit's and web/src/store's tests check them.
 */
class UsageLimitsTests {
    @Serializable
    private data class Vectors(val now: String, val cases: List<Case>, val notices: List<Notice>, val durations: List<Span>)

    @Serializable
    private data class Case(
        val name: String,
        val limits: UsageLimits,
        val gauge: String,
        val level: String,
        val windowLevels: List<String>,
        val windowPercents: List<Int>,
        val details: List<String>,
    )

    @Serializable
    private data class Notice(val window: UsageWindow, val text: String)

    @Serializable
    private data class Span(val seconds: Long, val future: String, val past: String)

    private val v = WireCoding.json.decodeFromString(Vectors.serializer(), vector("usage_limits.json"))
    private val now = Instant.parse(v.now)

    @Test
    fun cases() {
        assertTrue(v.cases.isNotEmpty())
        for (c in v.cases) {
            val l = c.limits
            assertEquals(c.gauge, UsageLimitsFormat.limitsGauge(l, now), "${c.name}: gauge")
            assertEquals(c.level, UsageLimitsFormat.limitsLevel(l, now).wire, "${c.name}: level")
            assertEquals(c.windowLevels, l.windows.map { UsageLimitsFormat.windowLevel(it, now).wire }, "${c.name}: window levels")
            assertEquals(c.windowPercents, l.windows.map { UsageLimitsFormat.windowPercent(it, now) }, "${c.name}: window percents")
            assertEquals(c.details, UsageLimitsFormat.limitsDetails(l, now), "${c.name}: details")
        }
    }

    @Test
    fun notices() {
        assertTrue(v.notices.isNotEmpty())
        for (n in v.notices) assertEquals(n.text, UsageLimitsFormat.limitNotice(n.window, now))
    }

    @Test
    fun durations() {
        assertTrue(v.durations.isNotEmpty())
        for (d in v.durations) {
            val span = Duration.ofSeconds(d.seconds)
            assertEquals(d.future, UsageLimitsFormat.limitDuration(span, future = true), "${d.seconds}s to come")
            assertEquals(d.past, UsageLimitsFormat.limitDuration(span, future = false), "${d.seconds}s past")
        }
    }
}
