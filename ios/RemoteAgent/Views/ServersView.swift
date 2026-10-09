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
                    .swipeActions {
                        Button("Remove", systemImage: "trash") { removing = server }
                            .tint(.red)
                    }
                    .contextMenu {
                        Button("Remove", systemImage: "trash", role: .destructive) { removing = server }
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

extension View {
    /// Confirmation before forgetting a server.
    func removeServerDialog(_ server: Binding<SavedServer?>, onRemove: @escaping () -> Void = {}) -> some View {
        modifier(RemoveServerDialog(server: server, onRemove: onRemove))
    }
}

private struct RemoveServerDialog: ViewModifier {
    @Environment(ServerStore.self) private var store
    @Binding var server: SavedServer?
    var onRemove: () -> Void

    func body(content: Content) -> some View {
        content.confirmationDialog(
            "Remove \(server?.name ?? "server")?",
            isPresented: .init(get: { server != nil }, set: { if !$0 { server = nil } }),
            titleVisibility: .visible, presenting: server
        ) { s in
            Button("Remove", role: .destructive) {
                onRemove()
                store.remove(s)
            }
        } message: { _ in
            Text("This device is unpaired from the server. Run `rad pair` on the computer to add it again.")
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
