package dev.remoteagent.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import dev.remoteagent.core.protocol.Approval
import dev.remoteagent.core.protocol.ApprovalOption
import dev.remoteagent.core.protocol.Item
import dev.remoteagent.core.protocol.ItemStatus
import dev.remoteagent.core.protocol.OptionKind
import dev.remoteagent.core.protocol.Question
import dev.remoteagent.core.store.SessionStore
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch

/** A permission request, a plan to approve or questions to answer. Mirrors ApprovalView in ItemViews.swift. */
@Composable
fun ApprovalView(item: Item, store: SessionStore) {
    val approval = item.approval
    if (item.status == ItemStatus.PENDING && approval != null) PendingCard(item, approval, store) else ResolvedLine(item)
}

@Composable
private fun PendingCard(item: Item, a: Approval, store: SessionStore) {
    val connection = LocalConnection.current
    val answers = remember(item.id) { mutableStateMapOf<String, Set<String>>() }
    val own = remember(item.id) { mutableStateMapOf<String, String>() } // answers in the user's own words
    var askingReason by remember { mutableStateOf<ApprovalOption?>(null) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }

    // The labels chosen, then the user's own words.
    fun answerOf(q: Question): List<String> {
        val text = if (q.optionsOnly == true) "" else own[q.question].orEmpty().trim()
        return answers[q.question].orEmpty().sorted() + listOfNotNull(text.ifEmpty { null })
    }

    fun respond(o: ApprovalOption, message: String?) {
        busy = true
        error = null
        val flat = (a.questions ?: emptyList()).associate { it.question to answerOf(it).joinToString(", ") }.filterValues { it.isNotEmpty() }
        connection.scope.launch {
            try {
                store.respond(item, o, message?.takeIf { it.isNotEmpty() }, if (a.special == Approval.Special.QUESTION) flat else null)
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                error = e.message
            } finally {
                busy = false
            }
        }
    }

    val allAnswered = (a.questions ?: emptyList()).all { answerOf(it).isNotEmpty() }
    Column(
        Modifier.fillMaxWidth()
            // A card like the others, which the border marks out.
            .background(palette.mantle, RoundedCornerShape(Metrics.Corner.bubble))
            .border(1.dp, palette.yellow, RoundedCornerShape(Metrics.Corner.bubble))
            .padding(10.dp).testTag("approval"),
        verticalArrangement = Arrangement.spacedBy(Metrics.gap),
    ) {
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.Top) {
            val icon = when (a.special) {
                Approval.Special.QUESTION -> Icons.question
                Approval.Special.PLAN -> Icons.plan
                else -> Icons.approval
            }
            // Yellow, for warnings; the title in the text colour, since yellow text is faint on Latte.
            Icon(painterResource(icon), null, Modifier.size(18.dp), tint = palette.yellow)
            Text(a.title, style = Type.subheadline.copy(fontWeight = FontWeight.SemiBold), color = palette.text)
        }
        a.detail?.takeIf { it.isNotEmpty() }?.let { Text(it, style = Type.footnote, color = palette.subtext) }
        when {
            a.special == Approval.Special.PLAN && a.planText != null ->
                Column(Modifier.heightIn(max = 320.dp).verticalScroll(rememberScrollState())) { AgentMarkdown(a.planText!!) }
            a.special == Approval.Special.QUESTION -> for (q in a.questions ?: emptyList()) {
                QuestionView(
                    q, answers[q.question] ?: emptySet(), own[q.question].orEmpty(),
                    onTap = { label ->
                        val set = answers[q.question] ?: emptySet()
                        answers[q.question] = if (q.multiSelect == true) (if (label in set) set - label else set + label) else setOf(label)
                        // One answer: a label replaces the user's own.
                        if (q.multiSelect != true) own.remove(q.question)
                    },
                    onType = { text ->
                        own[q.question] = text
                        if (q.multiSelect != true && text.isNotBlank()) answers[q.question] = emptySet()
                    },
                )
            }
            a.input != null -> ToolInputPreview(a.toolName, a.input!!)
        }
        error?.let { Text(it, style = Type.caption, color = palette.red) }
        // When one label wraps, the other buttons grow to its height.
        Row(Modifier.fillMaxWidth().height(IntrinsicSize.Min), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            for (o in a.options) {
                val color = when (o.kind) {
                    OptionKind.DENY -> palette.red
                    OptionKind.ALLOW_SESSION -> palette.blue
                    else -> palette.green
                }
                val enabled = !busy && !(a.special == Approval.Special.QUESTION && o.kind != OptionKind.DENY && !allAnswered)
                Button(
                    onClick = { if (o.kind == OptionKind.DENY && a.special != Approval.Special.QUESTION) askingReason = o else respond(o, null) },
                    enabled = enabled,
                    modifier = Modifier.weight(1f).fillMaxHeight(),
                    shape = RoundedCornerShape(Metrics.Corner.card),
                    contentPadding = androidx.compose.foundation.layout.PaddingValues(horizontal = 8.dp, vertical = 6.dp),
                    colors = ButtonDefaults.buttonColors(
                        containerColor = color, contentColor = palette.onAccent,
                        disabledContainerColor = color.copy(alpha = 0.35f), disabledContentColor = palette.onAccent.copy(alpha = 0.7f),
                    ),
                ) {
                    Text(o.label, style = Type.footnote.copy(fontWeight = FontWeight.SemiBold), maxLines = 2, textAlign = TextAlign.Center)
                }
            }
        }
    }

    askingReason?.let { option ->
        DenyReasonDialog(onDeny = { reason -> respond(option, reason) }, onDismiss = { askingReason = null })
    }
}

