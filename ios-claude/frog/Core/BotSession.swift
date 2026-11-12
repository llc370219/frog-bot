import Foundation
import Observation

/// 一个 Bot 的实时状态：连接它的 SSE 事件流，把事件合并成时间线（消息、任务卡、审批），
/// 断线后带 `?after=` 续传。合并规则逐条移植自网页版 web/src/api.ts 的 `apply`，两边看到的内容一致。
@MainActor
@Observable
final class BotSession {
    let ref: BotRef
    var profile: BotProfile
    var deviceName: String
    let server: ServerEntry

    // 时间线
    var items: [TimelineItem] = []
    var msgs: [String: Msg] = [:]
    var tasks: [String: BotTask] = [:]
    var approvals: [String: Approval] = [:]
    var scheduleRevision = 0

    // 聊天层这一轮（流式，临时）
    var streaming = ""
    var toolLabel = ""
    var busy = false
    var thinking = ""
    var thinkStart: Double = 0
    var thinkEnd: Double = 0

    var ready = false
    var connected = false
    var info: BotInfo?

    // 输入框状态按 Bot 保留：切走再回来，草稿和附件还在
    var draft = ""
    var uploads: [PendingUpload] = []

    @ObservationIgnored var lastSeq: Int64 = 0
    @ObservationIgnored var onNews: ((BotSession, String, NewsKind) -> Void)?
    @ObservationIgnored private var streamTask: Task<Void, Never>?

    init(ref: BotRef, profile: BotProfile, deviceName: String, server: ServerEntry) {
        self.ref = ref
        self.profile = profile
        self.deviceName = deviceName
        self.server = server
    }

    var client: ServerClient { server.client }
    var isLocalDevice: Bool { ref.device == "local" }

    /// 这个 Bot 的接口前缀：本机 /api/bots/{id}，互联设备 /api/devices/{d}/bots/{id}
    var basePath: String {
        let bot = ref.bot.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? ref.bot
        if isLocalDevice { return "/api/bots/\(bot)" }
        let dev = ref.device.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? ref.device
        return "/api/devices/\(dev)/bots/\(bot)"
    }

    // MARK: 派生状态

    var taskList: [BotTask] { Array(tasks.values) }
    var running: [BotTask] { tasks.values.filter { $0.status == .running } }
    var queued: [BotTask] { tasks.values.filter { $0.status == .queued }.sorted { $0.prio < $1.prio } }
    var paused: [BotTask] { tasks.values.filter { $0.status == .paused } }
    var working: Bool { tasks.values.contains { $0.isLive } }
    var pendingApprovals: Int { approvals.values.filter { $0.status == "pending" }.count }

    /// 排队任务的位置（第几位）
    var queuePositions: [String: Int] {
        var out: [String: Int] = [:]
        for (i, t) in queued.enumerated() { out[t.id] = i + 1 }
        return out
    }

    /// 任务多久没动静算“可能卡住”：后端判定时间的一半，最多 3 分钟（和网页版一致）
    var stallMs: Double { min(Double(info?.stall_sec ?? 720) * 1000 / 2, 180_000) }

    /// 只有最后一轮可以重新生成
    var regenerateID: String? {
        guard !busy else { return nil }
        let visible = items.filter { $0.kind == .msg }.compactMap { msgs[$0.id] }
        guard let lastUser = visible.last(where: { $0.role == "user" }),
              let lastReply = visible.last(where: { $0.role == "assistant" }),
              lastReply.created >= lastUser.created else { return nil }
        return lastReply.id
    }

    // MARK: 事件流

    func start() {
        guard streamTask == nil else { return }
        streamTask = Task { [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                let end = await self.streamOnce()
                if Task.isCancelled { return }
                self.connected = false
                if end == .unauthorized {
                    self.streamTask = nil
                    self.server.markNeedsLogin()
                    return
                }
                try? await Task.sleep(nanoseconds: 2_000_000_000)
            }
        }
        if info == nil { Task { await loadInfo() } }
    }

    func stop() {
        streamTask?.cancel()
        streamTask = nil
        connected = false
    }

    private enum StreamEnd { case dropped, unauthorized }

