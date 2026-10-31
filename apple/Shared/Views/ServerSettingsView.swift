import RAKit
import SwiftUI

/// A server's own settings, from its menu: updating rad and the agents,
/// signing an agent in again, the agent new sessions start with, and the
/// system agent that names sessions.
struct ServerSettingsView: View {
    let connection: ServerConnection
    @Environment(\.dismiss) private var dismiss
    @Environment(\.openURL) private var openURL

    @State private var update: UpdateInfo?
    @State private var updateError: String?
    @State private var checking = false
    @State private var settings: ServerSettings?
    @State private var settingsError: String?
    /// The setting `settingsError` is about; nil: loading them failed.
    @State private var failedSetting: AgentSetting?
    /// The server's rad has no update, or no settings, to manage: it's older.
    @State private var updateUnsupported = false
    @State private var settingsUnsupported = false
    /// The tasks followed here, by agent id, and "" for rad's update.
    @State private var progress: [String: ServerTaskProgress] = [:]
    /// The task whose sheet is open.
    @State private var shown: ServerTaskProgress?
    @State private var confirmingUpdate = false
    @State private var actionError: String?

    var body: some View {
        NavigationStack {
            Form {
                Group {
                    radSection
                    agentsSection
                    if !settingsUnsupported {
                        newSessionsSection
                        systemAgentSection
                    }
                    if let actionError {
                        Section { Label(actionError, systemImage: "exclamationmark.triangle").foregroundStyle(Palette.red) }
                    }
                }
                .paletteRows()
            }
            .compactForm()
            #if os(macOS)
            .frame(minWidth: 500, minHeight: 620)
            #endif
            .navigationTitle(connection.server.name)
            .navigationSubtitle("Server")
            .inlineTitle()
            .toolbar {
                ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } }
            }
            // Again when rad comes back as another version, as after an update.
            .task(id: connection.info?.version) { await load() }
            .onDisappear { progress.values.forEach { $0.stop() } }
            .sheet(item: $shown) { p in
                ServerTaskView(progress: p, title: title(of: p.task), agentName: p.task.harness.flatMap { connection.harness($0)?.name })
                    .appStyle()
            }
            .confirmationDialog(interruptTitle, isPresented: $confirmingUpdate, titleVisibility: .visible) {
                Button("Update Anyway", role: .destructive) { Task { await startUpdate() } }
                Button("Cancel", role: .cancel) {}
            } message: {
                Text("Updating restarts rad, which interrupts \(quoted(update?.busy ?? [])).")
            }
        }
    }

    // MARK: rad

    private var radSection: some View {
        Section {
            LabeledContent("Version", value: connection.info?.version ?? update?.current ?? "—")
            if updateUnsupported {
                Text("This rad is too old to update from here. Update it on the computer with `rad update`, or from the Remote Agent Server app's menu.")
                    .scaledFont(.callout)
                    .foregroundStyle(.secondary)
            } else if let p = progress[""], p.isActive {
                taskRow(p, label: p.phase == .restarting ? "Restarting rad…" : "Updating rad…")
            } else if let update {
                updateStatus(update)
            } else if let updateError {
                Text("Couldn't check for updates: \(updateError)").foregroundStyle(Palette.red)
            } else {
                HStack { ProgressView().controlSize(.small); Text("Checking for updates…").foregroundStyle(.secondary) }
            }
            if !updateUnsupported {
                Button("Check Again") { Task { await checkUpdate(refresh: true) } }
                    .disabled(checking || progress[""]?.isActive == true)
            }
        } header: {
            Text("rad").formHeaderFont()
        } footer: {
            if let footer = updateFooter { Text(footer).formFooterFont() }
        }
    }

    @ViewBuilder private func updateStatus(_ update: UpdateInfo) -> some View {
        if update.available, let latest = update.latest {
            Text("rad \(latest) is available.")
            if update.installable {
                Button {
                    if update.busy.isEmpty { Task { await startUpdate() } } else { confirmingUpdate = true }
                } label: {
                    Text("Update to \(latest)").foregroundStyle(Palette.onAccent)
                }
                .buttonStyle(.borderedProminent)
            } else if let reason = update.reason {
                Text(reason).scaledFont(.callout).foregroundStyle(.secondary)
            }
            if let url = update.url.flatMap(URL.init(string:)) {
                Button("What's New") { openURL(url) }
            }
        } else if update.latest != nil && update.reason == nil {
            Label("Up to date.", systemImage: "checkmark.circle").foregroundStyle(Palette.green)
        } else if let reason = update.reason {
            Text(reason).scaledFont(.callout).foregroundStyle(.secondary)
        }
    }

    private var updateFooter: String? {
        guard let update, update.installable else { return nil }
        var text = update.via == "app"
            ? "The Remote Agent Server app on the Mac installs the update and restarts, with rad."
            : "rad installs the update and restarts. Sessions reconnect by themselves."
        if !update.busy.isEmpty {
            let n = update.busy.count
            text += " Updating now interrupts \(n) busy \(n == 1 ? "session" : "sessions"): \(update.busy.joined(separator: ", "))."
        }
        return text
    }

    private var interruptTitle: String {
        let n = update?.busy.count ?? 0
        return "Interrupt \(n) \(n == 1 ? "session" : "sessions")?"
    }

    private func quoted(_ titles: [String]) -> String {
        titles.map { "“\($0)”" }.joined(separator: ", ")
    }

    // MARK: Agents

    private var agentsSection: some View {
        Section {
            if let harnesses = connection.harnesses {
                ForEach(harnesses) { agentRow($0) }
                if harnesses.isEmpty {
                    Text("No agents are installed on the server.").foregroundStyle(.secondary)
                }
            } else {
                HStack { ProgressView().controlSize(.small); Text("Checking installed agents…").foregroundStyle(.secondary) }
            }
        } header: {
            Text("Agents").formHeaderFont()
        } footer: {
            if connection.harnesses?.contains(where: { $0.caps.update == true || $0.caps.login == true }) == true {
                Text("Update runs the agent's own updater on the server. Sign In shows the agent's sign-in page here, and takes the code it gives you.")
                    .formFooterFont()
            }
        }
    }

    private func agentRow(_ h: HarnessInfo) -> some View {
        HStack(spacing: Metrics.gap) {
            // A column of its own, so that the names line up.
            HarnessIcon(id: h.id)
                .frame(width: 24)
            VStack(alignment: .leading, spacing: 2) {
                Text([h.name, h.version ?? ""].joined(separator: " ").trimmingCharacters(in: .whitespaces))
                if h.authOk {
                    Text("Ready").scaledFont(.caption).foregroundStyle(Palette.green)
                } else {
                    Text(h.hint ?? "Not signed in").scaledFont(.caption).foregroundStyle(Palette.peach)
                }
            }
            Spacer(minLength: 0)
            if let p = progress[h.id], p.isActive {
                taskRow(p, label: p.task.kind == .login ? "Signing in…" : "Updating…")
            } else {
                if h.caps.login == true && !h.authOk {
                    Button("Sign In") { Task { await start(h, login: true) } }
                        .buttonStyle(.bordered)
                }
                if h.caps.update == true || (h.caps.login == true && h.authOk) {
                    Menu {
                        if h.caps.update == true {
                            Button("Update", systemImage: "arrow.down.circle") { Task { await start(h, login: false) } }
                        }
                        if h.caps.login == true && h.authOk {
                            Button("Sign In Again", systemImage: "person.crop.circle.badge.checkmark") { Task { await start(h, login: true) } }
                        }
                        if let p = progress[h.id] {
                            Button("Show Last Output", systemImage: "text.alignleft") { shown = p }
                        }
                    } label: {
                        Label("\(h.name) actions", systemImage: "ellipsis.circle").labelStyle(.iconOnly)
                    }
                    .menuIndicator(.hidden)
                    .fixedSize()
                    #if os(macOS)
                    .menuStyle(.borderlessButton)
                    #endif
                }
            }
        }
    }

    /// A task that runs: what it does, and a button that shows it.
    private func taskRow(_ p: ServerTaskProgress, label: String) -> some View {
        Button {
            shown = p
        } label: {
            HStack(spacing: 6) {
                ProgressView().controlSize(.small)
                Text(label).foregroundStyle(.secondary)
                Text("Show")
            }
        }
        .buttonStyle(.borderless)
    }

    // MARK: Agent settings

    /// One of the settings that name an agent, with its model and effort.
    private typealias AgentSetting = WritableKeyPath<ServerSettings, AgentSpec?>

    private var newSessionsSection: some View {
        agentSection(\.defaultAgent, agents: connection.harnesses ?? [], none: "Last Used",
                     header: "New Sessions",
                     footer: "New sessions start with this agent, model and effort, on every device, and so do the sessions agents start with rad's tools unless they choose their own. With Last Used, each device starts with the agent it used last.")
    }

    private var systemAgentSection: some View {
        agentSection(\.systemAgent, agents: oneShotAgents, none: "None",
                     header: "System Agent",
                     footer: "Names new sessions and their worktree branches from the first prompt, in a call without tools that leaves no history with the agent. A small, fast model is best. With None, a session's title is its prompt's first line.")
    }

    private var oneShotAgents: [HarnessInfo] {
        (connection.harnesses ?? []).filter { $0.caps.oneShot == true }
    }

    /// A setting's section: the agent, from `agents` or `none`, and once one
    /// is chosen, its model and effort.
    private func agentSection(_ setting: AgentSetting, agents: [HarnessInfo], none: LocalizedStringKey,
                              header: LocalizedStringKey, footer: LocalizedStringKey) -> some View {
        Section {
            if let settings {
                let agent = settings[keyPath: setting]
                Picker("Agent", selection: agentBinding(setting)) {
                    Text(none).tag("")
                    ForEach(agents) { Text($0.name).tag($0.id) }
                    if let id = agent?.harness, !agents.contains(where: { $0.id == id }) {
                        Text(id).tag(id)
                    }
                }
                if let agent {
                    let h = connection.harness(agent.harness)
                    let models = h?.models ?? []
                    Picker("Model", selection: binding(setting, \.model)) {
                        Text("Default").tag("")
                        ForEach(models) { Text($0.name).tag($0.id) }
                        if let m = agent.model, !m.isEmpty, !models.contains(where: { $0.id == m }) {
                            Text(m).tag(m)
                        }
                    }
                    let efforts = h?.efforts(forModel: agent.model) ?? []
                    if !efforts.isEmpty {
                        Picker("Effort", selection: binding(setting, \.effort)) {
                            Text("Default").tag("")
                            ForEach(efforts) { Text($0.name).tag($0.id) }
                            if let e = agent.effort, !e.isEmpty, !efforts.contains(where: { $0.id == e }) {
                                Text(e).tag(e)
                            }
                        }
                    }
                }
            } else if settingsError == nil {
                HStack { ProgressView().controlSize(.small); Text("Loading…").foregroundStyle(.secondary) }
            }
            if let settingsError, failedSetting == nil || failedSetting == setting {
                Label(settingsError, systemImage: "exclamationmark.triangle").foregroundStyle(Palette.red)
            }
        } header: {
            Text(header).formHeaderFont()
        } footer: {
            Text(footer).formFooterFont()
        }
    }

    /// Choosing another agent starts it on its default model.
    private func agentBinding(_ setting: AgentSetting) -> Binding<String> {
        Binding {
            settings?[keyPath: setting]?.harness ?? ""
        } set: { id in
            guard id != (settings?[keyPath: setting]?.harness ?? "") else { return }
            save(setting, id.isEmpty ? nil : AgentSpec(harness: id))
        }
    }

    /// The model or the effort, "" for the default. A new model starts on its
    /// default effort.
    private func binding(_ setting: AgentSetting, _ field: WritableKeyPath<AgentSpec, String?>) -> Binding<String> {
        Binding {
            settings?[keyPath: setting]?[keyPath: field] ?? ""
        } set: { value in
            guard var agent = settings?[keyPath: setting], value != (agent[keyPath: field] ?? "") else { return }
            agent[keyPath: field] = value.isEmpty ? nil : value
            if field == \AgentSpec.model { agent.effort = nil }
            save(setting, agent)
        }
    }

    /// Changes one setting, keeping the others, since the server replaces
    /// them all. Shows it at once, then the server's answer.
    private func save(_ setting: AgentSetting, _ agent: AgentSpec?) {
        guard var new = settings else { return }
        let old = new[keyPath: setting]
        new[keyPath: setting] = agent
        settings = new
        settingsError = nil
        Task {
            do {
                settings = try await connection.setSettings(new)
            } catch {
                settings?[keyPath: setting] = old
                settingsError = error.localizedDescription
                failedSetting = setting
            }
        }
    }

    // MARK: Loading and tasks

    private func load() async {
        updateUnsupported = false
        settingsUnsupported = false
        // The server's probes are cached; this picks up an agent that signed out.
        await connection.refreshHarnesses()
        async let checked: Void = checkUpdate(refresh: false)
        do {
            settings = try await connection.settings()
        } catch let e as RPCError where e.code == "method_not_found" {
            settingsUnsupported = true
        } catch {
            settingsError = "Couldn't load the settings: \(error.localizedDescription)"
            failedSetting = nil
        }
        // Tasks still running from before, here or on another device.
        if let tasks = try? await connection.tasks() {
            for t in tasks where t.isRunning { follow(t) }
        }
        await checked
    }

    private func checkUpdate(refresh: Bool) async {
        checking = true
        defer { checking = false }
        do {
            update = try await connection.checkUpdate(refresh: refresh)
            updateError = nil
        } catch let e as RPCError where e.code == "method_not_found" {
            updateUnsupported = true
        } catch {
            if update == nil { updateError = error.localizedDescription }
        }
    }

    private func startUpdate() async {
        actionError = nil
        do {
            shown = follow(try await connection.updateServer())
        } catch {
            actionError = "Couldn't update rad: \(error.localizedDescription)"
        }
    }

    private func start(_ h: HarnessInfo, login: Bool) async {
        actionError = nil
        do {
            let t = login ? try await connection.loginHarness(h.id) : try await connection.updateHarness(h.id)
            shown = follow(t)
        } catch {
            actionError = login ? "Couldn't sign in to \(h.name): \(error.localizedDescription)"
                                : "Couldn't update \(h.name): \(error.localizedDescription)"
        }
    }

    /// Follows a task here, once: starting one that runs returns it again.
    @discardableResult private func follow(_ t: ServerTask) -> ServerTaskProgress {
        let key = t.kind == .updateServer ? "" : t.harness ?? t.id
        if let p = progress[key], p.id == t.id { return p }
        progress[key]?.stop()
        let p = ServerTaskProgress(task: t, source: connection)
        progress[key] = p
        p.start()
        return p
    }

    private func title(of t: ServerTask) -> String {
        let name = t.harness.flatMap { connection.harness($0)?.name } ?? t.harness ?? "the agent"
        switch t.kind {
        case .updateServer: return "Updating rad"
        case .login: return "Signing In to \(name)"
        case .commit: return "Committing"
        case .updateAgent, .unknown: return "Updating \(name)"
        }
    }
}

