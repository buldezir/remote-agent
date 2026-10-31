import Foundation
import Observation

public enum ConnectionState: Equatable, Sendable {
    case idle
    case connecting
    case connected
    case failed(String)
}

/// A live connection to one server: keeps the index (projects + sessions)
/// in sync, owns per-session stores, and reconnects with backoff. It stays
/// `.connecting` while it tries, and gives up after `maxAttempts` failures
/// in a row: `.failed` until `start()` or `retry()`. A socket that has been
/// silent for `heartbeat` is pinged, and dropped if it doesn't answer, so
/// one that died while the Mac slept doesn't pass for connected.
@MainActor @Observable
public final class ServerConnection {
    /// Failed attempts in a row before the connection gives up.
    public static let maxAttempts = 5
    public let server: SavedServer
    public private(set) var state: ConnectionState = .idle
    public private(set) var info: ServerInfo?
    public private(set) var projects: [String: Project] = [:]
    public private(set) var sessions: [String: Session] = [:]
    /// The agents installed on the server; nil until first loaded.
    public private(set) var harnesses: [HarnessInfo]?
    public private(set) var indexSynced = false

    private let client: RPCClient
    private var indexSeq: Int64 = 0
    private var indexLive = false
    /// As SessionStore's: index subscriptions not yet synchronized.
    private var indexOutstanding = 0
    @ObservationIgnored private var sessionStores: [String: SessionStore] = [:]
    @ObservationIgnored let drafts: Drafts
    private var wanted = false
    private var runTask: Task<Void, Never>?
    /// The wait after the first failed attempt, doubling after each one.
    @ObservationIgnored var firstRetryDelay: Double = 1
    /// How long a socket may stay silent before it's pinged.
    @ObservationIgnored var heartbeat: Duration = .seconds(15)
    /// Seconds to wait for a pong.
    @ObservationIgnored var pingTimeout: Double = 5
    private var messagesTask: Task<Void, Never>?
    private let onURLWorked: (URL) -> Void
    /// Called with a session in the index that changed, before and after:
    /// live, or while this device was away, in the replay after reconnecting.
    /// Not for the sessions the first sync brings.
    @ObservationIgnored public var onSessionChange: ((_ old: Session, _ new: Session) -> Void)?

