import AppKit
import Foundation
import Observation
import SwiftUI

enum ServerStatus: Equatable {
    case connecting
    case online
    case needsLogin
    case offline(String)
}

/// 面板里的一台服务器：连接设置、在线状态、上面的设备和 Bot 列表。
@MainActor
@Observable
final class ServerEntry: Identifiable {
    let id: UUID
    var config: ServerConfig
    var client: ServerClient
    var status: ServerStatus = .connecting
    var devices: [DeviceGroup] = []
    var authEnabled = true
    let gateway = GatewayClient()

    @ObservationIgnored var onNeedsLogin: ((ServerEntry) -> Void)?
    @ObservationIgnored var pollTask: Task<Void, Never>?

    init(config: ServerConfig) {
        id = config.id
        self.config = config
        client = ServerClient(config: config)
        gateway.attach(self)
    }

    var name: String { config.name }
    var isOnline: Bool { status == .online }

    func markNeedsLogin() {
        guard status != .needsLogin else { return }
        status = .needsLogin
        onNeedsLogin?(self)
    }
}

enum Route: Equatable { case chat, scheduler }

enum WorkspaceMode: String, Equatable {
    case bot
    case coding
}

struct CodingSelection: Hashable {
    var server: UUID
    var projectID: String
    var sessionID: String
    var title: String

    var key: String { "\(server.uuidString)|\(sessionID)" }
}

struct LocalHostDiscovery: Equatable {
    var port: Int
    var url: String
    var version: String
    var authRequired: Bool
    var authenticated: Bool
    var username: String?
}

/// 偏好设置（「设置 › 通用 / 通知」）
enum Prefs {
    static let keepRunning = "pref.keepRunning"
    static let appearance = "pref.appearance" // system | light | dark
    static let notifyApproval = "pref.notify.approval"
    static let notifyTask = "pref.notify.task"
    static let notifyReply = "pref.notify.reply"
    static let dockBadge = "pref.dockBadge"

    static func registerDefaults() {
        UserDefaults.standard.register(defaults: [
            keepRunning: true, appearance: "system",
            notifyApproval: true, notifyTask: true, notifyReply: true, dockBadge: true,
        ])
    }

    static func bool(_ key: String) -> Bool { UserDefaults.standard.bool(forKey: key) }

    @MainActor
    static func applyAppearance() {
        switch UserDefaults.standard.string(forKey: appearance) {
        case "light": NSApp.appearance = NSAppearance(named: .aqua)
        case "dark": NSApp.appearance = NSAppearance(named: .darkAqua)
        default: NSApp.appearance = nil
        }
    }
}

enum ActiveSheet: Identifiable, Equatable {
    case addServer
    case login(UUID)
    case editServer(UUID)
    case newBot(server: UUID, device: String)
    case editBot(BotRef)
    case cliAuth(UUID)
    case proxyGateway(UUID)
    case newProject(UUID)
    case projectFiles(server: UUID, project: AgyProject)

    var id: String {
        switch self {
        case .addServer: return "add"
        case .login(let id): return "login-\(id)"
        case .editServer(let id): return "edit-\(id)"
        case .newBot(let s, let d): return "new-\(s)-\(d)"
        case .editBot(let r): return "bot-\(r.key)"
        case .cliAuth(let id): return "cliauth-\(id)"
        case .proxyGateway(let id): return "proxy-\(id)"
        case .newProject(let id): return "newproj-\(id)"
        case .projectFiles(let s, let p): return "files-\(s)-\(p.id)"
        }
    }
}

/// 整个面板的状态：服务器列表、本机自动发现、Bot / Coding 双核模式、实时会话与未读计数。
@MainActor
@Observable
final class AppModel {
    static let shared = AppModel()

    var servers: [ServerEntry] = []
    var selection: BotRef? {
        didSet {
            if let selection {
                unread[selection] = nil
                activeServerID = selection.server
                UserDefaults.standard.set(selection.key, forKey: Self.selectionKey)
            }
        }
    }
    var workspaceMode: WorkspaceMode = .bot {
        didSet {
            UserDefaults.standard.set(workspaceMode.rawValue, forKey: Self.modeKey)
        }
    }
    var activeServerID: UUID? {
        didSet {
            if let activeServerID {
                UserDefaults.standard.set(activeServerID.uuidString, forKey: Self.activeServerKey)
            }
        }
    }
    var codingSelection: CodingSelection?
    var detectedLocal: LocalHostDiscovery?
    var scanningLocal = false
    var localScanMessage: String?

