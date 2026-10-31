import RAKit
import SwiftUI

/// A new session that continues this one after one of its turns, with its
/// agent or another, in a worktree of its own or in the project folder. Built
/// like New session, from its rows. The choices aren't remembered as New
/// session's.
struct ForkSessionView: View {
    let connection: ServerConnection
    let store: SessionStore
    let source: Session
    /// The fork, and whether it opened without a prompt.
    var onForked: (Session, _ empty: Bool) -> Void
    @Environment(\.dismiss) private var dismiss

    /// The turn the fork continues after, by its number.
    @State private var turn: Int
    @State private var harnessID = ""
    @State private var model = ""
    @State private var customModel = ""
    @State private var effort = ""
    @State private var mode = ""
    @State private var useWorktree: Bool
    @State private var branch = ""
    @State private var prompt = ""
    @State private var forking = false
    @State private var error: String?

    init(connection: ServerConnection, store: SessionStore, source: Session,
         onForked: @escaping (Session, _ empty: Bool) -> Void) {
        self.connection = connection
        self.store = store
        self.source = source
        self.onForked = onForked
        _turn = State(initialValue: Self.endedTurns(store).first?.n ?? 0)
        _useWorktree = State(initialValue: source.workspace.kind == .worktree)
    }

    /// The turns that ended, which a fork can continue after, the newest first.
    static func endedTurns(_ store: SessionStore) -> [Turn] {
        store.turns.values.filter { $0.status != .running }.sorted { $0.n > $1.n }
    }

    private var project: Project? { connection.projects[source.projectId] }
    private var harness: HarnessInfo? { connection.harness(harnessID) }
    private var canFork: Bool { harness != nil && turn > 0 }
    /// The model id to request; nil for the harness default.
    private var chosenModel: String? { harness?.modelID(model, custom: customModel) }

    var body: some View {
        NavigationStack {
            Form {
                Group {
                    Section {
                        let prompts = turnPrompts
                        Picker("After turn", selection: $turn) {
                            ForEach(Self.endedTurns(store)) { t in
                                Text(label(t, prompt: prompts[t.id])).tag(t.n)
                            }
                        }
                    } header: {
                        Text("From").formHeaderFont()
                    }

                    Section {
                        HarnessChoiceRows(connection: connection, harnessID: $harnessID)
                    } header: {
                        Text("Agent").formHeaderFont()
                    }

                    if let harness {
                        Section {
                            HarnessOptionRows(harness: harness, model: $model, customModel: $customModel,
                                              effort: $effort, mode: $mode)
                        } header: {
                            Text("Options").formHeaderFont()
                        }
                    }

                    if project?.isGitRepo == true {
                        Section {
                            Toggle("Isolated git worktree", isOn: $useWorktree)
                            if useWorktree {
                                TextField("Branch", text: $branch, prompt: Text(branchPrompt))
                                    .plainTextInput()
                            }
                        } header: {
                            Text("Workspace").formHeaderFont()
                        } footer: {
                            Text(useWorktree ? "The fork gets its own branch, with the files as they were after that turn."
                                             : "The fork works in the project folder, as it is now.")
                                .formFooterFont()
                        }
                    }

                    Section {
                        TextField("Prompt", text: $prompt, prompt: Text("What should the agent do next?"), axis: .vertical)
                            .labelsHidden()
                            .lineLimit(4...12)
                            .messageFont()
                            .shiftReturnNewline()
                    } header: {
                        Text("Prompt").formHeaderFont()
                    } footer: {
                        Text("Optional. Without one, the fork opens with the conversation so far, and you write the next prompt in it. The same agent picks up its own conversation; another agent gets it as a transcript with the first prompt.")
                            .formFooterFont()
                    }

                    if let error {
                        Section { Label(error, systemImage: "exclamationmark.triangle").foregroundStyle(Palette.red) }
                    }
                }
                .paletteRows()
            }
            .compactForm()
            #if os(macOS)
            .frame(minWidth: 480, minHeight: 560)
            #endif
            .navigationTitle("Fork session")
            .inlineTitle()
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    if forking {
                        ProgressView()
                    } else {
                        Button("Fork") { Task { await fork() } }
                            .disabled(!canFork)
                    }
                }
            }
            .task {
                if connection.harnesses == nil { await connection.refreshHarnesses() }
                if harnessID.isEmpty {
                    harnessID = connection.harness(source.harness)?.usable == true ? source.harness
                        : (connection.harnesses?.first(where: \.usable)?.id ?? "")
                }
            }
            .onChange(of: harnessID) { _, _ in restoreChoices() }
            .onChange(of: chosenModel) { _, m in
                // Models support different effort levels.
                if let harness, !harness.efforts(forModel: m).contains(where: { $0.id == effort }) {
                    effort = ""
                }
            }
        }
    }

    /// Each turn's prompt, by the turn's id: its first user message.
    private var turnPrompts: [String: String] {
        var prompts: [String: String] = [:]
        for item in store.transcript where item.kind == .userMessage {
            if let id = item.turnId, prompts[id] == nil { prompts[id] = item.text ?? "" }
        }
        return prompts
    }

    /// "Turn 3 · Fix the failing tests": the first line of its prompt, shortened.
    private func label(_ t: Turn, prompt: String?) -> String {
        let line = prompt?.split(whereSeparator: \.isNewline).first?.trimmingCharacters(in: .whitespaces) ?? ""
        guard !line.isEmpty else { return "Turn \(t.n)" }
        return "Turn \(t.n) · " + (line.count > 48 ? line.prefix(47).trimmingCharacters(in: .whitespaces) + "…" : line)
    }

    /// On the Mac the field's label, Branch, is shown beside it.
    private var branchPrompt: String {
        #if os(macOS)
        "auto"
        #else
        "Branch (auto)"
        #endif
    }

    /// With the source's agent, its model, effort and mode, if the agent
    /// still offers them; with another, that agent's defaults.
    private func restoreChoices() {
        model = ""
        customModel = ""
        effort = ""
        mode = harness?.defaultMode ?? harness?.modes?.first?.id ?? ""
        guard let harness, harness.id == source.harness else { return }
        if let m = source.model, let choice = harness.modelChoice(m) {
            (model, customModel) = choice
        }
        if let e = source.effort, harness.efforts(forModel: chosenModel).contains(where: { $0.id == e }) {
            effort = e
        }
        if let m = source.mode, harness.modes?.contains(where: { $0.id == m }) == true {
            mode = m
        }
    }

    private func fork() async {
        guard let harness else { return }
        forking = true
        error = nil
        defer { forking = false }
        var workspace: ServerConnection.NewSession.WorkspaceParams?
        let named = useWorktree ? self.branch.trimmingCharacters(in: .whitespaces) : ""
        if project?.isGitRepo == true, useWorktree != (source.workspace.kind == .worktree) || !named.isEmpty {
            workspace = .init(kind: useWorktree ? .worktree : .root, branch: named.isEmpty ? nil : named)
        }
        do {
            let s = try await connection.fork(.init(
                sessionId: source.id, turn: turn,
                harness: harness.id == source.harness ? nil : harness.id,
                // As the pickers show them, which start at the source's.
                model: chosenModel,
                effort: effort.isEmpty ? nil : effort,
                mode: mode.isEmpty ? nil : mode,
                workspace: workspace, prompt: prompt))
            dismiss()
            // As rad decides whether to start a turn.
            onForked(s, prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        } catch {
            self.error = error.localizedDescription
        }
    }
}
