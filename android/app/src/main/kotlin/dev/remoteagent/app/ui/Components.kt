package dev.remoteagent.app.ui

import androidx.annotation.DrawableRes
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.size
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import dev.remoteagent.core.protocol.Session
import dev.remoteagent.core.protocol.SessionStatus
import dev.remoteagent.core.protocol.SlashCommand
import dev.remoteagent.core.store.ConnectionState
import kotlinx.coroutines.delay
import java.time.Duration
import java.time.Instant
import kotlin.math.roundToInt

/**
 * Mirrors apple/Shared/Views/Components.swift. [background]: an idle session
 * whose agent left work running, which shows a clock. [unread]: something
 * happened since a device last showed the session, which shows a dot.
 * [draft]: a prompt waits in it, unsent, which shows a pencil.
 */
@Composable
fun StatusIcon(status: SessionStatus, size: Dp = 18.dp, background: Boolean = false, unread: Boolean = false, draft: Boolean = false) {
    Box(Modifier.size(size), contentAlignment = Alignment.Center) {
        when (status) {
            SessionStatus.RUNNING -> CircularProgressIndicator(Modifier.size(size - 4.dp), color = palette.subtext, strokeWidth = 2.dp)
            SessionStatus.AWAITING_APPROVAL -> Icon(painterResource(Icons.approval), "Needs you", tint = palette.yellow)
            SessionStatus.ERROR -> Icon(painterResource(Icons.warning), "Error", tint = palette.red)
            SessionStatus.STOPPED -> Icon(painterResource(Icons.stopped), "Stopped", tint = palette.subtext)
            else -> when {
                background -> Icon(painterResource(Icons.background), "Running in the background", tint = palette.subtext)
                // Mail's unread dot, smaller than the ring: a filled circle its
                // size reads as a chosen radio button. 10dp in the usual box.
                unread -> UnreadDot(size * (5f / 9f), Modifier.semantics { contentDescription = "Unread" })
                draft -> Icon(painterResource(Icons.draft), "Draft", Modifier.size(size - 2.dp), tint = palette.subtext)
                else -> Icon(painterResource(Icons.circle), "Idle", tint = palette.overlay)
            }
        }
    }
}

/**
 * A session's state as a small glyph, for the tablet sidebars' compact rows:
 * a spinner while it works, a hand while it waits for an answer, a triangle
 * once it failed, a stop, a clock while tasks run in the background, a dot
 * while it is unread, a pencil while a draft waits in it, else a ring.
 * Mirrors StatusGlyph in apple/Shared/Views/Components.swift.
 */
@Composable
fun StatusGlyph(session: Session, modifier: Modifier = Modifier) {
    val background = session.status == SessionStatus.IDLE && session.backgroundTasks.isNotEmpty()
    val label = when (session.status) {
        SessionStatus.RUNNING -> "Working"
        SessionStatus.AWAITING_APPROVAL -> "Needs you"
        SessionStatus.ERROR -> "Failed"
        SessionStatus.STOPPED -> "Stopped"
        else -> when {
            background -> "Running in the background"
            session.unread -> "Unread"
            session.draft != null -> "Draft"
            else -> "Idle"
        }
    }
    Box(modifier.size(16.dp).clearAndSetSemantics { contentDescription = label }, contentAlignment = Alignment.Center) {
        when (session.status) {
            SessionStatus.RUNNING -> CircularProgressIndicator(Modifier.size(11.dp), color = palette.subtext, strokeWidth = 1.5.dp)
            SessionStatus.AWAITING_APPROVAL -> Icon(painterResource(Icons.approval), null, Modifier.size(13.dp), tint = palette.yellow)
            SessionStatus.ERROR -> Icon(painterResource(Icons.warning), null, Modifier.size(13.dp), tint = palette.red)
            SessionStatus.STOPPED -> Icon(painterResource(Icons.stop), null, Modifier.size(11.dp), tint = palette.overlay)
            else -> when {
                background -> Icon(painterResource(Icons.background), null, Modifier.size(13.dp), tint = palette.subtext)
                // Mail's unread dot, where the ring would be.
                session.unread -> UnreadDot(8.dp)
                session.draft != null -> Icon(painterResource(Icons.draft), null, Modifier.size(13.dp), tint = palette.subtext)
                else -> {
                    val ring = palette.overlay
                    Canvas(Modifier.size(7.dp)) {
                        val stroke = 1.2.dp.toPx()
                        drawCircle(ring, radius = (size.minDimension - stroke) / 2, style = Stroke(stroke))
                    }
                }
            }
        }
    }
}

