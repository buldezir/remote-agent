package dev.remoteagent.app.ui

import android.text.format.DateFormat
import android.text.format.DateUtils
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.InlineTextContent
import androidx.compose.foundation.text.appendInlineContent
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.Placeholder
import androidx.compose.ui.text.PlaceholderVerticalAlign
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import dev.remoteagent.core.protocol.HarnessInfo
import dev.remoteagent.core.protocol.SearchResult
import dev.remoteagent.core.protocol.Session
import dev.remoteagent.core.protocol.SessionStatus
import dev.remoteagent.core.protocol.SnippetPart
import dev.remoteagent.core.protocol.Workspace
import kotlinx.coroutines.delay
import java.time.Duration
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.util.Locale
import kotlin.math.roundToInt

// The tablet sidebars' session rows, and the search's results, which every
// layout and a phone's Inbox show alike. Mirrors apple/Shared/Views/SidebarRows.swift.

/**
 * A session's row in a sidebar list: a tap opens it, a long press offers to
 * archive or delete it, and a swipe archives it at once, or deletes one
 * already archived. The selection is drawn as a surface inset from the edges, on
 * the list's [background], which also covers the swipe's colour behind the row.
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
fun SessionLink(
    item: ServerSession,
    selected: Boolean,
    onOpen: (SessionKey) -> Unit,
    modifier: Modifier = Modifier,
    background: Color = palette.mantle,
    padding: PaddingValues = PaddingValues(horizontal = 8.dp, vertical = 8.dp),
    /** For android/e2e, which finds rows by it. */
    tag: String = "session-row",
    content: @Composable () -> Unit,
) {
    val actions = LocalSessionActions.current
    var menu by remember { mutableStateOf(false) }
    val request = SessionRequest(item.session, item.server.connection)
    val archive = { actions.archive(request) }
    val delete = { actions.delete(request) }
    Box(modifier) {
        SessionSwipe(request, delete) {
            Box(Modifier.background(background)) {
                Box(
                    Modifier.fillMaxWidth().padding(horizontal = 8.dp).clip(RoundedCornerShape(Metrics.Corner.selection))
                        .background(if (selected) palette.surface0 else Color.Transparent)
                        .combinedClickable(onClick = { onOpen(item.key) }, onLongClick = { menu = true })
                        .padding(padding)
                        .testTag(tag),
                ) {
                    content()
                }
                SessionRowMenu(menu, { menu = false }, item.session.archived, archive, delete)
            }
        }
    }
}

/** A session in the Inbox: its state, title and age, and where it runs. */
@Composable
fun InboxRow(item: ServerSession, showsServer: Boolean) {
    val session = item.session
    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        LeadingGlyph(session)
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            TitleLine(session, if (session.standsOut) FontWeight.SemiBold else null)
            SessionPlace(item, showsServer, Type.caption, palette.subtext)
        }
    }
}

/** A session in the Projects layout, under its project: one line. */
@Composable
fun TreeRow(item: ServerSession) {
    val session = item.session
    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        StatusGlyph(session)
        Text(
            session.title, style = Type.body.copy(fontWeight = if (session.standsOut) FontWeight.SemiBold else null),
            color = palette.text, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f),
        )
        if (session.workspace.kind == Workspace.Kind.WORKTREE) {
            Icon(painterResource(Icons.branch), "Worktree ${session.workspace.branch ?: ""}", Modifier.size(12.dp), tint = palette.overlay)
        }
        SessionAge(session.updatedAt)
    }
}

/**
 * A session in the Columns layout's list, as Mail shows a message: the title
 * and age, where it runs, and what the agent is doing or last did.
 */
@Composable
fun SessionListRow(item: ServerSession, showsServer: Boolean) {
    val session = item.session
    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        LeadingGlyph(session)
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            TitleLine(session, if (session.standsOut) FontWeight.SemiBold else FontWeight.Medium)
            SessionPlace(item, showsServer, Type.caption, palette.overlay)
            Text(
                sessionActivity(session, item.server.harness(session.harness)), style = Type.subheadline,
                color = if (session.status == SessionStatus.ERROR) palette.red else palette.subtext,
                maxLines = 2, overflow = TextOverflow.Ellipsis,
            )
        }
    }
}

/**
 * A session a search found: its title, marked when archived; where it is,
 * its agent and its age; and the snippet that matched, its words marked,
 * with how many of its items match when that is more than one.
 */
@Composable
fun SearchResultRow(item: ServerSession, result: SearchResult, showsServer: Boolean) {
    val session = item.session
    val (age, _) = sessionAge(session.updatedAt)
    val agent = item.server.harness(session.harness)?.name ?: session.harness
    val snippet = snippetText(result.snippet)
    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        LeadingGlyph(session)
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            Row(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.CenterVertically) {
                Text(
                    session.title, style = Type.body.copy(fontWeight = if (session.standsOut) FontWeight.SemiBold else null), color = palette.text,
                    maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false),
                )
                if (session.archived) {
                    Text(
                        "Archived", style = Type.caption2.copy(fontWeight = FontWeight.SemiBold), color = palette.subtext, maxLines = 1,
                        modifier = Modifier.background(palette.surface0, RoundedCornerShape(4.dp)).padding(horizontal = 5.dp, vertical = 1.dp),
                    )
                }
            }
            Text(
                searchResultDetail(item.project?.name, agent, age, item.server.name.takeIf { showsServer }),
                style = Type.caption, color = palette.subtext, maxLines = 1, overflow = TextOverflow.Ellipsis,
            )
            if (snippet.isNotEmpty() || result.matches > 1) {
                Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    Text(
                        snippet, style = Type.subheadline, color = palette.subtext, maxLines = 2, overflow = TextOverflow.Ellipsis,
                        modifier = Modifier.weight(1f).alignByBaseline(),
                    )
                    if (result.matches > 1) {
                        Text(
                            "${result.matches} matches", style = Type.caption2.copy(fontFeatureSettings = "tnum"), color = palette.overlay,
                            maxLines = 1, modifier = Modifier.alignByBaseline(),
                        )
                    }
                }
            }
        }
    }
}

