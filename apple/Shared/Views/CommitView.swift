import RAKit
import SwiftUI

/// Commits a session's changes, then pushes them and opens a pull request,
/// as far as the user chooses. First it shows what a commit would take, with
/// the message the system agent wrote for it, which the user can change;
/// then each step as it goes on the server. Closing it leaves the commit
/// running there. Opened while the session's commit runs, from here or
/// another device, it follows that one.
struct CommitView: View {
    let connection: ServerConnection
    let sessionID: String
    @Environment(\.dismiss) private var dismiss
    @Environment(\.openURL) private var openURL
    #if os(iOS)
    @Environment(\.horizontalSizeClass) private var sizeClass
    #endif
    /// How far the last commit went, which the next one starts at.
    @AppStorage("commitScope") private var savedScope = ""

    @State private var preview: CommitPreview?
    @State private var loadError: String?
    @State private var message = ""
    @State private var scope: ServerConnection.CommitScope = .commit
    @State private var starting = false
    @State private var startError: String?
    @State private var progress: ServerTaskProgress?
    @State private var showOutput = false

    var body: some View {
        NavigationStack {
            Form {
                Group {
                    if let progress {
                        progressRows(progress)
                    } else if let preview {
                        previewRows(preview)
                    } else if let loadError {
                        Section {
                            Label(loadError, systemImage: "exclamationmark.triangle").foregroundStyle(Palette.red)
                            Button("Retry") { Task { await load() } }
                        }
                    } else {
                        Section {
                            HStack(spacing: Metrics.gap) {
                                ProgressView().controlSize(.small)
                                Text("Looking at the changes…").foregroundStyle(Palette.subtext)
                            }
                        }
                    }
                }
                .scaledFont(.body)
                .paletteRows()
            }
            .compactForm()
            #if os(macOS)
            .frame(minWidth: 480, minHeight: 520)
            #endif
            .navigationTitle("Commit")
            .inlineTitle()
            .toolbar { toolbar }
        }
        .task { await load() }
        .onDisappear { progress?.stop() }
    }

    // MARK: Before

    /// What the user can choose: committing needs changes, and pushing a
    /// remote.
    private func scopes(_ p: CommitPreview) -> [ServerConnection.CommitScope] {
        var out: [ServerConnection.CommitScope] = p.files.isEmpty ? [] : [.commit]
        if p.remote != nil && p.branch != nil { out += [.push, .pullRequest] }
        return out
    }

    private func label(_ s: ServerConnection.CommitScope, files: Bool) -> String {
        switch s {
        case .commit: "Commit only"
        case .push: files ? "Commit & push" : "Push"
        case .pullRequest: files ? "Commit, push & open PR" : "Push & open PR"
        }
    }

    private var actionTitle: String {
        let files = preview?.files.isEmpty == false
        switch scope {
        case .commit: return "Commit"
        case .push: return files ? "Commit & Push" : "Push"
        case .pullRequest: return files ? (narrow ? "Commit & Open PR" : "Commit, Push & Open PR") : "Push & Open PR"
        }
    }

    /// A phone's navigation bar, where the whole of the longest title would
    /// squeeze the sheet's own to a letter. Opening a pull request pushes.
    private var narrow: Bool {
        #if os(iOS)
        sizeClass == .compact
        #else
        false
        #endif
    }

    private var canStart: Bool {
        guard let preview, progress == nil, !starting else { return false }
        return scopes(preview).contains(scope)
    }

