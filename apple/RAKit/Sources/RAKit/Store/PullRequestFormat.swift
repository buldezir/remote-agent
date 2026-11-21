import Foundation

/// A session's pull request in words, as every client words it: its state,
/// checks and review, the status line they make ("Open · Checks failing ·
/// Changes requested"), its accessibility label and the forge's name.
public enum PullRequestFormat {
    /// "Open", "Draft", "Merged", "Closed".
    public static func state(_ s: PullRequest.State) -> String {
        switch s {
        case .open: "Open"
        case .draft: "Draft"
        case .merged: "Merged"
        case .closed: "Closed"
        }
    }

    /// "Checks passing", "Checks failing", "Checks running".
    public static func checks(_ c: PullRequest.Checks) -> String {
        switch c {
        case .passing: "Checks passing"
        case .failing: "Checks failing"
        case .pending: "Checks running"
        }
    }

    /// "Approved", "Changes requested", "Review required".
    public static func review(_ r: PullRequest.Review) -> String {
        switch r {
        case .approved: "Approved"
        case .changesRequested: "Changes requested"
        case .reviewRequired: "Review required"
        }
    }

    private static func parts(_ pr: PullRequest) -> [String] {
        [state(pr.state), pr.checks.map(checks), pr.review.map(review)].compactMap { $0 }
    }

    /// The state, checks and review, the absent ones left out:
    /// "Open · Checks failing · Changes requested", "Closed".
    public static func statusLine(_ pr: PullRequest) -> String {
        parts(pr).joined(separator: " · ")
    }

    /// "#42".
    public static func number(_ pr: PullRequest) -> String { "#\(pr.number)" }

    /// "Pull request #42, Open, Checks failing, Changes requested".
    public static func accessibilityLabel(_ pr: PullRequest) -> String {
        (["Pull request \(number(pr))"] + parts(pr)).joined(separator: ", ")
    }

    /// The forge by name: "GitHub", else the host of the pull request's page.
    public static func forgeName(_ pr: PullRequest) -> String {
        if pr.forge == "github" { return "GitHub" }
        if let host = URL(string: pr.url)?.host(), !host.isEmpty { return host }
        return pr.forge
    }

    /// "Open on GitHub".
    public static func openTitle(_ pr: PullRequest) -> String { "Open on \(forgeName(pr))" }
}
