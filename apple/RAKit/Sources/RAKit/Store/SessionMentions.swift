import Foundation

/// A session the @ picker can list.
public protocol MentionCandidate {
    var id: String { get }
    var title: String { get }
    var updatedAt: Date { get }
}

extension Session: MentionCandidate {}

/// Mentions of other sessions in a prompt: typing `@` lists the server's
/// sessions, and picking one writes `@[title](session:<id>)`, which rad tells
/// the agent to read with its read_session tool. Transcripts show a mention
/// as `@title`. The rules are every client's (gpui's `store/mentions.rs`,
/// web's `sessionMentions.ts`, android/core's `SessionMentions`), and their
/// tests share `protocol/vectors/session_mentions.json`.
public enum SessionMentions {
    /// The most sessions the picker lists.
    public static let maxSuggestions = 8
    /// A longer query is prose, not a session's name.
    static let maxQuery = 50

    /// An `@` being typed: where it starts and what follows it, up to the
    /// cursor.
    public struct Query: Equatable, Sendable {
        public var start: String.Index
        public var query: String
    }

    /// The `@` before the cursor, on its line, at the start or after a space,
    /// with no space right after it and not yet a mention.
    public static func mentionQuery(_ text: String, cursor: String.Index) -> Query? {
        guard cursor <= text.endIndex else { return nil }
        let before = text[..<cursor]
        let line = before.utf8.lastIndex(of: UInt8(ascii: "\n")).map { before.utf8.index(after: $0) } ?? before.startIndex
        guard let at = before[line...].utf8.lastIndex(of: UInt8(ascii: "@")) else { return nil }
        if let prev = before[..<at].unicodeScalars.last, !prev.properties.isWhitespace { return nil }
        let query = before[before.utf8.index(after: at)...]
        if let first = query.unicodeScalars.first, first.properties.isWhitespace || first == "[" { return nil }
        if query.unicodeScalars.count > maxQuery { return nil }
        return Query(start: at, query: String(query))
    }

    /// The mention of a session: its title, on one line and without brackets.
    public static func mentionText(title: String, id: String) -> String {
        var scalars = String.UnicodeScalarView()
        for c in title.unicodeScalars {
            switch c {
            case "[": scalars.append("(")
            case "]": scalars.append(")")
            case _ where c.properties.isWhitespace: scalars.append(" ")
            default: scalars.append(c)
            }
        }
        let trimmed = String(scalars).trimmingCharacters(in: .whitespacesAndNewlines)
        return "@[\(trimmed.isEmpty ? "Untitled" : trimmed)](session:\(id))"
    }

    /// The text with the query replaced by the mention and a space, and the
    /// cursor after that space.
    public static func completeMention(_ text: String, cursor: String.Index, query: Query,
                                       title: String, id: String) -> (text: String, cursor: String.Index) {
        let mention = mentionText(title: title, id: id)
        let cursor = min(cursor, text.endIndex)
        let after = text[cursor...]
        let space = after.unicodeScalars.first?.properties.isWhitespace == true ? "" : " "
        let head = String(text[..<query.start]) + mention
        let result = head + space + after
        let end = result.utf8.index(result.startIndex, offsetBy: head.utf8.count)
        // After the space added, or the one that was there.
        return (result, end < result.endIndex ? result.unicodeScalars.index(after: end) : end)
    }

    /// The sessions that match a query, best first: the title starts with it,
    /// a word of the title does, or the title holds it; then the latest. All
    /// but the session itself, archived ones too.
    public static func mentionSuggestions<T: MentionCandidate>(_ query: String, sessions: [T], selfID: String) -> [T] {
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        var ranked: [(rank: Int, updatedAt: Date, index: Int, session: T)] = []
        for (i, s) in sessions.enumerated() {
            if s.id == selfID { continue }
            guard let r = rank(s.title.lowercased(), q) else { continue }
            ranked.append((r, s.updatedAt, i, s))
        }
        ranked.sort { a, b in
            if a.rank != b.rank { return a.rank < b.rank }
            if a.updatedAt != b.updatedAt { return a.updatedAt > b.updatedAt }
            return a.index < b.index
        }
        return ranked.prefix(maxSuggestions).map(\.session)
    }

    private static func rank(_ title: String, _ q: String) -> Int? {
        let t = title.unicodeScalars, qs = q.unicodeScalars
        if qs.isEmpty || t.starts(with: qs) { return 0 }
        var prev: Unicode.Scalar?
        var contains = false
        var i = t.startIndex
        while i < t.endIndex {
            if t[i...].starts(with: qs) {
                if let prev, !isAlphanumeric(prev) { return 1 }
                contains = true
            }
            prev = t[i]
            i = t.index(after: i)
        }
        return contains ? 2 : nil
    }

    private static func isAlphanumeric(_ c: Unicode.Scalar) -> Bool {
        c.properties.isAlphabetic || c.properties.numericType != nil
    }

    /// A mention in a text.
    public struct Mention: Equatable, Sendable {
        public var range: Range<String.Index>
        public var title: String
        public var id: String
    }

    /// The mentions in a text, in order.
    public static func mentions(_ text: String) -> [Mention] {
        let bytes = Array(text.utf8)
        let n = bytes.count
        let marker = Array("](session:".utf8)
        func index(_ offset: Int) -> String.Index { text.utf8.index(text.startIndex, offsetBy: offset) }
        func string(_ from: Int, _ to: Int) -> String { String(decoding: bytes[from..<to], as: UTF8.self) }
        func isID(_ b: UInt8) -> Bool {
            (b >= 0x30 && b <= 0x39) || (b >= 0x41 && b <= 0x5A) || (b >= 0x61 && b <= 0x7A) || b == UInt8(ascii: "-")
        }

        var out: [Mention] = []
        var from = 0
        while from + 1 < n {
            // The next "@[".
            var start = from
            while start + 1 < n, !(bytes[start] == UInt8(ascii: "@") && bytes[start + 1] == UInt8(ascii: "[")) { start += 1 }
            guard start + 1 < n else { break }
            from = start + 2
            var close = from
            while close < n, ![UInt8(ascii: "["), UInt8(ascii: "]"), UInt8(ascii: "\n")].contains(bytes[close]) { close += 1 }
            guard close < n else { break }
            guard bytes[close...].starts(with: marker) else { continue }
            let idFrom = close + marker.count
            var idEnd = idFrom
            while idEnd < n, isID(bytes[idEnd]) { idEnd += 1 }
            guard idEnd > idFrom, idEnd < n, bytes[idEnd] == UInt8(ascii: ")") else { continue }
            let end = idEnd + 1
            out.append(Mention(range: index(start)..<index(end), title: string(from, close), id: string(idFrom, idEnd)))
            from = end
        }
        return out
    }

    /// A text as transcripts show it: each mention as `@title`.
    public static func mentionDisplay(_ text: String) -> String {
        let list = mentions(text)
        guard !list.isEmpty else { return text }
        var out = ""
        var last = text.startIndex
        for m in list {
            out += text[last..<m.range.lowerBound]
            out += "@" + m.title
            last = m.range.upperBound
        }
        out += text[last...]
        return out
    }
}
