import Foundation
import Testing
@testable import RAKit

@MainActor
struct SessionSearchTests {
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

    /// A session as the index sends it.
    private func session(_ id: String, title: String) -> String {
        #"{"id":"\#(id)","projectId":"p","harness":"fake","status":"idle","title":"\#(title)","archived":false,"#
            + #""workspace":{"kind":"root","path":"/tmp/p"},"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}"#
    }

    private func connect(_ server: TestServer, id: String, name: String) async throws -> ServerConnection {
        let url = try await server.start()
        let saved = SavedServer(id: id, name: name, urls: [url], deviceId: "dev1", addedAt: .now)
        let c = ServerConnection(server: saved, token: "t", defaults: UserDefaults(suiteName: "dev.remote-agent.SessionSearchTests")!) { _ in }
        c.start()
        try await wait { c.state == .connected }
        return c
    }

    private func search(_ connections: [ServerConnection]) -> SessionSearch {
        let s = SessionSearch { connections }
        s.debounce = .milliseconds(10)
        return s
    }

    private func searches(_ server: TestServer) -> [String] {
        server.calls.filter { $0.method == "session.search" }.compactMap { $0.params?["query"]?.stringValue }
    }

    private func hit(_ server: String, _ id: String) -> SessionSearch.Hit {
        let s = Session(id: id, projectId: "p", harness: "fake", workspace: Workspace(kind: .root, path: "/tmp/p"),
                        status: .idle, title: id, archived: false, createdAt: .now, updatedAt: .now)
        return SessionSearch.Hit(serverId: server, result: SearchResult(session: s))
    }

    @Test func mergesServersInTurn() {
        let a = [hit("a", "1"), hit("a", "2"), hit("a", "3")]
        let b = [hit("b", "1")]
        let c = [hit("c", "1"), hit("c", "2")]
        // Every server's first, then every second, each server's order kept.
        #expect(SessionSearch.mergeSearchResults([a, b, c]).map(\.id) == ["a/1", "b/1", "c/1", "a/2", "c/2", "a/3"])
        #expect(SessionSearch.mergeSearchResults([[], b]).map(\.id) == ["b/1"])
        #expect(SessionSearch.mergeSearchResults([]).isEmpty)
    }

    @Test func waitsForTwoCharacters() async throws {
        let server = try TestServer(index: [session("s1", title: "Fix the tests")], search: .init())
        let c = try await connect(server, id: "a", name: "Studio")
        defer { c.stop(); server.stop() }
        let s = search([c])

        s.query = " f "
        #expect(!s.isActive && !s.isSearching)
        try await Task.sleep(for: .milliseconds(60))
        #expect(searches(server).isEmpty)

        s.query = "fi"
        #expect(s.isActive && s.isSearching)
        try await wait { !s.isSearching }
        #expect(searches(server) == ["fi"])
        #expect(s.hits.map(\.id) == ["a/s1"])
        #expect(s.answered && !s.nothingFound)
        let call = try #require(server.calls.last { $0.method == "session.search" })
        #expect(call.params == .object(["query": .string("fi"), "archived": .bool(true), "limit": .number(30)]))

        // The space before the next word: the same search.
        s.query = "fi "
        #expect(!s.isSearching)
        try await Task.sleep(for: .milliseconds(60))
        #expect(searches(server) == ["fi"])
        #expect(s.hits.map(\.id) == ["a/s1"])

        // Shorter again: the rows come back.
        s.query = "f"
        #expect(s.hits.isEmpty && !s.isSearching && !s.answered)
    }

    @Test func coalescesTypingAndDropsStaleAnswers() async throws {
        let server = try TestServer(index: [session("s1", title: "Fix the tests"), session("s2", title: "Flaky build")],
                                    search: .init(delays: ["fix": 0.4]))
        let c = try await connect(server, id: "a", name: "Studio")
        defer { c.stop(); server.stop() }
        let s = search([c])
        s.debounce = .milliseconds(100)

        // Typed quickly: one search, for the last.
        s.query = "fi"
        s.query = "fix"
        try await wait { !searches(server).isEmpty }
        #expect(searches(server) == ["fix"])

        // A new query while the old one is out: the old answer never shows.
        s.query = "flaky"
        try await wait { !s.isSearching }
        #expect(s.hits.map(\.result.session.id) == ["s2"])
        try await Task.sleep(for: .milliseconds(500))
        #expect(s.hits.map(\.result.session.id) == ["s2"])
        #expect(searches(server) == ["fix", "flaky"])

        // Without archived sessions.
        s.includeArchived = false
        try await wait { searches(server).count == 3 }
        #expect(server.calls.last { $0.method == "session.search" }?.params == .object(["query": .string("flaky"), "limit": .number(30)]))
    }

