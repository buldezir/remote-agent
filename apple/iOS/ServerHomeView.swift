import RAKit
import SwiftUI

/// A server's sessions on iPhone, pushed from the server list in the Servers
/// tab. It pushes sessions in turn. The Inbox tab, and the iPad's sidebar,
/// have every server's.
struct ServerHomeView: View {
    let connection: ServerConnection
    @Environment(\.dismiss) private var dismiss
    @State private var removing: SavedServer?
    @State private var showSettings = false
    @State private var showNew = false
    @State private var openSession: String?
    /// A session started empty, whose prompt takes the keyboard when it opens.
    @State private var startedEmpty: String?
    @State private var showArchived = false
    @State private var archiving: SessionRequest?
    @State private var deleting: SessionRequest?

    var body: some View {
        List { sections }
            .listStyle(.plain)
            .listBackground(Palette.base)
            .overlay {
                if connection.indexSynced && connection.sessions.isEmpty {
                    ContentUnavailableView {
                        Label("No sessions yet", systemImage: "bubble.left.and.text.bubble.right")
                    } description: {
                        Text("Start an agent in one of your projects.")
                    } actions: {
                        Button { showNew = true } label: {
                            Text("New session").foregroundStyle(Palette.onAccent)
                        }
                        .buttonStyle(.borderedProminent)
                    }
                } else if !connection.indexSynced && connection.state == .connecting {
                    ProgressView("Connecting…")
                }
            }
            .navigationTitle(connection.server.name)
            .toolbar {
                ToolbarItem(placement: .primaryAction) {
                    Button("New session", systemImage: "square.and.pencil") { showNew = true }
                        .keyboardShortcut("n")
                        .disabled(connection.state != .connected)
                }
                ToolbarItem(placement: .secondaryAction) {
                    Button("Reconnect", systemImage: "arrow.clockwise") { connection.retry() }
                }
                ToolbarItem(placement: .secondaryAction) {
                    Button("Server Settings", systemImage: "gearshape.2") { showSettings = true }
                        .disabled(connection.state != .connected)
                }
                ToolbarItem(placement: .secondaryAction) {
                    SettingsButton()
                }
                ToolbarItem(placement: .secondaryAction) {
                    Button("Remove server", systemImage: "trash", role: .destructive) { removing = connection.server }
                }
            }
            .removeServerDialog($removing) { dismiss() }
            .sheet(isPresented: $showNew) {
                NewSessionView(connection: connection) { session, empty in
                    startedEmpty = empty ? session.id : nil
                    openSession = session.id
                }
                .appStyle()
            }
            .sheet(isPresented: $showSettings) {
                ServerSettingsView(connection: connection)
                    .appStyle()
            }
            .archiveSessionDialog($archiving)
            .deleteSessionDialog($deleting)
            .refreshable {
                if connection.state != .connected { connection.retry() }
                await connection.refreshHarnesses(force: true)
            }
            .navigationDestination(item: $openSession) { id in
                SessionView(connection: connection, store: connection.sessionStore(id), focusesPrompt: id == startedEmpty) { fork, empty in
                    startedEmpty = empty ? fork.id : nil
                    openSession = fork.id
                }
                // A fork takes the source's place: a view of its own, which
                // subscribes to it.
                .id(id)
                .toolbar(.hidden, for: .tabBar)
            }
            .onChange(of: openSession) { _, id in
                // Only when it first opens.
                if id != startedEmpty { startedEmpty = nil }
            }
            // Opening a server it gave up on tries again.
            .task { connection.start() }
    }

    @ViewBuilder private var sections: some View {
        switch connection.state {
        case .failed(let msg):
            Section {
                Label(msg, systemImage: "wifi.exclamationmark")
                    .foregroundStyle(Palette.red)
                Button("Try again") { connection.retry() }
            }
            .scaledFont(.body)
            .paletteRows()
        case .connecting where connection.indexSynced:
            Label("Reconnecting…", systemImage: "arrow.triangle.2.circlepath")
                .scaledFont(.body)
                .foregroundStyle(.secondary)
                .paletteRows()
        default:
            EmptyView()
        }

        let active = connection.activeSessions
        let attention = active.filter { $0.status == .awaitingApproval }
        if !attention.isEmpty {
            Section {
                ForEach(attention) { row($0) }
            } header: {
                Text("Needs you").listHeaderStyle()
            }
        }
        Section {
            ForEach(active.filter { $0.status != .awaitingApproval }) { row($0) }
        } header: {
            Text("Sessions").listHeaderStyle()
        }
        let archived = connection.archivedSessions
        if !archived.isEmpty {
            Section(isExpanded: $showArchived) {
                ForEach(archived) { row($0) }
            } header: {
                // Plain lists have no disclosure control of their own.
                Button {
                    withAnimation { showArchived.toggle() }
                } label: {
                    HStack {
                        Text("Archived")
                        Text("\(archived.count)").foregroundStyle(.tertiary)
                        Spacer()
                        Image(systemName: "chevron.right")
                            .rotationEffect(.degrees(showArchived ? 90 : 0))
                    }
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
            }
        }
    }

    private func row(_ s: Session) -> some View {
        NavigationLink(value: SessionRoute(serverID: connection.server.id, sessionID: s.id)) {
            SessionRow(session: s, project: connection.projects[s.projectId], harness: connection.harness(s.harness))
        }
        .listRowInsets(Metrics.rowInsets)
        .listRowBackground(Palette.base)
        .swipeActions {
            if !s.archived {
                Button("Archive", systemImage: "archivebox") { SessionRequest(session: s, connection: connection).archive() }
                    .tint(Palette.blue)
            }
            Button("Delete", systemImage: "trash") { deleting = SessionRequest(session: s, connection: connection) }
                .tint(Palette.red)
        }
        .contextMenu {
            if !s.archived {
                Button("Archive…", systemImage: "archivebox") { archiving = SessionRequest(session: s, connection: connection) }
            }
            Button("Delete…", systemImage: "trash", role: .destructive) { deleting = SessionRequest(session: s, connection: connection) }
        }
    }
}
