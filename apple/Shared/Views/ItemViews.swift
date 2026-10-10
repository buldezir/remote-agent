import MarkdownUI
import RAKit
import SwiftUI

struct ItemView: View {
    let item: Item
    let store: SessionStore

    var body: some View {
        switch item.kind {
        case .userMessage:
            UserBubble(text: item.text ?? "", images: item.images ?? [], store: store,
                       pending: item.status == .queued, cancelled: item.status == .cancelled)
        case .assistantMessage:
            Markdown(item.text ?? "")
                .agentMarkdown()
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        case .reasoning:
            ReasoningView(item: item)
        case .toolCall:
            ToolCallView(item: item, children: store.children(of: item), store: store)
        case .approval:
            ApprovalView(item: item, store: store)
        case .plan:
            PlanView(plan: item.plan)
        case .notice:
            Text(item.text ?? "")
                .scaledFont(.footnote)
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity)
                .multilineTextAlignment(.center)
        case .error:
            Label(item.text ?? "Error", systemImage: "exclamationmark.triangle.fill")
                .scaledFont(.footnote)
                .foregroundStyle(Palette.red)
        case .unknown:
            EmptyView()
        }
    }
}

struct UserBubble: View {
    let text: String
    var images: [ImageRef] = []
    var store: SessionStore?
    var pending = false
    var cancelled = false

    var body: some View {
        HStack {
            Spacer(minLength: 40)
            VStack(alignment: .trailing, spacing: 2) {
                if let store, !images.isEmpty {
                    ImageGallery(images: images, store: store, height: 120, alignment: .trailing)
                        .opacity(pending || cancelled ? 0.55 : 1)
                        .padding(.bottom, 2)
                }
                if !text.isEmpty {
                    Text(text)
                        .messageFont()
                        .textSelection(.enabled)
                        .padding(.horizontal, 10)
                        .padding(.vertical, 6)
                        .foregroundStyle(Palette.onAccent)
                        .background(Palette.accent.opacity(pending || cancelled ? 0.55 : 1), in: RoundedRectangle(cornerRadius: Metrics.Corner.bubble))
                }
                if pending {
                    Text("Queued").scaledFont(.caption2).foregroundStyle(.secondary)
                } else if cancelled {
                    Text("Not sent").scaledFont(.caption2).foregroundStyle(.secondary)
                }
            }
        }
    }
}

struct ReasoningView: View {
    let item: Item
    @State private var expanded = false

    var body: some View {
        DisclosureGroup(isExpanded: $expanded) {
            Text(item.text ?? "")
                .scaledFont(.footnote)
                .foregroundStyle(.secondary)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        } label: {
            Label(item.status == .inProgress ? "Thinking…" : "Thought", systemImage: "brain")
                .scaledFont(.footnote)
                .foregroundStyle(.secondary)
        }
        .tint(Palette.subtext)
    }
}

struct ToolCallView: View {
    let item: Item
    let children: [Item]
    let store: SessionStore
    @State private var expanded = false

    private var tool: ToolCall? { item.tool }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Button {
                withAnimation(.snappy) { expanded.toggle() }
            } label: {
                HStack(spacing: 8) {
                    Image(systemName: icon)
                        .foregroundStyle(.secondary)
                        .frame(width: 18)
                    Text(tool?.title ?? tool?.name ?? "Tool")
                        .scaledFont(.footnote, design: .monospaced)
                        .lineLimit(expanded ? nil : 2)
                        .multilineTextAlignment(.leading)
                        .foregroundStyle(.primary)
                    Spacer(minLength: 4)
                    statusView
                }
            }
            .buttonStyle(.plain)

