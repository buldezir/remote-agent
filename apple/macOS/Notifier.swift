import AppKit
import RAKit
import UserNotifications

/// Notifications in the Notification Center when a session on any server
/// needs the user, finishes or fails. None comes for the session the window
/// shows while the app is in front. A click opens the session. A session
/// that no longer needs the user, because they answered on another device,
/// takes its notification back, as does one read on another device. Settings
/// turns them off.
@MainActor @Observable
final class Notifier: NSObject, UNUserNotificationCenterDelegate {
    static let enabledKey = "notifications"
    /// The session a click on a notification asked to open.
    var opened: SessionRoute?
    /// Whether macOS lets the app show notifications.
    private(set) var authorization: UNAuthorizationStatus = .notDetermined
    /// The session the window shows.
    @ObservationIgnored var shown: SessionRoute? {
        didSet { if NSApp.isActive, let shown { remove(shown) } }
    }

    @ObservationIgnored private let store: ServerStore
    @ObservationIgnored private let defaults: UserDefaults
    @ObservationIgnored private let center = UNUserNotificationCenter.current()
    /// The notifications that say a session needs the user, by identifier.
    @ObservationIgnored private var waiting: Set<String> = []
    /// A turn that ends starts the next queued prompt at once: wait this long
    /// before saying it finished.
    private static let finishDelay: Duration = .seconds(1)

    init(store: ServerStore, defaults: UserDefaults = .standard) {
        self.store = store
        self.defaults = defaults
        super.init()
        defaults.register(defaults: [Self.enabledKey: true])
        // Before launch ends, so a click that launched the app gets here.
        center.delegate = self
        store.onSessionChange = { [weak self] c, old, new in self?.sessionChanged(c, old, new) }
        NotificationCenter.default.addObserver(forName: NSApplication.didBecomeActiveNotification, object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated {
                guard let self else { return }
                if let shown = self.shown { self.remove(shown) }
                Task { await self.refreshAuthorization() }
            }
        }
        if enabled { Task { await requestAuthorization() } }
    }

    var enabled: Bool { defaults.bool(forKey: Self.enabledKey) }

    /// Asks macOS once; after that it answers with the user's choice.
    func requestAuthorization() async {
        _ = try? await center.requestAuthorization(options: [.alert, .sound])
        await refreshAuthorization()
    }

    func refreshAuthorization() async {
        authorization = await center.notificationSettings().authorizationStatus
    }

    private func sessionChanged(_ c: ServerConnection, _ old: Session, _ new: Session) {
        let route = SessionRoute(serverID: c.server.id, sessionID: new.id)
        if old.status == .awaitingApproval && new.status != .awaitingApproval && waiting.contains(id(route)) {
            remove(route)
        }
        if old.isUnread && !new.isUnread { remove(route) }
        guard enabled, let alert = SessionAlert(from: old, to: new) else { return }
        if alert == .finished {
            Task {
                try? await Task.sleep(for: Self.finishDelay)
                guard let now = c.sessions[new.id], now.status == .idle else { return }
                post(alert, now, on: c)
            }
        } else {
            post(alert, new, on: c)
        }
    }

    private func post(_ alert: SessionAlert, _ session: Session, on c: ServerConnection) {
        let route = SessionRoute(serverID: c.server.id, sessionID: session.id)
        if NSApp.isActive && shown == route { return }
        let agent = c.harness(session.harness)?.name ?? session.harness
        let content = UNMutableNotificationContent()
        content.title = session.title.isEmpty ? "Session" : session.title
        // The project, and the server when there are several.
        var place = c.projects[session.projectId]?.name ?? ""
        if store.servers.count > 1 { place += place.isEmpty ? c.server.name : " on \(c.server.name)" }
        content.subtitle = place
        switch alert {
        case .needsYou: content.body = "\(agent) needs you."
        case .finished: content.body = "\(agent) finished."
        case .failed(let error?): content.body = "\(agent) failed: \(error)"
        case .failed(nil): content.body = "\(agent) failed."
        }
        content.sound = .default
        content.threadIdentifier = c.server.id
        content.userInfo = ["server": route.serverID, "session": route.sessionID]
        // One per session: a newer one replaces it.
        let id = id(route)
        if alert == .needsYou { waiting.insert(id) } else { waiting.remove(id) }
        center.add(UNNotificationRequest(identifier: id, content: content, trigger: nil))
    }

    private func remove(_ route: SessionRoute) {
        let id = id(route)
        waiting.remove(id)
        center.removeDeliveredNotifications(withIdentifiers: [id])
    }

    private func id(_ route: SessionRoute) -> String { "\(route.serverID) \(route.sessionID)" }

    // MARK: UNUserNotificationCenterDelegate

    /// While the app is in front, too: `post` has already left out the
    /// session in view.
    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter,
                                            willPresent notification: UNNotification) async -> UNNotificationPresentationOptions {
        [.banner, .list, .sound]
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
        let info = response.notification.request.content.userInfo
        guard response.actionIdentifier == UNNotificationDefaultActionIdentifier,
              let server = info["server"] as? String, let session = info["session"] as? String else { return }
        await open(SessionRoute(serverID: server, sessionID: session))
    }

    private func open(_ route: SessionRoute) {
        NSApp.activate()
        opened = route
    }
}
