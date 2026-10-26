import RAKit
import SwiftUI

/// How the iPad and the Mac lay out sessions: chosen in View › Sidebar on the
/// Mac, and in Settings.
enum SidebarLayout: String, CaseIterable, Identifiable {
    /// Every server's sessions by state: what needs you, what runs, the rest.
    case inbox
    /// Each server's projects, folding open to their sessions.
    case projects
    /// Mail's three columns: collections, their sessions, the transcript.
    case columns

    static let key = "sidebarLayout"

    var id: Self { self }

    var title: String {
        switch self {
        case .inbox: "Inbox"
        case .projects: "Projects"
        case .columns: "Columns"
        }
    }

    var summary: String {
        switch self {
        case .inbox: "Sessions from every server, those that need you first."
        case .projects: "Each server's projects, with their sessions under them."
        case .columns: "Needs You, Running and each project in the sidebar, their sessions beside it."
        }
    }
}

/// What a sidebar's rows and menus ask of the split view, which shows the
/// sheets and confirmations for every layout.
struct SessionActions {
    var startSession: (NewSessionRequest) -> Void = { _ in }
    var archive: (SessionRequest) -> Void = { _ in }
    var delete: (SessionRequest) -> Void = { _ in }
    var remove: (SavedServer) -> Void = { _ in }
    var manage: (ServerConnection) -> Void = { _ in }

    /// A new session on the server, in one of its projects when given.
    func newSession(_ connection: ServerConnection, projectID: String = "") {
        startSession(NewSessionRequest(connection: connection, projectID: projectID))
    }
}

/// A server whose settings to show.
struct ServerRequest: Identifiable {
    let id = UUID()
    let connection: ServerConnection
}

/// A new session to start: on a server, in one of its projects, or with ""
/// in the project used last.
struct NewSessionRequest: Identifiable {
    let id = UUID()
    let connection: ServerConnection
    var projectID = ""
}

extension EnvironmentValues {
    @Entry var sessionActions = SessionActions()
    /// The session's title heads its transcript: in three columns the Mac's
    /// toolbar title is the session list's, as in Mail.
    @Entry var inlineSessionTitle = false
}

/// A session and the server it is on, for the lists that mix servers.
struct ServerSession: Identifiable {
    let connection: ServerConnection
    let session: Session
    let id: SessionRoute

    @MainActor init(connection: ServerConnection, session: Session) {
        self.connection = connection
        self.session = session
        id = SessionRoute(serverID: connection.server.id, sessionID: session.id)
    }

    @MainActor var project: Project? { connection.projects[session.projectId] }

    /// Where the Columns layout shows the session when the chosen collection
    /// doesn't have it: in its project, or in the archive.
    @MainActor var collection: SessionCollection {
        if session.archived { return .archived }
        return project == nil ? .all : .project(serverID: connection.server.id, projectID: session.projectId)
    }
}

/// The sessions opened lately, the latest first, kept on this device as
/// lines of "<server id> <session id>". Choosing a collection in the Columns
/// layout opens the one of its sessions opened last.
enum RecentSessions {
    static let key = "recentSessions"
    static let limit = 100

    static func routes(_ stored: String) -> [SessionRoute] {
        stored.split(separator: "\n").compactMap { line in
            let ids = line.split(separator: " ")
            return ids.count == 2 ? SessionRoute(serverID: String(ids[0]), sessionID: String(ids[1])) : nil
        }
    }

    /// The list with `route` first.
    static func adding(_ route: SessionRoute, to stored: String) -> String {
        let line = "\(route.serverID) \(route.sessionID)"
        let rest = stored.split(separator: "\n").filter { $0 != line }.prefix(limit - 1)
        return ([line[...]] + rest).joined(separator: "\n")
    }
}

extension Session {
    /// Waiting for an answer, or failed: what a person has to look at.
    var needsYou: Bool { status == .awaitingApproval || status == .error }
    /// Needs you or is unread: its title is bold in the lists.
    var standsOut: Bool { needsYou || isUnread }
    /// Working, or done but with tasks left running in the background.
    var isBusy: Bool { status == .running || (status == .idle && backgroundTasks?.isEmpty == false) }

