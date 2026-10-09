import AppKit
import ApplicationServices
import CoreGraphics
import Darwin
import Network
import Observation

/// The privacy permissions agents commonly need. macOS asks the app that is
/// responsible for a process, and rad's agents are this app's children, so
/// what is granted here applies to them.
enum Permission: String, CaseIterable, Identifiable {
    case desktop, documents, downloads, fullDisk, accessibility, screenRecording, automation, localNetwork, developerTools

    var id: String { rawValue }

    var folder: URL? {
        let home = FileManager.default.homeDirectoryForCurrentUser
        switch self {
        case .desktop: return home.appendingPathComponent("Desktop")
        case .documents: return home.appendingPathComponent("Documents")
        case .downloads: return home.appendingPathComponent("Downloads")
        default: return nil
        }
    }

    /// The pane in System Settings › Privacy & Security. Local Network has
    /// no anchor, so it opens Privacy & Security itself.
    var settingsURL: URL {
        let anchor = switch self {
        case .desktop, .documents, .downloads: "?Privacy_FilesAndFolders"
        case .fullDisk: "?Privacy_AllFiles"
        case .accessibility: "?Privacy_Accessibility"
        case .screenRecording: "?Privacy_ScreenCapture"
        case .automation: "?Privacy_Automation"
        case .localNetwork: ""
        case .developerTools: "?Privacy_DevTools"
        }
        return URL(string: "x-apple.systempreferences:com.apple.preference.security\(anchor)")!
    }
}

enum PermissionStatus: Equatable {
    case granted
    case denied
    /// Not asked yet. Checking would itself make macOS ask, so the app waits
    /// for the user to press Allow.
    case notAsked
    /// A switch in System Settings that is off; nothing to ask.
    case off
    /// macOS has no way to check this one.
    case unknown
    case unavailable(String)
}

@MainActor @Observable
final class Permissions {
    private(set) var status: [Permission: PermissionStatus] = [:]
    /// Granted while rad was running; rad picks some up only when restarted.
    private(set) var needsRestart = false

    @ObservationIgnored private let defaults = UserDefaults.standard

    private func asked(_ p: Permission) -> Bool { defaults.bool(forKey: "asked.\(p.rawValue)") }
    private func setAsked(_ p: Permission) { defaults.set(true, forKey: "asked.\(p.rawValue)") }

    /// Re-reads what can be checked without making macOS ask. `slow` adds
    /// the checks that start System Events or touch the network.
    func refresh(slow: Bool = false) async {
        var next = status
        for p in [Permission.desktop, .documents, .downloads] {
            next[p] = asked(p) ? Self.folderStatus(p.folder!) : .notAsked
        }
        next[.fullDisk] = Self.fullDiskStatus()
        next[.accessibility] = AXIsProcessTrusted() ? .granted : (asked(.accessibility) ? .denied : .notAsked)
        next[.screenRecording] = CGPreflightScreenCaptureAccess() ? .granted : (asked(.screenRecording) ? .denied : .notAsked)
        next[.developerTools] = .unknown
        if slow {
            next[.automation] = await Self.automationStatus(ask: false, launch: asked(.automation))
            next[.localNetwork] = asked(.localNetwork) ? await LocalNetwork.status() : (LocalNetwork.gateway() == nil
                ? .unavailable("Not on a local network") : .notAsked)
        }
        for p in [Permission.fullDisk, .screenRecording] where status[p] != nil && status[p] != .granted && next[p] == .granted {
            needsRestart = true
        }
        status = next
    }

    func clearRestart() { needsRestart = false }

    /// Makes macOS ask. Once the user has answered, the switches are in
    /// System Settings instead (`settingsURL`).
    func request(_ p: Permission) async {
        setAsked(p)
        switch p {
        case .desktop, .documents, .downloads:
            // macOS holds the call until the user answers.
            let folder = p.folder!
            _ = await Task.detached { Self.folderStatus(folder) }.value
        case .accessibility:
            // kAXTrustedCheckOptionPrompt, spelled out: Swift 6 won't read
            // that global var here.
            _ = AXIsProcessTrustedWithOptions(["AXTrustedCheckOptionPrompt": true] as CFDictionary)
        case .screenRecording:
            _ = CGRequestScreenCaptureAccess()
        case .automation:
            status[p] = await Self.automationStatus(ask: true, launch: true)
        case .localNetwork:
            await LocalNetwork.trigger()
        case .fullDisk, .developerTools:
            break
        }
        await refresh(slow: true)
    }

    /// Listing a protected folder makes macOS ask the first time and fails
    /// with EPERM once the user said no.
    nonisolated private static func folderStatus(_ folder: URL) -> PermissionStatus {
        do {
            _ = try FileManager.default.contentsOfDirectory(atPath: folder.path)
            return .granted
        } catch let error as CocoaError where error.code == .fileReadNoPermission {
            return .denied
        } catch {
            return .unavailable(error.localizedDescription)
        }
    }

