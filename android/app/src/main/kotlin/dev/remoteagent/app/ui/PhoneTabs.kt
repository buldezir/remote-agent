package dev.remoteagent.app.ui

import androidx.compose.material3.Badge
import androidx.compose.material3.BadgedBox
import androidx.compose.material3.Icon
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.NavigationBarItemDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.compositionLocalOf
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.painterResource

/**
 * A phone's tabs, each with a stack of its own (AppNavigation): every
 * server's sessions by state, and the servers, each opening to its sessions.
 * Mirrors apple/iOS/PhoneTabs.swift.
 */
enum class Tab { INBOX, SERVERS }

/**
 * The tab bar, for the foot of a tab's own screens: the Inbox, the servers and
 * a server's sessions. Nothing on a wide window, which has no tabs.
 */
val LocalTabBar = compositionLocalOf<@Composable () -> Unit> { {} }

/** The tabs, the Inbox's with how many sessions need you or are unread. */
@Composable
fun TabBar(tab: Tab, onSelect: (Tab) -> Unit) {
    val count = serverStates().sumOf { s -> s.active.count { it.standsOut } }
    val colors = NavigationBarItemDefaults.colors(
        selectedIconColor = palette.accent,
        selectedTextColor = palette.accent,
        indicatorColor = palette.surface0,
        unselectedIconColor = palette.subtext,
        unselectedTextColor = palette.subtext,
    )
    NavigationBar(containerColor = palette.mantle, contentColor = palette.subtext) {
        NavigationBarItem(
            selected = tab == Tab.INBOX,
            onClick = { onSelect(Tab.INBOX) },
            icon = {
                BadgedBox(badge = {
                    if (count > 0) Badge(containerColor = palette.yellow, contentColor = palette.onAccent) { Text("$count") }
                }) {
                    Icon(painterResource(Icons.inbox), null)
                }
            },
            label = { Text("Inbox", style = Type.caption) },
            colors = colors,
            modifier = Modifier.testTag("tab-inbox"),
        )
        NavigationBarItem(
            selected = tab == Tab.SERVERS,
            onClick = { onSelect(Tab.SERVERS) },
            icon = { Icon(painterResource(Icons.server), null) },
            label = { Text("Servers", style = Type.caption) },
            colors = colors,
            modifier = Modifier.testTag("tab-servers"),
        )
    }
}
