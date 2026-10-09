import RAKit
import SwiftUI

struct SessionView: View {
    let connection: ServerConnection
    @Bindable var store: SessionStore
    @State private var draft = ""
    @State private var sending = false
    @State private var error: String?
    @State private var showDiff = false
    @State private var dictation = Dictation()
    @State private var attachments = Attachments()
    @FocusState private var composerFocused: Bool

    private var session: Session? { store.session ?? connection.sessions[store.sessionID] }
    private var harness: HarnessInfo? { session.flatMap { connection.harness($0.harness) } }
    private var project: Project? { session.flatMap { connection.projects[$0.projectId] } }

    var body: some View {
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
                if store.synced, store.transcript.isEmpty, store.outbox.isEmpty {
                    Text("No prompts yet. Write the first one below.")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity)
                        .padding(.top, 40)
                }
                if session?.status == .running {
                    HStack(spacing: 8) {
                        ProgressView().controlSize(.small)
                        Text("Working…").font(.footnote).foregroundStyle(.secondary)
                    }
                    .padding(.leading, 4)
                }
                if let err = session?.error, session?.status == .error {
                    Label(err, systemImage: "exclamationmark.octagon")
                        .font(.footnote)
                        .foregroundStyle(.red)
                }
            }
            .padding(.horizontal, Metrics.margin)
            .padding(.vertical, Metrics.gap)
            .frame(maxWidth: Metrics.readableWidth)
            .frame(maxWidth: .infinity)
        }
        .defaultScrollAnchor(.bottom)
        .scrollDismissesKeyboard(.interactively)
        .safeAreaInset(edge: .bottom) { composer }
        .acceptsImages(attachments, via: connection, pasting: composerFocused)
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
        .onAppear { store.activate() }
        .onDisappear {
            dictation.stop()
            store.deactivate()
        }
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
    private var rows: [Row] {
        var out: [Row] = []
        let items = store.transcript
        for (i, item) in items.enumerated() {
            out.append(.item(item))
            let nextTurn = i + 1 < items.count ? items[i + 1].turnId : nil
            if let tid = item.turnId, nextTurn != tid, let t = store.turns[tid], t.status == .interrupted || t.status == .failed {
                out.append(.turnFooter(t))
            }
        }
        return out
    }

    // MARK: Composer

    private var composer: some View {
        VStack(alignment: .leading, spacing: 4) {
            if !attachments.isEmpty {
                AttachmentStrip(attachments: attachments, connection: connection)
            }
            composerRow
        }
        .padding(.horizontal, Metrics.margin)
        .padding(.vertical, 6)
        .frame(maxWidth: Metrics.readableWidth)
        .frame(maxWidth: .infinity)
        .background(.bar)
        #if os(iOS)
        .onChange(of: composerFocused) { _, focused in
            // The keyboard has its own dictation key.
            if focused { dictation.stop() }
        }
        #endif
    }

    private var composerRow: some View {
        HStack(alignment: .bottom, spacing: 8) {
            AttachMenu(attachments: attachments, connection: connection)
                .disabled(session?.archived == true)
            TextField(placeholder, text: $draft, axis: .vertical)
                .textFieldStyle(.plain)
                .lineLimit(1...6)
                .focused($composerFocused)
                .shiftReturnNewline()
                #if os(macOS)
                .onSubmit { if mainAction == .send && canSend { mainButtonTapped() } }
                #endif
                .padding(.horizontal, 10)
                .padding(.vertical, 6)
                .background(.background.secondary, in: RoundedRectangle(cornerRadius: Metrics.Corner.field))
            if store.isRunning {
                Button {
                    Task { await run { try await store.interrupt() } }
                } label: {
                    Image(systemName: "stop.circle.fill").font(.system(size: 26))
                }
                .buttonStyle(.borderless)
                .tint(.red)
                .keyboardShortcut(".")
                .accessibilityLabel("Stop")
            }
            Button(action: mainButtonTapped) {
                Image(systemName: mainAction.symbol)
                    .font(.system(size: 26))
                    .contentTransition(.symbolEffect(.replace))
                    .symbolEffect(.variableColor.iterative, isActive: dictation.phase == .listening)
            }
            .buttonStyle(.borderless)
            .tint(mainAction == .stopDictation ? .red : .accentColor)
            .keyboardShortcut(mainAction == .send ? KeyboardShortcut(.return) : nil)
            // While dictation finishes, the final pass is still rewriting the draft.
            .disabled(session?.archived == true || (mainAction == .send && !canSend) || dictation.phase == .finishing)
            .accessibilityLabel(mainAction.label)
            .animation(.snappy, value: mainAction)
        }
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
        draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && attachments.isEmpty
    }

    /// There is something to send, and its images are on the server.
    private var canSend: Bool { !nothingToSend && attachments.ready }

    private func mainButtonTapped() {
        switch mainAction {
        case .dictate:
            startDictation()
        case .stopDictation:
            dictation.stop()
        case .send:
            let text = draft, images = attachments.refs
            draft = ""
            attachments.clear()
            Task { await run { try await store.send(text, images: images) } }
        }
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
        let prefix = draft
        let separator = prefix.isEmpty || prefix.last?.isWhitespace == true ? "" : " "
        let hints = [project?.name, harness?.name].compactMap { $0 }
        Task {
            await dictation.start(hints: hints) { text in draft = prefix + separator + text }
        }
    }

    @ToolbarContentBuilder
    private var toolbar: some ToolbarContent {
        #if os(iOS)
        ToolbarItem(placement: .principal) {
            VStack(spacing: 0) {
                Text(session?.title ?? "").font(.subheadline.weight(.semibold)).lineLimit(1)
                HStack(spacing: 4) {
                    if let s = session {
                        HarnessIcon(id: s.harness).imageScale(.small)
                        Text(subtitle(s)).lineLimit(1)
                    }
                    if let f = session?.context?.fraction {
                        Text("·")
                        ContextGauge(fraction: f)
                    }
                }
                .font(.caption2)
                .foregroundStyle(.secondary)
            }
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

    /// "Opus 5.5 · high · Ask": the model (the harness until the agent reports
    /// one), its reasoning effort and the permission mode.
    private func subtitle(_ s: Session) -> String {
        [s.modelName(in: harness) ?? harness?.name ?? s.harness, s.modelInfo?.effort ?? s.effort, modeName]
            .compactMap { $0 }
            .filter { !$0.isEmpty }
            .joined(separator: " · ")
    }

    #if os(macOS)
    /// The subtitle under the window title, with the context use at the end.
    private func macSubtitle(_ s: Session) -> String {
        let context = s.context?.fraction.map { "\($0.formatted(.percent.precision(.fractionLength(0)))) context" }
        return [subtitle(s), context].compactMap { $0 }.joined(separator: " · ")
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

/// Marks a turn that did not finish normally.
struct TurnFooter: View {
    let turn: Turn

    var body: some View {
        Group {
            switch turn.status {
            case .failed: Label("Failed", systemImage: "xmark.octagon.fill").foregroundStyle(.red)
            default: Label("Interrupted", systemImage: "stop.fill")
            }
        }
        .font(.caption2)
        .foregroundStyle(.secondary)
    }
}
