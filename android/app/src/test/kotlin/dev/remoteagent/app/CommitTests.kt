package dev.remoteagent.app

import dev.remoteagent.app.ui.CommitScope
import dev.remoteagent.app.ui.commitTarget
import dev.remoteagent.core.protocol.CommitPreview
import dev.remoteagent.core.protocol.FileStat
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/** Commit's choices, its button and where it goes. */
class CommitTests {
    private val file = FileStat("a.txt", status = FileStat.Status.MODIFIED, additions = 1)
    private val changes = CommitPreview(branch = "ra/x", files = listOf(file), message = "Fix", remote = "origin", base = "main")
    private val local = changes.copy(remote = null)
    private val pushed = changes.copy(files = emptyList(), message = "", ahead = 2)

    @Test
    fun choicesNeedChangesOrARemote() {
        assertTrue(CommitScope.entries.all { it.enabled(changes) })
        assertEquals(listOf(true, false, false), CommitScope.entries.map { it.enabled(local) })
        assertEquals(listOf(false, true, true), CommitScope.entries.map { it.enabled(pushed) })
    }

    @Test
    fun startsAtTheLastChoiceThenAPush() {
        assertEquals(CommitScope.PUSH, CommitScope.initial(null, changes))
        assertEquals(CommitScope.PULL_REQUEST, CommitScope.initial("PULL_REQUEST", changes))
        assertEquals(CommitScope.COMMIT, CommitScope.initial("COMMIT", changes))
        // What the preview doesn't allow gives way.
        assertEquals(CommitScope.COMMIT, CommitScope.initial("PULL_REQUEST", local))
        assertEquals(CommitScope.PUSH, CommitScope.initial("COMMIT", pushed))
        assertEquals(CommitScope.COMMIT, CommitScope.initial("bogus", local))
        // Nothing to commit and nowhere to push: the button is off.
        val nothing = local.copy(files = emptyList())
        assertEquals(CommitScope.COMMIT, CommitScope.initial(null, nothing))
        assertFalse(CommitScope.COMMIT.enabled(nothing))
    }

    @Test
    fun buttonSaysWhatItDoes() {
        assertEquals(listOf("Commit", "Commit & Push", "Commit, Push & Open PR"), CommitScope.entries.map { it.action(true) })
        assertEquals(listOf("Push", "Push & Open PR"), CommitScope.entries.drop(1).map { it.action(false) })
        assertEquals(listOf("Commit only", "Commit & push", "Commit, push & open PR"), CommitScope.entries.map { it.label })
    }

    @Test
    fun target() {
        assertEquals("ra/x → origin", commitTarget(changes))
        assertEquals("ra/x", commitTarget(local))
        assertEquals("Detached HEAD", commitTarget(changes.copy(branch = null)))
    }
}
