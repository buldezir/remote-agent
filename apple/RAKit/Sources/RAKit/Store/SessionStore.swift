import Foundation
import Observation

/// The live state of one session: its turns and transcript items.
@MainActor @Observable
public final class SessionStore {
    public let sessionID: String
    public var session: Session?
    public private(set) var turns: [String: Turn] = [:]
    public private(set) var items: [String: Item] = [:]
    /// Top-level transcript items in order (children of tool calls are excluded).
    public private(set) var transcript: [Item] = []
    public private(set) var synced = false
    /// Optimistic user messages not yet confirmed by the server, keyed by commandId.
    public private(set) var outbox: [String: Outgoing] = [:]

    /// A prompt on its way to the server.
    public struct Outgoing: Hashable, Sendable {
        public var text: String
        public var images: [ImageRef]
    }

    var lastSeq: Int64 = 0
    var live = false
    // Views showing the session. On iPad, two windows can show the same one.
    private var viewers = 0
    var isActive: Bool { viewers > 0 }
    private weak var connection: ServerConnection?
    private var children: [String: [Item]] = [:]

    init(sessionID: String, connection: ServerConnection?) {
        self.sessionID = sessionID
        self.connection = connection
    }

    /// Start receiving updates (call from onAppear). Updates stop when every
    /// activate() has had its deactivate().
    public func activate() {
        viewers += 1
        if viewers == 1 { connection?.activate(self) }
    }

    public func deactivate() {
        guard viewers > 0 else { return }
        viewers -= 1
        guard viewers == 0 else { return }
        live = false
        connection?.deactivate(self)
    }

    // MARK: Sync

    /// Replayed events are buffered until `synchronized`, then applied in one
    /// step (after dropping cached state if the server says reset).
    private var replay: [Event] = []

    func beginReplay() {
        live = false
        replay = []
    }

    func synchronized(seq: Int64, reset: Bool) {
        if reset {
            turns = [:]
            items = [:]
        }
        let buffered = replay
        replay = []
        for ev in buffered { upsert(ev) }
        lastSeq = seq
        live = true
        synced = true
        rebuild()
    }

    func apply(_ events: [Event]) {
        guard live else {
            replay.append(contentsOf: events)
            return
        }
        var changed = false
        for ev in events {
            if ev.seq <= lastSeq { continue }
            if ev.seq != lastSeq + 1 {
                // Gap: we missed something. Resubscribe from what we have.
                live = false
                if let c = connection { Task { await c.subscribe(self) } }
                break
            }
            lastSeq = ev.seq
            changed = upsert(ev) || changed
        }
        if changed { rebuild() }
    }

    @discardableResult
    private func upsert(_ ev: Event) -> Bool {
        switch ev.type {
        case "session.upserted": if let s = ev.session { session = s }
        case "turn.upserted": if let t = ev.turn { turns[t.id] = t }
        case "item.upserted":
            if let it = ev.item {
                items[it.id] = it
                return true
            }
        default: break
        }
        return false
    }

    private func rebuild() {
        var top: [Item] = []
        var kids: [String: [Item]] = [:]
        for it in items.values {
            if let p = it.parentItemId, items[p] != nil {
                kids[p, default: []].append(it)
            } else {
                top.append(it)
            }
        }
        top.sort { $0.order < $1.order }
        for k in kids.keys { kids[k]?.sort { $0.order < $1.order } }
        // Drop optimistic messages the server has confirmed.
        let confirmed = Set(top.filter { $0.kind == .userMessage }.map { Outgoing(text: $0.text ?? "", images: $0.images ?? []) })
        outbox = outbox.filter { !confirmed.contains($0.value) }
        transcript = top
        children = kids
    }

    public func children(of item: Item) -> [Item] { children[item.id] ?? [] }

    public var sortedTurns: [Turn] { turns.values.sorted { $0.n < $1.n } }

    public func turn(_ id: String?) -> Turn? { id.flatMap { turns[$0] } }

    public var pendingApprovals: [Item] {
        transcript.filter { $0.kind == .approval && $0.status == .pending }
    }

    public var isRunning: Bool {
        session?.status == .running || session?.status == .awaitingApproval
    }

    // MARK: Commands

    public func send(_ text: String, images: [ImageRef] = []) async throws {
        guard let c = connection else { throw RPCError.disconnected }
        let commandID = UUID().uuidString
        outbox[commandID] = Outgoing(text: text, images: images)
        struct P: Encodable, Sendable { var commandId: String; var sessionId: String; var text: String; var images: [String]? }
        do {
            let _: Item = try await c.call("session.prompt", P(commandId: commandID, sessionId: sessionID, text: text,
                                                               images: images.isEmpty ? nil : images.map(\.id)))
        } catch {
            outbox[commandID] = nil
            throw error
        }
    }

    public func interrupt(force: Bool = false) async throws {
        guard let c = connection else { throw RPCError.disconnected }
        struct P: Encodable, Sendable { var sessionId: String; var force: Bool }
        let _: Empty = try await c.call("session.interrupt", P(sessionId: sessionID, force: force))
    }

    public func respond(to approval: Item, option: ApprovalOption, message: String? = nil, answers: [String: String]? = nil) async throws {
        guard let c = connection else { throw RPCError.disconnected }
        struct P: Encodable, Sendable {
            var commandId: String; var sessionId: String; var approvalId: String; var optionId: String
            var message: String?; var answers: [String: String]?
        }
        let _: Empty = try await c.call("approval.respond", P(commandId: UUID().uuidString, sessionId: sessionID,
                                                              approvalId: approval.id, optionId: option.id, message: message, answers: answers))
    }

    /// An image's bytes, from the cache or the server.
    public func imageData(_ id: String) async throws -> Data {
        guard let c = connection else { throw RPCError.disconnected }
        return try await c.imageData(id)
    }

    /// An image as a file, for Quick Look and sharing.
    public func imageFile(_ id: String) async throws -> URL {
        guard let c = connection else { throw RPCError.disconnected }
        return try await c.imageFile(id)
    }

    /// The connection is up, so images can load.
    public var isConnected: Bool { connection?.state == .connected }

    public func setMode(_ mode: String) async throws {
        guard let c = connection else { throw RPCError.disconnected }
        let _: Empty = try await c.call("session.setMode", ["sessionId": sessionID, "mode": mode])
    }

    public func turnDiff(_ turnID: String) async throws -> Diff {
        guard let c = connection else { throw RPCError.disconnected }
        return try await c.call("git.turnDiff", ["turnId": turnID])
    }

    public func sessionDiff() async throws -> Diff {
        guard let c = connection else { throw RPCError.disconnected }
        return try await c.call("git.sessionDiff", ["sessionId": sessionID])
    }

    public func revert(toTurn n: Int) async throws -> [FileStat] {
        guard let c = connection else { throw RPCError.disconnected }
        struct P: Encodable, Sendable { var sessionId: String; var turn: Int }
        struct R: Decodable, Sendable { var files: [FileStat] }
        let r: R = try await c.call("git.revert", P(sessionId: sessionID, turn: n))
        return r.files
    }

    // MARK: Testing

    /// Feeds server messages directly (unit tests).
    public func _testApply(_ events: [Event]) { apply(events) }
    public func _testSynchronized(seq: Int64, reset: Bool) { synchronized(seq: seq, reset: reset) }
    public static func _testMake(_ id: String) -> SessionStore { SessionStore(sessionID: id, connection: nil) }
}
