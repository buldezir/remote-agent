import Foundation
import Observation

public enum ConnectionState: Equatable, Sendable {
    case idle
    case connecting
    case connected
    case failed(String)
}

/// A live connection to one server: keeps the index (projects + sessions)
/// in sync, owns per-session stores, and reconnects with backoff.
@MainActor @Observable
public final class ServerConnection {
    public let server: SavedServer
    public private(set) var state: ConnectionState = .idle
    public private(set) var info: ServerInfo?
    public private(set) var projects: [String: Project] = [:]
    public private(set) var sessions: [String: Session] = [:]
    public private(set) var harnesses: [HarnessInfo] = []
    public private(set) var indexSynced = false

    private let client: RPCClient
    private var indexSeq: Int64 = 0
    private var indexLive = false
    @ObservationIgnored private var sessionStores: [String: SessionStore] = [:]
    private var wanted = false
    private var runTask: Task<Void, Never>?
    private var messagesTask: Task<Void, Never>?
    private let onURLWorked: (URL) -> Void

    init(server: SavedServer, token: String, onURLWorked: @escaping (URL) -> Void) {
        self.server = server
        self.client = RPCClient(token: token)
        self.onURLWorked = onURLWorked
        messagesTask = Task { [weak self, client] in
            for await msg in client.messages {
                self?.handle(msg)
            }
        }
    }

    // MARK: Lifecycle

    public func start() {
        wanted = true
        guard runTask == nil else { return }
        runTask = Task { [weak self] in await self?.runLoop() }
    }

    public func stop() {
        wanted = false
        pause()
    }

    /// Stops for good, revoking this device on the server first if reachable.
    func unpairAndStop() {
        wanted = false
        runTask?.cancel()
        runTask = nil
        state = .idle
        Task { [client, server] in
            if await !client.isConnected {
                _ = try? await client.connect(baseURLs: server.urls)
            }
            let _: Empty? = try? await client.call("device.unpair", timeout: 5)
            await client.disconnect()
        }
    }

    func pause() {
        runTask?.cancel()
        runTask = nil
        Task { await client.disconnect() }
        state = .idle
    }

    func resumeIfWanted() {
        if wanted { start() }
    }

    /// Reconnect now (e.g. pull-to-refresh after a failure).
    public func retry() {
        runTask?.cancel()
        runTask = nil
        start()
    }

    private func runLoop() async {
        var delay: Double = 1
        while !Task.isCancelled && wanted {
            if await client.isConnected {
                try? await Task.sleep(for: .seconds(2))
                continue
            }
            state = .connecting
            do {
                info = try await client.connect(baseURLs: server.urls)
                if let url = await client.connectedURL { onURLWorked(url) }
                state = .connected
                delay = 1
                await resubscribeAll()
                Task { await refreshHarnesses() }
            } catch {
                if Task.isCancelled { return }
                let msg = (error as? RPCError)?.code == "unauthorized" ? "This device is no longer authorized; pair again." : error.localizedDescription
                state = .failed(msg)
                try? await Task.sleep(for: .seconds(delay))
                delay = min(delay * 2, 30)
            }
        }
    }

    private func resubscribeAll() async {
        await subscribeIndex()
        for store in sessionStores.values where store.isActive {
            await subscribe(store)
        }
    }

    // MARK: Messages

    private func handle(_ msg: ServerMessage) {
        switch msg {
        case .events(let stream, let events):
            if stream == "index" { applyIndex(events) }
            else if let s = store(forStream: stream) { s.apply(events) }
        case .synchronized(let stream, let seq, let reset):
            if stream == "index" {
                if reset { projects = [:]; sessions = [:] }
                let buffered = indexReplay
                indexReplay = []
                for ev in buffered { upsertIndex(ev) }
                indexSeq = seq
                indexLive = true
                indexSynced = true
                for (id, s) in sessions { sessionStores[id]?.session = sessionStores[id]?.session ?? s }
            } else if let s = store(forStream: stream) {
                s.synchronized(seq: seq, reset: reset)
            }
        case .resync(let stream):
            Task { await resubscribe(stream) }
        case .closed:
            indexLive = false
            for s in sessionStores.values { s.live = false }
            if wanted { state = .connecting }
        }
    }

    private func store(forStream stream: String) -> SessionStore? {
        guard stream.hasPrefix("session:") else { return nil }
        return sessionStores[String(stream.dropFirst("session:".count))]
    }

    private var indexReplay: [Event] = []

    private func applyIndex(_ events: [Event]) {
        guard indexLive else {
            indexReplay.append(contentsOf: events)
            return
        }
        for ev in events {
            if ev.seq <= indexSeq { continue }
            if ev.seq != indexSeq + 1 {
                indexLive = false
                Task { await subscribeIndex() }
                return
            }
            indexSeq = ev.seq
            upsertIndex(ev)
        }
    }

