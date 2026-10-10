import RAKit
import SwiftUI

struct DiffView: View {
    let store: SessionStore
    let initialTurn: Turn?
    let canRevert: Bool
    @Environment(\.dismiss) private var dismiss

    @State private var scope: String // "session" or a turn id
    @State private var diff: Diff?
    @State private var loading = false
    @State private var error: String?
    @State private var revertTurn: Turn?
    @State private var reverted: String?

    init(store: SessionStore, initialTurn: Turn?, canRevert: Bool) {
        self.store = store
        self.initialTurn = initialTurn
        self.canRevert = canRevert
        _scope = State(initialValue: initialTurn?.id ?? "session")
    }

    var body: some View {
        List {
            Group {
                Section {
                    Picker("Scope", selection: $scope) {
                        Text("Whole session").tag("session")
                        ForEach(store.sortedTurns.filter { $0.checkpointBefore != nil }) { t in
                            Text("Turn \(t.n)").tag(t.id)
                        }
                    }
                    .scaledFont(.body)
                }
                if let error {
                    Label(error, systemImage: "exclamationmark.triangle").scaledFont(.body).foregroundStyle(Palette.red)
                }
                if let reverted {
                    Label(reverted, systemImage: "arrow.uturn.backward.circle").scaledFont(.body).foregroundStyle(Palette.green)
                }
                if let diff {
                    let sections = PatchParser.split(diff.patch)
                    Section {
                        if diff.files.isEmpty {
                            Text("No changes").scaledFont(.body).foregroundStyle(.secondary)
                        }
                        ForEach(diff.files) { f in
                            NavigationLink {
                                FileDiffView(file: f, patch: sections[f.path] ?? "")
                            } label: {
                                FileRow(file: f)
                            }
                        }
                    } header: {
                        let adds = diff.files.reduce(0) { $0 + $1.additions }
                        let dels = diff.files.reduce(0) { $0 + $1.deletions }
                        Text("\(diff.files.count) file\(diff.files.count == 1 ? "" : "s")  +\(adds) −\(dels)")
                            .listHeaderFont()
                    } footer: {
                        if diff.truncated { Text("The patch was truncated; some files may show no lines.").listHeaderFont() }
                    }
                } else if loading {
                    ProgressView().frame(maxWidth: .infinity)
                }
            }
            .paletteRows()
        }
        .compactForm()
        .navigationTitle("Changes")
        .inlineTitle()
        .toolbar {
            ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } }
            if canRevert {
                ToolbarItem(placement: revertPlacement) {
                    Menu {
                        ForEach(store.sortedTurns.filter { $0.checkpointBefore != nil }.reversed()) { t in
                            Button("Before turn \(t.n)") { revertTurn = t }
                        }
                    } label: {
                        Label("Revert files…", systemImage: "arrow.uturn.backward")
                            .labelStyle(.titleAndIcon)
                    }
                    .disabled(store.isRunning)
                }
            }
        }
        .confirmationDialog("Revert files?", isPresented: .init(get: { revertTurn != nil }, set: { if !$0 { revertTurn = nil } }),
                            presenting: revertTurn) { t in
            Button("Revert to before turn \(t.n)", role: .destructive) {
                Task { await revert(t) }
            }
        } message: { t in
            Text("Files changed in turn \(t.n) and later are restored in the worktree. The conversation is kept, and the agent is told about the revert.")
        }
        .task(id: scope) { await load() }
        .refreshable { await load() }
    }

    private var revertPlacement: ToolbarItemPlacement {
        #if os(iOS)
        .bottomBar
        #else
        .automatic
        #endif
    }

    private func load() async {
        loading = true
        error = nil
        defer { loading = false }
        do {
            diff = scope == "session" ? try await store.sessionDiff() : try await store.turnDiff(scope)
        } catch {
            diff = nil
            self.error = error.localizedDescription
        }
    }

    private func revert(_ t: Turn) async {
        do {
            let files = try await store.revert(toTurn: t.n)
            reverted = "Reverted \(files.count) file\(files.count == 1 ? "" : "s")"
            await load()
        } catch {
            self.error = error.localizedDescription
        }
    }
}

