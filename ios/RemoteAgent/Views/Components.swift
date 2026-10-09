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

extension Usage {
    var summary: String {
        var parts: [String] = []
        let tokens = (inputTokens ?? 0) + (outputTokens ?? 0) + (cacheReadTokens ?? 0) + (cacheWriteTokens ?? 0)
        if tokens > 0 { parts.append("\(tokens.formatted(.number.notation(.compactName))) tokens") }
        if let c = costUsd, c > 0 { parts.append(c.formatted(.currency(code: "USD").precision(.fractionLength(c < 0.1 ? 3 : 2)))) }
        return parts.joined(separator: " · ")
    }
}
