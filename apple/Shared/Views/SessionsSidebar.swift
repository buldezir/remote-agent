import RAKit
import SwiftUI

/// The iPad and Mac sidebar beside a session: the Inbox or the Projects
/// layout. The Columns layout has its own (`CollectionsSidebar`). The split
/// view around it shows the sheets and confirmations its rows ask for.
struct SessionsSidebar: View {
    let layout: SidebarLayout
    @Binding var selection: SessionRoute?
    @Binding var showAdd: Bool

    var body: some View {
        Group {
            switch layout {
            case .inbox, .columns: InboxSidebar(selection: $selection)
            case .projects: ProjectsSidebar(selection: $selection)
            }
        }
        .sidebarList()
        .navigationTitle("Sessions")
        .toolbar { SidebarToolbar(selection: selection, showAdd: $showAdd) }
        .pairServerFooter(showAdd: $showAdd)
    }
}

extension View {
    /// A sidebar list on the palette's colour, which pulls to refresh on iPad.
    func sidebarList() -> some View {
        modifier(SidebarList())
    }

    /// On the Mac, a way to pair a server at the foot of the sidebar. iPad
    /// has it in the sidebar's ⋯ menu, from `SidebarToolbar`.
    func pairServerFooter(showAdd: Binding<Bool>) -> some View {
        #if os(macOS)
        safeAreaInset(edge: .bottom, spacing: 0) {
            Button { showAdd.wrappedValue = true } label: {
                Label("Pair a server", systemImage: "plus")
                    .scaledFont(.body)
                    .foregroundStyle(Palette.subtext)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .padding(.horizontal, 16)
            .padding(.vertical, 10)
            // The list scrolls under it.
            .background(Palette.mantle)
        }
        #else
        self
        #endif
    }
}

private struct SidebarList: ViewModifier {
    @Environment(ServerStore.self) private var store

    func body(content: Content) -> some View {
        content
            #if os(macOS)
            .listStyle(.sidebar)
            #else
            .listStyle(.plain)
            #endif
            .listBackground(Palette.mantle)
            .refreshable { await store.refreshConnections() }
    }
}

/// The sidebar's toolbar: a new session, archiving old ones, and on iPad,
/// pairing and Settings, which the Mac has at the foot of the sidebar and in
/// the app menu.
struct SidebarToolbar: ToolbarContent {
    var selection: SessionRoute?
    /// In three columns the session list has the button instead.
    var newSession = true
    @Binding var showAdd: Bool

    var body: some ToolbarContent {
        #if os(macOS)
        ToolbarItem(placement: .primaryAction) {
            ArchiveOldSessionsButton()
        }
        #endif
        if newSession {
            ToolbarItem(placement: .primaryAction) {
                NewSessionButton(selection: selection)
            }
        }
        #if os(iOS)
        ToolbarItem(placement: .secondaryAction) {
            ArchiveOldSessionsButton()
        }
        ToolbarItem(placement: .secondaryAction) {
            Button("Pair a server", systemImage: "plus") { showAdd = true }
        }
        ToolbarItem(placement: .secondaryAction) {
            SettingsButton()
        }
        #endif
    }
}

/// Archives, once the split view confirms, every connected server's sessions
/// with nothing new for a week.
struct ArchiveOldSessionsButton: View {
    @Environment(ServerStore.self) private var store
    @Environment(\.sessionActions) private var actions

    var body: some View {
        // Counted again on the click: the body only runs again when a session changes.
        let none = store.oldSessions().isEmpty
        Button("Archive old sessions", systemImage: "archivebox") { actions.archiveOld(store.oldSessions()) }
            .help(none ? "No sessions older than \(ServerStore.oldSessionAge) days"
                       : "Archive sessions with nothing new for \(ServerStore.oldSessionAge) days")
            .disabled(none)
    }
}

/// Starts a session in the project chosen in the Columns layout, else on the
/// open session's server, else on the first one connected; with more than one
/// connected, a menu offers each.
struct NewSessionButton: View {
    @Environment(ServerStore.self) private var store
    @Environment(\.sessionActions) private var actions
    var selection: SessionRoute?
    /// The Columns layout's collection: a project's starts the session in it.
    var collection: SessionCollection?

    var body: some View {
        let connected = store.connections.filter { $0.state == .connected }
        let target = store.newSessionTarget(in: collection, beside: selection)
        if connected.count > 1, let target {
            Menu {
                ForEach(connected, id: \.server.id) { c in
                    Button("On \(c.server.name)") {
                        // The chosen project's server starts in it.
                        actions.newSession(c, projectID: c.server.id == target.connection.server.id ? target.projectID : "")
                    }
                }
            } label: {
                Label("New session", systemImage: "square.and.pencil")
            } primaryAction: {
                actions.startSession(target)
            }
            .help(help(target, server: true))
            #if os(iOS)
            .keyboardShortcut("n") // On the Mac, File › New Session has it.
            #endif
        } else {
            Button("New session", systemImage: "square.and.pencil") {
                if let target { actions.startSession(target) }
            }
            .help(target.map { help($0, server: false) } ?? "New session")
            #if os(iOS)
            .keyboardShortcut("n")
            #endif
            .disabled(target == nil)
        }
    }