    @ViewBuilder private func previewRows(_ p: CommitPreview) -> some View {
        Section {
            HStack(spacing: Metrics.gap) {
                Label(p.branch ?? "Detached HEAD", systemImage: "arrow.triangle.branch")
                    .lineLimit(1).truncationMode(.middle)
                Spacer()
                Text(p.remote.map { "→ \($0)" } ?? "No remote to push to")
                    .foregroundStyle(Palette.subtext)
            }
            if scope == .pullRequest {
                Label("Pull request into \(p.base ?? "the default branch")", systemImage: "arrow.triangle.pull")
            }
        } header: {
            Text("Branch").formHeaderFont()
        }

        Section {
            if p.files.isEmpty {
                Text("Nothing to commit.").foregroundStyle(Palette.subtext)
                if p.ahead > 0 {
                    Text("\(p.ahead) commit\(p.ahead == 1 ? "" : "s") to push")
                }
            }
            ForEach(p.files) { FileRow(file: $0) }
        } header: {
            if p.files.isEmpty {
                Text("Changes").formHeaderFont()
            } else {
                let adds = p.files.reduce(0) { $0 + $1.additions }
                let dels = p.files.reduce(0) { $0 + $1.deletions }
                Text("\(p.files.count) file\(p.files.count == 1 ? "" : "s") changed  +\(adds) −\(dels)").formHeaderFont()
            }
        }

        if !p.files.isEmpty {
            Section {
                TextField("Message", text: $message, prompt: Text("Commit message"), axis: .vertical)
                    .labelsHidden()
                    .lineLimit(3...12)
                    .messageFont()
                    .shiftReturnNewline()
            } header: {
                Text("Message").formHeaderFont()
            } footer: {
                Text("Leave it empty and rad writes one.").formFooterFont()
            }
        }

        let choices = scopes(p)
        Section {
            if choices.count > 1 {
                Picker("Then", selection: $scope) {
                    ForEach(choices, id: \.self) { Text(label($0, files: !p.files.isEmpty)).tag($0) }
                }
                .labelsHidden()
                #if os(macOS)
                .pickerStyle(.radioGroup)
                #else
                .pickerStyle(.inline)
                #endif
            } else if let only = choices.first {
                Text(label(only, files: !p.files.isEmpty))
            } else {
                Text("Nothing to commit or push.").foregroundStyle(Palette.subtext)
            }
        } header: {
            Text("Do").formHeaderFont()
        } footer: {
            if p.branch != nil, p.remote == nil {
                Text("The repository has no remote to push to.").formFooterFont()
            } else if p.branch == nil {
                Text("HEAD is detached: check out a branch to push it.").formFooterFont()
            } else if scope == .pullRequest {
                Text("GitHub's gh opens it on the computer, with a title and description written from the branch's commits.").formFooterFont()
            }
        }

        if let startError {
            Section { Label(startError, systemImage: "exclamationmark.triangle").foregroundStyle(Palette.red) }
        }
    }

    // MARK: After

