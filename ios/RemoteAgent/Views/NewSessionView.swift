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
                    ForEach(connection.harnesses) { h in
                        HarnessChoiceRow(harness: h, selected: h.id == harnessID)
                            .contentShape(Rectangle())
                            .onTapGesture { if h.usable { harnessID = h.id } }
                            .disabled(!h.usable)
                    }
                    if connection.harnesses.isEmpty {
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
                                .textInputAutocapitalization(.never)
                                .autocorrectionDisabled()
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
                            TextField("Branch (auto)", text: $branch)
                                .textInputAutocapitalization(.never)
                                .autocorrectionDisabled()
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

                Section("Prompt") {
                    TextField("What should the agent do?", text: $prompt, axis: .vertical)
                        .lineLimit(4...12)
                        .focused($promptFocused)
                }

                if let error {
                    Section { Label(error, systemImage: "exclamationmark.triangle").foregroundStyle(.red) }
                }
            }
            .navigationTitle("New session")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    if creating {
                        ProgressView()
                    } else {
                        Button("Start") { Task { await create() } }
                            .disabled(project == nil || harness == nil || prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
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
            }
            .task {
                if connection.harnesses.isEmpty { await connection.refreshHarnesses() }
                if projectID.isEmpty, connection.projects[lastProject] != nil { projectID = lastProject }
                if harnessID.isEmpty {
                    harnessID = connection.harness(lastHarness)?.usable == true ? lastHarness
                        : (connection.harnesses.first(where: \.usable)?.id ?? "")
                }
            }
            .onChange(of: harnessID) { _, _ in
                model = ""
                effort = ""
                mode = harness?.defaultMode ?? harness?.modes?.first?.id ?? ""
            }
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

    /// The model id to request; nil for the harness default.
    private var chosenModel: String? {
        let m = model == "__custom" || model.isEmpty ? customModel : model
        return m.isEmpty ? nil : m
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
        .navigationTitle(path.map { ($0 as NSString).lastPathComponent } ?? "Folders")
        .navigationBarTitleDisplayMode(.inline)
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
