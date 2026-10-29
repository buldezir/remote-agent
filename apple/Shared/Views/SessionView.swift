import RAKit
import SwiftUI

struct SessionView: View {
    let connection: ServerConnection
    @Bindable var store: SessionStore
    /// The prompt takes the keyboard: the session was just started empty.
    var focusesPrompt = false
    @State private var commands: [SlashCommand] = []
    @State private var commandIndex = 0
    @State private var commandsDismissed = false
    @State private var commandsError = false
    @State private var loadingCommands = false
    @State private var sending = false
    /// The draft is being saved, or taken back into the composer.
    @State private var savingDraft = false
    @State private var error: String?
    @State private var showDiff = false
    @State private var showTerminal = false
    @State private var terminalFocused = false
    @State private var height: CGFloat = 0
    @AppStorage(TerminalPlacement.key) private var placementSetting = TerminalPlacement.over
    @AppStorage("terminalHeight") private var terminalHeight: Double = 280
    #if os(iOS)
    @Environment(\.horizontalSizeClass) private var sizeClass
    @Environment(\.dismiss) private var dismiss
    #endif
    @State private var dictation = Dictation()
    @State private var attachments = Attachments()
    @State private var scrollPosition = ScrollPosition(edge: .bottom)
    @FocusState private var composerFocused: Bool
    /// Where the prompt's cursor goes, when it's set from here.
    @State private var promptSelection: TextSelection?
    #if os(macOS)
    @Environment(\.inlineSessionTitle) private var inlineTitle
    @Environment(\.appearsActive) private var appearsActive
    #else
    @Environment(\.scenePhase) private var phase
    #endif

    private var session: Session? { store.session ?? connection.sessions[store.sessionID] }
    /// The session was deleted, here or on another device.
    private var deleted: Bool { store.removed || (connection.indexSynced && connection.sessions[store.sessionID] == nil) }
    private var harness: HarnessInfo? { session.flatMap { connection.harness($0.harness) } }
    private var project: Project? { session.flatMap { connection.projects[$0.projectId] } }

    /// The window is in front: on the Mac the active one, on iOS on screen.
    private var inFront: Bool {
        #if os(macOS)
        appearsActive
        #else
        phase == .active
        #endif
    }

    /// The index says the session is unread while it shows here, in front: a
    /// turn ended while the window was behind, or just now, in view.
    private var unreadInView: Bool { inFront && connection.sessions[store.sessionID]?.isUnread == true }

    var body: some View {
        // The transcript keeps its place: in place of the messages it stays
        // underneath, hidden, and the terminal below them is the stack's
        // second child.
        VStack(spacing: 0) {
            transcript
                .imagePreviews()
                .opacity(covered ? 0 : 1)
                .allowsHitTesting(!covered)
                .accessibilityHidden(covered)
                .overlay {
                    if covered, let s = session { terminalPanel(s) }
                }
            if showTerminal, placement == .below, let s = session {
                PanelResizeHandle(height: $terminalHeight, range: 120...max(120, Double(height) - 200))
                terminalPanel(s)
                    .frame(height: min(terminalHeight, max(120, Double(height) - 200)))
            }
        }
        .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { height = $0 }
        .background { terminalShortcuts }
        #if os(macOS)
        .safeAreaInset(edge: .top, spacing: 0) {
            if inlineTitle, let s = session { titleHeader(s) }
        }
        #endif
        #if os(iOS)
        .onChange(of: deleted, initial: true) { _, deleted in
            // An iPhone pushed the session, so it goes back to the list. The
            // iPad's split view drops the session from its selection.
            if deleted && UIDevice.current.userInterfaceIdiom == .phone { dismiss() }
        }
        #endif
        .navigationTitle(session?.title ?? "Session")
        #if os(macOS)
        .navigationSubtitle(session.map(macSubtitle) ?? "")
        #endif
        .inlineTitle()
        .toolbar { toolbar }
        .sheet(isPresented: $showDiff) {
            NavigationStack {
                DiffView(store: store, initialTurn: nil, canRevert: session?.workspace.kind == .worktree)
            }
            .presentationSizing(.page)
            .appStyle()
        }
        .inspector(isPresented: terminalShown(.right)) {
            // The Mac builds a hidden inspector's content too; it would connect.
            if showTerminal, placement == .right, let s = session {
                terminalPanel(s)
                    .inspectorColumnWidth(min: 320, ideal: 440, max: 900)
            }
        }
        .alert("Error", isPresented: .init(get: { error != nil }, set: { if !$0 { error = nil } })) {
            Button("OK") { error = nil }
        } message: {
            Text(error ?? "")
        }
        .onChange(of: dictation.error) { _, e in
            if let e { error = e; dictation.error = nil }
        }
        .onChange(of: attachments.error) { _, e in
            if let e { error = e; attachments.error = nil }
        }
        .task(id: "\(store.sessionID):\(wantsCommands)") {
            loadingCommands = false
            guard wantsCommands else { return }
            commands = []
            commandsError = false
            loadingCommands = true
            do {
                let result = try await store.commands()
                if !Task.isCancelled { commands = result }
            } catch {
                if !Task.isCancelled { commandsError = true }
            }
            if !Task.isCancelled { loadingCommands = false }
        }
        .onChange(of: store.draft) { _, _ in commandIndex = 0; commandsDismissed = false }
        .onChange(of: unreadInView, initial: true) { _, unread in
            if unread { connection.markRead(store.sessionID) }
        }
        .onAppear {
            store.activate()
            if focusesPrompt { composerFocused = true }
        }
        .onDisappear {
            dictation.stop()
            store.deactivate()
        }
    }

