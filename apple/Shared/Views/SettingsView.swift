import RAKit
import SwiftUI

/// App preferences and, in a wide window, the saved servers.
struct SettingsView: View {
    #if os(macOS)
    var wide = true
    #else
    var wide = false
    #endif
    @Environment(ServerStore.self) private var store
    @AppStorage("settingsServerID") private var selected = ""
    @AppStorage("settingsServerRequest") private var serverRequest = 0
    @State private var servers = false
    @State private var adding = false
    @State private var removing: SavedServer?
    #if os(macOS)
    /// The size the sheet's content takes: most of the window it is over.
    @State private var size: CGSize?
    @Environment(\.dismiss) private var dismiss
    #endif

    var body: some View {
        VStack(spacing: 0) {
            if wide {
                Picker("Settings", selection: $servers) {
                    Text("General").tag(false)
                    Text("Servers").tag(true)
                }
                .pickerStyle(.segmented)
                .labelsHidden()
                .padding()
            }
            tab
        }
        #if os(macOS)
        .frame(width: size?.width ?? 840, height: size?.height ?? 620)
        .background(SheetSize { size = $0 })
        #endif
        .background(Palette.mantle)
        .navigationTitle("Settings")
        .onAppear { servers = !selected.isEmpty }
        .onChange(of: selected) { _, value in if !value.isEmpty { servers = true } }
        .onChange(of: serverRequest) { _, _ in servers = true }
        .task(id: store.servers.map(\.id)) {
            for s in store.servers {
                let c = store.connection(for: s)
                if c.state == .idle { c.start() }
            }
        }
        .sheet(isPresented: $adding) {
            AddServerView { server in selected = server.id }.appStyle()
        }
        .removeServerDialog($removing) { selected = "" }
        .toolbar {
            #if os(macOS)
            // A sheet over the window; Escape closes it too.
            ToolbarItem(placement: .confirmationAction) {
                Button("Done") { dismiss() }.keyboardShortcut(.cancelAction)
            }
            #else
            if wide && servers {
                ToolbarItem(placement: .confirmationAction) { SettingsDoneButton() }
            }
            #endif
        }
    }

    /// The servers, or the general settings.
    @ViewBuilder private var tab: some View {
        if wide && servers {
            HStack(spacing: 0) {
                VStack {
                    List {
                        ForEach(store.servers) { server in
                            let c = store.connection(for: server)
                            Button { selected = server.id } label: {
                                HStack {
                                    VStack(alignment: .leading) {
                                        Text(server.name).scaledFont(.body)
                                        Text(server.urls.first?.absoluteString ?? "")
                                            .scaledFont(.caption).foregroundStyle(.secondary)
                                    }
                                    Spacer()
                                    ConnectionDot(state: c.state)
                                }
                            }
                            .buttonStyle(.plain)
                            .listRowBackground(selected == server.id ? Palette.surface0 : Palette.mantle)
                        }
                    }
                    .listBackground(Palette.mantle)
                    Button("Add Server", systemImage: "plus") { adding = true }.padding()
                }
                .frame(width: 240)
                Divider()
                if let server = store.servers.first(where: { $0.id == selected }) {
                    serverDetail(server).id(server.id)
                } else {
                    ContentUnavailableView(store.servers.isEmpty ? "No Servers" : "Select a Server", systemImage: "server.rack",
                                           description: Text("Add a server to manage it here."))
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                }
            }
            #if os(macOS)
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            #else
            .frame(minHeight: 620)
            #endif
        } else {
            GeneralSettingsView()
        }
    }

