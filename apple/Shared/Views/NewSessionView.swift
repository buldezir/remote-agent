import RAKit
import SwiftUI

struct NewSessionView: View {
    let connection: ServerConnection
    /// The new session, and whether it started empty, without a prompt.
    var onCreated: (Session, _ empty: Bool) -> Void
    @Environment(\.dismiss) private var dismiss

    @AppStorage("lastHarness") private var lastHarness = "claude"
    @AppStorage("lastProject") private var lastProject = ""
    @State private var projectID = ""
    @State private var harnessID = ""
    @State private var model = ""
    @State private var customModel = ""
    @State private var effort = ""
    @State private var mode = ""
    @State private var useWorktree = false
    @State private var branch = ""
    @State private var baseRef = ""
    @State private var branches: [String] = []
    @State private var prompt = ""
    @State private var creating = false
    @State private var error: String?
    /// Why the chosen project wasn't forgotten, beside its Forget button.
    @State private var forgetError: String?
    @State private var browsing = false
    /// Showing the agent's own sessions in the project, to pick one up.
    @State private var choosingImport = false
    @State private var attachments = Attachments()
    @FocusState private var promptFocused: Bool

    /// Starts in `projectID` when given, else in the project used last.
    init(connection: ServerConnection, projectID: String = "", onCreated: @escaping (Session, _ empty: Bool) -> Void) {
        self.connection = connection
        self.onCreated = onCreated
        _projectID = State(initialValue: projectID)
    }

    private var project: Project? { connection.projects[projectID] }
    private var harness: HarnessInfo? { connection.harness(harnessID) }
    private var canStart: Bool { project != nil && harness != nil && attachments.ready }
    private var hasPrompt: Bool { !prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !attachments.isEmpty }
    /// The server keeps drafts; an older one would start the session.
    private var offersDrafts: Bool { connection.info?.drafts == true }

