import AppKit
import SwiftUI

/// Grants for agents, made up front at the Mac. Opened on first launch.
struct PermissionsView: View {
    let rad: RadSupervisor
    @State private var permissions = Permissions()
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        Form {
            Section {
                Text("Agents run inside Remote Agent Server, so macOS asks about them under its name. Allow what your agents need now, while you're at the Mac: a prompt that comes up while you're away holds the agent until someone answers it here.")
                    .fixedSize(horizontal: false, vertical: true)
                if permissions.needsRestart {
                    HStack {
                        Text("Restart rad so running agents get the new permissions.")
                        Spacer()
                        Button("Restart rad") {
                            rad.restart()
                            permissions.clearRestart()
                        }
                    }
                }
            }
            Section("Files") {
                row(.desktop)
                row(.documents)
                row(.downloads)
                row(.fullDisk)
            }
            Section("Control this Mac") {
                row(.accessibility)
                row(.screenRecording)
                row(.automation)
            }
            Section("Network and tools") {
                row(.localNetwork)
                row(.developerTools)
            }
            HStack {
                Text("Next, pair your phone.")
                    .foregroundStyle(.secondary)
                Spacer()
                Button("Pair a Device…") { show("pair", with: openWindow) }
            }
        }
        .formStyle(.grouped)
        .frame(width: 600, height: 720)
        .onAppear { UserDefaults.standard.set(true, forKey: "onboarded") }
        .task {
            await permissions.refresh(slow: true)
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(2))
                await permissions.refresh()
            }
        }
        .onReceive(NotificationCenter.default.publisher(for: NSApplication.didBecomeActiveNotification)) { _ in
            Task { await permissions.refresh(slow: true) }
        }
    }

    private func row(_ p: Permission) -> some View {
        PermissionRow(permission: p, status: permissions.status[p]) {
            switch (p, permissions.status[p]) {
            case (.fullDisk, _), (.developerTools, _), (_, .denied):
                NSWorkspace.shared.open(p.settingsURL)
            default:
                Task { await permissions.request(p) }
            }
        }
    }
}

private struct PermissionRow: View {
    let permission: Permission
    let status: PermissionStatus?
    let action: () -> Void

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: icon)
                .font(.title3)
                .foregroundStyle(.secondary)
                .frame(width: 24)
            VStack(alignment: .leading, spacing: 3) {
                Text(title)
                Text(detail)
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                if permission == .fullDisk && status != .granted {
                    Button("Show App in Finder") { NSWorkspace.shared.activateFileViewerSelecting([Bundle.main.bundleURL]) }
                        .buttonStyle(.link)
                        .font(.callout)
                }
            }
            Spacer(minLength: 12)
            VStack(alignment: .trailing, spacing: 6) {
                statusLabel
                if let buttonTitle {
                    Button(buttonTitle, action: action)
                }
            }
        }
        .padding(.vertical, 2)
    }

    @ViewBuilder private var statusLabel: some View {
        switch status {
        case .granted:
            Label("Allowed", systemImage: "checkmark.circle.fill")
                .foregroundStyle(.green)
        case .denied:
            Label("Not allowed", systemImage: "xmark.circle.fill")
                .foregroundStyle(.orange)
        case .off:
            Text("Off")
                .foregroundStyle(.secondary)
        case .unavailable(let reason):
            Text(reason)
                .foregroundStyle(.secondary)
        case .notAsked, .unknown, nil:
            EmptyView()
        }
    }

    private var buttonTitle: String? {
        switch (permission, status) {
        case (_, .granted), (_, .unavailable), (_, nil): nil
        case (.fullDisk, _), (.developerTools, _), (_, .denied): "Open Settings…"
        default: "Allow…"
        }
    }

    private var title: String {
        switch permission {
        case .desktop: "Desktop folder"
        case .documents: "Documents folder"
        case .downloads: "Downloads folder"
        case .fullDisk: "Full Disk Access"
        case .accessibility: "Accessibility"
        case .screenRecording: "Screen Recording"
        case .automation: "Automation of System Events"
        case .localNetwork: "Local Network"
        case .developerTools: "Developer Tools"
        }
    }

    private var detail: String {
        switch permission {
        case .desktop: "Projects and files on your Desktop."
        case .documents: "Projects and files in Documents."
        case .downloads: "Files in Downloads."
        case .fullDisk: "Every file, including other apps' data, Mail and Time Machine. Covers the three folders above. In Settings, add this app with + or drag it into the list."
        case .accessibility: "Click, type and read other apps' windows, as UI scripting and computer-use tools do."
        case .screenRecording: "Take screenshots and read what's on screen. macOS asks to confirm this again every month."
        case .automation: "Send keystrokes and run AppleScript through System Events. Other apps ask the first time an agent scripts them."
        case .localNetwork: "Reach other devices on your network, such as dev servers. Your phone reaches rad without this. The switch is under Privacy & Security › Local Network."
        case .developerTools: "Programs your agents build run without Gatekeeper checks, and start faster. macOS can't report this one; in Settings, add this app with +."
        }
    }

    private var icon: String {
        switch permission {
        case .desktop: "menubar.dock.rectangle"
        case .documents: "doc"
        case .downloads: "arrow.down.circle"
        case .fullDisk: "internaldrive"
        case .accessibility: "accessibility"
        case .screenRecording: "rectangle.dashed.badge.record"
        case .automation: "applescript"
        case .localNetwork: "network"
        case .developerTools: "hammer"
        }
    }
}
