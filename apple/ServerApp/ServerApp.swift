import AppKit
import ServiceManagement
import SwiftUI

/// Remote Agent Server: a menu bar app that runs rad on this Mac. rad and the
/// agents it starts are this app's children, so the permissions granted to
/// it in Permissions… apply to them.
@main
struct ServerApp: App {
    @NSApplicationDelegateAdaptor private var delegate: AppDelegate

    var body: some Scene {
        MenuBarExtra {
            MenuContent(rad: delegate.rad, updater: delegate.updater, loginItem: delegate.loginItem)
        } label: {
            Image(nsImage: menuBarIcon(running: delegate.rad.state == .running))
        }
        .menuBarExtraStyle(.menu)

        Window("Permissions", id: "permissions") {
            PermissionsView(rad: delegate.rad)
        }
        .windowResizability(.contentSize)
        .restorationBehavior(.disabled)
        // The first launch starts with the permissions agents need.
        .defaultLaunchBehavior(UserDefaults.standard.bool(forKey: "onboarded") ? .suppressed : .presented)

        Window("Pair a Device", id: "pair") {
            PairView(rad: delegate.rad)
        }
        .windowResizability(.contentSize)
        .restorationBehavior(.disabled)
    }
}

/// The symbol, named for VoiceOver after the app rather than the symbol.
@MainActor
private func menuBarIcon(running: Bool) -> NSImage {
    let name = running ? "antenna.radiowaves.left.and.right" : "antenna.radiowaves.left.and.right.slash"
    let image = NSImage(systemSymbolName: name, accessibilityDescription: "Remote Agent Server")!
    image.isTemplate = true
    return image
}

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    let rad: RadSupervisor
    let updater: RadUpdater
    let loginItem = LoginItem()

    override init() {
        let rad = RadSupervisor()
        self.rad = rad
        updater = RadUpdater(rad: rad)
        super.init()
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        Task { await rad.start() }
        updater.startChecking()
    }

    func applicationDidBecomeActive(_ notification: Notification) {
        loginItem.refresh()
    }

    /// Quitting stops rad and its agents first.
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        guard rad.isActive else { return .terminateNow }
        rad.stop { NSApp.reply(toApplicationShouldTerminate: true) }
        return .terminateLater
    }
}

/// Start at Login, through the system's login items.
@MainActor @Observable
final class LoginItem {
    private(set) var status = SMAppService.mainApp.status

    func refresh() {
        status = SMAppService.mainApp.status
    }

    func set(_ on: Bool) {
        do {
            if on {
                try SMAppService.mainApp.register()
            } else {
                try SMAppService.mainApp.unregister()
            }
        } catch {
            NSAlert(error: error).runModal()
        }
        refresh()
    }
}

/// openWindow for an app without a Dock icon: the window must also come to
/// the front.
@MainActor
func show(_ id: String, with openWindow: OpenWindowAction) {
    openWindow(id: id)
    NSApp.activate()
}

struct MenuContent: View {
    let rad: RadSupervisor
    let updater: RadUpdater
    let loginItem: LoginItem
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        Text(status)
        if case .busy(let message) = rad.state {
            Text(message)
        }
        if let note = rad.note {
            Text(note)
        }
        if let ready = rad.ready {
            ForEach(ready.urls, id: \.self) { Text($0) }
            if ready.pairedDevices == 0 {
                Text("No devices paired yet")
            }
        }
        if serviceInstalled {
            Divider()
            Text("rad is also installed as a background service")
            Button("Use This App Instead…") { replaceService() }
        }
        agentsSection
        Divider()
        Button("Pair a Device…") { show("pair", with: openWindow) }
        Button("Permissions…") { show("permissions", with: openWindow) }
        Divider()
        Toggle("Start at Login", isOn: Binding(get: { loginItem.status == .enabled }, set: loginItem.set))
        if loginItem.status == .requiresApproval {
            Button("Allow in Login Items…") { SMAppService.openSystemSettingsLoginItems() }
        }
        switch rad.state {
        case .running, .starting:
            Button("Restart rad") { rad.restart() }
        default:
            Button(rad.state == .stopped ? "Start rad" : "Try Again") { Task { await rad.start() } }
        }
        updateItems
        Button("Open Log") { NSWorkspace.shared.open(rad.logURL) }
        Divider()
        Button("Quit and Stop rad") { NSApp.terminate(nil) }
            .keyboardShortcut("q")
    }

    private var status: String {
        switch rad.state {
        case .stopped: "rad is stopped"
        case .starting: "Starting rad…"
        case .running: "rad \(rad.version) is running" + (RadBinary.updatable ? "" : " (development build)")
        case .busy: "rad is already running elsewhere"
        case .failed(let message): "rad stopped: \(message)"
        }
    }

    @ViewBuilder private var updateItems: some View {
        if RadBinary.updatable {
            switch updater.state {
            case .available(let release):
                Button("Update rad to \(release.version)…") { Task { await updater.update() } }
                if let url = release.url {
                    Button("Release Notes") { NSWorkspace.shared.open(url) }
                }
            case .updating(let version):
                Text("Updating rad to \(version)…")
            case .checking:
                Text("Checking for Updates…")
            case .idle:
                Button("Check for Updates…") { Task { await updater.check(manual: true) } }
            }
        }
    }

    @ViewBuilder private var agentsSection: some View {
        if let agents = rad.agents?.filter(\.installed) {
            Divider()
            Section("Agents") {
                if agents.isEmpty {
                    Text("No agent CLIs found")
                }
                ForEach(agents) { agent in
                    if agent.authOk {
                        Text([agent.name, agent.version].compactMap(\.self).joined(separator: " "))
                    } else {
                        Text("\(agent.name): \(agent.hint ?? "not ready")")
                    }
                }
            }
        }
    }

    /// `rad install-service` writes this LaunchAgent. With RAD_HOME set the
    /// app serves a different config, so the two don't compete.
    private var serviceInstalled: Bool {
        ProcessInfo.processInfo.environment["RAD_HOME"] == nil && FileManager.default.fileExists(
            atPath: FileManager.default.homeDirectoryForCurrentUser
                .appendingPathComponent("Library/LaunchAgents/dev.remote-agent.rad.plist").path)
    }

    private func replaceService() {
        let alert = NSAlert()
        alert.messageText = "Remove rad's background service?"
        alert.informativeText = "The service stops, along with any agents it is running, and rad starts again inside this app. Turn on Start at Login to keep it running."
        alert.addButton(withTitle: "Remove Service")
        alert.addButton(withTitle: "Cancel")
        NSApp.activate()
        guard alert.runModal() == .alertFirstButtonReturn else { return }
        Task {
            let result = try? await RadSupervisor.run(["install-service", "--uninstall"], environment: rad.environment, executable: rad.executable)
            if let result, result.status != 0 {
                NSAlert(error: NSError(domain: "rad", code: Int(result.status),
                    userInfo: [NSLocalizedDescriptionKey: result.output])).runModal()
                return
            }
            // launchd may take a moment to stop the old rad and free its lock.
            for _ in 0..<10 {
                await rad.start()
                while rad.state == .starting { try? await Task.sleep(for: .milliseconds(200)) }
                guard case .busy = rad.state else { return }
                try? await Task.sleep(for: .seconds(1))
            }
        }
    }
}