    private func upsertIndex(_ ev: Event) {
        switch ev.type {
        case "project.upserted": if let p = ev.project { projects[p.id] = p }
        case "project.removed": if let id = ev.id { projects[id] = nil }
        case "session.upserted": if let s = ev.session { sessions[s.id] = s }
        case "session.removed": if let id = ev.id { sessions[id] = nil }
        default: break
        }
    }

    private func subscribeIndex() async {
        indexLive = false
        indexReplay = []
        let _: SubscribeResult? = try? await client.call("subscribe", SubscribeParams(stream: "index", afterSeq: indexSeq))
    }

    func subscribe(_ store: SessionStore) async {
        store.beginReplay()
        let _: SubscribeResult? = try? await client.call("subscribe", SubscribeParams(stream: "session:\(store.sessionID)", afterSeq: store.lastSeq))
    }

    private func resubscribe(_ stream: String) async {
        if stream == "index" { await subscribeIndex() }
        else if let s = store(forStream: stream) { await subscribe(s) }
    }

    // MARK: Session stores

    /// Returns the store for a session, subscribing while it's in use.
    public func sessionStore(_ id: String) -> SessionStore {
        if let s = sessionStores[id] { return s }
        let s = SessionStore(sessionID: id, connection: self)
        s.session = sessions[id]
        sessionStores[id] = s
        return s
    }

    func activate(_ store: SessionStore) {
        guard state == .connected else { return }
        Task { await subscribe(store) }
    }

    func deactivate(_ store: SessionStore) {
        Task { let _: Empty? = try? await client.call("unsubscribe", ["stream": "session:\(store.sessionID)"]) }
    }

    // MARK: API

    public func call<P: Encodable & Sendable, R: Decodable & Sendable>(_ method: String, _ params: P) async throws -> R {
        try await client.call(method, params)
    }

    public func refreshHarnesses(force: Bool = false) async {
        struct Res: Decodable { var harnesses: [HarnessInfo] }
        if let r: Res = try? await client.call("harness.list", ["refresh": force], timeout: 45) {
            harnesses = r.harnesses
        }
    }

    public func harness(_ id: String) -> HarnessInfo? { harnesses.first { $0.id == id } }

    public func listDirectory(_ path: String?) async throws -> FSListing {
        try await client.call("fs.list", ["path": path ?? ""])
    }

    public func addProject(path: String) async throws -> Project {
        let p: Project = try await client.call("project.add", ["path": path])
        projects[p.id] = p
        return p
    }

    public func removeProject(_ id: String) async throws {
        let _: Empty = try await client.call("project.remove", ["id": id])
    }

    public func branches(projectID: String) async throws -> (branches: [String], current: String) {
        struct Res: Decodable { var branches: [String]?; var current: String? }
        let r: Res = try await client.call("git.branches", ["projectId": projectID])
        return (r.branches ?? [], r.current ?? "")
    }

    public struct NewSession: Encodable, Sendable {
        public var commandId = UUID().uuidString
        public var projectId: String
        public var harness: String
        public var model: String?
        public var mode: String?
        public var workspace: WorkspaceParams
        public var prompt: String

        public struct WorkspaceParams: Encodable, Sendable {
            public var kind: Workspace.Kind
            public var branch: String?
            public var baseRef: String?
            public init(kind: Workspace.Kind, branch: String? = nil, baseRef: String? = nil) {
                self.kind = kind
                self.branch = branch
                self.baseRef = baseRef
            }
        }

        public init(projectId: String, harness: String, model: String?, mode: String?, workspace: WorkspaceParams, prompt: String) {
            self.projectId = projectId
            self.harness = harness
            self.model = model
            self.mode = mode
            self.workspace = workspace
            self.prompt = prompt
        }
    }

    public func createSession(_ p: NewSession) async throws -> Session {
        let s: Session = try await client.call("session.create", p, timeout: 90)
        sessions[s.id] = s
        return s
    }

    public func archive(_ sessionID: String, removeWorktree: Bool) async throws {
        struct P: Encodable, Sendable { var sessionId: String; var removeWorktree: Bool }
        let _: Empty = try await client.call("session.archive", P(sessionId: sessionID, removeWorktree: removeWorktree))
    }

    // MARK: Derived

    public var activeSessions: [Session] {
        sessions.values.filter { !$0.archived }.sorted { $0.updatedAt > $1.updatedAt }
    }

    public var archivedSessions: [Session] {
        sessions.values.filter(\.archived).sorted { $0.updatedAt > $1.updatedAt }
    }

    public var sortedProjects: [Project] {
        projects.values.sorted { $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending }
    }
}

struct SubscribeParams: Encodable, Sendable {
    var stream: String
    var afterSeq: Int64
}

struct SubscribeResult: Decodable, Sendable {
    var stream: String
    var seq: Int64
}
