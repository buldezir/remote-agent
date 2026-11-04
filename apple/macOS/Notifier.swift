import AppKit
import RAKit
import UserNotifications

/// Notifications in the Notification Center when a session on any server
/// needs the user, finishes or fails. None comes for the session the window
/// shows while the app is in front. A click opens the session. A session
/// that no longer needs the user, because they answered on another device,
/// takes its notification back, as does one read on another device. Settings
/// turns them off.
///
/// The Dock icon can also count the sessions that need the user, on every
/// server, as the Inbox's Needs You does: waiting for an answer, failed, or
/// with a reply nobody has read. It can bounce once when one starts
/// waiting while the app is in the background. Settings turns each off.
@MainActor @Observable
final class Notifier: NSObject, UNUserNotificationCenterDelegate {
    static let enabledKey = "notifications"
    static let dockBadgeKey = "dockBadge"
    static let dockBounceKey = "dockBounce"
    /// The session a click on a notification asked to open.
    var opened: SessionRoute?
    /// Whether macOS lets the app show notifications.
    private(set) var authorization: UNAuthorizationStatus = .notDetermined
    /// Whether macOS lets the app badge its Dock icon.
    private(set) var badgeSetting: UNNotificationSetting = .notSupported
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
        defaults.register(defaults: [Self.enabledKey: true, Self.dockBadgeKey: false, Self.dockBounceKey: false])
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
        NotificationCenter.default.addObserver(forName: UserDefaults.didChangeNotification, object: defaults, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.updateBadge() }
        }
        // NSApp doesn't exist yet while the app's init runs.
        NotificationCenter.default.addObserver(forName: NSApplication.didFinishLaunchingNotification, object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.trackBadge() }
        }
        // The Dock draws the badge only with the user's leave, as for
        // notifications.
        if enabled || badges { Task { await requestAuthorization() } }
    }

    var enabled: Bool { defaults.bool(forKey: Self.enabledKey) }
    private var badges: Bool { defaults.bool(forKey: Self.dockBadgeKey) }
    private var bounces: Bool { defaults.bool(forKey: Self.dockBounceKey) }

    // MARK: Dock

    /// The sessions that need the user, on every server.
    private var waitingCount: Int {
        store.connections.reduce(0) { $0 + $1.activeSessions.count(where: \.standsOut) }
    }

    /// Updates the badge now and again whenever the servers or their
    /// sessions change, whether or not the window is open.
    private func trackBadge() {
        withObservationTracking {
            updateBadge()
        } onChange: { [weak self] in
            Task { @MainActor in self?.trackBadge() }
        }
    }

    private func updateBadge() {
        guard NSApp != nil else { return }
        let count = badges ? waitingCount : 0
        let label = count > 0 ? String(count) : nil
        if NSApp.dockTile.badgeLabel != label { NSApp.dockTile.badgeLabel = label }
    }

    /// Asks macOS once; after that it answers with the user's choice.
    func requestAuthorization() async {
        _ = try? await center.requestAuthorization(options: [.alert, .sound, .badge])
        await refreshAuthorization()
    }

    func refreshAuthorization() async {
        let settings = await center.notificationSettings()
        authorization = settings.authorizationStatus
        badgeSetting = settings.badgeSetting
    }

    private func sessionChanged(_ c: ServerConnection, _ old: Session, _ new: Session) {
        let route = SessionRoute(serverID: c.server.id, sessionID: new.id)
        if old.status == .awaitingApproval && new.status != .awaitingApproval && waiting.contains(id(route)) {
            remove(route)
        }
        if old.isUnread && !new.isUnread { remove(route) }
        guard let alert = SessionAlert(from: old, to: new) else { return }
        if alert == .needsYou && bounces && !NSApp.isActive { NSApp.requestUserAttention(.informationalRequest) }
        guard enabled else { return }
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
