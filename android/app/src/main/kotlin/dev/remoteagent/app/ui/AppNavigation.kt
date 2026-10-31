package dev.remoteagent.app.ui

import android.Manifest
import android.content.pm.PackageManager
import android.os.Build
import androidx.activity.compose.LocalActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.adaptive.ExperimentalMaterial3AdaptiveApi
import androidx.compose.material3.adaptive.currentWindowAdaptiveInfoV2
import androidx.compose.material3.adaptive.layout.calculatePaneScaffoldDirective
import androidx.compose.material3.adaptive.navigation3.ListDetailSceneStrategy
import androidx.compose.material3.adaptive.navigation3.rememberListDetailSceneStrategy
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.saveable.rememberSaveableStateHolder
import androidx.compose.runtime.setValue
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.ExperimentalComposeUiApi
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.testTagsAsResourceId
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.navigation3.runtime.NavBackStack
import androidx.navigation3.runtime.NavKey
import androidx.navigation3.runtime.entryProvider
import androidx.navigation3.runtime.rememberNavBackStack
import androidx.navigation3.ui.NavDisplay
import dev.remoteagent.app.RemoteAgentApp
import dev.remoteagent.app.platform.AppSettings
import dev.remoteagent.app.platform.state
import dev.remoteagent.core.net.PairingLink
import dev.remoteagent.core.store.SavedServer
import dev.remoteagent.core.store.ServerConnection
import dev.remoteagent.core.store.ServerStore
import dev.remoteagent.core.store.SessionStore
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.Serializable

val LocalStore = staticCompositionLocalOf<ServerStore> { error("no ServerStore") }
val LocalSettings = staticCompositionLocalOf<AppSettings> { error("no AppSettings") }

// Destinations, registered once at the root with values that carry the
// server's id (AGENTS.md), so a screen never opens on the wrong server.
@Serializable
data object ServersKey : NavKey

/** A phone's Inbox tab: every server's sessions by state. */
@Serializable
data object InboxKey : NavKey

@Serializable
data class ServerHomeKey(val serverId: String) : NavKey

@Serializable
data class SessionKey(val serverId: String, val sessionId: String) : NavKey

@Serializable
data object SettingsKey : NavKey

@OptIn(ExperimentalComposeUiApi::class)
@Composable
fun App(app: RemoteAgentApp, pendingLink: MutableStateFlow<PairingLink?>) {
    val context = LocalContext.current
    var networkReady by rememberSaveable {
        mutableStateOf(
            Build.VERSION.SDK_INT < 37 ||
                context.checkSelfPermission(Manifest.permission.ACCESS_LOCAL_NETWORK) == PackageManager.PERMISSION_GRANTED,
        )
    }
    var networkDenied by rememberSaveable { mutableStateOf(false) }
    val networkPermission = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        networkDenied = !granted
        networkReady = true
    }
    LaunchedEffect(Unit) {
        if (!networkReady) networkPermission.launch(Manifest.permission.ACCESS_LOCAL_NETWORK)
    }
    CompositionLocalProvider(LocalStore provides app.store, LocalSettings provides app.settings) {
        val settings = app.settings
        val follows by settings.state(AppSettings.Keys.interfaceFollowsSystem, true)
        val step by settings.state(AppSettings.Keys.interfaceTextSize, TextSize.DEFAULT_INTERFACE)
        val messages by settings.state(AppSettings.Keys.messageTextSize, TextSize.DEFAULT_MESSAGES)
        TextSettings(follows, step, messages) {
            RemoteAgentTheme {
                // Test tags become resource ids, so `uiautomator dump` (android/e2e) can find views.
                Box(Modifier.fillMaxSize().background(palette.base).semantics { testTagsAsResourceId = true }) {
                    // Wait for the permission result before pairing or starting saved connections.
                    if (networkReady) AppNavigation(pendingLink)
                    ErrorDialog(
                        if (networkDenied) "To connect to servers over Wi-Fi, allow Nearby devices in Settings > Apps > Remote Agent > Permissions. You can continue using Tailscale." else null,
                        title = "Local network access denied",
                        onDismiss = { networkDenied = false },
                    )
                }
            }
        }
    }
}

