import Foundation
import Network
import RAKit

/// A WebSocket server on loopback that stands in for rad: it answers
/// `server.info`, `session.prompt`, the drafts' calls, the index's
/// `subscribe`, the commits', imports', searches' and renames' calls and pings, and records the calls. `quiet()`
/// makes the sockets open so far ignore everything, as a socket that died
/// while the Mac slept does: nothing comes back, and nothing says it closed.
final class TestServer: @unchecked Sendable {
    struct Call: Sendable {
        var connection: Int
        var method: String
        var commandID: String?
        var params: JSONValue?
    }

    private let listener: NWListener
    private let queue = DispatchQueue(label: "TestServer")
    /// The sessions the index replays, as JSON objects.
    private let index: [String]
    private let name: String
    private let search: Search?
    /// `server.info` offers `session.rename`.
    private let renames: Bool

    /// How the server answers `session.search`, which `server.info` then
    /// offers: with the sessions in the index whose title holds the query.
    struct Search: Sendable {
        /// Seconds to wait before answering a query.
        var delays: [String: Double] = [:]
        /// The error it answers with instead.
        var error: String?
        var more = false
        var indexing = false
    }
    // Guarded by `queue`.
    private var connections: [NWConnection] = []
    private var quietCount = 0
    private var recorded: [Call] = []

    init(index: [String] = [], name: String = "Test", search: Search? = nil, renames: Bool = false) throws {
        self.index = index
        self.name = name
        self.search = search
        self.renames = renames
        let ws = NWProtocolWebSocket.Options()
        ws.autoReplyPing = false
        let params = NWParameters.tcp
        params.defaultProtocolStack.applicationProtocols.insert(ws, at: 0)
        params.requiredLocalEndpoint = .hostPort(host: "127.0.0.1", port: .any)
        listener = try NWListener(using: params)
        listener.newConnectionHandler = { [weak self] c in self?.accept(c) }
    }

    /// Starts listening; returns the base URL.
    func start() async throws -> URL {
        try await withCheckedThrowingContinuation { (done: CheckedContinuation<Void, Error>) in
            listener.stateUpdateHandler = { [listener] state in
                switch state {
                case .ready:
                    listener.stateUpdateHandler = nil
                    done.resume()
                case .failed(let e):
                    listener.stateUpdateHandler = nil
                    done.resume(throwing: e)
                default: break
                }
            }
            listener.start(queue: queue)
        }
        return URL(string: "http://127.0.0.1:\(listener.port!.rawValue)")!
    }

    func stop() {
        listener.cancel()
        queue.sync { for c in connections { c.cancel() } }
    }

    /// The sockets open now stop answering; later ones answer.
    func quiet() { queue.sync { quietCount = connections.count } }

    var calls: [Call] { queue.sync { recorded } }
    var connectionCount: Int { queue.sync { connections.count } }

    private func accept(_ c: NWConnection) {
        let n = connections.count
        connections.append(c)
        c.start(queue: queue)
        receive(c, n)
    }

    private func receive(_ c: NWConnection, _ n: Int) {
        c.receiveMessage { [weak self] data, context, _, error in
            guard let self, error == nil else { return }
            let meta = context?.protocolMetadata(definition: NWProtocolWebSocket.definition) as? NWProtocolWebSocket.Metadata
            if n >= quietCount {
                switch meta?.opcode {
                case .ping: send(c, data ?? Data(), opcode: .pong)
                case .text: if let data { answer(c, n, data) }
                default: break
                }
            }
            if meta?.opcode != .close { receive(c, n) }
        }
    }

