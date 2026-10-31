import Foundation

// Wire entities. Mirrors server/internal/model in the Go server; see protocol/PROTOCOL.md.
// Unknown enum values decode to `.unknown` so newer servers don't break older apps.

public struct Project: Codable, Hashable, Identifiable, Sendable {
    public var id: String
    public var path: String
    public var name: String
    public var isGitRepo: Bool
    public var createdAt: Date
}

public struct Workspace: Codable, Hashable, Sendable {
    public enum Kind: String, Codable, Sendable { case root, worktree }
    public var kind: Kind
    public var path: String
    public var branch: String?
    public var baseRef: String?
    /// The folder the agent moved to and works in, such as a worktree it made itself; `branch` is that folder's.
    public var agentPath: String?
}

public enum SessionStatus: String, Codable, Sendable {
    case idle, running, awaitingApproval = "awaiting_approval", stopped, error, unknown
    public init(from decoder: Decoder) throws {
        self = SessionStatus(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .unknown
    }
}

public struct Session: Codable, Hashable, Identifiable, Sendable {
    public var id: String
    public var projectId: String
    public var harness: String
    public var model: String?
    public var effort: String?
    public var mode: String?
    public var workspace: Workspace
    public var status: SessionStatus
    public var nativeId: String?
    public var title: String
    public var error: String?
    public var archived: Bool
    /// A turn ended, or the agent asked for an answer, since a device last
    /// showed the session (`ServerConnection.markRead`). Absent: read.
    public var unread: Bool?
    /// A prompt saved to send later, from any device (`ServerConnection.saveDraft`,
    /// `SessionStore.sendDraft`). Absent: none.
    public var draft: Draft?
    public var context: ContextUsage?
    public var modelInfo: ModelInfo?
    public var backgroundTasks: [BackgroundTask]?
    /// The session this one is a fork of (`ServerConnection.fork`). Absent: none.
    public var forkedFrom: ForkedFrom?
    public var createdAt: Date
    public var updatedAt: Date
}

/// The session a fork continues, and the turn it continues after.
public struct ForkedFrom: Codable, Hashable, Sendable {
    public var sessionId: String
    public var turn: Int
}

extension Session {
    public var isUnread: Bool { unread == true }
    public var hasDraft: Bool { draft != nil }

    /// The model to show: the one the agent reported, else the requested one.
    /// Ids are named from the harness's model list when the agent gave no name.
    public func modelName(in harness: HarnessInfo?) -> String? {
        if let name = modelInfo?.name, !name.isEmpty { return name }
        guard let id = modelInfo?.id ?? model, !id.isEmpty else { return nil }
        return harness?.models?.first(where: { $0.id == id })?.name ?? id
    }
}

/// A prompt kept in a session, unsent, for any device to send later. The
/// transcript shows it at its foot.
public struct Draft: Codable, Hashable, Sendable {
    public var text: String
    public var images: [ImageRef]?

    public init(text: String, images: [ImageRef]? = nil) {
        self.text = text
        self.images = images
    }
}

/// Work the agent left running, such as a command it watches or a subagent,
/// while its process runs. It reports back when the task ends.
public struct BackgroundTask: Codable, Hashable, Identifiable, Sendable {
    public var id: String
    public var kind: Kind
    public var description: String

    public enum Kind: String, Codable, Sendable {
        case command, agent, other
        public init(from decoder: Decoder) throws {
            self = Kind(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .other
        }
    }
}

/// The model and reasoning effort the agent reported it runs with.
public struct ModelInfo: Codable, Hashable, Sendable {
    public var id: String?
    public var name: String?
    public var effort: String?
}

/// How full the agent's context window was at its last model call.
public struct ContextUsage: Codable, Hashable, Sendable {
    public var used: Int64
    public var window: Int64?

