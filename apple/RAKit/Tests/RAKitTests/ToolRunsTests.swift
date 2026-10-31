import Foundation
import Testing
@testable import RAKit

/// The vectors every client shares: web/src/store/toolRuns.test.ts,
/// android/core's ToolRunsTests.kt and internal/client/toolruns_test.go.
struct ToolRunsTests {
    func item(_ id: String, _ kind: ItemKind, turn: String? = "t1", status: ItemStatus = .completed) -> Item {
        Item(id: id, sessionId: "s", turnId: turn, order: 0, kind: kind, status: status)
    }

    func tool(_ id: String, _ kind: ToolKind, turn: String? = "t1", status: ItemStatus = .completed, paths: [String]? = nil) -> Item {
        var it = item(id, .toolCall, turn: turn, status: status)
        it.tool = ToolCall(name: id, kind: kind, title: id, paths: paths)
        return it
    }

    func turn(_ id: String, _ status: TurnStatus) -> Turn {
        Turn(id: id, sessionId: "s", n: 1, status: status, startedAt: .now)
    }

    /// A finished turn with a run, a failed command, a lone edit and a reply,
    /// and a running one with a run still going.
    var transcript: [Item] {
        [
            item("u1", .userMessage),
            item("r1", .reasoning),
            tool("a", .execute),
            tool("b", .read, paths: ["README.md"]),
            item("r2", .reasoning),
            tool("c", .search),
            tool("d", .read, paths: ["README.md"]),
            tool("e", .edit, paths: ["main.go"]),
            tool("f", .edit, paths: ["main.go", "main_test.go"]),
            tool("g", .execute, status: .failed),
            tool("h", .edit, paths: ["main.go"]),
            item("m1", .assistantMessage),
            item("u2", .userMessage, turn: "t2"),
            tool("i", .fetch, turn: "t2"),
            tool("j", .think, turn: "t2"),
            tool("k", .other, turn: "t2", status: .inProgress),
        ]
    }

    var turns: [String: Turn] { ["t1": turn("t1", .completed), "t2": turn("t2", .running)] }

    func ids(_ rows: [ToolRuns.Row]) -> [String] {
        rows.map { row in
            switch row {
            case .item(let item): item.id
            case .run(let run, let expanded): run.id + (expanded ? " open" : " shut")
            case .turnEnd(let turn): "end " + turn.id
            }
        }
    }

    @Test func foldsRunsOfToolCalls() {
        let runs = ToolRuns.runs(transcript, turns: turns)
        #expect(runs.map(\.id) == ["tools:r1", "tools:i"])
        #expect(runs[0].items.map(\.id) == ["r1", "a", "b", "r2", "c", "d", "e", "f"])
        #expect(runs[0].tools.count == 6)
        #expect(!runs[0].live)
        #expect(!runs[0].working)
        #expect(runs[0].title == "Ran 1 command, edited 2 files, read 1 file, searched 1 time")
        #expect(runs[0].parts.map(\.kind) == [.execute, .edit, .read, .search])
        #expect(runs[0].parts.map(\.count) == [1, 2, 1, 1])
        // Its turn runs and nothing came after it.
        #expect(runs[1].items.map(\.id) == ["i", "j", "k"])
        #expect(runs[1].live)
        #expect(runs[1].working)
        #expect(runs[1].title == "Fetched 1 page, started 1 agent, used 1 tool")
    }

