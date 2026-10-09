import RAKit
import SwiftUI

@main
struct RemoteAgentApp: App {
    @State private var store = ServerStore()
    @Environment(\.scenePhase) private var phase

    var body: some Scene {
        WindowGroup {
            WindowRoot()
                .environment(store)
        }
        .onChange(of: phase) { _, newPhase in
            // Sockets don't survive backgrounding; reconnect and resync on return.
            store.setActive(newPhase == .active)
        }
    }
}

/// One window. On iPad there can be several, and a pairing link opens in the
/// window that received it.
private struct WindowRoot: View {
    @State private var pendingLink: PairingLink?

    var body: some View {
        Group {
            // By device, not size class: the split view collapses in a narrow
            // iPad window by itself, and swapping roots would drop the navigation.
            if UIDevice.current.userInterfaceIdiom == .pad {
                ServerSplitView(pendingLink: $pendingLink)
            } else {
                ServersView(pendingLink: $pendingLink)
            }
        }
        .onOpenURL { url in
            pendingLink = PairingLink(string: url.absoluteString)
        }
    }
}