    /// 0...1, or nil when the window is unknown.
    public var fraction: Double? {
        guard let w = window, w > 0 else { return nil }
        return min(1, Double(used) / Double(w))
    }
}

public enum TurnStatus: String, Codable, Sendable {
    case running, completed, interrupted, failed, unknown
    public init(from decoder: Decoder) throws {
        self = TurnStatus(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .unknown
    }
}

public struct Usage: Codable, Hashable, Sendable {
    public var inputTokens: Int64?
    public var outputTokens: Int64?
    public var cacheReadTokens: Int64?
    public var cacheWriteTokens: Int64?
    public var costUsd: Double?
}

public struct Turn: Codable, Hashable, Identifiable, Sendable {
    public var id: String
    public var sessionId: String
    public var n: Int
    public var status: TurnStatus
    public var checkpointBefore: String?
    public var checkpointAfter: String?
    public var usage: Usage?
    public var error: String?
    public var startedAt: Date
    public var endedAt: Date?
}

public enum ItemKind: String, Codable, Sendable {
    case userMessage = "user_message", assistantMessage = "assistant_message", reasoning
    case toolCall = "tool_call", approval, plan, notice, error, unknown
    public init(from decoder: Decoder) throws {
        self = ItemKind(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .unknown
    }
}

public enum ItemStatus: String, Codable, Sendable {
    case queued, inProgress = "in_progress", completed, failed
    case pending, resolved, cancelled, expired, unknown
    public init(from decoder: Decoder) throws {
        self = ItemStatus(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .unknown
    }
}

public enum ToolKind: String, Codable, Sendable {
    case read, edit, execute, search, fetch, think, other
    public init(from decoder: Decoder) throws {
        self = ToolKind(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .other
    }
}

public struct ToolCall: Codable, Hashable, Sendable {
    public var name: String
    public var kind: ToolKind
    public var title: String?
    public var input: JSONValue?
    public var output: String?
    public var exitCode: Int?
    public var paths: [String]?
}

public enum OptionKind: String, Codable, Sendable {
    case allowOnce = "allow_once", allowSession = "allow_session", deny, unknown
    public init(from decoder: Decoder) throws {
        self = OptionKind(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .unknown
    }
}

public struct ApprovalOption: Codable, Hashable, Identifiable, Sendable {
    public var id: String
    public var label: String
    public var kind: OptionKind
}

public struct QuestionOption: Codable, Hashable, Sendable {
    public var label: String
    public var description: String?
}

public struct Question: Codable, Hashable, Sendable {
    public var question: String
    public var header: String?
    public var multiSelect: Bool?
    public var options: [QuestionOption]
    /// The agent takes only one of the options: no answer in the user's own words.
    public var optionsOnly: Bool?
}

public struct Decision: Codable, Hashable, Sendable {
    public var optionId: String
    public var message: String?
    public var answers: [String: String]?
    public var at: Date
}

public struct Approval: Codable, Hashable, Sendable {
    public enum Special: String, Codable, Sendable { case plan, question }
    public var toolItemId: String?
    public var toolName: String?
    public var title: String
    public var detail: String?
    public var input: JSONValue?
    public var options: [ApprovalOption]
    public var special: Special?
    public var planText: String?
    public var questions: [Question]?
    public var decision: Decision?

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        toolItemId = try c.decodeIfPresent(String.self, forKey: .toolItemId)
        toolName = try c.decodeIfPresent(String.self, forKey: .toolName)
        title = try c.decode(String.self, forKey: .title)
        detail = try c.decodeIfPresent(String.self, forKey: .detail)
        input = try c.decodeIfPresent(JSONValue.self, forKey: .input)
        options = try c.decodeIfPresent([ApprovalOption].self, forKey: .options) ?? []
        special = (try? c.decodeIfPresent(String.self, forKey: .special)).flatMap { Special(rawValue: $0) }
        planText = try c.decodeIfPresent(String.self, forKey: .planText)
        questions = try c.decodeIfPresent([Question].self, forKey: .questions)
        decision = try c.decodeIfPresent(Decision.self, forKey: .decision)
    }
}

public struct PlanEntry: Codable, Hashable, Sendable {
    public var content: String
    public var status: String
}

public struct Plan: Codable, Hashable, Sendable {
    public var entries: [PlanEntry]?
    public var text: String?
}

/// An image rad keeps: attached to a prompt, in a tool's output, or shown in
/// a reply. Fetch the bytes with `ServerConnection.imageData(_:)`. The id
/// names the content.
public struct ImageRef: Codable, Hashable, Identifiable, Sendable {
    public var id: String
    public var mimeType: String
    public var width: Int?
    public var height: Int?
    public var size: Int64

    public init(id: String, mimeType: String, width: Int? = nil, height: Int? = nil, size: Int64) {
        self.id = id
        self.mimeType = mimeType
        self.width = width
        self.height = height
        self.size = size
    }

    /// Width over height, when the server knows both.
    public var aspectRatio: Double? {
        guard let w = width, let h = height, w > 0, h > 0 else { return nil }
        return Double(w) / Double(h)
    }