@Composable
private fun DenyReasonDialog(onDeny: (String) -> Unit, onDismiss: () -> Unit) {
    var reason by rememberSaveable { mutableStateOf("") }
    AlertDialog(
        onDismissRequest = onDismiss,
        containerColor = palette.mantle,
        titleContentColor = palette.text,
        title = { Text("Reason (optional)") },
        text = {
            OutlinedTextField(
                reason, { reason = it }, placeholder = { Text("Tell the agent why", color = palette.overlay) },
                modifier = Modifier.fillMaxWidth().testTag("deny-reason"),
                colors = OutlinedTextFieldDefaults.colors(
                    focusedTextColor = palette.text, unfocusedTextColor = palette.text, focusedBorderColor = palette.accent,
                    unfocusedBorderColor = palette.surface1, cursorColor = palette.accent,
                ),
            )
        },
        confirmButton = { TextButton({ onDismiss(); onDeny(reason) }) { Text("Deny", color = palette.red) } },
        dismissButton = { TextButton(onDismiss) { Text("Cancel", color = palette.accent) } },
    )
}

@Composable
private fun QuestionView(q: Question, selected: Set<String>, own: String, onTap: (String) -> Unit, onType: (String) -> Unit) {
    Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        Text(q.question, style = Type.footnote.copy(fontWeight = FontWeight.Medium), color = palette.text)
        for (o in q.options) {
            val on = o.label in selected
            Row(
                Modifier.fillMaxWidth()
                    .background(if (on) palette.accent.copy(alpha = 0.15f) else Color.Transparent, RoundedCornerShape(Metrics.Corner.card))
                    .clickable { onTap(o.label) }.padding(6.dp).testTag("choice"),
                horizontalArrangement = Arrangement.spacedBy(8.dp),
                verticalAlignment = Alignment.Top,
            ) {
                val icon = if (q.multiSelect == true) (if (on) Icons.checkboxOn else Icons.checkboxOff) else (if (on) Icons.radioOn else Icons.radioOff)
                Icon(painterResource(icon), null, Modifier.size(18.dp), tint = if (on) palette.accent else palette.subtext)
                Column {
                    Text(o.label, style = Type.footnote, color = palette.text)
                    o.description?.takeIf { it.isNotEmpty() }?.let { Text(it, style = Type.caption, color = palette.subtext) }
                }
            }
        }
        // The last choice: an answer in the user's own words.
        if (q.optionsOnly != true) {
            val on = own.isNotBlank()
            Row(
                Modifier.fillMaxWidth()
                    .background(if (on) palette.accent.copy(alpha = 0.15f) else Color.Transparent, RoundedCornerShape(Metrics.Corner.card))
                    .padding(6.dp),
                horizontalArrangement = Arrangement.spacedBy(8.dp),
                verticalAlignment = Alignment.Top,
            ) {
                val icon = if (q.multiSelect == true) (if (on) Icons.checkboxOn else Icons.checkboxOff) else (if (on) Icons.radioOn else Icons.radioOff)
                Icon(painterResource(icon), null, Modifier.size(18.dp), tint = if (on) palette.accent else palette.subtext)
                BasicTextField(
                    own, onType,
                    modifier = Modifier.weight(1f).testTag("own-answer"),
                    textStyle = Type.footnote.copy(color = palette.text),
                    maxLines = 6,
                    cursorBrush = SolidColor(palette.accent),
                    decorationBox = { inner ->
                        Box {
                            if (own.isEmpty()) Text(if (q.options.isEmpty()) "Your answer…" else "Other…", style = Type.footnote, color = palette.overlay)
                            inner()
                        }
                    },
                )
            }
        }
    }
}

@Composable
private fun ResolvedLine(item: Item) {
    val a = item.approval
    val option = a?.options?.firstOrNull { it.id == a.decision?.optionId }
    val (icon, color, verb) = when {
        item.status == ItemStatus.RESOLVED && option?.kind == OptionKind.DENY -> Triple(Icons.failed, palette.red, option.label)
        item.status == ItemStatus.RESOLVED -> Triple(Icons.completed, palette.green, option?.label ?: "Allowed")
        item.status == ItemStatus.EXPIRED -> Triple(Icons.expired, palette.subtext, "Expired")
        else -> Triple(Icons.close, palette.subtext, "Cancelled")
    }
    Column(verticalArrangement = Arrangement.spacedBy(4.dp), modifier = Modifier.testTag("approval-resolved")) {
        Row(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.CenterVertically) {
            Icon(painterResource(icon), null, Modifier.size(14.dp), tint = color)
            Text("$verb: ${a?.title ?: ""}", style = Type.caption, color = color, maxLines = 2)
        }
        a?.decision?.answers?.toSortedMap()?.forEach { (q, ans) ->
            Text("$q → $ans", style = Type.caption2, color = palette.subtext)
        }
        a?.decision?.message?.takeIf { it.isNotEmpty() }?.let { Text("“$it”", style = Type.caption2, color = palette.subtext) }
    }
}