/// A task's progress: its state, the sign-in page and code when it asks for
/// them, and what its command printed.
struct ServerTaskView: View {
    let progress: ServerTaskProgress
    let title: String
    var agentName: String?
    @Environment(\.dismiss) private var dismiss
    @Environment(\.openURL) private var openURL
    @State private var code = ""
    @State private var error: String?
    @FocusState private var codeFocused: Bool

    private var task: ServerTask { progress.task }

    var body: some View {
        NavigationStack {
            VStack(alignment: .leading, spacing: Metrics.gap * 1.5) {
                status
                if progress.phase == .running, let url = task.url.flatMap(URL.init(string:)), ["http", "https"].contains(url.scheme) {
                    HStack {
                        Button { openURL(url) } label: {
                            Text("Open Sign-In Page").foregroundStyle(Palette.onAccent)
                        }
                        .buttonStyle(.borderedProminent)
                        Button("Copy Link") { Platform.copy(url.absoluteString) }
                            .buttonStyle(.bordered)
                    }
                }
                if progress.phase == .running, task.input == true {
                    HStack {
                        TextField("Paste the code here", text: $code)
                            .plainTextInput()
                            .textFieldStyle(.roundedBorder)
                            .focused($codeFocused)
                            .onSubmit(send)
                        Button("Send", action: send)
                            .buttonStyle(.bordered)
                            .disabled(code.trimmingCharacters(in: .whitespaces).isEmpty)
                    }
                }
                if let error {
                    Label(error, systemImage: "exclamationmark.triangle").foregroundStyle(Palette.red)
                }
                output
            }
            .scaledFont(.body)
            .padding(Metrics.margin)
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            .background(Palette.base)
            #if os(macOS)
            .frame(minWidth: 520, minHeight: 420)
            #endif
            .navigationTitle(title)
            .inlineTitle()
            .toolbar {
                if progress.phase == .running {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Cancel") { Task { try? await progress.cancel() } }
                    }
                }
                ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } }
            }
        }
    }

    @ViewBuilder private var status: some View {
        switch progress.phase {
        case .running:
            HStack(spacing: Metrics.gap) {
                ProgressView().controlSize(.small)
                Text(task.url != nil && task.kind == .login ? "Waiting for you to sign in…" : "Running…")
            }
        case .restarting:
            HStack(spacing: Metrics.gap) {
                ProgressView().controlSize(.small)
                Text("Restarting rad…")
            }
        case .done:
            Label(doneText, systemImage: "checkmark.circle.fill").foregroundStyle(Palette.green)
        case .failed(let why):
            Label("Failed: \(why)", systemImage: "xmark.octagon.fill").foregroundStyle(Palette.red)
        case .cancelled:
            Label("Cancelled.", systemImage: "xmark.circle").foregroundStyle(.secondary)
        }
    }

    private var doneText: String {
        switch task.kind {
        case .updateServer: "rad \(task.version ?? "") is running."
        case .login: "Signed in."
        case .commit: "Done."
        case .updateAgent, .unknown:
            if let v = task.version { "\(agentName ?? task.harness ?? "") \(v)" } else { "Updated." }
        }
    }

    @ViewBuilder private var output: some View {
        if !task.output.isEmpty {
            ScrollViewReader { proxy in
                ScrollView {
                    Text(task.output)
                        .scaledFont(.caption, design: .monospaced)
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(Metrics.padding)
                    Color.clear.frame(height: 1).id("end")
                }
                .background(Palette.mantle, in: RoundedRectangle(cornerRadius: 8))
                .onChange(of: task.output, initial: true) { _, _ in proxy.scrollTo("end", anchor: .bottom) }
            }
        }
    }

    private func send() {
        let text = code.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        error = nil
        Task {
            do {
                try await progress.send(text)
                code = ""
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}
