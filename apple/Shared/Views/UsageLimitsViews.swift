import RAKit
import SwiftUI

extension LimitLevel {
    /// Subtext while there's room, amber from 80% (`UsageLimitsFormat.warningPercent`),
    /// red once spent or limited.
    var color: Color {
        switch self {
        case .normal: Palette.subtext
        case .warning: Palette.yellow
        case .limited: Palette.red
        }
    }

    /// A gauge whose needle says the level.
    var symbol: String {
        switch self {
        case .normal: "gauge.with.dots.needle.33percent"
        case .warning: "gauge.with.dots.needle.67percent"
        case .limited: "gauge.with.dots.needle.100percent"
        }
    }
}

/// An agent's usage limits in a line, "5h 34% · week 61%", in the colour of
/// the nearest limit, with the details behind it on hover on the Mac and on
/// a tap on iOS (`UsageLimitsFormat`). It keeps itself current, as windows
/// reset and limits lapse, and shows nothing while there's nothing to say.
struct UsageLimitsGauge: View {
    let limits: UsageLimits
    #if os(iOS)
    @State private var showingDetails = false
    #endif

    var body: some View {
        TimelineView(.periodic(from: .now, by: 60)) { context in
            let now = context.date
            let text = limits.gauge(now: now)
            if !text.isEmpty {
                gauge(text, level: limits.level(now: now), details: limits.details(now: now))
            }
        }
    }

    @ViewBuilder private func gauge(_ text: String, level: LimitLevel, details: [String]) -> some View {
        let label = HStack(spacing: 4) {
            Image(systemName: level.symbol)
            Text(text).monospacedDigit()
        }
        .scaledFont(.caption)
        .foregroundStyle(level.color)
        .lineLimit(1)
        #if os(iOS)
        Button { showingDetails = true } label: { label.contentShape(Rectangle()) }
            .buttonStyle(.borderless)
            .popover(isPresented: $showingDetails) {
                UsageLimitsDetails(limits: limits)
                    .presentationCompactAdaptation(.popover)
                    .appStyle()
            }
            .accessibilityLabel("Usage limits")
            .accessibilityValue(text)
            .accessibilityHint(details.joined(separator: ", "))
        #else
        label
            .help(details.joined(separator: "\n"))
            .accessibilityElement(children: .ignore)
            .accessibilityLabel("Usage limits")
            .accessibilityValue(([text] + details).joined(separator: ", "))
        #endif
    }
}

/// The lines behind the gauge: the limit hit, each window's use and reset,
/// extra usage, the plan, and how old they are. Each window's line is in its
/// own colour.
struct UsageLimitsDetails: View {
    let limits: UsageLimits

    var body: some View {
        TimelineView(.periodic(from: .now, by: 60)) { context in
            let now = context.date
            let lines = limits.details(now: now)
            VStack(alignment: .leading, spacing: 4) {
                ForEach(Array(lines.enumerated()), id: \.offset) { i, line in
                    Text(line)
                        .scaledFont(i < firstPlain(now) ? .callout : .caption)
                        .foregroundStyle(color(ofLine: i, now: now))
                        .monospacedDigit()
                }
            }
            .fixedSize()
            .padding(Metrics.margin)
        }
    }

    /// The lines go: the limit hit (while it holds), the windows, then the
    /// rest (`UsageLimitsFormat.limitsDetails`).
    private func color(ofLine i: Int, now: Date) -> Color {
        let limited = UsageLimitsFormat.limitedNow(limits, now: now)
        if limited && i == 0 { return Palette.red }
        let w = i - (limited ? 1 : 0)
        if limits.windows.indices.contains(w) {
            let level = UsageLimitsFormat.windowLevel(limits.windows[w], now: now)
            return level == .normal ? Palette.text : level.color
        }
        return Palette.subtext
    }

    /// The first of the lines after the windows'.
    private func firstPlain(_ now: Date) -> Int {
        limits.windows.count + (UsageLimitsFormat.limitedNow(limits, now: now) ? 1 : 0)
    }
}

/// An error whose turn hit a usage limit, in the transcript: "5h limit
/// reached · resets in 2h 14m", red until the window resets. The agent's
/// own words, with the time of the reset, are its tooltip.
struct LimitNotice: View {
    let window: UsageWindow
    /// The error's text.
    var text: String?
    @Environment(\.findHighlight) private var highlight

    var body: some View {
        TimelineView(.periodic(from: .now, by: 60)) { context in
            let now = context.date
            let holds = window.resetsAt.map { $0 > now } ?? true
            Label {
                Text(FindHighlight.text(UsageLimitsFormat.limitNotice(window, now: now), highlight))
            } icon: {
                Image(systemName: "gauge.with.dots.needle.100percent")
            }
            .messageRelativeFont(Metrics.toolText)
            .foregroundStyle(holds ? Palette.red : Palette.subtext)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .help(text ?? "")
        .accessibilityElement(children: .combine)
        .accessibilityHint(text ?? "")
    }
}
