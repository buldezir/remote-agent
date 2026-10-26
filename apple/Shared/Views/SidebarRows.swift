import RAKit
import SwiftUI

/// A session in the Inbox: its state, title and age, and where it runs.
struct InboxRow: View {
    let item: ServerSession
    /// The server's name leads the second line, when there is more than one.
    let showsServer: Bool

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            StatusGlyph(session: item.session)
            VStack(alignment: .leading, spacing: 2) {
                HStack(alignment: .firstTextBaseline, spacing: 6) {
                    Text(item.session.title)
                        .scaledFont(.body, weight: item.session.standsOut ? .semibold : nil)
                        .lineLimit(1)
                    Spacer(minLength: 4)
                    SessionAge(date: item.session.updatedAt)
                        .scaledFont(.caption2)
                        .foregroundStyle(.tertiary)
                }
                // Where the line is too narrow for the branch, it goes.
                ViewThatFits(in: .horizontal) {
                    SessionPlace(item: item, server: showsServer, branch: true)
                    SessionPlace(item: item, server: showsServer, branch: false)
                }
                .scaledFont(.caption)
                .foregroundStyle(.secondary)
            }
        }
        .paletteText()
    }
}

/// A session in the Projects layout, under its project: one line.
struct TreeRow: View {
    let item: ServerSession

    var body: some View {
        HStack(spacing: 8) {
            StatusGlyph(session: item.session)
            Text(item.session.title)
                .scaledFont(.body, weight: item.session.standsOut ? .semibold : nil)
                .lineLimit(1)
            Spacer(minLength: 4)
            if item.session.workspace.kind == .worktree {
                Image(systemName: "arrow.triangle.branch")
                    .scaledFont(.caption2)
                    .foregroundStyle(.tertiary)
                    .help(item.session.workspace.branch ?? "Worktree")
                    .accessibilityLabel("Worktree \(item.session.workspace.branch ?? "")")
            }
            SessionAge(date: item.session.updatedAt)
                .scaledFont(.caption2)
                .foregroundStyle(.tertiary)
        }
        .paletteText()
    }
}

/// A session in the Columns layout's list, as Mail shows a message: the title
/// and age, where it runs, and what the agent is doing or last did.
struct SessionListRow: View {
    let item: ServerSession
    let showsServer: Bool

    private var session: Session { item.session }

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            StatusGlyph(session: session)
            VStack(alignment: .leading, spacing: 2) {
                HStack(alignment: .firstTextBaseline, spacing: 6) {
                    Text(session.title)
                        .scaledFont(.body, weight: session.standsOut ? .semibold : .medium)
                        .lineLimit(1)
                    Spacer(minLength: 4)
                    SessionAge(date: session.updatedAt)
                        .scaledFont(.caption2)
                        .foregroundStyle(.tertiary)
                }
                ViewThatFits(in: .horizontal) {
                    SessionPlace(item: item, server: showsServer, branch: true)
                    SessionPlace(item: item, server: showsServer, branch: false)
                }
                .scaledFont(.caption)
                .foregroundStyle(.tertiary)
                Text(activity)
                    .scaledFont(.callout)
                    .foregroundStyle(session.status == .error ? AnyShapeStyle(Palette.red) : AnyShapeStyle(.secondary))
                    .lineLimit(2)
            }
        }
        .padding(.vertical, 2)
        .paletteText()
    }

    /// What the agent is doing: from the session's state, its error or its
    /// background task, else the agent, model and context it has used.
    private var activity: String {
        switch session.status {
        case .awaitingApproval: return "Waiting for your answer"
        case .error: return session.error ?? "Failed"
        case .running: return "Working…"
        case .stopped: return "Stopped"
        case .idle, .unknown:
            if let task = session.backgroundTasks?.first {
                return "In the background: \(task.description)"
            }
            let harness = item.connection.harness(session.harness)
            let context = session.context?.fraction.map { "\($0.formatted(.percent.precision(.fractionLength(0)))) context" }
            return [session.modelName(in: harness) ?? harness?.name ?? session.harness, context]
                .compactMap { $0 }
                .joined(separator: " · ")
        }
    }
}

/// "Mac Studio › weather-api ⑂ ra/fix-cache": the server (when there is more
/// than one), the project, and a worktree's branch.
struct SessionPlace: View {
    let item: ServerSession
    let server: Bool
    let branch: Bool

    var body: some View {
        HStack(spacing: 4) {
            if server {
                Text(item.connection.server.name)
                Text("›").foregroundStyle(.tertiary)
            }
            if let project = item.project {
                Text(project.name)
            }
            if branch, item.session.workspace.kind == .worktree, let b = item.session.workspace.branch {
                HStack(spacing: 2) {
                    Image(systemName: "arrow.triangle.branch")
                    Text(b)
                }
                .padding(.leading, 2)
            }
        }
        .lineLimit(1)
        .fixedSize()
    }
}

/// "now", "4m", "3h", "2d", then the date: a session's age where a row has
/// little room. It keeps itself current.
struct SessionAge: View {
    let date: Date

    var body: some View {
        TimelineView(.periodic(from: .now, by: 60)) { context in
            Text(Self.text(date, now: context.date))
                .monospacedDigit()
        }
        .accessibilityLabel(Text(date, format: .relative(presentation: .named)))
    }

    static func text(_ date: Date, now: Date) -> String {
        let seconds = now.timeIntervalSince(date)
        switch seconds {
        case ..<60: return "now"
        case ..<3600: return "\(Int(seconds / 60))m"
        case ..<86_400: return "\(Int(seconds / 3600))h"
        case ..<(7 * 86_400): return "\(Int(seconds / 86_400))d"
        default: return date.formatted(.dateTime.month(.abbreviated).day())
        }
    }
}
