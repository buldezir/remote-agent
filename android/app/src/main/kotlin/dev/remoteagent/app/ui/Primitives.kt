package dev.remoteagent.app.ui

import androidx.annotation.DrawableRes
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.layout.wrapContentWidth
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SwipeToDismissBox
import androidx.compose.material3.SwipeToDismissBoxValue
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.material3.rememberSwipeToDismissBoxState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.Alignment
import androidx.compose.ui.ExperimentalComposeUiApi
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.testTagsAsResourceId
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.compose.ui.window.DialogWindowProvider
import androidx.compose.ui.platform.LocalView
import androidx.compose.runtime.SideEffect
import androidx.core.view.WindowCompat
import kotlinx.coroutines.launch

/** A screen with the app's top bar. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun Screen(
    title: @Composable () -> Unit,
    onBack: (() -> Unit)? = null,
    backIcon: Int = Icons.back,
    backLabel: String = "Back",
    background: Color = palette.base,
    actions: @Composable RowScope.() -> Unit = {},
    bottomBar: @Composable () -> Unit = {},
    content: @Composable (PaddingValues) -> Unit,
) {
    Scaffold(
        containerColor = background,
        topBar = {
            TopAppBar(
                title = title,
                navigationIcon = {
                    if (onBack != null) {
                        IconButton(onBack) { Icon(painterResource(backIcon), backLabel) }
                    }
                },
                actions = actions,
                colors = TopAppBarDefaults.topAppBarColors(
                    containerColor = background,
                    scrolledContainerColor = background,
                    titleContentColor = palette.text,
                    navigationIconContentColor = palette.accent,
                    actionIconContentColor = palette.accent,
                ),
            )
        },
        bottomBar = bottomBar,
        content = content,
    )
}

@Composable
fun ScreenTitle(text: String, subtitle: (@Composable () -> Unit)? = null) {
    Column {
        Text(text, style = Type.headline, maxLines = 1)
        subtitle?.invoke()
    }
}

/** A sheet over the screen, full screen as phones show forms. */
@OptIn(ExperimentalComposeUiApi::class)
@Composable
fun FullScreenDialog(onDismiss: () -> Unit, lightBars: Boolean = !palette.dark, content: @Composable () -> Unit) {
    Dialog(
        onDismissRequest = onDismiss,
        properties = DialogProperties(usePlatformDefaultWidth = false, decorFitsSystemWindows = false),
    ) {
        // A dialog is a window of its own: its system bars' icons follow the
        // palette, and its test tags need this again (android/e2e).
        val window = (LocalView.current.parent as? DialogWindowProvider)?.window
        val view = LocalView.current
        SideEffect {
            window?.let { WindowCompat.getInsetsController(it, view).apply { isAppearanceLightStatusBars = lightBars; isAppearanceLightNavigationBars = lightBars } }
        }
        Box(Modifier.fillMaxSize().semantics { testTagsAsResourceId = true }) { content() }
    }
}

/** A grouped form section: a header, rows on a card, and a footer. */
@Composable
fun FormSection(header: String? = null, footer: AnnotatedString? = null, content: @Composable ColumnScope.() -> Unit) {
    Column(Modifier.fillMaxWidth().padding(horizontal = Metrics.margin, vertical = 6.dp)) {
        if (header != null) {
            Text(header.uppercase(), style = Type.caption, color = palette.subtext, modifier = Modifier.padding(start = 12.dp, bottom = 4.dp))
        }
        Column(Modifier.fillMaxWidth().background(palette.base, RoundedCornerShape(Metrics.Corner.field)), content = content)
        if (footer != null) {
            Text(footer, style = Type.caption, color = palette.subtext, modifier = Modifier.padding(start = 12.dp, end = 12.dp, top = 4.dp))
        }
    }
}

@Composable
fun FormDivider() = HorizontalDivider(Modifier.padding(start = 12.dp), color = palette.surface0)

/** Text with `code` spans in the code font, as the Apple apps render Markdown in strings. */
@Composable
fun inlineCode(text: String): AnnotatedString {
    val codeBackground = palette.surface0
    return buildAnnotatedString {
        text.split('`').forEachIndexed { i, part ->
            if (i % 2 == 1) withStyle(SpanStyle(fontFamily = FontFamily.Monospace, background = codeBackground)) { append(part) }
            else append(part)
        }
    }
}

/** ContentUnavailableView: an icon, a title, a description and an action. */
@Composable
fun EmptyState(
    @DrawableRes icon: Int,
    title: String,
    modifier: Modifier = Modifier,
    description: String? = null,
    action: Pair<String, () -> Unit>? = null,
) {
    Box(modifier.fillMaxSize().padding(32.dp), contentAlignment = Alignment.Center) {
        Column(horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.widthIn(max = 420.dp)) {
            Icon(painterResource(icon), null, Modifier.size(44.dp), tint = palette.overlay)
            Text(title, style = Type.title, color = palette.text, textAlign = TextAlign.Center)
            if (description != null) {
                Text(inlineCode(description), style = Type.subheadline, color = palette.subtext, textAlign = TextAlign.Center)
            }
            if (action != null) {
                Spacer(Modifier.size(4.dp))
                Button(action.second, colors = ButtonDefaults.buttonColors(containerColor = palette.accent, contentColor = palette.onAccent)) {
                    Text(action.first)
                }
            }
        }
    }
}

