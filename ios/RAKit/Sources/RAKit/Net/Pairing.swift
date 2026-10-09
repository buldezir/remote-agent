import Foundation

/// A parsed `remoteagent://pair?...` link from `rad pair`.
public struct PairingLink: Equatable, Sendable {
    public var name: String
    public var code: String
    public var urls: [URL]

    public init?(string: String) {
        guard let comps = URLComponents(string: string.trimmingCharacters(in: .whitespacesAndNewlines)),
              comps.scheme == "remoteagent", comps.host == "pair" else { return nil }
        let items = comps.queryItems ?? []
        guard let code = items.first(where: { $0.name == "code" })?.value, !code.isEmpty else { return nil }
        self.code = code
        name = items.first(where: { $0.name == "name" })?.value ?? "Server"
        urls = items.filter { $0.name == "url" }.compactMap { $0.value.flatMap(URL.init(string:)) }
        if urls.isEmpty { return nil }
    }
}

public struct PairResult: Decodable, Sendable {
    public var serverId: String
    public var name: String
    public var protocolVersion: Int
    public var version: String
    public var token: String
    public var deviceId: String
}

public enum Pairing {
    /// Redeems the pairing code against each URL in order. Returns the result
    /// and the URL that worked.
    public static func pair(_ link: PairingLink, deviceName: String) async throws -> (PairResult, URL) {
        var lastError: Error = RPCError(code: "unreachable", message: "No server address was reachable")
        for base in link.urls {
            var req = URLRequest(url: base.appending(path: "v1/pair"))
            req.httpMethod = "POST"
            req.timeoutInterval = 6
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            req.httpBody = try JSONEncoder().encode(["code": link.code, "deviceName": deviceName])
            do {
                let (data, resp) = try await URLSession.shared.data(for: req)
                let status = (resp as? HTTPURLResponse)?.statusCode ?? 0
                if status == 200 {
                    return (try WireCoding.decoder().decode(PairResult.self, from: data), base)
                }
                if let e = try? JSONDecoder().decode(RPCError.self, from: data) {
                    throw e // the server answered; trying other URLs won't help
                }
                lastError = RPCError(code: "http", message: "Server returned HTTP \(status)")
            } catch let e as RPCError {
                throw e
            } catch {
                lastError = error
            }
        }
        throw lastError
    }
}
