import RAKit
import SwiftUI

/// The sessions an agent ran in a project's folder outside Remote Agent, such
/// as in a terminal, the newest first. Choosing one picks it up at once, with
/// the model, effort and mode New session shows for the agent: no
/// confirmation, since a session that may still run in a terminal says so on
/// its row, and its transcript ends with a notice that says it again.
struct ImportSessionView: View {
    let connection: ServerConnection
    let project: Project
    let harness: HarnessInfo
    var model: String?
    var effort: String?
    var mode: String?
    /// The session it picked up.
    var onImported: (Session) -> Void
    @Environment(\.dismiss) private var dismiss

    @State private var sessions: [ImportableSession]?
    @State private var loadError: String?
    /// The one being picked up, by its id.
    @State private var importing: String?
    @State private var importError: String?

    var body: some View {
        NavigationStack {
            Form {
                Group {
                    if let importError {
                        Section {
                            Label(importError, systemImage: "exclamationmark.triangle")
                                .scaledFont(.body)
                                .foregroundStyle(Palette.red)
                        }
                    }
                    Section {
                        // Not on the footer, which keeps the form's own font.
                        rows.scaledFont(.body)
                    } footer: {
                        Text("Picks up a session \(harness.name) ran in this folder outside Remote Agent. Its next prompt goes on with it here, in the project folder.")
                            .formFooterFont()
                    }
                }
                .paletteRows()
            }
            .compactForm()
            #if os(macOS)
            .frame(minWidth: 460, minHeight: 420)
            #endif
            .navigationTitle("Terminal sessions")
            .inlineTitle()
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
            }
        }
        .task { await load() }
    }

    @ViewBuilder private var rows: some View {
        if let sessions {
            ForEach(sessions) { s in row(s) }
            if sessions.isEmpty {
                Text("No \(harness.name) sessions to continue in \(project.name).")
                    .foregroundStyle(Palette.subtext)
            }
        } else if let loadError {
            Label(loadError, systemImage: "exclamationmark.triangle").foregroundStyle(Palette.red)
            Button("Retry") { Task { await load() } }
        } else {
            HStack(spacing: Metrics.gap) {
                ProgressView().controlSize(.small)
                Text("Looking for sessions…").foregroundStyle(Palette.subtext)
            }
        }
    }

    private func row(_ s: ImportableSession) -> some View {
        Button {
            Task { await pick(s) }
        } label: {
            HStack(spacing: Metrics.gap) {
                VStack(alignment: .leading, spacing: 3) {
                    Text(s.title)
                        .lineLimit(2)
                        .multilineTextAlignment(.leading)
                    Text("\(s.messages == 1 ? "1 message" : "\(s.messages) messages") · \(s.updatedAt, format: .relative(presentation: .named))")
                        .scaledFont(.caption)
                        .foregroundStyle(Palette.subtext)
                    if s.isActive {
                        Label("Active in the last minute: if it still runs in a terminal, quit it there first.",
                              systemImage: "exclamationmark.triangle.fill")
                            .scaledFont(.caption)
                            .foregroundStyle(Palette.yellow)
                    }
                }
                Spacer(minLength: 0)
                if importing == s.id {
                    ProgressView().controlSize(.small)
                }
            }
            .contentShape(Rectangle())
            .paletteText()
        }
        .buttonStyle(.plain)
        .disabled(importing != nil && importing != s.id)
    }

    private func load() async {
        loadError = nil
        do {
            sessions = try await connection.importable(projectID: project.id, harness: harness.id)
        } catch {
            loadError = error.localizedDescription
        }
    }

    private func pick(_ s: ImportableSession) async {
        guard importing == nil else { return }
        importing = s.id
        importError = nil
        defer { importing = nil }
        do {
            let session = try await connection.importSession(.init(
                projectId: project.id, harness: harness.id, nativeId: s.id,
                model: model, effort: effort, mode: mode))
            onImported(session)
        } catch {
            importError = error.localizedDescription
        }
    }
}
