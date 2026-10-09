import SwiftUI

/// The Mac menu bar. Like Mail, ⌘N makes the app's main thing, a session,
/// and ⌥⌘N opens another window.
struct AppCommands: Commands {
    @FocusedValue(\.newSession) private var newSession
    @FocusedValue(\.pairServer) private var pairServer
    @Environment(\.openWindow) private var openWindow

    var body: some Commands {
        CommandGroup(replacing: .newItem) {
            Button("New Session") { newSession?.wrappedValue = true }
                .keyboardShortcut("n")
                .disabled(newSession == nil)
            Button("New Window") { openWindow(id: "main") }
                .keyboardShortcut("n", modifiers: [.command, .option])
            Divider()
            Button("Pair a Server…") { pairServer?.wrappedValue = true }
                .disabled(pairServer == nil)
        }
        SidebarCommands()
    }
}
