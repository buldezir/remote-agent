package dev.remoteagent.app.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.ReadOnlyComposable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.remoteagent.core.protocol.UsageLimits
import dev.remoteagent.core.protocol.UsageWindow
import dev.remoteagent.core.store.LimitLevel
import dev.remoteagent.core.store.UsageLimitsFormat
import kotlinx.coroutines.delay
import java.time.Instant

/**
 * The time now, kept current every [periodMillis], for text that counts
 * down to a reset or up from an update.
 */
@Composable
fun rememberNow(periodMillis: Long = 20_000): Instant {
    var now by remember { mutableStateOf(Instant.now()) }
    LaunchedEffect(periodMillis) {
        while (true) {
            now = Instant.now()
            delay(periodMillis)
        }
    }
    return now
}

/** A limit level's colour: amber nearing a limit, red once spent. */
@Composable
@ReadOnlyComposable
fun limitColor(level: LimitLevel): Color = when (level) {
    LimitLevel.NORMAL -> palette.subtext
    LimitLevel.WARNING -> palette.yellow
    LimitLevel.LIMITED -> palette.red
}

/** Whether [UsageGauge] shows anything for these limits: a window, or a limit that holds. */
fun UsageLimits.hasGauge(now: Instant = Instant.now()): Boolean = UsageLimitsFormat.limitsGauge(this, now).isNotEmpty()

/**
 * An agent's plan usage, as a small gauge ("5h 34% · week 61%") coloured by
 * how near a limit it is. A tap opens the details beneath it, each window's
 * use and reset, the plan and how old they are, and closes them again. Shows
 * nothing when the agent reported nothing to show. In Server Settings'
 * agents and under the agent a new session starts with.
 */
@Composable
fun UsageGauge(limits: UsageLimits, modifier: Modifier = Modifier) {
    val now = rememberNow()
    val gauge = UsageLimitsFormat.limitsGauge(limits, now)
    if (gauge.isEmpty()) return
    var open by rememberSaveable(limits.harness) { mutableStateOf(false) }
    val color = limitColor(UsageLimitsFormat.limitsLevel(limits, now))
    Column(modifier) {
        Row(
            Modifier.clip(RoundedCornerShape(4.dp))
                .clickable(role = Role.Button, onClickLabel = if (open) "Hide usage details" else "Show usage details") { open = !open }
                .semantics { stateDescription = if (open) "Expanded" else "Collapsed" }
                .padding(vertical = 2.dp)
                .testTag("limits-${limits.harness}"),
            horizontalArrangement = Arrangement.spacedBy(4.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Icon(painterResource(Icons.usage), null, Modifier.size(14.dp), tint = color)
            Text(
                gauge, style = Type.caption.copy(fontFeatureSettings = "tnum"), color = color,
                maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false),
            )
            Icon(painterResource(Icons.expand), null, Modifier.size(14.dp).rotate(if (open) 180f else 0f), tint = palette.overlay)
        }
        if (open) {
            // The limit hit, then a line for each window, in their colours; the rest plain.
            val first = if (UsageLimitsFormat.limitedNow(limits, now)) 1 else 0
            Column(Modifier.padding(start = 18.dp, top = 2.dp).testTag("limits-details")) {
                UsageLimitsFormat.limitsDetails(limits, now).forEachIndexed { i, line ->
                    val level = when {
                        i < first -> LimitLevel.LIMITED
                        i - first < limits.windows.size -> UsageLimitsFormat.windowLevel(limits.windows[i - first], now)
                        else -> LimitLevel.NORMAL
                    }
                    Text(line, style = Type.caption.copy(fontFeatureSettings = "tnum"), color = limitColor(level))
                }
            }
        }
    }
}

/**
 * An error whose turn hit a usage limit, as a notice: "5h limit reached ·
 * resets in 2h 14m", red until the limit resets, then muted. A limit whose
 * reset is unknown stays red.
 */
@Composable
fun LimitNotice(limit: UsageWindow, modifier: Modifier = Modifier) {
    val now = rememberNow()
    val holds = limit.resetsAt?.isAfter(now) ?: true
    val color = if (holds) palette.red else palette.subtext
    Row(
        modifier.fillMaxWidth().testTag("limit-notice"),
        horizontalArrangement = Arrangement.spacedBy(6.dp, Alignment.CenterHorizontally),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Icon(painterResource(Icons.usage), null, Modifier.size(16.dp), tint = color)
        Text(
            UsageLimitsFormat.limitNotice(limit, now), style = Type.footnote, color = color, textAlign = TextAlign.Center,
            modifier = Modifier.weight(1f, fill = false),
        )
    }
}
