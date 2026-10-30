import RAKit
import SwiftUI

struct ItemView: View {
    let item: Item
    let store: SessionStore

    var body: some View {
        switch item.kind {
        case .userMessage:
            UserBubble(text: item.text ?? "", images: item.images ?? [], store: store,
                       pending: item.status == .queued, cancelled: item.status == .cancelled,
                       sendNow: item.status == .queued ? { try await store.sendNow(item) } : nil,
                       cancelQueued: item.status == .queued ? { try await store.cancelQueued(item) } : nil)
        case .assistantMessage:
            AgentMarkdown(text: item.text ?? "", images: item.images ?? [], store: store)
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
    /// The session's draft: outlined, since it hasn't gone.
    var draft = false
    /// Sends a queued prompt at once, stopping the turn it waits for.
    var sendNow: (() async throws -> Void)?
    /// Takes a queued prompt out of the queue, unsent.
    var cancelQueued: (() async throws -> Void)?
    @State private var sending = false
    @State private var error: String?

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
                        .foregroundStyle(draft ? Palette.text : Palette.onAccent)
                        .background { bubble }
                }
                if pending {
                    HStack(spacing: 4) {
                        Text("Queued")
                        if let sendNow {
                            Text("·")
                            Button("Send now") { send(sendNow) }
                                .buttonStyle(.borderless)
                                .fontWeight(.semibold)
                                .foregroundStyle(Palette.accent)
                                .disabled(sending)
                                .help("Stop the current turn and send this prompt now")
                        }
                        if let cancelQueued {
                            Text("·")
                            Button("Cancel") { send(cancelQueued) }
                                .buttonStyle(.borderless)
                                .fontWeight(.semibold)
                                .foregroundStyle(Palette.accent)
                                .disabled(sending)
                                .help("Don't send this prompt")
                        }
                    }
                    .scaledFont(.caption2)
                    .foregroundStyle(.secondary)
                } else if cancelled {
                    Text("Not sent").scaledFont(.caption2).foregroundStyle(.secondary)
                }
                if let error { Text(error).scaledFont(.caption2).foregroundStyle(Palette.red) }
            }
        }
    }

    @ViewBuilder private var bubble: some View {
        let shape = RoundedRectangle(cornerRadius: Metrics.Corner.bubble)
        if draft {
            shape.fill(Palette.accent.opacity(0.12))
                .overlay(shape.strokeBorder(Palette.accent, style: StrokeStyle(lineWidth: 1, dash: [4, 3])))
        } else {
            shape.fill(Palette.accent.opacity(pending || cancelled ? 0.55 : 1))
        }
    }

    private func send(_ f: @escaping () async throws -> Void) {
        sending = true
        error = nil
        Task {
            defer { sending = false }
            do { try await f() } catch { self.error = error.localizedDescription }
        }
    }
}

/// The session's draft at the foot of its transcript: the prompt as it will
/// go, outlined, with what to do with it under it.
struct DraftBubble: View {
    let draft: Draft
    let store: SessionStore
    /// Edit and Send wait: the session is archived, or a change to the draft
    /// is on its way to the server.
    var disabled = false
    /// Takes the draft back into the composer.
    let edit: () -> Void
    let send: () -> Void

