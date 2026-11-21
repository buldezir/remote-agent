package dev.remoteagent.core

import dev.remoteagent.core.protocol.PullRequest
import dev.remoteagent.core.protocol.Session
import dev.remoteagent.core.protocol.WireCoding
import dev.remoteagent.core.store.PullRequestFormat
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull

/** A session's pull request in words, as the other clients put it. */
class PullRequestFormatTests {
    private fun pr(
        state: PullRequest.State = PullRequest.State.OPEN,
        checks: PullRequest.Checks? = null,
        review: PullRequest.Review? = null,
        forge: String = "github",
        url: String = "https://github.com/acme/app/pull/42",
    ) = PullRequest(forge, 42, "Fix the flaky tests", url, state, "ra/fix-tests", "main", checks, review)

    @Test
    fun labels() {
        assertEquals(listOf("Open", "Draft", "Merged", "Closed"), PullRequest.State.entries.map(PullRequestFormat::state))
        assertEquals(
            listOf("Checks passing", "Checks failing", "Checks running", null),
            PullRequest.Checks.entries.map(PullRequestFormat::checks),
        )
        assertEquals(
            listOf("Approved", "Changes requested", "Review required", null),
            PullRequest.Review.entries.map(PullRequestFormat::review),
        )
    }

    @Test
    fun statusLine() {
        assertEquals(
            "Open · Checks failing · Changes requested",
            PullRequestFormat.statusLine(pr(checks = PullRequest.Checks.FAILING, review = PullRequest.Review.CHANGES_REQUESTED)),
        )
        assertEquals(
            "Merged · Checks passing · Approved",
            PullRequestFormat.statusLine(pr(PullRequest.State.MERGED, PullRequest.Checks.PASSING, PullRequest.Review.APPROVED)),
        )
        assertEquals("Draft · Checks running", PullRequestFormat.statusLine(pr(PullRequest.State.DRAFT, PullRequest.Checks.PENDING)))
        assertEquals("Open · Review required", PullRequestFormat.statusLine(pr(review = PullRequest.Review.REVIEW_REQUIRED)))
        assertEquals("Closed", PullRequestFormat.statusLine(pr(PullRequest.State.CLOSED)))
        // What this app doesn't know is left out.
        assertEquals("Open", PullRequestFormat.statusLine(pr(checks = PullRequest.Checks.UNKNOWN, review = PullRequest.Review.UNKNOWN)))
    }

    @Test
    fun accessibilityLabel() {
        assertEquals(
            "Pull request #42, Open, Checks failing, Changes requested",
            PullRequestFormat.accessibilityLabel(pr(checks = PullRequest.Checks.FAILING, review = PullRequest.Review.CHANGES_REQUESTED)),
        )
        assertEquals("Pull request #42, Closed", PullRequestFormat.accessibilityLabel(pr(PullRequest.State.CLOSED)))
        assertEquals("#42", PullRequestFormat.number(pr()))
    }

    @Test
    fun forgeName() {
        assertEquals("GitHub", PullRequestFormat.forgeName(pr()))
        assertEquals("gitlab.com", PullRequestFormat.forgeName(pr(forge = "gitlab", url = "https://gitlab.com/acme/app/-/merge_requests/42")))
        assertEquals("gitea", PullRequestFormat.forgeName(pr(forge = "gitea", url = "not a url")))
    }

    @Test
    fun unknownValuesDecode() {
        val json = """
            {"id":"s1","projectId":"p1","harness":"fake","workspace":{"kind":"worktree","path":"/w"},"status":"idle","title":"t",
             "pullRequest":{"forge":"github","number":7,"title":"T","url":"https://github.com/a/b/pull/7","state":"queued",
               "head":"b","checks":"skipped","review":"dismissed","extra":1},
             "createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}
        """.trimIndent()
        val p = WireCoding.json.decodeFromString(Session.serializer(), json).pullRequest!!
        assertEquals(PullRequest.State.OPEN, p.state)
        assertNull(p.shownChecks)
        assertNull(p.shownReview)
        assertEquals("Open", PullRequestFormat.statusLine(p))

        val none = WireCoding.json.decodeFromString(Session.serializer(), json.replace(Regex(""""pullRequest":\{[^}]*\},"""), ""))
        assertNull(none.pullRequest)
    }
}
