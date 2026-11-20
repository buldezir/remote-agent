import RAKit
import SwiftUI

/// App preferences and, in a wide window, the saved servers.
struct SettingsView: View {
    var wide = false
    @Environment(ServerStore.self) private var store
    @AppStorage("settingsServerID") private var selected = ""
    @AppStorage("settingsServerRequest") private var serverRequest = 0
    @State private var servers = false
    @State private var adding = false
    @State private var removing: SavedServer?

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
            if wide && servers {
                ToolbarItem(placement: .confirmationAction) { SettingsDoneButton() }
            }
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
            .frame(minHeight: 620)
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

private struct SettingsDoneButton: View {
    @Environment(\.dismiss) private var dismiss
    var body: some View { Button("Done") { dismiss() } }
}

/// The app's settings, in a sheet over the window.
private struct GeneralSettingsView: View {
    @AppStorage(TextSize.interfaceKey) private var interface = TextSize.defaultInterface
    @AppStorage(TextSize.messagesKey) private var messages = TextSize.defaultMessages
    @AppStorage(Appearance.key) private var appearance = Appearance.system
    @AppStorage(TerminalPlacement.key) private var terminalPlacement = TerminalPlacement.over
    @AppStorage(SidebarLayout.key) private var sidebarLayout = SidebarLayout.inbox
    @AppStorage(ToolRunRow.groupKey) private var groupToolCalls = true
    @AppStorage(TextSize.followsSystemKey) private var followsSystem = true
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    @Environment(\.dismiss) private var dismiss

    var body: some View {
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
    }

    /// The theme and the text: its sizes and a preview.
    @ViewBuilder private var textSections: some View {
        Section {
            Picker("Theme", selection: $appearance) {
                ForEach(Appearance.allCases) { Text($0.title).tag($0) }
            }
        } header: {
            Text("Appearance")
        } footer: {
            Text("Catppuccin Latte when light, Frappé when dark.")
        }

        Section {
            Toggle("Match System", isOn: matchSystem)
            if !followsSystem { interfaceSlider }
        } header: {
            Text("Interface")
        } footer: {
            Text(interfaceFooter)
        }

        Section {
            SizeSlider(value: $messages, range: TextSize.messageSizes, text: "\(Int(TextSize.messages(messages))) pt")
        } header: {
            Text("Messages")
        } footer: {
            Text(messagesFooter)
        }

        Section {
            VStack(alignment: .leading, spacing: Metrics.gap) {
                UserBubble(text: "Add tests for the parser.")
                AgentMarkdown(text: "Done. The new tests cover **empty input** and `\\r\\n` line endings, and all 42 pass:\n\n```\nswift test --filter ParserTests\n```")
            }
            .padding(.vertical, 4)
            .paletteText()
        } header: {
            Text("Preview")
        }
    }

    /// The transcript, the sidebar, the terminal, and the way back to the
    /// defaults.
    @ViewBuilder private var otherSections: some View {
        Section {
            Toggle("Group tool calls", isOn: $groupToolCalls)
        } header: {
            Text("Transcript")
        } footer: {
            Text("Folds each run of the agent's tool calls into one line, such as “Ran 4 commands, edited 3 files”, which opens to show them.")
        }

        if offersSplitView {
            Section {
                Picker("Layout", selection: $sidebarLayout) {
                    ForEach(SidebarLayout.allCases) { Text($0.title).tag($0) }
                }
            } header: {
                Text("Sidebar")
            } footer: {
                Text(sidebarFooter)
            }
        }

        if offersSplitView {
            Section {
                Picker("Opens", selection: $terminalPlacement) {
                    ForEach(TerminalPlacement.allCases) { Text($0.title).tag($0) }
                }
            } header: {
                Text("Terminal")
            } footer: {
                Text(terminalFooter)
            }
        }

        Section {
            Button("Restore Defaults", action: restoreDefaults)
                .disabled(isDefault)
        }
    }

    /// The sidebar's layout and where the terminal opens: for iPad. An
    /// iPhone shows a server's sessions in a list of their own, and the
    /// terminal in place of the messages, as there's no room beside them.
    private var offersSplitView: Bool {
        UIDevice.current.userInterfaceIdiom == .pad
    }

    private var sidebarFooter: String { sidebarLayout.summary }

    private var terminalFooter: String {
        "Where a session's terminal opens. In a narrow window it takes the place of the messages."
    }

    private var interfaceSlider: some View {
        let step = TextSize.step(interface)
        return SizeSlider(value: Binding(get: { Double(step) }, set: { interface = Int($0.rounded()) }),
                          range: 0...Double(TextSize.interfaceSizes.count - 1),
                          text: "\(Int(TextSize.interfaceSizes[step])) pt")
    }

    private var messagesFooter: String { "Your prompts, the agent's replies and its tool calls." }

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

    private func restoreDefaults() {
        appearance = .system
        terminalPlacement = .over
        sidebarLayout = .inbox
        groupToolCalls = true
        interface = TextSize.defaultInterface
        messages = TextSize.defaultMessages
        followsSystem = true
    }
}

/// A text size slider, from a small A to a large one, with the size it gives.
private struct SizeSlider: View {
    @Binding var value: Double
    let range: ClosedRange<Double>
    let text: String

    var body: some View {
        VStack(spacing: 4) {
            LabeledContent("Text size", value: text)
            slider
        }
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

extension EnvironmentValues {
    /// Shows the Settings sheet over the window.
    @Entry var showSettings: Binding<Bool>?
}

/// Opens Settings.
struct SettingsButton: View {
    @Environment(\.showSettings) private var showSettings

    var body: some View {
        Button("Settings", systemImage: "gearshape") { showSettings?.wrappedValue = true }
            .keyboardShortcut(",")
    }
}
