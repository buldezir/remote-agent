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

    @Test func failedLoadsAreRetried() async throws {
        let cache = ImageCache(dir: nil)
        await #expect(throws: RPCError.self) { try await cache.data(id) { throw RPCError.disconnected } }
        #expect(try await cache.data(id) { Data([7]) } == Data([7]))
    }
}