    private func streamOnce() async -> StreamEnd {
        var query: [URLQueryItem] = []
        if lastSeq > 0 { query.append(URLQueryItem(name: "after", value: String(lastSeq))) }
        var req = client.makeRequest("GET", basePath + "/stream", query: query)
        req.setValue("text/event-stream", forHTTPHeaderField: "Accept")
        req.timeoutInterval = 90 // 服务器每 20 秒发一次心跳
        do {
            let (bytes, resp) = try await client.streamSession.bytes(for: req)
            guard let http = resp as? HTTPURLResponse else { return .dropped }
            if http.statusCode == 401 { return .unauthorized }
            guard http.statusCode == 200 else { return .dropped }
            connected = true
            busy = false
            resetTurn()
            // 重放历史时先攒着，收到 replay.done 一次性合并，避免逐条刷新界面
            var replaying = true
            var batch: [JSON] = []
            for try await line in bytes.lines {
                guard line.hasPrefix("data:") else { continue } // id: 行和 ": ping" 心跳都不需要
                var payload = line.dropFirst(5)
                if payload.first == " " { payload = payload.dropFirst() }
                guard let ev = JSON.parse(Data(payload.utf8)) else { continue }
                if replaying {
                    batch.append(ev)
                    if ev["kind"].string == "replay.done" {
                        replaying = false
                        for e in batch { handle(e) }
                        batch.removeAll()
                    }
                } else {
                    handle(ev)
                }
            }
            for e in batch { handle(e) }
        } catch {
            // 断线：外层循环会重连
        }
        return .dropped
    }

    private func handle(_ ev: JSON) {
        let seq = Int64(ev["seq"].double ?? 0)
        if seq > 0 {
            if seq <= lastSeq { return }
            lastSeq = seq
        }
        apply(ev)
    }

    private func resetTurn() {
        streaming = ""; toolLabel = ""; thinking = ""; thinkStart = 0; thinkEnd = 0
    }

    private func endThink() {
        if thinkStart > 0 && thinkEnd == 0 { thinkEnd = Stamp.now }
    }

    private func news(_ text: String, _ kind: NewsKind) {
        guard ready else { return } // 重放历史不提醒
        onNews?(self, text, kind)
    }

    // MARK: 合并事件（对应 api.ts 的 apply）