    var body: some View {
        VStack(alignment: .trailing, spacing: 6) {
            UserBubble(text: draft.text, images: draft.images ?? [], store: store, draft: true)
            HStack(spacing: 10) {
                Label("Draft · not sent", systemImage: "pencil")
                    .scaledFont(.caption)
                    .foregroundStyle(Palette.subtext)
                Button(action: edit) {
                    Text("Edit").scaledFont(.footnote, weight: .medium).foregroundStyle(Palette.subtext)
                }
                .buttonStyle(.borderless)
                .help("Take the draft back into the prompt to change it")
                Button(action: send) {
                    Text("Send").scaledFont(.footnote, weight: .semibold).foregroundStyle(Palette.onAccent)
                }
                .buttonStyle(.borderedProminent)
                .buttonBorderShape(.roundedRectangle(radius: Metrics.Corner.card))
                .controlSize(.small)
                .tint(Palette.accent)
            }
            .disabled(disabled)
        }
        .frame(maxWidth: .infinity, alignment: .trailing)
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
                        .messageRelativeFont(Metrics.toolText, design: .monospaced)
                        .lineLimit(expanded ? nil : 2)
                        .multilineTextAlignment(.leading)
                        .foregroundStyle(.primary)
                    Spacer(minLength: 4)
                    statusView
                }
                .messageRelativeFont(Metrics.toolText)
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
                    .messageRelativeFont(Metrics.toolLabel)
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
        case .pending: Image(systemName: "hand.raised.fill").foregroundStyle(Palette.yellow).imageScale(.small)
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
            Text("Change").messageRelativeFont(Metrics.toolLabel, weight: .semibold).foregroundStyle(.secondary).padding(.bottom, 4)
            ForEach(Array(lines.prefix(60).enumerated()), id: \.offset) { _, line in
                Text(line.text.isEmpty ? " " : line.text)
                    .messageRelativeFont(Metrics.toolText, design: .monospaced)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background((line.added ? Palette.green : Palette.red).opacity(0.15))
            }
            if lines.count > 60 {
                Text("… \(lines.count - 60) more lines").messageRelativeFont(Metrics.toolLabel).foregroundStyle(.secondary)
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
            Text(title).messageRelativeFont(Metrics.toolLabel, weight: .semibold).foregroundStyle(.secondary)
            let lines = text.split(separator: "\n", omittingEmptySubsequences: false)
            let shown = full || lines.count <= 30 ? text : lines.prefix(30).joined(separator: "\n")
            ScrollView(.horizontal, showsIndicators: false) {
                Text(shown)
                    .messageRelativeFont(Metrics.toolText, design: .monospaced)
                    .textSelection(.enabled)
                    .fixedSize(horizontal: true, vertical: false)
            }
            if lines.count > 30 && !full {
                Button("Show all \(lines.count) lines") { full = true }
                    .messageRelativeFont(Metrics.toolLabel)
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
                AgentMarkdown(text: text)
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
    @State private var own: [String: String] = [:]  // answers in the user's own words
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
            // Yellow, for warnings in Catppuccin's style guide; the title in
            // the text colour, since yellow text is faint on Latte.
            Label {
                Text(a.title).foregroundStyle(Palette.text)
            } icon: {
                Image(systemName: a.special == .question ? "questionmark.bubble" : a.special == .plan ? "list.bullet.clipboard" : "hand.raised.fill")
                    .foregroundStyle(Palette.yellow)
            }
            .scaledFont(.subheadline, weight: .semibold)
            if let d = a.detail, !d.isEmpty {
                Text(d).scaledFont(.footnote).foregroundStyle(.secondary)
            }
            if a.special == .plan, let plan = a.planText {
                ScrollView { AgentMarkdown(text: plan) }
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
        // A card like the others, which the border marks out: a tint of
        // yellow over the page would be a grey that isn't in the palette.
        .background(Palette.mantle, in: RoundedRectangle(cornerRadius: Metrics.Corner.bubble))
        .overlay(RoundedRectangle(cornerRadius: Metrics.Corner.bubble).strokeBorder(Palette.yellow))
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
                    // One answer: a label replaces the user's own.
                    own[q.question] = nil
                }
                answers[q.question] = set
            }
            if q.optionsOnly != true { ownAnswer(q) }
        }
    }

    /// The last choice: an answer in the user's own words.
    private func ownAnswer(_ q: Question) -> some View {
        let on = !ownText(q).isEmpty
        let text = Binding {
            own[q.question] ?? ""
        } set: { text in
            own[q.question] = text
            if q.multiSelect != true && !ownText(q).isEmpty { answers[q.question] = [] }
        }
        let placeholder = q.options.isEmpty ? "Your answer…" : "Other…"
        // Centred, as FlowChips' rows: a vertical field's first baseline
        // sits below the circle's on iOS.
        return HStack {
            Image(systemName: on ? "checkmark.circle.fill" : "circle")
            TextField(placeholder, text: text, prompt: Text(placeholder).foregroundStyle(Palette.overlay), axis: .vertical)
                .textFieldStyle(.plain)
                .lineLimit(1...6)
        }
        .scaledFont(.footnote)
        .padding(6)
        .background(on ? Palette.accent.opacity(0.15) : Color.clear, in: RoundedRectangle(cornerRadius: Metrics.Corner.card))
    }

    private func ownText(_ q: Question) -> String {
        q.optionsOnly == true ? "" : (own[q.question] ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// The labels chosen, then the user's own words.
    private func answer(_ q: Question) -> [String] {
        let text = ownText(q)
        return (answers[q.question] ?? []).sorted() + (text.isEmpty ? [] : [text])
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
        (a.questions ?? []).allSatisfy { !answer($0).isEmpty }
    }

    private func respond(_ o: ApprovalOption, message: String?) {
        busy = true
        error = nil
        var flat: [String: String] = [:]
        for q in approval?.questions ?? [] where !answer(q).isEmpty {
            flat[q.question] = answer(q).joined(separator: ", ")
        }
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
