import Foundation
import Testing
@testable import RAKit

struct PullRequestFormatTests {
    private func pr(_ state: PullRequest.State, checks: PullRequest.Checks? = nil, review: PullRequest.Review? = nil,
                    forge: String = "github", url: String = "https://github.com/acme/app/pull/42") -> PullRequest {
        PullRequest(forge: forge, number: 42, title: "Fix the flaky tests", url: url, state: state,
                    head: "ra/fix-tests", base: "main", checks: checks, review: review)
    }

    @Test func words() {
        #expect([PullRequest.State.open, .draft, .merged, .closed].map(PullRequestFormat.state)
                == ["Open", "Draft", "Merged", "Closed"])
        #expect([PullRequest.Checks.passing, .failing, .pending].map(PullRequestFormat.checks)
                == ["Checks passing", "Checks failing", "Checks running"])
        #expect([PullRequest.Review.approved, .changesRequested, .reviewRequired].map(PullRequestFormat.review)
                == ["Approved", "Changes requested", "Review required"])
    }

    @Test func statusLine() {
        #expect(PullRequestFormat.statusLine(pr(.open, checks: .failing, review: .changesRequested))
                == "Open · Checks failing · Changes requested")
        #expect(PullRequestFormat.statusLine(pr(.merged, checks: .passing, review: .approved))
                == "Merged · Checks passing · Approved")
        #expect(PullRequestFormat.statusLine(pr(.draft, checks: .pending)) == "Draft · Checks running")
        #expect(PullRequestFormat.statusLine(pr(.open, review: .reviewRequired)) == "Open · Review required")
        #expect(PullRequestFormat.statusLine(pr(.closed)) == "Closed")
    }

    @Test func accessibility() {
        #expect(PullRequestFormat.accessibilityLabel(pr(.open, checks: .failing, review: .changesRequested))
                == "Pull request #42, Open, Checks failing, Changes requested")
        #expect(PullRequestFormat.accessibilityLabel(pr(.closed)) == "Pull request #42, Closed")
        #expect(PullRequestFormat.number(pr(.open)) == "#42")
    }

    @Test func forge() {
        #expect(PullRequestFormat.openTitle(pr(.open)) == "Open on GitHub")
        #expect(PullRequestFormat.forgeName(pr(.open, forge: "gitlab", url: "https://gitlab.example.com/a/b/-/merge_requests/42"))
                == "gitlab.example.com")
        #expect(PullRequestFormat.forgeName(pr(.open, forge: "gitea", url: "")) == "gitea")
    }

    /// Values this app doesn't know show as open, with no checks or review,
    /// and don't break the session.
    @Test func lenientDecoding() throws {
        let json = #"""
        {"forge":"forgejo","number":7,"title":"T","url":"https://x/7","state":"queued","head":"b",
         "checks":"skipped","review":"commented","extra":1}
        """#
        let p = try WireCoding.decoder().decode(PullRequest.self, from: Data(json.utf8))
        #expect(p.state == .open && p.checks == nil && p.review == nil && p.base == nil)
        #expect(PullRequestFormat.statusLine(p) == "Open")
        #expect(PullRequestFormat.forgeName(p) == "x")
    }
}
