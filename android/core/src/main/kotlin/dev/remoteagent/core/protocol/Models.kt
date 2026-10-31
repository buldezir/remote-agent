@file:UseSerializers(InstantSerializer::class)

package dev.remoteagent.core.protocol

import kotlinx.serialization.KSerializer
import kotlinx.serialization.Serializable
import kotlinx.serialization.SerializationException
import kotlinx.serialization.UseSerializers
import kotlinx.serialization.descriptors.SerialDescriptor
import kotlinx.serialization.descriptors.buildClassSerialDescriptor
import kotlinx.serialization.encoding.Decoder
import kotlinx.serialization.encoding.Encoder
import kotlinx.serialization.json.JsonDecoder
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonEncoder
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.long
import kotlinx.serialization.json.put
import java.time.Instant

// Wire entities. Mirrors server/internal/model in the Go server and
// apple/RAKit/Sources/RAKit/Protocol/Models.swift; see protocol/PROTOCOL.md.
// Unknown enum values decode to UNKNOWN so newer servers don't break older apps.

@Serializable
data class Project(
    val id: String,
    val path: String,
    val name: String,
    val isGitRepo: Boolean = false,
    val createdAt: Instant,
)

@Serializable
data class Workspace(
    val kind: Kind,
    val path: String,
    val branch: String? = null,
    val baseRef: String? = null,
    /** The folder the agent moved to and works in, such as a worktree it made itself; [branch] is that folder's. */
    val agentPath: String? = null,
) {
    @Serializable(with = Kind.Serializer::class)
    enum class Kind(override val wire: String) : WireEnum {
        ROOT("root"), WORKTREE("worktree"), UNKNOWN("unknown");

        object Serializer : LenientEnumSerializer<Kind>("Workspace.Kind", entries.toTypedArray(), UNKNOWN)
    }
}

@Serializable(with = SessionStatus.Serializer::class)
enum class SessionStatus(override val wire: String) : WireEnum {
    IDLE("idle"), RUNNING("running"), AWAITING_APPROVAL("awaiting_approval"), STOPPED("stopped"), ERROR("error"),
    UNKNOWN("unknown");

    object Serializer : LenientEnumSerializer<SessionStatus>("SessionStatus", entries.toTypedArray(), UNKNOWN)
}

@Serializable
data class Session(
    val id: String,
    val projectId: String,
    val harness: String,
    val model: String? = null,
    val effort: String? = null,
    val mode: String? = null,
    val workspace: Workspace,
    val status: SessionStatus,
    val nativeId: String? = null,
    val title: String,
    val error: String? = null,
    val archived: Boolean = false,
    /** A turn ended, or the agent asked for an answer, since a client last showed it. */
    val unread: Boolean = false,
    /** A prompt saved to send later, from any device (`session.saveDraft`); null: none. */
    val draft: Draft? = null,
    val context: ContextUsage? = null,
    val modelInfo: ModelInfo? = null,
    val backgroundTasks: List<BackgroundTask> = emptyList(),
    /** The session this one is a fork of (`ServerConnection.fork`); null: none. */
    val forkedFrom: ForkedFrom? = null,
    val createdAt: Instant,
    val updatedAt: Instant,
) {
    /**
     * The model to show: the one the agent reported, else the requested one.
     * Ids are named from the harness's model list when the agent gave no name.
     */
    fun modelName(harness: HarnessInfo?): String? {
        modelInfo?.name?.takeIf { it.isNotEmpty() }?.let { return it }
        val id = (modelInfo?.id ?: model)?.takeIf { it.isNotEmpty() } ?: return null
        return harness?.models?.firstOrNull { it.id == id }?.name ?: id
    }
}

/** The session a fork continues, and the turn it continues after. */
@Serializable
data class ForkedFrom(val sessionId: String, val turn: Int)

/**
 * A prompt kept in a session, unsent, to send later from any device with
 * `SessionStore.sendDraft`. Not the composer's text, which stays on the
 * device (`SessionStore.draft`).
 */
@Serializable
data class Draft(val text: String, val images: List<ImageRef> = emptyList())

/**
 * What the agent left running after its turn (a command it watches, a
 * subagent), while its process runs.
 */
