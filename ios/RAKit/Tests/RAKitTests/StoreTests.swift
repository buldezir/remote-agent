import Foundation
import Testing
@testable import RAKit

@MainActor
struct SessionStoreTests {
    func event(_ seq: Int64, item id: String, order: Int64, text: String, kind: String = "assistant_message", parent: String? = nil) throws -> Event {
        var obj: [String: Any] = ["id": id, "sessionId": "s", "order": order, "kind": kind, "status": "completed", "text": text,
                                  "createdAt": "2026-10-09T12:00:00.123456789Z", "updatedAt": "2026-10-09T12:00:00Z"]
        if let parent { obj["parentItemId"] = parent }
        let data = try JSONSerialization.data(withJSONObject: ["stream": "session:s", "seq": seq, "type": "item.upserted", "item": obj])
        return try WireCoding.decoder().decode(Event.self, from: data)
    }

    @Test func replayThenLive() throws {
        let s = SessionStore._testMake("s")
        // Replay (non-contiguous seqs are fine before synchronized).
        s._testApply([try event(3, item: "b", order: 2, text: "second"), try event(7, item: "a", order: 1, text: "first")])
        #expect(s.transcript.isEmpty) // not rebuilt until synchronized
        s._testSynchronized(seq: 7, reset: true)
        #expect(s.transcript.map(\.id) == ["a", "b"])
        // Live, contiguous.
        s._testApply([try event(8, item: "b", order: 2, text: "second, edited")])
        #expect(s.transcript.last?.text == "second, edited")
        // Duplicate seq is ignored.
        s._testApply([try event(8, item: "b", order: 2, text: "stale")])
        #expect(s.transcript.last?.text == "second, edited")
    }

    @Test func resetDropsStaleStateButKeepsReplay() throws {
        let s = SessionStore._testMake("s")
        s._testSynchronized(seq: 1, reset: true)
        s._testApply([try event(2, item: "old", order: 1, text: "old")])
        #expect(s.transcript.map(\.id) == ["old"])
        // Resubscribe from scratch: the server replays only what exists now.
        s.beginReplay()
        s._testApply([try event(9, item: "new", order: 1, text: "new")])
        s._testSynchronized(seq: 9, reset: true)
        #expect(s.transcript.map(\.id) == ["new"])
    }

    @Test func gapStopsLiveApply() throws {
        let s = SessionStore._testMake("s")
        s._testSynchronized(seq: 1, reset: true)
        s._testApply([try event(5, item: "x", order: 1, text: "after a gap")])
        #expect(s.items["x"] == nil)
    }

    @Test func childrenNestUnderParent() throws {
        let s = SessionStore._testMake("s")
        s._testApply([try event(1, item: "tool", order: 1, text: "", kind: "notice"),
                      try event(2, item: "child", order: 2, text: "sub", parent: "tool")])
        s._testSynchronized(seq: 2, reset: true)
        #expect(s.transcript.map(\.id) == ["tool"])
        let parent = try #require(s.transcript.first)
        #expect(s.children(of: parent).map(\.id) == ["child"])
    }
}

struct PairingTests {
    @Test func parsesLink() throws {
        let l = try #require(PairingLink(string: "remoteagent://pair?v=1&name=studio&code=abc&url=http%3A%2F%2F100.64.1.2%3A7421&url=http%3A%2F%2F127.0.0.1%3A7421"))
        #expect(l.name == "studio")
        #expect(l.code == "abc")
        #expect(l.urls.map(\.absoluteString) == ["http://100.64.1.2:7421", "http://127.0.0.1:7421"])
        #expect(PairingLink(string: "https://example.com") == nil)
        #expect(PairingLink(string: "remoteagent://pair?code=abc") == nil)
    }

    @Test func parsesGoDates() throws {
        #expect(WireCoding.parseDate("2026-10-09T01:50:59.955123456Z") != nil)
        #expect(WireCoding.parseDate("2026-10-09T01:50:59Z") != nil)
        #expect(WireCoding.parseDate("2026-10-09T01:50:59.9+02:00") != nil)
    }
}