    private var transcript: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: Metrics.gap) {
                if !store.synced {
                    ProgressView().frame(maxWidth: .infinity).padding(.top, 40)
                }
                ForEach(rows) { row in
                    switch row {
                    case .item(let item):
                        ItemView(item: item, store: store)
                            .id(item.id)
                    case .turnFooter(let turn):
                        TurnFooter(turn: turn)
                    }
                }
                ForEach(store.outbox.sorted(by: { $0.key < $1.key }), id: \.key) { _, out in
                    UserBubble(text: out.text, images: out.images, store: store, pending: true)
                }
                if store.synced, let draft = store.unsentDraft {
                    DraftBubble(draft: draft, store: store, disabled: savingDraft || session?.archived == true,
                                edit: { editDraft(draft) }, send: sendDraft)
                }
                if store.synced, store.transcript.isEmpty, store.outbox.isEmpty, session?.hasDraft != true {
                    Text("No prompts yet. Write the first one below.")
                        .scaledFont(.footnote)
                        .foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity)
                        .padding(.top, 40)
                }
                if session?.status == .running {
                    HStack(spacing: 8) {
                        ProgressView().controlSize(.small)
                        Text("Working…").scaledFont(.footnote).foregroundStyle(.secondary)
                    }
                    .padding(.leading, 4)
                }
                if let tasks = session?.backgroundTasks, !tasks.isEmpty {
                    BackgroundTasksView(tasks: tasks)
                }
                if let err = session?.error, session?.status == .error {
                    Label(err, systemImage: "exclamationmark.octagon")
                        .scaledFont(.footnote)
                        .foregroundStyle(Palette.red)
                }
            }
            .padding(.horizontal, Metrics.margin)
            .padding(.vertical, Metrics.gap)
            .frame(maxWidth: Metrics.readableWidth)
            .frame(maxWidth: .infinity)
            .paletteText()
        }
        .background(Palette.base)
        .scrollPosition($scrollPosition)
        .defaultScrollAnchor(.bottom)
        .scrollDismissesKeyboard(.interactively)
        .safeAreaInset(edge: .bottom) { composer }
        .acceptsImages(attachments, via: connection, pasting: composerFocused)
    }

    // MARK: Terminal

    /// Where the terminal shows: on an iPhone or in a narrow window always
    /// in place of the messages, otherwise where Settings says.
    private var placement: TerminalPlacement {
        #if os(iOS)
        if UIDevice.current.userInterfaceIdiom == .phone || sizeClass == .compact { return .over }
        #endif
        return placementSetting
    }

    /// The terminal is in place of the messages.
    private var covered: Bool { showTerminal && placement == .over }

    private func terminalShown(_ p: TerminalPlacement) -> Binding<Bool> {
        Binding(get: { showTerminal && placement == p }, set: { showTerminal = $0 })
    }

    private func terminalPanel(_ s: Session) -> some View {
        TerminalPanel(connection: connection, sessionID: s.id, folder: s.workspace.path,
                      focused: $terminalFocused, hide: { showTerminal = false })
    }

    /// Return and Esc belong to the terminal while it has the keyboard.
    private var terminalHasKeys: Bool {
        showTerminal && (terminalFocused || placement == .over)
    }

    private var canOpenTerminal: Bool {
        connection.info?.terminal == true && session?.archived == false
    }

    /// ⌘J and ⌘` show and hide the terminal. Hiding it gives the keyboard
    /// back to the prompt.
    @ViewBuilder private var terminalShortcuts: some View {
        if canOpenTerminal {
            let title = showTerminal ? "Hide Terminal" : "Show Terminal"
            Group {
                Button(title, action: toggleTerminal).keyboardShortcut("j")
                Button(title, action: toggleTerminal).keyboardShortcut("`")
            }
            // hidden() would turn the shortcuts off.
            .opacity(0)
            .accessibilityHidden(true)
        }
    }

    private func toggleTerminal() {
        showTerminal.toggle()
        if !showTerminal { composerFocused = true }
    }

    // MARK: Rows

    enum Row: Identifiable {
        case item(Item)
        case turnFooter(Turn)
        var id: String {
            switch self {
            case .item(let i): i.id
            case .turnFooter(let t): "turn-" + t.id
            }
        }
    }

    /// Items in order, with a footer after the last item of each interrupted or failed turn.
    /// A prompt queued during a turn sits among its items, so the last is looked up.
    private var rows: [Row] {
        var out: [Row] = []
        let items = store.transcript
        var last: [String: Int] = [:]
        for (i, item) in items.enumerated() {
            if let tid = item.turnId { last[tid] = i }
        }
        for (i, item) in items.enumerated() {
            out.append(.item(item))
            if let tid = item.turnId, last[tid] == i, let t = store.turns[tid], t.status == .interrupted || t.status == .failed {
                out.append(.turnFooter(t))
            }
        }
        return out
    }

    // MARK: Composer

    #if os(macOS)
    /// A card over the foot of the transcript, around the prompt and its buttons.
    private var composer: some View {
        let card = RoundedRectangle(cornerRadius: Metrics.Corner.composer)
        return VStack(alignment: .leading, spacing: 10) {
            commandSuggestions
            if !attachments.isEmpty {
                AttachmentStrip(attachments: attachments, connection: connection)
            }
            ComposerLayout {
                attachMenu
                promptField
                actionButtons
            }
        }
        .padding(Metrics.margin)
        .background {
            // A click in the card's margins goes to the prompt.
            card.fill(Palette.raised).onTapGesture { composerFocused = true }
        }
        .overlay { card.strokeBorder(composerFocused ? Palette.accent : Palette.surface1) }
        .shadow(color: .black.opacity(0.12), radius: 6, y: 2)
        .padding([.horizontal, .bottom], Metrics.margin)
        .padding(.top, 4)
        .frame(maxWidth: Metrics.readableWidth)
        .frame(maxWidth: .infinity)
        .background(Palette.base)
    }
    #else
    private var composer: some View {
        VStack(alignment: .leading, spacing: 4) {
            commandSuggestions
            if !attachments.isEmpty {
                AttachmentStrip(attachments: attachments, connection: connection)
            }
            HStack(alignment: .bottom, spacing: 8) {
                attachMenu
                promptField
                    .padding(.horizontal, 10)
                    .padding(.vertical, 6)
                    .background(Palette.surface0, in: RoundedRectangle(cornerRadius: Metrics.Corner.field))
                actionButtons
            }
        }
        .padding(.horizontal, Metrics.margin)
        .padding(.vertical, 6)
        .frame(maxWidth: Metrics.readableWidth)
        .frame(maxWidth: .infinity)
        .background(Palette.mantle)
        .onChange(of: composerFocused) { _, focused in
            // The keyboard has its own dictation key.
            if focused { dictation.stop() }
        }
    }
    #endif

    private var wantsCommands: Bool {
        composerFocused && store.isConnected && session?.archived != true && SlashCommand.query(store.draft) != nil
    }

    private var suggestions: [SlashCommand] {
        guard composerFocused, session?.archived != true, !commandsDismissed else { return [] }
        return SlashCommand.suggestions(for: store.draft, from: commands + (harness?.commands ?? []))
    }

    private func completeCommand(_ command: SlashCommand) {
        store.draft = command.prompt + " "
        composerFocused = true
    }

    @ViewBuilder private var commandSuggestions: some View {
        if composerFocused, SlashCommand.query(store.draft) != nil, !commandsDismissed, session?.archived != true {
            let list = suggestions
            if let first = list.first {
                ScrollViewReader { proxy in
                    ScrollView {
                        LazyVStack(alignment: .leading, spacing: 0) {
                            // Rows are known by the command, never by their
                            // place: a lazy stack keeps a row's old command
                            // when the list it shows filters.
                            ForEach(Array(list.enumerated()), id: \.element.id) { index, command in
                                Button { completeCommand(command) } label: {
                                    HStack(alignment: .top, spacing: 8) {
                                        VStack(alignment: .leading, spacing: 2) {
                                            Text(command.prompt).scaledFont(.footnote, design: .monospaced)
                                                .foregroundStyle(Palette.text)
                                            if let description = command.description {
                                                Text(description).scaledFont(.caption2).foregroundStyle(Palette.subtext).lineLimit(2)
                                            }
                                        }
                                        Spacer(minLength: 4)
                                        if let scope = command.scope {
                                            Text(scope.capitalized).scaledFont(.caption2).foregroundStyle(Palette.subtext)
                                        }
                                    }
                                    .frame(maxWidth: .infinity, alignment: .leading)
                                    .frame(minHeight: 44)
                                    .padding(8)
                                    .background(index == commandIndex ? Palette.surface0 : Palette.mantle)
                                    .contentShape(Rectangle())
                                }
                                .buttonStyle(.plain)
                            }
                        }
                    }
                    .frame(maxHeight: 220)
                    .onChange(of: store.draft) { _, _ in proxy.scrollTo(first.id, anchor: .top) }
                    .onChange(of: list) { _, _ in proxy.scrollTo(first.id, anchor: .top) }
                    .onChange(of: commandIndex) { _, index in
                        if list.indices.contains(index) { proxy.scrollTo(list[index].id) }
                    }
                }
                .background(Palette.mantle, in: RoundedRectangle(cornerRadius: Metrics.Corner.field))
                .accessibilityLabel("Command suggestions")
            }
            if loadingCommands || commandsError {
                Text(loadingCommands ? "Loading skills…" : "Skills unavailable. Try typing / again.")
                    .scaledFont(.caption).foregroundStyle(Palette.subtext)
            }
        }
    }

    private var promptField: some View {
        TextField(text: $store.draft, selection: $promptSelection, prompt: Text(placeholder).foregroundStyle(Palette.overlay), axis: .vertical) {
            Text(placeholder)
        }
            .textFieldStyle(.plain)
            .messageFont()
            .paletteText()
            #if os(macOS)
            .lineLimit(1...12)
            #else
            .lineLimit(1...6)
            #endif
            .focused($composerFocused)
            .onKeyPress(.downArrow) {
                guard !suggestions.isEmpty else { return .ignored }
                commandIndex = min(commandIndex + 1, suggestions.count - 1)
                return .handled
            }
            .onKeyPress(.upArrow) {
                guard !suggestions.isEmpty else { return .ignored }
                commandIndex = max(commandIndex - 1, 0)
                return .handled
            }
            .onKeyPress(.escape) {
                guard !suggestions.isEmpty else { return .ignored }
                commandsDismissed = true
                return .handled
            }
            .onKeyPress(keys: [.tab, .return]) { press in
                guard !press.modifiers.contains(.shift), suggestions.indices.contains(commandIndex) else { return .ignored }
                let selected = suggestions[commandIndex]
                if press.key == .return && store.draft == selected.prompt {
                    if mainAction == .send && canSend { mainButtonTapped() }
                } else { completeCommand(selected) }
                return .handled
            }
            .shiftReturnNewline()
            #if os(macOS)
            .onSubmit { if mainAction == .send && canSend { mainButtonTapped() } }
            #endif
    }

    private var attachMenu: some View {
        AttachMenu(attachments: attachments, connection: connection)
            .disabled(session?.archived == true)
    }

    /// Stop, while a turn runs, then save as a draft, and dictate or send.
    @ViewBuilder private var actionButtons: some View {
        if store.isRunning {
            Button {
                Task { await run { try await store.interrupt() } }
            } label: {
                Image(systemName: "stop.circle.fill").font(.system(size: Metrics.roundButton))
            }
            .buttonStyle(.borderless)
            .roundButtonFrame()
            .tint(Palette.red)
            .keyboardShortcut(".")
            .accessibilityLabel("Stop")
        }
        if offersDraft {
            Button(action: saveDraft) {
                // Smaller than the round buttons, on their middle.
                ZStack {
                    Image(systemName: "circle").font(.system(size: Metrics.roundButton)).hidden()
                    Image(systemName: "square.and.arrow.down").font(.system(size: 18))
                }
            }
            .buttonStyle(.borderless)
            .roundButtonFrame()
            .tint(Palette.subtext)
            .keyboardShortcut("s")
            .disabled(!attachments.ready || savingDraft)
            .help("Save as Draft")
            .accessibilityLabel("Save as Draft")
        }
        Button(action: mainButtonTapped) {
            Image(systemName: mainAction.symbol)
                .font(.system(size: Metrics.roundButton))
                .contentTransition(.symbolEffect(.replace))
                .symbolEffect(.variableColor.iterative, isActive: dictation.phase == .listening)
        }
        .buttonStyle(.borderless)
        .roundButtonFrame()
        .tint(mainAction == .stopDictation ? Palette.red : Palette.accent)
        .keyboardShortcut(mainAction == .send && !terminalHasKeys ? KeyboardShortcut(.return) : nil)
        // While dictation finishes, the final pass is still rewriting the draft.
        .disabled(session?.archived == true || (mainAction == .send && !canSend) || dictation.phase == .finishing)
        .accessibilityLabel(mainAction.label)
        .animation(.snappy, value: mainAction)
    }

    private enum MainAction {
        case dictate, stopDictation, send

        var symbol: String {
            switch self {
            case .dictate: "mic.circle.fill"
            case .stopDictation: "waveform.circle.fill"
            case .send: "arrow.up.circle.fill"
            }
        }

        var label: String {
            switch self {
            case .dictate: "Dictate"
            case .stopDictation: "Stop dictation"
            case .send: "Send"
            }
        }
    }

    /// The mic is the main button until the keyboard is up or there is text to
    /// send. On the Mac the field keeps focus, so only the text counts.
    private var mainAction: MainAction {
        if dictation.phase != .idle { return .stopDictation }
        #if os(macOS)
        return nothingToSend ? .dictate : .send
        #else
        return composerFocused || !nothingToSend ? .send : .dictate
        #endif
    }

    private var nothingToSend: Bool {
        store.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && attachments.isEmpty
    }

    /// There is something to send, and its images are on the server.
    private var canSend: Bool { !nothingToSend && attachments.ready }

    /// What the composer holds can wait in the session as its draft, to send
    /// from any device: a first prompt, on a server that keeps drafts.
    private var offersDraft: Bool {
        connection.info?.drafts == true && session?.archived == false && session?.hasDraft == false
            && store.synced && store.transcript.isEmpty && store.outbox.isEmpty && !nothingToSend
    }

    private func mainButtonTapped() {
        switch mainAction {
        case .dictate:
            startDictation()
        case .stopDictation:
            dictation.stop()
        case .send:
            let text = store.draft, sent = attachments.entries
            store.draft = ""
            attachments.clear()
            // Back to the foot of the transcript, which then follows the reply.
            withAnimation { scrollPosition.scrollTo(edge: .bottom) }
            Task {
                do {
                    try await store.send(text, images: sent.compactMap(\.ref))
                } catch {
                    // It didn't go: back into the composer, ahead of anything
                    // written since.
                    store.draft = [text, store.draft].filter { !$0.isEmpty }.joined(separator: "\n")
                    attachments.restore(sent)
                    self.error = error.localizedDescription
                }
            }
        }
    }

    /// Saves what the composer holds as the session's draft, which then shows
    /// in the transcript in its place. It stays in the composer if that fails.
    private func saveDraft() {
        let text = store.draft, saved = attachments.entries
        savingDraft = true
        Task {
            defer { savingDraft = false }
            do {
                try await connection.saveDraft(store.sessionID, text: text, images: saved.compactMap(\.ref))
                // Whatever was written meanwhile stays.
                if store.draft == text { store.draft = "" }
                for entry in saved { attachments.remove(entry.id) }
            } catch {
                self.error = error.localizedDescription
            }
        }
    }

    /// Sends the session's draft as its prompt.
    private func sendDraft() {
        withAnimation { scrollPosition.scrollTo(edge: .bottom) }
        Task { await run { try await store.sendDraft() } }
    }

    /// Takes the draft off the server and back into the composer, ahead of
    /// anything written there since.
    private func editDraft(_ draft: Draft) {
        savingDraft = true
        Task {
            defer { savingDraft = false }
            do {
                try await connection.saveDraft(store.sessionID, text: "")
                store.draft = [draft.text, store.draft].filter { !$0.isEmpty }.joined(separator: "\n")
                attachments.restore(draft.images ?? [], via: connection)
                composerFocused = true
                // On the Mac a field that takes focus selects its text, which
                // the next key would replace: the cursor goes to its end.
                await Task.yield()
                promptSelection = TextSelection(insertionPoint: store.draft.endIndex)
            } catch {
                self.error = error.localizedDescription
            }
        }
    }

    /// Runs a slash command, as a prompt of its own: the draft stays. While
    /// a turn runs, it waits for it, like a follow-up.
    private func send(_ command: SlashCommand) {
        withAnimation { scrollPosition.scrollTo(edge: .bottom) }
        Task { await run { try await store.send(command.prompt) } }
    }

    private var placeholder: String {
        switch dictation.phase {
        case .downloading: "Downloading the speech model…"
        case .starting, .listening: "Listening…"
        case .idle, .finishing: store.isRunning ? "Queue a follow-up…" : "Prompt"
        }
    }

    /// Dictated text is appended to whatever was already typed.
    private func startDictation() {
        let prefix = store.draft
        let separator = prefix.isEmpty || prefix.last?.isWhitespace == true ? "" : " "
        let hints = [project?.name, harness?.name].compactMap { $0 }
        Task {
            await dictation.start(hints: hints) { text in store.draft = prefix + separator + text }
        }
    }

    @ToolbarContentBuilder
    private var toolbar: some ToolbarContent {
        #if os(iOS)
        ToolbarItem(placement: .principal) {
            VStack(spacing: 0) {
                Text(session?.title ?? "").scaledFont(.subheadline, weight: .semibold).lineLimit(1)
                if let s = session {
                    // Where the line is too narrow for the branch, it goes.
                    ViewThatFits(in: .horizontal) {
                        titleDetails(s, branch: true)
                        titleDetails(s, branch: false)
                    }
                    .scaledFont(.caption2)
                    .foregroundStyle(.secondary)
                }
            }
        }
        #else
        // In three columns the title over the transcript has it.
        if !inlineTitle, let s = session, showsWorkspace(s) {
            ToolbarItem(placement: .primaryAction) {
                WorkspaceLabel(workspace: s.workspace)
            }
            .sharedBackgroundVisibility(.hidden)
        }
        #endif
        ToolbarItem(placement: .primaryAction) {
            Menu {
                if let modes = harness?.modes, !modes.isEmpty {
                    Picker("Permissions", selection: Binding(
                        get: { session?.mode ?? "" },
                        set: { m in Task { await run { try await store.setMode(m) } } })) {
                        ForEach(modes) { m in Text(m.name).tag(m.id) }
                    }
                    .pickerStyle(.menu)
                }
                if project?.isGitRepo == true {
                    Button("Changes", systemImage: "plusminus") { showDiff = true }
                }
                if canOpenTerminal {
                    Button(showTerminal ? "Hide Terminal" : "Terminal", systemImage: "apple.terminal") {
                        showTerminal.toggle()
                    }
                }
                if let commands = harness?.commands, !commands.isEmpty {
                    Section("Commands") {
                        ForEach(commands) { c in
                            Button { send(c) } label: {
                                Label(c.prompt, systemImage: c.symbol)
                                if let d = c.description { Text(d) }
                            }
                        }
                    }
                    .disabled(session?.archived == true)
                }
                if let s = session {
                    Section(project?.name ?? "") {
                        if project?.isGitRepo == true {
                            Label(s.workspace.branch ?? "Detached HEAD", systemImage: "arrow.triangle.branch")
                        }
                        if s.workspace.kind == .worktree {
                            Label("Worktree", systemImage: "square.on.square.dashed")
                        }
                        Text(s.workspace.path)
                    }
                }
                if store.isRunning {
                    Button("Force stop agent", systemImage: "xmark.octagon", role: .destructive) {
                        Task { await run { try await store.interrupt(force: true) } }
                    }
                }
            } label: {
                Image(systemName: "ellipsis.circle")
            }
        }
    }

    #if os(iOS)
    /// The line under the title: the project and branch, then the agent.
    private func titleDetails(_ s: Session, branch: Bool) -> some View {
        HStack(spacing: 4) {
            if let place = place(s, branch: branch) {
                Text(place).fontWeight(.medium)
                Text("·")
            }
            HarnessIcon(id: s.harness).imageScale(.small)
            Text(subtitle(s))
            if let f = s.context?.fraction {
                Text("·")
                ContextGauge(fraction: f)
            }
        }
        .lineLimit(1)
    }
    #endif

    /// "weather-api · main", or "weather-api · worktree ra/fix-cache": where
    /// the agent works, which nothing else shows once the sidebar is hidden.
    private func place(_ s: Session, branch: Bool = true) -> String? {
        guard let project else { return nil }
        var parts = [project.name]
        if branch {
            if s.workspace.kind == .worktree {
                parts.append(["worktree", s.workspace.branch].compactMap { $0 }.joined(separator: " "))
            } else if project.isGitRepo, let b = s.workspace.branch {
                parts.append(b)
            }
        }
        return parts.joined(separator: " · ")
    }

    /// "Opus 5.5 · high · Ask": the model (the harness until the agent reports
    /// one), its reasoning effort and the permission mode.
    private func subtitle(_ s: Session) -> String {
        [s.modelName(in: harness) ?? harness?.name ?? s.harness, s.modelInfo?.effort ?? s.effort, modeName]
            .compactMap { $0 }
            .filter { !$0.isEmpty }
            .joined(separator: " · ")
    }

    #if os(macOS)
    /// The title and subtitle over the transcript, where the toolbar's title
    /// is the session list's: in three columns, as in Mail.
    private func titleHeader(_ s: Session) -> some View {
        HStack(spacing: 12) {
            VStack(alignment: .leading, spacing: 2) {
                Text(s.title)
                    .scaledFont(.headline)
                    .foregroundStyle(Palette.text)
                Text(macSubtitle(s))
                    .scaledFont(.caption)
                    .foregroundStyle(Palette.subtext)
            }
            .lineLimit(1)
            .frame(maxWidth: .infinity, alignment: .leading)
            if showsWorkspace(s) {
                WorkspaceLabel(workspace: s.workspace)
            }
        }
        .padding(.horizontal, Metrics.margin)
        .padding(.vertical, 8)
        .background(Palette.base)
        .overlay(alignment: .bottom) {
            Rectangle().fill(Palette.surface0).frame(height: 1)
        }
    }

    /// The branch is worth showing: the project is a git repo, or the session
    /// has a worktree of its own.
    private func showsWorkspace(_ s: Session) -> Bool {
        project?.isGitRepo == true || s.workspace.kind == .worktree
    }

    /// The subtitle under the window title: the project, then the agent, with
    /// the context use at the end. The branch has a place of its own beside
    /// the title (`WorkspaceLabel`).
    private func macSubtitle(_ s: Session) -> String {
        let context = s.context?.fraction.map { "\($0.formatted(.percent.precision(.fractionLength(0)))) context" }
        let agent = [subtitle(s), context].compactMap { $0 }.joined(separator: " · ")
        return [place(s, branch: false), agent].compactMap { $0 }.joined(separator: " — ")
    }
    #endif

    private var modeName: String? {
        guard let m = session?.mode, !m.isEmpty else { return nil }
        return harness?.modes?.first(where: { $0.id == m })?.name ?? m
    }

    private func run(_ f: () async throws -> Void) async {
        do { try await f() } catch { self.error = error.localizedDescription }
    }
}

