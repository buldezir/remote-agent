import RAKit
import SwiftUI

/// The iPad and Mac layout: one server's sessions in the sidebar, the chosen
/// session beside them. The title menu (on the Mac, the server menu at the
/// foot of the sidebar) switches servers. In a narrow iPad window it collapses
/// into a stack, like the iPhone.
struct ServerSplitView: View {
    @Environment(ServerStore.self) private var store
    @Binding var pendingLink: PairingLink?
    // Kept per window, so each window has its own server and session.
    @SceneStorage("server") private var serverID = ""
    @SceneStorage("session") private var sessionID = ""
    @State private var showAdd = false
    #if os(macOS)
    @State private var removing: SavedServer?
    #endif

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
            sidebar
                #if os(macOS)
                .navigationSplitViewColumnWidth(min: 240, ideal: 300, max: 420)
                #else
                // As wide as an iPhone, which the rows and toolbar were laid out for.
                .navigationSplitViewColumnWidth(min: 320, ideal: 390, max: 480)
                #endif
        } detail: {
            detail
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(Palette.base)
        }
        .sheet(isPresented: $showAdd) {
            AddServerView(initialLink: pendingLink) { server in
                serverID = server.id
            }
            .appStyle()
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
        #if os(macOS)
        .removeServerDialog($removing)
        #endif
        .focusedSceneValue(\.pairServer, $showAdd)
    }

    @ViewBuilder private var sidebar: some View {
        if let server {
            ServerHomeView(connection: store.connection(for: server), selection: selection(on: server))
                .id(server.id)
                #if os(macOS)
                .safeAreaInset(edge: .bottom, spacing: 0) { macServerMenu(server) }
                #else
                .toolbarTitleMenu { serverMenu }
                #endif
        } else {
            List {}
                .listBackground(Palette.mantle)
                .navigationTitle("Servers")
                .toolbar {
                    ToolbarItem(placement: .primaryAction) {
                        Button("Add", systemImage: "plus") { showAdd = true }
                    }
                    #if os(iOS)
                    ToolbarItem(placement: .secondaryAction) {
                        SettingsButton()
                    }
                    #endif
                }
        }
    }

    @ViewBuilder private var serverMenu: some View {
        Picker("Server", selection: Binding(get: { server?.id ?? "" }, set: { serverID = $0 })) {
            ForEach(store.servers) { s in
                Text(s.name).tag(s.id)
            }
        }
        #if os(macOS)
        .pickerStyle(.inline) // A Mac menu would put it in a submenu.
        #endif
        Button("Pair a server", systemImage: "plus") { showAdd = true }
    }

    #if os(macOS)
    /// At the foot of the sidebar: the server's name and status, and a menu
    /// with the server actions that iOS keeps in the sidebar's toolbar.
    private func macServerMenu(_ server: SavedServer) -> some View {
        let connection = store.connection(for: server)
        return Menu {
            serverMenu
            Divider()
            Button("Reconnect", systemImage: "arrow.clockwise") { connection.retry() }
            Button("Remove \(server.name)…", systemImage: "trash", role: .destructive) { removing = server }
        } label: {
            HStack(spacing: 8) {
                ConnectionDot(state: connection.state)
                Text(server.name).lineLimit(1).scaledFont(.body)
                Spacer(minLength: 0)
                Image(systemName: "chevron.up.chevron.down")
                    .scaledFont(.caption)
                    .foregroundStyle(.secondary)
            }
            .contentShape(Rectangle())
            .paletteText()
            .accessibilityElement(children: .ignore)
        }
        .menuStyle(.button)
        .buttonStyle(.plain)
        .menuIndicator(.hidden)
        .help(ConnectionDot(state: connection.state).label)
        .accessibilityLabel("\(server.name), \(ConnectionDot(state: connection.state).label)")
        .padding(.horizontal, 16)
        .padding(.vertical, 10)
    }
    #endif

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
                #if os(macOS)
                Text("Run `rad serve` on your computer, then `rad pair`, and paste the link it prints.")
                #else
                Text("Run `rad serve` on your computer, then `rad pair` and scan the QR code.")
                #endif
            } actions: {
                Button { showAdd = true } label: {
                    Text("Pair a server").foregroundStyle(Palette.onAccent)
                }
                .buttonStyle(.borderedProminent)
            }
        } else {
            ContentUnavailableView("No session selected", systemImage: "bubble.left.and.text.bubble.right",
                                   description: Text("Choose a session in the sidebar, or start a new one."))
                #if os(macOS)
                // The window's title; on iPad the sidebar shows the name.
                .navigationTitle(server?.name ?? "")
                #endif
        }
    }
}
