import MarkdownUI
import RAKit
import SwiftUI

struct ItemView: View {
    let item: Item
    let store: SessionStore

    var body: some View {
        switch item.kind {
        case .userMessage:
            UserBubble(text: item.text ?? "", pending: item.status == .queued, cancelled: item.status == .cancelled)
        case .assistantMessage:
            Markdown(item.text ?? "")
                .markdownTheme(.agent)
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
                .font(.footnote)
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity)
                .multilineTextAlignment(.center)
        case .error:
            Label(item.text ?? "Error", systemImage: "exclamationmark.triangle.fill")
                .font(.footnote)
                .foregroundStyle(.red)
        case .unknown:
            EmptyView()
        }
    }
}

struct UserBubble: View {
    let text: String
    var pending = false
    var cancelled = false

    var body: some View {
        HStack {
            Spacer(minLength: 40)
            VStack(alignment: .trailing, spacing: 2) {
                Text(text)
                    .textSelection(.enabled)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                    .foregroundStyle(.white)
                    .background(Color.accentColor.opacity(pending || cancelled ? 0.55 : 1), in: RoundedRectangle(cornerRadius: 16))
                if pending {
                    Text("Queued").font(.caption2).foregroundStyle(.secondary)
                } else if cancelled {
                    Text("Not sent").font(.caption2).foregroundStyle(.secondary)
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
                .font(.footnote)
                .foregroundStyle(.secondary)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        } label: {
            Label(item.status == .inProgress ? "Thinking…" : "Thought", systemImage: "brain")
                .font(.footnote)
                .foregroundStyle(.secondary)
        }
        .tint(.secondary)
    }
}

struct ToolCallView: View {
    let item: Item
    let children: [Item]
    let store: SessionStore
    @State private var expanded = false

    private var tool: ToolCall? { item.tool }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Button {
                withAnimation(.snappy) { expanded.toggle() }
            } label: {
                HStack(spacing: 8) {
                    Image(systemName: icon)
                        .foregroundStyle(.secondary)
                        .frame(width: 18)
                    Text(tool?.title ?? tool?.name ?? "Tool")
                        .font(.footnote.monospaced())
                        .lineLimit(expanded ? nil : 2)
                        .multilineTextAlignment(.leading)
                        .foregroundStyle(.primary)
                    Spacer(minLength: 4)
                    statusView
                }
            }
            .buttonStyle(.plain)

            if expanded {
                if let input = tool?.input, input != .object([:]) {
                    ToolInputPreview(name: tool?.name, input: input)
                }
                if let out = tool?.output, !out.isEmpty {
                    CodeBlock(title: tool?.exitCode.map { "Output (exit \($0))" } ?? "Output", text: out)
                }
                if !children.isEmpty {
                    VStack(alignment: .leading, spacing: 10) {
                        ForEach(children) { child in
                            ItemView(item: child, store: store)
                        }
                    }
                    .padding(.leading, 10)
                    .overlay(alignment: .leading) { Rectangle().fill(.quaternary).frame(width: 2) }
                }
            } else if !children.isEmpty {
                Text("\(children.count) sub-agent step\(children.count == 1 ? "" : "s")")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .padding(.leading, 26)
            }
        }
        .padding(10)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 10))
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
        case .pending: Image(systemName: "hand.raised.fill").foregroundStyle(.orange).imageScale(.small)
        case .failed: Image(systemName: "xmark.circle.fill").foregroundStyle(.red).imageScale(.small)
        case .completed: Image(systemName: "checkmark.circle.fill").foregroundStyle(.green).imageScale(.small)
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
            Text("Change").font(.caption2.weight(.semibold)).foregroundStyle(.secondary).padding(.bottom, 4)
            ForEach(Array(lines.prefix(60).enumerated()), id: \.offset) { _, line in
                Text(line.text.isEmpty ? " " : line.text)
                    .font(.caption.monospaced())
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(line.added ? Color.green.opacity(0.14) : Color.red.opacity(0.14))
            }
            if lines.count > 60 {
                Text("… \(lines.count - 60) more lines").font(.caption2).foregroundStyle(.secondary)
            }
        }
        .textSelection(.enabled)
        .padding(8)
        .background(.background, in: RoundedRectangle(cornerRadius: 6))
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
            Text(title).font(.caption2.weight(.semibold)).foregroundStyle(.secondary)
            let lines = text.split(separator: "\n", omittingEmptySubsequences: false)
            let shown = full || lines.count <= 30 ? text : lines.prefix(30).joined(separator: "\n")
            ScrollView(.horizontal, showsIndicators: false) {
                Text(shown)
                    .font(.caption.monospaced())
                    .textSelection(.enabled)
                    .fixedSize(horizontal: true, vertical: false)
            }
            if lines.count > 30 && !full {
                Button("Show all \(lines.count) lines") { full = true }
                    .font(.caption2)
            }
        }
        .padding(8)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.background, in: RoundedRectangle(cornerRadius: 6))
    }
}

