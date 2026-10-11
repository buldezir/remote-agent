import Foundation

/// Which rad the app runs: its own, or a newer release that Update rad
/// downloaded (`rad update --to`). Downloads live next to the app, not in it,
/// so the app's signature, and the permissions macOS ties to it, stay intact.
/// rad is still this app's child either way, so those permissions apply to it.
enum RadBinary {
    nonisolated static let bundled = Bundle.main.url(forAuxiliaryExecutable: "rad")!

    /// The bundled rad's version: build-rad.sh gives it the app's.
    nonisolated static let bundledVersion =
        Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "dev"

    /// Where downloads go, as rad-<version>.
    nonisolated static let downloads: URL = {
        if let home = ProcessInfo.processInfo.environment["RAD_HOME"] {
            return URL(fileURLWithPath: home).appendingPathComponent("Remote Agent Server")
        }
        return FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("Remote Agent Server")
    }()

    /// Only release builds update rad. A development build, such as Xcode's
    /// "0.1", always runs the rad built with it.
    nonisolated static var updatable: Bool { ReleaseVersion(bundledVersion) != nil }

    /// The rad to run and its version: the newest download that is newer
    /// than the bundled rad, or else the bundled one. Other downloads are
    /// deleted, such as those an app update has caught up with.
    nonisolated static func choose() -> (url: URL, version: String) {
        guard let own = ReleaseVersion(bundledVersion) else { return (bundled, bundledVersion) }
        let files = FileManager.default
        let found = ((try? files.contentsOfDirectory(at: downloads, includingPropertiesForKeys: nil)) ?? [])
            .compactMap { url -> (url: URL, version: ReleaseVersion)? in
                let name = url.lastPathComponent
                guard name.hasPrefix("rad-"), let version = ReleaseVersion(String(name.dropFirst(4))) else { return nil }
                return (url, version)
            }
            .sorted { $0.version > $1.version }
        var chosen: (url: URL, version: ReleaseVersion)?
        for download in found {
            if chosen == nil && download.version > own && files.isExecutableFile(atPath: download.url.path) {
                chosen = download
            } else {
                try? files.removeItem(at: download.url)
            }
        }
        guard let chosen else { return (bundled, bundledVersion) }
        return (chosen.url, chosen.version.description)
    }

    /// Deletes a download that didn't start, so the next start runs the bundled rad.
    nonisolated static func discard(_ url: URL) {
        guard url != bundled else { return }
        try? FileManager.default.removeItem(at: url)
    }
}

/// A release's version, X.Y.Z or X.Y.Z-pre, ordered the way rad orders them
/// (server/internal/update/version.go). A development build's version, such
/// as "dev", "0.1" or git describe's "0.1.2-6-gd609b51", isn't one.
struct ReleaseVersion: Comparable, CustomStringConvertible, Sendable {
    let numbers: [Int]
    let prerelease: [String]
    let description: String

    init?(_ string: String) {
        guard let match = string.wholeMatch(of: /v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?/),
              !string.contains(/-\d+-g[0-9a-f]+$/),
              let major = Int(match.1), let minor = Int(match.2), let patch = Int(match.3)
        else { return nil }
        numbers = [major, minor, patch]
        prerelease = match.4.map { $0.split(separator: ".", omittingEmptySubsequences: false).map(String.init) } ?? []
        description = string.hasPrefix("v") ? String(string.dropFirst()) : string
    }

    static func < (a: Self, b: Self) -> Bool {
        if a.numbers != b.numbers { return a.numbers.lexicographicallyPrecedes(b.numbers) }
        // A prerelease comes before its release.
        if a.prerelease.isEmpty || b.prerelease.isEmpty { return !a.prerelease.isEmpty && b.prerelease.isEmpty }
        for (x, y) in zip(a.prerelease, b.prerelease) where x != y {
            switch (Int(x), Int(y)) {
            case let (i?, j?): return i < j
            case (.some, nil): return true
            case (nil, .some): return false
            default: return x < y
            }
        }
        return a.prerelease.count < b.prerelease.count
    }
}
