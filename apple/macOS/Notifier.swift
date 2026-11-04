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
/// with a reply nobody has read. When a session needs the user, finishes or
/// fails while the app is in the background, the icon can bounce once; or,
/// for a session that waits for an answer or failed, until the user comes
/// to the app or those sessions no longer need them. Settings turns each off.
@MainActor @Observable
final class Notifier: NSObject, UNUserNotificationCenterDelegate {
    static let enabledKey = "notifications"
    static let dockBadgeKey = "dockBadge"
    static let dockBounceKey = "dockBounceMode"
    /// 0.27's on/off bounce, which was once.
    private static let oldDockBounceKey = "dockBounce"
    /// The session a click on a notification asked to open.
    var opened: SessionRoute?
    /// Whether macOS lets the app show notifications.
    private(set) var authorization: UNAuthorizationStatus = .notDetermined
    /// Whether macOS lets the app badge its Dock icon.
    private(set) var badgeSetting: UNNotificationSetting = .notSupported
    /// The Dock icon's bounce until the user comes to the app, and the
    /// sessions it is for.
    @ObservationIgnored private var bouncing: (request: Int, routes: Set<SessionRoute>)?
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
        defaults.register(defaults: [Self.enabledKey: true, Self.dockBadgeKey: false, Self.dockBounceKey: DockBounce.off.rawValue])
        if defaults.object(forKey: Self.oldDockBounceKey) != nil {
            if defaults.bool(forKey: Self.oldDockBounceKey) { defaults.set(DockBounce.once.rawValue, forKey: Self.dockBounceKey) }
            defaults.removeObject(forKey: Self.oldDockBounceKey)
        }
        // Before launch ends, so a click that launched the app gets here.
        center.delegate = self
        store.onSessionChange = { [weak self] c, old, new in self?.sessionChanged(c, old, new) }
        NotificationCenter.default.addObserver(forName: NSApplication.didBecomeActiveNotification, object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated {
                guard let self else { return }
                if let shown = self.shown { self.remove(shown) }
                // macOS stops the bounce itself.
                self.bouncing = nil
                Task { await self.refreshAuthorization() }
            }
        }
        NotificationCenter.default.addObserver(forName: UserDefaults.didChangeNotification, object: defaults, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated {
                guard let self else { return }
                self.updateBadge()
                if self.bounceMode != .untilActive { self.stopBouncing() }
            }
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
    private var bounceMode: DockBounce {
        defaults.string(forKey: Self.dockBounceKey).flatMap(DockBounce.init) ?? .off
    }

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
        if !new.needsYou || new.archived { stopBouncing(for: route) }
        guard enabled || bounceMode != .off, let alert = SessionAlert(from: old, to: new) else { return }
        if alert == .finished {
            Task {
                try? await Task.sleep(for: Self.finishDelay)
                guard let now = c.sessions[new.id], now.status == .idle else { return }
                alertUser(alert, now, on: c)
            }
        } else {
            alertUser(alert, new, on: c)
        }
    }

    /// Bounces the Dock icon, which macOS does only while another app is in
    /// front, and posts the notification.
    private func alertUser(_ alert: SessionAlert, _ session: Session, on c: ServerConnection) {
        bounce(alert, for: SessionRoute(serverID: c.server.id, sessionID: session.id))
        if enabled { post(alert, session, on: c) }
    }

    /// A turn that finished bounces once, even in `untilActive`: only a
    /// session that is stuck keeps bouncing.
    private func bounce(_ alert: SessionAlert, for route: SessionRoute) {
        switch bounceMode {
        case .off: break
        case .once: NSApp.requestUserAttention(.informationalRequest)
        case .untilActive where alert == .finished: NSApp.requestUserAttention(.informationalRequest)
        case .untilActive:
            guard !NSApp.isActive else { return }
            if bouncing != nil {
                bouncing?.routes.insert(route)
            } else {
                bouncing = (NSApp.requestUserAttention(.criticalRequest), [route])
            }
        }
    }

    /// Stops the bounce once none of its sessions needs the user, such as
    /// when they answered on another device; or at once, without a route.
    private func stopBouncing(for route: SessionRoute? = nil) {
        guard var b = bouncing else { return }
        if let route {
            guard b.routes.remove(route) != nil else { return }
            bouncing = b
            if !b.routes.isEmpty { return }
        }
        NSApp.cancelUserAttentionRequest(b.request)
        bouncing = nil
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

/// How the Dock icon bounces when a session needs the user.
enum DockBounce: String, CaseIterable, Identifiable {
    case off, once
    /// Until the user comes to the app.
    case untilActive

    var id: Self { self }

    var title: String {
        switch self {
        case .off: "Off"
        case .once: "Once"
        case .untilActive: "Keep bouncing when stuck"
        }
    }
}
