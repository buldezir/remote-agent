import SwiftUI

/// The app's settings. On the Mac, the Settings window (⌘,); on iOS, a sheet.
struct SettingsView: View {
    @AppStorage(TextSize.interfaceKey) private var interface = TextSize.defaultInterface
    @AppStorage(TextSize.messagesKey) private var messages = TextSize.defaultMessages
    @AppStorage(Appearance.key) private var appearance = Appearance.system
    @AppStorage(TerminalPlacement.key) private var terminalPlacement = TerminalPlacement.over
    @AppStorage(SidebarLayout.key) private var sidebarLayout = SidebarLayout.inbox
    #if os(macOS)
    @AppStorage(FontChoice.messageKey) private var messageFont = ""
    @AppStorage(FontChoice.codeKey) private var codeFont = ""
    #else
    @AppStorage(TextSize.followsSystemKey) private var followsSystem = true
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @Environment(\.dismiss) private var dismiss
    #endif

    var body: some View {
        #if os(macOS)
        // Two columns, the text beside the rest, so that the window fits a
        // laptop's screen. A screen too short for them still scrolls.
        ScreenHeightScrollView {
            HStack(alignment: .top, spacing: 0) {
                column { textSections }
                column { otherSections }
            }
        }
        .background(Palette.mantle)
        .navigationTitle("Settings")
        #else
        Form {
            Group {
                textSections
                otherSections
            }
            .paletteRows()
        }
        .compactForm()
        .navigationTitle("Settings")
        .inlineTitle()
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                Button("Done") { dismiss() }
            }
        }
        #endif
    }

    #if os(macOS)
    /// A column of sections: a form of its own, as tall as they are, which
    /// the window's scroll view scrolls.
    private func column(@ViewBuilder _ sections: () -> some View) -> some View {
        Form { sections() }
            .compactForm()
            .scrollDisabled(true)
            // The form can come out a fraction of a point taller than its
            // sections, which would show a scroller.
            .scrollIndicators(.never)
            .frame(width: 420)
    }
    #endif

    /// The theme and the text: its sizes, fonts and a preview.
    @ViewBuilder private var textSections: some View {
        Section {
            // A pop-up, like the font pickers: a Mac segmented control
            // writes its selected label in white on the accent.
            Picker("Theme", selection: $appearance) {
                ForEach(Appearance.allCases) { Text($0.title).tag($0) }
            }
        } header: {
            Text("Appearance").formHeaderFont()
        } footer: {
            Text("Catppuccin Latte when light, Frappé when dark.").formFooterFont()
        }

        Section {
            #if os(iOS)
            Toggle("Match System", isOn: matchSystem)
            if !followsSystem { interfaceSlider }
            #else
            interfaceSlider
            #endif
        } header: {
            Text("Interface").formHeaderFont()
        } footer: {
            Text(interfaceFooter).formFooterFont()
        }

        Section {
            #if os(macOS)
            fontPickers
            #endif
            SizeSlider(value: $messages, range: TextSize.messageSizes, text: "\(Int(TextSize.messages(messages))) pt")
        } header: {
            Text("Messages").formHeaderFont()
        } footer: {
            Text(messagesFooter).formFooterFont()
        }

        Section {
            VStack(alignment: .leading, spacing: Metrics.gap) {
                UserBubble(text: "Add tests for the parser.")
                AgentMarkdown(text: "Done. The new tests cover **empty input** and `\\r\\n` line endings, and all 42 pass:\n\n```\nswift test --filter ParserTests\n```")
            }
            .padding(.vertical, 4)
            .paletteText()
        } header: {
            Text("Preview").formHeaderFont()
        }
    }

    /// The sidebar, the terminal, notifications and updates, and the way
    /// back to the defaults.
    @ViewBuilder private var otherSections: some View {
        if offersSplitView {
            Section {
                Picker("Layout", selection: $sidebarLayout) {
                    ForEach(SidebarLayout.allCases) { Text($0.title).tag($0) }
                }
            } header: {
                Text("Sidebar").formHeaderFont()
            } footer: {
                Text(sidebarFooter).formFooterFont()
            }
        }

        if offersSplitView {
            Section {
                Picker("Opens", selection: $terminalPlacement) {
                    ForEach(TerminalPlacement.allCases) { Text($0.title).tag($0) }
                }
            } header: {
                Text("Terminal").formHeaderFont()
            } footer: {
                Text("Where a session's terminal opens. In a narrow window it takes the place of the messages.").formFooterFont()
            }
        }

        #if os(macOS)
        NotificationsSection()
        UpdatesSection()
        #endif

        Section {
            Button("Restore Defaults", action: restoreDefaults)
                .disabled(isDefault)
        }
    }

    /// The sidebar's layout and where the terminal opens: for iPad and the
    /// Mac. An iPhone shows a server's sessions in a list of their own, and the
    /// terminal in place of the messages, as there's no room beside them.
    private var offersSplitView: Bool {
        #if os(iOS)
        UIDevice.current.userInterfaceIdiom == .pad
        #else
        true
        #endif
    }

    private var sidebarFooter: String {
        #if os(macOS)
        "\(sidebarLayout.summary) The View menu switches them too, with ⌘1, ⌘2 and ⌘3."
        #else
        sidebarLayout.summary
        #endif
    }

    private var interfaceSlider: some View {
        let step = TextSize.step(interface)
        return SizeSlider(value: Binding(get: { Double(step) }, set: { interface = Int($0.rounded()) }),
                          range: 0...Double(TextSize.interfaceSizes.count - 1),
                          text: "\(Int(TextSize.interfaceSizes[step])) pt")
    }

    #if os(macOS)
    private var fontPickers: some View {
        Group {
            Picker("Font", selection: fontBinding($messageFont, fallback: .message)) {
                Text("System").tag("")
                Text("System Serif").tag("system:serif")
                Text("System Rounded").tag("system:rounded")
                Text("System Mono").tag("system:monospaced")
                Divider()
                ForEach(FontChoice.installedFamilies, id: \.self) { Text($0).tag($0) }
            }
            Picker("Code font", selection: fontBinding($codeFont, fallback: .code)) {
                Text("System Mono").tag("")
                Divider()
                ForEach(FontChoice.monospacedFamilies, id: \.self) { Text($0).tag($0) }
            }
        }
    }

    /// Reads a family that is no longer installed as the default, so the
    /// picker always has a selection.
    private func fontBinding(_ stored: Binding<String>, fallback: FontChoice) -> Binding<String> {
        Binding {
            FontChoice(stored: stored.wrappedValue, fallback: fallback).stored(fallback: fallback)
        } set: {
            stored.wrappedValue = $0
        }
    }

    private var messagesFooter: String {
        "Your prompts, the agent's replies and its tool calls. Tool calls and diffs use the code font."
    }
    #else
    private var messagesFooter: String { "Your prompts, the agent's replies and its tool calls." }
    #endif

    #if os(iOS)
    /// Turning it off starts the slider at the system's size.
    private var matchSystem: Binding<Bool> {
        Binding {
            followsSystem
        } set: { follows in
            if !follows {
                interface = TextSize.dynamicTypeSizes.lastIndex { $0 <= dynamicTypeSize } ?? 0
            }
            followsSystem = follows
        }
    }

    private var interfaceFooter: String {
        followsSystem ? "Lists and labels, at the text size set in the Settings app."
                      : "Lists and labels."
    }

    private var isDefault: Bool {
        appearance == .system && followsSystem && messages == TextSize.defaultMessages && terminalPlacement == .over
            && sidebarLayout == .inbox
    }
    #else
    private var interfaceFooter: String { "Lists and labels." }

    private var isDefault: Bool {
        appearance == .system && interface == TextSize.defaultInterface && messages == TextSize.defaultMessages
            && messageFont.isEmpty && codeFont.isEmpty && terminalPlacement == .over && sidebarLayout == .inbox
    }
    #endif

    private func restoreDefaults() {
        appearance = .system
        terminalPlacement = .over
        sidebarLayout = .inbox
        interface = TextSize.defaultInterface
        messages = TextSize.defaultMessages
        #if os(iOS)
        followsSystem = true
        #else
        messageFont = ""
        codeFont = ""
        #endif
    }
}

