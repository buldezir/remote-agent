import RAKit
import SwiftUI

struct ServersView: View {
    @Environment(ServerStore.self) private var store
    @Binding var pendingLink: PairingLink?
    @State private var showAdd = false
    @State private var path = NavigationPath()
    @State private var autoOpened = false
    @State private var removing: SavedServer?

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
                    .contextMenu {
                        Button("Remove", systemImage: "trash", role: .destructive) { removing = server }
                    }
                }
                .paletteRows()
            }
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
                    SessionView(connection: connection, store: connection.sessionStore(route.sessionID))
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
            .onChange(of: pendingLink) { _, link in
                if link != nil { showAdd = true }
            }
            .onChange(of: showAdd) { _, shown in
                if !shown { pendingLink = nil }
            }
            .removeServerDialog($removing)
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
