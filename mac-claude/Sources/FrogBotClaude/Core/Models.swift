import Foundation

// 与 Go 后端、网页版 web/src/api.ts 的类型一一对应。

struct BotProfile: Codable, Hashable, Identifiable {
    var id: String
    var name: String
    var instructions: String?
    var avatarSlot: Int?
}

struct DeviceGroup: Codable, Hashable, Identifiable {
    var id: String
    var name: String
    var online: Bool
    var bots: [BotProfile]
}

struct AuthInfo: Codable {
    var enabled: Bool
    var authenticated: Bool?
    var username: String?
}

struct BotInfo: Codable {
    var name: String?
    var chat_model: String?
    var agent_model: String?
    var policy: String?
    var max_concurrent: Int?
    var stall_sec: Int?
    var version: String?
}

struct Attachment: Codable, Hashable, Identifiable {
    var path: String
    var name: String
    var size: Int
    var mime: String?

    var id: String { path }

    init(path: String, name: String, size: Int, mime: String?) {
        self.path = path; self.name = name; self.size = size; self.mime = mime
    }

    init(_ j: JSON) {
        path = j["path"].str
        name = j["name"].string ?? (j["path"].str as NSString).lastPathComponent
        size = j["size"].int ?? 0
        mime = j["mime"].string
    }

    var isImage: Bool {
        if let mime, !mime.isEmpty { return mime.hasPrefix("image/") && !mime.contains("svg") }
        let ext = (name as NSString).pathExtension.lowercased()
        return ["png", "jpg", "jpeg", "gif", "webp", "heic"].contains(ext)
    }

    var json: [String: Any] {
        var d: [String: Any] = ["path": path, "name": name, "size": size]
        if let mime { d["mime"] = mime }
        return d
    }
}

/// 一台设备上的一个 Bot：服务器 + 设备（本机为 "local"，互联设备为它的 ID）+ Bot ID。
struct BotRef: Hashable, Codable {
    var server: UUID
    var device: String
    var bot: String

    var key: String { "\(server.uuidString)|\(device)|\(bot)" }

    init(server: UUID, device: String, bot: String) {
        self.server = server; self.device = device; self.bot = bot
    }

    init?(key: String) {
        let parts = key.split(separator: "|", omittingEmptySubsequences: false).map(String.init)
        guard parts.count == 3, let id = UUID(uuidString: parts[0]) else { return nil }
        self.init(server: id, device: parts[1], bot: parts[2])
    }
}

// ---------- 时间线里的东西 ----------

struct Card: Hashable {
    var id: String
    var question: String
    var options: [String]
    var status: String // pending | answered | dismissed
    var answer: String?

    init(_ j: JSON) {
        id = j["id"].str
        question = j["question"].str
        options = j["options"].array.compactMap { $0.string }
        status = j["status"].string ?? "pending"
        answer = j["answer"].string
    }
}

struct SecretRequest: Hashable {
    var id: String
    var name: String
    var description: String?
    var status: String // pending | saved | cancelled
    var exists: Bool

    init(_ j: JSON) {
        id = j["id"].str
        name = j["name"].str
        description = j["description"].string
        status = j["status"].string ?? "pending"
        exists = j["exists"].bool
    }
}

struct Reaction: Hashable {
    var by: String // user | bot
    var emoji: String
}

struct Msg: Identifiable, Hashable {
    var id: String
    var role: String // user | assistant
    var text: String
    var card: Card?
    var secret: SecretRequest?
    var tools: [String]
    var createdAt: String
    var created: Double
    var thinking: String?
    var thinkingMs: Double?
    var files: [Attachment]
    var reactions: [Reaction]

    init(_ j: JSON) {
        id = j["id"].str
        role = j["role"].str
        text = j["text"].str
        card = j["card"].isNull ? nil : Card(j["card"])
        secret = j["secret"].isNull ? nil : SecretRequest(j["secret"])
        tools = j["tools"].array.compactMap { $0.string }
        createdAt = j["created_at"].str
        created = Stamp.ms(j["created_at"].string)
        thinking = j["thinking"].string
        thinkingMs = j["thinking_ms"].double
        files = j["files"].array.map(Attachment.init)
        reactions = j["reactions"].array.map { Reaction(by: $0["by"].str, emoji: $0["emoji"].str) }
    }
}

struct Step: Identifiable, Hashable {
    var id: String
    var tool: String
    var args: JSON
    var thought: String
    var output: String?
    var error: Bool = false
    var denied: Bool = false
    var done: Bool = false
    var at: Double
    var doneAt: Double?
}

enum TaskStatus: String {
    case queued, running, paused, completed, failed, cancelled

    var terminal: Bool { self == .completed || self == .failed || self == .cancelled }
}

struct BotTask: Identifiable, Hashable {
    var id: String
    var title: String
    var input: String
    var status: TaskStatus
    var steps: [Step] = []
    var text: String = ""
    var thinking: String = ""
    var output: String?
    var error: String?
    var stopReason: String?
    var created: Double
    var started: Double?
    var ended: Double?
    var awaiting: Int = 0
    var prio: Double
    var pausing: Bool = false
    var files: [String] = []
    var summary: String?
    var duration: Double?
    var lastEvent: Double
    var lastBeat: Double
    var activeMs: Double = 0

    var isOpen: Bool { !status.terminal }
    var isLive: Bool { status == .queued || status == .running }

    /// 已经干了多久：之前各段累计 + 当前这段。
    func elapsed(_ now: Double) -> Double {
        duration ?? (activeMs + (started.map { now - $0 } ?? 0))
    }
}