    @Test func rowsShowARunShutUnlessLive() {
        #expect(ids(ToolRuns.rows(transcript, turns: turns)) ==
                ["u1", "tools:r1 shut", "g", "h", "m1", "u2", "tools:i open", "i", "j", "k"])
        // The user's choice wins, either way.
        #expect(ids(ToolRuns.rows(transcript, turns: turns, expanded: ["tools:r1": true, "tools:i": false])) ==
                ["u1", "tools:r1 open", "r1", "a", "b", "r2", "c", "d", "e", "f", "g", "h", "m1", "u2", "tools:i shut"])
        // Not grouping: every item is a row.
        #expect(ids(ToolRuns.rows(transcript, turns: turns, grouping: false)) == transcript.map(\.id))
    }

    @Test func aLiveRunFoldsOnceTheAgentRepliesOrTheTurnEnds() {
        var items = transcript
        #expect(ToolRuns.runs(items, turns: turns)[1].live)
        // A prompt queued meanwhile isn't the turn's.
        items.append(item("q", .userMessage, turn: nil, status: .queued))
        #expect(ToolRuns.runs(items, turns: turns)[1].live)
        items.append(item("m2", .assistantMessage, turn: "t2", status: .inProgress))
        #expect(!ToolRuns.runs(items, turns: turns)[1].live)
        #expect(ids(ToolRuns.rows(items, turns: turns)).suffix(4) == ["u2", "tools:i shut", "q", "m2"])
        var ended = turns
        ended["t2"] = turn("t2", .completed)
        #expect(!ToolRuns.runs(transcript, turns: ended)[1].live)
    }

    @Test func aPromptQueuedMidRunWaitsAfterIt() {
        // Sent while the agent works, before it calls more tools: the run
        // goes on past it, live, and the prompt shows after it.
        var items = transcript
        items.insert(item("q", .userMessage, turn: nil, status: .queued), at: items.count - 1)
        let runs = ToolRuns.runs(items, turns: turns)
        #expect(runs[1].items.map(\.id) == ["i", "j", "k"])
        #expect(runs[1].live)
        #expect(ids(ToolRuns.rows(items, turns: turns)).suffix(6) == ["u2", "tools:i open", "i", "j", "k", "q"])
        #expect(ids(ToolRuns.rows(items, turns: turns, expanded: ["tools:i": false])).suffix(3) == ["u2", "tools:i shut", "q"])
        // After one call, which doesn't fold, it stays where it is.
        let queued = item("q", .userMessage, turn: nil, status: .queued)
        #expect(ids(ToolRuns.rows([tool("a", .execute), queued, item("m", .assistantMessage)], turns: turns)) == ["a", "q", "m"])
        #expect(ids(ToolRuns.rows([tool("a", .execute), queued, tool("b", .read)], turns: turns)) == ["tools:a shut", "q"])
        // Steered into the turn, it's conversation: the run ends before it.
        items[items.count - 2] = item("q", .userMessage, turn: "t2")
        #expect(ids(ToolRuns.rows(items, turns: turns)).suffix(4) == ["u2", "tools:i shut", "q", "k"])
    }

    @Test func callsThatNeedAttentionStandAlone() {
        var shot = tool("s", .other)
        shot.images = [ImageRef(id: "img", mimeType: "image/png", size: 1)]
        let items = [
            tool("a", .execute), tool("b", .edit, status: .pending), tool("c", .execute),
            shot, tool("d", .read), item("r", .reasoning), tool("e", .read),
        ]
        // Pending and with images, a call breaks the run; one call alone
        // doesn't fold, and reasoning doesn't count.
        #expect(ids(ToolRuns.rows(items, turns: turns)) == ["a", "b", "c", "s", "tools:d shut"])
        #expect(ToolRuns.runs(items, turns: turns)[0].title == "Read 2 files")
        // Nor does reasoning on its own.
        #expect(ToolRuns.runs([item("r1", .reasoning), item("r2", .reasoning), tool("x", .read)], turns: turns).isEmpty)
    }

    @Test func runsStayWithinATurn() {
        let items = [tool("a", .execute), tool("b", .execute, turn: "t2"), tool("c", .execute, turn: "t2")]
        #expect(ids(ToolRuns.rows(items, turns: turns)) == ["a", "tools:b open", "b", "c"])
    }

    @Test func aTurnThatEndedBadlyEndsAfterItsRun() {
        let items = [item("u", .userMessage), tool("a", .execute), tool("b", .read)]
        let failed = ["t1": turn("t1", .failed)]
        #expect(ids(ToolRuns.rows(items, turns: failed)) == ["u", "tools:a shut", "end t1"])
        #expect(ids(ToolRuns.rows(items, turns: failed, expanded: ["tools:a": true])) == ["u", "tools:a open", "a", "b", "end t1"])
        #expect(ids(ToolRuns.rows(items, turns: ["t1": turn("t1", .interrupted)], grouping: false)) == ["u", "a", "b", "end t1"])
    }

    @Test func findsTheRunAnItemIsIn() {
        #expect(ToolRuns.run(containing: "c", in: transcript, turns: turns)?.id == "tools:r1")
        #expect(ToolRuns.run(containing: "r2", in: transcript, turns: turns)?.id == "tools:r1")
        #expect(ToolRuns.run(containing: "g", in: transcript, turns: turns) == nil)
        #expect(ToolRuns.run(containing: "m1", in: transcript, turns: turns) == nil)
    }

    @Test func countsFilesByPathAndTheRestByCall() {
        let items = [
            tool("a", .read), tool("b", .read), tool("c", .edit, paths: ["x"]), tool("d", .edit, paths: ["x"]),
            tool("e", .search), tool("f", .search), tool("g", .fetch), tool("h", .fetch),
            tool("i", .think), tool("j", .think), tool("k", .other), tool("l", .other), tool("m", .execute), tool("n", .execute),
        ]
        #expect(ToolRuns.title(ToolRuns.parts(items)) ==
                "Ran 2 commands, edited 1 file, read 2 files, searched 2 times, fetched 2 pages, started 2 agents, used 2 tools")
        #expect(ToolRuns.title([]) == "")
    }
}
