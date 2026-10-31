package dev.remoteagent.core.store

import dev.remoteagent.core.protocol.UsageLimits
import dev.remoteagent.core.protocol.UsageWindow
import java.time.Duration
import java.time.Instant

/** How near a limit is, for the gauge's colour. */
enum class LimitLevel(val wire: String) {
    NORMAL("normal"),
    /** Amber: `UsageLimitsFormat.WARNING_PERCENT` or more used. */
    WARNING("warning"),
    /** Red: spent. */
    LIMITED("limited"),
}

/**
 * Usage limits: the gauge the clients show for an agent's plan ("5h 34% ·
 * week 61%"), its colour, the details behind it, and the notice for a turn
 * that hit a limit. A port of ra's internal/client/limits.go, as RAKit's
 * UsageLimitsFormat and web/src/store/usageLimits.ts are; their tests share
 * protocol/vectors/usage_limits.json.
 */
object UsageLimitsFormat {
    /** The use from which a window shows amber. */
    const val WARNING_PERCENT = 80

    /** A window's use now: 0 once it has reset since the agent said. */
    fun windowPercent(w: UsageWindow, now: Instant): Int {
        if (w.resetsAt != null && !w.resetsAt.isAfter(now)) return 0
        return w.usedPercent.coerceIn(0, 100)
    }

    private fun percentLevel(p: Int) = when {
        p >= 100 -> LimitLevel.LIMITED
        p >= WARNING_PERCENT -> LimitLevel.WARNING
        else -> LimitLevel.NORMAL
    }

    /** A window's colour now. */
    fun windowLevel(w: UsageWindow, now: Instant): LimitLevel = percentLevel(windowPercent(w, now))

    /** Whether the limit the agent hit hasn't reset yet. */
    fun limitedNow(l: UsageLimits, now: Instant): Boolean = l.limited && (l.resetsAt == null || l.resetsAt.isAfter(now))

    /** The gauge's colour: its most used window's, or red while a limit holds. */
    fun limitsLevel(l: UsageLimits, now: Instant): LimitLevel {
        if (limitedNow(l, now)) return LimitLevel.LIMITED
        var level = LimitLevel.NORMAL
        for (w in l.windows) {
            when (windowLevel(w, now)) {
                LimitLevel.LIMITED -> return LimitLevel.LIMITED
                LimitLevel.WARNING -> level = LimitLevel.WARNING
                LimitLevel.NORMAL -> {}
            }
        }
        return level
    }

    /** The gauge: each window's label and use, "5h 34% · week 61%"; "" with nothing to show. */
    fun limitsGauge(l: UsageLimits, now: Instant): String {
        val parts = l.windows.map { "${it.label} ${windowPercent(it, now)}%" }
        if (parts.isEmpty() && limitedNow(l, now)) return "Limit reached"
        return parts.joinToString(" · ")
    }

    /**
     * The lines behind the gauge, on a tap: the limit hit, each window's use
     * and reset, extra usage, the plan, and how old they are.
     */
    fun limitsDetails(l: UsageLimits, now: Instant): List<String> = buildList {
        if (limitedNow(l, now)) add("Limit reached" + resetsIn(l.resetsAt, now))
        for (w in l.windows) {
            val resetsAt = w.resetsAt
            if (resetsAt != null && !resetsAt.isAfter(now)) {
                add("${w.label}: reset ${limitDuration(Duration.between(resetsAt, now), future = false)} ago")
            } else {
                add("${w.label}: ${windowPercent(w, now)}% used" + resetsIn(resetsAt, now))
            }
        }
        if (l.overage) add("Using extra usage")
        if (!l.plan.isNullOrEmpty()) add("${l.plan} plan")
        val age = Duration.between(l.updatedAt, now)
        add(if (age < Duration.ofMinutes(1)) "Updated just now" else "Updated ${limitDuration(age, future = false)} ago")
    }

    private fun resetsIn(t: Instant?, now: Instant): String {
        if (t == null || !t.isAfter(now)) return ""
        return " · resets in " + limitDuration(Duration.between(now, t), future = true)
    }

    /**
     * What the session shows for an error whose turn hit the limit [w]:
     * "5h limit reached · resets in 2h 14m".
     */
    fun limitNotice(w: UsageWindow, now: Instant): String {
        var s = "${w.label} limit reached"
        val resetsAt = w.resetsAt
        when {
            resetsAt == null -> {}
            resetsAt.isAfter(now) -> s += " · resets in " + limitDuration(Duration.between(now, resetsAt), future = true)
            else -> s += " · reset " + limitDuration(Duration.between(resetsAt, now), future = false) + " ago"
        }
        return s.replaceFirstChar { it.uppercaseChar() }
    }

    /**
     * A span to a reset, or since one: "45m", "2h 14m", "3d 4h". A span to
     * come rounds its minutes up, one past down; either is a minute at least.
     */
    fun limitDuration(d: Duration, future: Boolean): String {
        var m = d.toMinutes()
        if (future && !d.minusMinutes(m).isZero) m++
        m = maxOf(m, 1)
        val day = 24 * 60L
        return when {
            m < 60 -> "${m}m"
            m < day -> if (m % 60 == 0L) "${m / 60}h" else "${m / 60}h ${m % 60}m"
            else -> {
                val h = m % day / 60
                if (h > 0) "${m / day}d ${h}h" else "${m / day}d"
            }
        }
    }
}
