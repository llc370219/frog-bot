import Foundation
import UIKit
import UserNotifications
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

    @ObservationIgnored var onNeedsLogin: ((ServerEntry) -> Void)?
    @ObservationIgnored var pollTask: Task<Void, Never>?

    init(config: ServerConfig) {
        id = config.id
        self.config = config
        client = ServerClient(config: config)
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

/// 偏好设置（「设置 › 外观 / 通知」）。外观由 RootView 用 preferredColorScheme 应用。
enum Prefs {
    static let appearance = "pref.appearance" // system | light | dark
    static let notifyApproval = "pref.notify.approval"
    static let notifyTask = "pref.notify.task"
    static let notifyReply = "pref.notify.reply"
    static let appBadge = "pref.appBadge"

    static func registerDefaults() {
        UserDefaults.standard.register(defaults: [
            appearance: "system",
            notifyApproval: true, notifyTask: true, notifyReply: true, appBadge: true,
        ])
    }

    static func bool(_ key: String) -> Bool { UserDefaults.standard.bool(forKey: key) }
}

enum ActiveSheet: Identifiable, Equatable {
    case addServer
    case login(UUID)
    case editServer(UUID)
    case newBot(server: UUID, device: String)
    case editBot(BotRef)

    var id: String {
        switch self {
        case .addServer: return "add"
        case .login(let id): return "login-\(id)"
        case .editServer(let id): return "edit-\(id)"
        case .newBot(let s, let d): return "new-\(s)-\(d)"
        case .editBot(let r): return "bot-\(r.key)"
        }
    }
}

/// 整个面板的状态：服务器列表、当前选中的 Bot、每个 Bot 的实时会话、未读计数。
@MainActor
@Observable
final class AppModel {
    static let shared = AppModel()

    var servers: [ServerEntry] = []
    var selection: BotRef? {
        didSet {
            if let selection {
                unread[selection] = nil
                UserDefaults.standard.set(selection.key, forKey: Self.selectionKey)
            }
        }
    }
    var sheet: ActiveSheet?
    var unread: [BotRef: Int] = [:] { didSet { updateBadge() } }
    var route: Route = .chat
    /// 聊天页正显示在屏幕上（返回列表或切到别的页面时为 false），用来决定要不要提醒
    var chatOnScreen = false
    /// 点通知或列表要打开的 Bot：导航栈看到后推进聊天页
    var pendingOpen: BotRef?
    /// 打开设置窗口时要显示的服务器（从侧栏「服务器设置…」进来）
    var settingsServer: UUID?
    /// 客户端互联（设置 › 互联）
    let relay = RelayManager()

    /// 会话：一旦打开就一直连着（后台任务的进展和审批都能及时提醒）
    @ObservationIgnored private(set) var sessions: [BotRef: BotSession] = [:]
    /// 最多同时保持的事件流连接数
    private let maxSessions = 48

    private static let serversKey = "frogbotclaude.servers"
    private static let selectionKey = "frogbotclaude.selection"

    private init() {
        Prefs.registerDefaults()
        if let data = UserDefaults.standard.data(forKey: Self.serversKey),
           let configs = try? JSONDecoder().decode([ServerConfig].self, from: data) {
            servers = configs.map(ServerEntry.init)
        }
        if let key = UserDefaults.standard.string(forKey: Self.selectionKey) {
            selection = BotRef(key: key)
        }
        for s in servers { wire(s) }
        relay.attach(self)
    }

    var totalUnread: Int { unread.values.reduce(0, +) }

    func server(_ id: UUID) -> ServerEntry? { servers.first { $0.id == id } }

    func session(_ ref: BotRef) -> BotSession? { sessions[ref] }

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
            // 选中的 Bot 不在了（或者还没选过）才换一个；别的服务器还没刷新时不要误判
            if let sel = selection {
                if sel.server == s.id && !exists(sel) || server(sel.server) == nil { selectFirst() }
            } else {
                selectFirst()
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

    private func selectFirst() {
        for s in servers {
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

    /// 添加服务器，或给已有服务器重新登录 / 修改设置。
    func connect(existing: ServerEntry?, name: String, address: String, username: String, password: String) async throws {
        guard let url = ServerConfig.normalize(address) else {
            throw APIError.invalid("地址格式不对，例如 https://bot.example.com 或 http://1.2.3.4:8080")
        }
        let user = username.trimmingCharacters(in: .whitespaces)
        var config = ServerConfig(id: existing?.id ?? UUID(), name: name.trimmingCharacters(in: .whitespaces),
                                  url: url.absoluteString, username: user.isEmpty ? "admin" : user)
        let client = ServerClient(config: config)
        let me = try await client.me()
        if me.enabled && me.authenticated != true {
            guard !password.isEmpty else { throw APIError.invalid("请输入密码") }
            try await client.login(username: config.username, password: password)
        }
        // 确认真的能读到 Bot 列表
        var groups: [DeviceGroup]
        do {
            groups = try await client.get("/api/devices")
        } catch let e as APIError where e.status == 404 {
            let bots: [BotProfile] = try await client.get("/api/bots")
            groups = [DeviceGroup(id: "local", name: url.host ?? "本机", online: true, bots: bots)]
        }
        if config.name.isEmpty {
            config.name = groups.first?.name ?? url.host ?? "frog"
        }
        client.update(config: config)

        if let s = existing {
            stopSessions(of: s)
            for ref in sessions.keys where ref.server == s.id { sessions[ref] = nil }
            s.config = config
            s.client = client
            s.authEnabled = me.enabled
            s.devices = groups
            s.status = .online
            persist()
            syncSessions(s)
            startPolling(s)
            relay.apply()
        } else {
            let s = ServerEntry(config: config)
            s.client = client
            s.authEnabled = me.enabled
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
        relay.forget(s)
        s.pollTask?.cancel()
        stopSessions(of: s)
        for ref in sessions.keys where ref.server == s.id {
            sessions[ref] = nil
            unread[ref] = nil
        }
        let client = s.client
        Task { try? await client.post("/api/auth/logout") }
        client.forgetSession()
        servers.removeAll { $0.id == s.id }
        persist()
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

    func checkUpdate(_ s: ServerEntry) async throws -> UpdateStatus {
        try await s.client.get("/api/update")
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
        let visible = UIApplication.shared.applicationState == .active && chatOnScreen
            && selection == session.ref && route == .chat
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

    // MARK: 前后台

    @ObservationIgnored private var backgroundAt: Date?

    /// 退到后台：互联暂停（马上通知各服务器）；事件流交给系统挂起，回来再接上
    func enterBackground() {
        backgroundAt = Date()
        var bg = UIBackgroundTaskIdentifier.invalid
        bg = UIApplication.shared.beginBackgroundTask(withName: "relay-pause") {
            UIApplication.shared.endBackgroundTask(bg)
        }
        relay.paused = true
        Task {
            try? await Task.sleep(nanoseconds: 3_000_000_000)
            UIApplication.shared.endBackgroundTask(bg)
        }
    }

    /// 回到前台：刷新服务器列表；离开超过 20 秒的话，挂起期间的连接多半已经断了，全部重连（带 after= 续传，不会丢消息）
    func becameActive() {
        let away = backgroundAt.map { Date().timeIntervalSince($0) } ?? 0
        backgroundAt = nil
        relay.paused = false
        if away > 20 {
            for session in sessions.values where session.server.isOnline {
                session.stop()
                session.start()
            }
        }
        for s in servers { Task { await refresh(s) } }
        updateBadge()
    }

    func updateBadge() {
        let n = Prefs.bool(Prefs.appBadge) ? totalUnread : 0
        UNUserNotificationCenter.current().setBadgeCount(n) { _ in }
    }

    func clearUnreadForSelection() {
        if let selection { unread[selection] = nil }
    }

    func open(_ ref: BotRef) {
        selection = ref
        route = .chat
        pendingOpen = ref
    }
}
