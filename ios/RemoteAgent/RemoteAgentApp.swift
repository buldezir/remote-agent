import RAKit
import SwiftUI

@main
struct RemoteAgentApp: App {
    @State private var store = ServerStore()
    @State private var pendingLink: PairingLink?
    @Environment(\.scenePhase) private var phase

    var body: some Scene {
        WindowGroup {
            ServersView(pendingLink: $pendingLink)
                .environment(store)
                .onOpenURL { url in
                    pendingLink = PairingLink(string: url.absoluteString)
                }
        }
        .onChange(of: phase) { _, newPhase in
            // Sockets don't survive backgrounding; reconnect and resync on return.
            store.setActive(newPhase == .active)
        }
    }
}
