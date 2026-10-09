import RAKit
import SwiftUI

/// The iPad layout: one server's sessions in the sidebar, the chosen session
/// beside them. The title menu switches servers. In a narrow window it
/// collapses into a stack, like the iPhone.
struct ServerSplitView: View {
    @Environment(ServerStore.self) private var store
    @Binding var pendingLink: PairingLink?
    // Kept per window, so each iPad window has its own server and session.
    @SceneStorage("server") private var serverID = ""
    @SceneStorage("session") private var sessionID = ""
    @State private var showAdd = false

    /// The chosen server, or the first one if it was removed.
    private var server: SavedServer? {
        store.servers.first { $0.id == serverID } ?? store.servers.first
    }

    /// The open session. A session saved for a server that was removed since
    /// doesn't carry over to the server shown in its place.
    private var openSession: String? {
        guard let server, server.id == serverID, !sessionID.isEmpty else { return nil }
        return sessionID
    }

    var body: some View {
        NavigationSplitView {
            // As wide as an iPhone, which the rows and toolbar were laid out for.
            sidebar
                .navigationSplitViewColumnWidth(min: 320, ideal: 390, max: 480)
        } detail: {
            detail
        }
        .sheet(isPresented: $showAdd) {
            AddServerView(initialLink: pendingLink) { server in
                serverID = server.id
            }
        }
        .onChange(of: pendingLink) { _, link in
            if link != nil { showAdd = true }
        }
        .onChange(of: showAdd) { _, shown in
            if !shown { pendingLink = nil }
        }
        .onChange(of: server?.id) {
            sessionID = ""
        }
    }

    @ViewBuilder private var sidebar: some View {
        if let server {
            ServerHomeView(connection: store.connection(for: server), selection: selection(on: server))
                .id(server.id)
                .toolbarTitleMenu { serverMenu }
        } else {
            List {}
                .navigationTitle("Servers")
                .toolbar {
                    ToolbarItem(placement: .primaryAction) {
                        Button("Add", systemImage: "plus") { showAdd = true }
                    }
                }
        }
    }

    @ViewBuilder private var serverMenu: some View {
        Picker("Server", selection: Binding(get: { server?.id ?? "" }, set: { serverID = $0 })) {
            ForEach(store.servers) { s in
                Text(s.name).tag(s.id)
            }
        }
        Button("Pair a server", systemImage: "plus") { showAdd = true }
    }

    private func selection(on server: SavedServer) -> Binding<SessionRoute?> {
        Binding {
            openSession.map { SessionRoute(serverID: server.id, sessionID: $0) }
        } set: { route in
            serverID = server.id
            sessionID = route?.sessionID ?? ""
        }
    }

    @ViewBuilder private var detail: some View {
        if let server, let sessionID = openSession {
            let connection = store.connection(for: server)
            if connection.indexSynced, connection.sessions[sessionID] == nil {
                ContentUnavailableView("Session not found", systemImage: "questionmark.bubble",
                                       description: Text("It is no longer on \(server.name)."))
            } else {
                // A new identity per session, so the draft and the subscription don't carry over.
                SessionView(connection: connection, store: connection.sessionStore(sessionID))
                    .id(sessionID)
            }
        } else if store.servers.isEmpty {
            ContentUnavailableView {
                Label("No servers", systemImage: "desktopcomputer")
            } description: {
                Text("Run `rad serve` on your computer, then `rad pair` and scan the QR code.")
            } actions: {
                Button("Pair a server") { showAdd = true }
                    .buttonStyle(.borderedProminent)
            }
        } else {
            ContentUnavailableView("No session selected", systemImage: "bubble.left.and.text.bubble.right",
                                   description: Text("Choose a session in the sidebar, or start a new one."))
        }
    }
}
