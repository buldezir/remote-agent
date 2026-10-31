package dev.remoteagent.app

import android.app.Application
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.ProcessLifecycleOwner
import dev.remoteagent.app.platform.AppSettings
import dev.remoteagent.app.platform.KeystoreTokenStore
import dev.remoteagent.app.platform.PrefsStorage
import dev.remoteagent.core.store.ImageCache
import dev.remoteagent.core.store.ServerStore
import dev.remoteagent.core.store.SessionSearch
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import java.io.File

/** One ServerStore for the app, as RemoteAgentApp.swift keeps one. */
class RemoteAgentApp : Application() {
    /** The stores change state here: on the main thread, for as long as the app runs. */
    val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    lateinit var store: ServerStore
        private set
    lateinit var settings: AppSettings
        private set
    /**
     * Search across every server, for the sidebar and the Inbox tab: one for
     * the app, so that its query and Include archived last while it runs.
     */
    val search: SessionSearch by lazy { SessionSearch(scope) { store.servers.value.map(store::connection) } }

    override fun onCreate() {
        super.onCreate()
        if (BuildConfig.DEBUG) dev.remoteagent.core.store.Log.sink = { android.util.Log.d("RemoteAgent", it) }
        settings = AppSettings(this, scope)
        store = ServerStore(PrefsStorage(this), KeystoreTokenStore(this), ImageCache(File(cacheDir, "Images")), scope)
        // Sockets don't survive the background; reconnect and resync on return.
        ProcessLifecycleOwner.get().lifecycle.addObserver(LifecycleEventObserver { _, event ->
            when (event) {
                Lifecycle.Event.ON_START -> store.setActive(true)
                Lifecycle.Event.ON_STOP -> store.setActive(false)
                else -> {}
            }
        })
    }
}
