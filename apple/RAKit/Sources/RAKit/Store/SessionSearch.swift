import Foundation
import Observation

/// Search across the paired servers' sessions (`session.search`): what the
/// sidebar's search field, and the iPhone's Inbox, show in place of their
/// rows. A query of two characters or more searches every connected server
/// that can, a moment after the last change, and the answers merge as they
/// come.
@MainActor @Observable
public final class SessionSearch {
    /// A session a server found.
    public struct Hit: Identifiable, Hashable, Sendable {
        /// The paired server's id (`SavedServer.id`).
        public var serverId: String
        public var result: SearchResult

        public var id: String { "\(serverId)/\(result.session.id)" }

        public init(serverId: String, result: SearchResult) {
            self.serverId = serverId
            self.result = result
        }
    }

    /// A server whose search failed, and why.
    public struct Failure: Hashable, Sendable {
        public var server: String
        public var message: String
    }

    /// The shortest query that searches, trimmed.
    public static let minLength = 2
    /// The sessions asked of each server.
    public static let limit = 30

    public var query = "" {
        didSet { if query != oldValue { changed() } }
    }

    /// Archived sessions are found too.
    public var includeArchived = true {
        didSet { if includeArchived != oldValue { changed() } }
    }

    /// The sessions found, merged across servers.
    public private(set) var hits: [Hit] = []
    /// A search is waiting to start, or a server hasn't answered yet.
    public private(set) var isSearching = false
    /// The servers whose search failed.
    public private(set) var failures: [Failure] = []
    /// A server found more sessions than it gave.
    public private(set) var more = false
    /// The names of the servers still indexing sessions an older rad wrote.
    public private(set) var indexing: [String] = []
    /// The names of the connected servers that can't search: their rad is older.
    public private(set) var unsupported: [String] = []
    /// Every server asked answered the current query, and one of them did
    /// without failing.
    public private(set) var answered = false

    /// The query is long enough to search: the results show in place of the rows.
    public var isActive: Bool { trimmedQuery.count >= Self.minLength }

    public var trimmedQuery: String { query.trimmingCharacters(in: .whitespacesAndNewlines) }

    /// Every server answered with nothing.
    public var nothingFound: Bool { isActive && !isSearching && answered && hits.isEmpty }
    /// No server could be searched: none is connected, or none that searches.
    public private(set) var noServers = false

    /// The wait after the last change before searching.
    @ObservationIgnored var debounce: Duration = .milliseconds(250)
    @ObservationIgnored private let connections: @MainActor () -> [ServerConnection]
    /// Bumped on each change, so answers to an older query are dropped.
    @ObservationIgnored private var generation = 0
    @ObservationIgnored private var pending: Task<Void, Never>?
    /// The trimmed query and `includeArchived` of the search asked for last.
    @ObservationIgnored private var asked: String?
    // The current round's answers, by the server's place among those asked.
    @ObservationIgnored private var round = Round()

    private struct Round {
        var servers: [String] = []
        var lists: [Int: [Hit]] = [:]
        var failures: [Int: Failure] = [:]
        var more = false
        var indexing: Set<Int> = []
        var unsupported: [String] = []
        var remaining = 0
        /// Some server has answered: the round's results replace the last.
        var heard = false
    }

    /// Searches the connections `connections` returns, in that order: the
    /// app's servers.
    public init(connections: @escaping @MainActor () -> [ServerConnection]) {
        self.connections = connections
    }

    /// Searches every paired server.
    public convenience init(store: ServerStore) {
        self.init { [weak store] in
            guard let store else { return [] }
            return store.servers.map { store.connection(for: $0) }
        }
    }

    /// Searches again, such as when a server connects.
    public func refresh() {
        guard isActive else { return }
        changed(force: true)
    }

    /// A change that leaves the trimmed query as it was, such as the space
    /// typed between two words, searches nothing new.
    private func changed(force: Bool = false) {
        let key = isActive ? "\(includeArchived) \(trimmedQuery)" : nil
        guard force || key != asked else { return }
        asked = key
        generation += 1
        pending?.cancel()
        pending = nil
        guard isActive else {
            clear()
            return
        }
        // The last results stay until the new ones come.
        isSearching = true
        let gen = generation
        let wait = debounce
        pending = Task { [weak self] in
            try? await Task.sleep(for: wait)
            guard !Task.isCancelled else { return }
            self?.run(gen)
        }
    }

    private func clear() {
        round = Round()
        hits = []
        isSearching = false
        failures = []
        more = false
        indexing = []
        unsupported = []
        answered = false
        noServers = false
    }

    private func run(_ gen: Int) {
        guard gen == generation else { return }
        let q = trimmedQuery, archived = includeArchived
        let connected = connections().filter { $0.state == .connected }
        let targets = connected.filter(\.searches)
        round = Round(servers: targets.map(\.server.name),
                      unsupported: connected.filter { !$0.searches }.map(\.server.name),
                      remaining: targets.count)
        guard !targets.isEmpty else {
            round.heard = true
            publish()
            return
        }
        for (i, c) in targets.enumerated() {
            Task { [weak self] in
                let answer: Result<SearchResults, Error>
                do {
                    answer = .success(try await c.search(query: q, archived: archived, limit: Self.limit))
                } catch {
                    answer = .failure(error)
                }
                self?.take(answer, from: c, at: i, generation: gen)
            }
        }
    }

    /// A server's answer, unless the query changed since it was asked.
    private func take(_ answer: Result<SearchResults, Error>, from c: ServerConnection, at i: Int, generation gen: Int) {
        guard gen == generation else { return }
        switch answer {
        case .success(let r):
            round.lists[i] = r.results.map { Hit(serverId: c.server.id, result: $0) }
            if r.more == true { round.more = true }
            if r.indexing == true { round.indexing.insert(i) }
        case .failure(let error):
            round.failures[i] = Failure(server: c.server.name, message: error.localizedDescription)
        }
        round.remaining -= 1
        round.heard = true
        publish()
    }

    private func publish() {
        guard round.heard else { return }
        let order = round.servers.indices
        hits = Self.mergeSearchResults(order.compactMap { round.lists[$0] })
        failures = order.compactMap { round.failures[$0] }
        more = round.more
        indexing = order.filter { round.indexing.contains($0) }.map { round.servers[$0] }
        unsupported = round.unsupported
        isSearching = round.remaining > 0
        answered = round.remaining == 0 && !round.lists.isEmpty
        noServers = round.servers.isEmpty && round.unsupported.isEmpty
    }

    /// Each server's results, in the app's order of servers, taken in turn:
    /// every server's first, then every second, and so on, each server's
    /// order kept. Scores from different servers can't be compared.
    public nonisolated static func mergeSearchResults(_ lists: [[Hit]]) -> [Hit] {
        var out: [Hit] = []
        out.reserveCapacity(lists.reduce(0) { $0 + $1.count })
        let longest = lists.map(\.count).max() ?? 0
        for rank in 0..<longest {
            for list in lists where rank < list.count {
                out.append(list[rank])
            }
        }
        return out
    }
}