    var sheet: ActiveSheet?
    var unread: [BotRef: Int] = [:]
    var route: Route = .chat
    /// 打开设置窗口时要显示的服务器（从侧栏「服务器设置…」进来）
    var settingsServer: UUID?
    /// 客户端互联（设置 › 互联）
    let relay = RelayManager()

    /// Bot 实时会话
    @ObservationIgnored private(set) var sessions: [BotRef: BotSession] = [:]
    /// Coding 项目仓库与实时 SSE 会话
    @ObservationIgnored private var agyStores: [UUID: AgyStore] = [:]
    @ObservationIgnored private var agyChatSessions: [String: AgyChatSession] = [:]
    @ObservationIgnored private var localDiscoveryTask: Task<Void, Never>?
    /// 最多同时保持的事件流连接数
    private let maxSessions = 48

    private static let serversKey = "frogbotclaude.servers"
    private static let selectionKey = "frogbotclaude.selection"
    private static let modeKey = "frogbotclaude.workspaceMode"
    private static let activeServerKey = "frogbotclaude.activeServer"
    private static let dismissedLocalKey = "frogbotclaude.dismissedLocal"

    private init() {
        Prefs.registerDefaults()
        if let rawMode = UserDefaults.standard.string(forKey: Self.modeKey),
           let m = WorkspaceMode(rawValue: rawMode) {
            workspaceMode = m
        }
        if let data = UserDefaults.standard.data(forKey: Self.serversKey),
           let configs = try? JSONDecoder().decode([ServerConfig].self, from: data) {
            servers = configs.map(ServerEntry.init)
        }
        if let key = UserDefaults.standard.string(forKey: Self.selectionKey) {
            selection = BotRef(key: key)
        }
        if let sid = UserDefaults.standard.string(forKey: Self.activeServerKey),
           let uuid = UUID(uuidString: sid),
           servers.contains(where: { $0.id == uuid }) {
            activeServerID = uuid
        } else {
            activeServerID = selection?.server ?? servers.first?.id
        }
        for s in servers { wire(s) }
        relay.attach(self)
        startLocalDiscoveryLoop()
    }

    var totalUnread: Int { unread.values.reduce(0, +) }

    var activeServer: ServerEntry? {
        if let id = activeServerID, let s = server(id) { return s }
        if let sid = selection?.server, let s = server(sid) { return s }
        return servers.first(where: \.isOnline) ?? servers.first
    }

    var hasLocalServer: Bool {
        servers.contains { $0.config.isLocalHost || $0.config.isLoopbackEndpoint }
    }

    func server(_ id: UUID) -> ServerEntry? { servers.first { $0.id == id } }

    func session(_ ref: BotRef) -> BotSession? { sessions[ref] }

    func agyStore(for server: ServerEntry) -> AgyStore {
        if let existing = agyStores[server.id] { return existing }
        let store = AgyStore(server: server)
        agyStores[server.id] = store
        return store
    }

    func agySession(for sel: CodingSelection) -> AgyChatSession? {
        guard let srv = server(sel.server) else { return nil }
        if let existing = agyChatSessions[sel.key] {
            existing.start()
            return existing
        }
        let s = AgyChatSession(server: srv, sessionID: sel.sessionID, projectID: sel.projectID, initialTitle: sel.title)
        agyChatSessions[sel.key] = s
        s.start()
        return s
    }

    func selectServer(_ s: ServerEntry) {
        activeServerID = s.id
        if workspaceMode == .coding {
            if codingSelection?.server != s.id {
                codingSelection = nil
            }
            if !s.isOnline && s.status != .needsLogin {
                Task { await refresh(s) }
            }
            Task { await agyStore(for: s).loadAll() }
        } else {
            route = .chat
            if let g = s.devices.first(where: { !$0.bots.isEmpty }), let b = g.bots.first {
                selection = BotRef(server: s.id, device: g.id, bot: b.id)
            } else {
                selection = nil
            }
            if !s.isOnline && s.status != .needsLogin {
                Task { await refresh(s) }
            }
        }
    }

    func selectCodingSession(server: ServerEntry, summary: AgySessionSummary) {
        let sel = CodingSelection(
            server: server.id,
            projectID: summary.project_id,
            sessionID: summary.id,
            title: summary.title.isEmpty ? "新对话" : summary.title
        )
        activeServerID = server.id
        codingSelection = sel
        workspaceMode = .coding
        _ = agySession(for: sel)
    }