@Serializable
data class BackgroundTask(val id: String, val kind: Kind, val description: String) {
    @Serializable(with = Kind.Serializer::class)
    enum class Kind(override val wire: String) : WireEnum {
        COMMAND("command"), AGENT("agent"), OTHER("other");

        object Serializer : LenientEnumSerializer<Kind>("BackgroundTask.Kind", entries.toTypedArray(), OTHER)
    }
}

/** The model and reasoning effort the agent reported it runs with. */
@Serializable
data class ModelInfo(val id: String? = null, val name: String? = null, val effort: String? = null)

/** How full the agent's context window was at its last model call. */
@Serializable
data class ContextUsage(val used: Long, val window: Long? = null) {
    /** 0..1, or null when the window is unknown. */
    val fraction: Double?
        get() {
            val w = window ?: return null
            if (w <= 0) return null
            return minOf(1.0, used.toDouble() / w.toDouble())
        }
}

@Serializable(with = TurnStatus.Serializer::class)
enum class TurnStatus(override val wire: String) : WireEnum {
    RUNNING("running"), COMPLETED("completed"), INTERRUPTED("interrupted"), FAILED("failed"), UNKNOWN("unknown");

    object Serializer : LenientEnumSerializer<TurnStatus>("TurnStatus", entries.toTypedArray(), UNKNOWN)
}

@Serializable
data class Usage(
    val inputTokens: Long? = null,
    val outputTokens: Long? = null,
    val cacheReadTokens: Long? = null,
    val cacheWriteTokens: Long? = null,
    val costUsd: Double? = null,
)

@Serializable
data class Turn(
    val id: String,
    val sessionId: String,
    val n: Int,
    val status: TurnStatus,
    val checkpointBefore: String? = null,
    val checkpointAfter: String? = null,
    val usage: Usage? = null,
    val error: String? = null,
    val startedAt: Instant,
    val endedAt: Instant? = null,
)

@Serializable(with = ItemKind.Serializer::class)
enum class ItemKind(override val wire: String) : WireEnum {
    USER_MESSAGE("user_message"), ASSISTANT_MESSAGE("assistant_message"), REASONING("reasoning"),
    TOOL_CALL("tool_call"), APPROVAL("approval"), PLAN("plan"), NOTICE("notice"), ERROR("error"), UNKNOWN("unknown");

    object Serializer : LenientEnumSerializer<ItemKind>("ItemKind", entries.toTypedArray(), UNKNOWN)
}

@Serializable(with = ItemStatus.Serializer::class)
enum class ItemStatus(override val wire: String) : WireEnum {
    QUEUED("queued"), IN_PROGRESS("in_progress"), COMPLETED("completed"), FAILED("failed"),
    PENDING("pending"), RESOLVED("resolved"), CANCELLED("cancelled"), EXPIRED("expired"), UNKNOWN("unknown");

    object Serializer : LenientEnumSerializer<ItemStatus>("ItemStatus", entries.toTypedArray(), UNKNOWN)
}

@Serializable(with = ToolKind.Serializer::class)
enum class ToolKind(override val wire: String) : WireEnum {
    READ("read"), EDIT("edit"), EXECUTE("execute"), SEARCH("search"), FETCH("fetch"), THINK("think"), OTHER("other");

    object Serializer : LenientEnumSerializer<ToolKind>("ToolKind", entries.toTypedArray(), OTHER)
}

@Serializable
data class ToolCall(
    val name: String,
    val kind: ToolKind = ToolKind.OTHER,
    val title: String? = null,
    val input: JsonElement? = null,
    val output: String? = null,
    val exitCode: Int? = null,
    val paths: List<String>? = null,
)

@Serializable(with = OptionKind.Serializer::class)
enum class OptionKind(override val wire: String) : WireEnum {
    ALLOW_ONCE("allow_once"), ALLOW_SESSION("allow_session"), DENY("deny"), UNKNOWN("unknown");

    object Serializer : LenientEnumSerializer<OptionKind>("OptionKind", entries.toTypedArray(), UNKNOWN)
}

@Serializable
data class ApprovalOption(val id: String, val label: String, val kind: OptionKind)

