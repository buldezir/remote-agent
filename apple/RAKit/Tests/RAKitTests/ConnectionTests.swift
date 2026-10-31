import Foundation
import Testing
@testable import RAKit

@MainActor
struct ConnectionTests {
    // Nothing listens on port 1, so each attempt fails at once.
    let server = SavedServer(id: "srv", name: "Test", urls: [URL(string: "http://127.0.0.1:1")!], deviceId: "dev1", addedAt: .now)

    private var failed: (ServerConnection) -> Bool {
        { if case .failed = $0.state { true } else { false } }
    }

    /// Polls until `condition` holds, or fails the test after `timeout`.
    private func wait(_ timeout: Duration = .seconds(10), until condition: () -> Bool) async throws {
        let deadline = ContinuousClock.now + timeout
        while !condition() {
            guard ContinuousClock.now < deadline else {
                Issue.record("timed out")
                return
            }
            try await Task.sleep(for: .milliseconds(5))
        }
    }

    @Test func givesUpUntilStartedAgain() async throws {
        let c = ServerConnection(server: server, token: "", defaults: UserDefaults(suiteName: "dev.remote-agent.ConnectionTests")!) { _ in }
        c.firstRetryDelay = 0.01
        c.start()
        #expect(!failed(c))
        try await wait { failed(c) }

        // Given up: no more attempts, so it stays failed.
        for _ in 0..<40 {
            try await Task.sleep(for: .milliseconds(5))
            #expect(failed(c))
        }

        // Opening the server, or coming back to the app, tries again.
        c.start()
        try await wait { c.state == .connecting }
        try await wait { failed(c) }
        c.stop()
    }

    private func connect(to server: TestServer) async throws -> ServerConnection {
        let url = try await server.start()
        let saved = SavedServer(id: "srv", name: "Test", urls: [url], deviceId: "dev1", addedAt: .now)
        let c = ServerConnection(server: saved, token: "t", defaults: UserDefaults(suiteName: "dev.remote-agent.ConnectionTests")!) { _ in }
        c.firstRetryDelay = 0.01
        c.heartbeat = .milliseconds(50)
        c.pingTimeout = 0.3
        c.start()
        try await wait { c.state == .connected }
        // The agents are asked for once connected, on the side: after this,
        // a test's own call is the last one the server saw.
        try await wait { server.calls.contains { $0.method == "harness.list" } }
        return c
    }

    @Test func oldServerCommandFallback() async throws {
        let server = try TestServer()
        let connection = try await connect(to: server)
        defer { connection.stop(); server.stop() }
        let commands = try await connection.sessionStore("s").commands()
        #expect(commands.isEmpty)
    }

