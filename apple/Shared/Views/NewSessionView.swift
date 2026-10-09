import RAKit
import SwiftUI

struct NewSessionView: View {
    let connection: ServerConnection
    var onCreated: (Session) -> Void
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
    @State private var browsing = false
    @FocusState private var promptFocused: Bool

    private var project: Project? { connection.projects[projectID] }
    private var harness: HarnessInfo? { connection.harness(harnessID) }

    var body: some View {
        NavigationStack {
            Form {
                Section("Project") {
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
                        Text(project.path).font(.caption.monospaced()).foregroundStyle(.secondary)
                    }
                }

                Section {
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
                } header: {
                    Text("Agent")
                }

                if let harness {
                    Section("Options") {
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
                        let efforts = harness.efforts(forModel: chosenModel)
                        if !efforts.isEmpty {
                            Picker("Effort", selection: $effort) {
                                Text("Default").tag("")
                                ForEach(efforts) { e in Text(e.name).tag(e.id) }
                            }
                            if let d = efforts.first(where: { $0.id == effort })?.description, !d.isEmpty {
                                Text(d).font(.caption).foregroundStyle(.secondary)
                            }
                        }
                        if let modes = harness.modes, !modes.isEmpty {
                            Picker("Permissions", selection: $mode) {
                                ForEach(modes) { m in Text(m.name).tag(m.id) }
                            }
                            if let d = modes.first(where: { $0.id == mode })?.description {
                                Text(d).font(.caption).foregroundStyle(.secondary)
                            }
                        }
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
                        Text("Workspace")
                    } footer: {
                        Text(useWorktree ? "The agent works on its own branch in a separate checkout. You can revert its turns."
                                         : "The agent works directly in the project folder.")
                    }
                }

                Section {
                    TextField("Prompt", text: $prompt, prompt: Text("What should the agent do?"), axis: .vertical)
                        .labelsHidden()
                        .lineLimit(4...12)
                        .focused($promptFocused)
                } header: {
                    Text("Prompt")
                } footer: {
                    Text("Optional. Without one, the session starts empty and you write the first prompt in it.")
                }

                if let error {
                    Section { Label(error, systemImage: "exclamationmark.triangle").foregroundStyle(.red) }
                }
            }
            .compactForm()
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
                            .disabled(project == nil || harness == nil)
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
                #if os(macOS)
                .frame(minWidth: 420, minHeight: 460)
                #endif
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
            .onChange(of: projectID) { _, id in
                useWorktree = false
                branches = []
                guard project?.isGitRepo == true else { return }
                Task { branches = (try? await connection.branches(projectID: id).branches) ?? [] }
            }
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
    private var chosenModel: String? {
        let m = model == "__custom" || (harness?.models ?? []).isEmpty ? customModel : model
        return m.isEmpty ? nil : m
    }

    /// Starts from the model, effort and permission mode last used with this
    /// agent, if it still offers them.
    private func restoreChoices() {
        model = ""
        customModel = ""
        effort = ""
        mode = harness?.defaultMode ?? harness?.modes?.first?.id ?? ""
        guard let harness else { return }
        let defaults = UserDefaults.standard
        let models = harness.models ?? []
        if let m = defaults.string(forKey: "lastModel." + harness.id), !m.isEmpty {
            if models.contains(where: { $0.id == m }) {
                model = m
            } else if harness.caps.freeModel {
                model = models.isEmpty ? "" : "__custom"
                customModel = m
            }
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

    private func create() async {
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
                mode: mode.isEmpty ? nil : mode, workspace: ws, prompt: prompt))
            lastHarness = harness.id
            lastProject = project.id
            UserDefaults.standard.set(chosenModel ?? "", forKey: "lastModel." + harness.id)
            UserDefaults.standard.set(effort, forKey: "lastEffort." + harness.id)
            UserDefaults.standard.set(mode, forKey: "lastMode." + harness.id)
            dismiss()
            onCreated(s)
        } catch {
            self.error = error.localizedDescription
        }
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
                    Text(harness.name).font(.body)
                    if let v = harness.version {
                        Text(v).font(.caption2.monospaced()).foregroundStyle(.tertiary).lineLimit(1)
                    }
                }
                if let hint = harness.hint, !harness.usable {
                    Text(hint).font(.caption).foregroundStyle(.orange)
                }
            }
            Spacer()
            if selected {
                Image(systemName: "checkmark").foregroundStyle(.tint).fontWeight(.semibold)
            }
        }
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
            if let error {
                Label(error, systemImage: "exclamationmark.triangle").foregroundStyle(.red)
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
                                    Text("git").font(.caption2.weight(.semibold))
                                        .padding(.horizontal, 5).padding(.vertical, 1)
                                        .background(.tint.opacity(0.15), in: Capsule())
                                }
                            }
                        } icon: {
                            Image(systemName: e.isGitRepo ? "folder.fill.badge.gearshape" : "folder")
                        }
                    }
                }
                if listing.entries.isEmpty {
                    Text("No subfolders").foregroundStyle(.secondary)
                }
            } else if error == nil {
                ProgressView()
            }
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
