import RAKit
import SwiftUI

/// The iPad and Mac layout: every server's sessions in the sidebar, the
/// chosen session beside them, or in the Columns layout, a list of sessions
/// between the two. In a narrow iPad window it collapses into a stack. The
/// app has this one window, which opens again on the session that was open
/// last. It shows the sheets and confirmations the sidebars ask for.
struct ServerSplitView: View {
    @Environment(ServerStore.self) private var store
    @Binding var pendingLink: PairingLink?
    @AppStorage(SidebarLayout.key) private var layout = SidebarLayout.inbox
    /// The open session, as "<server id> <session id>", or "" for none.
    @AppStorage("openSession") private var openSession = ""
    /// The Columns layout's collection, as `SessionCollection.stored`.
    @AppStorage("sessionCollection") private var openCollection = ""
    @AppStorage(RecentSessions.key) private var recentSessions = ""
    @State private var showAdd = false
    @State private var newSession: NewSessionRequest?
    /// A session started empty, whose prompt takes the keyboard when it opens.
    @State private var startedEmpty: SessionRoute?
    @State private var renaming: SessionRequest?
    @State private var archiving: SessionRequest?
    @State private var archivingOld: [ServerSession]?
    @State private var deleting: SessionRequest?
    @State private var removing: SavedServer?
    @State private var managing: ServerRequest?
    /// The item a search result opens the session at.
    @State private var itemFocus: ItemFocus?
    /// The search field over the sidebar, or the Columns layout's session list.
    @FocusState private var searchFocused: Bool
    @State private var columns = NavigationSplitViewVisibility.automatic
    #if os(macOS)
    @Environment(Notifier.self) private var notifier
    #else
    @Environment(\.horizontalSizeClass) private var sizeClass
    #endif

    /// The open session, while its server is still paired.
    private var route: SessionRoute? {
        let ids = openSession.split(separator: " ")
        guard ids.count == 2, store.servers.contains(where: { $0.id == ids[0] }) else { return nil }
        return SessionRoute(serverID: String(ids[0]), sessionID: String(ids[1]))
    }

    private var selection: Binding<SessionRoute?> {
        Binding { route } set: { openSession = $0.map { "\($0.serverID) \($0.sessionID)" } ?? "" }
    }

    /// The Columns layout's collection.
    private var current: SessionCollection { SessionCollection(stored: openCollection) ?? .all }

    /// The sidebar's selection. The list may set nil, which leaves the
    /// collection as it is.
    private var collection: Binding<SessionCollection?> {
        Binding { current } set: { if let c = $0 { choose(c) } }
    }

    /// Chosen in the sidebar: the session beside it is one of the
    /// collection's, the one opened last, else its first.
    private func choose(_ c: SessionCollection) {
        guard c != current else { return }
        openCollection = c.stored
        #if os(iOS)
        // Collapsed into a stack, a session would be pushed over the list.
        guard sizeClass == .regular else { return }
        #endif
        selection.wrappedValue = c.sessionToOpen(in: store, recent: RecentSessions.routes(recentSessions))
    }

    private func connection(_ route: SessionRoute) -> ServerConnection? {
        store.servers.first { $0.id == route.serverID }.map { store.connection(for: $0) }
    }

    /// The open session is no longer on the server. Only the synced index can
    /// tell: before it, every session looks missing.
    private var openSessionGone: Bool {
        guard let route, let connection = connection(route) else { return false }
        return connection.indexSynced && connection.sessions[route.sessionID] == nil
    }

    /// The Columns layout's project is no longer there: it was forgotten, here
    /// or on another device, or its server was removed. Only the synced index
    /// can tell that it was forgotten.
    private var collectionProjectGone: Bool {
        guard case .project(let serverID, let projectID) = current else { return false }
        guard let server = store.servers.first(where: { $0.id == serverID }) else { return true }
        let c = store.connection(for: server)
        return c.indexSynced && c.projects[projectID] == nil
    }

    /// Where a new session starts from the menu bar, as with the New Session
    /// button: in the Columns layout's project, else on the open session's
    /// server, else on the first one connected.
    private var newSessionTarget: NewSessionRequest? {
        store.newSessionTarget(in: layout == .columns ? current : nil, beside: route)
    }

