package dev.remoteagent.core.store

import dev.remoteagent.core.protocol.PullRequest
import java.net.URI

/**
 * A session's pull request in words: its state, checks and review, the
 * status line that joins them ("Open · Checks failing · Changes requested"),
 * what TalkBack says for it, and its forge's name. The same wording as the
 * other clients'.
 */
object PullRequestFormat {
    fun state(s: PullRequest.State): String = when (s) {
        PullRequest.State.OPEN -> "Open"
        PullRequest.State.DRAFT -> "Draft"
        PullRequest.State.MERGED -> "Merged"
        PullRequest.State.CLOSED -> "Closed"
    }

    /** Null for checks this app doesn't know. */
    fun checks(c: PullRequest.Checks): String? = when (c) {
        PullRequest.Checks.PASSING -> "Checks passing"
        PullRequest.Checks.FAILING -> "Checks failing"
        PullRequest.Checks.PENDING -> "Checks running"
        PullRequest.Checks.UNKNOWN -> null
    }

    /** Null for a review this app doesn't know. */
    fun review(r: PullRequest.Review): String? = when (r) {
        PullRequest.Review.APPROVED -> "Approved"
        PullRequest.Review.CHANGES_REQUESTED -> "Changes requested"
        PullRequest.Review.REVIEW_REQUIRED -> "Review required"
        PullRequest.Review.UNKNOWN -> null
    }

    private fun parts(pr: PullRequest): List<String> =
        listOfNotNull(state(pr.state), pr.checks?.let(::checks), pr.review?.let(::review))

    /** "Open · Checks failing · Changes requested": the state, then its checks and review, when there are any. */
    fun statusLine(pr: PullRequest): String = parts(pr).joinToString(" · ")

    /** "#42" */
    fun number(pr: PullRequest): String = "#${pr.number}"

    /** "Pull request #42, Open, Checks failing, Changes requested" */
    fun accessibilityLabel(pr: PullRequest): String = (listOf("Pull request #${pr.number}") + parts(pr)).joinToString(", ")

    /** The forge's name, for "Open on GitHub": a forge this app doesn't know goes by its url's host. */
    fun forgeName(pr: PullRequest): String = when (pr.forge) {
        "github" -> "GitHub"
        else -> runCatching { URI(pr.url).host }.getOrNull()?.takeIf { it.isNotEmpty() } ?: pr.forge
    }
}
