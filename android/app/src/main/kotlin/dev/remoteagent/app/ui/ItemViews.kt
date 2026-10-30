package dev.remoteagent.app.ui

import androidx.compose.animation.animateContentSize
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.PathEffect
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import dev.remoteagent.core.protocol.Draft
import dev.remoteagent.core.protocol.ImageRef
import dev.remoteagent.core.protocol.Item
import dev.remoteagent.core.protocol.ItemKind
import dev.remoteagent.core.protocol.ItemStatus
import dev.remoteagent.core.protocol.Plan
import dev.remoteagent.core.protocol.ToolKind
import dev.remoteagent.core.protocol.Turn
import dev.remoteagent.core.protocol.TurnStatus
import dev.remoteagent.core.protocol.get
import dev.remoteagent.core.protocol.pretty
import dev.remoteagent.core.protocol.stringValue
import dev.remoteagent.core.store.SessionStore
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject

/** One transcript item. Mirrors apple/Shared/Views/ItemViews.swift. */
@Composable
fun ItemView(item: Item, store: SessionStore, children: Map<String, List<Item>>) {
    when (item.kind) {
        ItemKind.USER_MESSAGE -> UserBubble(
            item.text ?: "", item.images ?: emptyList(),
            pending = item.status == ItemStatus.QUEUED, cancelled = item.status == ItemStatus.CANCELLED,
            sendNow = if (item.status == ItemStatus.QUEUED) ({ store.sendNow(item.id) }) else null,
            cancelQueued = if (item.status == ItemStatus.QUEUED) ({ store.cancelQueued(item.id) }) else null,
        )
        ItemKind.ASSISTANT_MESSAGE -> SelectionContainer {
            AgentMarkdown(item.text ?: "", Modifier.testTag("reply"), images = item.images ?: emptyList())
        }
        ItemKind.REASONING -> ReasoningView(item)
        ItemKind.TOOL_CALL -> ToolCallView(item, children[item.id] ?: emptyList(), store, children)
        ItemKind.APPROVAL -> ApprovalView(item, store)
        ItemKind.PLAN -> PlanView(item.plan)
        ItemKind.NOTICE -> Text(
            item.text ?: "", style = Type.footnote, color = palette.subtext, textAlign = TextAlign.Center,
            modifier = Modifier.fillMaxWidth().testTag("notice"),
        )
        ItemKind.ERROR -> Row(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.CenterVertically) {
            Icon(painterResource(Icons.warning), null, Modifier.size(16.dp), tint = palette.red)
            Text(item.text ?: "Error", style = Type.footnote, color = palette.red)
        }
        ItemKind.UNKNOWN -> {}
    }
}

/**
 * A prompt. `pending` fades it, as one waiting to be sent or queued behind the
 * running turn; `sendNow`, for one queued on the server, sends it at once, and
 * `cancelQueued` takes it out of the queue, unsent.
 * `draft` outlines it, as a prompt saved to send later (DraftBubble).
 */
@Composable
fun UserBubble(
    text: String, images: List<ImageRef> = emptyList(), pending: Boolean = false, cancelled: Boolean = false,
    draft: Boolean = false, sendNow: (suspend () -> Unit)? = null, cancelQueued: (suspend () -> Unit)? = null,
) {
    val faded = pending || cancelled
    val shape = RoundedCornerShape(Metrics.Corner.bubble)
    Row(Modifier.fillMaxWidth().testTag(if (draft) "draft-bubble" else "user-bubble"), horizontalArrangement = Arrangement.End) {
        Spacer(Modifier.width(40.dp))
        Column(horizontalAlignment = Alignment.End, verticalArrangement = Arrangement.spacedBy(2.dp)) {
            if (images.isNotEmpty()) {
                ImageGallery(images, height = 120.dp, alignEnd = true, modifier = Modifier.alpha(if (faded) 0.55f else 1f).padding(bottom = 2.dp))
            }
            if (text.isNotEmpty()) {
                SelectionContainer {
                    Text(
                        text, style = messageStyle(), color = if (draft) palette.text else palette.onAccent,
                        modifier = Modifier
                            .then(
                                if (draft) Modifier.background(palette.accent.copy(alpha = 0.12f), shape).dashedOutline(palette.accent, Metrics.Corner.bubble)
                                else Modifier.background(palette.accent.copy(alpha = if (faded) 0.55f else 1f), shape),
                            )
                            .padding(horizontal = 10.dp, vertical = 6.dp),
                    )
                }
            }
            if (pending && sendNow != null) QueuedCaption(sendNow, cancelQueued)
            else if (pending) Text("Queued", style = Type.caption2, color = palette.subtext)
            else if (cancelled) Text("Not sent", style = Type.caption2, color = palette.subtext)
        }
    }
}