    /// The id in a reply's link to one of rad's images: rad points the
    /// Markdown images in agents' replies at its copies, `rad-image:<id>`.
    public static func id(linkedBy url: URL) -> String? {
        guard url.scheme == "rad-image" else { return nil }
        let id = url.absoluteString.dropFirst("rad-image:".count)
        return id.isEmpty ? nil : String(id)
    }
}

public struct Item: Codable, Hashable, Identifiable, Sendable {
    public var id: String
    public var sessionId: String
    public var turnId: String?
    public var parentItemId: String?
    public var order: Int64
    public var kind: ItemKind
    public var status: ItemStatus
    public var text: String?
    public var tool: ToolCall?
    public var approval: Approval?
    public var plan: Plan?
    /// User messages: the attached images. Tool calls: images in the output.
    /// Agent messages: the images the text links to (`ImageRef.id(linkedBy:)`).
    public var images: [ImageRef]?
    public var createdAt: Date
    public var updatedAt: Date

    public init(id: String, sessionId: String, turnId: String? = nil, parentItemId: String? = nil, order: Int64,
                kind: ItemKind, status: ItemStatus, text: String? = nil, images: [ImageRef]? = nil,
                createdAt: Date = .now, updatedAt: Date = .now) {
        self.id = id
        self.sessionId = sessionId
        self.turnId = turnId
        self.parentItemId = parentItemId
        self.order = order
        self.kind = kind
        self.status = status
        self.text = text
        self.images = images
        self.createdAt = createdAt
        self.updatedAt = updatedAt
    }
}

public struct Event: Decodable, Sendable {
    public var stream: String
    public var seq: Int64
    public var type: String
    public var project: Project?
    public var session: Session?
    public var turn: Turn?
    public var item: Item?
    public var id: String?
    public var ts: Date?

    enum CodingKeys: String, CodingKey { case stream, seq, type, project, session, turn, item, id, ts }

    // Payloads of unknown event types are skipped rather than failing the batch.
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        stream = try c.decode(String.self, forKey: .stream)
        seq = try c.decode(Int64.self, forKey: .seq)
        type = try c.decode(String.self, forKey: .type)
        project = try? c.decodeIfPresent(Project.self, forKey: .project)
        session = try? c.decodeIfPresent(Session.self, forKey: .session)
        turn = try? c.decodeIfPresent(Turn.self, forKey: .turn)
        item = try? c.decodeIfPresent(Item.self, forKey: .item)
        id = try c.decodeIfPresent(String.self, forKey: .id)
        ts = try? c.decodeIfPresent(Date.self, forKey: .ts)
    }
}

public struct Choice: Codable, Hashable, Identifiable, Sendable {
    public var id: String
    public var name: String
    public var description: String?
    public var efforts: [Choice]?  // for a model: the reasoning efforts it supports
}

/// A slash command: a prompt of "/<name>", optionally followed by text for
/// it, runs it rather than going to the model.
public struct SlashCommand: Codable, Hashable, Identifiable, Sendable {
    public var name: String  // without the slash
    public var description: String?
    public var kind: String? = nil
    public var scope: String? = nil
    public var id: String { name }

    /// The prompt that runs it.
    public var prompt: String { "/" + name }
}

public struct HarnessCaps: Codable, Hashable, Sendable {
    public var resume: Bool
    public var interrupt: Bool
    public var setMode: Bool
    public var freeModel: Bool
    public var modelSelect: Bool
    /// `harness.update` runs the agent's own updater.
    public var update: Bool?
    /// `harness.login` signs the agent in again.
    public var login: Bool?
    /// The agent can be the system agent (`ServerSettings`).
    public var oneShot: Bool?
    /// The agent takes a prompt into the running turn (`session.steer`).
    public var steer: Bool?
}

public struct HarnessInfo: Codable, Hashable, Identifiable, Sendable {
    public var id: String
    public var name: String
    public var `protocol`: String
    public var installed: Bool
    public var version: String?
    public var authOk: Bool
    public var hint: String?
    public var models: [Choice]?
    public var efforts: [Choice]?
    public var modes: [Choice]?
    public var defaultMode: String?
    /// The slash commands to offer in a menu, such as /compact.
    public var commands: [SlashCommand]?
    public var caps: HarnessCaps

    public var usable: Bool { installed && authOk }