@Serializable
data class QuestionOption(val label: String, val description: String? = null)

@Serializable
data class Question(
    val question: String,
    val header: String? = null,
    val multiSelect: Boolean? = null,
    val options: List<QuestionOption> = emptyList(),
    /** The agent takes only one of the options: no answer in the user's own words. */
    val optionsOnly: Boolean? = null,
)

@Serializable
data class Decision(
    val optionId: String,
    val message: String? = null,
    val answers: Map<String, String>? = null,
    val at: Instant,
)

@Serializable
data class Approval(
    val toolItemId: String? = null,
    val toolName: String? = null,
    val title: String,
    val detail: String? = null,
    val input: JsonElement? = null,
    val options: List<ApprovalOption> = emptyList(),
    /** UNKNOWN (a kind this app doesn't know) shows as an ordinary approval, as null does. */
    val special: Special? = null,
    val planText: String? = null,
    val questions: List<Question>? = null,
    val decision: Decision? = null,
) {
    @Serializable(with = Special.Serializer::class)
    enum class Special(override val wire: String) : WireEnum {
        PLAN("plan"), QUESTION("question"), UNKNOWN("unknown");

        object Serializer : LenientEnumSerializer<Special>("Approval.Special", entries.toTypedArray(), UNKNOWN)
    }
}

@Serializable
data class PlanEntry(val content: String, val status: String)

@Serializable
data class Plan(val entries: List<PlanEntry>? = null, val text: String? = null)

/**
 * An image rad keeps: attached to a prompt, in a tool's output, or shown in
 * a reply. Fetch the bytes with `ServerConnection.imageData`. The id names
 * the content.
 */
@Serializable
data class ImageRef(
    val id: String,
    val mimeType: String,
    val width: Int? = null,
    val height: Int? = null,
    val size: Long,
) {
    /** Width over height, when the server knows both. */
    val aspectRatio: Double?
        get() {
            val w = width ?: return null
            val h = height ?: return null
            if (w <= 0 || h <= 0) return null
            return w.toDouble() / h.toDouble()
        }

    companion object {
        /**
         * The id in a reply's link to one of rad's images: rad points the
         * Markdown images in agents' replies at its copies, `rad-image:<id>`.
         */
        fun idLinkedBy(url: String): String? =
            if (url.startsWith("rad-image:")) url.removePrefix("rad-image:").ifEmpty { null } else null
    }
}

@Serializable
data class Item(
    val id: String,
    val sessionId: String,
    val turnId: String? = null,
    val parentItemId: String? = null,
    val order: Long,
    val kind: ItemKind,
    val status: ItemStatus,
    val text: String? = null,
    val tool: ToolCall? = null,
    val approval: Approval? = null,
    val plan: Plan? = null,
    /**
     * User messages: the attached images. Tool calls: images in the output.
     * Agent messages: the images the text links to (`ImageRef.idLinkedBy`).
     */
    val images: List<ImageRef>? = null,
    val createdAt: Instant,
    val updatedAt: Instant,
)

/** One change on a stream. The payload of an event type this app doesn't know is skipped. */
@Serializable(with = EventSerializer::class)
data class Event(
    val stream: String,
    val seq: Long,
    val type: String,
    val project: Project? = null,
    val session: Session? = null,
    val turn: Turn? = null,
    val item: Item? = null,
    val id: String? = null,
    val ts: Instant? = null,
)

// Payloads of unknown event types are skipped rather than failing the batch.
object EventSerializer : KSerializer<Event> {
    override val descriptor: SerialDescriptor = buildClassSerialDescriptor("Event")

    override fun deserialize(decoder: Decoder): Event {
        val input = decoder as? JsonDecoder ?: throw SerializationException("Event decodes from JSON only")
        val o = input.decodeJsonElement().jsonObject
        fun <T> payload(key: String, s: KSerializer<T>): T? =
            o[key]?.takeUnless { it is JsonNull }?.let { runCatching { input.json.decodeFromJsonElement(s, it) }.getOrNull() }
        fun required(key: String) = o[key]?.jsonPrimitive ?: throw SerializationException("event without $key")
        return Event(
            stream = required("stream").content,
            seq = required("seq").long,
            type = required("type").content,
            project = payload("project", Project.serializer()),
            session = payload("session", Session.serializer()),
            turn = payload("turn", Turn.serializer()),
            item = payload("item", Item.serializer()),
            id = (o["id"] as? JsonPrimitive)?.takeIf { it.isString }?.content,
            ts = (o["ts"] as? JsonPrimitive)?.takeIf { it.isString }?.content?.let(WireCoding::parseDate),
        )
    }

