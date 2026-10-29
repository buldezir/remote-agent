import RAKit
import SwiftUI

/// The iPhone's two ways in: the Inbox, every server's sessions by state,
/// and the Servers, each with its own list of sessions. Each tab keeps its
/// own place, and the tab bar hides while a session is open.
struct PhoneTabs: View {
    @Environment(ServerStore.self) private var store
    @Binding var pendingLink: PairingLink?
    @State private var tab = Tab.inbox

    enum Tab: Hashable {
        case inbox, servers
    }

    var body: some View {
        TabView(selection: $tab) {
            SwiftUI.Tab("Inbox", systemImage: "tray", value: Tab.inbox) {
                InboxView()
            }
            .badge(store.sessions(archived: false).count { $0.session.standsOut })
            SwiftUI.Tab("Servers", systemImage: "desktopcomputer", value: Tab.servers) {
                ServersView(pendingLink: $pendingLink)
            }
        }
        // The Servers tab pairs, and opens the new server.
        .onChange(of: pendingLink) { _, link in
            if link != nil { tab = .servers }
        }
    }
}