struct Approval: Identifiable, Hashable {
    var id: String
    var taskID: String
    var toolName: String
    var args: JSON
    var risk: String
    var reason: String
    var status: String // pending | allow | deny | expired
    var scope: String?
    var created: Double

    var fromChat: Bool { taskID.hasPrefix("chat_") }
}

enum ItemKind: Hashable { case msg, task, approval }

struct TimelineItem: Hashable, Identifiable {
    var kind: ItemKind
    var id: String
}

// ---------- 设置（每个 Bot 独立：API 渠道、聊天模型、Agent 模型、Agent CLI） ----------

struct APIChannel: Codable, Hashable, Identifiable {
    var id: String
    var name: String
    var base_url: String
    var enabled: Bool
    var auth_required: Bool
    /// 只写：nil 表示保留服务器上已有的密钥
    var api_key: String?
    var has_api_key: Bool?
    var models: [String]?
    var hidden_models: [String]?

    var visibleModels: [String] {
        let hidden = Set(hidden_models ?? [])
        return (models ?? []).filter { !hidden.contains($0) }
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id, forKey: .id)
        try c.encode(name, forKey: .name)
        try c.encode(base_url, forKey: .base_url)
        try c.encode(enabled, forKey: .enabled)
        try c.encode(auth_required, forKey: .auth_required)
        try c.encodeIfPresent(api_key, forKey: .api_key)
        try c.encode(models ?? [], forKey: .models)
        try c.encode(hidden_models ?? [], forKey: .hidden_models)
    }
}

struct APISettings: Codable, Hashable {
    var base_url: String?
    var model: String?
    var has_api_key: Bool?
    var providers: [APIChannel]?
    var default_provider_id: String?
    var agent_provider_id: String?
    var agent_model: String?
    var env_override: Bool?

    var channels: [APIChannel] { providers ?? [] }
    var chatChannelID: String { default_provider_id ?? channels.first?.id ?? "" }
    var agentChannelID: String { agent_provider_id ?? chatChannelID }
    var chatModel: String { model ?? "" }
    var agentModelID: String { agent_model ?? chatModel }

    func channel(_ id: String) -> APIChannel? { channels.first { $0.id == id } }

    /// 保存用的请求体：总是带上完整渠道列表（不带密钥 = 保留原密钥）
    func draft() -> APISettings {
        var d = self
        d.providers = channels
        d.default_provider_id = chatChannelID
        d.agent_provider_id = agentChannelID
        d.agent_model = agentModelID
        d.base_url = channel(chatChannelID)?.base_url ?? ""
        d.has_api_key = nil
        d.env_override = nil
        return d
    }
}

/// 拉取模型列表 / 测试连接时用的请求体
struct ChannelProbe: Encodable {
    var provider_id: String
    var base_url: String
    var api_key: String?
    var model: String?
}

struct ModelList: Decodable { var models: [String]? }
struct Latency: Decodable { var latency_ms: Int? }

struct CLIStatus: Codable, Hashable, Identifiable {
    var id: String
    var name: String
    var installed: Bool
    /// 检测到的可执行文件；managed 表示由 frog 一键安装；installable 表示这台设备可以一键安装/更新。
    var path: String?
    var managed: Bool?
    var installable: Bool?
    var install: CLIInstall?
    /// 现在能选的只有 Pi；其他 CLI 显示 note（「暂不支持」）
    var supported: Bool?
    var note: String?

    var installing: Bool { install?.state == "running" }
}

/// 提权到 root 的状态（GET /api/settings/privilege）：只在服务器模式、Agent 沙箱可用并装了提权助手时 available。
struct PrivilegeStatus: Codable, Hashable {
    struct Sandbox: Codable, Hashable { var available: Bool; var reason: String? }
    struct Helper: Codable, Hashable {
        var configured: Bool
        var enabled: Bool
        var grant_left_sec: Int?
        var locked_left_sec: Int?
    }
    var available: Bool
    var reason: String?
    var error: String?
    var sandbox: Sandbox
    var status: Helper?

    /// 给界面看的一句话状态。
    var summary: String {
        if available {
            if let s = status?.grant_left_sec, s > 0 { return "已开启 · 免密窗口还剩 \((s + 59) / 60) 分钟" }
            return "已开启 · 每条 root 命令都要输入登录密码"
        }
        if let s = status?.locked_left_sec, s > 0 { return "密码输错太多次，\((s + 59) / 60) 分钟后再试" }
        return reason ?? error ?? "不可用"
    }
}

/// 一键安装的进度（frog 把 Node.js 和 CLI 装到服务器的数据目录，不需要 root）。
struct CLIInstall: Codable, Hashable {
    var state: String // running / done / failed
    var step: String?
    var error: String?
    var log: String?
    var version: String?
}

struct AgentConnections: Codable, Hashable {
    var cli: String
    var cli_model: String?
    /// Pi 用 frog 的哪个 API 渠道（经本机网关转发，Pi 拿不到真实 Key）
    var cli_channel: String?
    var clis: [CLIStatus]?
}

struct UpdateStatus: Codable, Hashable {
    var current: String?
    var latest: String?
    var release_url: String?
    var state: String?
}

struct Schedule: Codable, Hashable, Identifiable {
    var id: String
    var title: String
    var prompt: String?
    var enabled: Bool
    var kind: String?
    var timezone: String?
    var hour: Int?
    var minute: Int?
    var weekdays: [Int]?
    var next_run_local: String?
    var last_run_local: String?
    var last_status: String?
    var at_local: String?
}
