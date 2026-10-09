import RAKit
import SwiftUI

struct StatusIcon: View {
    let status: SessionStatus

    var body: some View {
        Group {
            switch status {
            case .running:
                ProgressView().controlSize(.small)
            case .awaitingApproval:
                Image(systemName: "hand.raised.fill").foregroundStyle(.orange)
            case .error:
                Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(.red)
            case .stopped:
                Image(systemName: "stop.circle").foregroundStyle(.secondary)
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

    private var color: Color {
        switch id {
        case "claude": .orange
        case "codex": .primary
        case "acp:gemini": .blue
        default: .secondary
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
        fraction >= 0.9 ? .red : fraction >= 0.7 ? .orange : .secondary
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
        case .connected: .green
        case .connecting: .yellow
        case .failed: .red
        case .idle: .gray.opacity(0.5)
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
