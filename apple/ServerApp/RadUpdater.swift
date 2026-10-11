import AppKit
import Foundation
import Observation

/// Update rad: the bundled rad looks for a newer release on GitHub
/// (`rad update --check`) and downloads it next to the app (`rad update --to`),
/// then rad restarts into it (RadBinary.choose). The bundled rad does the
/// work because it always has the updater, whichever rad is running.
@MainActor @Observable
final class RadUpdater {
    struct Release: Equatable {
        let version: String
        let url: URL?
        /// Sessions a restart would interrupt.
        let busy: [String]
    }

    enum State: Equatable {
        case idle
        case checking
        case available(Release)
        case updating(String)
    }

    private(set) var state: State = .idle
    private let rad: RadSupervisor
    @ObservationIgnored private var schedule: Task<Void, Never>?

    init(rad: RadSupervisor) {
        self.rad = rad
    }

    /// Checks shortly after launch, then once a day.
    func startChecking() {
        guard RadBinary.updatable, schedule == nil else { return }
        schedule = Task {
            try? await Task.sleep(for: .seconds(5))
            while !Task.isCancelled {
                await check(manual: false)
                try? await Task.sleep(for: .seconds(24 * 3600))
            }
        }
    }

    /// Looks for a newer release. A check from the menu answers with an
    /// alert; the daily one only updates the menu, and stays quiet offline.
    func check(manual: Bool) async {
        guard RadBinary.updatable, !busy else { return }
        let previous = state
        state = .checking
        do {
            guard let release = try await latest() else {
                state = .idle
                if manual { inform("rad is up to date", "\(rad.version) is the latest release.") }
                return
            }
            state = .available(release)
            if manual && ask("rad \(release.version) is available", "This Mac runs rad \(rad.version).", action: "Update") {
                await update()
            }
        } catch {
            state = previous
            if manual { inform("Couldn't check for updates", error.localizedDescription) }
        }
    }

    /// Downloads the latest release and restarts rad into it, after asking
    /// if that would interrupt sessions.
    func update() async {
        guard RadBinary.updatable, !busy else { return }
        state = .checking
        do {
            guard let release = try await latest() else {
                state = .idle
                return
            }
            state = .available(release)
            if !release.busy.isEmpty {
                let sessions = release.busy.count == 1 ? "a session" : "\(release.busy.count) sessions"
                guard ask("Interrupt \(sessions)?",
                          "Updating restarts rad, which interrupts \(release.busy.map { "“\($0)”" }.joined(separator: ", ")).",
                          action: "Update Anyway", cancel: "Cancel") else { return }
            }
            state = .updating(release.version)
            let result = try await RadSupervisor.run(
                ["update", "--to", RadBinary.downloads.path, "--json"], environment: rad.environment)
            guard result.status == 0 else { throw Self.failure(result.output) }
            rad.restart()
            for _ in 0..<150 {
                try? await Task.sleep(for: .milliseconds(200))
                if rad.ready?.version == release.version {
                    state = .idle
                    return
                }
                if rad.note != nil { break }
            }
            throw Self.failure(rad.note ?? "rad \(release.version) hasn't started.")
        } catch {
            state = .idle
            inform("Couldn't update rad", error.localizedDescription)
        }
    }

    private var busy: Bool {
        switch state {
        case .checking, .updating: true
        default: false
        }
    }

    /// The latest release if it is newer than the rad that runs.
    private func latest() async throws -> Release? {
        struct Check: Decodable {
            let latest: String
            let url: String
            let busy: [String]
        }
        let result = try await RadSupervisor.run(["update", "--check", "--json"], environment: rad.environment)
        guard result.status == 0, let line = result.output.split(separator: "\n").last,
              let check = try? JSONDecoder().decode(Check.self, from: Data(line.utf8))
        else { throw Self.failure(result.output) }
        guard let latest = ReleaseVersion(check.latest), let current = ReleaseVersion(rad.version),
              latest > current
        else { return nil }
        return Release(version: check.latest, url: URL(string: check.url), busy: check.busy)
    }

    /// rad's error, from the last line of its output ("rad: …").
    private static func failure(_ output: String) -> NSError {
        let line = output.split(separator: "\n").last.map(String.init) ?? "rad update failed."
        let message = line.hasPrefix("rad: ") ? String(line.dropFirst(5)) : line
        return NSError(domain: "rad", code: 1, userInfo: [NSLocalizedDescriptionKey: message])
    }

    private func inform(_ title: String, _ text: String) {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = text
        NSApp.activate()
        alert.runModal()
    }

    private func ask(_ title: String, _ text: String, action: String, cancel: String = "Later") -> Bool {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = text
        alert.addButton(withTitle: action)
        alert.addButton(withTitle: cancel)
        NSApp.activate()
        return alert.runModal() == .alertFirstButtonReturn
    }
}