    private func serverDetail(_ server: SavedServer) -> some View {
        let c = store.connection(for: server)
        return VStack(alignment: .leading, spacing: 0) {
            HStack {
                Text(server.name).scaledFont(.headline)
                Spacer()
                Button("Reconnect") { c.retry() }
                Button("Remove Server…", role: .destructive) { removing = server }
            }.padding()
            if c.state != .connected {
                Text(ConnectionDot(state: c.state).label).scaledFont(.callout).padding(.horizontal)
            }
            ServerSettingsView(connection: c, embedded: true).id(ObjectIdentifier(c))
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

#if os(iOS)
private struct SettingsDoneButton: View {
    @Environment(\.dismiss) private var dismiss
    var body: some View { Button("Done") { dismiss() } }
}
#endif

/// The app's settings, in a sheet over the window (⌘, on the Mac).
private struct GeneralSettingsView: View {
    @AppStorage(TextSize.interfaceKey) private var interface = TextSize.defaultInterface
    @AppStorage(TextSize.messagesKey) private var messages = TextSize.defaultMessages
    @AppStorage(Appearance.key) private var appearance = Appearance.system
    @AppStorage(TerminalPlacement.key) private var terminalPlacement = TerminalPlacement.over
    @AppStorage(SidebarLayout.key) private var sidebarLayout = SidebarLayout.inbox
    @AppStorage(ToolRunRow.groupKey) private var groupToolCalls = true
    #if os(macOS)
    @AppStorage(FontChoice.messageKey) private var messageFont = ""
    @AppStorage(FontChoice.codeKey) private var codeFont = ""
    @AppStorage(TerminalPanel.copyOnSelectKey) private var copyOnSelect = true
    #else
    @AppStorage(TextSize.followsSystemKey) private var followsSystem = true
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @Environment(\.dismiss) private var dismiss
    #endif

    var body: some View {
        #if os(macOS)
        // Two columns, the text beside the rest, as wide as the sheet; one
        // when it's too narrow for two.
        ScrollView {
            ViewThatFits(in: .horizontal) {
                HStack(alignment: .top, spacing: 0) {
                    column { textSections }
                    column { otherSections }
                }
                VStack(spacing: 0) {
                    column { textSections }
                    column { otherSections }
                }
            }
        }
        .scrollBounceBehavior(.basedOnSize)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
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
    /// the sheet's scroll view scrolls. Two fit side by side from 760 points.
    private func column(@ViewBuilder _ sections: () -> some View) -> some View {
        Form { sections() }
            .compactForm()
            .scrollDisabled(true)
            // The form can come out a fraction of a point taller than its
            // sections, which would show a scroller.
            .scrollIndicators(.never)
            .frame(minWidth: 320, idealWidth: 380, maxWidth: .infinity)
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

    /// The transcript, the sidebar, the terminal, notifications and updates,
    /// and the way back to the defaults.
    @ViewBuilder private var otherSections: some View {
        Section {
            Toggle("Group tool calls", isOn: $groupToolCalls)
        } header: {
            Text("Transcript").formHeaderFont()
        } footer: {
            Text("Folds each run of the agent's tool calls into one line, such as “Ran 4 commands, edited 3 files”, which opens to show them.")
                .formFooterFont()
        }

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
                #if os(macOS)
                Toggle("Copy on select", isOn: $copyOnSelect)
                #endif
            } header: {
                Text("Terminal").formHeaderFont()
            } footer: {
                Text(terminalFooter).formFooterFont()
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

    private var terminalFooter: String {
        let placement = "Where a session's terminal opens. In a narrow window it takes the place of the messages."
        #if os(macOS)
        return placement + " Copy on select puts the text you select on the clipboard."
        #else
        return placement
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
            && sidebarLayout == .inbox && groupToolCalls
    }
    #else
    private var interfaceFooter: String { "Lists and labels." }

    private var isDefault: Bool {
        appearance == .system && interface == TextSize.defaultInterface && messages == TextSize.defaultMessages
            && messageFont.isEmpty && codeFont.isEmpty && terminalPlacement == .over && copyOnSelect && sidebarLayout == .inbox
            && groupToolCalls
    }
    #endif

    private func restoreDefaults() {
        appearance = .system
        terminalPlacement = .over
        sidebarLayout = .inbox
        groupToolCalls = true
        interface = TextSize.defaultInterface
        messages = TextSize.defaultMessages
        #if os(iOS)
        followsSystem = true
        #else
        messageFont = ""
        codeFont = ""
        copyOnSelect = true
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

/// Reports the size the view behind it takes in a sheet: 90% of the window
/// the sheet is over, less the sheet's own bars. As the sheet lays out and
/// when the window resizes.
private struct SheetSize: NSViewRepresentable {
    let changed: @MainActor (CGSize) -> Void

    func makeNSView(context: Context) -> SizeView { SizeView() }

    func updateNSView(_ view: SizeView, context: Context) {
        view.changed = changed
    }

    final class SizeView: NSView {
        var changed: (@MainActor (CGSize) -> Void)?

        override func viewDidMoveToWindow() {
            NotificationCenter.default.removeObserver(self)
            guard let window else { return }
            for w in Set([window, parent(of: window) ?? window]) {
                NotificationCenter.default.addObserver(self, selector: #selector(report),
                                                       name: NSWindow.didResizeNotification, object: w)
            }
            report()
        }

        /// The window the sheet is over. The sheet lays out before AppKit
        /// attaches it, while that window is still the main one.
        private func parent(of sheet: NSWindow) -> NSWindow? {
            sheet.sheetParent ?? NSApp.mainWindow.flatMap { $0 === sheet ? nil : $0 }
        }

        // Once laid out, the bars around it are known.
        override func setFrameSize(_ newSize: NSSize) {
            super.setFrameSize(newSize)
            report()
        }

        @objc private func report() {
            guard let window, let changed, let parent = parent(of: window) else { return }
            // The sheet hangs below the window's toolbar, so it's no taller
            // than the content below it, with a margin.
            let width = (parent.frame.width * 0.9).rounded()
            let height = min(parent.frame.height * 0.9, parent.contentLayoutRect.height - 2 * Metrics.margin).rounded()
            let size = CGSize(width: width - (window.frame.width - frame.width),
                              height: height - (window.frame.height - frame.height))
            // Not while SwiftUI is updating the view.
            Task { @MainActor in changed(size) }
        }
    }
}
#endif

extension EnvironmentValues {
    /// Shows the Settings sheet over the window.
    @Entry var showSettings: Binding<Bool>?
}

#if os(iOS)
/// Opens Settings. The Mac has it in the app menu instead.
struct SettingsButton: View {
    @Environment(\.showSettings) private var showSettings

    var body: some View {
        Button("Settings", systemImage: "gearshape") { showSettings?.wrappedValue = true }
            .keyboardShortcut(",")
    }
}
#endif