    override fun serialize(encoder: Encoder, value: Event) {
        val output = encoder as? JsonEncoder ?: throw SerializationException("Event encodes to JSON only")
        val json = output.json
        output.encodeJsonElement(buildJsonObject {
            put("stream", value.stream)
            put("seq", value.seq)
            put("type", value.type)
            value.project?.let { put("project", json.encodeToJsonElement(Project.serializer(), it)) }
            value.session?.let { put("session", json.encodeToJsonElement(Session.serializer(), it)) }
            value.turn?.let { put("turn", json.encodeToJsonElement(Turn.serializer(), it)) }
            value.item?.let { put("item", json.encodeToJsonElement(Item.serializer(), it)) }
            value.id?.let { put("id", it) }
            value.ts?.let { put("ts", it.toString()) }
        })
    }
}

@Serializable
data class Choice(
    val id: String,
    val name: String,
    val description: String? = null,
    /** For a model: the reasoning efforts it supports. */
    val efforts: List<Choice>? = null,
)

/**
 * A slash command: a prompt of "/<name>", optionally followed by text for
 * it, runs it rather than going to the model.
 */
@Serializable
data class SlashCommand(
    /** Without the slash. */
    val name: String,
    val description: String? = null,
    val kind: String? = null,
    val scope: String? = null,
) {
    /** The prompt that runs it. */
    val prompt: String get() = "/$name"
}

@Serializable
data class HarnessCaps(
    val resume: Boolean = false,
    val interrupt: Boolean = false,
    val setMode: Boolean = false,
    val freeModel: Boolean = false,
    val modelSelect: Boolean = false,
    /** `harness.update` runs the agent's own updater. Older servers leave these out. */
    val update: Boolean = false,
    /** `harness.login` signs the agent in again. */
    val login: Boolean = false,
    /** It can be the system agent (`ServerSettings`). */
    val oneShot: Boolean = false,
)

@Serializable
data class HarnessInfo(
    val id: String,
    val name: String,
    val protocol: String,
    val installed: Boolean,
    val version: String? = null,
    val authOk: Boolean,
    val hint: String? = null,
    val models: List<Choice>? = null,
    val efforts: List<Choice>? = null,
    val modes: List<Choice>? = null,
    val defaultMode: String? = null,
    /** The slash commands to offer in a menu, such as /compact. */
    val commands: List<SlashCommand>? = null,
    val caps: HarnessCaps = HarnessCaps(),
) {
    val usable: Boolean get() = installed && authOk

    /** The reasoning efforts to offer for a model (null or unlisted: the default model). */
    fun efforts(forModel: String?): List<Choice> =
        models?.firstOrNull { it.id == forModel }?.efforts ?: efforts ?: emptyList()
}

@Serializable
data class FSEntry(val name: String, val path: String, val isGitRepo: Boolean = false)

@Serializable
data class FSListing(val path: String, val parent: String? = null, val entries: List<FSEntry> = emptyList())

@Serializable
data class FileStat(
    val path: String,
    val oldPath: String? = null,
    val status: Status,
    val additions: Int = 0,
    val deletions: Int = 0,
    val binary: Boolean? = null,
) {
    @Serializable(with = Status.Serializer::class)
    enum class Status(override val wire: String) : WireEnum {
        ADDED("added"), MODIFIED("modified"), DELETED("deleted"), RENAMED("renamed"), UNKNOWN("unknown");

        object Serializer : LenientEnumSerializer<Status>("FileStat.Status", entries.toTypedArray(), UNKNOWN)
    }
}

@Serializable
data class Diff(
    val from: String,
    val to: String,
    val files: List<FileStat> = emptyList(),
    val patch: String = "",
    val truncated: Boolean = false,
)

