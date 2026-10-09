import RAKit
import SwiftUI

/// A server's sessions. On iPhone it is pushed and pushes sessions; in the
/// iPad sidebar, selection says which session the detail column shows.
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
        .listStyle(.plain)
        .overlay {
            if connection.indexSynced && connection.sessions.isEmpty {
                ContentUnavailableView {
                    Label("No sessions yet", systemImage: "bubble.left.and.text.bubble.right")
                } description: {
                    Text("Start an agent in one of your projects.")
                } actions: {
                    Button("New session") { showNew = true }
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
                Button("Remove server", systemImage: "trash", role: .destructive) { removing = connection.server }
            }
        }
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

    @ViewBuilder private var sections: some View {
        switch connection.state {
        case .failed(let msg):
            Section {
                Label(msg, systemImage: "wifi.exclamationmark")
                    .foregroundStyle(.red)
                Button("Retry now") { connection.retry() }
            }
        case .connecting where connection.indexSynced:
            Label("Reconnecting…", systemImage: "arrow.triangle.2.circlepath")
                .foregroundStyle(.secondary)
        default:
            EmptyView()
        }

        let active = connection.activeSessions
        let attention = active.filter { $0.status == .awaitingApproval }
        if !attention.isEmpty {
            Section("Needs you") {
                ForEach(attention) { row($0) }
            }
        }
        Section("Sessions") {
            ForEach(active.filter { $0.status != .awaitingApproval }) { row($0) }
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
        .swipeActions {
            if !s.archived {
                Button("Archive", systemImage: "archivebox") { archiveTarget = s }
                    .tint(.indigo)
            }
        }
    }

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
                    .font(.body.weight(.medium))
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
                        .font(.caption2)
                        .foregroundStyle(.tertiary)
                        .fixedSize()
                }
                .lineLimit(1)
                .font(.caption)
                .foregroundStyle(.secondary)
            }
        }
    }
}