    var body: some View {
        NavigationStack {
            Form {
                Group {
                    Section {
                        Picker("Project", selection: $projectID) {
                            Text("Choose…").tag("")
                            ForEach(connection.sortedProjects) { p in
                                Text(p.name).tag(p.id)
                            }
                        }
                        Button {
                            browsing = true
                        } label: {
                            Label("Add a folder…", systemImage: "folder.badge.plus")
                        }
                        if let project {
                            HStack {
                                Text(project.path).scaledFont(.caption, design: .monospaced).foregroundStyle(.secondary)
                                Spacer()
                                Button("Forget") { Task { await forget(project) } }
                                    .buttonStyle(.borderless)
                                    .scaledFont(.callout)
                                    .help("Take \(project.name) off the list of projects. The folder stays.")
                                    .accessibilityLabel("Forget \(project.name)")
                            }
                            if let forgetError {
                                Label(forgetError, systemImage: "exclamationmark.triangle")
                                    .scaledFont(.caption)
                                    .foregroundStyle(Palette.red)
                            }
                        }
                    } header: {
                        Text("Project").formHeaderFont()
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
                            if project != nil, harness.caps.import == true {
                                Button {
                                    choosingImport = true
                                } label: {
                                    Label("Continue a terminal session…", systemImage: "apple.terminal")
                                }
                                .disabled(creating)
                            }
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
                                Picker("Based on", selection: $baseRef) {
                                    Text("Current HEAD").tag("")
                                    ForEach(branches, id: \.self) { Text($0).tag($0) }
                                }
                            }
                        } header: {
                            Text("Workspace").formHeaderFont()
                        } footer: {
                            Text(useWorktree ? "The agent works on its own branch in a separate checkout. You can revert its turns."
                                             : "The agent works directly in the project folder.")
                                .formFooterFont()
                        }
                    }

                    Section {
                        TextField("Prompt", text: $prompt, prompt: Text("What should the agent do?"), axis: .vertical)
                            .labelsHidden()
                            .lineLimit(4...12)
                            .messageFont()
                            .focused($promptFocused)
                            .shiftReturnNewline()
                        if !attachments.isEmpty {
                            AttachmentStrip(attachments: attachments, connection: connection)
                        }
                        AttachMenu(attachments: attachments, connection: connection, title: "Add Images")
                        if offersDrafts {
                            Button {
                                Task { await create(draft: true) }
                            } label: {
                                Label("Save as Draft", systemImage: "square.and.arrow.down")
                            }
                            .disabled(!canStart || !hasPrompt || creating)
                        }
                    } header: {
                        Text("Prompt").formHeaderFont()
                    } footer: {
                        Text(offersDrafts
                             ? "Optional. Without one, the session starts empty and you write the first prompt in it. Save as Draft keeps the prompt in the session, unsent, to send later from any device."
                             : "Optional. Without one, the session starts empty and you write the first prompt in it.")
                            .formFooterFont()
                    }

                    if let error {
                        Section { Label(error, systemImage: "exclamationmark.triangle").foregroundStyle(Palette.red) }
                    }
                }
                .paletteRows()
            }
            .compactForm()
            .acceptsImages(attachments, via: connection, pasting: promptFocused)
            .onChange(of: attachments.error) { _, e in
                if let e { error = e; attachments.error = nil }
            }
            #if os(macOS)
            .frame(minWidth: 480, minHeight: 600)
            #endif
            .navigationTitle("New session")
            .inlineTitle()
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    if creating {
                        ProgressView()
                    } else {
                        Button("Start") { Task { await create() } }
                            .disabled(!canStart)
                    }
                }
            }
            .sheet(isPresented: $browsing) {
                NavigationStack {
                    DirectoryBrowserView(connection: connection, path: nil) { picked in
                        browsing = false
                        Task {
                            do { projectID = try await connection.addProject(path: picked).id }
                            catch { self.error = error.localizedDescription }
                        }
                    }
                    .toolbar {
                        ToolbarItem(placement: .cancellationAction) { Button("Cancel") { browsing = false } }
                    }
                }
                .appStyle()
                #if os(macOS)
                .frame(minWidth: 420, minHeight: 460)
                #endif
            }
            .sheet(isPresented: $choosingImport) {
                if let project, let harness {
                    ImportSessionView(connection: connection, project: project, harness: harness,
                                      model: chosenModel, effort: effort.isEmpty ? nil : effort,
                                      mode: mode.isEmpty ? nil : mode) { s in
                        choosingImport = false
                        imported(s, project: project, harness: harness)
                    }
                    .appStyle()
                }
            }
            .task {
                if connection.harnesses == nil { await connection.refreshHarnesses() }
                if projectID.isEmpty, connection.projects[lastProject] != nil { projectID = lastProject }
                if harnessID.isEmpty {
                    harnessID = connection.harness(lastHarness)?.usable == true ? lastHarness
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
            // Initially too, for a project given at the start.
            .onChange(of: projectID, initial: true) { _, id in
                forgetError = nil
                useWorktree = false
                branches = []
                guard project?.isGitRepo == true else { return }
                Task { branches = (try? await connection.branches(projectID: id).branches) ?? [] }
            }
        }
    }

    /// Takes the project off the server's list, which it refuses while the
    /// project has sessions that aren't archived.
    private func forget(_ project: Project) async {
        forgetError = nil
        do {
            try await connection.removeProject(project.id)
            if projectID == project.id { projectID = "" }
        } catch {
            forgetError = error.localizedDescription
        }
    }

    /// On the Mac the field's label, Branch, is shown beside it.
    private var branchPrompt: String {
        #if os(macOS)
        "auto"
        #else
        "Branch (auto)"
        #endif
    }

    /// The model id to request; nil for the harness default.
    private var chosenModel: String? { harness?.modelID(model, custom: customModel) }

    /// Starts from the model, effort and permission mode last used with this
    /// agent, if it still offers them.
    private func restoreChoices() {
        model = ""
        customModel = ""
        effort = ""
        mode = harness?.defaultMode ?? harness?.modes?.first?.id ?? ""
        guard let harness else { return }
        let defaults = UserDefaults.standard
        if let m = defaults.string(forKey: "lastModel." + harness.id), let choice = harness.modelChoice(m) {
            (model, customModel) = choice
        }
        if let e = defaults.string(forKey: "lastEffort." + harness.id),
           harness.efforts(forModel: chosenModel).contains(where: { $0.id == e }) {
            effort = e
        }
        if let m = defaults.string(forKey: "lastMode." + harness.id),
           harness.modes?.contains(where: { $0.id == m }) == true {
            mode = m
        }
    }

    /// Starts the session, or with `draft`, keeps the prompt in it unsent.
    private func create(draft: Bool = false) async {
        guard let project, let harness else { return }
        creating = true
        error = nil
        defer { creating = false }
        let ws = ServerConnection.NewSession.WorkspaceParams(
            kind: useWorktree ? .worktree : .root,
            branch: useWorktree && !branch.isEmpty ? branch : nil,
            baseRef: useWorktree && !baseRef.isEmpty ? baseRef : nil)
        do {
            let s = try await connection.createSession(.init(
                projectId: project.id, harness: harness.id,
                model: chosenModel, effort: effort.isEmpty ? nil : effort,
                mode: mode.isEmpty ? nil : mode, workspace: ws, prompt: prompt, images: attachments.refs, draft: draft))
            rememberChoices(project: project, harness: harness)
            dismiss()
            // As rad decides whether to start a turn. A draft isn't empty:
            // the prompt is there, waiting.
            onCreated(s, !draft && prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && attachments.refs.isEmpty)
        } catch {
            self.error = error.localizedDescription
        }
    }

    /// A session of the agent's own, picked up: it opens as a new one does,
    /// waiting for the prompt that goes on with it.
    private func imported(_ s: Session, project: Project, harness: HarnessInfo) {
        rememberChoices(project: project, harness: harness)
        dismiss()
        onCreated(s, true)
    }

    /// The next session starts in this project, with this agent and its options.
    private func rememberChoices(project: Project, harness: HarnessInfo) {
        lastHarness = harness.id
        lastProject = project.id
        UserDefaults.standard.set(chosenModel ?? "", forKey: "lastModel." + harness.id)
        UserDefaults.standard.set(effort, forKey: "lastEffort." + harness.id)
        UserDefaults.standard.set(mode, forKey: "lastMode." + harness.id)
    }
}

