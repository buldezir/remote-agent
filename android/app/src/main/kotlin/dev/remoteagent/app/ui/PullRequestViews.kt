package dev.remoteagent.app.ui

import android.content.Intent
import androidx.annotation.DrawableRes
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.onLongClick
import androidx.compose.ui.semantics.role
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.core.net.toUri
import dev.remoteagent.app.platform.Platform
import dev.remoteagent.core.protocol.PullRequest
import dev.remoteagent.core.store.PullRequestFormat

// A session's pull request: the badge on its rows, and the button in its
// header that opens it. The wording is PullRequestFormat's, in core.

/** A pull request's colour, by its state: open green, draft grey, merged mauve, closed red. */
@Composable
fun pullRequestColor(state: PullRequest.State): Color = when (state) {
    PullRequest.State.OPEN -> palette.green
    PullRequest.State.DRAFT -> palette.subtext
    PullRequest.State.MERGED -> palette.mauve
    PullRequest.State.CLOSED -> palette.red
}

@DrawableRes
fun pullRequestIcon(state: PullRequest.State): Int =
    if (state == PullRequest.State.MERGED) Icons.pullRequestMerged else Icons.pullRequest

/** "⇅ #42", tinted by its state, for a session's row. It takes no taps: the row stays one target. */
@Composable
fun PullRequestBadge(pr: PullRequest, modifier: Modifier = Modifier, style: TextStyle = Type.caption) {
    val color = pullRequestColor(pr.state)
    Row(
        modifier.clearAndSetSemantics { contentDescription = PullRequestFormat.accessibilityLabel(pr) },
        horizontalArrangement = Arrangement.spacedBy(2.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Icon(painterResource(pullRequestIcon(pr.state)), null, Modifier.size(12.dp), tint = color)
        Text(PullRequestFormat.number(pr), style = style.copy(fontWeight = FontWeight.Medium, fontFeatureSettings = "tnum"), color = color, maxLines = 1)
    }
}

/** A small mark for a pull request's checks: a green check, a red cross, or a yellow dot while they run. */
@Composable
fun PullRequestChecksMark(checks: PullRequest.Checks, size: Dp = 12.dp) {
    when (checks) {
        PullRequest.Checks.PASSING -> Icon(painterResource(Icons.check), null, Modifier.size(size), tint = palette.green)
        PullRequest.Checks.FAILING -> Icon(painterResource(Icons.close), null, Modifier.size(size), tint = palette.red)
        PullRequest.Checks.PENDING -> Box(Modifier.size(size), contentAlignment = Alignment.Center) {
            Box(Modifier.size(size * 0.55f).background(palette.yellow, CircleShape))
        }
        PullRequest.Checks.UNKNOWN -> {}
    }
}

/**
 * The header's pull request: its glyph and number, tinted by its state, and
 * its checks. A tap opens it in the browser; a long press shows its title and
 * status, with Open on <forge> and Copy Link.
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
fun PullRequestButton(pr: PullRequest) {
    val context = LocalContext.current
    var menu by remember { mutableStateOf(false) }
    val color = pullRequestColor(pr.state)
    val forge = PullRequestFormat.forgeName(pr)
    fun open() {
        runCatching { context.startActivity(Intent(Intent.ACTION_VIEW, pr.url.toUri())) }
    }
    Box {
        Row(
            Modifier.testTag("pull-request").clip(RoundedCornerShape(8.dp))
                .combinedClickable(onClick = ::open, onLongClick = { menu = true })
                .clearAndSetSemantics {
                    contentDescription = PullRequestFormat.accessibilityLabel(pr)
                    role = Role.Button
                    onLongClick("Details") { menu = true; true }
                }
                .padding(horizontal = 8.dp, vertical = 8.dp),
            horizontalArrangement = Arrangement.spacedBy(3.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Icon(painterResource(pullRequestIcon(pr.state)), null, Modifier.size(16.dp), tint = color)
            Text(
                PullRequestFormat.number(pr), style = Type.subheadline.copy(fontWeight = FontWeight.SemiBold, fontFeatureSettings = "tnum"),
                color = color, maxLines = 1,
            )
            pr.shownChecks?.let { PullRequestChecksMark(it, 14.dp) }
        }
        DropdownMenu(menu, { menu = false }, containerColor = palette.mantle) {
            Column(Modifier.widthIn(max = 280.dp).padding(horizontal = 16.dp, vertical = 6.dp), verticalArrangement = Arrangement.spacedBy(2.dp)) {
                Text("${PullRequestFormat.number(pr)} ${pr.title}", style = Type.subheadline.copy(fontWeight = FontWeight.SemiBold), color = palette.text)
                Text(PullRequestFormat.statusLine(pr), style = Type.caption, color = palette.subtext)
            }
            HorizontalDivider(color = palette.surface0)
            MenuItem("Open on $forge", Icons.openPage) { menu = false; open() }
            MenuItem("Copy Link", Icons.copy) { menu = false; Platform.copy(context, pr.url, "Link") }
        }
    }
}