/// A text size slider, from a small A to a large one, with the size it gives.
private struct SizeSlider: View {
    @Binding var value: Double
    let range: ClosedRange<Double>
    let text: String

    var body: some View {
        #if os(macOS)
        LabeledContent("Text size") {
            HStack {
                slider
                Text(text)
                    .monospacedDigit()
                    .foregroundStyle(.secondary)
                    .frame(minWidth: 40, alignment: .trailing)
            }
        }
        #else
        VStack(spacing: 4) {
            LabeledContent("Text size", value: text)
            slider
        }
        #endif
    }

    private var slider: some View {
        Slider(value: $value, in: range, step: 1) {
            Text("Text size")
        } minimumValueLabel: {
            Image(systemName: "textformat.size.smaller")
        } maximumValueLabel: {
            Image(systemName: "textformat.size.larger")
        }
        .labelsHidden()
        .accessibilityValue(text)
    }
}

#if os(macOS)
/// Turns notifications on and off, and says when macOS holds them back.
private struct NotificationsSection: View {
    @Environment(Notifier.self) private var notifier
    @Environment(\.openURL) private var openURL
    @AppStorage(Notifier.enabledKey) private var enabled = true

    var body: some View {
        Section {
            Toggle("Show notifications", isOn: $enabled)
            if blocked {
                Button("Open System Settings") {
                    let id = Bundle.main.bundleIdentifier ?? ""
                    openURL(URL(string: "x-apple.systempreferences:com.apple.Notifications-Settings.extension?id=\(id)")!)
                }
            }
        } header: {
            Text("Notifications").formHeaderFont()
        } footer: {
            Text(blocked ? "Remote Agent's notifications are turned off in System Settings."
                         : "When a session needs you, finishes or fails, unless the window shows it.")
                .formFooterFont()
        }
        .onChange(of: enabled) { _, on in
            if on { Task { await notifier.requestAuthorization() } }
        }
        .task { await notifier.refreshAuthorization() }
    }