/// The agents on the server, as a form's rows, the chosen one ticked. The
/// ones that aren't installed or signed in can't be chosen.
struct HarnessChoiceRows: View {
    let connection: ServerConnection
    @Binding var harnessID: String

    var body: some View {
        if let harnesses = connection.harnesses {
            ForEach(harnesses) { h in
                Button {
                    harnessID = h.id
                } label: {
                    HarnessChoiceRow(harness: h, selected: h.id == harnessID)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .disabled(!h.usable)
                .accessibilityAddTraits(h.id == harnessID ? .isSelected : [])
            }
            if harnesses.isEmpty {
                Text("No agents are installed on the server. Install Claude Code, Codex, Pi or an ACP agent there.")
                    .foregroundStyle(.secondary)
            }
        } else {
            HStack { ProgressView(); Text("Checking installed agents…").foregroundStyle(.secondary) }
        }
    }
}

/// An agent's model, effort and permissions pickers, as a form's rows. The
/// model is `model`, a model's id, "" for the agent's default or "__custom"
/// for `customModel`, an id of the user's own (`HarnessInfo.modelID`).
struct HarnessOptionRows: View {
    let harness: HarnessInfo
    @Binding var model: String
    @Binding var customModel: String
    @Binding var effort: String
    @Binding var mode: String