/**
 * "Queued", with a way to send the prompt now, where rad interrupts the turn
 * and starts the next with it, and one to cancel it.
 */
@Composable
private fun QueuedCaption(sendNow: suspend () -> Unit, cancelQueued: (suspend () -> Unit)?) {
    val connection = LocalConnection.current
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    @Composable
    fun Action(label: String, tag: String, f: suspend () -> Unit) = Text(
        label, style = Type.caption2.copy(fontWeight = FontWeight.SemiBold), color = if (busy) palette.overlay else palette.accent,
        modifier = Modifier.clickable(enabled = !busy, role = Role.Button) {
            busy = true
            connection.scope.launch {
                try {
                    f()
                } catch (e: CancellationException) {
                    throw e
                } catch (e: Exception) {
                    error = e.message ?: "The request failed"
                } finally {
                    busy = false
                }
            }
        }.testTag(tag),
    )
    Row(verticalAlignment = Alignment.CenterVertically) {
        Text("Queued · ", style = Type.caption2, color = palette.subtext)
        Action("Send now", "send-now", sendNow)
        if (cancelQueued != null) {
            Text(" · ", style = Type.caption2, color = palette.subtext)
            Action("Cancel", "cancel-queued", cancelQueued)
        }
    }
    ErrorDialog(error, title = "Error") { error = null }
}

/**
 * The session's draft: a prompt saved to send later, from any device. It
 * shows as the prompt it will be, outlined, with Send, and Edit, which takes
 * it back into the composer. [enabled]: neither is under way, and the
 * session isn't archived.
 */
@Composable
fun DraftBubble(draft: Draft, enabled: Boolean, onEdit: () -> Unit, onSend: () -> Unit) {
    Column(Modifier.fillMaxWidth(), horizontalAlignment = Alignment.End, verticalArrangement = Arrangement.spacedBy(4.dp)) {
        UserBubble(draft.text, draft.images, draft = true)
        Row(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.CenterVertically) {
            Row(
                Modifier.semantics(mergeDescendants = true) {}.padding(end = 2.dp),
                horizontalArrangement = Arrangement.spacedBy(3.dp), verticalAlignment = Alignment.CenterVertically,
            ) {
                Icon(painterResource(Icons.draft), null, Modifier.size(13.dp), tint = palette.subtext)
                Text("Draft · not sent", style = Type.caption2, color = palette.subtext)
            }
            DraftButton("Edit", palette.surface0, palette.text, enabled, onEdit, Modifier.testTag("edit-draft"))
            DraftButton("Send", palette.accent, palette.onAccent, enabled, onSend, Modifier.testTag("send-draft"))
        }
    }
}

@Composable
private fun DraftButton(label: String, container: Color, content: Color, enabled: Boolean, onClick: () -> Unit, modifier: Modifier) {
    Button(
        onClick, modifier.heightIn(min = 30.dp), enabled = enabled,
        shape = RoundedCornerShape(Metrics.Corner.card),
        contentPadding = PaddingValues(horizontal = 12.dp, vertical = 4.dp),
        colors = ButtonDefaults.buttonColors(
            containerColor = container, contentColor = content,
            disabledContainerColor = container.copy(alpha = 0.35f), disabledContentColor = content.copy(alpha = 0.7f),
        ),
    ) {
        Text(label, style = Type.footnote.copy(fontWeight = FontWeight.SemiBold))
    }
}

/** A dashed outline inside the bounds, with rounded corners: what isn't sent yet. */
private fun Modifier.dashedOutline(color: Color, corner: Dp): Modifier = drawBehind {
    val stroke = 1.dp.toPx()
    drawRoundRect(
        color, topLeft = Offset(stroke / 2, stroke / 2), size = Size(size.width - stroke, size.height - stroke),
        cornerRadius = CornerRadius(corner.toPx() - stroke / 2),
        style = Stroke(stroke, pathEffect = PathEffect.dashPathEffect(floatArrayOf(5.dp.toPx(), 3.dp.toPx()))),
    )
}