            // Shown without expanding: a screenshot is often the point.
            if let images = item.images, !images.isEmpty {
                ImageGallery(images: images, store: store, height: 160)
            }
            if expanded {
                if let input = tool?.input, input != .object([:]) {
                    ToolInputPreview(name: tool?.name, input: input)
                }
                if let out = tool?.output, !out.isEmpty {
                    CodeBlock(title: tool?.exitCode.map { "Output (exit \($0))" } ?? "Output", text: out)
                }
                if !children.isEmpty {
                    VStack(alignment: .leading, spacing: Metrics.gap) {
                        ForEach(children) { child in
                            ItemView(item: child, store: store)
                        }
                    }
                    .padding(.leading, 10)
                    .overlay(alignment: .leading) { Rectangle().fill(.quaternary).frame(width: 2) }
                }
            } else if !children.isEmpty {
                Text("\(children.count) sub-agent step\(children.count == 1 ? "" : "s")")
                    .scaledFont(.caption2)
                    .foregroundStyle(.secondary)
                    .padding(.leading, 26)
            }
        }
        .padding(.horizontal, Metrics.padding)
        .padding(.vertical, 6)
        .background(Palette.mantle, in: RoundedRectangle(cornerRadius: Metrics.Corner.card))
    }

    private var icon: String {
        switch tool?.kind ?? .other {
        case .read: "doc.text"
        case .edit: "pencil"
        case .execute: "terminal"
        case .search: "magnifyingglass"
        case .fetch: "globe"
        case .think: "person.2"
        case .other: "wrench.and.screwdriver"
        }
    }

    @ViewBuilder private var statusView: some View {
        switch item.status {
        case .inProgress: ProgressView().controlSize(.mini)
        case .pending: Image(systemName: "hand.raised.fill").foregroundStyle(Palette.peach).imageScale(.small)
        case .failed: Image(systemName: "xmark.circle.fill").foregroundStyle(Palette.red).imageScale(.small)
        case .completed: Image(systemName: "checkmark.circle.fill").foregroundStyle(Palette.green).imageScale(.small)
        default: EmptyView()
        }
    }
}

/// Shows a tool's input the way a reviewer wants to see it: the command for
/// shell calls, a mini diff for edits, the content for writes, JSON otherwise.
struct ToolInputPreview: View {
    let name: String?
    let input: JSONValue

    var body: some View {
        if let cmd = input["command"]?.stringValue {
            CodeBlock(title: "Command", text: cmd)
        } else if let old = input["old_string"]?.stringValue, let new = input["new_string"]?.stringValue {
            EditPreview(edits: [(old, new)])
        } else if case .array(let edits)? = input["edits"] {
            EditPreview(edits: edits.compactMap { e in
                guard let o = e["old_string"]?.stringValue, let n = e["new_string"]?.stringValue else { return nil }
                return (o, n)
            })
        } else if let content = input["content"]?.stringValue {
            CodeBlock(title: "New content", text: content)
        } else {
            CodeBlock(title: name ?? "Input", text: input.pretty)
        }
    }
}

struct EditPreview: View {
    let edits: [(String, String)]

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Text("Change").scaledFont(.caption2, weight: .semibold).foregroundStyle(.secondary).padding(.bottom, 4)
            ForEach(Array(lines.prefix(60).enumerated()), id: \.offset) { _, line in
                Text(line.text.isEmpty ? " " : line.text)
                    .scaledFont(.caption, design: .monospaced)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background((line.added ? Palette.green : Palette.red).opacity(0.15))
            }
            if lines.count > 60 {
                Text("… \(lines.count - 60) more lines").scaledFont(.caption2).foregroundStyle(.secondary)
            }
        }
        .textSelection(.enabled)
        .padding(6)
        .background(Palette.crust, in: RoundedRectangle(cornerRadius: Metrics.Corner.inset))
    }

    private var lines: [(text: String, added: Bool)] {
        edits.flatMap { old, new in
            old.split(separator: "\n", omittingEmptySubsequences: false).map { ("- " + $0, false) } +
            new.split(separator: "\n", omittingEmptySubsequences: false).map { ("+ " + $0, true) }
        }
    }
}

