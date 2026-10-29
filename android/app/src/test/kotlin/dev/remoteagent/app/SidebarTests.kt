package dev.remoteagent.app

import dev.remoteagent.app.ui.RecentSessions
import dev.remoteagent.app.ui.ServerSession
import dev.remoteagent.app.ui.ServerState
import dev.remoteagent.app.ui.SessionCollection
import dev.remoteagent.app.ui.SessionKey
import dev.remoteagent.app.ui.SidebarLayout
import dev.remoteagent.app.ui.inboxGroups
import dev.remoteagent.app.ui.mostPressing
import dev.remoteagent.app.ui.projectGroups
import dev.remoteagent.app.ui.sessionActivity
import dev.remoteagent.app.ui.sessions
import dev.remoteagent.app.ui.shortAge
import dev.remoteagent.app.ui.standsOut
import dev.remoteagent.app.ui.toggled
import dev.remoteagent.app.ui.urgency
import dev.remoteagent.core.protocol.BackgroundTask
import dev.remoteagent.core.protocol.ContextUsage
import dev.remoteagent.core.protocol.HarnessInfo
import dev.remoteagent.core.protocol.ModelInfo
import dev.remoteagent.core.protocol.Project
import dev.remoteagent.core.protocol.Session
import dev.remoteagent.core.protocol.SessionStatus
import dev.remoteagent.core.protocol.Workspace
import dev.remoteagent.core.store.ConnectionState
import dev.remoteagent.core.store.ImageCache
import dev.remoteagent.core.store.SavedServer
import dev.remoteagent.core.store.ServerConnection
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.cancel
import java.time.Instant
import java.time.ZoneOffset
import java.util.Locale
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull

/** The tablet sidebars' grouping, collections and short ages. */
class SidebarTests {
    private val scope = CoroutineScope(Job())
    private val t0: Instant = Instant.parse("2026-10-15T12:00:00Z")

    @AfterTest
    fun tearDown() = scope.cancel()

    private fun session(
        id: String,
        minutesAgo: Long,
        status: SessionStatus = SessionStatus.IDLE,
        project: String = "p1",
        archived: Boolean = false,
        background: Boolean = false,
        unread: Boolean = false,
    ) = Session(
        id = id, projectId = project, harness = "fake", workspace = Workspace(Workspace.Kind.ROOT, "/repo"), status = status,
        title = id, archived = archived, unread = unread, createdAt = t0, updatedAt = t0.minusSeconds(minutesAgo * 60),
        backgroundTasks = if (background) listOf(BackgroundTask("t", BackgroundTask.Kind.COMMAND, "Wait")) else emptyList(),
    )

    /** A server that is never connected: only its state matters here. */
    private fun server(id: String, vararg sessions: Session, projects: List<String> = listOf("p1", "p2")): ServerState {
        val saved = SavedServer(id, id, listOf("http://127.0.0.1:1"), "d", t0)
        return ServerState(
            ServerConnection(saved, "", scope, ImageCache(null), {}), saved, ConnectionState.Connected, true,
            sessions.associateBy { it.id }, projects.associateWith { Project(it, "/$it", "name-$it", createdAt = t0) }, null,
        )
    }

    private val servers by lazy {
        listOf(
            server(
                "a",
                session("approval", 5, SessionStatus.AWAITING_APPROVAL),
                session("error", 50, SessionStatus.ERROR, project = "p2"),
                session("running", 1, SessionStatus.RUNNING, project = "p2"),
                session("background", 30, background = true),
                session("idle", 10),
                session("old", 100, archived = true),
            ),
            server("b", session("stopped", 3, SessionStatus.STOPPED), session("b-idle", 20, project = "p2")),
        )
    }

    private fun List<ServerSession>.ids() = map { it.session.id }

    @Test
    fun sessionsMixServersLatestFirst() {
        assertEquals(listOf("running", "stopped", "approval", "idle", "b-idle", "background", "error"), servers.sessions(archived = false).ids())
        assertEquals(listOf("old"), servers.sessions(archived = true).ids())
    }

    @Test
    fun inboxGroupsByState() {
        val groups = inboxGroups(servers.sessions(archived = false))
        assertEquals(listOf("approval", "error"), groups.needsYou.ids())
        // Work goes with the rest.
        assertEquals(listOf("running", "stopped", "idle", "b-idle", "background"), groups.recent.ids())
    }

    @Test
    fun inboxPutsUnreadSessionsFirstAndKeepsTheOpenOneWhereItWas() {
        val unread = session("unread", 2, unread = true)
        val active = listOf(server("a", session("running", 1, SessionStatus.RUNNING), unread, session("approval", 3, SessionStatus.AWAITING_APPROVAL)))
            .sessions(archived = false)
        assertEquals(listOf("unread", "approval"), inboxGroups(active).needsYou.ids())
        // Read where it shows, it stays; a turn that ends in view doesn't move it up.
        val read = listOf(server("a", unread.copy(unread = false))).sessions(archived = false)
        assertEquals(listOf("unread"), inboxGroups(read, SessionKey("a", "unread"), openedStoodOut = true).needsYou.ids())
        assertEquals(listOf("approval"), inboxGroups(active, SessionKey("a", "unread"), openedStoodOut = false).needsYou.ids())
        // One that comes to need you moves up.
        assertEquals(listOf("unread", "approval"), inboxGroups(active, SessionKey("a", "approval"), openedStoodOut = false).needsYou.ids())
    }