@Composable
private fun ReasoningView(item: Item) {
    var expanded by rememberSaveable(item.id) { mutableStateOf(false) }
    Column(Modifier.animateContentSize()) {
        Row(
            Modifier.clickable { expanded = !expanded }.padding(vertical = 4.dp),
            horizontalArrangement = Arrangement.spacedBy(6.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Icon(painterResource(Icons.reasoning), null, Modifier.size(16.dp), tint = palette.subtext)
            Text(if (item.status == ItemStatus.IN_PROGRESS) "Thinking…" else "Thought", style = Type.footnote, color = palette.subtext)
            Icon(painterResource(Icons.chevronRight), null, Modifier.size(14.dp).rotate(if (expanded) 90f else 0f), tint = palette.subtext)
        }
        if (expanded) {
            SelectionContainer {
                Text(item.text ?: "", style = Type.footnote, color = palette.subtext, modifier = Modifier.fillMaxWidth().padding(start = 22.dp))
            }
        }
    }
}

@Composable
private fun ToolCallView(item: Item, children: List<Item>, store: SessionStore, allChildren: Map<String, List<Item>>) {
    var expanded by rememberSaveable(item.id) { mutableStateOf(false) }
    val tool = item.tool
    Column(
        Modifier.fillMaxWidth().background(palette.mantle, RoundedCornerShape(Metrics.Corner.card))
            .animateContentSize().padding(horizontal = Metrics.padding, vertical = 6.dp).testTag("tool-call"),
        verticalArrangement = Arrangement.spacedBy(6.dp),
    ) {
        Row(
            Modifier.fillMaxWidth().clickable { expanded = !expanded },
            horizontalArrangement = Arrangement.spacedBy(8.dp),
            verticalAlignment = Alignment.Top,
        ) {
            Icon(painterResource(toolIcon(tool?.kind)), null, Modifier.size(18.dp).padding(top = 1.dp), tint = palette.subtext)
            Text(
                tool?.title ?: tool?.name ?: "Tool", style = codeStyle(), color = palette.text,
                maxLines = if (expanded) Int.MAX_VALUE else 2, modifier = Modifier.weight(1f),
            )
            ToolStatus(item.status)
        }
        // Shown without expanding: a screenshot is often the point.
        item.images?.takeIf { it.isNotEmpty() }?.let { ImageGallery(it, height = 160.dp) }
        if (expanded) {
            tool?.input?.takeIf { !(it is JsonObject && it.isEmpty()) }?.let { ToolInputPreview(tool.name, it) }
            tool?.output?.takeIf { it.isNotEmpty() }?.let { CodeBlock(tool.exitCode?.let { c -> "Output (exit $c)" } ?: "Output", it) }
            if (children.isNotEmpty()) {
                Row(Modifier.height(IntrinsicSize.Min)) {
                    Box(Modifier.width(2.dp).fillMaxHeight().background(palette.surface1))
                    Column(Modifier.padding(start = 10.dp), verticalArrangement = Arrangement.spacedBy(Metrics.gap)) {
                        for (child in children) ItemView(child, store, allChildren)
                    }
                }
            }
        } else if (children.isNotEmpty()) {
            Text(
                "${children.size} sub-agent step${if (children.size == 1) "" else "s"}",
                style = messageStyle(Metrics.TOOL_LABEL), color = palette.subtext, modifier = Modifier.padding(start = 26.dp),
            )
        }
    }
}

private fun toolIcon(kind: ToolKind?) = when (kind ?: ToolKind.OTHER) {
    ToolKind.READ -> Icons.toolRead
    ToolKind.EDIT -> Icons.toolEdit
    ToolKind.EXECUTE -> Icons.terminal
    ToolKind.SEARCH -> Icons.toolSearch
    ToolKind.FETCH -> Icons.toolFetch
    ToolKind.THINK -> Icons.toolThink
    ToolKind.OTHER -> Icons.toolOther
}

@Composable
private fun ToolStatus(status: ItemStatus) {
    when (status) {
        ItemStatus.IN_PROGRESS -> CircularProgressIndicator(Modifier.size(14.dp), color = palette.subtext, strokeWidth = 2.dp)
        ItemStatus.PENDING -> Icon(painterResource(Icons.approval), "Waiting for approval", Modifier.size(16.dp), tint = palette.yellow)
        ItemStatus.FAILED -> Icon(painterResource(Icons.failed), "Failed", Modifier.size(16.dp), tint = palette.red)
        ItemStatus.COMPLETED -> Icon(painterResource(Icons.completed), "Done", Modifier.size(16.dp), tint = palette.green)
        else -> {}
    }
}

/**
 * Shows a tool's input the way a reviewer wants to see it: the command for
 * shell calls, a mini diff for edits, the content for writes, JSON otherwise.
 */
@Composable
fun ToolInputPreview(name: String?, input: JsonElement) {
    val command = input["command"]?.stringValue
    val old = input["old_string"]?.stringValue
    val new = input["new_string"]?.stringValue
    val edits = input["edits"] as? JsonArray
    val content = input["content"]?.stringValue
    when {
        command != null -> CodeBlock("Command", command)
        old != null && new != null -> EditPreview(listOf(old to new))
        edits != null -> EditPreview(edits.mapNotNull { e ->
            val o = e["old_string"]?.stringValue ?: return@mapNotNull null
            val n = e["new_string"]?.stringValue ?: return@mapNotNull null
            o to n
        })
        content != null -> CodeBlock("New content", content)
        else -> CodeBlock(name ?: "Input", input.pretty)
    }
}

@Composable
private fun EditPreview(edits: List<Pair<String, String>>) {
    val lines = edits.flatMap { (old, new) -> old.split("\n").map { "- $it" to false } + new.split("\n").map { "+ $it" to true } }
    Column(Modifier.fillMaxWidth().background(palette.crust, RoundedCornerShape(Metrics.Corner.inset)).padding(6.dp)) {
        Text("Change", style = messageStyle(Metrics.TOOL_LABEL).copy(fontWeight = FontWeight.SemiBold), color = palette.subtext, modifier = Modifier.padding(bottom = 4.dp))
        SelectionContainer {
            Column {
                for ((text, added) in lines.take(60)) {
                    Text(
                        text.ifEmpty { " " }, style = codeStyle(), color = palette.text,
                        modifier = Modifier.fillMaxWidth().background((if (added) palette.green else palette.red).copy(alpha = 0.15f)),
                    )
                }
            }
        }
        if (lines.size > 60) Text("… ${lines.size - 60} more lines", style = messageStyle(Metrics.TOOL_LABEL), color = palette.subtext)
    }
}

@Composable
fun CodeBlock(title: String, text: String) {
    var full by rememberSaveable(text) { mutableStateOf(false) }
    val lines = text.split("\n")
    val shown = if (full || lines.size <= 30) text else lines.take(30).joinToString("\n")
    Column(
        Modifier.fillMaxWidth().background(palette.crust, RoundedCornerShape(Metrics.Corner.inset)).padding(6.dp),
        verticalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        Text(title, style = messageStyle(Metrics.TOOL_LABEL).copy(fontWeight = FontWeight.SemiBold), color = palette.subtext)
        SelectionContainer {
            Text(shown, style = codeStyle(), color = palette.text, softWrap = false, modifier = Modifier.horizontalScroll(rememberScrollState()))
        }
        if (lines.size > 30 && !full) {
            Text("Show all ${lines.size} lines", style = messageStyle(Metrics.TOOL_LABEL), color = palette.accent, modifier = Modifier.clickable { full = true })
        }
    }
}

@Composable
private fun PlanView(plan: Plan?) {
    Column(
        Modifier.fillMaxWidth().background(palette.mantle, RoundedCornerShape(Metrics.Corner.card)).padding(Metrics.padding),
        verticalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        Row(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.CenterVertically) {
            Icon(painterResource(Icons.plan), null, Modifier.size(16.dp), tint = palette.text)
            Text("Plan", style = Type.footnote.copy(fontWeight = FontWeight.SemiBold), color = palette.text)
        }
        plan?.text?.takeIf { it.isNotEmpty() }?.let { AgentMarkdown(it) }
        for (e in plan?.entries ?: emptyList()) {
            val done = e.status == "completed"
            val working = e.status == "in_progress"
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.Top) {
                Icon(
                    painterResource(if (done) Icons.completed else if (working) Icons.inProgress else Icons.circle), null,
                    Modifier.size(16.dp).padding(top = 1.dp), tint = if (done) palette.green else if (working) palette.peach else palette.subtext,
                )
                Text(
                    e.content, style = Type.footnote.copy(textDecoration = if (done) TextDecoration.LineThrough else null),
                    color = if (done) palette.subtext else palette.text,
                )
            }
        }
    }
}

/** Marks a turn that did not finish normally. */
@Composable
fun TurnFooter(turn: Turn) {
    val failed = turn.status == TurnStatus.FAILED
    Row(horizontalArrangement = Arrangement.spacedBy(4.dp), verticalAlignment = Alignment.CenterVertically, modifier = Modifier.testTag("turn-footer")) {
        Icon(painterResource(if (failed) Icons.forceStop else Icons.stop), null, Modifier.size(12.dp), tint = if (failed) palette.red else palette.subtext)
        Text(if (failed) "Failed" else "Interrupted", style = Type.caption2, color = if (failed) palette.red else palette.subtext)
    }
}

/** A sample of the transcript at the chosen sizes, for Settings. */
@Composable
fun SettingsPreview(modifier: Modifier = Modifier) {
    Column(modifier, verticalArrangement = Arrangement.spacedBy(Metrics.gap)) {
        UserBubble("Add tests for the parser.")
        AgentMarkdown("Done. The new tests cover **empty input** and `\\r\\n` line endings, and all 42 pass:\n\n```\nswift test --filter ParserTests\n```")
    }
}
