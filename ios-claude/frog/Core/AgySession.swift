import Foundation
import Observation

// MARK: - 直连 Antigravity CLI (agy) 数据模型

struct AgyModeOption: Codable, Hashable, Identifiable {
    var id: String
    var name: String
    var description: String?
}

struct AgyModelOption: Codable, Hashable, Identifiable {
    var id: String
    var name: String
}

struct AgyCommandOption: Codable, Hashable, Identifiable {
    var name: String
    var description: String?
    var input_hint: String?
    var id: String { name }
}

struct AgyQueuedPrompt: Hashable, Identifiable {
    var id: String
    var text: String
    var behavior: String

    init(_ j: JSON) {
        id = j["id"].str
        text = j["text"].str
        behavior = j["behavior"].str.isEmpty ? "follow_up" : j["behavior"].str
    }
}

struct AgyAgentCLI: Codable, Hashable, Identifiable {
    var id: String
    var name: String
    var installed: Bool
    var path: String?
    var version: String?
    var modes: [AgyModeOption]?
    var models: [AgyModelOption]?
    var default_mode: String?
    var default_model: String?
}

struct AgyStatus: Codable, Equatable {
    var installed: Bool
    var default_agent: String?
    var agents: [AgyAgentCLI]?
    var path: String?
    var agent_version: String?
    var projects_root: String
    var modes: [AgyModeOption]
    var models: [AgyModelOption]
    var commands: [AgyCommandOption]?
    var default_mode: String
    var default_model: String?
    var error: String?

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        installed = (try? c.decodeIfPresent(Bool.self, forKey: .installed)) ?? false
        default_agent = try? c.decodeIfPresent(String.self, forKey: .default_agent)
        agents = try? c.decodeIfPresent([AgyAgentCLI].self, forKey: .agents)
        path = try? c.decodeIfPresent(String.self, forKey: .path)
        agent_version = try? c.decodeIfPresent(String.self, forKey: .agent_version)
        projects_root = (try? c.decodeIfPresent(String.self, forKey: .projects_root)) ?? ""
        modes = (try? c.decodeIfPresent([AgyModeOption].self, forKey: .modes)) ?? []
        models = (try? c.decodeIfPresent([AgyModelOption].self, forKey: .models)) ?? []
        commands = try? c.decodeIfPresent([AgyCommandOption].self, forKey: .commands)
        default_mode = (try? c.decodeIfPresent(String.self, forKey: .default_mode)) ?? "auto"
        default_model = try? c.decodeIfPresent(String.self, forKey: .default_model)
        error = try? c.decodeIfPresent(String.self, forKey: .error)
    }
}

struct AgyProject: Codable, Hashable, Identifiable {
    var id: String
    var name: String
    var path: String
    var managed: Bool
    var created_at: String?
}

struct AgyFileEntry: Codable, Hashable, Identifiable {
    var name: String
    var is_dir: Bool
    var size: Int?
    var id: String { name }
}

struct AgySessionSummary: Codable, Hashable, Identifiable {
    var id: String
    var project_id: String
    var cwd: String
    var agent: String?
    var agent_name: String?
    var title: String
    var mode: String
    var model: String?
    var busy: Bool
    var pending_permissions: Int
    var updated_at: String
}

struct AgyToolCall: Hashable, Identifiable {
    var id: String
    var name: String
    var title: String
    var kind: String
    var status: String
    var detail: String
    var output: String

    init(_ j: JSON) {
        id = j["id"].str
        name = j["name"].str
        title = j["title"].str
        kind = j["kind"].str
        status = j["status"].str
        detail = j["detail"].str
        output = j["output"].str
    }
}

struct AgyMessage: Hashable, Identifiable {
    var id: String
    var role: String
    var text: String
    var thinking: String?
    var thinkingMs: Double?
    var tools: [AgyToolCall]
    var error: String?
    var created: Double

    init(_ j: JSON) {
        id = j["id"].str
        role = j["role"].str
        text = j["text"].str
        thinking = j["thinking"].string
        thinkingMs = j["thinking_ms"].double
        tools = j["tools"].array.map(AgyToolCall.init)
        error = j["error"].string
        created = Stamp.ms(j["created_at"].string)
    }
}

struct AgyPermOption: Hashable, Identifiable {
    var optionId: String
    var name: String
    var kind: String

    var id: String { optionId }

    init(_ j: JSON) {
        optionId = j["optionId"].str
        name = j["name"].str
        kind = j["kind"].str
    }
}

struct AgyPermission: Hashable, Identifiable {
    var id: String
    var sessionID: String
    var toolName: String
    var title: String
    var detail: String
    var options: [AgyPermOption]
    var status: String
    var chosenID: String?

