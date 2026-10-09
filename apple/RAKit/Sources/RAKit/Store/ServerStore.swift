import Foundation
import Observation

public struct SavedServer: Codable, Hashable, Identifiable, Sendable {
    public var id: String          // serverId
    public var name: String
    public var urls: [URL]         // preference order; the last one that worked comes first
    public var deviceId: String
    public var addedAt: Date
}

/// The list of paired servers. Tokens are kept in the Keychain.
@MainActor @Observable
public final class ServerStore {
    public private(set) var servers: [SavedServer] = []
    // A cache filled lazily from view bodies; not UI state, so not observed
    // (mutating observed state during a view update makes navigation stutter).
    @ObservationIgnored private var connections: [String: ServerConnection] = [:]
    private let defaults: UserDefaults
    private static let key = "servers.v1"

    public init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        if let data = defaults.data(forKey: Self.key),
           let list = try? JSONDecoder().decode([SavedServer].self, from: data) {
            servers = list
        }
    }

    private func save() {
        defaults.set(try? JSONEncoder().encode(servers), forKey: Self.key)
    }

    public func connection(for server: SavedServer) -> ServerConnection {
        if let c = connections[server.id] { return c }
        let c = ServerConnection(server: server, token: Keychain.get(server.id) ?? "") { [weak self] url in
            self?.promote(url, for: server.id)
        }
        connections[server.id] = c
        return c
    }

    /// Pairs with a server from a pairing link and saves it.
    @discardableResult
    public func pair(_ link: PairingLink, deviceName: String) async throws -> SavedServer {
        let (res, worked) = try await Pairing.pair(link, deviceName: deviceName)
        var urls = link.urls
        urls.removeAll { $0 == worked }
        urls.insert(worked, at: 0)
        let s = SavedServer(id: res.serverId, name: res.name, urls: urls, deviceId: res.deviceId, addedAt: .now)
        Keychain.set(res.token, for: s.id)
        connections.removeValue(forKey: s.id)?.stop()
        servers.removeAll { $0.id == s.id }
        servers.append(s)
        save()
        return s
    }

    /// Forgets a server. Also asks the server to revoke this device's token
    /// (best effort, in the background) so it can't be reused.
    public func remove(_ server: SavedServer) {
        connection(for: server).unpairAndStop()
        connections.removeValue(forKey: server.id)
        Keychain.delete(server.id)
        servers.removeAll { $0.id == server.id }
        save()
    }

    public func rename(_ server: SavedServer, to name: String) {
        guard let i = servers.firstIndex(where: { $0.id == server.id }) else { return }
        servers[i].name = name
        save()
    }

    private func promote(_ url: URL, for id: String) {
        guard let i = servers.firstIndex(where: { $0.id == id }), servers[i].urls.first != url else { return }
        servers[i].urls.removeAll { $0 == url }
        servers[i].urls.insert(url, at: 0)
        save()
    }

    /// Pause all connections (app backgrounded) or resume the ones in use.
    public func setActive(_ active: Bool) {
        for c in connections.values {
            if active { c.resumeIfWanted() } else { c.pause() }
        }
    }
}