@OptIn(ExperimentalMaterial3AdaptiveApi::class)
@Composable
private fun AppNavigation(pendingLink: MutableStateFlow<PairingLink?>) {
    val store = LocalStore.current
    val settings = LocalSettings.current
    // Each tab keeps a stack of its own, so that switching returns to where it
    // was. A wide window shows the current one's, with no tabs.
    val inbox = rememberNavBackStack(InboxKey)
    val serverList = rememberNavBackStack(ServersKey)
    var tab by rememberSaveable { mutableStateOf(Tab.INBOX) }
    val backStack = if (tab == Tab.INBOX) inbox else serverList
    val link by pendingLink.collectAsStateWithLifecycle()
    var showAdd by rememberSaveable { mutableStateOf(false) }
    // A session started empty, whose prompt takes the keyboard when it opens, and only then.
    var startedEmpty by remember { mutableStateOf<SessionKey?>(null) }
    val top = backStack.lastOrNull()
    LaunchedEffect(top) { if (top != startedEmpty) startedEmpty = null }
    fun startedSession(key: SessionKey, empty: Boolean) {
        startedEmpty = key.takeIf { empty }
        backStack.openSession(key)
    }

    val layoutName by settings.state(AppSettings.Keys.sidebarLayout, SidebarLayout.INBOX.stored)
    val layout = SidebarLayout.parse(layoutName)
    // Wide is where the list-detail strategy puts the list beside the session,
    // decided from the same directive: there the root is the sidebar of every
    // server's sessions, as on iPad. Narrow, it is a phone's tabs. The Columns
    // layout's wider list pane goes through the directive, not the list
    // entry's metadata: a scene keeps the pane width it was first given, which
    // would be the Inbox's until the setting has loaded.
    val window = currentWindowAdaptiveInfoV2()
    val directive = calculatePaneScaffoldDirective(window).let { d ->
        listPaneWidth(layout, window.windowSizeClass)?.let { d.copy(defaultPanePreferredWidth = it) } ?: d
    }
    val wide = directive.maxHorizontalPartitions > 1
    val listDetail = rememberListDetailSceneStrategy<NavKey>(directive = directive)
    LaunchedEffect(wide, tab) {
        // A copy: the stack itself compares by identity, and may come back as it is.
        val current = backStack.toList()
        val normalized = normalizedBackStack(current, wide)
        if (normalized != current) {
            backStack.clear()
            backStack.addAll(normalized)
        }
    }

    // With a single server, the Servers tab opens it the first time it shows
    // on a phone; not on every return to the list, which would bounce the user
    // back in, and not in the Inbox, which has every server's sessions.
    var autoOpened by rememberSaveable { mutableStateOf(false) }
    LaunchedEffect(tab, wide) {
        if (autoOpened || wide || tab != Tab.SERVERS) return@LaunchedEffect
        autoOpened = true
        val servers = store.servers.value
        if (serverList.size == 1 && servers.size == 1) serverList.add(ServerHomeKey(servers[0].id))
    }

    // The tab chosen again goes back to its first screen.
    fun select(t: Tab) {
        if (t == tab) {
            val stack = if (t == Tab.INBOX) inbox else serverList
            while (stack.size > 1) stack.removeAt(stack.lastIndex)
        } else {
            tab = t
        }
    }
    val tabBar = remember(wide, tab) {
        val bar: @Composable () -> Unit = if (wide) ({}) else ({ TabBar(tab, ::select) })
        bar
    }
    // A server removed: nothing of it stays in either tab.
    fun forget(server: SavedServer) {
        inbox.removeAll { it.serverId == server.id }
        serverList.removeAll { it.serverId == server.id }
    }

    val activity = LocalActivity.current
    @Composable
    fun splitView(layout: SidebarLayout, narrow: Boolean) = ServerSplitView(
        layout,
        narrow = narrow,
        selection = backStack.lastOrNull() as? SessionKey,
        onOpenSession = { backStack.openSession(it) },
        onStartedSession = ::startedSession,
        onCloseSession = { if (backStack.lastOrNull() is SessionKey) backStack.removeAt(backStack.lastIndex) },
        onAdd = { showAdd = true },
        onSettings = { backStack.add(SettingsKey) },
        onRemoved = ::forget,
    )
    val listPane = if (wide) ListDetailSceneStrategy.listPane(detailPlaceholder = { NoSessionSelected(onAdd = { showAdd = true }) }) else emptyMap()
    // A tab's screens keep their state, such as where a list was scrolled to,
    // while the other tab shows.
    val tabStates = rememberSaveableStateHolder()
    CompositionLocalProvider(LocalTabBar provides tabBar) {
        tabStates.SaveableStateProvider(tab.name) {
            NavDisplay(
                backStack = backStack,
                // Never pops the last entry (NavDisplay needs one): back there leaves the app.
                onBack = { if (backStack.size > 1) backStack.removeLastOrNull() else activity?.finish() },
                sceneStrategies = listOf(listDetail),
                entryProvider = entryProvider {
                    // Wide, both tabs' first screen is the sidebar beside the open session.
                    entry<InboxKey>(metadata = listPane) {
                        if (wide) splitView(layout, narrow = false) else splitView(SidebarLayout.INBOX, narrow = true)
                    }
                    entry<ServersKey>(metadata = listPane) {
                        if (wide) {
                            splitView(layout, narrow = false)
                        } else {
                            ServersView(
                                onOpen = { backStack.add(ServerHomeKey(it.id)) },
                                onStartedSession = { key, empty ->
                                    backStack.add(ServerHomeKey(key.serverId))
                                    startedSession(key, empty)
                                },
                                onAdd = { showAdd = true },
                                onSettings = { backStack.add(SettingsKey) },
                            )
                        }
                    }
                    entry<ServerHomeKey> { key ->
                        val server = store.server(key.serverId)
                        if (server == null) {
                            LaunchedEffect(key) { backStack.remove(key) }
                        } else {
                            ServerHomeView(
                                connection = store.connection(server),
                                onOpenSession = { id -> backStack.openSession(SessionKey(key.serverId, id)) },
                                onStartedSession = { id, empty -> startedSession(SessionKey(key.serverId, id), empty) },
                                onBack = { if (backStack.size > 1) backStack.removeLastOrNull() },
                                onSettings = { backStack.add(SettingsKey) },
                                onRemoved = { forget(server) },
                            )
                        }
                    }
                    entry<SessionKey>(metadata = ListDetailSceneStrategy.detailPane()) { key ->
                        val server = store.server(key.serverId)
                        if (server == null) {
                            LaunchedEffect(key) { backStack.remove(key) }
                        } else {
                            val connection = store.connection(server)
                            val session = connection.sessionStore(key.sessionId)
                            // Deleted, here or on another device: back to the list it
                            // was opened from, or on a tablet, none selected.
                            if (isDeleted(connection, session)) LaunchedEffect(key) { backStack.remove(key) }
                            SessionView(
                                connection, session, focusPrompt = key == startedEmpty,
                                onBack = { if (backStack.size > 1) backStack.removeLastOrNull() },
                                onForked = { forked, empty -> startedSession(SessionKey(key.serverId, forked.id), empty) },
                            )
                        }
                    }
                    entry<SettingsKey> { SettingsView(onBack = { if (backStack.size > 1) backStack.removeLastOrNull() }) }
                },
            )
        }
    }

    if (showAdd || link != null) {
        AddServerDialog(
            initialLink = link,
            onDismiss = {
                showAdd = false
                pendingLink.value = null
            },
            onPaired = { server ->
                showAdd = false
                pendingLink.value = null
                // A session open on it from before it was paired again had the
                // old connection.
                forget(server)
                if (wide) {
                    // The sidebar lists it with the others.
                    backStack.remove(SettingsKey)
                } else {
                    // Its sessions, in the Servers tab.
                    serverList.removeAll { it != ServersKey }
                    serverList.add(ServerHomeKey(server.id))
                    tab = Tab.SERVERS
                }
            },
        )
    }
}

