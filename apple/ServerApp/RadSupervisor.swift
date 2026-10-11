import AppKit
import Foundation
import Observation

/// What `rad serve --supervised` reports on stdout, one JSON object per line
/// (server/cmd/rad/supervised.go).
struct ReadyStatus: Decodable, Equatable, Sendable {
    let version: String
    let urls: [String]
    let listen: [String]
    let config: String
    let data: String
    let pairedDevices: Int
}

struct AgentInfo: Decodable, Identifiable, Sendable {
    let id: String
    let name: String
    let installed: Bool
    let version: String?
    let authOk: Bool
    let hint: String?
}

private struct StatusLine: Decodable {
    let event: String
}

private struct AgentsLine: Decodable {
    let agents: [AgentInfo]
}

/// Runs rad as this app's child, so macOS attributes rad and every agent it
/// starts to this app when it asks for permissions. Restarts rad if it dies,
/// and stops it when the app quits.
@MainActor @Observable
final class RadSupervisor {
    enum State: Equatable {
        case stopped
        case starting
        case running
        /// Another rad already serves this config (exit 75); not retried.
        case busy(String)
        /// rad exited on its own; it is restarted after a pause.
        case failed(String)
    }

    private(set) var state: State = .stopped
    private(set) var ready: ReadyStatus?
    private(set) var agents: [AgentInfo]?
    /// The login-shell environment rad last started with.
    private(set) var environment = ProcessInfo.processInfo.environment
    /// The rad binary that runs, or last ran, and its version (RadBinary).
    private(set) var executable = RadBinary.bundled
    private(set) var version = RadBinary.bundledVersion
    /// Why rad went back to the bundled copy, after a download didn't start.
    private(set) var note: String?

    let logURL: URL = {
        if let home = ProcessInfo.processInfo.environment["RAD_HOME"] {
            return URL(fileURLWithPath: home).appendingPathComponent("rad.log")
        }
        return FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Logs/Remote Agent Server/rad.log")
    }()

    @ObservationIgnored private var process: Process?
    /// rad's stdin. It stays open while the app runs; when the app dies, rad
    /// sees EOF and shuts down with its agents.
    @ObservationIgnored private var lifeline: Pipe?
    @ObservationIgnored private var launching = false
    @ObservationIgnored private var stopping = false
    @ObservationIgnored private var onStop: [() -> Void] = []
    @ObservationIgnored private var startedAt = Date.distantPast
    @ObservationIgnored private var restartDelay: TimeInterval = 1
    @ObservationIgnored private var restartTask: Task<Void, Never>?
    @ObservationIgnored private var activity: NSObjectProtocol?
    /// rad said it was ready since it was last launched.
    @ObservationIgnored private var wasReady = false

    /// rad or a start is under way.
    var isActive: Bool { process != nil || launching }

    func start() async {
        restartTask?.cancel()
        guard !isActive else { return }
        launching = true
        state = .starting
        environment = await LoginShell.environment()
        (executable, version) = RadBinary.choose()
        launching = false
        launch()
    }

    /// Stops rad: SIGTERM, then SIGKILL if it hasn't exited after 10 seconds.
    /// rad itself gives its agents a few seconds to stop.
    func stop(then done: (() -> Void)? = nil) {
        restartTask?.cancel()
        guard let process else {
            if !launching { state = .stopped }
            done?()
            return
        }
        if let done { onStop.append(done) }
        stopping = true
        process.terminate()
        let pid = process.processIdentifier
        Task {
            try? await Task.sleep(for: .seconds(10))
            if self.process?.processIdentifier == pid { kill(pid, SIGKILL) }
        }
    }

    func restart() {
        note = nil
        stop { Task { await self.start() } }
    }

    private func launch() {
        let p = Process()
        p.executableURL = executable
        p.arguments = ["serve", "--supervised"]
        p.environment = environment
        let lifeline = Pipe(), output = Pipe()
        p.standardInput = lifeline
        p.standardOutput = output
        let log = Self.openLog(logURL)
        p.standardError = log ?? FileHandle.nullDevice
        Self.readLines(output.fileHandleForReading) { [weak self] line in self?.handle(line) }
        p.terminationHandler = Self.onExit { [weak self] status, reason in self?.exited(status, reason) }
        do {
            try p.run()
        } catch {
            state = .failed(error.localizedDescription)
            return
        }
        try? log?.close()
        process = p
        self.lifeline = lifeline
        startedAt = Date()
        wasReady = false
        activity = ProcessInfo.processInfo.beginActivity(
            options: .userInitiatedAllowingIdleSystemSleep, reason: "rad is serving agents")
    }

    private func handle(_ line: Data) {
        guard let event = try? JSONDecoder().decode(StatusLine.self, from: line) else { return }
        switch event.event {
        case "ready":
            ready = try? JSONDecoder().decode(ReadyStatus.self, from: line)
            state = .running
            wasReady = true
        case "agents":
            agents = (try? JSONDecoder().decode(AgentsLine.self, from: line))?.agents
        default:
            break
        }
    }