#if os(macOS)
/// The Mac composer's prompt between its buttons while it fits on one line;
/// longer, it spans the card with the buttons in a row under it. A layout
/// rather than two stacks, so the field keeps its focus when it switches.
///
/// Subviews: the add menu, the prompt, then the buttons on the right.
private struct ComposerLayout: Layout {
    var spacing: CGFloat = 8
    var rowSpacing: CGFloat = 10

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = proposal.replacingUnspecifiedDimensions().width
        return CGSize(width: width, height: arrange(width: width, subviews).height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        for (subview, frame) in zip(subviews, arrange(width: bounds.width, subviews).frames) {
            subview.place(at: CGPoint(x: bounds.minX + frame.minX, y: bounds.minY + frame.minY),
                          proposal: ProposedViewSize(frame.size))
        }
    }

    private func arrange(width: CGFloat, _ subviews: Subviews) -> (frames: [CGRect], height: CGFloat) {
        guard subviews.count >= 2 else { return ([], 0) }
        let field = subviews[1]
        let left = subviews[0].sizeThatFits(.unspecified)
        let right = subviews.dropFirst(2).map { $0.sizeThatFits(.unspecified) }
        let rightWidth = right.reduce(0) { $0 + $1.width + spacing }
        let buttonsHeight = ([left] + right).map(\.height).max() ?? 0

        // The buttons in a row of their own height, from `y`.
        func buttons(centeredIn rowHeight: CGFloat, at y: CGFloat) -> [CGRect] {
            var x = width - rightWidth
            return [CGRect(origin: CGPoint(x: 0, y: y + (rowHeight - left.height) / 2), size: left)]
                + right.map { size in
                    defer { x += size.width + spacing }
                    return CGRect(origin: CGPoint(x: x + spacing, y: y + (rowHeight - size.height) / 2), size: size)
                }
        }

        let between = max(width - left.width - spacing - rightWidth, 0)
        let inRow = field.dimensions(in: ProposedViewSize(width: between, height: nil))
        if abs(inRow[.lastTextBaseline] - inRow[.firstTextBaseline]) < 1 {
            let height = max(buttonsHeight, inRow.height)
            var frames = buttons(centeredIn: height, at: 0)
            frames.insert(CGRect(x: left.width + spacing, y: (height - inRow.height) / 2,
                                 width: between, height: inRow.height), at: 1)
            return (frames, height)
        }
        let fieldHeight = field.sizeThatFits(ProposedViewSize(width: width, height: nil)).height
        var frames = buttons(centeredIn: buttonsHeight, at: fieldHeight + rowSpacing)
        frames.insert(CGRect(x: 0, y: 0, width: width, height: fieldHeight), at: 1)
        return (frames, fieldHeight + rowSpacing + buttonsHeight)
    }
}

