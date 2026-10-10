import RAKit
import SwiftUI

/// A server's sessions. On iPhone it is pushed and pushes sessions; in the
/// iPad and Mac sidebar, selection says which session the detail column shows.
struct ServerHomeView: View {
    let connection: ServerConnection
    var selection: Binding<SessionRoute?>?
    @Environment(\.dismiss) private var dismiss
    @State private var removing: SavedServer?
    @State private var showNew = false
    @State private var openSession: String?
    @State private var showArchived = false
    @State private var archiveTarget: Session?

    var body: some View {
        Group {
            if let selection {
                List(selection: selection) { sections }
            } else {
                List { sections }
            }
        }
        #if os(macOS)
        .listStyle(.sidebar)
        #else
        .listStyle(.plain)
        #endif
        .listBackground(listColor)
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
                    #if os(iOS)
                    .keyboardShortcut("n") // On the Mac, File › New Session has it.
                    #endif
                    .disabled(connection.state != .connected)
            }
            // On the Mac these are in the server menu at the foot of the
            // sidebar, and Settings is in the app menu.
            #if os(iOS)
            ToolbarItem(placement: .secondaryAction) {
                Button("Reconnect", systemImage: "arrow.clockwise") { connection.retry() }
            }
            ToolbarItem(placement: .secondaryAction) {
                SettingsButton()
            }
            ToolbarItem(placement: .secondaryAction) {
                Button("Remove server", systemImage: "trash", role: .destructive) { removing = connection.server }
            }
            #endif
        }
        .focusedSceneValue(\.newSession, connection.state == .connected ? $showNew : nil)
        // In the sidebar there is nothing to go back to; the split view picks another server.
        .removeServerDialog($removing) { if selection == nil { dismiss() } }
        .sheet(isPresented: $showNew) {
            NewSessionView(connection: connection) { session in
                if let selection {
                    selection.wrappedValue = SessionRoute(serverID: connection.server.id, sessionID: session.id)
                } else {
                    openSession = session.id
                }
            }
            .appStyle()
        }
        .confirmationDialog("Archive session?", isPresented: .init(get: { archiveTarget != nil }, set: { if !$0 { archiveTarget = nil } }),
                            presenting: archiveTarget) { s in
            Button("Archive") { archive(s, removeWorktree: false) }
            if s.workspace.kind == .worktree {
                Button("Archive and delete worktree", role: .destructive) { archive(s, removeWorktree: true) }
            }
        } message: { s in
            Text(s.workspace.kind == .worktree ? "The branch \(s.workspace.branch ?? "") is kept either way." : "The agent process is stopped.")
        }
        .refreshable {
            if connection.state != .connected { connection.retry() }
            await connection.refreshHarnesses(force: true)
        }
        .navigationDestination(item: $openSession) { id in
            SessionView(connection: connection, store: connection.sessionStore(id))
        }
        .task { connection.start() }
    }

    /// A sidebar on iPad and the Mac, the main view on iPhone.
    private var listColor: Color { selection == nil ? Palette.base : Palette.mantle }

    @ViewBuilder private var sections: some View {
        switch connection.state {
        case .failed(let msg):
            Section {
                Label(msg, systemImage: "wifi.exclamationmark")
                    .foregroundStyle(Palette.red)
                Button("Retry now") { connection.retry() }
            }
            .scaledFont(.body)
            .paletteRows(listColor)
        case .connecting where connection.indexSynced:
            Label("Reconnecting…", systemImage: "arrow.triangle.2.circlepath")
                .scaledFont(.body)
                .foregroundStyle(.secondary)
                .paletteRows(listColor)
        default:
            EmptyView()
        }

        let active = connection.activeSessions
        let attention = active.filter { $0.status == .awaitingApproval }
        if !attention.isEmpty {
            Section {
                ForEach(attention) { row($0) }
            } header: {
                Text("Needs you").listHeaderFont()
            }
        }
        Section {
            ForEach(active.filter { $0.status != .awaitingApproval }) { row($0) }
        } header: {
            Text("Sessions").listHeaderFont()
        }
        let archived = connection.archivedSessions
        if !archived.isEmpty {
            #if os(macOS)
            // Sidebar sections show their own disclosure control.
            Section(isExpanded: $showArchived) {
                ForEach(archived) { row($0) }
            } header: {
                Text("Archived \(Text("\(archived.count)").foregroundStyle(.tertiary))")
                    .listHeaderFont()
            }
            #else
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
            #endif
        }
    }

    private func row(_ s: Session) -> some View {
        let route = SessionRoute(serverID: connection.server.id, sessionID: s.id)
        return NavigationLink(value: route) {
            SessionRow(session: s, project: connection.projects[s.projectId], harness: connection.harness(s.harness))
        }
        #if os(iOS)
        .listRowInsets(Metrics.rowInsets)
        .listRowBackground(rowBackground(selected: selection?.wrappedValue == route))
        #endif
        .swipeActions {
            if !s.archived {
                Button("Archive", systemImage: "archivebox") { archiveTarget = s }
                    .tint(Palette.blue)
            }
        }
        .contextMenu {
            if !s.archived {
                Button("Archive…", systemImage: "archivebox") { archiveTarget = s }
            }
        }
    }

    #if os(iOS)
    /// The list's colour, which iOS would otherwise cover with the system's.
    /// It also covers the selection, so the iPad sidebar draws that itself.
    private func rowBackground(selected: Bool) -> some View {
        listColor.overlay {
            if selected {
                RoundedRectangle(cornerRadius: Metrics.Corner.selection)
                    .fill(Palette.surface0)
                    .padding(.horizontal, 8)
            }
        }
    }
    #endif

    private func archive(_ s: Session, removeWorktree: Bool) {
        Task { try? await connection.archive(s.id, removeWorktree: removeWorktree) }
    }
}

struct SessionRow: View {
    let session: Session
    let project: Project?
    let harness: HarnessInfo?

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            StatusIcon(status: session.status)
                .padding(.top, 1)
            VStack(alignment: .leading, spacing: 2) {
                Text(session.title)
                    .scaledFont(.body, weight: .medium)
                    .lineLimit(2)
                // The time sits on this line, so the title gets the full width.
                HStack(spacing: 6) {
                    HarnessBadge(id: session.harness, name: harness?.name)
                        .fixedSize()
                    if let project { Text(project.name).fixedSize() }
                    if session.workspace.kind == .worktree, let b = session.workspace.branch {
                        HStack(spacing: 2) {
                            Image(systemName: "arrow.triangle.branch")
                            Text(b).truncationMode(.middle)
                        }
                    }
                    Spacer(minLength: 4)
                    Text(session.updatedAt, format: .relative(presentation: .named, unitsStyle: .narrow))
                        .scaledFont(.caption2)
                        .foregroundStyle(.tertiary)
                        .fixedSize()
                }
                .lineLimit(1)
                .scaledFont(.caption)
                .foregroundStyle(.secondary)
            }
        }
        .paletteText()
    }
}

/// Navigation value for a session on a given server.
struct SessionRoute: Hashable {
    let serverID: String
    let sessionID: String
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
