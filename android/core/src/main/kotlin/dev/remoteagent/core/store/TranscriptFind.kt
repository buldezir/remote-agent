package dev.remoteagent.core.store

import dev.remoteagent.core.protocol.Approval
import dev.remoteagent.core.protocol.Item
import dev.remoteagent.core.protocol.ItemKind
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive

/**
 * Find in a session's transcript: the items whose text has the query. What
 * an item's text is follows rad's search (`SearchText` in
 * server/internal/model/search.go), so that find shows what search found.
 * Mirrors TranscriptFind in RAKit and web/src/store/transcriptFind.ts.
 */
object TranscriptFind {
    /**
     * The text of an item that search finds: prompts, replies, notices and
     * errors, a tool call's title and command, a plan, and a proposed plan's
     * or a question's text. Reasoning, tool output, approvals of tool calls
     * and sub-agents' items aren't searched.
     */
    fun searchText(item: Item): String {
        if (!item.parentItemId.isNullOrEmpty()) return ""
        return when (item.kind) {
            ItemKind.USER_MESSAGE, ItemKind.ASSISTANT_MESSAGE, ItemKind.NOTICE, ItemKind.ERROR -> item.text ?: ""
            ItemKind.TOOL_CALL -> {
                val tool = item.tool ?: return ""
                var s = tool.title ?: ""
                val command = toolCommand(tool.input)
                if (command.isNotEmpty() && !s.contains(command)) s += "\n" + command
                s
            }
            ItemKind.PLAN -> {
                val plan = item.plan ?: return ""
                val parts = plan.entries.orEmpty().map { it.content }.toMutableList()
                if (!plan.text.isNullOrEmpty()) parts += plan.text
                parts.joinToString("\n")
            }
            ItemKind.APPROVAL -> {
                val approval = item.approval ?: return ""
                when (approval.special) {
                    Approval.Special.PLAN -> approval.planText ?: ""
                    Approval.Special.QUESTION -> approval.questions.orEmpty().joinToString("\n") { it.question }
                    else -> ""
                }
            }
            else -> ""
        }
    }

    /**
     * The command a tool call ran, from its input's "command": a string, or a
     * list of arguments, which it joins with spaces; else "".
     */
    fun toolCommand(input: JsonElement?): String {
        val command = (input as? JsonObject)?.get("command") ?: return ""
        return when (command) {
            is JsonNull -> ""
            is JsonPrimitive -> if (command.isString) command.content else ""
            is JsonArray -> {
                val args = command.map { arg ->
                    when {
                        arg is JsonNull -> ""
                        arg is JsonPrimitive && arg.isString -> arg.content
                        else -> return ""
                    }
                }
                args.joinToString(" ")
            }
            else -> ""
        }
    }

    /**
     * The ids of the items that match, in the order given (the transcript's):
     * those whose text has the trimmed query, as one string, in any case.
     * None for an empty query.
     */
    fun findMatches(items: List<Item>, query: String): List<String> {
        val q = query.trim()
        if (q.isEmpty()) return emptyList()
        return items.filter { searchText(it).contains(q, ignoreCase = true) }.map { it.id }
    }
}
