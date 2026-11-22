import Foundation
import Testing
@testable import RAKit

/// The vectors every client's @ session mentions share
/// (protocol/vectors/session_mentions.json): gpui's `store/mentions.rs`,
/// web/src/store/sessionMentions.ts and android/core's tests.
struct SessionMentionsTests {
    struct Vectors: Decodable {
        struct S: Decodable, MentionCandidate {
            var id: String
            var title: String
            var updatedAt: Date
        }
        struct QueryCase: Decodable { var text: String; var query: String? }
        struct SuggestCase: Decodable { var query: String; var ids: [String] }
        struct CompleteCase: Decodable { var text: String; var id: String; var result: String }
        struct TextCase: Decodable { var title: String; var id: String; var text: String }
        struct DisplayCase: Decodable { var text: String; var display: String }

        var selfID: String
        var sessions: [S]
        var query: [QueryCase]
        var suggestions: [SuggestCase]
        var complete: [CompleteCase]
        var mentionText: [TextCase]
        var display: [DisplayCase]
        enum CodingKeys: String, CodingKey {
            case selfID = "self", sessions, query, suggestions, complete, mentionText, display
        }
    }

    func load() throws -> Vectors {
        let url = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
            .appending(path: "../../../../protocol/vectors/session_mentions.json").standardized
        return try WireCoding.decoder().decode(Vectors.self, from: Data(contentsOf: url))
    }

    /// The text without its `|`, and the cursor where it was.
    func cursor(_ marked: String) -> (String, String.Index) {
        let offset = marked.utf8.distance(from: marked.startIndex, to: marked.firstIndex(of: "|")!)
        let text = marked.replacingOccurrences(of: "|", with: "")
        return (text, text.utf8.index(text.startIndex, offsetBy: offset))
    }

    @Test func queries() throws {
        let v = try load()
        #expect(v.query.count >= 10)
        for c in v.query {
            let (text, at) = cursor(c.text)
            #expect(SessionMentions.mentionQuery(text, cursor: at)?.query == c.query, "\(c.text)")
        }
    }

    @Test func suggestions() throws {
        let v = try load()
        #expect(!v.suggestions.isEmpty)
        for c in v.suggestions {
            let ids = SessionMentions.mentionSuggestions(c.query, sessions: v.sessions, selfID: v.selfID).map(\.id)
            #expect(ids == c.ids, "\(c.query)")
        }
    }

    @Test func completes() throws {
        let v = try load()
        #expect(!v.complete.isEmpty)
        for c in v.complete {
            let (text, at) = cursor(c.text)
            let q = try #require(SessionMentions.mentionQuery(text, cursor: at))
            let s = try #require(v.sessions.first { $0.id == c.id })
            let (result, end) = SessionMentions.completeMention(text, cursor: at, query: q, title: s.title, id: s.id)
            #expect(String(result[..<end]) + "|" + String(result[end...]) == c.result)
        }
    }

    @Test func mentionTexts() throws {
        let v = try load()
        #expect(!v.mentionText.isEmpty)
        for c in v.mentionText {
            #expect(SessionMentions.mentionText(title: c.title, id: c.id) == c.text)
        }
    }

    @Test func displays() throws {
        let v = try load()
        #expect(!v.display.isEmpty)
        for c in v.display {
            #expect(SessionMentions.mentionDisplay(c.text) == c.display, "\(c.text)")
        }
    }

    @Test func findsMentions() {
        let text = "a @[Fix](session:s-1) b @[x](other:1) @[y](session:) @[Ünï](session:s-2)"
        let m = SessionMentions.mentions(text)
        #expect(m.count == 2)
        #expect(String(text[m[0].range]) == "@[Fix](session:s-1)")
        #expect(m[1].title == "Ünï" && m[1].id == "s-2")
    }

    @Test func findSearchesMentionsAsShown() {
        var item = Item(id: "u", sessionId: "s", order: 1, kind: .userMessage, status: .completed)
        item.text = "see @[Fix the login bug](session:s-login)"
        #expect(TranscriptFind.searchText(item) == "see @Fix the login bug")
        #expect(TranscriptFind.matches([item], query: "session:").isEmpty)
    }
}