    /// Only Full Disk Access opens the system's own privacy database.
    nonisolated private static func fullDiskStatus() -> PermissionStatus {
        let fd = open("/Library/Application Support/com.apple.TCC/TCC.db", O_RDONLY)
        guard fd >= 0 else { return .off }
        close(fd)
        return .granted
    }

    /// Whether this app may send Apple Events to System Events, which agents
    /// use for keystrokes and clicks. System Events has to be running to be
    /// asked about; `launch` starts it in the background. With `ask`, macOS
    /// shows its prompt, and the call blocks until the user answers, so it
    /// runs off the main actor.
    nonisolated private static func automationStatus(ask: Bool, launch: Bool) async -> PermissionStatus {
        let id = "com.apple.systemevents"
        if NSRunningApplication.runningApplications(withBundleIdentifier: id).isEmpty {
            guard launch else { return .notAsked }
            let config = NSWorkspace.OpenConfiguration()
            config.activates = false
            config.addsToRecentItems = false
            _ = try? await NSWorkspace.shared.openApplication(
                at: URL(fileURLWithPath: "/System/Library/CoreServices/System Events.app"), configuration: config)
        }
        return await Task.detached {
            let target = NSAppleEventDescriptor(bundleIdentifier: id)
            switch AEDeterminePermissionToAutomateTarget(target.aeDesc, typeWildCard, typeWildCard, ask) {
            case noErr: return PermissionStatus.granted
            case OSStatus(errAEEventNotPermitted): return .denied
            case OSStatus(errAEEventWouldRequireUserConsent): return .notAsked
            case OSStatus(procNotFound): return .notAsked
            case let other: return .unavailable("Error \(other)")
            }
        }.value
    }
}

/// Local network privacy has no API to read. A connection to a LAN address
/// makes macOS ask once, and a connection that waits with
/// `.localNetworkDenied` shows the user said no.
enum LocalNetwork {
    /// The likely router: the first host on this Mac's private IPv4 subnet.
    /// Tailscale (100.64/10) isn't a local network to macOS.
    nonisolated static func gateway() -> String? {
        var list: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&list) == 0, let first = list else { return nil }
        defer { freeifaddrs(list) }
        for entry in sequence(first: first, next: { $0.pointee.ifa_next }) {
            let ifa = entry.pointee
            guard ifa.ifa_flags & UInt32(IFF_UP) != 0, ifa.ifa_flags & UInt32(IFF_LOOPBACK) == 0,
                  let addr = ifa.ifa_addr, addr.pointee.sa_family == UInt8(AF_INET),
                  let mask = ifa.ifa_netmask else { continue }
            let ip = addr.withMemoryRebound(to: sockaddr_in.self, capacity: 1) { UInt32(bigEndian: $0.pointee.sin_addr.s_addr) }
            let netmask = mask.withMemoryRebound(to: sockaddr_in.self, capacity: 1) { UInt32(bigEndian: $0.pointee.sin_addr.s_addr) }
            let isPrivate = ip >> 24 == 10 || ip >> 20 == 0xAC1 || ip >> 16 == 0xC0A8
            guard isPrivate, netmask != 0xFFFF_FFFF else { continue }
            var host = (ip & netmask) + 1
            if host == ip { host += 1 }
            return [24, 16, 8, 0].map { String(host >> UInt32($0) & 0xFF) }.joined(separator: ".")
        }
        return nil
    }

    /// Sends one UDP datagram to the LAN, which makes macOS ask.
    nonisolated static func trigger() async {
        guard let host = gateway() else { return }
        let connection = NWConnection(host: NWEndpoint.Host(host), port: 9, using: .udp)
        connection.start(queue: .global())
        connection.send(content: Data([0]), completion: .idempotent)
        try? await Task.sleep(for: .seconds(1))
        connection.cancel()
    }

    nonisolated static func status() async -> PermissionStatus {
        guard let host = gateway() else { return .unavailable("Not on a local network") }
        let connection = NWConnection(host: NWEndpoint.Host(host), port: 9, using: .tcp)
        let result: PermissionStatus = await withCheckedContinuation { done in
            let once = Once()
            connection.stateUpdateHandler = { state in
                switch state {
                case .waiting where connection.currentPath?.unsatisfiedReason == .localNetworkDenied:
                    once.run { done.resume(returning: .denied) }
                case .ready, .failed, .waiting:
                    once.run { done.resume(returning: .granted) }
                default:
                    break
                }
            }
            connection.start(queue: .global())
            // A router that drops the connection says nothing; that's allowed too.
            DispatchQueue.global().asyncAfter(deadline: .now() + 2) {
                once.run { done.resume(returning: .granted) }
            }
        }
        connection.cancel()
        return result
    }
}

/// Runs a closure at most once, from any thread.
private final class Once: @unchecked Sendable {
    private let lock = NSLock()
    private var done = false

    func run(_ body: () -> Void) {
        lock.lock()
        defer { lock.unlock() }
        guard !done else { return }
        done = true
        body()
    }
}
