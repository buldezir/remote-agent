import Foundation
import Network
import RAKit

/// A WebSocket server on loopback that stands in for rad: it answers
/// `server.info`, `session.prompt`, the drafts' calls, the index's
/// `subscribe`, the commits' calls and pings, and records the calls. `quiet()`
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
    // Guarded by `queue`.
    private var connections: [NWConnection] = []
    private var quietCount = 0
    private var recorded: [Call] = []

    init(index: [String] = []) throws {
        self.index = index
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
            result = #"{"serverId":"srv","name":"Test","protocolVersion":1,"version":"0.1"}"#
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
        default:
            result = "{}"
        }
        send(c, Data(#"{"id":\#(id),"result":\#(result)}"#.utf8), opcode: .text)
    }

    private func send(_ c: NWConnection, _ data: Data, opcode: NWProtocolWebSocket.Opcode) {
        let meta = NWProtocolWebSocket.Metadata(opcode: opcode)
        let context = NWConnection.ContentContext(identifier: "frame", metadata: [meta])
        c.send(content: data, contentContext: context, isComplete: true, completion: .idempotent)
    }
}