struct CodeBlock: View {
    let title: String
    let text: String
    @State private var full = false

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title).scaledFont(.caption2, weight: .semibold).foregroundStyle(.secondary)
            let lines = text.split(separator: "\n", omittingEmptySubsequences: false)
            let shown = full || lines.count <= 30 ? text : lines.prefix(30).joined(separator: "\n")
            ScrollView(.horizontal, showsIndicators: false) {
                Text(shown)
                    .scaledFont(.caption, design: .monospaced)
                    .textSelection(.enabled)
                    .fixedSize(horizontal: true, vertical: false)
            }
            if lines.count > 30 && !full {
                Button("Show all \(lines.count) lines") { full = true }
                    .scaledFont(.caption2)
                    .foregroundStyle(.tint)
            }
        }
        .padding(6)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Palette.crust, in: RoundedRectangle(cornerRadius: Metrics.Corner.inset))
    }
}

struct PlanView: View {
    let plan: Plan?

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Label("Plan", systemImage: "list.bullet.clipboard").scaledFont(.footnote, weight: .semibold)
            if let text = plan?.text, !text.isEmpty {
                Markdown(text).agentMarkdown()
            }
            ForEach(Array((plan?.entries ?? []).enumerated()), id: \.offset) { _, e in
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    Image(systemName: e.status == "completed" ? "checkmark.circle.fill" : e.status == "in_progress" ? "circle.dotted.circle" : "circle")
                        .foregroundStyle(e.status == "completed" ? Palette.green : e.status == "in_progress" ? Palette.peach : Palette.subtext)
                    Text(e.content)
                        .scaledFont(.footnote)
                        .strikethrough(e.status == "completed", color: Palette.subtext)
                        .foregroundStyle(e.status == "completed" ? .secondary : .primary)
                }
            }
        }
        .padding(Metrics.padding)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Palette.mantle, in: RoundedRectangle(cornerRadius: Metrics.Corner.card))
    }
}

struct ApprovalView: View {
    let item: Item
    let store: SessionStore
    @State private var answers: [String: Set<String>] = [:]
    @State private var denyReason = ""
    @State private var askingReason: ApprovalOption?
    @State private var busy = false
    @State private var error: String?

    private var approval: Approval? { item.approval }

    var body: some View {
        if item.status == .pending, let approval {
            pendingCard(approval)
        } else {
            resolvedLine
        }
    }

    private func pendingCard(_ a: Approval) -> some View {
        VStack(alignment: .leading, spacing: Metrics.gap) {
            Label(a.title, systemImage: a.special == .question ? "questionmark.bubble" : a.special == .plan ? "list.bullet.clipboard" : "hand.raised.fill")
                .scaledFont(.subheadline, weight: .semibold)
                .foregroundStyle(Palette.peach)
            if let d = a.detail, !d.isEmpty {
                Text(d).scaledFont(.footnote).foregroundStyle(.secondary)
            }
            if a.special == .plan, let plan = a.planText {
                ScrollView { Markdown(plan).agentMarkdown() }
                    .frame(maxHeight: 320)
            } else if a.special == .question {
                ForEach(a.questions ?? [], id: \.question) { q in questionView(q) }
            } else if let input = a.input {
                ToolInputPreview(name: a.toolName, input: input)
            }
            if let error { Text(error).scaledFont(.caption).foregroundStyle(Palette.red) }
            buttons(a)
        }
        .padding(10)
        .background(Palette.peach.opacity(0.08), in: RoundedRectangle(cornerRadius: Metrics.Corner.bubble))
        .overlay(RoundedRectangle(cornerRadius: Metrics.Corner.bubble).strokeBorder(Palette.peach.opacity(0.5)))
        .alert("Reason (optional)", isPresented: .init(get: { askingReason != nil }, set: { if !$0 { askingReason = nil } })) {
            TextField("Tell the agent why", text: $denyReason)
            Button("Deny", role: .destructive) {
                if let o = askingReason { respond(o, message: denyReason) }
            }
            Button("Cancel", role: .cancel) {}
        }
    }