/// The branch the agent works on, and that it is a worktree when it is one,
/// across from the session's title: in the toolbar, or in three columns, in
/// the header over the transcript. The path is its help.
private struct WorkspaceLabel: View {
    let workspace: Workspace

    var body: some View {
        HStack(spacing: 10) {
            if workspace.kind == .worktree {
                Label("Worktree", systemImage: "square.on.square.dashed")
            }
            if workspace.branch != nil || workspace.kind == .root {
                Label(workspace.branch ?? "Detached HEAD", systemImage: "arrow.triangle.branch")
                    .truncationMode(.middle)
            }
        }
        .labelStyle(WorkspaceLabelStyle())
        .lineLimit(1)
        .scaledFont(.callout)
        .foregroundStyle(Palette.subtext)
        .frame(maxWidth: 360, alignment: .trailing)
        .help(workspace.path)
        .accessibilityElement(children: .combine)
    }
}

private struct WorkspaceLabelStyle: LabelStyle {
    func makeBody(configuration: Configuration) -> some View {
        HStack(spacing: 4) {
            configuration.icon.foregroundStyle(Palette.overlay)
            configuration.title
        }
    }
}
#endif

/// What the agent left running after its turn, at the foot of the
/// transcript. The agent reports back when each task ends.
struct BackgroundTasksView: View {
    let tasks: [RAKit.BackgroundTask] // not SwiftUI's

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            ProgressView().controlSize(.small)
            VStack(alignment: .leading, spacing: 4) {
                Text("Running in the background")
                ForEach(tasks) { task in
                    Label(task.description, systemImage: symbol(task.kind))
                        .lineLimit(2)
                }
            }
        }
        .scaledFont(.footnote)
        .foregroundStyle(.secondary)
        .padding(.leading, 4)
        .accessibilityElement(children: .combine)
    }

    private func symbol(_ kind: RAKit.BackgroundTask.Kind) -> String {
        switch kind {
        case .command: "terminal"
        case .agent: "person.2"
        case .other: "circle.dotted"
        }
    }
}

/// Marks a turn that did not finish normally.
struct TurnFooter: View {
    let turn: Turn

    var body: some View {
        Group {
            switch turn.status {
            case .failed: Label("Failed", systemImage: "xmark.octagon.fill").foregroundStyle(Palette.red)
            default: Label("Interrupted", systemImage: "stop.fill")
            }
        }
        .scaledFont(.caption2)
        .foregroundStyle(.secondary)
    }
}