struct FileRow: View {
    let file: FileStat

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: icon).foregroundStyle(color)
            VStack(alignment: .leading, spacing: 1) {
                Text((file.path as NSString).lastPathComponent).scaledFont(.footnote, weight: .medium)
                let dir = (file.path as NSString).deletingLastPathComponent
                if let old = file.oldPath {
                    Text("\(old) → \(file.path)")
                        .scaledFont(.caption2, design: .monospaced).foregroundStyle(.secondary)
                        .lineLimit(1).truncationMode(.head)
                } else if !dir.isEmpty {
                    Text(dir)
                        .scaledFont(.caption2, design: .monospaced).foregroundStyle(.secondary)
                        .lineLimit(1).truncationMode(.head)
                }
            }
            Spacer()
            if file.binary == true {
                Text("binary").scaledFont(.caption2).foregroundStyle(.secondary)
            } else {
                Text("+\(file.additions)").foregroundStyle(Palette.green).scaledFont(.caption, design: .monospaced)
                Text("−\(file.deletions)").foregroundStyle(Palette.red).scaledFont(.caption, design: .monospaced)
            }
        }
        .paletteText()
    }

    private var icon: String {
        switch file.status {
        case .added: "plus.square"
        case .deleted: "minus.square"
        case .renamed: "arrow.right.square"
        case .modified: "pencil.and.list.clipboard"
        }
    }

    private var color: Color {
        switch file.status {
        case .added: Palette.green
        case .deleted: Palette.red
        case .renamed: Palette.blue
        case .modified: Palette.peach
        }
    }
}

struct FileDiffView: View {
    let file: FileStat
    let patch: String
    @AppStorage("diffWrap") private var wrap = true

    var body: some View {
        let lines = PatchParser.lines(patch)
        ScrollView(wrap ? .vertical : [.vertical, .horizontal]) {
            LazyVStack(alignment: .leading, spacing: 0) {
                if lines.isEmpty {
                    Text(file.binary == true ? "Binary file" : "No textual changes")
                        .foregroundStyle(.secondary).padding()
                }
                ForEach(lines) { line in
                    Text(line.text.isEmpty ? " " : line.text)
                        .scaledFont(size: 12, design: .monospaced)
                        .lineLimit(wrap ? nil : 1)
                        .fixedSize(horizontal: !wrap, vertical: true)
                        .frame(maxWidth: wrap ? .infinity : nil, alignment: .leading)
                        .padding(.horizontal, 8)
                        .padding(.vertical, 1)
                        .background(line.kind.background)
                        .foregroundStyle(line.kind.foreground)
                }
            }
            .textSelection(.enabled)
        }
        .background(Palette.base)
        .navigationTitle((file.path as NSString).lastPathComponent)
        .inlineTitle()
        .toolbar {
            Toggle(isOn: $wrap) { Label("Wrap lines", systemImage: "text.word.spacing") }
                .toggleStyle(.button)
        }
    }
}

struct DiffLine: Identifiable {
    enum Kind {
        case add, del, hunk, meta, context
        var background: Color {
            switch self {
            case .add: Palette.green.opacity(0.15)
            case .del: Palette.red.opacity(0.15)
            case .hunk: Palette.blue.opacity(0.12)
            default: .clear
            }
        }
        var foreground: Color {
            switch self {
            case .hunk, .meta: Palette.subtext
            default: Palette.text
            }
        }
    }
    let id: Int
    let text: String
    let kind: Kind
}

enum PatchParser {
    /// Splits a multi-file unified diff into per-file sections keyed by new path.
    static func split(_ patch: String) -> [String: String] {
        var out: [String: String] = [:]
        var current: [Substring] = []
        func flush() {
            guard let header = current.first else { return }
            var path: String?
            for l in current.prefix(8) {
                if l.hasPrefix("+++ b/") { path = String(l.dropFirst(6)) }
                else if l.hasPrefix("rename to ") { path = String(l.dropFirst(10)) }
                else if l.hasPrefix("--- a/"), path == nil { path = String(l.dropFirst(6)) }
            }
            if path == nil, let r = header.range(of: " b/") { path = String(header[r.upperBound...]) }
            if let path { out[path] = current.joined(separator: "\n") }
        }
        for line in patch.split(separator: "\n", omittingEmptySubsequences: false) {
            if line.hasPrefix("diff --git ") {
                flush()
                current = [line]
            } else if !current.isEmpty {
                current.append(line)
            }
        }
        flush()
        return out
    }

    static func lines(_ section: String) -> [DiffLine] {
        var out: [DiffLine] = []
        var inHunk = false
        for (i, l) in section.split(separator: "\n", omittingEmptySubsequences: false).enumerated() {
            let s = String(l)
            let kind: DiffLine.Kind
            if s.hasPrefix("@@") { kind = .hunk; inHunk = true }
            else if !inHunk { continue } // skip diff/index/---/+++ headers
            else if s.hasPrefix("+") { kind = .add }
            else if s.hasPrefix("-") { kind = .del }
            else if s.hasPrefix("\\") { kind = .meta }
            else { kind = .context }
            out.append(DiffLine(id: i, text: s, kind: kind))
        }
        if out.last?.text.isEmpty == true { out.removeLast() }
        return out
    }
}
