import Foundation
import Network

/// A WebSocket server on loopback that stands in for rad: it answers
/// `server.info`, `session.prompt` and pings, and records the calls. `quiet()`
/// makes the sockets open so far ignore everything, as a socket that died
/// while the Mac slept does: nothing comes back, and nothing says it closed.
final class TestServer: @unchecked Sendable {
    struct Call: Sendable {
        var connection: Int
        var method: String
        var commandID: String?
    }

    private let listener: NWListener
    private let queue = DispatchQueue(label: "TestServer")
    // Guarded by `queue`.
    private var connections: [NWConnection] = []
    private var quietCount = 0
    private var recorded: [Call] = []

    init() throws {
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
        recorded.append(Call(connection: n, method: method, commandID: params?["commandId"] as? String))
        let result: String
        switch method {
        case "server.info":
            result = #"{"serverId":"srv","name":"Test","protocolVersion":1,"version":"0.1"}"#
        case "session.commands":
            send(c, Data(#"{"id":\#(id),"error":{"code":"method_not_found","message":"old server"}}"#.utf8), opcode: .text)
            return
        case "session.prompt":
            let sid = params?["sessionId"] as? String ?? ""
            result = #"{"id":"i\#(id)","sessionId":"\#(sid)","order":1,"kind":"user_message","status":"queued","#
                + #""createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}"#
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