    private func answer(_ c: NWConnection, _ n: Int, _ data: Data) {
        guard let req = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let id = req["id"] as? Int, let method = req["method"] as? String else { return }
        let params = req["params"] as? [String: Any]
        let raw = params.flatMap { try? JSONSerialization.data(withJSONObject: $0) }
        recorded.append(Call(connection: n, method: method, commandID: params?["commandId"] as? String,
                             params: raw.flatMap { try? JSONDecoder().decode(JSONValue.self, from: $0) }))
        let result: String
        switch method {
        case "server.info":
            result = #"{"serverId":"srv","name":"\#(name)","protocolVersion":1,"version":"0.1""#
                + (search == nil ? "" : #","search":true"#) + (renames ? #","rename":true"# : "") + "}"
        case "session.commands":
            send(c, Data(#"{"id":\#(id),"error":{"code":"method_not_found","message":"old server"}}"#.utf8), opcode: .text)
            return
        case "subscribe" where params?["stream"] as? String == "index":
            let events = index.enumerated().map { i, s in
                #"{"stream":"index","seq":\#(i + 1),"type":"session.upserted","session":\#(s)}"#
            }
            send(c, Data(#"{"method":"events","params":{"stream":"index","events":[\#(events.joined(separator: ","))]}}"#.utf8), opcode: .text)
            send(c, Data(#"{"method":"synchronized","params":{"stream":"index","seq":\#(index.count),"reset":true}}"#.utf8), opcode: .text)
            result = #"{"stream":"index","seq":\#(index.count)}"#
        case "session.prompt", "session.sendDraft":
            let sid = params?["sessionId"] as? String ?? ""
            result = #"{"id":"i\#(id)","sessionId":"\#(sid)","order":1,"kind":"user_message","status":"queued","#
                + #""createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}"#
        case "session.saveDraft":
            // The session, with the draft as saved: none for no text.
            let sid = params?["sessionId"] as? String ?? ""
            let text = params?["text"] as? String ?? ""
            let draft = text.isEmpty ? "" : #""draft":{"text":"\#(text)"},"#
            result = #"{"id":"\#(sid)","projectId":"p","harness":"fake","status":"idle","title":"\#(sid)","archived":false,"#
                + draft + #""workspace":{"kind":"root","path":"/tmp/p"},"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}"#
        case "session.rename":
            // The session with the title given, the user's; with none, rad's.
            let sid = params?["sessionId"] as? String ?? ""
            let title = params?["title"] as? String ?? ""
            let named = title.isEmpty ? #""title":"\#(sid)","# : #""title":"\#(title)","customTitle":true,"#
            result = #"{"id":"\#(sid)","projectId":"p","harness":"fake","status":"idle","archived":false,"#
                + named + #""workspace":{"kind":"root","path":"/tmp/p"},"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}"#
        case "git.commitPreview":
            result = #"{"branch":"ra/x","files":[{"path":"a.txt","status":"modified","additions":1,"deletions":0}],"message":"Fix it","remote":"origin","ahead":0}"#
        case "git.commit":
            let sid = params?["sessionId"] as? String ?? ""
            result = #"{"id":"k\#(id)","kind":"commit","sessionId":"\#(sid)","status":"running","output":"","startedAt":"2026-01-01T00:00:00Z"}"#
        case "task.list":
            // A session's commit runs; the server's own tasks are none.
            let sid = params?["sessionId"] as? String
            result = sid.map { #"{"tasks":[{"id":"k0","kind":"commit","sessionId":"\#($0)","status":"running","output":"","startedAt":"2026-01-01T00:00:00Z"}]}"# }
                ?? #"{"tasks":[]}"#
        case "session.importable":
            result = #"{"sessions":[{"id":"n2","title":"Add a dark theme","messages":6,"updatedAt":"2026-01-01T00:00:00Z","active":true},"#
                + #"{"id":"n1","title":"Fix the login redirect","messages":4,"updatedAt":"2025-12-31T22:00:00Z"}]}"#
        case "session.import":
            // A session of the agent's own, in the project folder.
            let pid = params?["projectId"] as? String ?? ""
            let harness = params?["harness"] as? String ?? ""
            let native = params?["nativeId"] as? String ?? ""
            result = #"{"id":"imported-\#(native)","projectId":"\#(pid)","harness":"\#(harness)","nativeId":"\#(native)","status":"idle","title":"Fix the login redirect","archived":false,"#
                + #""workspace":{"kind":"root","path":"/tmp/p"},"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}"#
        case "session.search" where search != nil:
            answerSearch(c, id, query: params?["query"] as? String ?? "", search!)
            return
        default:
            result = "{}"
        }
        send(c, Data(#"{"id":\#(id),"result":\#(result)}"#.utf8), opcode: .text)
    }

    /// The sessions in the index whose title holds the query, each its title
    /// as the snippet, after the query's delay.
    private func answerSearch(_ c: NWConnection, _ id: Int, query: String, _ search: Search) {
        if let error = search.error {
            send(c, Data(#"{"id":\#(id),"error":{"code":"internal","message":"\#(error)"}}"#.utf8), opcode: .text)
            return
        }
        let results: [String] = index.compactMap { s in
            let session = (try? JSONSerialization.jsonObject(with: Data(s.utf8))) as? [String: Any]
            guard let title = session?["title"] as? String, title.range(of: query, options: .caseInsensitive) != nil else { return nil }
            return #"{"session":\#(s),"snippet":[{"text":"\#(title)","match":true}],"matches":0}"#
        }
        let answer = #"{"id":\#(id),"result":{"results":[\#(results.joined(separator: ","))],"more":\#(search.more),"indexing":\#(search.indexing)}}"#
        queue.asyncAfter(deadline: .now() + (search.delays[query] ?? 0)) { [weak self] in
            self?.send(c, Data(answer.utf8), opcode: .text)
        }
    }

    private func send(_ c: NWConnection, _ data: Data, opcode: NWProtocolWebSocket.Opcode) {
        let meta = NWProtocolWebSocket.Metadata(opcode: opcode)
        let context = NWConnection.ContentContext(identifier: "frame", metadata: [meta])
        c.send(content: data, contentContext: context, isComplete: true, completion: .idempotent)
    }
}
