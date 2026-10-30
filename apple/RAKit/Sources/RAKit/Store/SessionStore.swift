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
    /// The session was deleted on the server: its stream says so.
    public private(set) var removed = false
    /// Optimistic user messages not yet confirmed by the server, keyed by commandId.
    public private(set) var outbox: [String: Outgoing] = [:]
    /// The prompt being written and not sent yet. It outlives the session's
    /// view, and the app, but stays on this device.
    public var draft: String {
        didSet { connection?.drafts[sessionID] = draft }
    }

    /// A prompt on its way to the server.
    public struct Outgoing: Hashable, Sendable {
        public var text: String
        public var images: [ImageRef]
    }

    /// The session's draft (`Session.draft`), unless it is on its way: while
    /// `sendDraft` sends it, the outbox shows it instead.
    public var unsentDraft: Draft? {
        guard let d = (session ?? connection?.sessions[sessionID])?.draft else { return nil }
        return outbox.values.contains(Outgoing(text: d.text, images: d.images ?? [])) ? nil : d
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
        self.draft = connection?.drafts[sessionID] ?? ""
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

    /// Subscriptions asked for and not yet synchronized. Two can overlap (a
    /// view subscribes as the connection resubscribes everything): only the
    /// last one's `synchronized` makes the store live, so a newer replay is
    /// buffered rather than skipped, and its reset can't drop what it replays.
    private var outstanding = 0

    func beginReplay() {
        outstanding += 1
        live = false
        replay = []
    }

    /// A subscribe call failed: its `synchronized` won't come.
    func subscriptionFailed() {
        outstanding = max(0, outstanding - 1)
    }

    /// The socket closed: no subscription is outstanding any more.
    func closed() {
        outstanding = 0
        live = false
    }

    func synchronized(seq: Int64, reset: Bool) {
        outstanding = max(0, outstanding - 1)
        if reset {
            turns = [:]
            items = [:]
        }
        let buffered = replay
        replay = []
        for ev in buffered { upsert(ev) }
        lastSeq = seq
        live = outstanding == 0
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
        case "session.removed":
            session = nil
            turns = [:]
            items = [:]
            outbox = [:]
            removed = true
            return true
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

    /// Sends a prompt. It shows in `outbox` until the transcript has it, and
    /// waits out a dropped connection; it throws once the connection gives up.
    public func send(_ text: String, images: [ImageRef] = []) async throws {
        guard let c = connection else { throw RPCError.disconnected }
        let commandID = UUID().uuidString
        outbox[commandID] = Outgoing(text: text, images: images)
        struct P: Encodable, Sendable { var commandId: String; var sessionId: String; var text: String; var images: [String]? }
        do {
            let _: Item = try await c.deliver("session.prompt", P(commandId: commandID, sessionId: sessionID, text: text,
                                                                  images: images.isEmpty ? nil : images.map(\.id)))
        } catch {
            outbox[commandID] = nil
            throw error
        }
    }

    /// Sends the session's draft as its prompt. Like `send`, it shows in
    /// `outbox` until the transcript has it, which comes with the session
    /// without its draft, and it waits out a dropped connection.
    public func sendDraft() async throws {
        guard let c = connection else { throw RPCError.disconnected }
        guard let d = (session ?? c.sessions[sessionID])?.draft else {
            throw RPCError(code: "conflict", message: "The session has no draft to send.")
        }
        let commandID = UUID().uuidString
        outbox[commandID] = Outgoing(text: d.text, images: d.images ?? [])
        struct P: Encodable, Sendable { var commandId: String; var sessionId: String }
        do {
            let _: Item = try await c.deliver("session.sendDraft", P(commandId: commandID, sessionId: sessionID))
        } catch {
            outbox[commandID] = nil
            throw error
        }
    }

    /// Workspace-specific suggestions; this never sends a prompt.
    public func commands() async throws -> [SlashCommand] {
        guard let c = connection else { throw RPCError.disconnected }
        struct Result: Decodable, Sendable { var commands: [SlashCommand] }
        do {
            let result: Result = try await c.call("session.commands", ["sessionId": sessionID], timeout: 80)
            return result.commands
        } catch let error as RPCError where error.code == "method_not_found" { return [] }
    }

    public func interrupt(force: Bool = false) async throws {
        guard let c = connection else { throw RPCError.disconnected }
        struct P: Encodable, Sendable { var sessionId: String; var force: Bool }
        let _: Empty = try await c.call("session.interrupt", P(sessionId: sessionID, force: force))
    }

    /// Interrupts the running turn and starts the next one with this queued
    /// prompt, ahead of the others.
    public func sendNow(_ item: Item) async throws {
        guard let c = connection else { throw RPCError.disconnected }
        struct P: Encodable, Sendable { var sessionId: String; var itemId: String }
        do {
            let _: Empty = try await c.call("session.sendNow", P(sessionId: sessionID, itemId: item.id))
        } catch let e as RPCError where e.code == "method_not_found" {
            throw RPCError(code: e.code, message: "This server's rad is too old to send a queued prompt now. Update rad on it.")
        }
    }

    /// Takes this queued prompt out of the queue, unsent.
    public func cancelQueued(_ item: Item) async throws {
        guard let c = connection else { throw RPCError.disconnected }
        struct P: Encodable, Sendable { var sessionId: String; var itemId: String }
        do {
            let _: Empty = try await c.call("session.cancelQueued", P(sessionId: sessionID, itemId: item.id))
        } catch let e as RPCError where e.code == "method_not_found" {
            throw RPCError(code: e.code, message: "This server's rad is too old to cancel a queued prompt. Update rad on it.")
        }
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