    /// In the Columns layout, a session asked for on the chosen project's
    /// server, as from its header, starts in that project.
    private func inChosenProject(_ request: NewSessionRequest) -> NewSessionRequest {
        guard layout == .columns, request.projectID.isEmpty, case .project(let server, let project) = current,
              request.connection.server.id == server, request.connection.projects[project] != nil else { return request }
        var request = request
        request.projectID = project
        return request
    }

    var body: some View {
        split
            .environment(\.sessionActions, SessionActions(
                startSession: { newSession = inChosenProject($0) },
                rename: { renaming = $0 },
                archive: { archiving = $0 },
                archiveOld: { archivingOld = $0 },
                delete: { deleting = $0 },
                remove: { removing = $0 },
                manage: { managing = ServerRequest(connection: $0) },
                open: { route, itemID in
                    itemFocus = itemID.map { ItemFocus(route: route, itemID: $0) }
                    selection.wrappedValue = route
                }))
            .sheet(item: $newSession) { request in
                NewSessionView(connection: request.connection, projectID: request.projectID) { session, empty in
                    let route = SessionRoute(serverID: request.connection.server.id, sessionID: session.id)
                    startedEmpty = empty ? route : nil
                    selection.wrappedValue = route
                }
                .appStyle()
            }
            .sheet(item: $managing) { request in
                ServerSettingsView(connection: request.connection)
                    .appStyle()
            }
            .sheet(isPresented: $showAdd) {
                AddServerView(initialLink: pendingLink) { _ in }
                    .appStyle()
            }
            // Every server connects; one that was given up on waits for a click.
            .task(id: store.servers.map(\.id)) {
                for c in store.connections where c.state == .idle { c.start() }
            }
            .focusedSceneValue(\.newSession, newSessionTarget.map { target in
                Binding { newSession != nil } set: { newSession = $0 ? target : nil }
            })
            .focusedSceneValue(\.pairServer, $showAdd)
            .focusedSceneValue(\.searchSessions, Binding { searchFocused } set: { if $0 { focusSearch() } else { searchFocused = false } })
            .background { searchShortcut }
            .onChange(of: pendingLink) { _, link in
                if link != nil { showAdd = true }
            }
            .onChange(of: showAdd) { _, shown in
                if !shown { pendingLink = nil }
            }
            .onChange(of: openSessionGone, initial: true) { _, gone in
                // Landing on a session that is gone shows nothing rather than an error.
                if gone { openSession = "" }
            }
            .onChange(of: revealKey, initial: true) { _, _ in reveal() }
            .onChange(of: collectionProjectGone, initial: true) { _, gone in
                if gone { openCollection = SessionCollection.all.stored }
            }
            .onChange(of: route, initial: true) { _, route in
                if let route { recentSessions = RecentSessions.adding(route, to: recentSessions) }
                // Only when it first opens.
                if route != startedEmpty { startedEmpty = nil }
                if itemFocus?.route != route { itemFocus = nil }
            }
            #if os(macOS)
            .onChange(of: route, initial: true) { _, route in notifier.shown = route }
            .onChange(of: notifier.opened, initial: true) { _, route in
                // A click on a notification, which may have launched the app.
                guard let route else { return }
                selection.wrappedValue = route
                notifier.opened = nil
            }
            #endif
    }