    /// How much the session asks for a look: an answer it waits for, then a
    /// failure, then what nobody has read yet, then work, then work left in
    /// the background. Other idle and stopped sessions ask nothing.
    var urgency: Int {
        switch status {
        case .awaitingApproval: 5
        case .error: 4
        case _ where isUnread: 3
        case .running: 2
        case .idle where backgroundTasks?.isEmpty == false: 1
        default: 0
        }
    }
}

extension Sequence<Session> {
    /// The session whose state a project's row shows for all of them: the
    /// one that asks most, if any does.
    var mostPressing: Session? {
        filter { $0.urgency > 0 }.max { $0.urgency < $1.urgency }
    }
}

extension ServerStore {
    var connections: [ServerConnection] { servers.map { connection(for: $0) } }

    /// Every server's sessions, the latest first.
    func sessions(archived: Bool) -> [ServerSession] {
        connections
            .flatMap { c in (archived ? c.archivedSessions : c.activeSessions).map { ServerSession(connection: c, session: $0) } }
            .sorted { $0.session.updatedAt > $1.session.updatedAt }
    }

    /// Where a new session starts: in the project chosen in the Columns
    /// layout, on its server; else on the open session's server; else on the
    /// first one connected.
    func newSessionTarget(in collection: SessionCollection?, beside open: SessionRoute?) -> NewSessionRequest? {
        let connected = connections.filter { $0.state == .connected }
        if case .project(let server, let project) = collection,
           let c = connected.first(where: { $0.server.id == server }) {
            // Its server alone, if the project was forgotten while chosen.
            return NewSessionRequest(connection: c, projectID: c.projects[project] == nil ? "" : project)
        }
        return (connected.first { $0.server.id == open?.serverID } ?? connected.first)
            .map { NewSessionRequest(connection: $0) }
    }

    /// Tries again where a connection gave up, and asks the others for their agents.
    func refreshConnections() async {
        for c in connections {
            if c.state == .connected { await c.refreshHarnesses(force: true) } else { c.retry() }
        }
    }
}

/// What the sidebar of the three-column layout shows the sessions of.
enum SessionCollection: Hashable {
    case needsYou, running, all, archived
    case project(serverID: String, projectID: String)

    /// As stored: "needsYou", or "project <server id> <project id>".
    init?(stored: String) {
        let parts = stored.split(separator: " ").map(String.init)
        switch parts.first {
        case "needsYou": self = .needsYou
        case "running": self = .running
        case "all": self = .all
        case "archived": self = .archived
        case "project" where parts.count == 3: self = .project(serverID: parts[1], projectID: parts[2])
        default: return nil
        }
    }

    var isProject: Bool {
        if case .project = self { true } else { false }
    }

    var stored: String {
        switch self {
        case .needsYou: "needsYou"
        case .running: "running"
        case .all: "all"
        case .archived: "archived"
        case .project(let server, let project): "project \(server) \(project)"
        }
    }

    @MainActor func sessions(in store: ServerStore) -> [ServerSession] {
        switch self {
        case .needsYou: store.sessions(archived: false).filter(\.session.needsYou)
        case .running: store.sessions(archived: false).filter(\.session.isBusy)
        case .all:
            // What needs you first, as in the other layouts.
            store.sessions(archived: false).filter(\.session.needsYou) + store.sessions(archived: false).filter { !$0.session.needsYou }
        case .archived: store.sessions(archived: true)
        case .project(let server, let project):
            store.sessions(archived: false).filter { $0.connection.server.id == server && $0.session.projectId == project }
        }
    }

    /// The session to open on choosing the collection: of its sessions, the
    /// one opened last, else its first.
    @MainActor func sessionToOpen(in store: ServerStore, recent: [SessionRoute]) -> SessionRoute? {
        let ids = sessions(in: store).map(\.id)
        return recent.first(where: ids.contains) ?? ids.first
    }

    @MainActor func title(in store: ServerStore) -> String {
        switch self {
        case .needsYou: "Needs You"
        case .running: "Running"
        case .all: "All Sessions"
        case .archived: "Archived"
        case .project(let server, let project):
            store.servers.first { $0.id == server }.flatMap { store.connection(for: $0).projects[project]?.name } ?? "Project"
        }
    }
}