/** A result's second line: "app · Claude Code · 4m", and its server when there are several. */
fun searchResultDetail(project: String?, agent: String, age: String, server: String?): String =
    listOfNotNull(project, agent, age, server).filter { it.isNotEmpty() }.joinToString(" · ")

/** A snippet on one run of text, its matches semibold on the palette's yellow, in the palette's text colour. */
@Composable
private fun snippetText(parts: List<SnippetPart>): AnnotatedString {
    val match = SpanStyle(color = palette.text, fontWeight = FontWeight.SemiBold, background = palette.yellow.copy(alpha = 0.35f))
    return remember(parts, match) {
        buildAnnotatedString {
            for (p in parts) {
                // The lines of a match's context, run together, as the snippet has two at most.
                val text = p.text.replace('\n', ' ')
                if (p.match) withStyle(match) { append(text) } else append(text)
            }
        }
    }
}

/** The title, and the age at the end of the line. */
@Composable
private fun TitleLine(session: Session, weight: FontWeight?) {
    Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
        Text(
            session.title, style = Type.body.copy(fontWeight = weight), color = palette.text,
            maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f).alignByBaseline(),
        )
        SessionAge(session.updatedAt, Modifier.alignByBaseline())
    }
}

/** The status glyph, centred on the title's line. */
@Composable
private fun LeadingGlyph(session: Session) {
    val height = with(LocalDensity.current) { Type.body.lineHeight.toDp() }
    Box(Modifier.height(height), contentAlignment = Alignment.Center) { StatusGlyph(session) }
}

/**
 * What the agent is doing: from the session's state, its error or its
 * background task, else the agent, model and context it has used.
 */
fun sessionActivity(session: Session, harness: HarnessInfo?): String = when (session.status) {
    SessionStatus.AWAITING_APPROVAL -> "Waiting for your answer"
    SessionStatus.ERROR -> session.error ?: "Failed"
    SessionStatus.RUNNING -> "Working…"
    SessionStatus.STOPPED -> "Stopped"
    SessionStatus.IDLE, SessionStatus.UNKNOWN -> session.backgroundTasks.firstOrNull()?.let { "In the background: ${it.description}" }
        ?: listOfNotNull(
            session.modelName(harness) ?: harness?.name ?: session.harness,
            session.context?.fraction?.let { "${(it * 100).roundToInt()}% context" },
        ).joinToString(" · ")
}

/**
 * "Mac Studio › weather-api ⑂ ra/fix-cache": the server (when there is more
 * than one), the project, and a worktree's branch. Where the line is too
 * narrow, the branch is cut short first.
 */
@Composable
fun SessionPlace(item: ServerSession, server: Boolean, style: TextStyle, color: Color) {
    val workspace = item.session.workspace
    val branch = workspace.branch?.takeIf { workspace.kind == Workspace.Kind.WORKTREE }
    val separator = palette.overlay
    val text = buildAnnotatedString {
        if (server) {
            append(item.server.name)
            withStyle(SpanStyle(color = separator)) { append(" › ") }
        }
        item.project?.let { append(it.name) }
        if (branch != null) {
            append("  ")
            appendInlineContent("branch", "⑂")
            append(branch)
        }
    }
    val size = style.fontSize
    val icon = InlineTextContent(Placeholder(size * 1.15f, size, PlaceholderVerticalAlign.TextCenter)) {
        Icon(painterResource(Icons.branch), null, tint = color)
    }
    Text(text, style = style, color = color, maxLines = 1, overflow = TextOverflow.Ellipsis, inlineContent = mapOf("branch" to icon))
}

/** A session's age where a row has little room ("4m"). It keeps itself current. */
@Composable
fun SessionAge(at: Instant, modifier: Modifier = Modifier) {
    val (short, spoken) = sessionAge(at)
    Text(
        short, style = Type.caption2.copy(fontFeatureSettings = "tnum"),
        color = palette.overlay, maxLines = 1, modifier = modifier.semantics { contentDescription = spoken },
    )
}

/** A session's short age ("4m"), and as TalkBack says it ("4 minutes ago"), kept current. */
@Composable
fun sessionAge(at: Instant): Pair<String, String> {
    var now by remember { mutableStateOf(Instant.now()) }
    LaunchedEffect(Unit) {
        while (true) {
            delay(60_000)
            now = Instant.now()
        }
    }
    val locale = LocalConfiguration.current.locales[0]
    val pattern = remember(locale) { DateFormat.getBestDateTimePattern(locale, "MMMd") }
    val spoken = DateUtils.getRelativeTimeSpanString(at.toEpochMilli(), now.toEpochMilli(), DateUtils.MINUTE_IN_MILLIS).toString()
    return shortAge(at, now, pattern = pattern, locale = locale) to spoken
}

/** "now", "4m", "3h", "2d", then the date ("Oct 3"). [pattern]: the date's, for the locale. */
fun shortAge(
    at: Instant,
    now: Instant,
    zone: ZoneId = ZoneId.systemDefault(),
    pattern: String = "MMM d",
    locale: Locale = Locale.getDefault(),
): String {
    val s = Duration.between(at, now).seconds
    return when {
        s < 60 -> "now"
        s < 3600 -> "${s / 60}m"
        s < 86_400 -> "${s / 3600}h"
        s < 7 * 86_400 -> "${s / 86_400}d"
        else -> DateTimeFormatter.ofPattern(pattern, locale).format(at.atZone(zone))
    }
}
