package dev.remoteagent.app

import dev.remoteagent.app.ui.occurrences
import dev.remoteagent.app.ui.searchResultDetail
import kotlin.test.Test
import kotlin.test.assertEquals

/** A search result's lines, and where find marks its query in an item's text. */
class SearchTests {
    @Test
    fun aResultSaysWhereItIsAndItsServerWhenThereAreSeveral() {
        assertEquals("app · Claude Code · 4m", searchResultDetail("app", "Claude Code", "4m", null))
        assertEquals("app · Claude Code · 4m · Mac Studio", searchResultDetail("app", "Claude Code", "4m", "Mac Studio"))
        // A project the index doesn't have yet is left out.
        assertEquals("codex · Oct 3", searchResultDetail(null, "codex", "Oct 3", null))
    }

    @Test
    fun findMarksEveryOccurrenceInAnyCase() {
        assertEquals(listOf(4..8, 20..24), occurrences("Fix tests, then run TESTS", "tests"))
        // The query as the find bar has it, trimmed, spaces inside kept.
        assertEquals(listOf(0..6), occurrences("go test ./...", " go test "))
        // Next to each other, and none.
        assertEquals(listOf(0..1, 2..3), occurrences("aaaa", "aa"))
        assertEquals(emptyList(), occurrences("Fix tests", "lint"))
        assertEquals(emptyList(), occurrences("Fix tests", "  "))
    }
}
