import RAKit
import SwiftUI

struct StatusIcon: View {
    let status: SessionStatus
    /// The agent has tasks running in the background.
    var background = false
    /// Something happened since a device last showed the session.
    var unread = false

    var body: some View {
        Group {
            switch status {
            case .idle where background:
                Image(systemName: "clock")
                    .foregroundStyle(.secondary)
                    .accessibilityLabel("Running in the background")
            case .running:
                ProgressView().controlSize(.small)
            case .awaitingApproval:
                Image(systemName: "hand.raised.fill").foregroundStyle(Palette.yellow)
            case .error:
                Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(Palette.red)
            case .stopped:
                Image(systemName: "stop.circle").foregroundStyle(.secondary)
            case .idle where unread, .unknown where unread:
                // Mail's unread dot, smaller than the ring it replaces.
                Circle()
                    .fill(Palette.accent)
                    .frame(width: 10, height: 10)
                    .accessibilityLabel("Unread")
            case .idle, .unknown:
                Image(systemName: "circle").foregroundStyle(.tertiary)
            }
        }
        .frame(width: 18, height: 18)
    }
}

struct HarnessIcon: View {
    let id: String

    var body: some View {
        Image(systemName: symbol)
            .foregroundStyle(color)
    }

    private var symbol: String {
        switch id {
        case "claude": "sparkle"
        case "codex": "chevron.left.forwardslash.chevron.right"
        case "fake": "theatermasks"
        case "acp:cursor": "cursorarrow.rays"
        case "acp:opencode": "curlybraces"
        case "acp:gemini": "diamond"
        case "pi", "acp:omp": "pi"
        default: "cpu"
        }
    }

    private var color: AnyShapeStyle {
        switch id {
        case "claude": AnyShapeStyle(Palette.peach)
        case "codex": AnyShapeStyle(.primary)
        case "acp:gemini": AnyShapeStyle(Palette.blue)
        default: AnyShapeStyle(.secondary)
        }
    }
}

extension SlashCommand {
    /// Its symbol in menus.
    var symbol: String {
        switch name {
        case "compact": "rectangle.compress.vertical"
        default: "slash.circle"
        }
    }
}

struct HarnessBadge: View {
    let id: String
    var name: String?

    var body: some View {
        HStack(spacing: 3) {
            HarnessIcon(id: id).imageScale(.small)
            Text(name ?? id)
        }
    }
}

/// A small ring and percentage showing how full the agent's context is.
struct ContextGauge: View {
    let fraction: Double

    var body: some View {
        HStack(spacing: 3) {
            ZStack {
                Circle().stroke(.quaternary, lineWidth: 2)
                Circle()
                    .trim(from: 0, to: fraction)
                    .stroke(color, style: StrokeStyle(lineWidth: 2, lineCap: .round))
                    .rotationEffect(.degrees(-90))
            }
            .frame(width: 9, height: 9)
            Text(percent).monospacedDigit()
        }
        .foregroundStyle(color)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(percent) of context used")
    }

    var percent: String { fraction.formatted(.percent.precision(.fractionLength(0))) }

    private var color: Color {
        fraction >= 0.9 ? Palette.red : fraction >= 0.7 ? Palette.yellow : Palette.subtext
    }
}

struct ConnectionDot: View {
    let state: ConnectionState

    var body: some View {
        Circle()
            .fill(color)
            .frame(width: 9, height: 9)
            .accessibilityLabel(label)
    }

    var color: Color {
        switch state {
        case .connected: Palette.green
        case .connecting: Palette.yellow
        case .failed: Palette.red
        case .idle: Palette.overlay
        }
    }

    var label: String {
        switch state {
        case .connected: "Connected"
        case .connecting: "Connecting"
        case .failed(let msg): "Failed: \(msg)"
        case .idle: "Not connected"
        }
    }
}

/// A session's state in the sidebar's denser rows: a ring while idle, a
/// spinner while it works, and the symbols that ask for the user. It sits on
/// the middle of the first line of text beside it.
struct StatusGlyph: View {
    let session: Session

    var body: some View {
        Group {
            switch session.status {
            case .running:
                ProgressView().controlSize(.mini)
            case .awaitingApproval:
                Image(systemName: "hand.raised.fill").foregroundStyle(Palette.yellow)
            case .error:
                Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(Palette.red)
            case .stopped:
                Image(systemName: "stop.fill").imageScale(.small).foregroundStyle(Palette.overlay)
            case .idle where session.backgroundTasks?.isEmpty == false:
                Image(systemName: "clock").foregroundStyle(Palette.subtext)
            case .idle where session.isUnread, .unknown where session.isUnread:
                // Mail's unread dot, where the ring would be.
                Circle().fill(Palette.accent).frame(width: 8, height: 8)
            case .idle, .unknown:
                Circle().strokeBorder(Palette.overlay, lineWidth: 1.2).frame(width: 7, height: 7)
            }
        }
        .scaledFont(.caption)
        .frame(width: 16, height: 16)
        .alignmentGuide(.firstTextBaseline) { $0[VerticalAlignment.center] + 4.5 }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(label)
    }

    private var label: String {
        switch session.status {
        case .running: "Working"
        case .awaitingApproval: "Needs you"
        case .error: "Failed"
        case .stopped: "Stopped"
        case .idle where session.backgroundTasks?.isEmpty == false: "Running in the background"
        case .idle where session.isUnread, .unknown where session.isUnread: "Unread"
        case .idle, .unknown: "Idle"
        }
    }
}