/** The server a destination is on, if it is on one. */
private val NavKey.serverId: String?
    get() = when (this) {
        is ServerHomeKey -> serverId
        is SessionKey -> serverId
        else -> null
    }

/**
 * A tab's back stack for the layout. Wide, the sidebar lists every server's
 * sessions, so there are no server homes in it. Narrow, a session in the
 * Servers tab sits on its server's sessions, so back from it goes there, as
 * on a phone; the Inbox's sit on the Inbox.
 */
internal fun normalizedBackStack(stack: List<NavKey>, wide: Boolean): List<NavKey> =
    if (wide) {
        stack.filter { it !is ServerHomeKey }
    } else if (stack.firstOrNull() != ServersKey) {
        stack
    } else {
        buildList {
            for (key in stack) {
                if (key is SessionKey && lastOrNull() != ServerHomeKey(key.serverId)) add(ServerHomeKey(key.serverId))
                add(key)
            }
        }
    }

/**
 * The session is gone from the server: its stream says it was deleted, or the
 * synced index doesn't have it. Before the index syncs, every session looks missing.
 */
@Composable
private fun isDeleted(connection: ServerConnection, store: SessionStore): Boolean {
    val synced by connection.indexSynced.collectAsStateWithLifecycle()
    val sessions by connection.sessions.collectAsStateWithLifecycle()
    val removed by store.removed.collectAsStateWithLifecycle()
    return removed || (synced && store.sessionID !in sessions)
}

/** Opens a session: in place of the one shown beside the list, or pushed on a phone. */
private fun NavBackStack<NavKey>.openSession(key: SessionKey) {
    if (lastOrNull() == key) return
    if (lastOrNull() is SessionKey) removeAt(lastIndex)
    add(key)
}

/** Beside the sidebar, until a session is open; with no servers, the way to pair one. */
@Composable
private fun NoSessionSelected(onAdd: () -> Unit) {
    val servers by LocalStore.current.servers.collectAsStateWithLifecycle()
    if (servers.isEmpty()) {
        EmptyState(
            Icons.server, "No servers", description = "Run `rad serve` on your computer, then `rad pair` and scan the QR code.",
            action = "Pair a server" to onAdd,
        )
    } else {
        EmptyState(Icons.question, "No session selected", description = "Choose a session in the list, or start a new one.")
    }
}