@Composable
fun Loading(label: String, modifier: Modifier = Modifier) {
    Box(modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
        Column(horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(10.dp)) {
            CircularProgressIndicator(color = palette.subtext)
            Text(label, style = Type.subheadline, color = palette.subtext)
        }
    }
}

/** A confirmation with a destructive (or plain) action. */
@Composable
fun ConfirmDialog(
    title: String,
    message: String?,
    confirm: String,
    destructive: Boolean = true,
    onConfirm: () -> Unit,
    onDismiss: () -> Unit,
    extra: Pair<String, () -> Unit>? = null,
) {
    val cancel: @Composable () -> Unit = { TextButton(onDismiss) { Text("Cancel", color = palette.accent) } }
    AlertDialog(
        onDismissRequest = onDismiss,
        containerColor = palette.mantle,
        titleContentColor = palette.text,
        textContentColor = palette.subtext,
        title = { Text(title) },
        text = message?.let { { Text(inlineCode(it)) } },
        confirmButton = {
            Column(horizontalAlignment = Alignment.End) {
                TextButton({ onDismiss(); onConfirm() }) { Text(confirm, color = if (destructive) palette.red else palette.accent) }
                if (extra != null) {
                    TextButton({ onDismiss(); extra.second() }) { Text(extra.first, color = palette.red) }
                    cancel()
                }
            }
        },
        // With three choices Cancel goes below the others, in the same slot:
        // AlertDialog lays its buttons out in rows that it reverses without
        // their offsets, so a row taller than Cancel's would be drawn over it.
        dismissButton = if (extra == null) cancel else null,
    )
}

/** An error to acknowledge, as the Apple apps' alerts. */
@Composable
fun ErrorDialog(message: String?, title: String = "Something went wrong", onDismiss: () -> Unit) {
    if (message == null) return
    AlertDialog(
        onDismissRequest = onDismiss,
        containerColor = palette.mantle,
        titleContentColor = palette.text,
        textContentColor = palette.subtext,
        title = { Text(title) },
        text = { Text(message) },
        confirmButton = { TextButton(onDismiss) { Text("OK", color = palette.accent) } },
    )
}

/** An item of a dropdown menu, greyed out while disabled. */
@Composable
fun MenuItem(text: String, icon: Int?, color: Color = palette.text, enabled: Boolean = true, onClick: () -> Unit) {
    DropdownMenuItem(
        text = { Text(text, color = if (enabled) color else palette.overlay) },
        leadingIcon = icon?.let { { Icon(painterResource(it), null, tint = if (enabled) color else palette.overlay) } },
        enabled = enabled,
        onClick = onClick,
    )
}

/**
 * A row that swipes from the end to reveal one action, as iOS swipe actions
 * do. A drag past half the row, or a fling, takes it; a shorter one springs back.
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
fun SwipeAction(
    label: String,
    @DrawableRes icon: Int,
    color: Color,
    enabled: Boolean = true,
    onSwipe: () -> Unit,
    content: @Composable () -> Unit,
) {
    if (!enabled) {
        content()
        return
    }
    val state = rememberSwipeToDismissBoxState(positionalThreshold = { it / 2 })
    val scope = rememberCoroutineScope()
    SwipeToDismissBox(
        state = state,
        enableDismissFromStartToEnd = false,
        onDismiss = {
            onSwipe()
            scope.launch { state.reset() }
        },
        backgroundContent = {
            val shown = state.dismissDirection == SwipeToDismissBoxValue.EndToStart
            Box(Modifier.fillMaxSize().background(if (shown) color else Color.Transparent).padding(horizontal = 20.dp), contentAlignment = Alignment.CenterEnd) {
                if (shown) {
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                        Icon(painterResource(icon), null, tint = Color.White)
                        Text(label, color = Color.White, style = Type.subheadline)
                    }
                }
            }
        },
    ) { content() }
}

/** A text field as a row of a form: no border, the placeholder in the overlay colour. */
@Composable
fun FormTextField(
    value: String,
    onValueChange: (String) -> Unit,
    placeholder: String,
    modifier: Modifier = Modifier,
    style: androidx.compose.ui.text.TextStyle = Type.body,
    maxLines: Int = 1,
    minLines: Int = 1,
    keyboardOptions: androidx.compose.foundation.text.KeyboardOptions = androidx.compose.foundation.text.KeyboardOptions.Default,
    keyboardActions: androidx.compose.foundation.text.KeyboardActions = androidx.compose.foundation.text.KeyboardActions.Default,
) {
    androidx.compose.foundation.text.BasicTextField(
        value = value,
        onValueChange = onValueChange,
        modifier = modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 12.dp),
        textStyle = style.copy(color = palette.text),
        singleLine = maxLines == 1,
        maxLines = maxLines,
        minLines = minLines,
        keyboardOptions = keyboardOptions,
        keyboardActions = keyboardActions,
        cursorBrush = androidx.compose.ui.graphics.SolidColor(palette.accent),
        decorationBox = { inner ->
            Box {
                if (value.isEmpty()) Text(placeholder, style = style, color = palette.overlay)
                inner()
            }
        },
    )
}

/** Forms stop widening at a readable width on a tablet, centred. */
fun Modifier.formWidth(): Modifier = this.wrapContentWidth(Alignment.CenterHorizontally).widthIn(max = 720.dp)
