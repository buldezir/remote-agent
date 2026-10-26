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
        return c
    }

    @Test func oldServerCommandFallback() async throws {
        let server = try TestServer()
        let connection = try await connect(to: server)
        defer { connection.stop(); server.stop() }
        let commands = try await connection.sessionStore("s").commands()
        #expect(commands.isEmpty)
    }

    @Test func marksReadOnce() async throws {
        func session(_ id: String, unread: Bool) -> String {
            #"{"id":"\#(id)","projectId":"p","harness":"fake","status":"idle","title":"\#(id)","archived":false,"unread":\#(unread),"#
                + #""workspace":{"kind":"root","path":"/tmp/p"},"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}"#
        }
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
