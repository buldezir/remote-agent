import Foundation
import Testing
@testable import RAKit

struct ImageCacheTests {
    let id = String(repeating: "ab", count: 32) + ".png"

    func tempDir() -> URL {
        FileManager.default.temporaryDirectory.appending(path: "ImageCacheTests-\(UUID().uuidString)")
    }

    /// Counts loads across concurrent requests.
    actor Counter {
        var n = 0
        func next() -> Int { n += 1; return n }
    }

    @Test func loadsOnceAndKeepsOnDisk() async throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let loads = Counter()
        let cache = ImageCache(dir: dir)
        let id = id
        async let a = cache.data(id) { _ = await loads.next(); try await Task.sleep(for: .milliseconds(50)); return Data([1, 2, 3]) }
        async let b = cache.data(id) { _ = await loads.next(); return Data([9]) }
        let (x, y) = try await (a, b)
        #expect(x == Data([1, 2, 3]) && y == x)
        #expect(await loads.n == 1)

        // A new cache (the app relaunched) reads the file instead of loading.
        let again = try await ImageCache(dir: dir).data(id) { Issue.record("loaded again"); return Data() }
        #expect(again == Data([1, 2, 3]))
    }

    @Test func keepsNoFileForOddIds() async throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let cache = ImageCache(dir: dir)
        await cache.store(Data([1]), for: "../escape.png")
        #expect(try await cache.data("../escape.png") { Data() } == Data([1]))  // memory only
        #expect((try? FileManager.default.contentsOfDirectory(atPath: dir.path))?.isEmpty == true)
    }

    @Test func namedFileHasTheImage() async throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let cache = ImageCache(dir: dir)
        let url = try await cache.namedFile(id) { Data([1, 2, 3]) }
        #expect(url.lastPathComponent == "Image.png")
        #expect(try Data(contentsOf: url) == Data([1, 2, 3]))
        // Asked again, it's the same file, and nothing loads.
        #expect(try await cache.namedFile(id) { Issue.record("loaded again"); return Data() } == url)

        // The system cleared the caches: the file is written again from memory.
        try FileManager.default.removeItem(at: dir)
        #expect(try await cache.namedFile(id) { Data() } == url)
        #expect(try Data(contentsOf: url) == Data([1, 2, 3]))

        // Without a cache directory there is no file to give.
        let memoryOnly = ImageCache(dir: nil)
        await #expect(throws: CocoaError.self) { try await memoryOnly.namedFile(id) { Data([1]) } }
    }

    @Test func failedLoadsAreRetried() async throws {
        let cache = ImageCache(dir: nil)
        await #expect(throws: RPCError.self) { try await cache.data(id) { throw RPCError.disconnected } }
        #expect(try await cache.data(id) { Data([7]) } == Data([7]))
    }
}
