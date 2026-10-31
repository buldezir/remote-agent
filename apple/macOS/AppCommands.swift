import SwiftUI

/// The Mac menu bar. Like Mail, ⌘N makes the app's main thing, a session.
/// The app has one window, so there is no New Window.
struct AppCommands: Commands {
    let updater: AppUpdater
    @FocusedValue(\.newSession) private var newSession
    @FocusedValue(\.pairServer) private var pairServer
    @FocusedValue(\.searchSessions) private var searchSessions
    @FocusedValue(\.sessionFind) private var sessionFind
    @FocusedValue(\.sessionMenu) private var session
    @AppStorage(SidebarLayout.key) private var layout = SidebarLayout.inbox

    var body: some Commands {
        CommandGroup(after: .appInfo) {
            if updater.isRelease {
                Button("Check for Updates…") { updater.checkForUpdates() }
                    .disabled(!updater.canCheck)
            }
        }
        CommandGroup(replacing: .newItem) {
            Button("New Session") { newSession?.wrappedValue = true }
                .keyboardShortcut("n")
                .disabled(newSession == nil)
            Divider()
            Button("Pair a Server…") { pairServer?.wrappedValue = true }
                .disabled(pairServer == nil)
        }
        // Edit › Find, as Safari's: searching every session, and finding in
        // the open one.
        CommandGroup(after: .pasteboard) {
            Divider()
            Menu("Find") {
                Button("Search Sessions") { searchSessions?.wrappedValue = true }
                    .keyboardShortcut("f", modifiers: [.command, .shift])
                    .disabled(searchSessions == nil)
                Divider()
                Button("Find in Session") { sessionFind?.open() }
                    .keyboardShortcut("f")
                    .disabled(sessionFind == nil)
                Button("Find Next") { sessionFind?.next() }
                    .keyboardShortcut("g")
                    .disabled(sessionFind?.canStep != true)
                Button("Find Previous") { sessionFind?.previous() }
                    .keyboardShortcut("g", modifiers: [.command, .shift])
                    .disabled(sessionFind?.canStep != true)
            }
        }
        // The open session's ⋯ menu, as Mail's Message menu has what its
        // toolbar does. Commit… takes Xcode's shortcut.
        CommandMenu("Session") {
            Button("Rename…") { session?.rename?() }
                .disabled(session?.rename == nil)
            Divider()
            Button("Changes") { session?.changes?() }
                .disabled(session?.changes == nil)
            Button("Commit…") { session?.commit?() }
                .keyboardShortcut("c", modifiers: [.command, .option])
                .disabled(session?.commit == nil)
            Button("Fork…") { session?.fork?() }
                .disabled(session?.fork == nil)
        }
        SidebarCommands()
        // The sidebar's layouts, as Finder's View menu has "as Icons" and "as List".
        CommandGroup(after: .sidebar) {
            Divider()
            ForEach(Array(SidebarLayout.allCases.enumerated()), id: \.element) { i, l in
                Toggle("as \(l.title)", isOn: Binding { layout == l } set: { if $0 { layout = l } })
                    .keyboardShortcut(KeyEquivalent(Character("\(i + 1)")), modifiers: .command)
            }
            Divider()
        }
    }
}