    init(server: SavedServer, token: String, defaults: UserDefaults = .standard, onURLWorked: @escaping (URL) -> Void) {
        self.server = server
        self.client = RPCClient(token: token)
        self.drafts = Drafts(serverID: server.id, defaults: defaults)
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
        var delay = firstRetryDelay
        var failures = 0
        while !Task.isCancelled && wanted {
            if await client.isConnected {
                try? await Task.sleep(for: min(.seconds(2), heartbeat))
                // After a sleep the silence is at least as long, so the
                // first check after waking pings.
                if await client.silence >= heartbeat {
                    await client.ping(timeout: pingTimeout)
                }
                continue
            }
            state = .connecting
            do {
                info = try await client.connect(baseURLs: server.urls)
                if let url = await client.connectedURL { onURLWorked(url) }
                state = .connected
                delay = firstRetryDelay
                failures = 0
                await resubscribeAll()
                Task { await refreshHarnesses() }
            } catch {
                if Task.isCancelled { return }
                failures += 1
                // Trying again won't bring back a revoked token.
                let unauthorized = (error as? RPCError)?.code == "unauthorized"
                if unauthorized || failures >= Self.maxAttempts {
                    state = .failed(unauthorized ? "This device is no longer authorized; pair again." : error.localizedDescription)
                    runTask = nil
                    return
                }
                try? await Task.sleep(for: .seconds(delay))
                delay *= 2
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
                // A reset drops what was cached, but the replay is still
                // compared with it.
                let before = sessions
                if reset { projects = [:]; sessions = [:] }
                let buffered = indexReplay
                indexReplay = []
                let changes = buffered.compactMap { upsertIndex($0, before: before) }
                indexSeq = seq
                indexOutstanding = max(0, indexOutstanding - 1)
                indexLive = indexOutstanding == 0
                indexSynced = true
                for (id, s) in sessions { sessionStores[id]?.session = sessionStores[id]?.session ?? s }
                drafts.keep { sessions[$0] != nil }
                report(changes)
            } else if let s = store(forStream: stream) {
                s.synchronized(seq: seq, reset: reset)
            }
        case .resync(let stream):
            Task { await resubscribe(stream) }
        case .closed:
            indexLive = false
            indexOutstanding = 0
            for s in sessionStores.values { s.closed() }
            // Not after pause(): the socket closed because we closed it.
            if wanted && runTask != nil { state = .connecting }
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
        var changes: [SessionChange] = []
        defer { report(changes) }
        for ev in events {
            if ev.seq <= indexSeq { continue }
            if ev.seq != indexSeq + 1 {
                indexLive = false
                Task { await subscribeIndex() }
                return
            }
            indexSeq = ev.seq
            if let c = upsertIndex(ev) { changes.append(c) }
        }
    }

    private typealias SessionChange = (old: Session, new: Session)

    /// Applies an index event, and returns the session it changed. A session
    /// is compared with the one cached, else with the one in `before`.
    private func upsertIndex(_ ev: Event, before: [String: Session] = [:]) -> SessionChange? {
        switch ev.type {
        case "project.upserted": if let p = ev.project { projects[p.id] = p }
        case "project.removed": if let id = ev.id { projects[id] = nil }
        case "session.upserted":
            if let s = ev.session {
                let old = sessions[s.id] ?? before[s.id]
                sessions[s.id] = s
                if let old, old != s { return (old, s) }
            }
        case "session.removed":
            if let id = ev.id {
                sessions[id] = nil
                drafts[id] = ""
            }
        default: break
        }
        return nil
    }

    /// Called once the batch they came in is applied, so the index is whole.
    private func report(_ changes: [SessionChange]) {
        for c in changes { onSessionChange?(c.old, c.new) }
    }

    private func subscribeIndex() async {
        indexOutstanding += 1
        indexLive = false
        indexReplay = []
        let res: SubscribeResult? = try? await client.call("subscribe", SubscribeParams(stream: "index", afterSeq: indexSeq))
        if res == nil { indexOutstanding = max(0, indexOutstanding - 1) }
    }

    func subscribe(_ store: SessionStore) async {
        store.beginReplay()
        let res: SubscribeResult? = try? await client.call("subscribe", SubscribeParams(stream: "session:\(store.sessionID)", afterSeq: store.lastSeq))
        if res == nil { store.subscriptionFailed() }
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

    public func call<P: Encodable & Sendable, R: Decodable & Sendable>(_ method: String, _ params: P, timeout: Double = 30) async throws -> R {
        try await client.call(method, params, timeout: timeout)
    }

    /// Calls a command that carries a commandId until it gets through: when
    /// the socket drops or the server doesn't answer, it waits for the
    /// connection to come back and sends it again. rad applies a commandId
    /// once, so a repeat of one that got there returns the first result. It
    /// throws once the connection gives up or stops.
    func deliver<P: Encodable & Sendable, R: Decodable & Sendable>(_ method: String, _ params: P,
                                                                   timeout: Double = 30) async throws -> R {
        var attempts = 0
        while true {
            try await waitUntilConnected()
            attempts += 1
            do {
                return try await client.call(method, params, timeout: timeout)
            } catch let e as RPCError where attempts < Self.maxAttempts
                        && (e.code == RPCError.disconnected.code || e.code == RPCError.timeout.code) {
                continue
            }
        }
    }

    /// Returns once connected, or throws after `limit`.
    private func waitUntilConnected(for limit: Duration) async throws {
        try await withThrowingTaskGroup(of: Void.self) { group in
            group.addTask { try await self.waitUntilConnected() }
            group.addTask {
                try await Task.sleep(for: limit)
                throw RPCError.disconnected
            }
            defer { group.cancelAll() }
            try await group.next()
        }
    }

    /// Returns once connected. While paused (the app is in the background)
    /// it waits for the app to come back.
    private func waitUntilConnected() async throws {
        while true {
            if !wanted { throw RPCError.disconnected }
            if case .failed = state { throw RPCError.disconnected }
            // The socket goes before `state` hears that it closed.
            if state == .connected, await client.isConnected { return }
            try await Task.sleep(for: .milliseconds(100))
        }
    }

    public func refreshHarnesses(force: Bool = false) async {
        struct Res: Decodable { var harnesses: [HarnessInfo] }
        if let r: Res = try? await client.call("harness.list", ["refresh": force], timeout: 45) {
            harnesses = r.harnesses
        }
    }

    public func harness(_ id: String) -> HarnessInfo? { harnesses?.first { $0.id == id } }

    /// Uploads an image to attach to a prompt.
    public func uploadImage(_ data: Data) async throws -> ImageRef {
        let ref = try await client.uploadImage(data)
        await ImageCache.shared.store(data, for: ref.id)
        return ref
    }

    /// An image's bytes, from the cache or the server.
    public func imageData(_ id: String) async throws -> Data {
        try await ImageCache.shared.data(id) { [client] in try await client.imageData(id) }
    }

    /// An image as a file, for Quick Look and sharing.
    public func imageFile(_ id: String) async throws -> URL {
        try await ImageCache.shared.namedFile(id) { [client] in try await client.imageData(id) }
    }

    /// The session's shell on the server, for one view to show.
    public func terminal(sessionID: String) -> TerminalSession {
        TerminalSession(connection: self, sessionID: sessionID)
    }

    func terminalTask(_ query: [URLQueryItem]) async throws -> URLSessionWebSocketTask {
        try await client.terminalTask(query: query)
    }

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
        public var effort: String?
        public var mode: String?
        public var workspace: WorkspaceParams
        public var prompt: String
        /// Ids of uploaded images (`uploadImage`) that go with the prompt.
        public var images: [String]?
        /// Keep the prompt and images as the session's draft, and start no
        /// turn. Only for a server whose `info.drafts` is true.
        public var draft: Bool?

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

        public init(projectId: String, harness: String, model: String?, effort: String? = nil, mode: String?,
                    workspace: WorkspaceParams, prompt: String, images: [ImageRef] = [], draft: Bool = false) {
            self.projectId = projectId
            self.harness = harness
            self.model = model
            self.effort = effort
            self.mode = mode
            self.workspace = workspace
            self.prompt = prompt
            self.images = images.isEmpty ? nil : images.map(\.id)
            self.draft = draft ? true : nil
        }
    }

    public func createSession(_ p: NewSession) async throws -> Session {
        let s: Session = try await deliver("session.create", p, timeout: 90)
        sessions[s.id] = s
        return s
    }

    /// A session that continues another after one of its turns. What is left
    /// nil is the source's: its last turn that ended, its agent, model,
    /// effort, mode and kind of workspace. Only for a server whose
    /// `info.forks` is true.
    public struct ForkSession: Encodable, Sendable {
        public var commandId = UUID().uuidString
        public var sessionId: String
        public var turn: Int?
        public var harness: String?
        public var model: String?
        public var effort: String?
        public var mode: String?
        /// A worktree starts with the files as they were after the turn, the
        /// project folder as it is. `baseRef` isn't taken.
        public var workspace: NewSession.WorkspaceParams?
        public var prompt: String?
        /// Ids of uploaded images (`uploadImage`) that go with the prompt.
        public var images: [String]?
        public var title: String?
        public var draft: Bool?

        public init(sessionId: String, turn: Int? = nil, harness: String? = nil, model: String? = nil,
                    effort: String? = nil, mode: String? = nil, workspace: NewSession.WorkspaceParams? = nil,
                    prompt: String = "", images: [ImageRef] = [], title: String? = nil, draft: Bool = false) {
            self.sessionId = sessionId
            self.turn = turn
            self.harness = harness
            self.model = model
            self.effort = effort
            self.mode = mode
            self.workspace = workspace
            self.prompt = prompt.isEmpty ? nil : prompt
            self.images = images.isEmpty ? nil : images.map(\.id)
            self.title = title
            self.draft = draft ? true : nil
        }
    }

    /// Forks a session (`session.fork`), as `createSession` creates one.
    public func fork(_ p: ForkSession) async throws -> Session {
        let s: Session = try await deliver("session.fork", p, timeout: 90)
        sessions[s.id] = s
        return s
    }

    // MARK: Picking up the agent's own sessions

    /// The sessions an agent ran in the project's folder outside rad, such as
    /// in a terminal, the newest first. Only for an agent whose
    /// `caps.import` is true. It reads the agent's files, which may take a
    /// while the first time.
    public func importable(projectID: String, harness: String) async throws -> [ImportableSession] {
        struct Res: Decodable, Sendable { var sessions: [ImportableSession]? }
        let r: Res = try await client.call("session.importable", ["projectId": projectID, "harness": harness], timeout: 60)
        return r.sessions ?? []
    }

    /// A session of the agent's own (`importable`) to pick up. Left nil, the
    /// model, effort and mode are the agent's defaults.
    public struct ImportSession: Encodable, Sendable {
        public var commandId = UUID().uuidString
        public var projectId: String
        public var harness: String
        /// The agent's id for it: `ImportableSession.id`.
        public var nativeId: String
        public var model: String?
        public var effort: String?
        public var mode: String?
        public var title: String?

        public init(projectId: String, harness: String, nativeId: String, model: String? = nil,
                    effort: String? = nil, mode: String? = nil, title: String? = nil) {
            self.projectId = projectId
            self.harness = harness
            self.nativeId = nativeId
            self.model = model
            self.effort = effort
            self.mode = mode
            self.title = title
        }
    }

    /// Picks up one of the agent's own sessions (`session.import`), as
    /// `createSession` creates one: a session in the project folder with the
    /// conversation so far, whose next prompt goes on with it.
    public func importSession(_ p: ImportSession) async throws -> Session {
        let s: Session = try await deliver("session.import", p, timeout: 90)
        sessions[s.id] = s
        return s
    }

    /// Keeps a prompt in the session to send later, from any device, in place
    /// of the draft it had; no text and no images remove it. The session the
    /// server returns goes into the index at once, so the views change before
    /// its event comes. Saving again is harmless, so a dropped call is made
    /// again once the connection is back.
    @discardableResult
    public func saveDraft(_ sessionID: String, text: String, images: [ImageRef] = []) async throws -> Session {
        struct P: Encodable, Sendable { var sessionId: String; var text: String; var images: [String]? }
        let s: Session = try await deliver("session.saveDraft", P(sessionId: sessionID, text: text,
                                                                  images: images.isEmpty ? nil : images.map(\.id)))
        sessions[s.id] = s
        sessionStores[s.id]?.session = s
        return s
    }

    public func archive(_ sessionID: String, removeWorktree: Bool) async throws {
        struct P: Encodable, Sendable { var sessionId: String; var removeWorktree: Bool }
        let _: Empty = try await client.call("session.archive", P(sessionId: sessionID, removeWorktree: removeWorktree))
    }

    /// Whether the server renames sessions (`session.rename`).
    public var renames: Bool { info?.rename == true }

    /// Gives the session the user's title, which rad puts on one line and
    /// cuts at 120 characters, and its naming then leaves alone. "" gives the
    /// session back to rad, which names it from its first prompt again. The
    /// title comes to the views with the session's event. Only for a server
    /// whose `info.rename` is true.
    @discardableResult
    public func rename(_ sessionID: String, title: String) async throws -> Session {
        struct P: Encodable, Sendable { var sessionId: String; var title: String }
        return try await client.call("session.rename", P(sessionId: sessionID, title: title))
    }

    /// Marks a session read: here at once, so its row loses its mark, and on
    /// the server, which tells the other devices. A session that isn't unread
    /// is left alone.
    public func markRead(_ sessionID: String) {
        guard sessions[sessionID]?.isUnread == true else { return }
        sessions[sessionID]?.unread = false
        Task {
            struct P: Encodable, Sendable { var sessionId: String }
            // An older server doesn't know it, and a failed call is made again
            // when the index next says the session is unread.
            let _: Empty? = try? await client.call("session.markRead", P(sessionId: sessionID))
        }
    }

    /// Deletes a session and its transcript for good; the index then drops it.
    public func delete(_ sessionID: String, removeWorktree: Bool) async throws {
        struct P: Encodable, Sendable { var sessionId: String; var removeWorktree: Bool }
        let _: Empty = try await client.call("session.delete", P(sessionId: sessionID, removeWorktree: removeWorktree))
    }

    // MARK: Server management

    public func settings() async throws -> ServerSettings {
        try await client.call("server.settings")
    }

    /// Replaces the server's settings; returns them as the server keeps them.
    public func setSettings(_ settings: ServerSettings) async throws -> ServerSettings {
        try await client.call("server.setSettings", settings)
    }

    /// Whether rad has an update; `refresh` asks GitHub again.
    public func checkUpdate(refresh: Bool = false) async throws -> UpdateInfo {
        try await client.call("server.checkUpdate", ["refresh": refresh], timeout: 45)
    }

    /// Installs rad's latest release, which restarts it.
    public func updateServer() async throws -> ServerTask {
        try await client.call("server.update")
    }

    public func updateHarness(_ id: String) async throws -> ServerTask {
        try await client.call("harness.update", ["harness": id])
    }

    public func loginHarness(_ id: String) async throws -> ServerTask {
        try await client.call("harness.login", ["harness": id])
    }

    /// The tasks that run on the server, and those that ended lately: the
    /// server's own, or with a session, that session's (its commits).
    public func tasks(sessionID: String? = nil) async throws -> [ServerTask] {
        struct Res: Decodable, Sendable { var tasks: [ServerTask] }
        struct P: Encodable, Sendable { var sessionId: String? }
        let r: Res = sessionID == nil ? try await client.call("task.list") : try await client.call("task.list", P(sessionId: sessionID))
        return r.tasks
    }

    // MARK: Searching

    /// Whether the server searches sessions (`session.search`).
    public var searches: Bool { info?.search == true }

    /// The sessions that match `query`, by what was said in them, their title
    /// or their branch, the best first: archived ones too with `archived`,
    /// and at most `limit` (rad's default without). Only for a server whose
    /// `info.search` is true.
    public func search(query: String, archived: Bool = false, limit: Int? = nil) async throws -> SearchResults {
        struct P: Encodable, Sendable { var query: String; var archived: Bool?; var limit: Int? }
        return try await client.call("session.search", P(query: query, archived: archived ? true : nil, limit: limit), timeout: 15)
    }

    // MARK: Committing

    /// Whether the server commits, pushes and opens pull requests.
    public var commits: Bool { info?.commit == true }

    /// What a commit would take in the session, with a message the system
    /// agent writes, which may take up to a minute.
    public func commitPreview(sessionID: String) async throws -> CommitPreview {
        try await client.call("git.commitPreview", ["sessionId": sessionID], timeout: 90)
    }

    /// How far a commit goes.
    public enum CommitScope: String, CaseIterable, Sendable {
        case commit, push, pullRequest

        /// Where a commit starts: `saved`, the last one chosen, where the
        /// preview allows it, else as far as a push. With nothing to commit a
        /// commit becomes a push, and so does a pull request the branch can't
        /// open, such as one chosen last on another branch.
        public static func initial(saved: String, for preview: CommitPreview) -> CommitScope {
            let choices = preview.scopes
            let wanted = CommitScope(rawValue: saved) ?? (preview.remote != nil ? .push : .commit)
            if choices.contains(wanted) { return wanted }
            if choices.contains(.push) { return .push }
            return choices.first ?? .commit
        }
    }

    struct CommitParams: Encodable, Sendable {
        var sessionId: String
        var message: String?
        var push: Bool?
        var pullRequest: Bool?
    }

    /// Commits the session's changes, then pushes and opens a pull request as
    /// far as `scope` says: a task to follow. An empty message has rad
    /// write one.
    public func commit(sessionID: String, message: String, scope: CommitScope) async throws -> ServerTask {
        let text = message.trimmingCharacters(in: .whitespacesAndNewlines)
        let p = CommitParams(sessionId: sessionID, message: text.isEmpty ? nil : text,
                             push: scope == .push ? true : nil, pullRequest: scope == .pullRequest ? true : nil)
        return try await client.call("git.commit", p)
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

extension ServerConnection: TaskSource {
    public func task(_ id: String) async throws -> ServerTask {
        try await client.call("task.get", ["id": id])
    }

    /// Waits for the connection, which iOS drops while the user is in the
    /// browser signing in, but sends once: again would type the line twice.
    public func taskInput(_ id: String, text: String) async throws {
        try await waitUntilConnected(for: .seconds(10))
        let _: Empty = try await client.call("task.input", ["id": id, "text": text])
    }

    public func cancelTask(_ id: String) async throws {
        try await waitUntilConnected(for: .seconds(10))
        let _: Empty = try await client.call("task.cancel", ["id": id])
    }

    /// rad may take longer to restart than the connection keeps trying.
    public func reviveConnection() {
        if case .failed = state { retry() }
    }

    public var serverVersion: String? { info?.version }

    public func taskEnded(_ task: ServerTask) async {
        await refreshHarnesses(force: true)
    }
}

extension ServerConnection {
    func _testHandle(_ msg: ServerMessage) { handle(msg) }
}

struct SubscribeParams: Encodable, Sendable {
    var stream: String
    var afterSeq: Int64
}

struct SubscribeResult: Decodable, Sendable {
    var stream: String
    var seq: Int64
}

extension CommitPreview {
    /// How far a commit can go: committing needs changes, pushing a branch
    /// and a remote, and a pull request a branch that can open one.
    public var scopes: [ServerConnection.CommitScope] {
        var out: [ServerConnection.CommitScope] = files.isEmpty ? [] : [.commit]
        if remote != nil && branch != nil {
            out.append(.push)
            if noPullRequest == nil { out.append(.pullRequest) }
        }
        return out
    }
}
