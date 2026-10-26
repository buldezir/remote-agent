import RAKit
import SwiftUI

struct ServersView: View {
    @Environment(ServerStore.self) private var store
    @Binding var pendingLink: PairingLink?
    @State private var showAdd = false
    @State private var path = NavigationPath()
    @State private var autoOpened = false
    @State private var removing: SavedServer?
    @State private var newSession: NewSessionRequest?
    /// A session started empty from a server's menu, whose prompt takes the
    /// keyboard when it opens.
    @State private var startedEmpty: SessionRoute?
    @State private var managing: ServerRequest?

    var body: some View {
        NavigationStack(path: $path) {
            List {
                ForEach(store.servers) { server in
                    NavigationLink(value: server) {
                        ServerRow(connection: store.connection(for: server))
                    }
                    .listRowInsets(Metrics.rowInsets)
                    .swipeActions {
                        Button("Remove", systemImage: "trash") { removing = server }
                            .tint(Palette.red)
                    }
                    .contextMenu { ServerMenuItems(connection: store.connection(for: server)) }
                }
                .paletteRows()
            }
            .environment(\.sessionActions, SessionActions(
                startSession: { newSession = $0 },
                remove: { removing = $0 },
                manage: { managing = ServerRequest(connection: $0) }))
            .listStyle(.plain)
            .listBackground(Palette.base)
            .overlay {
                if store.servers.isEmpty {
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
            .navigationTitle("Servers")
            .navigationDestination(for: SavedServer.self) { server in
                ServerHomeView(connection: store.connection(for: server))
            }
            // Registered here, not in ServerHomeView: SwiftUI can keep a pushed
            // view's destination closure, which would then open a session on the
            // previously visited server.
            .navigationDestination(for: SessionRoute.self) { route in
                if let server = store.servers.first(where: { $0.id == route.serverID }) {
                    let connection = store.connection(for: server)
                    SessionView(connection: connection, store: connection.sessionStore(route.sessionID), focusesPrompt: route == startedEmpty)
                }
            }
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    SettingsButton()
                }
                ToolbarItem(placement: .primaryAction) {
                    Button("Add", systemImage: "plus") { showAdd = true }
                }
            }
            .sheet(isPresented: $showAdd) {
                AddServerView(initialLink: pendingLink) { server in
                    path.append(server)
                }
                .appStyle()
            }
            .sheet(item: $newSession) { request in
                NewSessionView(connection: request.connection, projectID: request.projectID) { session, empty in
                    let route = SessionRoute(serverID: request.connection.server.id, sessionID: session.id)
                    startedEmpty = empty ? route : nil
                    path.append(request.connection.server)
                    path.append(route)
                }
                .appStyle()
            }
            .sheet(item: $managing) { request in
                ServerSettingsView(connection: request.connection)
                    .appStyle()
            }
            // Only when it first opens: back from it, it was closed.
            .onChange(of: path.count) { _, count in
                if count < 2 { startedEmpty = nil }
            }
            .onChange(of: pendingLink) { _, link in
                if link != nil { showAdd = true }
            }
            .onChange(of: showAdd) { _, shown in
                if !shown { pendingLink = nil }
            }
            .removeServerDialog($removing)
            // Every server connects, so that its menu can open its settings and
            // its dot says how it is; one that was given up on waits for a tap.
            .task(id: store.servers.map(\.id)) {
                for c in store.connections where c.state == .idle { c.start() }
            }
            .onAppear {
                // With a single server, open it once at launch. Not on every
                // appearance: that would bounce the user back in when they
                // navigate back to this list.
                guard !autoOpened else { return }
                autoOpened = true
                if path.isEmpty, store.servers.count == 1, let s = store.servers.first {
                    path.append(s)
                }
            }
        }
    }
}

struct ServerRow: View {
    let connection: ServerConnection

    var body: some View {
        HStack(spacing: 12) {
            Image(systemName: "desktopcomputer")
                .font(.title2)
                .foregroundStyle(.tint)
            VStack(alignment: .leading, spacing: 2) {
                Text(connection.server.name).font(.headline)
                Text(connection.server.urls.first?.host() ?? "")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Spacer()
            ConnectionDot(state: connection.state)
        }
        .paletteText()
    }
}