    private var blocked: Bool { enabled && notifier.authorization == .denied }
}

/// Whether the app looks for updates, and installs them by itself.
private struct UpdatesSection: View {
    @Environment(AppUpdater.self) private var updater

    var body: some View {
        @Bindable var updater = updater
        if updater.isRelease {
            Section {
                Toggle("Check for updates automatically", isOn: $updater.checksAutomatically)
                Toggle("Download and install automatically", isOn: $updater.downloadsAutomatically)
                    .disabled(!updater.checksAutomatically)
            } header: {
                Text("Updates").formHeaderFont()
            } footer: {
                Text(updater.downloadsAutomatically && updater.checksAutomatically
                     ? "Once a day. A new version installs when you quit Remote Agent."
                     : "Once a day. Remote Agent asks before installing a new version.")
                    .formFooterFont()
            }
        }
    }
}

/// As tall as its content, unless the window's screen has less room, below
/// the menu bar and above the Dock: then it scrolls.
private struct ScreenHeightScrollView<Content: View>: View {
    @ViewBuilder let content: Content
    @State private var contentHeight: CGFloat?
    @State private var available = CGFloat.infinity

    var body: some View {
        ScrollView {
            content
                // Rounded up: a fraction of a point more than the window
                // would show a scroller.
                .onGeometryChange(for: CGFloat.self) { $0.size.height.rounded(.up) } action: { contentHeight = $0 }
        }
        .scrollBounceBehavior(.basedOnSize)
        .frame(height: contentHeight.map { min($0, available) })
        .background(WindowScreen { available = $0 })
    }
}

/// Reports the height a window's content can take on its screen: when the
/// window opens, moves to another screen, or the screens change.
private struct WindowScreen: NSViewRepresentable {
    let changed: @MainActor (CGFloat) -> Void

    func makeNSView(context: Context) -> ScreenView { ScreenView() }

    func updateNSView(_ view: ScreenView, context: Context) {
        view.changed = changed
    }

    final class ScreenView: NSView {
        var changed: (@MainActor (CGFloat) -> Void)?

        override func viewDidMoveToWindow() {
            NotificationCenter.default.removeObserver(self)
            guard let window else { return }
            NotificationCenter.default.addObserver(self, selector: #selector(report),
                                                   name: NSWindow.didChangeScreenNotification, object: window)
            NotificationCenter.default.addObserver(self, selector: #selector(report),
                                                   name: NSApplication.didChangeScreenParametersNotification, object: nil)
            report()
        }

        @objc private func report() {
            guard let window, let screen = window.screen, let changed else { return }
            // The screen's room for windows, less the title bar.
            let height = screen.visibleFrame.height - (window.frame.height - window.contentLayoutRect.height)
            // Not while SwiftUI is updating the view.
            Task { @MainActor in changed(height) }
        }
    }
}
#endif

#if os(iOS)
extension EnvironmentValues {
    /// Shows the Settings sheet over the window.
    @Entry var showSettings: Binding<Bool>?
}

/// Opens Settings. The Mac has it in the app menu instead.
struct SettingsButton: View {
    @Environment(\.showSettings) private var showSettings

    var body: some View {
        Button("Settings", systemImage: "gearshape") { showSettings?.wrappedValue = true }
            .keyboardShortcut(",")
    }
}
#endif