    private func apply(_ ev: JSON) {
        let d = ev["data"]
        switch ev["kind"].str {
        case "replay.done":
            ready = true
            busy = d["busy"].bool
            scheduleRevision += 1

        case "message":
            let role = d["role"].str
            if role == "notice" { return } // 系统通知只给聊天层看
            let id = d["id"].str
            if d["hidden"].bool {
                items.removeAll { $0.id == id }
                msgs[id] = nil
                return
            }
            let m = Msg(d)
            if msgs[id] == nil {
                // 聊天层的回应（例如“开始做了”）排在它这一轮刚派出的工作卡片前面，读起来更顺
                var at = items.count
                if role == "assistant" {
                    while at > 0, items[at - 1].kind == .task, let t = tasks[items[at - 1].id],
                          t.steps.isEmpty, t.created > m.created - 60_000 {
                        at -= 1
                    }
                }
                items.insert(TimelineItem(kind: .msg, id: id), at: at)
                if role == "assistant" && !m.text.isEmpty { news(m.text, .reply) }
            }
            msgs[id] = m
            if role == "assistant" && (!m.text.isEmpty || !(m.thinking ?? "").isEmpty) { resetTurn() }

        case "message.hidden":
            let ids = Set(d.array.compactMap { $0.string })
            items.removeAll { $0.kind == .msg && ids.contains($0.id) }
            for id in ids { msgs[id] = nil }

        case "message.reaction":
            let id = d["id"].str
            guard var m = msgs[id] else { return }
            let by = d["by"].str
            var keep = m.reactions.filter { $0.by != by } // 每一方一条，切换
            if d["on"].bool { keep.append(Reaction(by: by, emoji: d["emoji"].str)) }
            m.reactions = keep
            msgs[id] = m

        case "card":
            let card = Card(d)
            for (k, m) in msgs where m.card?.id == card.id { msgs[k]?.card = card }

        case "secret.card":
            let req = SecretRequest(d)
            for (k, m) in msgs where m.secret?.id == req.id { msgs[k]?.secret = req }

        case "chat.thinking":
            if thinkStart == 0 { thinkStart = Stamp.now }
            thinkEnd = 0
            thinking += d["text"].str

        case "chat.delta":
            endThink()
            streaming += d["text"].str

        case "chat.status":
            busy = d["busy"].bool
            if !busy { resetTurn() }

        case "chat.tool":
            endThink()
            toolLabel = d["label"].str

        case "task.created":
            let id = d["id"].str
            if tasks[id] == nil { items.append(TimelineItem(kind: .task, id: id)) }
            let c = Stamp.ms(d["created_at"].string)
            let prio = d["prio"].double.flatMap { $0 != 0 ? $0 : nil } ?? c * 1e6
            tasks[id] = BotTask(id: id, title: d["title"].str, input: d["input"].str,
                                status: TaskStatus(rawValue: d["status"].str) ?? .queued,
                                stopReason: d["stop_reason"].string, created: c, prio: prio,
                                lastEvent: c, lastBeat: c)

        case "task.updated":
            let id = d["id"].str
            guard var t = tasks[id] else { return }
            if let st = TaskStatus(rawValue: d["status"].str) {
                t.status = st
                if st.terminal { t.ended = Stamp.ms(ev["time"].string) }
                if st == .queued { t.stopReason = nil; t.pausing = false }
            }
            if let p = d["prio"].double, p != 0 { t.prio = p }
            if let e = d["error"].string, !e.isEmpty { t.error = e }
            if let r = d["stop_reason"].string, !r.isEmpty { t.stopReason = r }
            tasks[id] = t

        case "task.pausing":
            let id = d["id"].str
            guard var t = tasks[id] else { return }
            t.pausing = true
            t.stopReason = d["stop_reason"].string
            tasks[id] = t

        case "task.prio":
            let id = d["id"].str
            if let p = d["prio"].double { tasks[id]?.prio = p }

        case "task.summary":
            let id = d["id"].str
            guard var t = tasks[id] else { return }
            t.summary = d["summary"].string
            let files = d["files"].array.compactMap { $0.string }
            if !files.isEmpty { t.files = files }
            if let sec = d["duration_sec"].double { t.duration = sec * 1000 }
            tasks[id] = t

        case "agent":
            applyAgent(d)

        case "schedule.upserted", "schedule.deleted":
            scheduleRevision += 1

        case "schedule.run":
            scheduleRevision += 1
            let st = d["claim"]["status"].str
            if st == "needs_verify" || st == "failed" || st == "expired" {
                news("定时计划「\(d["plan"]["title"].str)」：\(st)", .task)
            }

        default:
            break
        }
    }

