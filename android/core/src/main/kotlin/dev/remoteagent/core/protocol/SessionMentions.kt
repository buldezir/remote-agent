package dev.remoteagent.core.protocol

import java.time.Instant

/*
 * Mentions of other sessions in a prompt: typing `@` lists the server's
 * sessions, and picking one writes `@[title](session:<id>)`, which rad tells
 * the agent to read with its read_session tool. Transcripts show a mention
 * as `@title`. The rules are every client's (gpui's store/mentions.rs,
 * RAKit's and the web's SessionMentions), and their tests share
 * protocol/vectors/session_mentions.json.
 */

/** The most sessions the picker lists. */
const val MAX_MENTION_SUGGESTIONS = 8

/** A longer query is prose, not a session's name. */
private const val MAX_MENTION_QUERY = 50

/** An `@` being typed: where it starts (a string index) and what follows it, up to the cursor. */
data class MentionQuery(val start: Int, val query: String)

/** The `@` before the cursor, on its line, at the start or after a space, with no space right after it and not yet a mention. */
fun mentionQuery(text: String, cursor: Int): MentionQuery? {
    if (cursor < 0 || cursor > text.length) return null
    val before = text.substring(0, cursor)
    val line = before.lastIndexOf('\n') + 1
    val at = before.lastIndexOf('@').takeIf { it >= line } ?: return null
    if (at > 0 && !before[at - 1].isWhitespace()) return null
    val query = before.substring(at + 1)
    if (query.firstOrNull()?.let { it.isWhitespace() || it == '[' } == true) return null
    if (query.codePointCount(0, query.length) > MAX_MENTION_QUERY) return null
    return MentionQuery(at, query)
}

/** The mention of a session: its title, on one line and without brackets. */
fun mentionText(title: String, id: String): String {
    val clean = buildString {
        for (c in title) append(
            when {
                c == '[' -> '('
                c == ']' -> ')'
                c.isWhitespace() -> ' '
                else -> c
            },
        )
    }.trim()
    return "@[${clean.ifEmpty { "Untitled" }}](session:$id)"
}

/** A completed mention: the new text, and the cursor after the space that follows the mention. */
data class CompletedMention(val text: String, val cursor: Int)

/** The text with the query replaced by the mention and a space, and the cursor after that space. */
fun completeMention(text: String, cursor: Int, query: MentionQuery, title: String, id: String): CompletedMention {
    val mention = mentionText(title, id)
    val after = text.substring(cursor)
    val space = if (after.firstOrNull()?.isWhitespace() == true) "" else " "
    return CompletedMention(text.substring(0, query.start) + mention + space + after, query.start + mention.length + 1)
}

/**
 * The sessions that match a query, best first: the title starts with it, a
 * word of the title does, or the title holds it; then the latest. All but
 * the session itself, archived ones too.
 */
fun mentionSuggestions(query: String, sessions: List<Session>, selfId: String): List<Session> =
    mentionSuggestions(query, sessions, selfId, { it.id }, { it.title }, { it.updatedAt })

/** [mentionSuggestions] for any kind of session, through its id, title and last change. */
fun <T> mentionSuggestions(
    query: String,
    sessions: List<T>,
    selfId: String,
    id: (T) -> String,
    title: (T) -> String,
    updatedAt: (T) -> Instant,
): List<T> {
    val q = query.trim().lowercase()
    data class Ranked<T>(val rank: Int, val updatedAt: Instant, val index: Int, val session: T)
    val ranked = sessions.mapIndexedNotNull { i, s ->
        if (id(s) == selfId) return@mapIndexedNotNull null
        val rank = mentionRank(title(s).lowercase(), q) ?: return@mapIndexedNotNull null
        Ranked(rank, updatedAt(s), i, s)
    }
    return ranked
        .sortedWith(compareBy<Ranked<T>> { it.rank }.thenByDescending { it.updatedAt }.thenBy { it.index })
        .take(MAX_MENTION_SUGGESTIONS)
        .map { it.session }
}

private fun mentionRank(title: String, q: String): Int? {
    if (q.isEmpty() || title.startsWith(q)) return 0
    var contains = false
    var i = 0
    while (i < title.length) {
        if (title.startsWith(q, i)) {
            if (i > 0) {
                val prev = title.codePointBefore(i)
                if (!Character.isLetterOrDigit(prev)) return 1
            }
            contains = true
        }
        i += Character.charCount(title.codePointAt(i))
    }
    return if (contains) 2 else null
}

/** A mention in a text: its range in the text, the title it shows and the session's id. */
data class SessionMention(val range: IntRange, val title: String, val id: String)

private const val MENTION_LINK = "](session:"

/** The mentions in a text, in order. */
fun mentions(text: String): List<SessionMention> {
    val out = mutableListOf<SessionMention>()
    var from = 0
    while (true) {
        val start = text.indexOf("@[", from)
        if (start < 0) break
        from = start + 2
        val close = (from until text.length).firstOrNull { text[it] == '[' || text[it] == ']' || text[it] == '\n' } ?: break
        if (!text.startsWith(MENTION_LINK, close)) continue
        val idFrom = close + MENTION_LINK.length
        var idEnd = idFrom
        while (idEnd < text.length && (text[idEnd].isAsciiLetterOrDigit() || text[idEnd] == '-')) idEnd++
        if (idEnd == idFrom || !text.startsWith(")", idEnd)) continue
        val end = idEnd + 1
        out += SessionMention(start until end, text.substring(from, close), text.substring(idFrom, idEnd))
        from = end
    }
    return out
}

private fun Char.isAsciiLetterOrDigit() = this in 'a'..'z' || this in 'A'..'Z' || this in '0'..'9'

/** A text as transcripts show it: each mention as `@title`. */
fun mentionDisplay(text: String): String {
    if (!text.contains(MENTION_LINK)) return text
    val list = mentions(text)
    if (list.isEmpty()) return text
    return buildString {
        var last = 0
        for (m in list) {
            append(text, last, m.range.first)
            append('@').append(m.title)
            last = m.range.last + 1
        }
        append(text, last, text.length)
    }
}
