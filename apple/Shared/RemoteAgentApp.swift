import RAKit
import SwiftUI
#if os(macOS)
import AppKit
#endif

@main
struct RemoteAgentApp: App {
    @State private var store: ServerStore
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
        _notifier = State(initialValue: Notifier(store: store))
    }
    #else
    init() {
        _store = State(initialValue: ServerStore())
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
        #if os(macOS)
        Settings {
            SettingsView()
                .environment(notifier)
                .environment(updater)
                // As tall as the columns, which grow with the text sizes, up
                // to the screen's height.
                .fixedSize(horizontal: false, vertical: true)
                .appStyle()
        }
        .windowResizability(.contentSize)
        #endif
    }

    // One window. On the Mac a Window scene has no New Window; on iPad,
    // project.yml turns off multiple scenes.
    #if os(macOS)
    private var mainWindow: some Scene {
        Window("Remote Agent", id: "main") {
            WindowRoot()
                .environment(store)
                .environment(notifier)
        }
        .defaultSize(width: 1100, height: 760)
        .commands { AppCommands(updater: updater) }
    }
    #else
    private var mainWindow: some Scene {
        WindowGroup(id: "main") {
            WindowRoot()
                .environment(store)
        }
    }
    #endif
}

/// The app's window: the Inbox and Servers tabs on iPhone, the split view on
/// iPad and the Mac. A pairing link opens the pairing sheet in it.
private struct WindowRoot: View {
    @State private var pendingLink: PairingLink?
    #if os(iOS)
    @State private var showSettings = false
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
        #if os(iOS)
        .environment(\.showSettings, $showSettings)
        .sheet(isPresented: $showSettings) {
            NavigationStack { SettingsView() }
                .appStyle()
        }
        #endif
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
}