    @Test
    fun projectsComeByTheirLatestSessionWithWhatNeedsYouFirst() {
        val groups = projectGroups(servers[0])
        assertEquals(listOf("p2", "p1"), groups.map { it.projectId })
        assertEquals(listOf("name-p2", "name-p1"), groups.map { it.name })
        assertEquals(listOf("error", "running"), groups[0].items.ids())
        assertEquals(listOf("approval", "idle", "background"), groups[1].items.ids())
        // A project the index doesn't have yet.
        assertEquals("Project", projectGroups(server("c", session("x", 1, project = "gone"), projects = emptyList())).single().name)
    }

    @Test
    fun projectsWithoutSessionsComeLastByName() {
        val groups = projectGroups(server("d", session("x", 1, project = "p2"), projects = listOf("p3", "p2", "p1")))
        assertEquals(listOf("p2", "p1", "p3"), groups.map { it.projectId })
        assertEquals(listOf(listOf("x"), emptyList(), emptyList()), groups.map { it.items.ids() })
    }

    @Test
    fun projectsShowTheirMostPressingSession() {
        val a = projectGroups(servers[0]).associate { it.projectId to it.items.map { i -> i.session }.mostPressing()?.id }
        assertEquals(mapOf("p2" to "error", "p1" to "approval"), a)
        // Stopped and idle sessions ask nothing.
        assertEquals(listOf(null, null), projectGroups(servers[1]).map { g -> g.items.map { it.session }.mostPressing() })
        assertEquals("run", listOf(session("bg", 1, background = true), session("run", 2, SessionStatus.RUNNING)).mostPressing()?.id)
        assertEquals("bg", listOf(session("idle", 1), session("bg", 2, background = true)).mostPressing()?.id)
        assertNull(emptyList<Session>().mostPressing())
    }

    @Test
    fun unreadSessionsAskForALookAfterWhatNeedsYou() {
        val unread = session("unread", 1, unread = true)
        assertEquals("unread", listOf(session("run", 1, SessionStatus.RUNNING), unread, session("bg", 1, background = true)).mostPressing()?.id)
        assertEquals("error", listOf(unread, session("error", 1, SessionStatus.ERROR, unread = true)).mostPressing()?.id)
        assertEquals("approval", listOf(unread, session("approval", 1, SessionStatus.AWAITING_APPROVAL)).mostPressing()?.id)
        // Checked in this order, so a running session that is also unread counts as unread.
        val ranked = listOf(
            session("a", 1, SessionStatus.AWAITING_APPROVAL, unread = true), session("e", 1, SessionStatus.ERROR), unread,
            session("ru", 1, SessionStatus.RUNNING, unread = true), session("su", 1, SessionStatus.STOPPED, unread = true),
            session("r", 1, SessionStatus.RUNNING), session("bg", 1, background = true), session("i", 1),
        )
        assertEquals(listOf(5, 4, 3, 3, 3, 2, 1, 0), ranked.map { it.urgency })
        assertEquals(listOf(true, true, false), listOf(unread, session("e", 1, SessionStatus.ERROR), session("i", 1)).map { it.standsOut })
    }

    @Test
    fun collectionsFilterEveryServersSessions() {
        assertEquals(listOf("approval", "error"), SessionCollection.NeedsYou.sessions(servers).ids())
        assertEquals(listOf("running", "background"), SessionCollection.Running.sessions(servers).ids())
        assertEquals(
            listOf("approval", "error", "running", "stopped", "idle", "b-idle", "background"),
            SessionCollection.All.sessions(servers).ids(),
        )
        assertEquals(listOf("old"), SessionCollection.Archived.sessions(servers).ids())
        assertEquals(listOf("stopped"), SessionCollection.Project("b", "p1").sessions(servers).ids())
        assertEquals(SessionKey("b", "stopped"), SessionCollection.Project("b", "p1").sessions(servers).single().key)
        assertEquals("name-p2", SessionCollection.Project("a", "p2").title(servers))
        assertEquals("Project", SessionCollection.Project("z", "p2").title(servers))
        assertEquals("All Sessions", SessionCollection.All.title(servers))
    }

    @Test
    fun sessionsShowInTheirProjectOrTheArchive() {
        val a = servers[0]
        assertEquals(SessionCollection.Project("a", "p2"), ServerSession(a, a.sessions.getValue("error")).collection)
        assertEquals(SessionCollection.Archived, ServerSession(a, a.sessions.getValue("old")).collection)
        assertEquals(SessionCollection.All, ServerSession(a, session("lost", 1, project = "forgotten")).collection)
    }

