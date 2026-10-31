import RAKit
import SwiftUI

/// Navigation value for a session on a given server.
struct SessionRoute: Hashable {
    let serverID: String
    let sessionID: String
}

/// A session in the iPhone's list of a server's sessions, or in the iPad and
/// Mac sidebar.
struct SessionRow: View {
    let session: Session
    let project: Project?
    let harness: HarnessInfo?

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            StatusIcon(status: session.status, background: session.backgroundTasks?.isEmpty == false,
                       unread: session.isUnread, draft: session.hasDraft)
                .padding(.top, 1)
            VStack(alignment: .leading, spacing: 2) {
                Text(session.title)
                    .scaledFont(.body, weight: session.isUnread || session.needsYou ? .semibold : .medium)
                    .lineLimit(2)
                // The time sits on this line, so the title gets the full width.
                // Where the agent's name would leave the branch too little
                // room, its icon stands for it.
                ViewThatFits(in: .horizontal) {
                    details(agentName: true)
                    details(agentName: false)
                }
                .lineLimit(1)
                .scaledFont(.caption)
                .foregroundStyle(.secondary)
            }
        }
        .paletteText()
    }

    /// The project leads, the agent a step quieter after it.
    private func details(agentName: Bool) -> some View {
        HStack(spacing: 6) {
            if let project {
                Text(project.name)
                    .scaledFont(.caption, weight: .medium)
                    .fixedSize()
            }
            Group {
                if agentName {
                    HarnessBadge(id: session.harness, name: harness?.name)
                } else {
                    HarnessIcon(id: session.harness)
                        .imageScale(.small)
                        .help(harness?.name ?? session.harness)
                        .accessibilityLabel(harness?.name ?? session.harness)
                }
            }
            .foregroundStyle(.tertiary)
            .fixedSize()
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
    }
}

/// A session to rename, archive or delete, and the server it is on.
struct SessionRequest {
    let session: Session
    let connection: ServerConnection

    /// Archives the session without asking, as a swipe does; its worktree stays.
    func archive(removeWorktree: Bool = false) {
        Task { try? await connection.archive(session.id, removeWorktree: removeWorktree) }
    }
}

extension View {
    /// Confirmation before forgetting a server.
    func removeServerDialog(_ server: Binding<SavedServer?>, onRemove: @escaping () -> Void = {}) -> some View {
        modifier(RemoveServerDialog(server: server, onRemove: onRemove))
    }

    /// The prompt to rename a session (`SessionActions.rename`): its title to
    /// type over, or to empty, which gives it back to rad's naming.
    func renameSessionAlert(_ request: Binding<SessionRequest?>) -> some View {
        modifier(RenameSessionAlert(request: request))
    }

    /// Confirmation before archiving a session, which can also delete its worktree.
    func archiveSessionDialog(_ request: Binding<SessionRequest?>) -> some View {
        confirmationDialog("Archive session?", isPresented: .init(get: { request.wrappedValue != nil }, set: { if !$0 { request.wrappedValue = nil } }),
                           presenting: request.wrappedValue) { r in
            Button("Archive") { r.archive() }
            if r.session.workspace.kind == .worktree {
                Button("Archive and delete worktree", role: .destructive) { r.archive(removeWorktree: true) }
            }
        } message: { r in
            Text(r.session.workspace.kind == .worktree ? "The branch \(r.session.workspace.branch ?? "") is kept either way." : "The agent process is stopped.")
        }
    }

    /// Confirmation before archiving the sessions with nothing new for a week,
    /// on every connected server. Their worktrees stay.
    func archiveOldSessionsDialog(_ sessions: Binding<[ServerSession]?>) -> some View {
        let count = sessions.wrappedValue?.count ?? 0
        return confirmationDialog(count == 1 ? "Archive 1 session?" : "Archive \(count) sessions?",
                                  isPresented: .init(get: { sessions.wrappedValue != nil }, set: { if !$0 { sessions.wrappedValue = nil } }),
                                  presenting: sessions.wrappedValue) { list in
            Button("Archive") {
                Task {
                    for s in list { try? await s.connection.archive(s.session.id, removeWorktree: false) }
                }
            }
        } message: { _ in
            Text("Sessions on every connected server with nothing new for \(ServerStore.oldSessionAge) days. Their worktrees are kept.")
        }
    }

    /// Confirmation before deleting a session for good, which can also delete its worktree.
    func deleteSessionDialog(_ request: Binding<SessionRequest?>) -> some View {
        confirmationDialog("Delete session?", isPresented: .init(get: { request.wrappedValue != nil }, set: { if !$0 { request.wrappedValue = nil } }),
                           presenting: request.wrappedValue) { r in
            Button("Delete", role: .destructive) { delete(r, removeWorktree: false) }
            if r.session.workspace.kind == .worktree {
                Button("Delete with worktree", role: .destructive) { delete(r, removeWorktree: true) }
            }
        } message: { r in
            Text(r.session.workspace.kind == .worktree ? "This can't be undone. The branch \(r.session.workspace.branch ?? "") is kept either way." : "This can't be undone.")
        }
    }
}

private func delete(_ r: SessionRequest, removeWorktree: Bool) {
    Task { try? await r.connection.delete(r.session.id, removeWorktree: removeWorktree) }
}

private struct RenameSessionAlert: ViewModifier {
    @Binding var request: SessionRequest?
    @State private var title = ""
    @State private var error: String?

    func body(content: Content) -> some View {
        content
            .alert("Rename Session", isPresented: .init(get: { request != nil }, set: { if !$0 { request = nil } }),
                   presenting: request) { r in
                TextField("Title", text: $title)
                Button("Cancel", role: .cancel) {}
                Button("Rename") { rename(r) }
                    // iOS renames on Return without it, and would draw the
                    // palette's accent on the system's blue.
                    #if os(macOS)
                    .keyboardShortcut(.defaultAction)
                    #endif
            } message: { _ in
                Text("Leave it empty to name it automatically.")
            }
            .alert("Couldn't rename the session", isPresented: .init(get: { error != nil }, set: { if !$0 { error = nil } })) {
                Button("OK") { error = nil }
            } message: {
                Text(error ?? "")
            }
            // Each prompt starts from the session's title.
            .onChange(of: request?.session.id, initial: true) { _, _ in
                if let r = request { title = r.session.title }
            }
    }

    /// The prompt closes at once, and a failure shows once the server says.
    /// The new title comes with the session's event. The title as it was
    /// asks nothing of the server.
    private func rename(_ r: SessionRequest) {
        let current = r.connection.sessions[r.session.id]?.title ?? r.session.title
        guard title.trimmingCharacters(in: .whitespacesAndNewlines) != current else { return }
        let title = title
        Task {
            do { try await r.connection.rename(r.session.id, title: title) }
            catch { self.error = error.localizedDescription }
        }
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