    /// The confirmations go on the column their rows are in, which an iPad
    /// points its popover at.
    @ViewBuilder private var split: some View {
        switch layout {
        case .columns:
            NavigationSplitView(columnVisibility: $columns) {
                CollectionsSidebar(collection: collection, showAdd: $showAdd)
                    .removeServerDialog($removing)
                    .archiveOldSessionsDialog($archivingOld)
                    #if os(macOS)
                    .navigationSplitViewColumnWidth(min: 180, ideal: 210, max: 300)
                    #else
                    .navigationSplitViewColumnWidth(min: 260, ideal: 300, max: 360)
                    #endif
            } content: {
                SessionListColumn(collection: current, selection: selection, searchFocused: $searchFocused)
                    .renameSessionAlert($renaming)
                    .archiveSessionDialog($archiving)
                    .deleteSessionDialog($deleting)
                    #if os(macOS)
                    .navigationSplitViewColumnWidth(min: 260, ideal: 320, max: 460)
                    #else
                    .navigationSplitViewColumnWidth(min: 300, ideal: 360, max: 460)
                    #endif
            } detail: {
                detail
            }
        case .inbox, .projects:
            NavigationSplitView(columnVisibility: $columns) {
                SessionsSidebar(layout: layout, selection: selection, showAdd: $showAdd, searchFocused: $searchFocused)
                    .renameSessionAlert($renaming)
                    .archiveSessionDialog($archiving)
                    .archiveOldSessionsDialog($archivingOld)
                    .deleteSessionDialog($deleting)
                    .removeServerDialog($removing)
                    #if os(macOS)
                    .navigationSplitViewColumnWidth(min: 240, ideal: 300, max: 420)
                    #else
                    // As wide as an iPhone, which the rows and toolbar were laid out for.
                    .navigationSplitViewColumnWidth(min: 320, ideal: 390, max: 480)
                    #endif
            } detail: {
                detail
            }
        }
    }

    private var detail: some View {
        Group {
            if let route, let connection = connection(route), !openSessionGone {
                // A new identity per session, so the attachments and the subscription don't carry over.
                SessionView(connection: connection, store: connection.sessionStore(route.sessionID),
                            focusesPrompt: route == startedEmpty, focusItem: $itemFocus) { fork, empty in
                    let next = SessionRoute(serverID: route.serverID, sessionID: fork.id)
                    startedEmpty = empty ? next : nil
                    selection.wrappedValue = next
                }
                .id(route)
            } else {
                placeholder
                    #if os(macOS)
                    // A detail column with nothing in the toolbar leaves its
                    // part of it to the column beside it, whose New Session
                    // button then moves to the window's corner.
                    .toolbar { ToolbarSpacer(.flexible) }
                    #endif
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Palette.base)
        .environment(\.inlineSessionTitle, layout == .columns)
    }

    @ViewBuilder private var placeholder: some View {
        if store.servers.isEmpty {
            ContentUnavailableView {
                Label("No servers", systemImage: "desktopcomputer")
            } description: {
                #if os(macOS)
                Text("Run `rad serve` on your computer, then `rad pair`, and paste the link it prints.")
                #else
                Text("Run `rad serve` on your computer, then `rad pair` and scan the QR code.")
                #endif
            } actions: {
                Button { showAdd = true } label: {
                    Text("Pair a server").foregroundStyle(Palette.onAccent)
                }
                .buttonStyle(.borderedProminent)
            }
        } else {
            ContentUnavailableView("No session selected", systemImage: "bubble.left.and.text.bubble.right",
                                   description: Text("Choose a session in the sidebar, or start a new one."))
        }
    }

    /// Puts the keyboard in the search field, showing the column it's in.
    private func focusSearch() {
        guard columns == .detailOnly else {
            searchFocused = true
            return
        }
        columns = .all
        // Once the column is there.
        Task {
            try? await Task.sleep(for: .milliseconds(300))
            searchFocused = true
        }
    }

    /// ⇧⌘F searches the sessions, from a keyboard on iPad. The Mac's Edit menu has it.
    @ViewBuilder private var searchShortcut: some View {
        #if os(iOS)
        Button("Search Sessions", action: focusSearch)
            .keyboardShortcut("f", modifiers: [.command, .shift])
            // hidden() would turn the shortcut off.
            .opacity(0)
            .accessibilityHidden(true)
        #endif
    }

    /// The open session and whether it is archived, once the index has it.
    private var revealKey: String? {
        guard layout == .columns, let route, let s = connection(route)?.sessions[route.sessionID] else { return nil }
        return "\(route.serverID) \(route.sessionID) \(s.archived)"
    }

    /// In three columns, a session opened from elsewhere, such as the one open
    /// last, a notification or a new session, shows in the list: when the
    /// collection doesn't have it, its project or Archived does.
    private func reveal() {
        guard layout == .columns, let route, let c = connection(route), let s = c.sessions[route.sessionID] else { return }
        if !current.sessions(in: store).contains(where: { $0.id == route }) {
            openCollection = ServerSession(connection: c, session: s).collection.stored
        }
    }
}