    private func applyAgent(_ e: JSON) {
        let taskID = e["task_id"].str
        let type = e["type"].str
        let d = e["data"]
        let at = Stamp.ms(e["time"].string)

        if type == "approval.requested" {
            let id = d["id"].str
            if approvals[id] == nil {
                items.append(TimelineItem(kind: .approval, id: id))
                news("有一个操作等你确认", .approval)
            }
            approvals[id] = Approval(id: id, taskID: taskID, toolName: d["tool_name"].str, args: d["args"],
                                     risk: d["risk"].str, reason: d["reason"].str, status: "pending",
                                     scope: nil, created: at)
            if var t = tasks[taskID] { t.awaiting += 1; t.lastEvent = at; tasks[taskID] = t }
            return
        }
        if type == "approval.decided" {
            let aid = d["approval_id"].str
            if var a = approvals[aid], a.status == "pending" {
                a.status = d["decision"].str
                a.scope = d["scope"].string
                approvals[aid] = a
                if var t = tasks[taskID] { t.awaiting = max(0, t.awaiting - 1); t.lastEvent = at; tasks[taskID] = t }
            }
            return
        }

        guard var t = tasks[taskID] else { return } // 聊天层直接发起的审批没有对应任务
        if type == "heartbeat" {
            t.lastBeat = at
            tasks[taskID] = t
            return
        }
        t.lastEvent = max(t.lastEvent, at)

        switch type {
        case "task.started":
            t.status = .running
            t.stopReason = nil
            t.pausing = false
            t.started = at
            t.ended = nil

        case "thinking.delta":
            t.thinking = String((t.thinking + d["text"].str).suffix(600))

        case "text.delta":
            t.text = String((t.text + d["text"].str).suffix(4000))
            t.thinking = ""

        case "tool.call":
            t.thinking = ""
            t.steps.append(Step(id: d["call_id"].str, tool: d["tool"].str, args: d["args"],
                                thought: t.text.trimmingCharacters(in: .whitespacesAndNewlines), at: at))
            t.text = ""

        case "tool.result":
            let callID = d["call_id"].str
            let idx = t.steps.lastIndex(where: { $0.id == callID && !$0.done }) ?? (t.steps.isEmpty ? nil : t.steps.count - 1)
            if let i = idx {
                t.steps[i].output = d["output"].string
                t.steps[i].error = d["error"].bool
                t.steps[i].denied = d["denied"].bool
                t.steps[i].done = true
                t.steps[i].doneAt = at
                let st = t.steps[i]
                let path = st.args["path"].str
                if (st.tool == "write_file" || st.tool == "edit_file"), !path.isEmpty, !st.error, !st.denied, !t.files.contains(path) {
                    t.files.append(path)
                }
            }

        case "task.paused":
            if let s = t.started { t.activeMs += at - s }
            t.status = .paused
            t.pausing = false
            t.text = ""
            t.thinking = ""
            t.started = nil
            for i in t.steps.indices where !t.steps[i].done { t.steps[i].done = true }

        case "task.completed", "task.failed", "task.cancelled":
            if let s = t.started { t.activeMs += at - s }
            let st = TaskStatus(rawValue: d["status"].str) ?? TaskStatus(rawValue: String(type.dropFirst(5))) ?? .completed
            t.status = st
            t.output = d["output"].string
            t.ended = at
            t.started = nil
            t.pausing = false
            if let e = d["error"].string, !e.isEmpty { t.error = e }
            t.text = ""
            t.thinking = ""
            for i in t.steps.indices where !t.steps[i].done { t.steps[i].done = true }
            let verb = st == .completed ? "完成了" : st == .failed ? "失败了" : "已停止"
            tasks[taskID] = t
            news("任务「\(t.title)」\(verb)", .task)
            return

        default:
            break
        }
        tasks[taskID] = t
    }

    // MARK: 操作

    func loadInfo() async {
        info = try? await client.get(basePath + "/info", as: BotInfo.self)
    }

    func send(_ text: String, files: [Attachment]) async throws {
        try await client.post(basePath + "/messages", body: ["text": text, "files": files.map(\.json)])
    }

    func stopAll() async throws { try await client.post(basePath + "/stop") }
    func stopReply() async throws { try await client.post(basePath + "/stop", query: [URLQueryItem(name: "scope", value: "reply")]) }
    func regenerate() async throws { try await client.post(basePath + "/regenerate") }
    func react(_ id: String, _ emoji: String) async throws { try await client.post(basePath + "/messages/\(id)/react", body: ["emoji": emoji]) }
    func task(_ id: String, _ action: String) async throws { try await client.post(basePath + "/tasks/\(id)/\(action)") }
    func decide(_ id: String, _ decision: String, password: String? = nil, grantMinutes: Int = 0) async throws {
        var body: [String: Any] = ["decision": decision]
        if let password { body["password"] = password; body["grant_minutes"] = grantMinutes } // 只有 root 命令需要
        try await client.post(basePath + "/approvals/\(id)", body: body)
    }
    func answer(_ cardID: String, _ answer: String) async throws { try await client.post(basePath + "/cards/\(cardID)", body: ["answer": answer]) }
    func dismiss(_ cardID: String) async throws { try await client.post(basePath + "/cards/\(cardID)", body: ["dismiss": true]) }
    func saveSecret(_ requestID: String, _ value: String) async throws { try await client.post(basePath + "/secrets", body: ["request_id": requestID, "value": value]) }
    func cancelSecret(_ requestID: String) async throws { try await client.post(basePath + "/secrets/requests/\(requestID)/cancel") }

    // MARK: 这个 Bot 自己的设置（模型、渠道、Agent）

    func loadSettings() async throws -> APISettings {
        try await client.get(basePath + "/settings")
    }

    @discardableResult
    func saveSettings(_ s: APISettings) async throws -> APISettings {
        let saved: APISettings = try await client.send("POST", basePath + "/settings", body: s.draft())
        await loadInfo()
        return saved
    }