    /// A session as the index sends it.
    private func session(_ id: String, unread: Bool = false, draft: String? = nil) -> String {
        #"{"id":"\#(id)","projectId":"p","harness":"fake","status":"idle","title":"\#(id)","archived":false,"unread":\#(unread),"#
            + (draft.map { #""draft":{"text":"\#($0)"},"# } ?? "")
            + #""workspace":{"kind":"root","path":"/tmp/p"},"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}"#
    }

    @Test func marksReadOnce() async throws {
        let server = try TestServer(index: [session("new", unread: true), session("seen", unread: false)])
        let connection = try await connect(to: server)
        defer { connection.stop(); server.stop() }
        try await wait { connection.indexSynced }
        #expect(connection.sessions["new"]?.isUnread == true)

        // Read here at once; the server hears once.
        connection.markRead("new")
        #expect(connection.sessions["new"]?.isUnread == false)
        connection.markRead("new")
        connection.markRead("seen")
        try await wait { server.calls.contains { $0.method == "session.markRead" } }
        try await Task.sleep(for: .milliseconds(50))
        #expect(server.calls.filter { $0.method == "session.markRead" }.count == 1)
    }

    @Test func createsADraftOnlyWhenAsked() throws {
        func json(_ p: ServerConnection.NewSession) throws -> JSONValue {
            try JSONDecoder().decode(JSONValue.self, from: WireCoding.encoder().encode(p))
        }
        let ws = ServerConnection.NewSession.WorkspaceParams(kind: .root)
        let draft = try json(.init(projectId: "p", harness: "fake", model: nil, mode: nil, workspace: ws, prompt: "later", draft: true))
        #expect(draft["draft"] == .bool(true))
        // An older server would start the session with the prompt either way.
        let started = try json(.init(projectId: "p", harness: "fake", model: nil, mode: nil, workspace: ws, prompt: "now"))
        #expect(started["draft"] == nil)
    }

    @Test func forkLeavesOutWhatIsUnset() throws {
        func json(_ p: ServerConnection.ForkSession) throws -> JSONValue {
            try JSONDecoder().decode(JSONValue.self, from: WireCoding.encoder().encode(p))
        }
        let plain = try json(.init(sessionId: "s"))
        #expect(plain["sessionId"] == .string("s"))
        #expect(plain["commandId"] != nil)
        for key in ["turn", "harness", "model", "effort", "mode", "workspace", "prompt", "images", "title", "draft"] {
            #expect(plain[key] == nil)
        }
        let full = try json(.init(sessionId: "s", turn: 2, harness: "codex",
                                  workspace: .init(kind: .worktree, branch: "ra/try"), prompt: "again"))
        #expect(full["turn"] == .number(2))
        #expect(full["harness"] == .string("codex"))
        #expect(full["workspace"]?["kind"] == .string("worktree"))
        #expect(full["workspace"]?["branch"] == .string("ra/try"))
        #expect(full["workspace"]?["baseRef"] == nil)
        #expect(full["prompt"] == .string("again"))
    }

    @Test func savesADraftIntoTheIndexAtOnce() async throws {
        let server = try TestServer(index: [session("s")])
        let connection = try await connect(to: server)
        defer { connection.stop(); server.stop() }
        try await wait { connection.indexSynced }
        let store = connection.sessionStore("s")
        #expect(store.unsentDraft == nil)

        let image = ImageRef(id: "a.png", mimeType: "image/png", size: 1)
        let saved = try await connection.saveDraft("s", text: "later", images: [image])
        #expect(saved.draft?.text == "later")
        // The session's views have it before its event comes.
        #expect(connection.sessions["s"]?.draft?.text == "later")
        #expect(store.unsentDraft?.text == "later")
        let call = try #require(server.calls.last { $0.method == "session.saveDraft" })
        #expect(call.params?["sessionId"] == .string("s"))
        #expect(call.params?["text"] == .string("later"))
        #expect(call.params?["images"] == .array([.string("a.png")]))

        // Nothing saved removes it.
        try await connection.saveDraft("s", text: "")
        #expect(server.calls.last { $0.method == "session.saveDraft" }?.params?["images"] == nil)
        #expect(connection.sessions["s"]?.hasDraft == false)
        #expect(store.unsentDraft == nil)
    }

    @Test func draftSentOnADeadSocketIsDeliveredAfterReconnecting() async throws {
        let server = try TestServer(index: [session("s", draft: "later")])
        let c = try await connect(to: server)
        defer { c.stop(); server.stop() }
        try await wait { c.indexSynced }
        let store = c.sessionStore("s")
        #expect(store.unsentDraft?.text == "later")

        server.quiet()
        try await store.sendDraft()

        // Sent again on the new socket, with the same commandId.
        let sends = server.calls.filter { $0.method == "session.sendDraft" }
        #expect(sends.last?.connection == 1)
        #expect(Set(sends.map(\.commandID)).count == 1)
        #expect(sends.last?.params?["sessionId"] == .string("s"))
        // The outbox shows it in place of the draft until the transcript has it,
        // which comes with the session without its draft.
        #expect(store.outbox.values.map(\.text) == ["later"])
        #expect(store.unsentDraft == nil)
        store._testSynchronized(seq: 0, reset: true)
        let session = #"{"stream":"session:s","seq":1,"type":"session.upserted","session":\#(self.session("s"))}"#
        let item = #"{"stream":"session:s","seq":2,"type":"item.upserted","item":{"id":"i1","sessionId":"s","order":1,"#
            + #""kind":"user_message","status":"completed","text":"later","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}"#
        store._testApply(try [session, item].map { try WireCoding.decoder().decode(Event.self, from: Data($0.utf8)) })
        #expect(store.outbox.isEmpty)
        #expect(store.unsentDraft == nil)
        #expect(store.transcript.map(\.text) == ["later"])
    }

    @Test func sendingNoDraftFails() async throws {
        let server = try TestServer(index: [session("s")])
        let c = try await connect(to: server)
        defer { c.stop(); server.stop() }
        try await wait { c.indexSynced }
        let store = c.sessionStore("s")
        await #expect(throws: RPCError.self) { try await store.sendDraft() }
        #expect(store.outbox.isEmpty)
        #expect(!server.calls.contains { $0.method == "session.sendDraft" })
    }

    @Test func draftFailsOnceTheConnectionGivesUp() async throws {
        let server = try TestServer(index: [session("s", draft: "later")])
        let c = try await connect(to: server)
        defer { c.stop() }
        try await wait { c.indexSynced }
        let store = c.sessionStore("s")

        server.stop()
        await #expect(throws: RPCError.self) { try await store.sendDraft() }
        #expect(failed(c))
        // Not sent: the session's draft again.
        #expect(store.outbox.isEmpty)
        #expect(store.unsentDraft?.text == "later")
    }

    @Test func dropsASocketThatStopsAnswering() async throws {
        let server = try TestServer()
        let c = try await connect(to: server)
        defer { c.stop(); server.stop() }

        // While it answers, the pings keep it.
        try await Task.sleep(for: .milliseconds(300))
        #expect(server.connectionCount == 1)

        // Gone quiet, as after a sleep: the ping goes unanswered, so it
        // reconnects rather than passing for connected.
        server.quiet()
        try await wait { server.connectionCount == 2 }
        try await wait { c.state == .connected }
    }

    @Test func promptSentOnADeadSocketIsDeliveredAfterReconnecting() async throws {
        let server = try TestServer()
        let c = try await connect(to: server)
        defer { c.stop(); server.stop() }
        let store = c.sessionStore("s1")

        server.quiet()
        try await store.send("hello")

        // Sent again on the new socket, with the same commandId.
        let prompts = server.calls.filter { $0.method == "session.prompt" }
        #expect(prompts.last?.connection == 1)
        #expect(Set(prompts.map(\.commandID)).count == 1)
        // Shown until the transcript has it.
        #expect(store.outbox.values.map(\.text) == ["hello"])
    }

    @Test func steersPrompts() async throws {
        let server = try TestServer()
        let c = try await connect(to: server)
        defer { c.stop(); server.stop() }
        let store = c.sessionStore("s1")

        try await store.send("also this", steer: true)
        try await store.send("later")
        let prompts = server.calls.filter { $0.method == "session.prompt" }
        #expect(prompts.map { $0.params?["steer"] } == [.bool(true), nil])

        let queued = try WireCoding.decoder().decode(Item.self, from: Data(
            #"{"id":"q","sessionId":"s1","order":2,"kind":"user_message","status":"queued","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}"#.utf8))
        try await store.steer(queued)
        let steer = try #require(server.calls.last)
        #expect(steer.method == "session.steer")
        #expect(steer.params == .object(["sessionId": .string("s1"), "itemId": .string("q")]))
    }

    @Test func commits() async throws {
        let server = try TestServer()
        let c = try await connect(to: server)
        defer { c.stop(); server.stop() }

        let preview = try await c.commitPreview(sessionID: "s1")
        #expect(preview.branch == "ra/x" && preview.files.count == 1 && preview.message == "Fix it" && preview.base == nil)
        #expect(server.calls.last?.params == .object(["sessionId": .string("s1")]))

        // The message goes trimmed, and only what's asked for with it.
        let task = try await c.commit(sessionID: "s1", message: "  Fix it\n\nBecause.\n ", scope: .pullRequest)
        #expect(task.kind == .commit && task.sessionId == "s1" && task.isRunning)
        let full = try #require(server.calls.last)
        #expect(full.method == "git.commit")
        #expect(full.params == .object(["sessionId": .string("s1"), "message": .string("Fix it\n\nBecause."), "pullRequest": .bool(true)]))

        _ = try await c.commit(sessionID: "s1", message: " \n", scope: .commit)
        #expect(server.calls.last?.params == .object(["sessionId": .string("s1")]))
        _ = try await c.commit(sessionID: "s1", message: "Fix", scope: .push)
        #expect(server.calls.last?.params == .object(["sessionId": .string("s1"), "message": .string("Fix"), "push": .bool(true)]))

        // A session's tasks are asked for with its id; the server's without.
        let running = try await c.tasks(sessionID: "s1")
        #expect(running.map(\.kind) == [.commit] && running.first?.sessionId == "s1")
        #expect(server.calls.last?.params == .object(["sessionId": .string("s1")]))
        #expect(try await c.tasks().isEmpty)
        #expect(server.calls.last?.method == "task.list" && server.calls.last?.params?["sessionId"] == nil)
    }

    @Test func importsTheAgentsOwnSessions() async throws {
        let server = try TestServer()
        let c = try await connect(to: server)
        defer { c.stop(); server.stop() }

        let list = try await c.importable(projectID: "p", harness: "claude")
        #expect(list.map(\.id) == ["n2", "n1"])
        #expect(list.map(\.isActive) == [true, false])
        #expect(list.map(\.messages) == [6, 4])
        let listed = try #require(server.calls.last)
        #expect(listed.method == "session.importable")
        #expect(listed.params == .object(["projectId": .string("p"), "harness": .string("claude")]))

        // A socket that dies on the way: sent again on the new one, with the
        // same commandId, and the session is in the index at once.
        server.quiet()
        let s = try await c.importSession(.init(projectId: "p", harness: "claude", nativeId: "n1", model: "opus", mode: "plan"))
        #expect(s.nativeId == "n1" && s.harness == "claude" && s.workspace.kind == .root)
        #expect(c.sessions[s.id]?.nativeId == "n1")
        let imports = server.calls.filter { $0.method == "session.import" }
        #expect(imports.last?.connection == 1)
        #expect(Set(imports.map(\.commandID)).count == 1)
        let call = try #require(imports.last)
        #expect(call.commandID?.isEmpty == false)
        #expect(call.params?["projectId"] == .string("p"))
        #expect(call.params?["harness"] == .string("claude"))
        #expect(call.params?["nativeId"] == .string("n1"))
        #expect(call.params?["model"] == .string("opus"))
        #expect(call.params?["mode"] == .string("plan"))
        // What's unset is left out: the agent's defaults, and its own title.
        #expect(call.params?["effort"] == nil && call.params?["title"] == nil)
    }

    @Test func searches() async throws {
        let server = try TestServer(index: [session("Fix the tests"), session("Flaky tests"), session("Docs")], search: .init(more: true))
        let c = try await connect(to: server)
        defer { c.stop(); server.stop() }
        #expect(c.searches)

        let r = try await c.search(query: "tests", archived: true, limit: 30)
        #expect(r.results.map(\.session.id) == ["Fix the tests", "Flaky tests"])
        #expect(r.results.first?.snippet.first?.isMatch == true)
        #expect(r.more == true && r.indexing != true)
        let call = try #require(server.calls.last { $0.method == "session.search" })
        #expect(call.commandID == nil)
        #expect(call.params == .object(["query": .string("tests"), "archived": .bool(true), "limit": .number(30)]))

        // What isn't asked for is left out: rad's defaults.
        _ = try await c.search(query: "docs")
        #expect(server.calls.last { $0.method == "session.search" }?.params == .object(["query": .string("docs")]))
    }

    @Test func olderServerDoesntSearch() async throws {
        let server = try TestServer()
        let c = try await connect(to: server)
        defer { c.stop(); server.stop() }
        #expect(c.info?.search == nil && !c.searches)
    }

    @Test func promptFailsOnceTheConnectionGivesUp() async throws {
        let server = try TestServer()
        let c = try await connect(to: server)
        defer { c.stop() }
        let store = c.sessionStore("s1")

        server.stop()
        await #expect(throws: RPCError.self) { try await store.send("hello") }
        #expect(failed(c))
        #expect(store.outbox.isEmpty)
    }
}