/** A filled dot in the accent colour: a session that is unread. */
@Composable
fun UnreadDot(size: Dp, modifier: Modifier = Modifier) {
    val color = palette.accent
    Canvas(modifier.size(size)) { drawCircle(color) }
}

@DrawableRes
fun harnessSymbol(id: String): Int = when (id) {
    "claude" -> Icons.claude
    "codex" -> Icons.codex
    "fake" -> Icons.fake
    "acp:cursor" -> Icons.cursor
    "acp:opencode" -> Icons.opencode
    "acp:gemini" -> Icons.gemini
    "pi", "acp:omp" -> Icons.pi
    else -> Icons.cpu
}

@Composable
fun HarnessIcon(id: String, modifier: Modifier = Modifier) {
    val tint = when (id) {
        "claude" -> palette.peach
        "codex" -> palette.text
        "acp:gemini" -> palette.blue
        else -> palette.subtext
    }
    Icon(painterResource(harnessSymbol(id)), null, modifier.size(16.dp), tint = tint)
}

/** Its icon in menus. */
val SlashCommand.symbol: Int
    @DrawableRes get() = when (name) {
        "compact" -> Icons.compact
        else -> Icons.terminal
    }

@Composable
fun HarnessBadge(id: String, name: String?, style: TextStyle = Type.caption, color: Color = palette.subtext) {
    Row(horizontalArrangement = Arrangement.spacedBy(3.dp), verticalAlignment = Alignment.CenterVertically) {
        HarnessIcon(id, Modifier.size(13.dp))
        Text(name ?: id, style = style, color = color, maxLines = 1)
    }
}

/** A small ring and percentage showing how full the agent's context is. */
@Composable
fun ContextGauge(fraction: Double, style: TextStyle = Type.caption) {
    val color = when {
        fraction >= 0.9 -> palette.red
        fraction >= 0.7 -> palette.yellow
        else -> palette.subtext
    }
    val track = palette.surface1
    val percent = "${(fraction * 100).roundToInt()}%"
    Row(
        Modifier.semantics(mergeDescendants = true) { contentDescription = "$percent of context used" },
        horizontalArrangement = Arrangement.spacedBy(3.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Canvas(Modifier.size(10.dp)) {
            val stroke = 2.dp.toPx()
            drawCircle(track, style = Stroke(stroke))
            drawArc(color, -90f, (fraction * 360).toFloat(), false, style = Stroke(stroke, cap = StrokeCap.Round))
        }
        Text(percent, style = style, color = color)
    }
}

@Composable
fun ConnectionDot(state: ConnectionState, size: Dp = 10.dp) {
    val color = when (state) {
        ConnectionState.Connected -> palette.green
        ConnectionState.Connecting -> palette.yellow
        is ConnectionState.Failed -> palette.red
        ConnectionState.Idle -> palette.overlay
    }
    Canvas(Modifier.size(size).semantics { contentDescription = connectionLabel(state) }) { drawCircle(color) }
}

fun connectionLabel(state: ConnectionState) = when (state) {
    ConnectionState.Connected -> "Connected"
    ConnectionState.Connecting -> "Connecting"
    is ConnectionState.Failed -> "Failed: ${state.message}"
    ConnectionState.Idle -> "Not connected"
}

/** "now", "5m", "3h", "2d": how long ago, updated as time passes. */
@Composable
fun RelativeTime(at: Instant, style: TextStyle = Type.caption2, color: Color = palette.overlay) {
    var now by remember { mutableLongStateOf(System.currentTimeMillis()) }
    LaunchedEffect(at) {
        while (true) {
            delay(30_000)
            now = System.currentTimeMillis()
        }
    }
    Text(relative(at, Instant.ofEpochMilli(now)), style = style, color = color, maxLines = 1)
}

fun relative(at: Instant, now: Instant): String {
    val s = Duration.between(at, now).seconds.coerceAtLeast(0)
    return when {
        s < 60 -> "now"
        s < 3600 -> "${s / 60}m"
        s < 86_400 -> "${s / 3600}h"
        s < 7 * 86_400 -> "${s / 86_400}d"
        s < 365 * 86_400 -> "${s / (7 * 86_400)}w"
        else -> "${s / (365 * 86_400)}y"
    }
}