    private func exited(_ status: Int32, _ reason: Process.TerminationReason) {
        process = nil
        lifeline = nil
        ready = nil
        agents = nil
        if let activity { ProcessInfo.processInfo.endActivity(activity) }
        activity = nil
        if stopping {
            stopping = false
            state = .stopped
            let callbacks = onStop
            onStop = []
            callbacks.forEach { $0() }
            return
        }
        let message = lastLogLine() ?? "rad exited with status \(status)."
        if reason == .exit && status == 75 {
            state = .busy(message)
            return
        }
        state = .failed(message)
        if executable != RadBinary.bundled && !wasReady {
            // A downloaded rad that never got going goes, rather than
            // failing again and again; the bundled one takes over.
            note = "rad \(version) didn't start (\(message)), so the app went back to rad \(RadBinary.bundledVersion)."
            RadBinary.discard(executable)
            restartDelay = 1
        }
        if Date().timeIntervalSince(startedAt) > 60 { restartDelay = 1 }
        let delay = restartDelay
        restartDelay = min(restartDelay * 2, 60)
        restartTask = Task {
            try? await Task.sleep(for: .seconds(delay))
            guard !Task.isCancelled else { return }
            await self.start()
        }
    }

    /// rad's last words, e.g. "another rad is already running with …".
    private func lastLogLine() -> String? {
        guard let handle = try? FileHandle(forReadingFrom: logURL) else { return nil }
        defer { try? handle.close() }
        let end = (try? handle.seekToEnd()) ?? 0
        try? handle.seek(toOffset: end > 4096 ? end - 4096 : 0)
        let tail = String(decoding: handle.readDataToEndOfFile(), as: UTF8.self)
        guard let line = tail.split(separator: "\n").last else { return nil }
        let text = line.hasPrefix("rad: ") ? line.dropFirst(5) : line
        return text.prefix(1).uppercased() + text.dropFirst()
    }

    /// Opens the log for appending, moving it to rad.log.1 first once it
    /// passes 10 MB.
    nonisolated private static func openLog(_ url: URL) -> FileHandle? {
        let files = FileManager.default
        try? files.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        if let size = try? url.resourceValues(forKeys: [.fileSizeKey]).fileSize, size > 10_000_000 {
            let old = url.appendingPathExtension("1")
            try? files.removeItem(at: old)
            try? files.moveItem(at: url, to: old)
        }
        let fd = open(url.path, O_WRONLY | O_CREAT | O_APPEND | O_CLOEXEC, 0o644)
        return fd < 0 ? nil : FileHandle(fileDescriptor: fd, closeOnDealloc: true)
    }

    // The handlers below run on Foundation's threads, so they are made here,
    // outside the main actor, and hop back to it.

    nonisolated private static func readLines(_ handle: FileHandle, _ onLine: @escaping @MainActor (Data) -> Void) {
        let buffer = LineBuffer()
        handle.readabilityHandler = { handle in
            let data = handle.availableData
            if data.isEmpty {
                handle.readabilityHandler = nil
                return
            }
            for line in buffer.append(data) {
                DispatchQueue.main.async { MainActor.assumeIsolated { onLine(line) } }
            }
        }
    }

    nonisolated private static func onExit(
        _ body: @escaping @MainActor (Int32, Process.TerminationReason) -> Void
    ) -> @Sendable (Process) -> Void {
        { process in
            let (status, reason) = (process.terminationStatus, process.terminationReason)
            DispatchQueue.main.async { MainActor.assumeIsolated { body(status, reason) } }
        }
    }

    /// Runs rad to completion, e.g. `rad pair --print-url`.
    nonisolated static func run(
        _ arguments: [String], environment: [String: String], executable: URL = RadBinary.bundled
    ) async throws -> (status: Int32, output: String) {
        let p = Process()
        p.executableURL = executable
        p.arguments = arguments
        p.environment = environment
        let output = Pipe()
        p.standardOutput = output
        p.standardError = output
        p.standardInput = FileHandle.nullDevice
        return try await withCheckedThrowingContinuation { done in
            p.terminationHandler = { p in
                let data = output.fileHandleForReading.readDataToEndOfFile()
                done.resume(returning: (p.terminationStatus, String(decoding: data, as: UTF8.self)))
            }
            do {
                try p.run()
            } catch {
                done.resume(throwing: error)
            }
        }
    }
}

/// Splits a byte stream into lines. Only the readability handler's queue
/// touches it.
private final class LineBuffer: @unchecked Sendable {
    private var pending = Data()

    func append(_ data: Data) -> [Data] {
        pending.append(data)
        var lines: [Data] = []
        while let newline = pending.firstIndex(of: UInt8(ascii: "\n")) {
            lines.append(pending[pending.startIndex..<newline])
            pending = pending[pending.index(after: newline)...]
        }
        return lines
    }
}
