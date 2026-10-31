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

    @Test func overlappingSubscriptionsKeepTheLastReplay() throws {
        let s = SessionStore._testMake("s")
        // A view subscribes while the connection resubscribes everything.
        s.beginReplay()
        s.beginReplay()
        let replay = [try event(3, item: "b", order: 2, text: "second"), try event(7, item: "a", order: 1, text: "first")]
        s._testApply(replay)
        s._testSynchronized(seq: 7, reset: true)
        #expect(!s.live) // the second subscription hasn't synchronized yet
        s._testApply(replay)
        s._testSynchronized(seq: 7, reset: true)
        #expect(s.live)
        #expect(s.transcript.map(\.id) == ["a", "b"])
    }

    @Test func liveOnlyOnceTheReplayIsIn() throws {
        let s = SessionStore._testMake("s")
        #expect(!s.isLive)
        s.beginReplay()
        s._testSynchronized(seq: 1, reset: true)
        #expect(s.isLive)
        // Opened again later: synced from before, but not live until the new replay.
        s.deactivate()
        s.closed()
        s.beginReplay()
        #expect(s.synced && !s.isLive)
        s._testSynchronized(seq: 1, reset: false)
        #expect(s.isLive)
    }

    @Test func gapStopsLiveApply() throws {
        let s = SessionStore._testMake("s")
        s._testSynchronized(seq: 1, reset: true)
        s._testApply([try event(5, item: "x", order: 1, text: "after a gap")])
        #expect(s.items["x"] == nil)
    }

    @Test func removalEmptiesTheStore() throws {
        let s = SessionStore._testMake("s")
        s._testSynchronized(seq: 1, reset: true)
        s._testApply([try event(2, item: "a", order: 1, text: "first")])
        #expect(!s.removed)
        let data = Data(#"{"stream": "session:s", "seq": 3, "type": "session.removed", "id": "s", "ts": "2026-10-09T12:00:00Z"}"#.utf8)
        s._testApply([try WireCoding.decoder().decode(Event.self, from: data)])
        #expect(s.removed)
        #expect(s.session == nil && s.transcript.isEmpty && s.items.isEmpty)
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

    @Test func staysActiveWhileAnyViewShowsIt() {
        let s = SessionStore._testMake("s")
        s.activate()
        s.activate()
        s.deactivate()
        #expect(s.isActive)
        s.deactivate()
        #expect(!s.isActive)
        s.deactivate()
        s.activate()
        #expect(s.isActive)
    }
}

extension SessionStoreTests {
    func harness(steers: Bool) throws -> HarnessInfo {
        let json = #"{"id":"h","name":"H","protocol":"claude","installed":true,"authOk":true,"#
            + #""caps":{"resume":true,"interrupt":true,"setMode":true,"freeModel":true,"modelSelect":true\#(steers ? #","steer":true"# : "")}}"#
        return try WireCoding.decoder().decode(HarnessInfo.self, from: Data(json.utf8))
    }

    @Test func steersWhileAnAgentThatCanWorks() throws {
        let s = SessionStore._testMake("s")
        let steers = try harness(steers: true)
        s._testSynchronized(seq: 0, reset: true)
        s._testApply([try sessionEvent(1, "s", "idle")])
        #expect(!s.canSteer("more", harness: steers))
        s._testApply([try sessionEvent(2, "s", "running")])
        #expect(s.canSteer("more", harness: steers))
        s._testApply([try sessionEvent(3, "s", "awaiting_approval")])
        #expect(s.canSteer("more", harness: steers))
        #expect(!s.canSteer("more", harness: try harness(steers: false)))
        #expect(!s.canSteer("more", harness: nil))
        // Slash commands run only as prompts of their own.
        #expect(!s.canSteer(" /compact", harness: steers))
    }
}

func sessionEvent(_ seq: Int64, _ id: String, _ status: String, archived: Bool = false, error: String? = nil) throws -> Event {
    var obj: [String: Any] = ["id": id, "projectId": "p", "harness": "fake", "status": status, "title": id, "archived": archived,
                              "workspace": ["kind": "root", "path": "/tmp/p"],
                              "createdAt": "2026-10-09T12:00:00Z", "updatedAt": "2026-10-09T12:00:\(String(format: "%02d", seq % 60))Z"]
    if let error { obj["error"] = error }
    let data = try JSONSerialization.data(withJSONObject: ["stream": "index", "seq": seq, "type": "session.upserted", "session": obj])
    return try WireCoding.decoder().decode(Event.self, from: data)
}

struct SessionAlertTests {
    func alert(_ from: String, _ to: String, archived: Bool = false) throws -> SessionAlert? {
        let old = try #require(try sessionEvent(1, "s", from).session)
        let new = try #require(try sessionEvent(2, "s", to, archived: archived, error: to == "error" ? "boom" : nil).session)
        return SessionAlert(from: old, to: new)
    }

    @Test func fromStatusChanges() throws {
        #expect(try alert("idle", "running") == nil)
        #expect(try alert("running", "awaiting_approval") == .needsYou)
        #expect(try alert("idle", "awaiting_approval") == .needsYou)
        #expect(try alert("awaiting_approval", "running") == nil)
        #expect(try alert("running", "idle") == .finished)
        // Answered elsewhere and done, or interrupted: nothing to tell.
        #expect(try alert("awaiting_approval", "idle") == nil)
        #expect(try alert("running", "error") == .failed("boom"))
        #expect(try alert("idle", "error") == .failed("boom"))
        #expect(try alert("running", "running") == nil)
        #expect(try alert("running", "idle", archived: true) == nil)
        #expect(try alert("running", "stopped") == nil)
    }
}

@MainActor
struct IndexChangeTests {
    let server = SavedServer(id: "srv", name: "Test", urls: [URL(string: "http://127.0.0.1:1")!], deviceId: "d", addedAt: .now)

    func connection() -> (ServerConnection, () -> [String]) {
        let c = ServerConnection(server: server, token: "", defaults: UserDefaults(suiteName: "dev.remote-agent.IndexChangeTests")!) { _ in }
        var seen: [String] = []
        c.onSessionChange = { [unowned c] old, new in
            // Reported once the batch is applied.
            #expect(c.sessions[new.id] == new)
            if let alert = SessionAlert(from: old, to: new) { seen.append("\(new.id) \(alert)") }
        }
        return (c, { seen })
    }

    @Test func notForTheFirstSync() throws {
        let (c, seen) = connection()
        c._testHandle(.events(stream: "index", events: [try sessionEvent(3, "a", "running")]))
        c._testHandle(.synchronized(stream: "index", seq: 3, reset: true))
        #expect(seen().isEmpty)
        c._testHandle(.events(stream: "index", events: [try sessionEvent(4, "a", "awaiting_approval"), try sessionEvent(5, "b", "running")]))
        #expect(seen() == ["a needsYou"])
    }

    @Test func whileAway() throws {
        let (c, seen) = connection()
        c._testHandle(.events(stream: "index", events: [try sessionEvent(2, "a", "running"), try sessionEvent(3, "b", "running")]))
        c._testHandle(.synchronized(stream: "index", seq: 3, reset: true))
        // Back after a sleep: the replay holds what changed meanwhile.
        c._testHandle(.closed(reason: nil))
        c._testHandle(.events(stream: "index", events: [try sessionEvent(7, "a", "idle")]))
        c._testHandle(.synchronized(stream: "index", seq: 7, reset: false))
        #expect(seen() == ["a finished"])
        // The server lost its place: a reset, compared with what was cached.
        c._testHandle(.closed(reason: nil))
        c._testHandle(.events(stream: "index", events: [try sessionEvent(1, "a", "idle"), try sessionEvent(2, "b", "error", error: "boom")]))
        c._testHandle(.synchronized(stream: "index", seq: 2, reset: true))
        #expect(seen() == ["a finished", #"b failed(Optional("boom"))"#])
    }

    @Test func onceForARepeatedReplay() throws {
        let (c, seen) = connection()
        c._testHandle(.events(stream: "index", events: [try sessionEvent(2, "a", "running")]))
        c._testHandle(.synchronized(stream: "index", seq: 2, reset: true))
        c._testHandle(.closed(reason: nil))
        let replay = try sessionEvent(5, "a", "awaiting_approval")
        c._testHandle(.events(stream: "index", events: [replay, replay]))
        c._testHandle(.synchronized(stream: "index", seq: 5, reset: false))
        #expect(seen() == ["a needsYou"])
    }
}

/// Drafts stay on the device, by server and session.
@MainActor @Suite(.serialized)
struct DraftTests {
    let server = SavedServer(id: "srv", name: "Test", urls: [URL(string: "http://127.0.0.1:1")!], deviceId: "d", addedAt: .now)

    /// Runs `body` with empty defaults, and empties them after. One suite
    /// for every run, since macOS keeps a file for each.
    func withDefaults(_ body: (UserDefaults) -> Void) {
        let suite = "dev.remote-agent.RAKitTests"
        let defaults = UserDefaults(suiteName: suite)!
        defaults.removePersistentDomain(forName: suite)
        defer { defaults.removePersistentDomain(forName: suite) }
        body(defaults)
    }

    @Test func outliveTheStoreAndTheApp() {
        withDefaults { defaults in
            let c = ServerConnection(server: server, token: "", defaults: defaults) { _ in }
            c.sessionStore("a").draft = "half a thought"
            #expect(c.sessionStore("b").draft == "")
            // The next launch reads it back.
            let next = ServerConnection(server: server, token: "", defaults: defaults) { _ in }
            #expect(next.sessionStore("a").draft == "half a thought")
            // Sent: forgotten.
            c.sessionStore("a").draft = ""
            #expect(defaults.dictionary(forKey: "drafts.srv") == nil)
        }
    }

    @Test func forgetSessionsGoneFromTheServer() {
        withDefaults { defaults in
            let d = Drafts(serverID: "srv", defaults: defaults)
            d["a"] = "kept"
            d["b"] = "gone"
            d.keep { $0 == "a" }
            let reread = Drafts(serverID: "srv", defaults: defaults)
            #expect(reread["a"] == "kept")
            #expect(reread["b"] == "")
            d.removeAll()
            #expect(defaults.dictionary(forKey: "drafts.srv") == nil)
        }
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

    @Test func decodesNameWithSpaces() throws {
        // As written by netinfo.PairingLink for `name = "Studio Mac + iPad"`.
        let l = try #require(PairingLink(string: "remoteagent://pair?code=abc&name=Studio%20Mac%20%2B%20iPad&url=http%3A%2F%2F127.0.0.1%3A7421&v=1"))
        #expect(l.name == "Studio Mac + iPad")
    }

    @Test func parsesGoDates() throws {
        #expect(WireCoding.parseDate("2026-10-09T01:50:59.955123456Z") != nil)
        #expect(WireCoding.parseDate("2026-10-09T01:50:59Z") != nil)
        #expect(WireCoding.parseDate("2026-10-09T01:50:59.9+02:00") != nil)
    }
}
