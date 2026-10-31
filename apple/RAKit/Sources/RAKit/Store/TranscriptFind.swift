import Foundation

/// Find in a session's transcript: the text of each item that find (and rad's
/// `session.search`) looks in, and the items that hold a query.
public enum TranscriptFind {
    /// The text of an item that find looks in, as rad's `model.SearchText`:
    /// prompts, replies, notices and errors, a tool call's title and command,
    /// a plan, and a proposed plan's or a question's text. Reasoning, tool
    /// output, approvals of tool calls (their tool call has the same title)
    /// and sub-agents' items, which the transcript folds away, aren't searched.
    public static func searchText(_ item: Item) -> String {
        if let parent = item.parentItemId, !parent.isEmpty { return "" }
        switch item.kind {
        case .userMessage, .assistantMessage, .notice, .error:
            return item.text ?? ""
        case .toolCall:
            guard let tool = item.tool else { return "" }
            var s = tool.title ?? ""
            let command = toolCommand(tool.input)
            if !command.isEmpty && !s.contains(command) {
                s += "\n" + command
            }
            return s
        case .plan:
            guard let plan = item.plan else { return "" }
            var parts = (plan.entries ?? []).map(\.content)
            if let text = plan.text, !text.isEmpty { parts.append(text) }
            return parts.joined(separator: "\n")
        case .approval:
            guard let approval = item.approval else { return "" }
            switch approval.special {
            case .plan: return approval.planText ?? ""
            case .question: return (approval.questions ?? []).map(\.question).joined(separator: "\n")
            case nil: return ""
            }
        case .reasoning, .unknown:
            return ""
        }
    }

    /// The command a tool call ran, from its input's "command": a string, or
    /// a list of arguments, which it joins with spaces. "" without one.
    public static func toolCommand(_ input: JSONValue?) -> String {
        switch input?["command"] {
        case .string(let s):
            return s
        case .array(let args):
            let strings = args.compactMap(\.stringValue)
            return strings.count == args.count ? strings.joined(separator: " ") : ""
        default:
            return ""
        }
    }

    /// The ids of the items whose text holds the query, ignoring case, in the
    /// order given (the transcript's). The query is trimmed and taken whole,
    /// spaces and all; an empty one matches nothing.
    public static func matches(_ items: [Item], query: String) -> [String] {
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !q.isEmpty else { return [] }
        return items.filter { contains(searchText($0), q) }.map(\.id)
    }

    /// Whether `text` holds `query`, ignoring case.
    public static func contains(_ text: String, _ query: String) -> Bool {
        !query.isEmpty && text.range(of: query, options: .caseInsensitive) != nil
    }
}