@Serializable
data class ServerInfo(
    val serverId: String,
    val name: String,
    val protocolVersion: Int,
    val version: String,
    val roots: List<String>? = null,
    val deviceId: String? = null,
    /** The server runs shells for sessions (`GET /v1/terminal`). */
    val terminal: Boolean? = null,
    /**
     * The server keeps drafts (`Session.draft`). An older one would ignore
     * `NewSession.draft` and start the session with the prompt.
     */
    val drafts: Boolean? = null,
    /** The server forks sessions (`session.fork`); an older one answers `method_not_found`. */
    val forks: Boolean? = null,
)

/**
 * The agent, and its model, that does rad's own small jobs: naming new
 * sessions and their worktree branches from the first prompt.
 */
@Serializable
data class SystemAgent(
    val harness: String,
    /** Null: the harness's default. */
    val model: String? = null,
    /** Null: the model's default. */
    val effort: String? = null,
)

/** The settings clients change (`server.settings`, `server.setSettings`). */
@Serializable
data class ServerSettings(
    /** Null: none, and a session's title is its prompt's first line. */
    val systemAgent: SystemAgent? = null,
)

/** What `server.checkUpdate` found out about rad's releases. */
@Serializable
data class UpdateInfo(
    val current: String,
    /** Null: the lookup failed, and `reason` says why. */
    val latest: String? = null,
    /** The latest release's page. */
    val url: String? = null,
    /** `latest` is newer than `current`. */
    val available: Boolean = false,
    /** How an update installs, `VIA_RAD` or `VIA_APP` (unknown ones kept); null: it can't, and `reason` says why. */
    val via: String? = null,
    val reason: String? = null,
    /** The titles of the sessions a restart would interrupt. */
    val busy: List<String> = emptyList(),
) {
    companion object {
        /** rad downloads the release and restarts as it. */
        const val VIA_RAD = "rad"
        /** The Remote Agent Server app updates itself, and its rad. */
        const val VIA_APP = "app"
    }
}

/**
 * A maintenance job on the server: updating rad or an agent, or signing an
 * agent in. Clients poll it with `task.get` while it runs (ServerTaskProgress).
 */
@Serializable
data class ServerTask(
    val id: String,
    /** `UPDATE_SERVER`, `UPDATE_AGENT` or `LOGIN`; a newer server's kinds are kept as sent. */
    val kind: String,
    /** The agent, for an update or a login. */
    val harness: String? = null,
    /** UNKNOWN (a status this app doesn't know) counts as ended, as failed. */
    val status: Status,
    /** The command's output as a terminal shows it, without colours: its last lines. */
    val output: String = "",
    /** The last web address in the output, such as a sign-in page. */
    val url: String? = null,
    /** It takes `task.input` now, such as a code to paste. */
    val input: Boolean = false,
    val error: String? = null,
    /** For an update, the version installed; for a login, the agent's. */
    val version: String? = null,
    val startedAt: Instant,
    val endedAt: Instant? = null,
) {
    @Serializable(with = Status.Serializer::class)
    enum class Status(override val wire: String) : WireEnum {
        RUNNING("running"), SUCCEEDED("succeeded"), FAILED("failed"), CANCELLED("cancelled"), UNKNOWN("unknown");

        object Serializer : LenientEnumSerializer<Status>("ServerTask.Status", entries.toTypedArray(), UNKNOWN)
    }

    val running: Boolean get() = status == Status.RUNNING

    companion object {
        const val UPDATE_SERVER = "update_server"
        const val UPDATE_AGENT = "update_agent"
        const val LOGIN = "login"
    }
}

/**
 * A text frame on a terminal socket. The shell's output and the user's
 * input travel as binary frames.
 */
@Serializable
data class TerminalMessage(
    /** attached, reset, exit or error from rad; resize or close from a client. */
    val type: String,
    val id: String? = null,
    val offset: Long? = null,
    val reset: Boolean? = null,
    val cols: Int? = null,
    val rows: Int? = null,
    val exitCode: Int? = null,
    val code: String? = null,
    val message: String? = null,
)

/** The `result` of a subscribe call. */
@Serializable
data class SubscribeResult(val stream: String, val seq: Long)

