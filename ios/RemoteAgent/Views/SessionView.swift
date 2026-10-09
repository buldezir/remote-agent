import RAKit
import SwiftUI

struct SessionView: View {
    let connection: ServerConnection
    @Bindable var store: SessionStore
    @State private var draft = ""
    @State private var sending = false
    @State private var error: String?
    @State private var showDiff = false
    @State private var diffTurn: Turn?
    @FocusState private var composerFocused: Bool

    private var session: Session? { store.session ?? connection.sessions[store.sessionID] }
    private var harness: HarnessInfo? { session.flatMap { connection.harness($0.harness) } }
    private var project: Project? { session.flatMap { connection.projects[$0.projectId] } }

    var body: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 14) {
                if !store.synced {
                    ProgressView().frame(maxWidth: .infinity).padding(.top, 40)
                }
                ForEach(rows) { row in
                    switch row {
                    case .item(let item):
                        ItemView(item: item, store: store)
                            .id(item.id)
                    case .turnFooter(let turn):
                        TurnFooter(turn: turn, canDiff: project?.isGitRepo == true) {
                            diffTurn = turn
                            showDiff = true
                        }
                    }
                }
                ForEach(store.outbox.sorted(by: { $0.key < $1.key }), id: \.key) { _, text in
                    UserBubble(text: text, pending: true)
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
            .padding(.horizontal)
            .padding(.vertical, 12)
        }
        .defaultScrollAnchor(.bottom)
        .scrollDismissesKeyboard(.interactively)
        .safeAreaInset(edge: .bottom) { composer }
        .navigationTitle(session?.title ?? "Session")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar { toolbar }
        .sheet(isPresented: $showDiff) {
            NavigationStack {
                DiffView(store: store, initialTurn: diffTurn, canRevert: session?.workspace.kind == .worktree)
            }
        }
        .alert("Error", isPresented: .init(get: { error != nil }, set: { if !$0 { error = nil } })) {
            Button("OK") { error = nil }
        } message: {
            Text(error ?? "")
        }
        .onAppear { store.activate() }
        .onDisappear { store.deactivate() }
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

    /// Items in order, with a footer after the last item of each finished turn.
    private var rows: [Row] {
        var out: [Row] = []
        let items = store.transcript
        for (i, item) in items.enumerated() {
            out.append(.item(item))
            let nextTurn = i + 1 < items.count ? items[i + 1].turnId : nil
            if let tid = item.turnId, nextTurn != tid, let t = store.turns[tid], t.status != .running {
                out.append(.turnFooter(t))
            }
        }
        return out
    }

    // MARK: Composer

    private var composer: some View {
        HStack(alignment: .bottom, spacing: 8) {
            TextField(store.isRunning ? "Queue a follow-up…" : "Message", text: $draft, axis: .vertical)
                .lineLimit(1...6)
                .focused($composerFocused)
                .padding(.horizontal, 12)
                .padding(.vertical, 8)
                .background(.background.secondary, in: RoundedRectangle(cornerRadius: 18))
            if store.isRunning {
                Button {
                    Task { await run { try await store.interrupt() } }
                } label: {
                    Image(systemName: "stop.circle.fill").font(.system(size: 30))
                }
                .tint(.red)
                .accessibilityLabel("Stop")
            }
            Button {
                let text = draft
                draft = ""
                Task { await run { try await store.send(text) } }
            } label: {
                Image(systemName: "arrow.up.circle.fill").font(.system(size: 30))
            }
            .disabled(draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || session?.archived == true)
            .accessibilityLabel("Send")
        }
        .padding(.horizontal)
        .padding(.vertical, 8)
        .background(.bar)
    }

    @ToolbarContentBuilder
    private var toolbar: some ToolbarContent {
        ToolbarItem(placement: .principal) {
            VStack(spacing: 0) {
                Text(session?.title ?? "").font(.subheadline.weight(.semibold)).lineLimit(1)
                HStack(spacing: 4) {
                    if let s = session { HarnessBadge(id: s.harness, name: harness?.name) }
                    if let m = modeName { Text("· \(m)") }
                }
                .font(.caption2)
                .foregroundStyle(.secondary)
            }
        }
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
                    Button("Changes", systemImage: "plusminus") {
                        diffTurn = nil
                        showDiff = true
                    }
                }
                if let s = session {
                    Section(s.workspace.kind == .worktree ? "Worktree: \(s.workspace.branch ?? "")" : (project?.name ?? "")) {
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

    private var modeName: String? {
        guard let m = session?.mode, !m.isEmpty else { return nil }
        return harness?.modes?.first(where: { $0.id == m })?.name ?? m
    }

    private func run(_ f: () async throws -> Void) async {
        do { try await f() } catch { self.error = error.localizedDescription }
    }
}

struct TurnFooter: View {
    let turn: Turn
    let canDiff: Bool
    var onDiff: () -> Void

    var body: some View {
        HStack(spacing: 6) {
            switch turn.status {
            case .interrupted: Label("Interrupted", systemImage: "stop.fill")
            case .failed: Label("Failed", systemImage: "xmark.octagon.fill").foregroundStyle(.red)
            default: EmptyView()
            }
            if let u = turn.usage, !u.summary.isEmpty { Text(u.summary) }
            Spacer()
            if canDiff, turn.checkpointBefore != nil {
                Button("Changes", systemImage: "plusminus", action: onDiff)
                    .labelStyle(.titleAndIcon)
                    .buttonStyle(.borderless)
            }
        }
        .font(.caption2)
        .foregroundStyle(.secondary)
        .padding(.vertical, 2)
        .overlay(alignment: .top) { Divider().offset(y: -8) }
    }
}
