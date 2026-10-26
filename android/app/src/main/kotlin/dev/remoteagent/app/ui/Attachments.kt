package dev.remoteagent.app.ui

import android.content.Context
import android.graphics.Bitmap
import android.net.Uri
import androidx.compose.runtime.Composable
import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import dev.remoteagent.app.platform.ImageCoding
import dev.remoteagent.core.protocol.ImageRef
import dev.remoteagent.core.store.ServerConnection
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * Images attached to a prompt being written. Each uploads as soon as it is
 * added, so sending doesn't wait. Mirrors apple/Shared/ImageAttachments.swift.
 */
@Stable
class Attachments(private val connection: ServerConnection, private val onError: (String) -> Unit) {
    sealed interface State {
        data object Uploading : State
        data class Uploaded(val ref: ImageRef) : State
        data class Failed(val message: String) : State
    }

    /** [thumbnail]: null for an image that was on the server already, which shows from there. */
    data class Entry(val id: Long, val thumbnail: Bitmap?, val state: State) {
        val ref: ImageRef? get() = (state as? State.Uploaded)?.ref
    }

    val entries = mutableStateListOf<Entry>()
    private val data = HashMap<Long, ByteArray>()
    private var nextID = 0L

    val isEmpty: Boolean get() = entries.isEmpty()
    /** Every image is on the server, so the prompt can go. */
    val ready: Boolean get() = entries.all { it.state is State.Uploaded }
    val refs: List<ImageRef> get() = entries.mapNotNull { it.ref }

    fun clear() {
        entries.clear()
        data.clear()
    }

    /**
     * Puts back the images of a prompt that couldn't be sent, ahead of any
     * added since. They are on the server already.
     */
    fun restore(sent: List<Entry>) {
        entries.addAll(0, sent)
    }

    /** Puts images that are on the server already, such as a draft's, ahead of the others. */
    fun insert(refs: List<ImageRef>) {
        entries.addAll(0, refs.map { Entry(nextID++, null, State.Uploaded(it)) })
    }

    fun remove(id: Long) {
        entries.removeAll { it.id == id }
        data.remove(id)
    }

    /** Adds images given as encoded bytes in any format the platform reads. */
    fun add(images: List<ByteArray>) {
        connection.scope.launch {
            for (raw in images) {
                val prepared = withContext(Dispatchers.Default) { ImageCoding.prepare(raw) }
                if (prepared == null) {
                    onError("That isn't an image this app can read.")
                    continue
                }
                val id = nextID++
                entries += Entry(id, prepared.thumbnail, State.Uploading)
                data[id] = prepared.data
                upload(id)
            }
        }
    }

    /** Images from the photo picker, the file picker or the clipboard. */
    fun add(uris: List<Uri>, context: Context) {
        if (uris.isEmpty()) return
        connection.scope.launch {
            val images = withContext(Dispatchers.IO) {
                uris.mapNotNull { uri -> runCatching { context.contentResolver.openInputStream(uri)?.use { it.readBytes() } }.getOrNull() }
            }
            add(images)
        }
    }

    fun retry(id: Long) {
        val i = entries.indexOfFirst { it.id == id }
        if (i < 0) return
        entries[i] = entries[i].copy(state = State.Uploading)
        upload(id)
    }

    private fun upload(id: Long) {
        val bytes = data[id] ?: return
        connection.scope.launch {
            val state = try {
                State.Uploaded(connection.uploadImage(bytes))
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                State.Failed(e.message ?: "The upload failed")
            }
            val i = entries.indexOfFirst { it.id == id }
            if (i >= 0) entries[i] = entries[i].copy(state = state)
        }
    }
}

@Composable
fun rememberAttachments(connection: ServerConnection, onError: (String) -> Unit): Attachments {
    val report by rememberUpdatedState(onError)
    return remember(connection) { Attachments(connection) { report(it) } }
}
