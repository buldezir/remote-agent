package dev.remoteagent.app.ui

import android.text.format.DateFormat
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import dev.remoteagent.core.protocol.HarnessInfo
import dev.remoteagent.core.protocol.ImportableSession
import dev.remoteagent.core.protocol.Project
import dev.remoteagent.core.protocol.Session
import dev.remoteagent.core.store.ConnectionState
import dev.remoteagent.core.store.ImportSession
import dev.remoteagent.core.store.ServerConnection
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import java.time.Duration
import java.time.Instant
import java.time.ZoneId
import java.util.Locale

/** What an active session's row warns of: one still open in a terminal would write over it. */
internal const val ACTIVE_IMPORT_WARNING = "Active in the last minute: if it still runs in a terminal, quit it there first."

/** "4 messages · 2h ago": how long the session is, and when the agent last wrote to it. [pattern]: the date's, for the locale. */
internal fun importableDetail(
    session: ImportableSession,
    now: Instant,
    zone: ZoneId = ZoneId.systemDefault(),
    pattern: String = "MMM d",
    locale: Locale = Locale.getDefault(),
): String {
    val seconds = Duration.between(session.updatedAt, now).seconds
    val age = when {
        seconds < 60 -> "just now"
        seconds < 7 * 86_400 -> "${shortAge(session.updatedAt, now)} ago"
        else -> shortAge(session.updatedAt, now, zone, pattern, locale)
    }
    return "${plural(session.messages, "message")} · $age"
}

/**
 * "Terminal sessions": the sessions `harness` ran in `project`'s folder
 * outside Remote Agent, such as in a terminal, newest first. Tapping one picks
 * it up at once with the New Session form's `model`, `effort` and `mode`
 * (null: the agent's defaults); an active one's row warns first, as the
 * transcript's last notice does. `onImported` gets the new session.
 */
@Composable
fun ImportSessionDialog(
    connection: ServerConnection,
    project: Project,
    harness: HarnessInfo,
    model: String?,
    effort: String?,
    mode: String?,
    onDismiss: () -> Unit,
    onImported: suspend (Session) -> Unit,
) {
    val scope = rememberCoroutineScope()
    var sessions by remember { mutableStateOf<List<ImportableSession>?>(null) }
    var loadError by remember { mutableStateOf<String?>(null) }
    var reload by remember { mutableIntStateOf(0) }
    var now by remember { mutableStateOf(Instant.now()) }
    // The one being picked up; the others wait.
    var importing by remember { mutableStateOf<String?>(null) }
    var error by remember { mutableStateOf<String?>(null) }

    LaunchedEffect(reload) {
        loadError = null
        try {
            sessions = connection.importable(project.id, harness.id)
            now = Instant.now()
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            loadError = e.message ?: "The server didn't answer"
        }
    }

    fun pickUp(s: ImportableSession) {
        importing = s.id
        error = null
        scope.launch {
            try {
                val imported = connection.importSession(
                    ImportSession(projectId = project.id, harness = harness.id, nativeId = s.id, model = model, effort = effort, mode = mode),
                )
                onImported(imported)
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                error = e.message ?: "The server didn't answer"
            } finally {
                importing = null
            }
        }
    }

    FullScreenDialog(onDismiss) {
        Screen(
            title = { Text("Terminal sessions", style = Type.headline) },
            onBack = onDismiss, background = palette.mantle,
        ) { padding ->
            Column(Modifier.fillMaxSize().padding(padding).verticalScroll(rememberScrollState()).formWidth()) {
                val list = sessions
                val footer = AnnotatedString(
                    "Picks up a session ${harness.name} ran in this folder outside Remote Agent. " +
                        "Its next prompt goes on with it here, in the project folder.",
                )
                when {
                    loadError != null && list == null -> FormSection {
                        Row(Modifier.padding(12.dp), horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                            Icon(painterResource(Icons.warning), null, tint = palette.red, modifier = Modifier.size(18.dp))
                            Text(loadError ?: "", color = palette.red, style = Type.subheadline, modifier = Modifier.weight(1f))
                            TextButton(
                                {
                                    // A connection that gave up waits for this to try again.
                                    if (connection.state.value is ConnectionState.Failed) connection.retry()
                                    reload++
                                },
                                modifier = Modifier.testTag("import-retry"),
                            ) { Text("Retry", color = palette.accent, style = Type.body) }
                        }
                    }
                    list == null -> Row(
                        Modifier.fillMaxWidth().padding(24.dp).testTag("import-loading"),
                        horizontalArrangement = Arrangement.spacedBy(10.dp, Alignment.CenterHorizontally),
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        CircularProgressIndicator(Modifier.size(18.dp), color = palette.subtext, strokeWidth = 2.dp)
                        Text("Looking for sessions…", style = Type.body, color = palette.subtext)
                    }
                    list.isEmpty() -> FormSection(footer = footer) {
                        Text(
                            "No ${harness.name} sessions to continue in ${project.name}.",
                            style = Type.body, color = palette.subtext, modifier = Modifier.padding(12.dp).testTag("import-empty"),
                        )
                    }
                    else -> FormSection(project.name, footer = footer) {
                        val locale = LocalConfiguration.current.locales[0]
                        val pattern = remember(locale) { DateFormat.getBestDateTimePattern(locale, "MMMd") }
                        list.forEachIndexed { i, s ->
                            if (i > 0) FormDivider()
                            ImportableRow(
                                s, importableDetail(s, now, pattern = pattern, locale = locale),
                                importing = importing == s.id, enabled = importing == null,
                            ) { pickUp(s) }
                        }
                    }
                }
            }
        }
    }
    ErrorDialog(error, title = "Couldn't continue the session") { error = null }
}

/** A session to pick up: its title, its length and age, and a warning when it may still be open in a terminal. */
@Composable
private fun ImportableRow(session: ImportableSession, detail: String, importing: Boolean, enabled: Boolean, onClick: () -> Unit) {
    Row(
        Modifier.fillMaxWidth().clickable(enabled = enabled, role = Role.Button, onClick = onClick)
            .alpha(if (enabled || importing) 1f else 0.5f)
            .padding(horizontal = 12.dp, vertical = 10.dp).testTag("importable"),
        horizontalArrangement = Arrangement.spacedBy(12.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            Text(session.title.ifBlank { "Untitled" }, style = Type.body, color = palette.text, maxLines = 2, overflow = TextOverflow.Ellipsis)
            Text(detail, style = Type.footnote, color = palette.subtext, maxLines = 1)
            if (session.active) {
                Row(Modifier.padding(top = 2.dp), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    Icon(painterResource(Icons.warning), null, Modifier.padding(top = 1.dp).size(14.dp), tint = palette.yellow)
                    Text(ACTIVE_IMPORT_WARNING, style = Type.footnote, color = palette.yellow)
                }
            }
        }
        if (importing) {
            CircularProgressIndicator(Modifier.size(18.dp), color = palette.subtext, strokeWidth = 2.dp)
        } else {
            Icon(painterResource(Icons.chevronRight), null, tint = palette.overlay)
        }
    }
}