struct PlanView: View {
    let plan: Plan?

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Label("Plan", systemImage: "list.bullet.clipboard").font(.footnote.weight(.semibold))
            if let text = plan?.text, !text.isEmpty {
                Markdown(text).markdownTheme(.agent)
            }
            ForEach(Array((plan?.entries ?? []).enumerated()), id: \.offset) { _, e in
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    Image(systemName: e.status == "completed" ? "checkmark.circle.fill" : e.status == "in_progress" ? "circle.dotted.circle" : "circle")
                        .foregroundStyle(e.status == "completed" ? .green : e.status == "in_progress" ? .orange : .secondary)
                    Text(e.content)
                        .font(.footnote)
                        .strikethrough(e.status == "completed", color: .secondary)
                        .foregroundStyle(e.status == "completed" ? .secondary : .primary)
                }
            }
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.background.secondary, in: RoundedRectangle(cornerRadius: 10))
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
        VStack(alignment: .leading, spacing: 10) {
            Label(a.title, systemImage: a.special == .question ? "questionmark.bubble" : a.special == .plan ? "list.bullet.clipboard" : "hand.raised.fill")
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(.orange)
            if let d = a.detail, !d.isEmpty {
                Text(d).font(.footnote).foregroundStyle(.secondary)
            }
            if a.special == .plan, let plan = a.planText {
                ScrollView { Markdown(plan).markdownTheme(.agent) }
                    .frame(maxHeight: 320)
            } else if a.special == .question {
                ForEach(a.questions ?? [], id: \.question) { q in questionView(q) }
            } else if let input = a.input {
                ToolInputPreview(name: a.toolName, input: input)
            }
            if let error { Text(error).font(.caption).foregroundStyle(.red) }
            buttons(a)
        }
        .padding(12)
        .background(.orange.opacity(0.08), in: RoundedRectangle(cornerRadius: 12))
        .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(.orange.opacity(0.5)))
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
            Text(q.question).font(.footnote.weight(.medium))
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
                    Text(o.label).font(.footnote.weight(.semibold)).lineLimit(2).frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent)
                .tint(o.kind == .deny ? .red : o.kind == .allowSession ? .indigo : .green)
                .disabled(busy || (a.special == .question && o.kind != .deny && !allAnswered(a)))
            }
        }
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
        case .resolved where option?.kind == .deny: ("xmark.circle", .red, option?.label ?? "Denied")
        case .resolved: ("checkmark.circle", .green, option?.label ?? "Allowed")
        case .expired: ("clock.badge.xmark", .secondary, "Expired")
        default: ("minus.circle", .secondary, "Cancelled")
        }
        return VStack(alignment: .leading, spacing: 4) {
            Label("\(verb): \(a?.title ?? "")", systemImage: icon)
                .font(.caption)
                .foregroundStyle(color)
                .lineLimit(2)
            if let answers = a?.decision?.answers, !answers.isEmpty {
                ForEach(answers.sorted(by: { $0.key < $1.key }), id: \.key) { q, ans in
                    Text("\(q) → \(ans)").font(.caption2).foregroundStyle(.secondary)
                }
            }
            if let m = a?.decision?.message, !m.isEmpty {
                Text("“\(m)”").font(.caption2).foregroundStyle(.secondary)
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
                    .font(.footnote)
                    .padding(8)
                    .background(selected.contains(label) ? Color.accentColor.opacity(0.15) : Color.clear,
                                in: RoundedRectangle(cornerRadius: 8))
                }
                .buttonStyle(.plain)
            }
        }
    }
}

extension MarkdownUI.Theme {
    /// GitHub-like, sized for a phone transcript.
    @MainActor static let agent = Theme.gitHub
        .text { FontSize(15) }
        .code {
            FontFamilyVariant(.monospaced)
            FontSize(.em(0.88))
            BackgroundColor(Color.secondary.opacity(0.12))
        }
        .codeBlock { configuration in
            ScrollView(.horizontal, showsIndicators: false) {
                configuration.label
                    .relativeLineSpacing(.em(0.2))
                    .markdownTextStyle {
                        FontFamilyVariant(.monospaced)
                        FontSize(.em(0.8))
                    }
                    .padding(10)
            }
            .background(Color.secondary.opacity(0.1), in: RoundedRectangle(cornerRadius: 8))
            .markdownMargin(top: 4, bottom: 8)
        }
}