    var body: some View {
        if let models = harness.models, !models.isEmpty {
            Picker("Model", selection: $model) {
                Text("Default").tag("")
                ForEach(models) { m in Text(m.name).tag(m.id) }
                if harness.caps.freeModel { Text("Custom…").tag("__custom") }
            }
        }
        if model == "__custom" || (harness.caps.freeModel && (harness.models ?? []).isEmpty) {
            TextField("Model id", text: $customModel)
                .plainTextInput()
        }
        let efforts = harness.efforts(forModel: harness.modelID(model, custom: customModel))
        if !efforts.isEmpty {
            Picker("Effort", selection: $effort) {
                Text("Default").tag("")
                ForEach(efforts) { e in Text(e.name).tag(e.id) }
            }
            if let d = efforts.first(where: { $0.id == effort })?.description, !d.isEmpty {
                Text(d).scaledFont(.caption).foregroundStyle(.secondary)
            }
        }
        if let modes = harness.modes, !modes.isEmpty {
            Picker("Permissions", selection: $mode) {
                ForEach(modes) { m in Text(m.name).tag(m.id) }
            }
            if let d = modes.first(where: { $0.id == mode })?.description {
                Text(d).scaledFont(.caption).foregroundStyle(.secondary)
            }
        }
    }
}

extension HarnessInfo {
    /// The model id that `HarnessOptionRows` shows chosen; nil for the agent's default.
    func modelID(_ model: String, custom: String) -> String? {
        let m = model == "__custom" || (models ?? []).isEmpty ? custom : model
        return m.isEmpty ? nil : m
    }

    /// What `HarnessOptionRows` shows for a model id, if the agent still
    /// offers it: one of its models, or one of the user's own.
    func modelChoice(_ id: String) -> (model: String, custom: String)? {
        guard !id.isEmpty else { return nil }
        let models = models ?? []
        if models.contains(where: { $0.id == id }) { return (id, "") }
        guard caps.freeModel else { return nil }
        return (models.isEmpty ? "" : "__custom", id)
    }
}

struct HarnessChoiceRow: View {
    let harness: HarnessInfo
    let selected: Bool

    var body: some View {
        HStack(spacing: 12) {
            HarnessIcon(id: harness.id)
                .frame(width: 28)
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 6) {
                    Text(harness.name).scaledFont(.body)
                    if let v = harness.version {
                        Text(v).scaledFont(.caption2, design: .monospaced).foregroundStyle(.tertiary).lineLimit(1)
                    }
                }
                if let hint = harness.hint, !harness.usable {
                    Text(hint).scaledFont(.caption).foregroundStyle(Palette.yellow)
                }
            }
            Spacer()
            if selected {
                Image(systemName: "checkmark").foregroundStyle(.tint).fontWeight(.semibold)
            }
        }
        .paletteText()
        .opacity(harness.usable ? 1 : 0.5)
    }
}

struct DirectoryBrowserView: View {
    let connection: ServerConnection
    let path: String?
    var onPick: (String) -> Void
    @State private var listing: FSListing?
    @State private var error: String?

    var body: some View {
        List {
            Group {
                if let error {
                    Label(error, systemImage: "exclamationmark.triangle").scaledFont(.body).foregroundStyle(Palette.red)
                }
                if let listing {
                    ForEach(listing.entries) { e in
                        NavigationLink {
                            DirectoryBrowserView(connection: connection, path: e.path, onPick: onPick)
                        } label: {
                            Label {
                                HStack {
                                    Text(path == nil ? e.path : e.name)
                                    if e.isGitRepo {
                                        Text("git").scaledFont(.caption2, weight: .semibold)
                                            .padding(.horizontal, 5).padding(.vertical, 1)
                                            .background(.tint.opacity(0.15), in: Capsule())
                                    }
                                }
                            } icon: {
                                Image(systemName: e.isGitRepo ? "folder.fill.badge.gearshape" : "folder")
                            }
                            .scaledFont(.body)
                        }
                    }
                    if listing.entries.isEmpty {
                        Text("No subfolders").scaledFont(.body).foregroundStyle(.secondary)
                    }
                } else if error == nil {
                    ProgressView()
                }
            }
            .paletteRows()
        }
        .compactForm()
        .navigationTitle(path.map { ($0 as NSString).lastPathComponent } ?? "Folders")
        .inlineTitle()
        .toolbar {
            if let path {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Use folder") { onPick(path) }
                }
            }
        }
        .task {
            do { listing = try await connection.listDirectory(path) }
            catch { self.error = error.localizedDescription }
        }
    }
}