    private func questionView(_ q: Question) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(q.question).scaledFont(.footnote, weight: .medium)
            FlowChips(options: q.options.map(\.label), selected: answers[q.question] ?? []) { label in
                var set = answers[q.question] ?? []
                if q.multiSelect == true {
                    if set.contains(label) { set.remove(label) } else { set.insert(label) }
                } else {
                    set = [label]
                }
                answers[q.question] = set
            }
        }
    }

    private func buttons(_ a: Approval) -> some View {
        HStack(spacing: 8) {
            ForEach(a.options) { o in
                Button {
                    if o.kind == .deny && a.special != .question { askingReason = o } else { respond(o, message: nil) }
                } label: {
                    Text(o.label).scaledFont(.footnote, weight: .semibold).lineLimit(2)
                        .foregroundStyle(Palette.onAccent)
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .buttonBorderShape(.roundedRectangle(radius: Metrics.Corner.card))
                .controlSize(.small)
                .tint(o.kind == .deny ? Palette.red : o.kind == .allowSession ? Palette.blue : Palette.green)
                .disabled(busy || (a.special == .question && o.kind != .deny && !allAnswered(a)))
            }
        }
        // When one label wraps, the other buttons grow to its height.
        .fixedSize(horizontal: false, vertical: true)
    }

    private func allAnswered(_ a: Approval) -> Bool {
        (a.questions ?? []).allSatisfy { !(answers[$0.question] ?? []).isEmpty }
    }

    private func respond(_ o: ApprovalOption, message: String?) {
        busy = true
        error = nil
        let flat = answers.mapValues { $0.sorted().joined(separator: ", ") }
        Task {
            defer { busy = false }
            do {
                try await store.respond(to: item, option: o, message: message?.isEmpty == true ? nil : message,
                                        answers: approval?.special == .question ? flat : nil)
            } catch {
                self.error = error.localizedDescription
            }
        }
    }

    private var resolvedLine: some View {
        let a = approval
        let option = a?.options.first { $0.id == a?.decision?.optionId }
        let (icon, color, verb): (String, Color, String) = switch item.status {
        case .resolved where option?.kind == .deny: ("xmark.circle", Palette.red, option?.label ?? "Denied")
        case .resolved: ("checkmark.circle", Palette.green, option?.label ?? "Allowed")
        case .expired: ("clock.badge.xmark", Palette.subtext, "Expired")
        default: ("minus.circle", Palette.subtext, "Cancelled")
        }
        return VStack(alignment: .leading, spacing: 4) {
            Label("\(verb): \(a?.title ?? "")", systemImage: icon)
                .scaledFont(.caption)
                .foregroundStyle(color)
                .lineLimit(2)
            if let answers = a?.decision?.answers, !answers.isEmpty {
                ForEach(answers.sorted(by: { $0.key < $1.key }), id: \.key) { q, ans in
                    Text("\(q) → \(ans)").scaledFont(.caption2).foregroundStyle(.secondary)
                }
            }
            if let m = a?.decision?.message, !m.isEmpty {
                Text("“\(m)”").scaledFont(.caption2).foregroundStyle(.secondary)
            }
        }
    }
}

struct FlowChips: View {
    let options: [String]
    let selected: Set<String>
    var onTap: (String) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            ForEach(options, id: \.self) { label in
                Button {
                    onTap(label)
                } label: {
                    HStack {
                        Image(systemName: selected.contains(label) ? "checkmark.circle.fill" : "circle")
                        Text(label).multilineTextAlignment(.leading)
                        Spacer()
                    }
                    .scaledFont(.footnote)
                    .padding(6)
                    .background(selected.contains(label) ? Palette.accent.opacity(0.15) : Color.clear,
                                in: RoundedRectangle(cornerRadius: Metrics.Corner.card))
                }
                .buttonStyle(.plain)
            }
        }
    }
}

extension View {
    /// The transcript's Markdown, in the message and code fonts and the
    /// message size from Settings.
    func agentMarkdown() -> some View {
        modifier(AgentMarkdown())
    }
}

private struct AgentMarkdown: ViewModifier {
    @Environment(\.messageTextSize) private var size
    @Environment(\.messageFont) private var font
    @Environment(\.codeFont) private var codeFont