    @ViewBuilder private func progressRows(_ p: ServerTaskProgress) -> some View {
        let task = p.task
        Section {
            if let steps = task.steps, !steps.isEmpty {
                ForEach(steps) { StepRow(step: $0) }
            } else {
                HStack(spacing: Metrics.gap) {
                    ProgressView().controlSize(.small)
                    Text("Starting…").foregroundStyle(Palette.subtext)
                }
            }
        } header: {
            Text("Steps").formHeaderFont()
        }

        Section {
            switch p.phase {
            case .running, .restarting:
                HStack(spacing: Metrics.gap) {
                    ProgressView().controlSize(.small)
                    Text("Working…")
                }
            case .done:
                Label("Done.", systemImage: "checkmark.circle.fill").foregroundStyle(Palette.green)
            case .failed(let why):
                Label("Failed: \(why)", systemImage: "xmark.octagon.fill").foregroundStyle(Palette.red)
            case .cancelled:
                Label("Cancelled.", systemImage: "xmark.circle").foregroundStyle(Palette.subtext)
            }
            if let url = task.url.flatMap(URL.init(string:)), ["http", "https"].contains(url.scheme) {
                HStack {
                    Button { openURL(url) } label: {
                        Label("Open Pull Request", systemImage: "arrow.up.right.square").foregroundStyle(Palette.onAccent)
                    }
                    .buttonStyle(.borderedProminent)
                    Button("Copy Link") { Platform.copy(url.absoluteString) }
                        .buttonStyle(.bordered)
                }
                Text(url.absoluteString)
                    .scaledFont(.caption, design: .monospaced)
                    .foregroundStyle(Palette.subtext)
                    .textSelection(.enabled)
            }
        }
        .onChange(of: p.phase, initial: true) { _, phase in
            if case .failed = phase { showOutput = true }
        }

        if !task.output.isEmpty {
            Section {
                DisclosureGroup("Output", isExpanded: $showOutput) {
                    Text(task.output)
                        .scaledFont(.caption, design: .monospaced)
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
        }
    }

    // MARK: Toolbar

    @ToolbarContentBuilder private var toolbar: some ToolbarContent {
        if let progress {
            if progress.isActive {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { Task { try? await progress.cancel() } }
                }
            }
            ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } }
        } else {
            ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
            // Where the other sheets have theirs, so that it's there without
            // scrolling past the files, saying how far it goes.
            ToolbarItem(placement: .confirmationAction) {
                if starting {
                    ProgressView().controlSize(.small)
                } else {
                    Button(actionTitle) { Task { await start() } }
                        .disabled(!canStart)
                }
            }
        }
    }

    // MARK: Actions

    private func load() async {
        loadError = nil
        // A commit that runs already, started here or on another device.
        if progress == nil, let running = try? await connection.tasks(sessionID: sessionID).last(where: { $0.kind == .commit && $0.isRunning }) {
            follow(running)
            return
        }
        do {
            let p = try await connection.commitPreview(sessionID: sessionID)
            preview = p
            if message.isEmpty { message = p.message }
            // The last choice, else as far as a push; with nothing to commit,
            // a commit becomes a push.
            let choices = scopes(p)
            let wanted = ServerConnection.CommitScope(rawValue: savedScope) ?? (p.remote != nil ? .push : .commit)
            if choices.contains(wanted) {
                scope = wanted
            } else if wanted == .commit, choices.contains(.push) {
                scope = .push
            } else {
                scope = choices.first ?? .commit
            }
        } catch {
            loadError = error.localizedDescription
        }
    }

    private func start() async {
        guard let preview, canStart else { return }
        starting = true
        startError = nil
        defer { starting = false }
        savedScope = scope.rawValue
        do {
            let task = try await connection.commit(sessionID: sessionID, message: preview.files.isEmpty ? "" : message, scope: scope)
            follow(task)
        } catch {
            startError = error.localizedDescription
        }
    }

    private func follow(_ task: ServerTask) {
        let p = ServerTaskProgress(task: task, source: connection)
        progress = p
        p.start()
    }
}

/// A step of a task: how it stands, what it is, and what it did.
private struct StepRow: View {
    let step: ServerTask.Step

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: Metrics.gap) {
            icon.frame(width: 18)
            VStack(alignment: .leading, spacing: 2) {
                Text(step.title)
                    .foregroundStyle(step.status == .pending || step.status == .skipped ? Palette.subtext : Palette.text)
                if let detail = step.detail, !detail.isEmpty {
                    Text(detail)
                        .scaledFont(.footnote)
                        .foregroundStyle(step.status == .failed ? Palette.red : Palette.subtext)
                        .textSelection(.enabled)
                }
            }
        }
        .accessibilityElement(children: .combine)
    }

    @ViewBuilder private var icon: some View {
        switch step.status {
        case .pending: Image(systemName: "circle").foregroundStyle(Palette.subtext)
        case .running: ProgressView().controlSize(.small)
        case .succeeded: Image(systemName: "checkmark.circle.fill").foregroundStyle(Palette.green)
        case .skipped: Image(systemName: "minus.circle").foregroundStyle(Palette.subtext)
        case .failed: Image(systemName: "xmark.circle.fill").foregroundStyle(Palette.red)
        }
    }
}