    @Test func mergesAsServersAnswer() async throws {
        let a = try TestServer(index: [session("a1", title: "tests one"), session("a2", title: "tests two")], search: .init())
        let b = try TestServer(index: [session("b1", title: "tests three")], search: .init(delays: ["tests": 0.3], more: true, indexing: true))
        let ca = try await connect(a, id: "a", name: "Studio")
        let cb = try await connect(b, id: "b", name: "Laptop")
        defer { ca.stop(); cb.stop(); a.stop(); b.stop() }
        let s = search([ca, cb])

        s.query = "tests"
        // The first server's, while the second is still at it.
        try await wait { !s.hits.isEmpty }
        #expect(s.hits.map(\.id) == ["a/a1", "a/a2"])
        #expect(s.isSearching && !s.answered)
        try await wait { !s.isSearching }
        // In the order of the servers, in turn.
        #expect(s.hits.map(\.id) == ["a/a1", "b/b1", "a/a2"])
        #expect(s.more && s.indexing == ["Laptop"])
    }

    @Test func reportsAFailedServerWithTheOthersResults() async throws {
        let a = try TestServer(index: [session("a1", title: "Fix the tests")], search: .init())
        let b = try TestServer(index: [session("b1", title: "Fix more tests")], search: .init(error: "index is broken"))
        let ca = try await connect(a, id: "a", name: "Studio")
        let cb = try await connect(b, id: "b", name: "Laptop")
        defer { ca.stop(); cb.stop(); a.stop(); b.stop() }
        let s = search([ca, cb])

        s.query = "fix"
        try await wait { !s.isSearching }
        #expect(s.hits.map(\.id) == ["a/a1"])
        #expect(s.failures == [SessionSearch.Failure(server: "Laptop", message: "index is broken")])
        #expect(s.answered)

        // Nothing anywhere: one failed, the other found nothing.
        s.query = "nothing"
        try await wait { !s.isSearching }
        #expect(s.hits.isEmpty && s.nothingFound)
        #expect(s.failures.map(\.server) == ["Laptop"])
    }

    @Test func skipsServersThatCantSearch() async throws {
        let a = try TestServer(index: [session("a1", title: "Fix the tests")], search: .init())
        let old = try TestServer(index: [session("o1", title: "Fix it")])
        let ca = try await connect(a, id: "a", name: "Studio")
        let cold = try await connect(old, id: "o", name: "Old")
        // Never connected: left out, said nothing about.
        let off = ServerConnection(server: SavedServer(id: "x", name: "Away", urls: [URL(string: "http://127.0.0.1:1")!], deviceId: "d", addedAt: .now),
                                   token: "", defaults: UserDefaults(suiteName: "dev.remote-agent.SessionSearchTests")!) { _ in }
        defer { ca.stop(); cold.stop(); a.stop(); old.stop() }
        let s = search([ca, cold, off])

        s.query = "fix"
        try await wait { !s.isSearching }
        #expect(s.hits.map(\.id) == ["a/a1"])
        #expect(s.unsupported == ["Old"])
        #expect(s.failures.isEmpty)
        #expect(!old.calls.contains { $0.method == "session.search" })

        // No server that can: nothing searched, and no "no results" either.
        let only = search([cold])
        only.query = "fix"
        try await wait { !only.isSearching }
        #expect(only.unsupported == ["Old"] && only.hits.isEmpty && !only.nothingFound && !only.noServers)

        // None connected: it says so, rather than that nothing matched.
        let none = search([off])
        none.query = "fix"
        try await wait { !none.isSearching }
        #expect(none.noServers && !none.nothingFound && none.hits.isEmpty)
        none.query = ""
        #expect(!none.noServers)
    }
}
