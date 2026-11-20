import RAKit
import SwiftUI

@main
struct RemoteAgentApp: App {
    @State private var store: ServerStore
    /// Search across the servers' sessions, kept while the app runs.
    @State private var search: SessionSearch
    /// The Settings sheet over the window.
    @State private var showSettings = false
    @Environment(\.scenePhase) private var phase

    init() {
        let store = ServerStore()
        _store = State(initialValue: store)
        _search = State(initialValue: SessionSearch(store: store))
    }

    var body: some Scene {
        // One window: on iPad, project.yml turns off multiple scenes.
        WindowGroup(id: "main") {
            WindowRoot(showSettings: $showSettings)
                .environment(store)
                .environment(search)
        }
        .onChange(of: phase) { _, newPhase in
            // Sockets don't survive backgrounding; reconnect and resync on return.
            store.setActive(newPhase == .active)
        }
    }
}

/// The app's window: the Inbox and Servers tabs on iPhone, the split view on
/// iPad. A pairing link opens the pairing sheet in it, and Settings is a
/// sheet over it.
private struct WindowRoot: View {
    @Binding var showSettings: Bool
    @State private var pendingLink: PairingLink?
    @Environment(\.horizontalSizeClass) private var sizeClass

    var body: some View {
        Group {
            // By device, not size class: the split view collapses in a narrow
            // iPad window by itself, and swapping roots would drop the navigation.
            if UIDevice.current.userInterfaceIdiom == .pad {
                ServerSplitView(pendingLink: $pendingLink)
            } else {
                PhoneTabs(pendingLink: $pendingLink)
            }
        }
        .onOpenURL { url in
            pendingLink = PairingLink(string: url.absoluteString)
        }
        .environment(\.showSettings, $showSettings)
        .sheet(isPresented: $showSettings) {
            NavigationStack { SettingsView(wide: sizeClass == .regular) }
                .appStyle()
        }
        .appStyle()
    }
}
