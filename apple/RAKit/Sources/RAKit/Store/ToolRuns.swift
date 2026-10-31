import Foundation

/// Runs of tool calls in a transcript, each folded into one row that sums it
/// up ("Ran 4 commands, edited 3 files, read 2 files") and opens to show the
/// calls. The web client (`web/src/store/toolRuns.ts`), Android
/// (`android/core`'s `ToolRuns`) and ra (`internal/client/toolruns.go`) fold
/// and word them the same way.
///
/// A run is the tool calls and the agent's thinking between two pieces of
/// conversation (a prompt, a reply, an approval, a plan, a notice, an error),
/// within one turn. It folds with two tool calls or more. A call that waits
/// for approval, failed, or shows images stands on its own, between runs. A
/// run is live while its turn runs and nothing has come after it: it shows
/// open, unless the user shut it, so that the user can watch the agent work,
/// and folds once the agent replies or the turn ends. A prompt queued while
/// the agent works isn't conversation yet: it doesn't end the run it was
/// sent during, and waits after it.
public enum ToolRuns {
    /// The fewest tool calls that fold: one on its own stays as it is.
    public static let minimum = 2

    public struct Run: Identifiable, Hashable, Sendable {
        /// "tools:" and the first item's id, which stays as the run grows.
        public let id: String
        /// Its tool calls, and the reasoning between them.
        public let items: [Item]
        /// Its turn runs and nothing has come after it.
        public let live: Bool

        public var tools: [Item] { items.filter { $0.kind == .toolCall } }
        public var parts: [Part] { ToolRuns.parts(tools) }
        /// "Ran 4 commands, edited 3 files".
        public var title: String { ToolRuns.title(parts) }
        /// The agent is at work in it.
        public var working: Bool { live || items.contains { $0.status == .inProgress } }

        public func contains(_ itemID: String) -> Bool { items.contains { $0.id == itemID } }
    }

    /// The calls of one kind in a run: "ran 4 commands".
    public struct Part: Hashable, Sendable {
        public let kind: ToolKind
        public let count: Int
        public let text: String
    }

    /// A row of the transcript: an item, a run's summary (followed by its
    /// items when it's open), or the end of a turn that was interrupted or
    /// failed, after its last item.
    public enum Row: Identifiable {
        case item(Item)
        case run(Run, expanded: Bool)
        case turnEnd(Turn)

        public var id: String {
            switch self {
            case .item(let item): item.id
            case .run(let run, _): run.id
            case .turnEnd(let turn): "turn-" + turn.id
            }
        }
    }

    /// The transcript's rows. A run is open as `expanded` says, by its id,
    /// else while it's live. Without `grouping`, every item is a row.
    public static func rows(_ items: [Item], turns: [String: Turn], expanded: [String: Bool] = [:],
                            grouping: Bool = true) -> [Row] {
        // A prompt queued during a turn sits among its items, so the last is looked up.
        var last: [String: String] = [:]
        for item in items {
            if let t = item.turnId { last[t] = item.id }
        }
        func end(after item: Item) -> Row? {
            guard let t = item.turnId, last[t] == item.id, let turn = turns[t],
                  turn.status == .interrupted || turn.status == .failed else { return nil }
            return .turnEnd(turn)
        }
        var out: [Row] = []
        for segment in grouping ? segments(items, turns: turns) : items.map(Segment.item) {
            switch segment {
            case .item(let item):
                out.append(.item(item))
                if let row = end(after: item) { out.append(row) }
            case .run(let run):
                let open = expanded[run.id] ?? run.live
                out.append(.run(run, expanded: open))
                if open { out += run.items.map(Row.item) }
                if let last = run.items.last, let row = end(after: last) { out.append(row) }
            }
        }
        return out
    }

    /// The runs that fold, in order.
    public static func runs(_ items: [Item], turns: [String: Turn]) -> [Run] {
        segments(items, turns: turns).compactMap { if case .run(let run) = $0 { run } else { nil } }
    }

    /// The run an item folds into, if it does.
    public static func run(containing itemID: String, in items: [Item], turns: [String: Turn]) -> Run? {
        runs(items, turns: turns).first { $0.contains(itemID) }
    }

    /// Whether an item can be part of a run: reasoning, and tool calls that
    /// neither wait for approval, failed, nor show images.
    public static func folds(_ item: Item) -> Bool {
        switch item.kind {
        case .reasoning:
            true
        case .toolCall:
            item.status != .pending && item.status != .failed && (item.images ?? []).isEmpty
        default:
            false
        }
    }

    enum Segment {
        case item(Item)
        case run(Run)
    }

    static func segments(_ items: [Item], turns: [String: Turn]) -> [Segment] {
        var lastOfTurn: [String: Int] = [:]
        for (i, item) in items.enumerated() { lastOfTurn[item.turnId ?? ""] = i }
        var out: [Segment] = []
        var i = 0
        while i < items.count {
            let first = items[i]
            guard folds(first) else {
                out.append(.item(first))
                i += 1
                continue
            }
            // The run's last item is j; k looks past the prompts queued meanwhile.
            var j = i, k = i
            while k + 1 < items.count {
                let next = items[k + 1]
                if next.status == .queued {
                    k += 1
                } else if folds(next), next.turnId == first.turnId {
                    k += 1
                    j = k
                } else {
                    break
                }
            }
            let span = items[i...j]
            let run = span.filter { $0.status != .queued }
            if run.filter({ $0.kind == .toolCall }).count >= minimum {
                let running = first.turnId.flatMap { turns[$0] }?.status == .running
                out.append(.run(Run(id: "tools:" + first.id, items: run,
                                    live: running && lastOfTurn[first.turnId ?? ""] == j)))
                out += span.filter { $0.status == .queued }.map(Segment.item)
            } else {
                out += span.map(Segment.item)
            }
            i = j + 1
        }
        return out
    }

    /// The order the kinds are summed up in.
    static let kinds: [ToolKind] = [.execute, .edit, .read, .search, .fetch, .think, .other]

    /// The calls of each kind: files edited and read count once each, by
    /// their paths (a call without paths is a file of its own); the rest
    /// count by calls.
    public static func parts(_ tools: [Item]) -> [Part] {
        kinds.compactMap { kind in
            let calls = tools.filter { ($0.tool?.kind ?? .other) == kind }
            guard !calls.isEmpty else { return nil }
            var count = calls.count
            if kind == .edit || kind == .read {
                var files = Set<String>()
                for call in calls {
                    let paths = call.tool?.paths ?? []
                    if paths.isEmpty { files.insert("\u{0}" + call.id) } else { files.formUnion(paths) }
                }
                count = files.count
            }
            return Part(kind: kind, count: count, text: phrase(kind, count))
        }
    }

    static func phrase(_ kind: ToolKind, _ n: Int) -> String {
        func of(_ one: String, _ many: String) -> String { "\(n) \(n == 1 ? one : many)" }
        return switch kind {
        case .execute: "ran " + of("command", "commands")
        case .edit: "edited " + of("file", "files")
        case .read: "read " + of("file", "files")
        case .search: "searched " + of("time", "times")
        case .fetch: "fetched " + of("page", "pages")
        case .think: "started " + of("agent", "agents")
        case .other: "used " + of("tool", "tools")
        }
    }

    /// The parts as a sentence: "Ran 4 commands, edited 3 files".
    public static func title(_ parts: [Part]) -> String {
        capitalized(parts.map(\.text).joined(separator: ", "))
    }

    /// The text with its first letter in capitals, as a part that starts the summary.
    public static func capitalized(_ text: String) -> String {
        text.prefix(1).uppercased() + text.dropFirst()
    }
}
