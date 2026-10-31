import RAKit
import SwiftUI

/// The Columns layout's sidebar, as Mail's: the collections a session can be
/// in. What needs you, what is working and every session, then each server's
/// projects, then the archive. The sessions are a column of
/// their own beside it (`SessionListColumn`), which keeps the sidebar to two
/// levels, as the HIG asks.
struct CollectionsSidebar: View {
    @Environment(ServerStore.self) private var store
    @Binding var collection: SessionCollection?
    @Binding var showAdd: Bool
    private var collapsed = CollapsedServers()

    init(collection: Binding<SessionCollection?>, showAdd: Binding<Bool>) {
        _collection = collection
        _showAdd = showAdd
    }

    var body: some View {
        let active = store.sessions(archived: false)
        let archived = store.sessions(archived: true)
        List(selection: $collection) {
            Section {
                // A fixed colour where it means something, as Mail's VIP.
                link(.needsYou, "Needs You", symbol: "hand.raised.fill", count: active.filter(\.session.needsYou).count,
                     tint: Palette.yellow)
                link(.running, "Running", symbol: "play.circle", count: active.filter(\.session.isBusy).count)
                link(.all, "All Sessions", symbol: "tray.full", count: active.count)
            }
            ForEach(store.connections, id: \.server.id) { c in
                let expanded = collapsed.expanded(c.server.id, among: store.servers)
                Section(isExpanded: expanded) {
                    ConnectionStatusRow(connection: c)
                    ForEach(projects(c), id: \.project.id) { p in
                        link(.project(serverID: c.server.id, projectID: p.project.id), p.project.name,
                             symbol: "folder", count: p.sessions.count, status: p.sessions.mostPressing)
                            .contextMenu { ProjectMenuItems(connection: c, projectID: p.project.id) }
                    }
                } header: {
                    ServerHeader(connection: c, expanded: expanded)
                }
            }
            if !archived.isEmpty {
                Section {
                    link(.archived, "Archived", symbol: "archivebox", count: archived.count)
                }
            }
        }
        .sidebarList()
        .navigationTitle("Sessions")
        .toolbar { SidebarToolbar(selection: nil, newSession: false, showAdd: $showAdd) }
        .pairServerFooter(showAdd: $showAdd)
    }

    private func link(_ c: SessionCollection, _ title: String, symbol: String, count: Int,
                      tint: Color = Palette.accent, status: Session? = nil) -> some View {
        NavigationLink(value: c) {
            Label {
                HStack(spacing: 6) {
                    Text(title)
                        .foregroundStyle(Palette.text)
                        .lineLimit(1)
                    // A project's: the state of its session that asks most for a look.
                    if let status {
                        StatusGlyph(session: status)
                    }
                }
            } icon: {
                Image(systemName: symbol)
                    .foregroundStyle(tint)
            }
            .scaledFont(.body)
            .environment(\.backgroundProminence, .standard)
        }
        // A project shows that it has none; the others, as Mail's, nothing.
        .badge(count > 0 || c.isProject ? Text(count, format: .number) : nil)
        // On the Mac a badge hides the link's value from the list, which then
        // selects nothing when the row is clicked; the tag gives the value back.
        .tag(c)
        #if os(iOS)
        .listRowInsets(Metrics.rowInsets)
        #endif
        .listRowBackground(SidebarSelection(selected: collection == c))
    }

    /// The server's projects by name, with their sessions.
    private func projects(_ c: ServerConnection) -> [(project: Project, sessions: [Session])] {
        let sessions = Dictionary(grouping: c.activeSessions, by: \.projectId)
        return c.sortedProjects.map { p in (p, sessions[p.id] ?? []) }
    }
}

/// The Columns layout's middle column: the sessions in the collection chosen
/// in the sidebar, three lines each, under the search field, as Mail's
/// messages. While it searches, what search found takes the list's place.
struct SessionListColumn: View {
    @Environment(ServerStore.self) private var store
    let collection: SessionCollection
    @Binding var selection: SessionRoute?
    var searchFocused: FocusState<Bool>.Binding

    /// Over the list, as Mail's: in its navigation bar on iPad, which would
    /// otherwise put it over the session. The Mac's toolbar puts it there
    /// whatever the placement, so the list has the app's own field.
    private var searchPlacement: SearchFieldPlacement? {
        #if os(macOS)
        nil
        #else
        .navigationBarDrawer(displayMode: .always)
        #endif
    }

    var body: some View {
        let items = collection.sessions(in: store)
        SearchableSessions(placement: searchPlacement, selection: selection, focused: searchFocused, background: Palette.base) {
            List(selection: $selection) {
                // On the Mac, a row added to a list without sections keeps the
                // table's default height, one line, and the next row covers the
                // rest: a new session, or one that starts running. In a section
                // it is measured.
                Section {
                    ForEach(items) { item in
                        SessionLink(item: item, selected: selection == item.id, background: Palette.base) {
                            SessionListRow(item: item, showsServer: store.servers.count > 1)
                        }
                        .listRowSeparator(.hidden)
                    }
                }
            }
            .overlay {
                if items.isEmpty { empty }
            }
        }
        #if os(macOS)
        .listStyle(.inset)
        #else
        .listStyle(.plain)
        #endif
        .listBackground(Palette.base)
        .refreshable { await store.refreshConnections() }
        .navigationTitle(collection.title(in: store))
        #if os(macOS)
        .navigationSubtitle(items.count == 1 ? "1 session" : "\(items.count) sessions")
        #endif
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                NewSessionButton(selection: selection, collection: collection)
            }
        }
    }

    @ViewBuilder private var empty: some View {
        switch collection {
        case .needsYou:
            ContentUnavailableView("Nothing needs you", systemImage: "checkmark.circle",
                                   description: Text("Sessions that wait for your answer, or failed, show here."))
        case .running:
            ContentUnavailableView("Nothing is running", systemImage: "play.circle",
                                   description: Text("Sessions show here while their agent works."))
        case .archived:
            ContentUnavailableView("Nothing archived", systemImage: "archivebox")
        case .all, .project:
            ContentUnavailableView("No sessions", systemImage: "bubble.left.and.text.bubble.right",
                                   description: Text("Start one with the New Session button."))
        }
    }
}
