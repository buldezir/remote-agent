import RAKit
import SwiftUI

struct ServersView: View {
    @Environment(ServerStore.self) private var store
    @Binding var pendingLink: PairingLink?
    @State private var showAdd = false
    @State private var path = NavigationPath()

    var body: some View {
        NavigationStack(path: $path) {
            List {
                ForEach(store.servers) { server in
                    NavigationLink(value: server) {
                        ServerRow(connection: store.connection(for: server))
                    }
                    .swipeActions {
                        Button("Forget", role: .destructive) { store.remove(server) }
                    }
                }
            }
            .overlay {
                if store.servers.isEmpty {
                    ContentUnavailableView {
                        Label("No servers", systemImage: "desktopcomputer")
                    } description: {
                        Text("Run `rad serve` on your computer, then `rad pair` and scan the QR code.")
                    } actions: {
                        Button("Pair a server") { showAdd = true }
                            .buttonStyle(.borderedProminent)
                    }
                }
            }
            .navigationTitle("Servers")
            .navigationDestination(for: SavedServer.self) { server in
                ServerHomeView(connection: store.connection(for: server))
            }
            .toolbar {
                ToolbarItem(placement: .primaryAction) {
                    Button("Add", systemImage: "plus") { showAdd = true }
                }
            }
            .sheet(isPresented: $showAdd) {
                AddServerView(initialLink: pendingLink) { server in
                    path.append(server)
                }
            }
            .onChange(of: pendingLink) { _, link in
                if link != nil { showAdd = true }
            }
            .onChange(of: showAdd) { _, shown in
                if !shown { pendingLink = nil }
            }
            .onAppear {
                // With a single server, go straight to it.
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
        .padding(.vertical, 4)
    }
}

struct ConnectionDot: View {
    let state: ConnectionState

    var body: some View {
        Circle()
            .fill(color)
            .frame(width: 9, height: 9)
            .accessibilityLabel(label)
    }

    var color: Color {
        switch state {
        case .connected: .green
        case .connecting: .yellow
        case .failed: .red
        case .idle: .gray.opacity(0.5)
        }
    }

    var label: String {
        switch state {
        case .connected: "Connected"
        case .connecting: "Connecting"
        case .failed(let msg): "Failed: \(msg)"
        case .idle: "Not connected"
        }
    }
}
