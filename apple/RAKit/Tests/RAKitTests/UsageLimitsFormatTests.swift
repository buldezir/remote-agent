import Foundation
import Testing
@testable import RAKit

/// The vectors every client's usage-limits wording shares
/// (protocol/vectors/usage_limits.json): ra's internal/client/limits_test.go,
/// web/src/store/usageLimits.test.ts and android/core's tests.
struct UsageLimitsFormatTests {
    struct Vectors: Decodable {
        struct Case: Decodable {
            var name: String
            var limits: UsageLimits
            var gauge: String
            var level: String
            var windowLevels: [String]
            var windowPercents: [Int]
            var details: [String]
        }
        struct Notice: Decodable { var window: UsageWindow; var text: String }
        struct Duration: Decodable { var seconds: Int; var future: String; var past: String }

        var now: Date
        var cases: [Case]
        var notices: [Notice]
        var durations: [Duration]
    }

    func load() throws -> Vectors {
        let url = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
            .appending(path: "../../../../protocol/vectors/usage_limits.json").standardized
        return try WireCoding.decoder().decode(Vectors.self, from: Data(contentsOf: url))
    }

    @Test func limits() throws {
        let v = try load()
        #expect(v.cases.count >= 7)
        for c in v.cases {
            let l = c.limits
            #expect(UsageLimitsFormat.limitsGauge(l, now: v.now) == c.gauge, "\(c.name)")
            #expect(UsageLimitsFormat.limitsLevel(l, now: v.now).rawValue == c.level, "\(c.name)")
            #expect(l.windows.map { UsageLimitsFormat.windowLevel($0, now: v.now).rawValue } == c.windowLevels, "\(c.name)")
            #expect(l.windows.map { UsageLimitsFormat.windowPercent($0, now: v.now) } == c.windowPercents, "\(c.name)")
            #expect(UsageLimitsFormat.limitsDetails(l, now: v.now) == c.details, "\(c.name)")
            // The shorthands say the same.
            #expect(l.gauge(now: v.now) == c.gauge && l.level(now: v.now).rawValue == c.level
                    && l.details(now: v.now) == c.details, "\(c.name)")
        }
    }

    @Test func notices() throws {
        let v = try load()
        #expect(!v.notices.isEmpty)
        for n in v.notices {
            #expect(UsageLimitsFormat.limitNotice(n.window, now: v.now) == n.text)
        }
    }

    @Test func durations() throws {
        let v = try load()
        #expect(!v.durations.isEmpty)
        for d in v.durations {
            let span = TimeInterval(d.seconds)
            #expect(UsageLimitsFormat.limitDuration(span, future: true) == d.future, "\(d.seconds)s to come")
            #expect(UsageLimitsFormat.limitDuration(span, future: false) == d.past, "\(d.seconds)s past")
        }
    }

    @Test func limitedLapsesAtItsReset() {
        let now = Date(timeIntervalSince1970: 1_000_000)
        var l = UsageLimits(harness: "claude", windows: [], limited: true, resetsAt: now.addingTimeInterval(60), updatedAt: now)
        #expect(UsageLimitsFormat.limitedNow(l, now: now))
        #expect(!UsageLimitsFormat.limitedNow(l, now: now.addingTimeInterval(60)))
        // Without a reset it holds until the agent says otherwise.
        l.resetsAt = nil
        #expect(UsageLimitsFormat.limitedNow(l, now: now.addingTimeInterval(86_400)))
    }
}
