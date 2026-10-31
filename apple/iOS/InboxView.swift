import RAKit
import SwiftUI

/// The iPhone's Inbox tab: every server's sessions by state, as in the iPad's
/// Inbox sidebar, each pushing its session. The servers are the Servers tab's.
/// Its search field finds sessions on every server; Back from one that a
/// result opened comes back to the results.
struct InboxView: View {
    @Environment(ServerStore.self) private var store
    @State private var path: [SessionRoute] = []
    @State private var showAdd = false
    @State private var newSession: NewSessionRequest?
    /// A session started empty, whose prompt takes the keyboard when it opens.
    @State private var startedEmpty: SessionRoute?
    @State private var renaming: SessionRequest?
    @State private var archiving: SessionRequest?
    @State private var archivingOld: [ServerSession]?
    @State private var deleting: SessionRequest?
    @State private var removing: SavedServer?
    @State private var managing: ServerRequest?
    /// The item a search result opens the session at.
    @State private var itemFocus: ItemFocus?
    @FocusState private var searchFocused: Bool

    var body: some View {
        NavigationStack(path: $path) {
            // Always there: in the drawer, a phone shows it only when the
            // list is pulled down, where nobody finds it.
            SearchableSessions(placement: .navigationBarDrawer(displayMode: .always), selection: nil, focused: $searchFocused) {
                InboxSidebar(showsServers: false)
            }
            .sidebarList()
            .overlay {
                if store.servers.isEmpty { NoServersView(showAdd: $showAdd) }
            }
            .navigationTitle("Inbox")
            .toolbar { SidebarToolbar(showAdd: $showAdd) }
            .navigationDestination(for: SessionRoute.self) { route in
                if let server = store.servers.first(where: { $0.id == route.serverID }) {
                    let connection = store.connection(for: server)
                    SessionView(connection: connection, store: connection.sessionStore(route.sessionID),
                                focusesPrompt: route == startedEmpty, focusItem: $itemFocus) { fork, empty in
                        let next = SessionRoute(serverID: route.serverID, sessionID: fork.id)
                        startedEmpty = empty ? next : nil
                        path = [next]
                    }
                    // A view of its own for a session in another's place,
                    // which subscribes to it.
                    .id(route)
                    .toolbar(.hidden, for: .tabBar)
                }
            }
            .renameSessionAlert($renaming)
            .archiveSessionDialog($archiving)
            .archiveOldSessionsDialog($archivingOld)
            .deleteSessionDialog($deleting)
            .removeServerDialog($removing)
        }
        .environment(\.sessionActions, SessionActions(
            startSession: { newSession = $0 },
            rename: { renaming = $0 },
            archive: { archiving = $0 },
            archiveOld: { archivingOld = $0 },
            delete: { deleting = $0 },
            remove: { removing = $0 },
            manage: { managing = ServerRequest(connection: $0) },
            open: { route, itemID in
                itemFocus = itemID.map { ItemFocus(route: route, itemID: $0) }
                path = [route]
            }))
        .sheet(item: $newSession) { request in
            NewSessionView(connection: request.connection, projectID: request.projectID) { session, empty in
                let route = SessionRoute(serverID: request.connection.server.id, sessionID: session.id)
                startedEmpty = empty ? route : nil
                path = [route]
            }
            .appStyle()
        }
        .sheet(item: $managing) { request in
            ServerSettingsView(connection: request.connection)
                .appStyle()
        }
        .sheet(isPresented: $showAdd) {
            AddServerView(initialLink: nil) { _ in }
                .appStyle()
        }
        // Only when it first opens.
        .onChange(of: path) { _, path in
            if path.last != startedEmpty { startedEmpty = nil }
            if path.last != itemFocus?.route { itemFocus = nil }
        }
        // ⇧⌘F searches, from a keyboard.
        .background {
            Button("Search Sessions") {
                path = []
                searchFocused = true
            }
            .keyboardShortcut("f", modifiers: [.command, .shift])
            // hidden() would turn the shortcut off.
            .opacity(0)
            .accessibilityHidden(true)
        }
        // Every server connects; one that was given up on waits for a tap.
        .task(id: store.servers.map(\.id)) {
            for c in store.connections where c.state == .idle { c.start() }
        }
    }
}

/// With no servers paired, how to pair one.
struct NoServersView: View {
    @Binding var showAdd: Bool

    var body: some View {
        ContentUnavailableView {
            Label("No servers", systemImage: "desktopcomputer")
        } description: {
            Text("Run `rad serve` on your computer, then `rad pair` and scan the QR code.")
        } actions: {
            Button { showAdd = true } label: {
                Text("Pair a server").foregroundStyle(Palette.onAccent)
            }
            .buttonStyle(.borderedProminent)
        }
    }
}
