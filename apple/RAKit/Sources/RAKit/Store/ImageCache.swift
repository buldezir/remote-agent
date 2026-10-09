import Foundation

/// Images by id, in memory and on disk. An id names the image's content, so a
/// cached copy never goes stale, and servers can share entries.
public actor ImageCache {
    public static let shared = ImageCache()

    private let memory = NSCache<NSString, NSData>()
    private var loading: [String: Task<Data, Error>] = [:]
    private let dir: URL?

    init(dir: URL? = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask).first?.appending(path: "Images")) {
        memory.totalCostLimit = 64 << 20
        self.dir = dir
        if let dir { try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true) }
    }

    /// The image's bytes, from the cache or else from `load`. Concurrent
    /// requests for one id share a load.
    public func data(_ id: String, load: @escaping @Sendable () async throws -> Data) async throws -> Data {
        if let data = memory.object(forKey: id as NSString) { return data as Data }
        if let file = file(id), let data = try? Data(contentsOf: file) {
            memory.setObject(data as NSData, forKey: id as NSString, cost: data.count)
            return data
        }
        if let task = loading[id] { return try await task.value }
        let task = Task { try await load() }
        loading[id] = task
        defer { loading[id] = nil }
        let data = try await task.value
        store(data, for: id)
        return data
    }

    /// The image as a file named for people ("Image.png") rather than by its
    /// hash, for Quick Look and sharing. Loads the image first if needed.
    public func namedFile(_ id: String, load: @escaping @Sendable () async throws -> Data) async throws -> URL {
        let data = try await data(id, load: load)
        guard let dir, let cached = file(id) else { throw CocoaError(.fileNoSuchFile) }
        let named = dir.appending(path: "Named/\(cached.deletingPathExtension().lastPathComponent)/Image.\(cached.pathExtension)")
        let fm = FileManager.default
        if !fm.fileExists(atPath: named.path) {
            try fm.createDirectory(at: named.deletingLastPathComponent(), withIntermediateDirectories: true)
            // A link to the cached file takes no space; it may be missing, though.
            if (try? fm.linkItem(at: cached, to: named)) == nil { try data.write(to: named, options: .atomic) }
        }
        return named
    }

    /// Keeps bytes this device already has, e.g. an image it uploaded.
    public func store(_ data: Data, for id: String) {
        memory.setObject(data as NSData, forKey: id as NSString, cost: data.count)
        if let file = file(id) { try? data.write(to: file, options: .atomic) }
    }

    /// The cache file for an id the server made ("<sha256>.<ext>"); nil for
    /// anything else, so no id can name a path outside the directory.
    private func file(_ id: String) -> URL? {
        guard let dir, id.wholeMatch(of: /[0-9a-f]{64}\.[a-z]{3,4}/) != nil else { return nil }
        return dir.appending(path: id)
    }
}
