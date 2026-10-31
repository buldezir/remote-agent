package dev.remoteagent.core.store

import dev.remoteagent.core.protocol.Item
import dev.remoteagent.core.protocol.ItemKind
import dev.remoteagent.core.protocol.ItemStatus
import dev.remoteagent.core.protocol.ToolKind
import dev.remoteagent.core.protocol.Turn
import dev.remoteagent.core.protocol.TurnStatus

/**
 * Runs of tool calls in a transcript, each folded into one row that sums it
 * up ("Ran 4 commands, edited 3 files, read 2 files") and opens to show the
 * calls. Mirrors ToolRuns in RAKit, web/src/store/toolRuns.ts and ra's
 * internal/client/toolruns.go.
 *
 * A run is the tool calls and the agent's thinking between two pieces of
 * conversation (a prompt, a reply, an approval, a plan, a notice, an error),
 * within one turn. It folds with two tool calls or more. A call that waits
 * for approval, failed, or shows images stands on its own, between runs. A
 * run is live while its turn runs and nothing has come after it: it shows
 * open, unless the user shut it, so that the user can watch the agent work,
 * and folds once the agent replies or the turn ends. A prompt queued while
 * the agent works isn't conversation yet: it doesn't end the run it was sent
 * during, and waits after it.
 */
object ToolRuns {
    /** The fewest tool calls that fold: one on its own stays as it is. */
    const val MINIMUM = 2

    /**
     * A run: its id is "tools:" and the first item's id, which stays as the
     * run grows; its items are its tool calls and the reasoning between them;
     * it's live while its turn runs and nothing has come after it.
     */
    data class Run(val id: String, val items: List<Item>, val live: Boolean) {
        val tools: List<Item> get() = items.filter { it.kind == ItemKind.TOOL_CALL }
        val parts: List<Part> get() = parts(tools)

        /** "Ran 4 commands, edited 3 files". */
        val title: String get() = title(parts)

        /** The agent is at work in it. */
        val working: Boolean get() = live || items.any { it.status == ItemStatus.IN_PROGRESS }

        operator fun contains(itemId: String) = items.any { it.id == itemId }
    }

    /** The calls of one kind in a run: "ran 4 commands". */
    data class Part(val kind: ToolKind, val count: Int, val text: String)

    /**
     * A row of the transcript: an item, a run's summary (followed by its
     * items when it's open), or the end of a turn that was interrupted or
     * failed, after its last item.
     */
    sealed interface Row {
        val key: String

        data class ItemRow(val item: Item) : Row {
            override val key get() = item.id
        }

        data class RunRow(val run: Run, val expanded: Boolean) : Row {
            override val key get() = run.id
        }

        data class TurnEnd(val turn: Turn) : Row {
            override val key get() = "turn-" + turn.id
        }
    }

    private sealed interface Segment {
        data class One(val item: Item) : Segment
        data class Many(val run: Run) : Segment
    }

    /**
     * The transcript's rows. A run is open as [expanded] says, by its id,
     * else while it's live. Without [grouping], every item is a row.
     */
    fun rows(items: List<Item>, turns: Map<String, Turn>, expanded: Map<String, Boolean> = emptyMap(), grouping: Boolean = true): List<Row> {
        // A prompt queued during a turn sits among its items, so the last is looked up.
        val last = buildMap { for (item in items) item.turnId?.let { put(it, item.id) } }
        fun MutableList<Row>.end(item: Item) {
            val tid = item.turnId ?: return
            val turn = turns[tid] ?: return
            if (last[tid] == item.id && (turn.status == TurnStatus.INTERRUPTED || turn.status == TurnStatus.FAILED)) add(Row.TurnEnd(turn))
        }
        return buildList {
            for (segment in if (grouping) segments(items, turns) else items.map { Segment.One(it) }) {
                when (segment) {
                    is Segment.One -> {
                        add(Row.ItemRow(segment.item))
                        end(segment.item)
                    }
                    is Segment.Many -> {
                        val run = segment.run
                        val open = expanded[run.id] ?: run.live
                        add(Row.RunRow(run, open))
                        if (open) run.items.forEach { add(Row.ItemRow(it)) }
                        end(run.items.last())
                    }
                }
            }
        }
    }