    private func help(_ target: NewSessionRequest, server: Bool) -> String {
        let project = target.connection.projects[target.projectID]?.name
        let on = server ? " on \(target.connection.server.name)" : ""
        return project.map { "New session in \($0)\(on)" } ?? "New session\(on)"
    }
}

/// A session's row in a sidebar list: choosing it opens the session, and a
/// swipe or the context menu archives or deletes it.
struct SessionLink<Content: View>: View {
    let item: ServerSession
    let selected: Bool
    /// The list's colour: the sidebar's, or the transcript's for the Columns layout's session list.
    var background = Palette.mantle
    @ViewBuilder let content: Content
    @Environment(\.sessionActions) private var actions

    var body: some View {
        NavigationLink(value: item.id) {
            content
                // The selection drawn below is a surface, not the system's
                // accent, so the row keeps the palette's text colours on it.
                .environment(\.backgroundProminence, .standard)
        }
        #if os(iOS)
        .listRowInsets(Metrics.rowInsets)
        #endif
        .listRowBackground(SidebarSelection(selected: selected, background: background))
        .swipeActions {
            if !item.session.archived {
                Button("Archive", systemImage: "archivebox") { actions.archive(request) }
                    .tint(Palette.blue)
            }
            Button("Delete", systemImage: "trash") { actions.delete(request) }
                .tint(Palette.red)
        }
        .contextMenu {
            if !item.session.archived {
                Button("Archive…", systemImage: "archivebox") { actions.archive(request) }
            }
            Button("Delete…", systemImage: "trash", role: .destructive) { actions.delete(request) }
        }
    }

    private var request: SessionRequest { SessionRequest(session: item.session, connection: item.connection) }
}

/// A row's background: the sidebar's colour, which iOS would otherwise cover
/// with the system's, and the selection, drawn in a surface colour. The Mac's
/// own would be the system accent, and white text on Frappé's mauve is faint.
struct SidebarSelection: View {
    let selected: Bool
    var background = Palette.mantle

    var body: some View {
        background.overlay {
            if selected {
                RoundedRectangle(cornerRadius: Metrics.Corner.selection)
                    .fill(Palette.surface0)
                    .padding(.horizontal, 8)
            }
        }
    }
}

/// While a server isn't connected, why; once it has given up, a way to try
/// again; and once it has no sessions, a way to start one.
struct ConnectionStatusRow: View {
    let connection: ServerConnection
    @Environment(\.sessionActions) private var actions

