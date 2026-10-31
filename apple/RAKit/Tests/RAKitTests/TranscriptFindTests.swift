import Foundation
import Testing
@testable import RAKit

/// The test vectors every client shares, on the items of
/// protocol/fixtures/notification_events_session.json.
struct TranscriptFindTests {
    struct Notification: Decodable { var params: Params }
    struct Params: Decodable { var events: [Event] }

    func items() throws -> [Item] {
        let data = try Data(contentsOf: FixtureTests.dir.appending(path: "notification_events_session.json"))
        return try WireCoding.decoder().decode(Notification.self, from: data).params.events.compactMap(\.item)
    }

    @Test func findsItemsByTheirText() throws {
        let items = try items()
        #expect(TranscriptFind.matches(items, query: "TESTS") == ["i1", "i4", "i8"])
        #expect(TranscriptFind.matches(items, query: "which") == ["i6"])
        #expect(TranscriptFind.matches(items, query: "go test") == ["i3"])
        // Reasoning isn't searched, nor are sub-agents' items.
        #expect(TranscriptFind.matches(items, query: "test output").isEmpty)
        #expect(TranscriptFind.matches(items, query: "sub-agent").isEmpty)
        #expect(TranscriptFind.matches(items, query: "").isEmpty)
        #expect(TranscriptFind.matches(items, query: "  ").isEmpty)
        // Trimmed, then taken whole.
        #expect(TranscriptFind.matches(items, query: " go test ") == ["i3"])
    }

    func tool(title: String?, input: JSONValue?) -> Item {
        var item = Item(id: "t", sessionId: "s", order: 1, kind: .toolCall, status: .completed)
        item.tool = ToolCall(name: "Bash", kind: .execute, title: title, input: input)
        return item
    }

    @Test func toolCallsAreTheirTitleAndCommand() {
        #expect(TranscriptFind.searchText(tool(title: "Run", input: .object(["command": .array([.string("git"), .string("status")])])))
                == "Run\ngit status")
        #expect(TranscriptFind.searchText(tool(title: "Run go test ./...", input: .object(["command": .string("go test ./...")])))
                == "Run go test ./...")
        #expect(TranscriptFind.searchText(tool(title: "Read main.go", input: nil)) == "Read main.go")
        // A list with something other than strings isn't a command.
        #expect(TranscriptFind.searchText(tool(title: "Run", input: .object(["command": .array([.string("ls"), .number(1)])]))) == "Run")
        #expect(TranscriptFind.searchText(tool(title: nil, input: .object(["command": .string("ls")]))) == "\nls")
    }

    @Test func plansApprovalsAndChildren() throws {
        let items = try items()
        let byID = Dictionary(uniqueKeysWithValues: items.map { ($0.id, $0) })
        #expect(TranscriptFind.searchText(try #require(byID["i8"])) == "Run tests\nFix bug")
        #expect(TranscriptFind.searchText(try #require(byID["i6"])) == "Which DB?\nWhich port?")
        #expect(TranscriptFind.searchText(try #require(byID["i7"])) == "1. Do it")
        // A tool call's approval: its tool call has the title.
        #expect(TranscriptFind.searchText(try #require(byID["i5"])) == "")
        #expect(TranscriptFind.searchText(try #require(byID["i2"])) == "")
        #expect(TranscriptFind.searchText(try #require(byID["i11"])) == "")
    }
}