    /** The runs that fold, in order. */
    fun runs(items: List<Item>, turns: Map<String, Turn>): List<Run> =
        segments(items, turns).mapNotNull { (it as? Segment.Many)?.run }

    /** The run an item folds into, if it does. */
    fun runContaining(itemId: String, items: List<Item>, turns: Map<String, Turn>): Run? =
        runs(items, turns).firstOrNull { itemId in it }

    /**
     * Whether an item can be part of a run: reasoning, and tool calls that
     * neither wait for approval, failed, nor show images.
     */
    fun folds(item: Item): Boolean = when (item.kind) {
        ItemKind.REASONING -> true
        ItemKind.TOOL_CALL -> item.status != ItemStatus.PENDING && item.status != ItemStatus.FAILED && item.images.isNullOrEmpty()
        else -> false
    }

    private fun segments(items: List<Item>, turns: Map<String, Turn>): List<Segment> {
        val lastOfTurn = HashMap<String, Int>()
        items.forEachIndexed { i, item -> lastOfTurn[item.turnId ?: ""] = i }
        val out = ArrayList<Segment>()
        var i = 0
        while (i < items.size) {
            val first = items[i]
            if (!folds(first)) {
                out.add(Segment.One(first))
                i++
                continue
            }
            // The run's last item is j; k looks past the prompts queued meanwhile.
            var j = i
            var k = i
            while (k + 1 < items.size) {
                val next = items[k + 1]
                if (next.status == ItemStatus.QUEUED) {
                    k++
                } else if (folds(next) && next.turnId == first.turnId) {
                    k++
                    j = k
                } else {
                    break
                }
            }
            val span = items.subList(i, j + 1).toList()
            val run = span.filter { it.status != ItemStatus.QUEUED }
            if (run.count { it.kind == ItemKind.TOOL_CALL } >= MINIMUM) {
                val running = first.turnId?.let { turns[it] }?.status == TurnStatus.RUNNING
                out.add(Segment.Many(Run("tools:" + first.id, run, live = running && lastOfTurn[first.turnId ?: ""] == j)))
                span.filter { it.status == ItemStatus.QUEUED }.forEach { out.add(Segment.One(it)) }
            } else {
                span.forEach { out.add(Segment.One(it)) }
            }
            i = j + 1
        }
        return out
    }

    /** The order the kinds are summed up in. */
    private val kinds = listOf(ToolKind.EXECUTE, ToolKind.EDIT, ToolKind.READ, ToolKind.SEARCH, ToolKind.FETCH, ToolKind.THINK, ToolKind.OTHER)

    /**
     * The calls of each kind: files edited and read count once each, by
     * their paths (a call without paths is a file of its own); the rest
     * count by calls.
     */
    fun parts(tools: List<Item>): List<Part> = kinds.mapNotNull { kind ->
        val calls = tools.filter { (it.tool?.kind ?: ToolKind.OTHER) == kind }
        if (calls.isEmpty()) return@mapNotNull null
        val count = if (kind == ToolKind.EDIT || kind == ToolKind.READ) {
            buildSet {
                for (call in calls) {
                    val paths = call.tool?.paths.orEmpty()
                    if (paths.isEmpty()) add("\u0000" + call.id) else addAll(paths)
                }
            }.size
        } else {
            calls.size
        }
        Part(kind, count, phrase(kind, count))
    }

    private fun phrase(kind: ToolKind, n: Int): String {
        fun of(one: String, many: String) = "$n ${if (n == 1) one else many}"
        return when (kind) {
            ToolKind.EXECUTE -> "ran " + of("command", "commands")
            ToolKind.EDIT -> "edited " + of("file", "files")
            ToolKind.READ -> "read " + of("file", "files")
            ToolKind.SEARCH -> "searched " + of("time", "times")
            ToolKind.FETCH -> "fetched " + of("page", "pages")
            ToolKind.THINK -> "started " + of("agent", "agents")
            ToolKind.OTHER -> "used " + of("tool", "tools")
        }
    }

    /** The parts as a sentence: "Ran 4 commands, edited 3 files". */
    fun title(parts: List<Part>): String = capitalized(parts.joinToString(", ") { it.text })

    /** The text with its first letter in capitals, as a part that starts the summary. */
    fun capitalized(text: String): String = text.replaceFirstChar { it.uppercaseChar() }
}