    /// 只保存这个 Bot 用哪个渠道的哪个模型（渠道本身整台设备共用）
    @discardableResult
    func saveRoutes(chatChannel: String, chatModel: String, agentChannel: String, agentModel: String) async throws -> APISettings {
        let body = ["default_provider_id": chatChannel, "model": chatModel, "agent_provider_id": agentChannel, "agent_model": agentModel]
        let saved: APISettings = try await client.send("POST", basePath + "/settings/routes", body: body)
        await loadInfo()
        return saved
    }

    /// 换聊天模型：任务模型保持不变
    func setChatModel(channel: String, model: String) async throws {
        let s = try await loadSettings()
        try await saveRoutes(chatChannel: channel, chatModel: model, agentChannel: s.agentChannelID, agentModel: s.agentModelID)
    }

    func loadConnections() async throws -> AgentConnections {
        try await client.get(basePath + "/settings/connections")
    }

    /// 选择 Agent。Pi 要同时给出 API 渠道和模型。
    func saveConnection(_ cli: String, channel: String? = nil, model: String? = nil) async throws -> AgentConnections {
        var body = ["cli": cli]
        if let channel { body["cli_channel"] = channel }
        if let model { body["cli_model"] = model }
        return try await client.send("POST", basePath + "/settings/connections", body: body)
    }

    /// 提权状态和开关（整台服务器共用；设备互联不转发，只能管这台服务器本机）。
    func loadPrivilege() async throws -> PrivilegeStatus {
        try await client.get("/api/settings/privilege")
    }

    func privilegeAction(_ action: String, password: String? = nil) async throws -> PrivilegeStatus {
        var body = ["action": action]
        if let password { body["password"] = password }
        return try await client.send("POST", "/api/settings/privilege", body: body)
    }

    /// 一键安装 / 更新 CLI。只能装在这台服务器本机：设备互联不转发这个接口。
    func installCLI(_ id: String) async throws -> AgentConnections {
        try await client.send("POST", "/api/settings/clis/install", body: ["id": id])
    }

    func fetchModels(_ probe: ChannelProbe) async throws -> [String] {
        let out: ModelList = try await client.send("POST", basePath + "/settings/models", body: probe)
        return out.models ?? []
    }

    func testChannel(_ probe: ChannelProbe) async throws -> Int {
        let out: Latency = try await client.send("POST", basePath + "/settings/test", body: probe)
        return out.latency_ms ?? 0
    }

    func schedules() async throws -> [Schedule] {
        try await client.get(basePath + "/schedules")
    }

    func schedule(_ id: String, _ action: String) async throws {
        try await client.post(basePath + "/schedules/\(id)/\(action)")
    }

    func fileData(_ path: String, download: Bool = false) async throws -> Data {
        var q = [URLQueryItem(name: "path", value: path)]
        if download { q.append(URLQueryItem(name: "download", value: "1")) }
        return try await client.data(basePath + "/files", query: q)
    }

    // MARK: 附件

    func attach(_ urls: [URL]) {
        for url in urls {
            let p = PendingUpload(name: url.lastPathComponent, size: (try? url.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? 0)
            uploads.append(p)
            Task {
                do {
                    let att = try await client.upload(basePath + "/uploads", file: url)
                    setUpload(p.id) { $0.attachment = att }
                } catch {
                    setUpload(p.id) { $0.error = error.localizedDescription }
                }
            }
        }
    }

    func attach(name: String, data: Data) {
        let p = PendingUpload(name: name, size: data.count)
        uploads.append(p)
        Task {
            do {
                let att = try await client.upload(basePath + "/uploads", name: name, data: data)
                setUpload(p.id) { $0.attachment = att }
            } catch {
                setUpload(p.id) { $0.error = error.localizedDescription }
            }
        }
    }

    private func setUpload(_ id: UUID, _ change: (inout PendingUpload) -> Void) {
        guard let i = uploads.firstIndex(where: { $0.id == id }) else { return }
        change(&uploads[i])
    }
}

enum NewsKind { case reply, task, approval }

struct PendingUpload: Identifiable, Hashable {
    let id = UUID()
    var name: String
    var size: Int
    var attachment: Attachment?
    var error: String?

    var uploading: Bool { attachment == nil && error == nil }
}
