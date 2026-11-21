import RAKit
import SwiftUI

/// A pull request's glyph and colour, by its state: open green, draft grey,
/// merged mauve, closed red. The words are `PullRequestFormat`'s.
extension PullRequest {
    var symbol: String { state == .merged ? "arrow.triangle.merge" : "arrow.triangle.pull" }

    var color: Color {
        switch state {
        case .open: Palette.green
        case .draft: Palette.overlay
        case .merged: Palette.mauve
        case .closed: Palette.red
        }
    }

    var link: URL? { URL(string: url) }
}

/// "⇅ #42" in the state's colour: a session's pull request on its row. Only
/// shows it: the row stays one tap target.
struct PullRequestBadge: View {
    let pullRequest: PullRequest

    var body: some View {
        HStack(spacing: 2) {
            Image(systemName: pullRequest.symbol)
            Text(PullRequestFormat.number(pullRequest))
                .monospacedDigit()
        }
        .foregroundStyle(pullRequest.color)
        .lineLimit(1)
        .fixedSize()
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(PullRequestFormat.accessibilityLabel(pullRequest))
    }
}

/// A pull request's checks as a small mark: a green check, a red cross, a
/// yellow dot while they run.
struct PullRequestChecksMark: View {
    let checks: PullRequest.Checks

    var body: some View {
        switch checks {
        case .passing:
            Image(systemName: "checkmark").fontWeight(.bold).foregroundStyle(Palette.green)
        case .failing:
            Image(systemName: "xmark").fontWeight(.bold).foregroundStyle(Palette.red)
        case .pending:
            Circle().fill(Palette.yellow).frame(width: 6, height: 6)
        }
    }
}

/// The session's pull request in its header: a tap opens it on the forge, and
/// a long press shows its title and status, to open it or copy its link.
struct PullRequestButton: View {
    let pullRequest: PullRequest
    @Environment(\.openURL) private var openURL

    var body: some View {
        Menu {
            Section("\(PullRequestFormat.number(pullRequest)) \(pullRequest.title)") {
                Text(PullRequestFormat.statusLine(pullRequest))
            }
            if let link = pullRequest.link {
                Button(PullRequestFormat.openTitle(pullRequest), systemImage: "safari") { openURL(link) }
                Button("Copy Link", systemImage: "link") { Platform.copy(pullRequest.url) }
            }
        } label: {
            HStack(spacing: 4) {
                HStack(spacing: 2) {
                    Image(systemName: pullRequest.symbol)
                    Text(PullRequestFormat.number(pullRequest))
                        .monospacedDigit()
                }
                .foregroundStyle(pullRequest.color)
                if let checks = pullRequest.checks {
                    PullRequestChecksMark(checks: checks)
                        .imageScale(.small)
                }
            }
            .scaledFont(.footnote, weight: .semibold)
            .fixedSize()
        } primaryAction: {
            if let link = pullRequest.link { openURL(link) }
        }
        .accessibilityLabel(PullRequestFormat.accessibilityLabel(pullRequest))
        .accessibilityHint("Opens it on \(PullRequestFormat.forgeName(pullRequest))")
    }
}