    init(_ j: JSON) {
        id = j["id"].str
        sessionID = j["session_id"].str
        toolName = j["tool_name"].str
        title = j["title"].str
        detail = j["detail"].str
        options = j["options"].array.map(AgyPermOption.init)
        status = j["status"].str
        chosenID = j["chosen_id"].string
    }
}

// MARK: - 单台服务器的 agy 项目与会话目录管理

@MainActor
@Observable
final class AgyStore {
    static let allProjectsID = "all"
    static let allAgentsID = "all"

    let server: ServerEntry
    var status: AgyStatus?
    var projects: [AgyProject] = []
    var selectedProjectID: String = ""
    var selectedAgentFilter: String = "" // "" 跟随当前选中的 CLI；"all" 显示全部 CLI
    var sessions: [AgySessionSummary] = []
    var loading = false
    var error: String?

    init(server: ServerEntry) {
        self.server = server
        let saved = UserDefaults.standard.string(forKey: "agy.project.\(server.id)") ?? ""
        self.selectedProjectID = saved.isEmpty ? Self.allProjectsID : saved
        self.selectedAgentFilter = UserDefaults.standard.string(forKey: "agy.agentFilter.\(server.id)") ?? ""
    }

    var client: ServerClient { server.client }

    var isAllProjects: Bool {
        selectedProjectID.isEmpty || selectedProjectID == Self.allProjectsID
    }

    var activeProject: AgyProject? {
        guard !isAllProjects else { return nil }
        return projects.first(where: { $0.id == selectedProjectID })
    }

    var defaultTargetProject: AgyProject? {
        activeProject ?? projects.first
    }

    var installedAgents: [AgyAgentCLI] {
        (status?.agents ?? []).filter(\.installed)
    }

    var activeAgentID: String {
        status?.default_agent ?? installedAgents.first?.id ?? "agy"
    }

    var effectiveAgentFilter: String {
        if selectedAgentFilter == Self.allAgentsID {
            return Self.allAgentsID
        }
        if !selectedAgentFilter.isEmpty {
            return selectedAgentFilter
        }
        return activeAgentID
    }

    func setAgentFilter(_ id: String) {
        selectedAgentFilter = id
        UserDefaults.standard.set(id, forKey: "agy.agentFilter.\(server.id)")
    }

    func selectAgent(_ agentID: String) async {
        setAgentFilter(agentID)
        do {
            let st: AgyStatus = try await client.send("POST", "/api/agy/agent", body: ["agent": agentID])
            status = st
            error = nil
        } catch where error.isCancellation {
            return
        } catch {
            self.error = error.localizedDescription
        }
    }

    func loadAll() async {
        loading = true
        error = nil
        do {
            async let stReq: AgyStatus = client.get("/api/agy/status")
            async let prReq: [AgyProject] = client.get("/api/agy/projects")
            let (st, projs) = try await (stReq, prReq)
            status = st
            projects = projs
            if !isAllProjects && !projs.contains(where: { $0.id == selectedProjectID }) {
                selectProject(Self.allProjectsID)
            }
            let pid = isAllProjects ? Self.allProjectsID : (activeProject?.id ?? Self.allProjectsID)
            sessions = (try? await client.get("/api/agy/sessions", query: [URLQueryItem(name: "project_id", value: pid)])) ?? []
        } catch where error.isCancellation {
            loading = false
            return
        } catch {
            self.error = error.localizedDescription
        }
        loading = false
    }

    func selectProject(_ id: String) {
        let next = id.isEmpty ? Self.allProjectsID : id
        selectedProjectID = next
        UserDefaults.standard.set(next, forKey: "agy.project.\(server.id)")
    }

    func refreshSessions() async {
        let pid = isAllProjects ? Self.allProjectsID : (activeProject?.id ?? Self.allProjectsID)
        do {
            sessions = try await client.get("/api/agy/sessions", query: [URLQueryItem(name: "project_id", value: pid)])
            error = nil
        } catch where error.isCancellation {
            return
        } catch {
            self.error = error.localizedDescription
        }
    }

    func createProject(name: String, path: String) async throws -> AgyProject {
        let p: AgyProject = try await client.send("POST", "/api/agy/projects", body: ["name": name, "path": path])
        if !projects.contains(where: { $0.id == p.id }) {
            projects.append(p)
        }
        selectProject(p.id)
        await refreshSessions()
        return p
    }

    func deleteProject(_ p: AgyProject, deleteFiles: Bool) async throws {
        var q: [URLQueryItem] = []
        if deleteFiles { q.append(URLQueryItem(name: "delete_files", value: "1")) }
        try await client.post("/api/agy/projects/\(p.id)", method: "DELETE", query: q)
        projects.removeAll { $0.id == p.id }
        if selectedProjectID == p.id {
            selectProject(Self.allProjectsID)
        }
        await refreshSessions()
    }