    func body(content: Content) -> some View {
        let codeScale = font.codeScale(codeFont)
        // Inside the theme, so they replace its text styles.
        return content
            .markdownTextStyle(\.text) {
                FontSize(size)
                FontFamily(font.markdownFamily)
            }
            .markdownTextStyle(\.code) {
                FontFamily(codeFont.markdownFamily)
                FontSize(.em(codeScale))
                BackgroundColor(Palette.surface0)
            }
            .markdownTheme(.agent)
    }
}

/// A fenced code block, in the code font.
private struct MarkdownCodeBlock: View {
    let configuration: CodeBlockConfiguration
    @Environment(\.messageFont) private var font
    @Environment(\.codeFont) private var codeFont

    var body: some View {
        let codeScale = font.codeScale(codeFont)
        return ScrollView(.horizontal, showsIndicators: false) {
            configuration.label
                .relativeLineSpacing(.em(0.2))
                .markdownTextStyle {
                    FontFamily(codeFont.markdownFamily)
                    FontSize(.em(codeScale))
                }
                .padding(8)
        }
        .background(Palette.crust, in: RoundedRectangle(cornerRadius: Metrics.Corner.inset))
        .markdownMargin(top: 0, bottom: 8)
    }
}

extension FontChoice {
    var markdownFamily: FontProperties.Family {
        switch self {
        case .system(let design): .system(design)
        case .family(let name): .custom(name)
        }
    }
}

extension MarkdownUI.Theme {
    /// GitHub-like, sized for a transcript and in the palette's colours.
    /// `agentMarkdown()` sets the text and inline code styles, which follow Settings.
    @MainActor static let agent = Theme.gitHub
        // GitHub leaves 16pt after paragraphs and 24pt above headings; a
        // transcript reads better tighter.
        .paragraph { configuration in
            configuration.label
                .fixedSize(horizontal: false, vertical: true)
                .relativeLineSpacing(.em(0.2))
                .markdownMargin(top: 0, bottom: 8)
        }
        .heading1 { heading($0, size: 1.4) }
        .heading2 { heading($0, size: 1.25) }
        .heading3 { heading($0, size: 1.1) }
        .heading4 { heading($0, size: 1) }
        .heading5 { heading($0, size: 0.9) }
        .heading6 { heading($0, size: 0.85) }
        .listItem { configuration in
            configuration.label.markdownMargin(top: .em(0.15))
        }
        .table { configuration in
            configuration.label
                .fixedSize(horizontal: false, vertical: true)
                .markdownTableBorderStyle(.init(color: Palette.surface1))
                .markdownTableBackgroundStyle(.alternatingRows(Color.clear, Palette.mantle))
                .markdownMargin(top: 0, bottom: 8)
        }
        .link {
            ForegroundColor(Palette.blue)
        }
        .blockquote { configuration in
            HStack(spacing: 0) {
                RoundedRectangle(cornerRadius: 2)
                    .fill(Palette.surface1)
                    .relativeFrame(width: .em(0.2))
                configuration.label
                    .markdownTextStyle { ForegroundColor(Palette.subtext) }
                    .relativePadding(.horizontal, length: .em(1))
            }
            .fixedSize(horizontal: false, vertical: true)
        }
        .taskListMarker { configuration in
            Image(systemName: configuration.isCompleted ? "checkmark.square.fill" : "square")
                .foregroundStyle(configuration.isCompleted ? Palette.green : Palette.overlay)
                .imageScale(.small)
                .relativeFrame(minWidth: .em(1.5), alignment: .trailing)
        }
        .thematicBreak {
            Rectangle().fill(Palette.surface1).frame(height: 1).markdownMargin(top: 8, bottom: 8)
        }
        .codeBlock { MarkdownCodeBlock(configuration: $0) }

    @MainActor private static func heading(_ configuration: BlockConfiguration, size: CGFloat) -> some View {
        configuration.label
            .relativeLineSpacing(.em(0.125))
            .markdownMargin(top: 12, bottom: 6)
            .markdownTextStyle {
                FontWeight(.semibold)
                FontSize(.em(size))
            }
    }
}