    // MARK: 本机 Frog Bot 自动识别

    private func startLocalDiscoveryLoop() {
        localDiscoveryTask?.cancel()
        localDiscoveryTask = Task { [weak self] in
            // 启动立即探测一次本机 127.0.0.1:8080..8089
            await self?.discoverLocalHost(manual: false)
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 15_000_000_000)
                guard let self else { return }
                // 若还没注册本机或本机处于离线状态，持续静默扫描
                let dismissed = UserDefaults.standard.bool(forKey: Self.dismissedLocalKey)
                if !dismissed && (!self.hasLocalServer || self.servers.contains(where: { ($0.config.isLocalHost || $0.config.isLoopbackEndpoint) && !$0.isOnline })) {
                    await self.discoverLocalHost(manual: false)
                }
            }
        }
    }

    /// 自动探测本机运行的 Frog Bot（8080..8089），同时兼容免密本机模式和带认证服务端模式。
    func discoverLocalHost(manual: Bool = false) async {
        if scanningLocal { return }
        scanningLocal = true
        if manual {
            localScanMessage = nil
            UserDefaults.standard.set(false, forKey: Self.dismissedLocalKey)
        }
        defer { scanningLocal = false }

        let found = await Self.probeLoopbackPorts(8080...8089)
        detectedLocal = found

        guard let found else {
            if manual { localScanMessage = "未检测到本机运行的 Frog Bot（端口 8080–8089）" }
            return
        }

        // 若列表里已经存在同端口本机条目，补标 isLocalHost 并唤醒刷新
        if let existing = servers.first(where: { entry in
            guard entry.config.isLoopbackEndpoint, let u = entry.config.baseURL else { return false }
            let port = u.port ?? (u.scheme == "https" ? 443 : 80)
            return port == found.port
        }) {
            if !existing.config.isLocalHost {
                existing.config.isLocalHost = true
                existing.client.update(config: existing.config)
                persist()
            }
            existing.authEnabled = found.authRequired
            if !existing.isOnline {
                if found.authRequired && !found.authenticated && !existing.client.hasSession {
                    existing.markNeedsLogin()
                } else {
                    await refresh(existing)
                }
            }
            if manual {
                activeServerID = existing.id
                localScanMessage = "已连接本机 Frog Bot（\(found.url)）"
            }
            return
        }

        let dismissed = UserDefaults.standard.bool(forKey: Self.dismissedLocalKey)
        guard manual || !dismissed else { return }

        let config = ServerConfig(
            id: UUID(),
            name: "本机",
            url: found.url,
            username: found.username?.isEmpty == false ? found.username! : (found.authRequired ? "admin" : "local"),
            tunnelURI: nil,
            isLocalHost: true
        )
        let entry = ServerEntry(config: config)
        entry.authEnabled = found.authRequired
        servers.insert(entry, at: 0)
        persist()
        wire(entry)
        if activeServerID == nil || manual {
            activeServerID = entry.id
        }
        if found.authRequired && !found.authenticated {
            entry.markNeedsLogin()
            if manual {
                sheet = .login(entry.id)
                localScanMessage = "发现本机 Frog Bot（\(found.url)），请登录后使用"
            }
        } else {
            await refresh(entry)
            if manual {
                localScanMessage = "已自动接入本机 Frog Bot（\(found.url)）"
            }
        }
    }

    private static func probeLoopbackPorts(_ range: ClosedRange<Int>) async -> LocalHostDiscovery? {
        let cfg = URLSessionConfiguration.ephemeral
        cfg.timeoutIntervalForRequest = 0.75
        cfg.timeoutIntervalForResource = 1.2
        cfg.httpCookieStorage = nil
        cfg.urlCache = nil
        let probeSession = URLSession(configuration: cfg)
        defer { probeSession.invalidateAndCancel() }

        return await withTaskGroup(of: LocalHostDiscovery?.self) { group in
            for port in range {
                group.addTask {
                    guard let healthURL = URL(string: "http://127.0.0.1:\(port)/api/health") else { return nil }
                    do {
                        let (data, resp) = try await probeSession.data(from: healthURL)
                        guard let http = resp as? HTTPURLResponse, http.statusCode == 200,
                              let j = JSON.parse(data) else { return nil }
                        // 确认是 Frog Bot 自己的健康检查接口（返回 {"ok":true,...} 或带 version/status 字段）
                        let ok = j["ok"].bool || j["status"].str == "ok" || !j["version"].str.isEmpty
                        guard ok else { return nil }
                        let version = j["version"].str

                        var authRequired = false
                        var authenticated = true
                        var username: String? = nil
                        if let meURL = URL(string: "http://127.0.0.1:\(port)/api/auth/me"),
                           let (meData, meResp) = try? await probeSession.data(from: meURL),
                           let meHTTP = meResp as? HTTPURLResponse, meHTTP.statusCode == 200,
                           let me = try? JSONDecoder().decode(AuthInfo.self, from: meData) {
                            authRequired = me.enabled
                            authenticated = !me.enabled || (me.authenticated ?? false)
                            username = me.username
                        }
                        return LocalHostDiscovery(
                            port: port,
                            url: "http://127.0.0.1:\(port)",
                            version: version,
                            authRequired: authRequired,
                            authenticated: authenticated,
                            username: username
                        )
                    } catch {
                        return nil
                    }
                }
            }
            var hits: [LocalHostDiscovery] = []
            for await hit in group {
                if let hit { hits.append(hit) }
            }
            return hits.min(by: { $0.port < $1.port })
        }
    }

    // MARK: 服务器

    private func persist() {
        let configs = servers.map(\.config)
        if let data = try? JSONEncoder().encode(configs) {
            UserDefaults.standard.set(data, forKey: Self.serversKey)
        }
    }

    private func wire(_ s: ServerEntry) {
        s.onNeedsLogin = { [weak self] entry in self?.stopSessions(of: entry) }
        startPolling(s)
    }

    /// 每 10 秒刷新一次设备和 Bot 列表（和网页版一样）。每台服务器单独轮询，离线的不拖累别的。
    private func startPolling(_ s: ServerEntry) {
        s.pollTask?.cancel()
        s.pollTask = Task { [weak self, weak s] in
            while !Task.isCancelled {
                guard let self, let s else { return }
                await self.refresh(s)
                try? await Task.sleep(nanoseconds: 10_000_000_000)
            }
        }
    }

    func refresh(_ s: ServerEntry) async {
        if s.status == .needsLogin { return }
        defer { relay.apply() }
        do {
            var groups: [DeviceGroup]
            do {
                groups = try await s.client.get("/api/devices")
            } catch let e as APIError where e.status == 404 {
                // 没有设备互联功能的旧版本：只有本机的 Bot
                let bots: [BotProfile] = try await s.client.get("/api/bots")
                groups = [DeviceGroup(id: "local", name: s.name, online: true, bots: bots)]
            }
            s.devices = groups
            s.status = .online
            syncSessions(s)
            // 如果当前激活主机还不在线，自动切到刚连上的在线主机（例如本机）
            if activeServer == nil || activeServer?.isOnline != true {
                activeServerID = s.id
            }
            // 选中的 Bot 不在了（或者属于离线主机而当前主机在线）才换一个
            if let sel = selection {
                let selServerOnline = server(sel.server)?.isOnline == true
                if sel.server == s.id && !exists(sel) || server(sel.server) == nil || (!selServerOnline && activeServerID == s.id) {
                    selectFirst(preferring: activeServer)
                }
            } else {
                selectFirst(preferring: activeServer)
            }
        } catch APIError.unauthorized {
            s.markNeedsLogin()
        } catch {
            s.status = .offline(error.localizedDescription)
        }
    }

    func exists(_ ref: BotRef) -> Bool {
        guard let s = server(ref.server) else { return false }
        return s.devices.contains { $0.id == ref.device && $0.bots.contains { $0.id == ref.bot } }
    }

    private func selectFirst(preferring preferred: ServerEntry? = nil) {
        if let preferred {
            for g in preferred.devices {
                if let b = g.bots.first {
                    selection = BotRef(server: preferred.id, device: g.id, bot: b.id)
                    return
                }
            }
        }
        let ordered = servers.filter(\.isOnline) + servers.filter { !$0.isOnline }
        for s in ordered {
            for g in s.devices {
                if let b = g.bots.first {
                    selection = BotRef(server: s.id, device: g.id, bot: b.id)
                    return
                }
            }
        }
    }

    /// 让会话和服务器上的 Bot 列表保持一致：新 Bot 开始连接，删掉的 Bot 断开。
    private func syncSessions(_ s: ServerEntry) {
        var alive = Set<BotRef>()
        for g in s.devices where g.online {
            for b in g.bots {
                let ref = BotRef(server: s.id, device: g.id, bot: b.id)
                alive.insert(ref)
                let deviceName = g.id == "local" ? s.name : g.name
                if let existing = sessions[ref] {
                    if existing.profile != b { existing.profile = b }
                    existing.deviceName = deviceName
                    existing.start()
                } else if sessions.count < maxSessions || ref == selection {
                    let session = BotSession(ref: ref, profile: b, deviceName: deviceName, server: s)
                    session.onNews = { [weak self] sess, text, kind in self?.handleNews(sess, text, kind) }
                    sessions[ref] = session
                    session.start()
                }
            }
        }
        // 设备离线时保留会话（它会自己重连）；Bot 被删除时才断开
        let removed = sessions.keys.filter { ref in
            ref.server == s.id && !alive.contains(ref) && !s.devices.contains { $0.id == ref.device && !$0.online }
        }
        for ref in removed {
            sessions[ref]?.stop()
            sessions[ref] = nil
            unread[ref] = nil
        }
    }

    func stopSessions(of s: ServerEntry) {
        for (ref, session) in sessions where ref.server == s.id { session.stop() }
    }

    /// 添加服务器，或给已有服务器重新登录 / 修改设置。支持 `froge2ee://` 零配置端到端加密连接码。
    func connect(existing: ServerEntry?, name: String, address: String, username: String, password: String) async throws {
        var config: ServerConfig
        let client: ServerClient
        let authEnabled: Bool
        let fallbackHost: String?

        if let extracted = ServerConfig.extractTunnelURI(address) {
            let parsed = try E2EEConfig.parse(extracted.uri)
            let preferred = name.trimmingCharacters(in: .whitespaces)
            let resolved = !preferred.isEmpty ? preferred : (extracted.serverName ?? parsed.serverName)
            config = ServerConfig(
                id: existing?.id ?? UUID(),
                name: resolved,
                url: "http://frog.internal",
                username: "e2ee",
                tunnelURI: extracted.uri
            )
            client = ServerClient(config: config)
            authEnabled = false
            fallbackHost = resolved.isEmpty ? "frog" : resolved
        } else {
            guard let url = ServerConfig.normalize(address) else {
                throw APIError.invalid("地址格式不对，例如 froge2ee://...、https://bot.example.com 或 http://1.2.3.4:8080")
            }
            let user = username.trimmingCharacters(in: .whitespaces)
            config = ServerConfig(
                id: existing?.id ?? UUID(),
                name: name.trimmingCharacters(in: .whitespaces),
                url: url.absoluteString,
                username: user.isEmpty ? "admin" : user,
                tunnelURI: nil
            )
            client = ServerClient(config: config)
            let me = try await client.me()
            if me.enabled && me.authenticated != true {
                guard !password.isEmpty else { throw APIError.invalid("请输入密码") }
                try await client.login(username: config.username, password: password)
            }
            authEnabled = me.enabled
            fallbackHost = url.host
        }

        // 确认真的能读到 Bot 列表
        var groups: [DeviceGroup]
        do {
            do {
                groups = try await client.get("/api/devices")
            } catch let e as APIError where e.status == 404 {
                let bots: [BotProfile] = try await client.get("/api/bots")
                groups = [DeviceGroup(id: "local", name: fallbackHost ?? "本机", online: true, bots: bots)]
            }
        } catch {
            client.close()
            throw error
        }
        if config.name.isEmpty {
            config.name = groups.first?.name ?? fallbackHost ?? "frog"
        }
        client.update(config: config)

        if let s = existing {
            stopSessions(of: s)
            for ref in sessions.keys where ref.server == s.id { sessions[ref] = nil }
            s.client.close()
            s.config = config
            s.client = client
            s.authEnabled = authEnabled
            s.devices = groups
            s.status = .online
            persist()
            syncSessions(s)
            startPolling(s)
            relay.apply()
        } else {
            let s = ServerEntry(config: config)
            s.client = client
            s.authEnabled = authEnabled
            s.devices = groups
            s.status = .online
            servers.append(s)
            persist()
            wire(s)
            syncSessions(s)
            relay.apply()
            if let g = groups.first, let b = g.bots.first {
                selection = BotRef(server: s.id, device: g.id, bot: b.id)
            }
        }
    }

    func remove(_ s: ServerEntry) {
        if s.config.isLocalHost || s.config.isLoopbackEndpoint {
            UserDefaults.standard.set(true, forKey: Self.dismissedLocalKey)
        }
        relay.forget(s)
        s.pollTask?.cancel()
        s.gateway.disconnect()
        stopSessions(of: s)
        for ref in sessions.keys where ref.server == s.id {
            sessions[ref] = nil
            unread[ref] = nil
        }
        agyStores[s.id] = nil
        for (k, cs) in agyChatSessions where cs.server.id == s.id {
            cs.stop()
            agyChatSessions[k] = nil
        }
        if codingSelection?.server == s.id {
            codingSelection = nil
        }
        let client = s.client
        Task {
            if !s.config.isTunneled {
                _ = try? await client.post("/api/auth/logout")
            }
            client.close()
        }
        client.forgetSession()
        servers.removeAll { $0.id == s.id }
        persist()
        if activeServerID == s.id {
            activeServerID = servers.first?.id
        }
        if selection?.server == s.id {
            selection = nil
            selectFirst()
        }
    }

    /// 改密码：服务器会让所有旧会话失效，这里用新密码立刻重新登录。
    func changePassword(_ s: ServerEntry, old: String, new: String) async throws {
        try await s.client.post("/api/auth/password", body: ["old_password": old, "new_password": new])
        try await s.client.login(username: s.config.username, password: new)
        s.status = .connecting
        for (ref, session) in sessions where ref.server == s.id { session.stop(); session.start() }
        await refresh(s)
    }

    func logout(_ s: ServerEntry) async {
        _ = try? await s.client.post("/api/auth/logout")
        s.client.forgetSession()
        stopSessions(of: s)
        s.markNeedsLogin()
        relay.apply()
    }

    func checkUpdate(_ s: ServerEntry, channel: String? = nil) async throws -> UpdateStatus {
        var q: [URLQueryItem] = []
        if let channel, !channel.isEmpty {
            q.append(URLQueryItem(name: "channel", value: channel))
        }
        return try await s.client.get("/api/update", query: q)
    }

    func applyUpdate(_ s: ServerEntry, channel: String, version: String = "", password: String = "") async throws -> JSON {
        try await s.client.post("/api/update/apply", body: [
            "channel": channel,
            "version": version,
            "password": password,
        ])
    }

    func rename(_ s: ServerEntry, to name: String) {
        let n = name.trimmingCharacters(in: .whitespaces)
        guard !n.isEmpty else { return }
        s.config.name = n
        s.client.update(config: s.config)
        persist()
        syncSessions(s)
        relay.apply()
    }

    func move(from: IndexSet, to: Int) {
        servers.move(fromOffsets: from, toOffset: to)
        persist()
    }

    // MARK: Bot

    func saveBot(server s: ServerEntry, device: String, existing: BotProfile?, name: String, instructions: String) async throws {
        let body: [String: Any] = ["name": name, "instructions": instructions]
        if let existing {
            try await s.client.post("/api/bots/\(existing.id)", body: body, method: "PATCH")
        } else {
            let path = device == "local" ? "/api/bots" : "/api/devices/\(device)/bots"
            let out = try await s.client.post(path, body: body)
            let id = out["id"].str
            if !id.isEmpty { selection = BotRef(server: s.id, device: device, bot: id) }
        }
        await refresh(s)
    }

    func deleteBot(_ ref: BotRef, purge: Bool) async throws {
        guard let s = server(ref.server) else { return }
        try await s.client.post("/api/bots/\(ref.bot)", method: "DELETE", query: purge ? [URLQueryItem(name: "purge", value: "1")] : [])
        sessions[ref]?.stop()
        sessions[ref] = nil
        if selection == ref { selection = nil }
        await refresh(s)
        if selection == nil { selectFirst() }
    }

    // MARK: 提醒

    private func handleNews(_ session: BotSession, _ text: String, _ kind: NewsKind) {
        let visible = NSApp.isActive && selection == session.ref && route == .chat
        if visible { return }
        unread[session.ref, default: 0] += 1
        let wanted: Bool
        switch kind {
        case .reply: wanted = Prefs.bool(Prefs.notifyReply)
        case .task: wanted = Prefs.bool(Prefs.notifyTask)
        case .approval: wanted = Prefs.bool(Prefs.notifyApproval)
        }
        guard wanted else { return }
        let title = session.ref.device == "local" && servers.count == 1
            ? session.profile.name
            : "\(session.profile.name) · \(session.deviceName)"
        Notifier.post(title: title, body: text, ref: session.ref)
    }

    func clearUnreadForSelection() {
        if let selection { unread[selection] = nil }
    }

    func open(_ ref: BotRef) {
        selection = ref
        route = .chat
        NSApp.activate(ignoringOtherApps: true)
    }
}