    func createSession(mode: String = "", model: String = "", agent: String = "") async throws -> AgySessionSummary {
        guard let pid = defaultTargetProject?.id else {
            throw APIError.invalid("请先创建或选择一个项目文件夹")
        }
        let targetAgent = agent.isEmpty ? activeAgentID : agent
        let res = try await client.post("/api/agy/sessions", body: ["project_id": pid, "agent": targetAgent, "mode": mode, "model": model])
        let s = res["session"]
        let summary = AgySessionSummary(
            id: s["id"].str,
            project_id: s["project_id"].str,
            cwd: s["cwd"].str,
            agent: s["agent"].string,
            agent_name: s["agent_name"].string,
            title: s["title"].str.isEmpty ? "新对话" : s["title"].str,
            mode: s["mode"].str.isEmpty ? "auto" : s["mode"].str,
            model: s["model"].string,
            busy: s["busy"].bool,
            pending_permissions: Int(s["pending_permissions"].double ?? 0),
            updated_at: s["updated_at"].str
        )
        sessions.removeAll { $0.id == summary.id }
        sessions.insert(summary, at: 0)
        return summary
    }

    func deleteSession(_ id: String) async throws {
        try await client.post("/api/agy/sessions/\(id)", method: "DELETE")
        sessions.removeAll { $0.id == id }
    }
}

// MARK: - 单个 agy 对话的实时 SSE 会话

@MainActor
@Observable
final class AgyChatSession {
    let server: ServerEntry
    let sessionID: String
    let projectID: String

    var title: String
    var cwd = ""
    var mode = "auto"
    var model = ""
    var busy = false
    var ready = false
    var connected = false
    var loadError: String? = nil

    var messages: [AgyMessage] = []
    var permissions: [AgyPermission] = []
    var queue: [AgyQueuedPrompt] = []
    var streaming = ""
    var thinking = ""
    var thinkStart: Double = 0
    var thinkEnd: Double = 0
    var liveTools: [AgyToolCall] = []
    var usedTokens: Int = 0
    var maxTokens: Int = 0
    var modes: [AgyModeOption] = []
    var models: [AgyModelOption] = []
    var commands: [AgyCommandOption] = []
    var draft = ""

    @ObservationIgnored private var streamTask: Task<Void, Never>?

    init(server: ServerEntry, sessionID: String, projectID: String, initialTitle: String) {
        self.server = server
        self.sessionID = sessionID
        self.projectID = projectID
        self.title = initialTitle
    }

    var client: ServerClient { server.client }

    var pendingPermissions: [AgyPermission] {
        permissions.filter { $0.status == "pending" }
    }

    func start() {
        guard streamTask == nil else { return }
        streamTask = Task { [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                let unauth = await self.streamOnce()
                if Task.isCancelled { return }
                self.connected = false
                if unauth {
                    self.streamTask = nil
                    self.server.markNeedsLogin()
                    return
                }
                try? await Task.sleep(nanoseconds: 2_000_000_000)
            }
        }
    }

    func stop() {
        streamTask?.cancel()
        streamTask = nil
        connected = false
    }

    private func streamOnce() async -> Bool {
        let encoded = sessionID.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? sessionID
        var req = client.makeRequest("GET", "/api/agy/sessions/\(encoded)/stream",
                                     query: [URLQueryItem(name: "project_id", value: projectID)])
        req.setValue("text/event-stream", forHTTPHeaderField: "Accept")
        req.timeoutInterval = 90
        do {
            let (statusCode, lines) = try await client.streamLines(for: req)
            if statusCode == 401 { return true }
            guard statusCode == 200 else {
                var errBody = ""
                for try await line in lines { errBody += line }
                if let j = JSON.parse(Data(errBody.utf8)), let msg = j["error"].string, !msg.isEmpty {
                    loadError = msg
                } else {
                    loadError = "加载会话失败（HTTP \(statusCode)）"
                }
                return false
            }
            connected = true
            loadError = nil
            for try await line in lines {
                guard line.hasPrefix("data:") else { continue }
                var payload = line.dropFirst(5)
                if payload.first == " " { payload = payload.dropFirst() }
                guard let ev = JSON.parse(Data(payload.utf8)) else { continue }
                apply(ev)
            }
        } catch where error.isCancellation {
            return false
        } catch {
            if !ready { loadError = error.localizedDescription }
            // 外层自动重连
        }
        return false
    }

