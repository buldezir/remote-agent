package dev.remoteagent.app.ui

import android.content.ClipboardManager
import android.content.ContentValues
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.provider.MediaStore
import android.util.LruCache
import android.widget.Toast
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.PickVisualMediaRequest
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.pager.HorizontalPager
import androidx.compose.foundation.pager.rememberPagerState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.core.content.FileProvider
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import dev.remoteagent.app.platform.ImageCoding
import dev.remoteagent.core.protocol.ImageRef
import dev.remoteagent.core.store.ConnectionState
import dev.remoteagent.core.store.ServerConnection
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import me.saket.telephoto.zoomable.ZoomableContentLocation
import me.saket.telephoto.zoomable.rememberZoomableState
import me.saket.telephoto.zoomable.zoomable

// Attaching. Mirrors apple/Shared/Views/ImageViews.swift.

/** The composer's + button: photos, files or the clipboard. With a title, a labelled row instead, for forms. */
@Composable
fun AttachMenu(attachments: Attachments, title: String? = null, enabled: Boolean = true) {
    val context = LocalContext.current
    var open by remember { mutableStateOf(false) }
    val photos = rememberLauncherForActivityResult(ActivityResultContracts.PickMultipleVisualMedia(10)) { attachments.add(it, context) }
    val files = rememberLauncherForActivityResult(ActivityResultContracts.OpenMultipleDocuments()) { attachments.add(it, context) }
    val clipboard = context.getSystemService(ClipboardManager::class.java)
    Box {
        if (title != null) {
            Row(
                Modifier.fillMaxWidth().clickable(enabled = enabled) { open = true }.padding(12.dp),
                horizontalArrangement = Arrangement.spacedBy(10.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Icon(painterResource(Icons.photos), null, tint = palette.accent)
                Text(title, color = palette.accent, style = Type.body)
            }
        } else {
            IconButton({ open = true }, Modifier.size(40.dp).testTag("attach"), enabled = enabled) {
                Icon(painterResource(Icons.addCircle), "Add images", Modifier.size(Metrics.roundButton), tint = palette.overlay)
            }
        }
        DropdownMenu(open, { open = false }, containerColor = palette.mantle) {
            MenuItem("Photo Library", Icons.photos) {
                open = false
                photos.launch(PickVisualMediaRequest(ActivityResultContracts.PickVisualMedia.ImageOnly))
            }
            MenuItem("Files", Icons.folder) {
                open = false
                files.launch(arrayOf("image/*"))
            }
            // Reading only the clip's description doesn't count as a paste.
            val hasImage = clipboard?.primaryClipDescription?.hasMimeType("image/*") == true
            MenuItem("Paste Image", Icons.paste, enabled = hasImage) {
                open = false
                val clip = clipboard?.primaryClip ?: return@MenuItem
                attachments.add((0 until clip.itemCount).mapNotNull { clip.getItemAt(it).uri }, context)
            }
        }
    }
}

/** Thumbnails of the images attached to a prompt being written. */
@Composable
fun AttachmentStrip(attachments: Attachments, modifier: Modifier = Modifier) {
    LazyRow(modifier.testTag("attachments"), horizontalArrangement = Arrangement.spacedBy(10.dp)) {
        items(attachments.entries, key = { it.id }) { entry ->
            Box(Modifier.padding(top = 6.dp, end = 6.dp)) {
                Box(Modifier.size(56.dp).clip(RoundedCornerShape(Metrics.Corner.card)), contentAlignment = Alignment.Center) {
                    val thumbnail = entry.thumbnail
                    if (thumbnail != null) Image(thumbnail.asImageBitmap(), null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
                    else entry.ref?.let { RemoteImage(it, 56.dp) }
                    when (val state = entry.state) {
                        Attachments.State.Uploading -> CircularProgressIndicator(Modifier.size(20.dp), color = Color.White, strokeWidth = 2.dp)
                        is Attachments.State.Uploaded -> {}
                        is Attachments.State.Failed -> Box(
                            Modifier.fillMaxSize().background(Color.Black.copy(alpha = 0.35f)).clickable { attachments.retry(entry.id) },
                            contentAlignment = Alignment.Center,
                        ) {
                            Icon(painterResource(Icons.reconnect), "Upload failed: ${state.message}. Try again", tint = palette.red)
                        }
                    }
                }
                Box(
                    Modifier.align(Alignment.TopEnd).offset(x = 6.dp, y = (-6).dp).size(20.dp)
                        .background(Color.Black.copy(alpha = 0.6f), CircleShape).clickable { attachments.remove(entry.id) },
                    contentAlignment = Alignment.Center,
                ) {
                    Icon(painterResource(Icons.close), "Remove image", Modifier.size(14.dp), tint = Color.White)
                }
            }
        }
    }
}

// Showing.

/** Decoded thumbnails, so rows that scroll back into view don't flicker. */
private object Thumbnails {
    val cache = LruCache<String, ImageBitmap>(200)
}

/** One image from the server, at a fixed height and its own width (very wide or tall ones are cropped). */
@Composable
fun RemoteImage(ref: ImageRef, height: Dp, modifier: Modifier = Modifier) {
    val connection = LocalConnection.current
    val state by connection.state.collectAsStateWithLifecycle()
    val heightPx = with(LocalDensity.current) { height.roundToPx() }
    val key = "${ref.id}@$heightPx"
    var image by remember(key) { mutableStateOf(Thumbnails.cache[key]) }
    var failed by remember(key) { mutableStateOf(false) }
    val ratio = ref.aspectRatio ?: image?.let { it.width.toDouble() / it.height } ?: 1.0
    val width = height * ratio.coerceIn(0.5, 2.0).toFloat()
    // From the disk cache even offline; from the server once connected.
    LaunchedEffect(key, state == ConnectionState.Connected) {
        if (image != null) return@LaunchedEffect
        try {
            val bytes = connection.imageData(ref.id)
            // Twice the frame, so cropped wide or tall images still fill it.
            val thumb = withContext(Dispatchers.Default) { ImageCoding.thumbnail(bytes, heightPx * 4) }?.asImageBitmap()
            thumb?.let { Thumbnails.cache.put(key, it) }
            image = thumb
            failed = thumb == null
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) {
            failed = true
        }
    }
    Box(
        modifier.size(width, height).clip(RoundedCornerShape(Metrics.Corner.card)).background(palette.surface0),
        contentAlignment = Alignment.Center,
    ) {
        val img = image
        when {
            img != null -> Image(img, "Image", Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
            failed -> Icon(painterResource(Icons.image), null, tint = palette.overlay)
            else -> CircularProgressIndicator(Modifier.size(18.dp), color = palette.subtext, strokeWidth = 2.dp)
        }
    }
}

/**
 * An item's images as thumbnails that wrap onto more lines; a tap opens one
 * full screen, where it zooms and shares, and the item's others are a swipe away.
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
fun ImageGallery(images: List<ImageRef>, height: Dp, modifier: Modifier = Modifier, alignEnd: Boolean = false, browsing: List<ImageRef>? = null) {
    var shown by remember { mutableStateOf<ImageRef?>(null) }
    FlowRow(
        modifier.testTag("image-gallery"),
        horizontalArrangement = Arrangement.spacedBy(6.dp, if (alignEnd) Alignment.End else Alignment.Start),
        verticalArrangement = Arrangement.spacedBy(6.dp),
    ) {
        for (ref in images) RemoteImage(ref, height, Modifier.clickable { shown = ref })
    }
    shown?.let { ref ->
        val all = browsing ?: images
        ImageViewer(all, all.indexOfFirst { it.id == ref.id }.coerceAtLeast(0), onDismiss = { shown = null })
    }
}

/**
 * An image an agent's reply links to: as large as fits the transcript's
 * width, up to a height that leaves the text around it in view.
 */
@Composable
fun ReplyImage(ref: ImageRef, all: List<ImageRef>) {
    BoxWithConstraints(Modifier.fillMaxWidth()) {
        val ratio = (ref.aspectRatio ?: 1.0).coerceIn(0.5, 2.0).toFloat()
        val height = listOf(320.dp, 240.dp, 180.dp, 120.dp).firstOrNull { it * ratio <= maxWidth } ?: 120.dp
        ImageGallery(listOf(ref), height, browsing = all)
    }
}

/** Images full screen: pinch or double-tap to zoom, swipe between them, share or save one. */
@Composable
fun ImageViewer(images: List<ImageRef>, start: Int, onDismiss: () -> Unit) {
    val connection = LocalConnection.current
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val pager = rememberPagerState(start) { images.size }
    FullScreenDialog(onDismiss, lightBars = false) {
        Box(Modifier.fillMaxSize().background(Color.Black).testTag("image-viewer")) {
            HorizontalPager(pager, Modifier.fillMaxSize()) { page -> ZoomableImage(connection, images[page]) }
            Row(
                Modifier.fillMaxWidth().statusBarsPadding().padding(horizontal = 4.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                IconButton(onDismiss) { Icon(painterResource(Icons.close), "Close", tint = Color.White) }
                Text(
                    if (images.size > 1) "${pager.currentPage + 1} of ${images.size}" else "",
                    color = Color.White, style = Type.subheadline, modifier = Modifier.weight(1f),
                )
                IconButton({ scope.launch { share(context, connection, images[pager.currentPage]) } }) {
                    Icon(painterResource(Icons.share), "Share", tint = Color.White)
                }
                IconButton({ scope.launch { save(context, connection, images[pager.currentPage]) } }) {
                    Icon(painterResource(Icons.save), "Save to Pictures", tint = Color.White)
                }
            }
        }
    }
}

@Composable
private fun ZoomableImage(connection: ServerConnection, ref: ImageRef) {
    val zoom = rememberZoomableState()
    var image by remember(ref.id) { mutableStateOf<ImageBitmap?>(null) }
    var failed by remember(ref.id) { mutableStateOf(false) }
    LaunchedEffect(ref.id) {
        try {
            val bytes = connection.imageData(ref.id)
            image = withContext(Dispatchers.Default) { ImageCoding.thumbnail(bytes, 4096) }?.asImageBitmap()
            failed = image == null
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) {
            failed = true
        }
    }
    val img = image
    LaunchedEffect(img) {
        if (img != null) {
            zoom.setContentLocation(ZoomableContentLocation.scaledInsideAndCenterAligned(androidx.compose.ui.geometry.Size(img.width.toFloat(), img.height.toFloat())))
        }
    }
    Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
        when {
            img != null -> Image(img, "Image", Modifier.fillMaxSize().zoomable(zoom), contentScale = ContentScale.Inside)
            failed -> Text("The image couldn't be loaded.", color = Color.White, style = Type.subheadline)
            else -> CircularProgressIndicator(color = Color.White)
        }
    }
}

private suspend fun share(context: Context, connection: ServerConnection, ref: ImageRef) {
    try {
        val file = connection.imageFile(ref.id)
        val uri = FileProvider.getUriForFile(context, context.packageName + ".files", file)
        val send = Intent(Intent.ACTION_SEND).setType(ref.mimeType.ifEmpty { "image/*" }).putExtra(Intent.EXTRA_STREAM, uri)
            .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        context.startActivity(Intent.createChooser(send, null))
    } catch (e: CancellationException) {
        throw e
    } catch (e: Exception) {
        Toast.makeText(context, e.message ?: "The image couldn't be shared.", Toast.LENGTH_LONG).show()
    }
}

/** Saves to Pictures/Remote Agent; MediaStore needs no permission for the app's own files. */
private suspend fun save(context: Context, connection: ServerConnection, ref: ImageRef) {
    try {
        val bytes = connection.imageData(ref.id)
        val ext = ref.id.substringAfterLast('.', "png")
        val uri: Uri = withContext(Dispatchers.IO) {
            val values = ContentValues().apply {
                put(MediaStore.Images.Media.DISPLAY_NAME, "Remote Agent ${ref.id.take(8)}.$ext")
                put(MediaStore.Images.Media.MIME_TYPE, ref.mimeType.ifEmpty { "image/$ext" })
                put(MediaStore.Images.Media.RELATIVE_PATH, "Pictures/Remote Agent")
            }
            val uri = context.contentResolver.insert(MediaStore.Images.Media.EXTERNAL_CONTENT_URI, values) ?: error("MediaStore refused the image")
            context.contentResolver.openOutputStream(uri)?.use { it.write(bytes) }
            uri
        }
        Toast.makeText(context, "Saved to Pictures", Toast.LENGTH_SHORT).show()
        uri.toString()
    } catch (e: CancellationException) {
        throw e
    } catch (e: Exception) {
        Toast.makeText(context, e.message ?: "The image couldn't be saved.", Toast.LENGTH_LONG).show()
    }
}

