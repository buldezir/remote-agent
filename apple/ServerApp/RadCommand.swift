import AppKit
import Observation

/// `rad` in Terminal: a link at /usr/local/bin/rad, a folder every Mac's PATH
/// has (/etc/paths), to the rad inside this app. An update replaces the app
/// around it, so the link keeps running the app's rad, and `rad update` has
/// the app update itself (server/cmd/rad/appupdate.go).
@MainActor @Observable
final class RadCommand {
    nonisolated static let link = URL(fileURLWithPath: "/usr/local/bin/rad")

    /// The link runs this app's rad.
    private(set) var installed = RadCommand.linksHere()

    func refresh() {
        installed = Self.linksHere()
    }

    /// Links /usr/local/bin/rad to this app's rad, asking first if another
    /// rad is there. `environment` is the login shell's, whose PATH should
    /// find the link.
    func install(environment: [String: String]) {
        NSApp.activate()
        // fileExists follows the link: one into an app that has gone doesn't count.
        if FileManager.default.fileExists(atPath: Self.link.path) {
            let target = (try? FileManager.default.destinationOfSymbolicLink(atPath: Self.link.path)).map { " (it links to \($0))" } ?? ""
            let alert = NSAlert()
            alert.messageText = "Replace the rad in /usr/local/bin?"
            alert.informativeText = "\(Self.link.path) isn't this app's rad\(target). The rad command then runs this app's rad instead."
            alert.addButton(withTitle: "Replace")
            alert.addButton(withTitle: "Cancel")
            guard alert.runModal() == .alertFirstButtonReturn else { return }
        }
        do {
            guard try Self.makeLink() else { return }
        } catch {
            NSAlert(error: error).runModal()
            return
        }
        refresh()
        let alert = NSAlert()
        switch Self.firstOnPath(environment["PATH"] ?? "") {
        case let found? where Self.isBundled(found):
            alert.messageText = "rad is installed"
            alert.informativeText = "In Terminal, rad now runs this app's rad: rad pair, rad devices and so on. rad update has the app update itself, and rad with it."
        case let found?:
            alert.messageText = "Your shell finds another rad first"
            alert.informativeText = "\(found) comes before /usr/local/bin in your PATH. Remove it, or run \(Self.link.path)."
        case nil:
            alert.messageText = "/usr/local/bin isn't on your PATH"
            alert.informativeText = "Add it to PATH in your shell's profile, or run \(Self.link.path)."
        }
        alert.runModal()
    }

    nonisolated private static func linksHere() -> Bool {
        isBundled(link.path)
    }

    nonisolated private static func isBundled(_ path: String) -> Bool {
        URL(fileURLWithPath: path).resolvingSymlinksInPath().path == RadBinary.bundled.resolvingSymlinksInPath().path
    }

    /// The first rad on PATH.
    nonisolated private static func firstOnPath(_ path: String) -> String? {
        path.split(separator: ":").lazy
            .map { URL(fileURLWithPath: String($0)).appendingPathComponent("rad").path }
            .first { FileManager.default.isExecutableFile(atPath: $0) }
    }

    /// Puts the link in place: itself if it may write to /usr/local/bin, else
    /// with an administrator's password, since on Apple silicon the folder is
    /// root's, if it's there at all. False if the user cancelled.
    private static func makeLink() throws -> Bool {
        let target = RadBinary.bundled.path
        let temporary = link.deletingLastPathComponent().appendingPathComponent(".rad-\(getpid())").path
        if symlink(target, temporary) == 0 {
            if rename(temporary, link.path) == 0 { return true }
            unlink(temporary)
        }
        let command = "/bin/mkdir -p /usr/local/bin && /bin/ln -sfh \(shellQuoted(target)) \(link.path)"
        let source = "do shell script \(appleScriptString(command)) with prompt \"Remote Agent Server wants to put the rad command in /usr/local/bin.\" with administrator privileges"
        var error: NSDictionary?
        _ = NSAppleScript(source: source)?.executeAndReturnError(&error)
        guard let error else { return true }
        if error[NSAppleScript.errorNumber] as? Int == -128 { return false } // userCanceledErr
        throw NSError(domain: "RadCommand", code: 1, userInfo: [
            NSLocalizedDescriptionKey: "Couldn't put rad in /usr/local/bin.",
            NSLocalizedRecoverySuggestionErrorKey: error[NSAppleScript.errorMessage] as? String ?? "",
        ])
    }

    private static func shellQuoted(_ s: String) -> String {
        "'" + s.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }

    private static func appleScriptString(_ s: String) -> String {
        "\"" + s.replacingOccurrences(of: "\\", with: "\\\\").replacingOccurrences(of: "\"", with: "\\\"") + "\""
    }
}