    /// The reasoning efforts to offer for a model (nil or unlisted: the default model).
    public func efforts(forModel id: String?) -> [Choice] {
        models?.first(where: { $0.id == id })?.efforts ?? efforts ?? []
    }
}

public struct FSEntry: Codable, Hashable, Identifiable, Sendable {
    public var name: String
    public var path: String
    public var isGitRepo: Bool
    public var id: String { path }
}

public struct FSListing: Codable, Hashable, Sendable {
    public var path: String
    public var parent: String?
    public var entries: [FSEntry]
}

public struct FileStat: Codable, Hashable, Identifiable, Sendable {
    public enum Status: String, Codable, Sendable { case added, modified, deleted, renamed }
    public var path: String
    public var oldPath: String?
    public var status: Status
    public var additions: Int
    public var deletions: Int
    public var binary: Bool?
    public var id: String { path }
}

public struct Diff: Codable, Hashable, Sendable {
    public var from: String
    public var to: String
    public var files: [FileStat]
    public var patch: String
    public var truncated: Bool
}

public struct ServerInfo: Codable, Hashable, Sendable {
    public var serverId: String
    public var name: String
    public var protocolVersion: Int
    public var version: String
    public var roots: [String]?
    public var deviceId: String?
    /// The server runs shells for sessions (`GET /v1/terminal`).
    public var terminal: Bool?
    /// The server keeps drafts (`Session.draft`). An older one would start a
    /// session created as a draft with its prompt.
    public var drafts: Bool?
    /// The server forks sessions (`session.fork`).
    public var forks: Bool?
}

/// A text frame on a terminal socket. The shell's output and the user's
/// input travel as binary frames.
public struct TerminalMessage: Codable, Hashable, Sendable {
    public var type: String  // attached, reset, exit or error from rad; resize or close from a client
    public var id: String?
    public var offset: UInt64?
    public var reset: Bool?
    public var cols: Int?
    public var rows: Int?
    public var exitCode: Int?
    public var code: String?
    public var message: String?

    public init(type: String, cols: Int? = nil, rows: Int? = nil) {
        self.type = type
        self.cols = cols
        self.rows = rows
    }
}

/// The agent, and its model, that names sessions and their worktree branches.
public struct SystemAgent: Codable, Hashable, Sendable {
    public var harness: String
    public var model: String?
    public var effort: String?

    public init(harness: String, model: String? = nil, effort: String? = nil) {
        self.harness = harness
        self.model = model
        self.effort = effort
    }
}

/// The settings clients change on a server (`server.settings`).
public struct ServerSettings: Codable, Hashable, Sendable {
    public var systemAgent: SystemAgent?

    public init(systemAgent: SystemAgent? = nil) {
        self.systemAgent = systemAgent
    }
}

/// What `server.checkUpdate` found out about rad's releases.
public struct UpdateInfo: Codable, Hashable, Sendable {
    public var current: String
    /// nil: the lookup failed, and `reason` says why.
    public var latest: String?
    /// The latest release's page.
    public var url: String?
    /// `latest` is newer than `current`.
    public var available: Bool
    /// How an update installs: "rad" or "app"; nil: it can't, and `reason` says why.
    public var via: String?
    public var reason: String?
    /// The sessions a restart would interrupt.
    public var busy: [String]

    enum CodingKeys: String, CodingKey { case current, latest, url, available, via, reason, busy }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        current = try c.decode(String.self, forKey: .current)
        latest = try c.decodeIfPresent(String.self, forKey: .latest)
        url = try c.decodeIfPresent(String.self, forKey: .url)
        available = try c.decodeIfPresent(Bool.self, forKey: .available) ?? false
        via = try c.decodeIfPresent(String.self, forKey: .via)
        reason = try c.decodeIfPresent(String.self, forKey: .reason)
        busy = try c.decodeIfPresent([String].self, forKey: .busy) ?? []
    }

    /// Whether the client can install `latest`.
    public var installable: Bool { available && !(via ?? "").isEmpty }
}

/// A maintenance job on the server: updating rad or an agent, or signing an
/// agent in. Clients poll it (`task.get`) while it runs.
public struct ServerTask: Codable, Hashable, Identifiable, Sendable {
    public enum Kind: String, Codable, Sendable {
        case updateServer = "update_server", updateAgent = "update_agent", login, unknown
        public init(from decoder: Decoder) throws {
            self = Kind(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .unknown
        }
    }

    public enum Status: String, Codable, Sendable {
        case running, succeeded, failed, cancelled, unknown
        public init(from decoder: Decoder) throws {
            self = Status(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .unknown
        }
    }

    public var id: String
    public var kind: Kind
    public var harness: String?
    public var status: Status
    /// What the command printed, as a terminal shows it, without colours.
    public var output: String
    /// The last web address in the output, such as a sign-in page.
    public var url: String?
    /// It takes typed lines now (`task.input`), such as a code to paste.
    public var input: Bool?
    public var error: String?
    /// For an update, the version installed.
    public var version: String?
    public var startedAt: Date
    public var endedAt: Date?

    public var isRunning: Bool { status == .running }
}