    var body: some View {
        switch connection.state {
        case .failed(let msg):
            Button { connection.retry() } label: {
                HStack(alignment: .top, spacing: 8) {
                    Image(systemName: "wifi.exclamationmark")
                        .foregroundStyle(Palette.red)
                        .frame(width: 16)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(msg)
                            .foregroundStyle(Palette.subtext)
                            .lineLimit(3)
                        Text("Try again")
                            .foregroundStyle(Palette.accent)
                    }
                    Spacer(minLength: 0)
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .scaledFont(.callout)
            .paletteRows(Palette.mantle)
        case .connecting:
            HStack(spacing: 8) {
                ProgressView()
                    .controlSize(.mini)
                    .frame(width: 16)
                Text(connection.indexSynced ? "Reconnecting…" : "Connecting…")
                    .foregroundStyle(Palette.subtext)
            }
            .scaledFont(.callout)
            .paletteRows(Palette.mantle)
        case .connected where connection.indexSynced && connection.sessions.isEmpty:
            Button { actions.newSession(connection) } label: {
                HStack(spacing: 8) {
                    Image(systemName: "square.and.pencil")
                        .foregroundStyle(Palette.overlay)
                        .frame(width: 16)
                    Text("Start a session")
                        .foregroundStyle(Palette.subtext)
                    Spacer(minLength: 0)
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .scaledFont(.callout)
            .paletteRows(Palette.mantle)
        default:
            EmptyView()
        }
    }
}

/// A server as a section header: its name, how its connection is doing, and
/// while its rows are hidden, how many sessions it has and whether one needs
/// you or is unread. A click shows or hides the rows, or tries again once the connection
/// has given up. A + starts a session on it (on the Mac, on hover). Its other
/// actions are in a ⋯ menu on iPad, and in the context menu on the Mac.
struct ServerHeader: View {
    let connection: ServerConnection
    @Binding var expanded: Bool
    @Environment(\.sessionActions) private var actions
    @State private var hovering = false

    private var name: String { connection.server.name }
    private var offline: Bool {
        if case .failed = connection.state { true } else { false }
    }
    private var connected: Bool { connection.state == .connected }

    var body: some View {
        let active = connection.activeSessions
        let waiting = active.contains(where: \.needsYou)
        let unread = !waiting && active.contains(where: \.isUnread)
        HStack(spacing: 6) {
            Button(action: activate) {
                HStack(spacing: 6) {
                    Text(name)
                        .lineLimit(1)
                    ConnectionDot(state: connection.state)
                        .scaleEffect(0.7)
                    if !expanded && !active.isEmpty {
                        Text("\(active.count)")
                            .foregroundStyle(Palette.overlay)
                            .monospacedDigit()
                    }
                    if !expanded && waiting {
                        Image(systemName: "hand.raised.fill")
                            .foregroundStyle(Palette.yellow)
                    } else if !expanded && unread {
                        Circle()
                            .fill(Palette.accent)
                            .frame(width: 7, height: 7)
                    }
                    Spacer(minLength: 0)
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .help(offline ? "Try to connect to \(name) again" : ConnectionDot(state: connection.state).label)
            .accessibilityLabel(name)
            .accessibilityValue(accessibilityValue(count: active.count, waiting: waiting, unread: unread))
            .accessibilityHint(offline ? "Tries to connect again" : "")

            if connected {
                Button("New session on \(name)", systemImage: "plus") { actions.newSession(connection) }
                    .labelStyle(.iconOnly)
                    .buttonStyle(.plain)
                    .foregroundStyle(Palette.subtext)
                    .help("New session on \(name)")
                    #if os(macOS)
                    .opacity(hovering ? 1 : 0)
                    #endif
            }
            #if os(iOS)
            // A long press goes to the button, so touch gets the actions here.
            Menu { ServerMenuItems(connection: connection) } label: {
                Label("\(name) actions", systemImage: "ellipsis.circle")
                    .labelStyle(.iconOnly)
            }
            .foregroundStyle(Palette.subtext)
            #endif
        }
        .listHeaderStyle()
        #if os(iOS)
        .scaledFont(.subheadline, weight: .semibold)
        #endif
        .onHover { hovering = $0 }
        #if os(macOS)
        .contextMenu { ServerMenuItems(connection: connection) }
        #endif
        // The header is one element to VoiceOver, which can't hover.
        .accessibilityActions {
            if connected {
                Button("New session on \(name)") { actions.newSession(connection) }
            }
            Button("Reconnect") { connection.retry() }
        }
    }

    private func accessibilityValue(count: Int, waiting: Bool, unread: Bool) -> String {
        var parts = [ConnectionDot(state: connection.state).label]
        parts.append(expanded ? "Expanded" : "Collapsed, \(count) sessions")
        if !expanded && waiting { parts.append("Needs you") }
        if !expanded && unread { parts.append("Unread") }
        return parts.joined(separator: ", ")
    }

    /// Shows or hides the rows; once the connection has given up, tries again.
    private func activate() {
        if offline {
            connection.retry()
            withAnimation(.snappy(duration: 0.2)) { expanded = true }
        } else {
            withAnimation(.snappy(duration: 0.2)) { expanded.toggle() }
        }
    }
}

/// A server's actions, for its menus: a new session, reconnecting, its
/// settings, removing it.
struct ServerMenuItems: View {
    let connection: ServerConnection
    @Environment(\.sessionActions) private var actions

    var body: some View {
        let name = connection.server.name
        Button("New Session on \(name)", systemImage: "plus") { actions.newSession(connection) }
            .disabled(connection.state != .connected)
        Button("Reconnect", systemImage: "arrow.clockwise") { connection.retry() }
        Button("Server Settings…", systemImage: "gearshape.2") { actions.manage(connection) }
            .disabled(connection.state != .connected)
        Divider()
        Button("Remove \(name)…", systemImage: "trash", role: .destructive) { actions.remove(connection.server) }
    }
}

/// A project's actions, for its context menu in the sidebar: a new session
/// in it, and forgetting it.
struct ProjectMenuItems: View {
    let connection: ServerConnection
    let projectID: String
    @Environment(\.sessionActions) private var actions

    var body: some View {
        Button("New Session", systemImage: "plus") { actions.newSession(connection, projectID: projectID) }
            .disabled(connection.state != .connected)
        Divider()
        // The server keeps a project while it has sessions that aren't archived.
        Button("Forget Project", systemImage: "folder.badge.minus") {
            Task { try? await connection.removeProject(projectID) }
        }
        .disabled(connection.state != .connected || connection.activeSessions.contains { $0.projectId == projectID })
    }
}

/// The servers whose rows are hidden, which the Projects and Columns
/// sidebars share.
struct CollapsedServers: DynamicProperty {
    /// By id, separated by spaces.
    @AppStorage("collapsedServers") private var ids = ""

    func expanded(_ id: String, among servers: [SavedServer]) -> Binding<Bool> {
        Binding {
            !ids.split(separator: " ").contains(Substring(id))
        } set: { shown in
            var kept = ids.split(separator: " ").map(String.init).filter { $0 != id }
            if !shown { kept.append(id) }
            // Forgets servers that were removed.
            ids = kept.filter { id in servers.contains { $0.id == id } }.joined(separator: " ")
        }
    }
}
