import RAKit
import SwiftUI

@main
struct RemoteAgentApp: App {
    @State private var store = ServerStore()
    @Environment(\.scenePhase) private var phase

    var body: some Scene {
        WindowGroup(id: "main") {
            WindowRoot()
                .environment(store)
        }
        #if os(macOS)
        .defaultSize(width: 1100, height: 760)
        .commands { AppCommands() }
        #endif
        .onChange(of: phase) { _, newPhase in
            #if os(macOS)
            // A Mac window stays in view while another app is in front.
            store.setActive(newPhase != .background)
            #else
            // Sockets don't survive backgrounding; reconnect and resync on return.
            store.setActive(newPhase == .active)
            #endif
        }
        #if os(macOS)
        Settings {
            SettingsView()
                .frame(width: 460)
                // As tall as the form, which grows with the text sizes.
                .fixedSize(horizontal: false, vertical: true)
                .appTextSizes()
        }
        .windowResizability(.contentSize)
        #endif
    }
}

/// One window. On iPad and the Mac there can be several, and a pairing link
/// opens in the window that received it.
private struct WindowRoot: View {
    @State private var pendingLink: PairingLink?
    #if os(iOS)
    @State private var showSettings = false
    #endif

    var body: some View {
        Group {
            #if os(macOS)
            ServerSplitView(pendingLink: $pendingLink)
            #else
            // By device, not size class: the split view collapses in a narrow
            // iPad window by itself, and swapping roots would drop the navigation.
            if UIDevice.current.userInterfaceIdiom == .pad {
                ServerSplitView(pendingLink: $pendingLink)
            } else {
                ServersView(pendingLink: $pendingLink)
            }
            #endif
        }
        .onOpenURL { url in
            pendingLink = PairingLink(string: url.absoluteString)
        }
        #if os(macOS)
        // Otherwise the Mac opens a new window for every link.
        .handlesExternalEvents(preferring: ["*"], allowing: ["*"])
        #else
        .environment(\.showSettings, $showSettings)
        .sheet(isPresented: $showSettings) {
            NavigationStack { SettingsView() }
                .appTextSizes()
        }
        #endif
        .appTextSizes()
    }
}

extension FocusedValues {
    /// Shows the New session sheet in the front window, while its server is connected.
    @Entry var newSession: Binding<Bool>?
    /// Shows the pairing sheet in the front window.
    @Entry var pairServer: Binding<Bool>?
}
