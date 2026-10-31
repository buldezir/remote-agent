package dev.remoteagent.app

import android.content.Intent
import android.os.Bundle
import android.view.KeyEvent
import android.view.KeyboardShortcutGroup
import android.view.KeyboardShortcutInfo
import android.view.Menu
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import dev.remoteagent.app.ui.App
import dev.remoteagent.app.ui.Shortcut
import dev.remoteagent.core.net.PairingLink
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow

class MainActivity : ComponentActivity() {
    /** A pairing link the app was opened with (remoteagent://pair?…), for the pairing sheet. */
    private val pendingLink = MutableStateFlow<PairingLink?>(null)
    /** A hardware keyboard's shortcuts, for the views shown now (with no one listening, dropped). */
    private val shortcuts = MutableSharedFlow<Shortcut>(extraBufferCapacity = 4)

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        if (savedInstanceState == null) handle(intent)
        setContent { App(application as RemoteAgentApp, pendingLink, shortcuts) }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handle(intent)
    }

    // Ctrl and a key the views left alone comes here, wherever the focus is,
    // such as in the composer, or nowhere.
    override fun onKeyShortcut(keyCode: Int, event: KeyEvent): Boolean {
        val shortcut = Shortcut.of(event) ?: return super.onKeyShortcut(keyCode, event)
        shortcuts.tryEmit(shortcut)
        return true
    }

    /** The shortcuts, for the list a keyboard's Meta+/ shows. */
    override fun onProvideKeyboardShortcuts(data: MutableList<KeyboardShortcutGroup>, menu: Menu?, deviceId: Int) {
        super.onProvideKeyboardShortcuts(data, menu, deviceId)
        data += KeyboardShortcutGroup(
            getString(R.string.app_name),
            Shortcut.entries.map { KeyboardShortcutInfo(it.title, it.keyCode, it.modifiers) },
        )
    }

    private fun handle(intent: Intent?) {
        intent?.dataString?.let(PairingLink::parse)?.let { pendingLink.value = it }
    }
}