    private func apply(_ ev: JSON) {
        let d = ev["data"]
        switch ev["kind"].str {
        case "snapshot":
            ready = true
            connected = true
            let s = d["session"]
            if !s["title"].str.isEmpty { title = s["title"].str }
            cwd = s["cwd"].str
            if !s["mode"].str.isEmpty { mode = s["mode"].str }
            model = s["model"].str
            busy = s["busy"].bool
            messages = d["messages"].array.map(AgyMessage.init)
            permissions = d["permissions"].array.map(AgyPermission.init)
            queue = d["queue"].array.map(AgyQueuedPrompt.init)
            streaming = d["streaming"].str
            thinking = d["thinking"].str
            thinkStart = d["think_start"].double ?? 0
            thinkEnd = d["think_end"].double ?? 0
            liveTools = d["live_tools"].array.map(AgyToolCall.init)
            usedTokens = Int(d["used_tokens"].double ?? 0)
            maxTokens = Int(d["max_tokens"].double ?? 0)
            let mds = d["modes"].array.map { AgyModeOption(id: $0["id"].str, name: $0["name"].str, description: $0["description"].string) }
            if !mds.isEmpty { modes = mds }
            let mls = d["models"].array.map { AgyModelOption(id: $0["id"].str, name: $0["name"].str) }
            if !mls.isEmpty { models = mls }
            let cmds = d["commands"].array.map { AgyCommandOption(name: $0["name"].str, description: $0["description"].string, input_hint: $0["input_hint"].string) }
            if !cmds.isEmpty { commands = cmds }

        case "queue.update":
            queue = d.array.map(AgyQueuedPrompt.init)

        case "commands":
            commands = d.array.map { AgyCommandOption(name: $0["name"].str, description: $0["description"].string, input_hint: $0["input_hint"].string) }

        case "message":
            let m = AgyMessage(d)
            if !messages.contains(where: { $0.id == m.id }) {
                messages.append(m)
            }
            if m.role == "assistant" {
                streaming = ""; thinking = ""; thinkStart = 0; thinkEnd = 0; liveTools = []
            }

        case "thinking.delta":
            if thinkStart == 0 { thinkStart = Stamp.now }
            thinkEnd = 0
            thinking += d["text"].str

        case "text.delta":
            if thinkStart > 0 && thinkEnd == 0 { thinkEnd = Stamp.now }
            streaming += d["text"].str

        case "tool.upsert":
            if thinkStart > 0 && thinkEnd == 0 { thinkEnd = Stamp.now }
            let tc = AgyToolCall(d)
            if let i = liveTools.firstIndex(where: { $0.id == tc.id }) {
                liveTools[i] = tc
            } else {
                liveTools.append(tc)
            }

        case "permission.upsert":
            let pr = AgyPermission(d)
            if let i = permissions.firstIndex(where: { $0.id == pr.id }) {
                permissions[i] = pr
            } else {
                permissions.append(pr)
            }

        case "status":
            busy = d["busy"].bool
            if let t = d["title"].string, !t.isEmpty { title = t }

        case "session.meta":
            let s = d["session"]
            if !s["title"].str.isEmpty { title = s["title"].str }
            if !s["mode"].str.isEmpty { mode = s["mode"].str }
            if !s["model"].str.isEmpty { model = s["model"].str }
            if let u = d["used_tokens"].double, u > 0 { usedTokens = Int(u) }
            if let mx = d["max_tokens"].double, mx > 0 { maxTokens = Int(mx) }

        case "turn.done":
            busy = d["busy"].bool
            streaming = ""; thinking = ""; thinkStart = 0; thinkEnd = 0; liveTools = []

        default:
            break
        }
    }

    // MARK: 操作

    func send(_ text: String, behavior: String? = nil) async throws {
        let encoded = sessionID.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? sessionID
        var body: [String: Any] = ["project_id": projectID, "text": text]
        if let behavior { body["behavior"] = behavior }
        try await client.post("/api/agy/sessions/\(encoded)/messages", body: body)
    }

    func cancelQueued(_ queueID: String) async throws {
        let sid = sessionID.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? sessionID
        let qid = queueID.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? queueID
        try await client.post("/api/agy/sessions/\(sid)/queue/\(qid)", method: "DELETE")
    }

    func stopPrompt() async throws {
        let encoded = sessionID.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? sessionID
        try await client.post("/api/agy/sessions/\(encoded)/stop")
    }

    func decide(permID: String, optionID: String) async throws {
        let sid = sessionID.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? sessionID
        let pid = permID.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? permID
        try await client.post("/api/agy/sessions/\(sid)/permissions/\(pid)", body: ["option_id": optionID])
    }

    func setConfig(mode: String? = nil, model: String? = nil) async throws {
        let sid = sessionID.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? sessionID
        var body: [String: Any] = [:]
        if let mode { body["mode"] = mode }
        if let model { body["model"] = model }
        let out = try await client.post("/api/agy/sessions/\(sid)/config", body: body)
        if !out["mode"].str.isEmpty { self.mode = out["mode"].str }
        if !out["model"].str.isEmpty { self.model = out["model"].str }
    }
}