    @Test
    fun choosingACollectionOpensItsSessionOpenedLast() {
        val p2 = SessionCollection.Project("a", "p2")
        assertEquals(SessionKey("a", "running"), p2.sessionToOpen(servers, emptyList()))
        assertEquals(SessionKey("a", "error"), p2.sessionToOpen(servers, listOf(SessionKey("b", "stopped"), SessionKey("a", "error"))))
        assertEquals(SessionKey("a", "running"), p2.sessionToOpen(servers, listOf(SessionKey("a", "gone"))))
        assertNull(SessionCollection.Project("a", "p3").sessionToOpen(servers, listOf(SessionKey("a", "idle"))))
    }

    @Test
    fun recentSessionsKeepTheLatestFirst() {
        var stored = RecentSessions.adding(SessionKey("s", "a"), "")
        stored = RecentSessions.adding(SessionKey("s", "b"), stored)
        stored = RecentSessions.adding(SessionKey("s", "a"), stored)
        assertEquals(listOf(SessionKey("s", "a"), SessionKey("s", "b")), RecentSessions.keys(stored))
        repeat(150) { stored = RecentSessions.adding(SessionKey("s", "$it"), stored) }
        assertEquals(RecentSessions.LIMIT, RecentSessions.keys(stored).size)
        assertEquals(SessionKey("s", "149"), RecentSessions.keys(stored).first())
        assertEquals(emptyList(), RecentSessions.keys(""))
    }

    @Test
    fun collectionsAreStoredAsText() {
        val all = listOf(
            SessionCollection.NeedsYou, SessionCollection.Running, SessionCollection.All, SessionCollection.Archived,
            SessionCollection.Project("srv", "prj"),
        )
        for (c in all) assertEquals(c, SessionCollection.parse(c.stored))
        assertEquals("needsYou", SessionCollection.NeedsYou.stored)
        assertEquals("project srv prj", SessionCollection.Project("srv", "prj").stored)
        assertNull(SessionCollection.parse(""))
        assertNull(SessionCollection.parse("project srv"))
        assertNull(SessionCollection.parse("inbox"))
    }

    @Test
    fun layoutsAreStoredAsText() {
        assertEquals(SidebarLayout.COLUMNS, SidebarLayout.parse("columns"))
        assertEquals(SidebarLayout.PROJECTS, SidebarLayout.parse("projects"))
        assertEquals(SidebarLayout.INBOX, SidebarLayout.parse(""))
        assertEquals(SidebarLayout.INBOX, SidebarLayout.parse("COLUMNS"))
    }

    @Test
    fun shortAges() {
        fun age(seconds: Long) = shortAge(t0.minusSeconds(seconds), t0, ZoneOffset.UTC, locale = Locale.US)
        assertEquals("now", age(0))
        assertEquals("now", age(59))
        assertEquals("now", age(-30))
        assertEquals("1m", age(60))
        assertEquals("59m", age(3599))
        assertEquals("3h", age(3 * 3600 + 59))
        assertEquals("23h", age(86_399))
        assertEquals("2d", age(2 * 86_400 + 5))
        assertEquals("6d", age(7 * 86_400 - 1))
        assertEquals("Oct 8", age(7 * 86_400))
        assertEquals("Oct 3", shortAge(Instant.parse("2025-10-03T09:00:00Z"), t0, ZoneOffset.UTC, locale = Locale.US))
    }

    @Test
    fun hiddenIdsAreToggled() {
        assertEquals("a", toggled("", "a", present = true))
        assertEquals("b a", toggled("a b", "a", present = true))
        assertEquals("b", toggled("a b", "a", present = false))
        assertEquals("", toggled("", "a", present = false))
    }

    @Test
    fun activitySaysWhatTheAgentIsDoing() {
        val s = session("s", 1)
        assertEquals("Waiting for your answer", sessionActivity(s.copy(status = SessionStatus.AWAITING_APPROVAL), null))
        assertEquals("Failed", sessionActivity(s.copy(status = SessionStatus.ERROR), null))
        assertEquals("exit 1", sessionActivity(s.copy(status = SessionStatus.ERROR, error = "exit 1"), null))
        assertEquals("Working…", sessionActivity(s.copy(status = SessionStatus.RUNNING), null))
        assertEquals("Stopped", sessionActivity(s.copy(status = SessionStatus.STOPPED), null))
        assertEquals("In the background: Wait", sessionActivity(session("bg", 1, background = true), null))
        assertEquals("fake", sessionActivity(s, null))
        val echo = HarnessInfo(id = "fake", name = "Echo", protocol = "fake", installed = true, authOk = true)
        assertEquals("Echo · 15% context", sessionActivity(s.copy(context = ContextUsage(150, 1000)), echo))
        assertEquals("Opus", sessionActivity(s.copy(modelInfo = ModelInfo(name = "Opus")), echo))
    }
}
