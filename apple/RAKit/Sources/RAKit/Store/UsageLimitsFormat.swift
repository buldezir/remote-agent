import Foundation

/// How near an agent is to a usage limit, for the gauge's colour.
public enum LimitLevel: String, Sendable {
    case normal
    /// Amber: `UsageLimitsFormat.warningPercent` or more used.
    case warning
    /// Red: spent.
    case limited
}

/// Usage limits in words: the gauge the apps show for an agent's plan ("5h
/// 34% · week 61%"), its colour, the details behind it, and the notice for a
/// turn that hit a limit. A port of ra's `internal/client/limits.go`, as are
/// `web/src/store/usageLimits.ts` and android/core's `UsageLimitsFormat`;
/// their tests share `protocol/vectors/usage_limits.json`.
///
/// A window whose reset has passed counts as 0% until the agent says again,
/// and `limited` lapses at its `resetsAt`, so what's shown depends on `now`.
public enum UsageLimitsFormat {
    /// The use from which a window shows amber.
    public static let warningPercent = 80

    /// A window's use now: 0 once it has reset since the agent said.
    public static func windowPercent(_ w: UsageWindow, now: Date) -> Int {
        if let r = w.resetsAt, r <= now { return 0 }
        return max(0, min(w.usedPercent, 100))
    }

    static func percentLevel(_ p: Int) -> LimitLevel {
        if p >= 100 { return .limited }
        if p >= warningPercent { return .warning }
        return .normal
    }

    /// A window's colour now.
    public static func windowLevel(_ w: UsageWindow, now: Date) -> LimitLevel {
        percentLevel(windowPercent(w, now: now))
    }

    /// Whether the limit the agent hit hasn't reset yet.
    public static func limitedNow(_ l: UsageLimits, now: Date) -> Bool {
        l.limited && (l.resetsAt.map { $0 > now } ?? true)
    }

    /// The gauge's colour: its most used window's, or red while a limit holds.
    public static func limitsLevel(_ l: UsageLimits, now: Date) -> LimitLevel {
        if limitedNow(l, now: now) { return .limited }
        var level = LimitLevel.normal
        for w in l.windows {
            switch windowLevel(w, now: now) {
            case .limited: return .limited
            case .warning: level = .warning
            case .normal: break
            }
        }
        return level
    }

    /// The gauge: each window's label and use, "5h 34% · week 61%"; "" with
    /// nothing to show.
    public static func limitsGauge(_ l: UsageLimits, now: Date) -> String {
        let parts = l.windows.map { "\($0.label) \(windowPercent($0, now: now))%" }
        if parts.isEmpty && limitedNow(l, now: now) { return "Limit reached" }
        return parts.joined(separator: " · ")
    }

    /// The lines behind the gauge, on hover or a tap: the limit hit, each
    /// window's use and reset, extra usage, the plan, and how old they are.
    public static func limitsDetails(_ l: UsageLimits, now: Date) -> [String] {
        var lines: [String] = []
        if limitedNow(l, now: now) {
            lines.append("Limit reached" + resetsIn(l.resetsAt, now: now))
        }
        for w in l.windows {
            if let r = w.resetsAt, r <= now {
                lines.append("\(w.label): reset \(limitDuration(now.timeIntervalSince(r), future: false)) ago")
                continue
            }
            lines.append("\(w.label): \(windowPercent(w, now: now))% used" + resetsIn(w.resetsAt, now: now))
        }
        if l.overage { lines.append("Using extra usage") }
        if let plan = l.plan, !plan.isEmpty { lines.append(plan + " plan") }
        let age = now.timeIntervalSince(l.updatedAt)
        lines.append(age < 60 ? "Updated just now" : "Updated \(limitDuration(age, future: false)) ago")
        return lines
    }

    private static func resetsIn(_ t: Date?, now: Date) -> String {
        guard let t, t > now else { return "" }
        return " · resets in " + limitDuration(t.timeIntervalSince(now), future: true)
    }

    /// What the session shows for an error whose turn hit the limit `w`: "5h
    /// limit reached · resets in 2h 14m".
    public static func limitNotice(_ w: UsageWindow, now: Date) -> String {
        var s = w.label + " limit reached"
        if let r = w.resetsAt {
            if r > now {
                s += " · resets in " + limitDuration(r.timeIntervalSince(now), future: true)
            } else {
                s += " · reset " + limitDuration(now.timeIntervalSince(r), future: false) + " ago"
            }
        }
        guard let first = s.first else { return s }
        return first.uppercased() + s.dropFirst()
    }

    /// A span to a reset, or since one: "45m", "2h 14m", "3d 4h". A span to
    /// come rounds its minutes up, one past down; either is a minute at least.
    public static func limitDuration(_ d: TimeInterval, future: Bool) -> String {
        // In whole milliseconds, as the wire's times are, so that a span of
        // whole minutes has no remainder from floating point.
        let ms = Int64((d * 1000).rounded())
        var m = Int(ms / 60_000)
        if future && ms % 60_000 != 0 { m += 1 }
        m = max(m, 1)
        if m < 60 { return "\(m)m" }
        if m < 24 * 60 {
            return m % 60 == 0 ? "\(m / 60)h" : "\(m / 60)h \(m % 60)m"
        }
        let h = m % (24 * 60) / 60
        return h > 0 ? "\(m / (24 * 60))d \(h)h" : "\(m / (24 * 60))d"
    }
}

extension UsageLimits {
    /// The gauge now (`UsageLimitsFormat.limitsGauge`).
    public func gauge(now: Date) -> String { UsageLimitsFormat.limitsGauge(self, now: now) }
    /// The gauge's colour now (`UsageLimitsFormat.limitsLevel`).
    public func level(now: Date) -> LimitLevel { UsageLimitsFormat.limitsLevel(self, now: now) }
    /// The lines behind the gauge now (`UsageLimitsFormat.limitsDetails`).
    public func details(now: Date) -> [String] { UsageLimitsFormat.limitsDetails(self, now: now) }
}
