import RAKit
import SwiftUI
#if os(macOS)
import AppKit
#endif

@main
struct RemoteAgentApp: App {
    @State private var store: ServerStore
    /// Search across the servers' sessions, kept while the app runs.
    @State private var search: SessionSearch
    /// The Settings sheet over the window, as on iPad: the Mac has no
    /// Settings window, and ⌘, shows the sheet.
    @State private var showSettings = false
    @Environment(\.scenePhase) private var phase
    #if os(macOS)
    @State private var notifier: Notifier
    @State private var updater = AppUpdater()

    init() {
        // The app has one window: without this, "Prefer tabs when opening
        // documents" would still give it a tab bar and the View menu its items.
        NSWindow.allowsAutomaticWindowTabbing = false
        let store = ServerStore()
        _store = State(initialValue: store)
        _search = State(initialValue: SessionSearch(store: store))
        _notifier = State(initialValue: Notifier(store: store))
    }
    #else
    init() {
        let store = ServerStore()
        _store = State(initialValue: store)
        _search = State(initialValue: SessionSearch(store: store))
    }
    #endif

    var body: some Scene {
        mainWindow
            .onChange(of: phase) { _, newPhase in
                #if os(macOS)
                // A Mac window stays in view while another app is in front.
                store.setActive(newPhase != .background)
                #else
                // Sockets don't survive backgrounding; reconnect and resync on return.
                store.setActive(newPhase == .active)
                #endif
            }
    }

    // One window. On the Mac a Window scene has no New Window; on iPad,
    // project.yml turns off multiple scenes.
    #if os(macOS)
    private var mainWindow: some Scene {
        Window("Remote Agent", id: "main") {
            WindowRoot(showSettings: $showSettings)
                .environment(store)
                .environment(search)
                .environment(notifier)
                .environment(updater)
        }
        .defaultSize(width: 1100, height: 760)
        .commands { AppCommands(updater: updater, showSettings: $showSettings) }
    }
    #else
    private var mainWindow: some Scene {
        WindowGroup(id: "main") {
            WindowRoot(showSettings: $showSettings)
                .environment(store)
                .environment(search)
        }
    }
    #endif
}

/// The app's window: the Inbox and Servers tabs on iPhone, the split view on
/// iPad and the Mac. A pairing link opens the pairing sheet in it, and
/// Settings is a sheet over it.
private struct WindowRoot: View {
    @Binding var showSettings: Bool
    @State private var pendingLink: PairingLink?
    #if os(iOS)
    @Environment(\.horizontalSizeClass) private var sizeClass
    #endif

    var body: some View {
        Group {
            #if os(macOS)
            ServerSplitView(pendingLink: $pendingLink)
            #else
            // By device, not size class: the split view collapses in a narrow
            // iPad window by itself, and swapping roots would drop the navigation.
            if UIDevice.current.userInterfaceIdiom == .pad {
                ServerSplitView(pendingLink: $pendingLink)
            } else {
                PhoneTabs(pendingLink: $pendingLink)
            }
            #endif
        }
        .onOpenURL { url in
            pendingLink = PairingLink(string: url.absoluteString)
        }
        .environment(\.showSettings, $showSettings)
        .sheet(isPresented: $showSettings) {
            #if os(macOS)
            NavigationStack { SettingsView() }
                .appStyle()
            #else
            NavigationStack { SettingsView(wide: sizeClass == .regular) }
                .appStyle()
            #endif
        }
        .appStyle()
        #if os(macOS)
        .containerBackground(Palette.base, for: .window)
        #endif
    }
}

extension FocusedValues {
    /// Shows the New session sheet, while a server is connected.
    @Entry var newSession: Binding<Bool>?
    /// Shows the pairing sheet.
    @Entry var pairServer: Binding<Bool>?
    /// The keyboard is in the sidebar's search field; setting it puts it there.
    @Entry var searchSessions: Binding<Bool>?
    /// The open session's find bar.
    @Entry var sessionFind: SessionFindCommands?
    /// What the Session menu does to the open session.
    @Entry var sessionMenu: SessionMenuCommands?
}
