import Foundation
import Observation

/// 客户端互联：这个客户端里登录的几台服务器，它们的聊天 Bot 可以互相发文字（问问题、托付任务）。
///
/// - 只在客户端开着、总开关打开时工作；服务器之间不直接连接，消息由客户端转交，不排队。
/// - 每台服务器、每个 Bot 都可以单独关掉，关掉的不会出现在别人的通讯录里，也收不到消息。
/// - 只转交文字：对方收到后做不做、怎么做，都在对方自己的机器上决定。
///
/// 每台参与的服务器一个长轮询循环：`POST /api/relay/sync` 上报通讯录并取走要转发的消息，
/// 转给目标服务器的 `POST /api/relay/inbox`，下一轮带上回执。
@MainActor
@Observable
final class RelayManager {
    enum ServerState: Equatable {
        case off
        case connecting
        case waiting      // 打开了，但还没有可以联系的对象（至少要两台服务器各有一个 Bot 参与）
        case active
        case unsupported  // 服务器版本太旧
        case failed(String)
    }

    private static let enabledKey = "relay.enabled"
    private static let offServersKey = "relay.offServers"
    private static let offBotsKey = "relay.offBots"
    private static let clientIDKey = "relay.clientID"

    /// 总开关（默认关）
    var enabled: Bool { didSet { UserDefaults.standard.set(enabled, forKey: Self.enabledKey); apply() } }
    private(set) var offServers: Set<String>
    private(set) var offBots: Set<String>
    private(set) var states: [UUID: ServerState] = [:]

    @ObservationIgnored private weak var model: AppModel?
    private let clientID: String
    @ObservationIgnored private var loops: [UUID: Task<Void, Never>] = [:]
    @ObservationIgnored private var pushed: [UUID: String] = [:]
    /// 曾经同步过的服务器（关掉时要通知它们）
    @ObservationIgnored private var joined: [UUID: ServerClient] = [:]

    init() {
        let d = UserDefaults.standard
        enabled = d.bool(forKey: Self.enabledKey)
        offServers = Set(d.stringArray(forKey: Self.offServersKey) ?? [])
        offBots = Set(d.stringArray(forKey: Self.offBotsKey) ?? [])
        if let id = d.string(forKey: Self.clientIDKey), !id.isEmpty {
            clientID = id
        } else {
            clientID = "mac-" + UUID().uuidString.replacingOccurrences(of: "-", with: "").lowercased()
            d.set(clientID, forKey: Self.clientIDKey)
        }
    }

    func attach(_ model: AppModel) {
        self.model = model
        apply()
    }

    // MARK: 开关

    func isOn(_ s: ServerEntry) -> Bool { !offServers.contains(s.id.uuidString) }
    func isOn(_ s: ServerEntry, bot: String) -> Bool { !offBots.contains(Self.botKey(s, bot)) }

    func set(_ s: ServerEntry, on: Bool) {
        if on { offServers.remove(s.id.uuidString) } else { offServers.insert(s.id.uuidString) }
        UserDefaults.standard.set(Array(offServers), forKey: Self.offServersKey)
        apply()
    }

    func set(_ s: ServerEntry, bot: String, on: Bool) {
        let k = Self.botKey(s, bot)
        if on { offBots.remove(k) } else { offBots.insert(k) }
        UserDefaults.standard.set(Array(offBots), forKey: Self.offBotsKey)
        apply()
    }

    private static func botKey(_ s: ServerEntry, _ bot: String) -> String { "\(s.id.uuidString)/\(bot)" }

    /// 服务器在互联地址里的名字：relay:<key>/<BotID>
    static func key(_ s: ServerEntry) -> String {
        String(s.id.uuidString.replacingOccurrences(of: "-", with: "").lowercased().prefix(12))
    }

    /// 这台服务器本机上参与互联的 Bot（设备互联过来的远程 Bot 不算，它们走服务器自己的设备互联）
    func participants(_ s: ServerEntry) -> [BotProfile] {
        guard enabled, isOn(s), s.isOnline else { return [] }
        return (s.devices.first { $0.id == "local" }?.bots ?? []).filter { isOn(s, bot: $0.id) }
    }

    func localBots(_ s: ServerEntry) -> [BotProfile] { s.devices.first { $0.id == "local" }?.bots ?? [] }

    // MARK: 同步

    /// 开关、服务器列表或 Bot 列表变了以后调用：启停循环，并立刻把新通讯录告诉每台服务器。
    func apply() {
        guard let model else { return }
        var wanted: [UUID: [String: Any]] = [:]
        for s in model.servers {
            guard let body = syncBody(s, in: model) else {
                states[s.id] = enabled && isOn(s) && s.isOnline ? .waiting : .off
                continue
            }
            wanted[s.id] = body
        }
        // 不再参与的：停掉循环，告诉服务器马上把通讯录清空
        let me = clientID
        for (id, client) in joined where wanted[id] == nil {
            loops[id]?.cancel()
            loops[id] = nil
            pushed[id] = nil
            joined[id] = nil
            Task { _ = try? await client.post("/api/relay/sync", body: ["client_id": me, "enabled": false], timeout: 5) }
        }
        for s in model.servers {
            guard let body = wanted[s.id] else { continue }
            joined[s.id] = s.client
            if loops[s.id] == nil {
                pushed[s.id] = signature(body)
                states[s.id] = .connecting
                loops[s.id] = Task { [weak self, weak s] in
                    guard let s else { return }
                    await self?.loop(s)
                }
            } else if pushed[s.id] != signature(body) {
                // 循环正在长轮询，旧通讯录要等它返回才更新；单独发一次不等待的同步，立刻生效
                pushed[s.id] = signature(body)
                Task { [weak self, weak s] in
                    guard let self, let s else { return }
                    if let items = try? await self.sync(s, acks: [], wait: false) {
                        let acks = await self.forward(items, from: s)
                        if !acks.isEmpty { _ = try? await self.sync(s, acks: acks, wait: false) }
                    }
                }
            }
        }
    }

    var isRunning: Bool { !joined.isEmpty }

    /// 退出 App 前调用：让各服务器马上忘掉这个客户端（最多等 2 秒）。
    func shutdown() async {
        let clients = Array(joined.values)
        for t in loops.values { t.cancel() }
        loops = [:]
        joined = [:]
        let id = clientID
        await withTaskGroup(of: Void.self) { group in
            for c in clients {
                group.addTask { @MainActor in
                    _ = try? await c.post("/api/relay/sync", body: ["client_id": id, "enabled": false], timeout: 2)
                }
            }
        }
    }

    /// 服务器从客户端里删掉之前调用
    func forget(_ s: ServerEntry) {
        states[s.id] = nil
        guard let client = joined.removeValue(forKey: s.id) else { return }
        loops.removeValue(forKey: s.id)?.cancel()
        pushed[s.id] = nil
        let id = clientID
        Task { _ = try? await client.post("/api/relay/sync", body: ["client_id": id, "enabled": false], timeout: 5) }
        // 其他服务器的通讯录里也要去掉它
        Task { @MainActor [weak self] in self?.apply() }
    }

    /// 这台服务器要上报的内容；没有可参与的 Bot 或没有可以联系的对象时返回 nil。
    private func syncBody(_ s: ServerEntry, in model: AppModel) -> [String: Any]? {
        let mine = participants(s)
        guard !mine.isEmpty else { return nil }
        var peers: [[String: String]] = []
        for other in model.servers where other.id != s.id {
            for b in participants(other) {
                peers.append(["address": "relay:\(Self.key(other))/\(b.id)", "name": b.name, "server": other.name])
            }
        }
        guard !peers.isEmpty else { return nil }
        return ["client_id": clientID, "enabled": true, "allowed": mine.map(\.id), "peers": peers]
    }

    private func signature(_ body: [String: Any]) -> String {
        let data = (try? JSONSerialization.data(withJSONObject: body, options: [.sortedKeys])) ?? Data()
        return String(decoding: data, as: UTF8.self)
    }

    private struct Idle: Error {}

    private func sync(_ s: ServerEntry, acks: [[String: String]], wait: Bool) async throws -> [JSON] {
        guard let model, var body = syncBody(s, in: model) else { throw Idle() }
        body["acks"] = acks
        body["wait"] = wait
        let out = try await s.client.post("/api/relay/sync", body: body, timeout: wait ? 40 : 10)
        return out["items"].array
    }

    private func loop(_ s: ServerEntry) async {
        var acks: [[String: String]] = []
        var failures = 0
        while !Task.isCancelled {
            do {
                let items = try await sync(s, acks: acks, wait: true)
                acks = await forward(items, from: s)
                failures = 0
                states[s.id] = .active
            } catch is Idle {
                // 刚好没有可参与的对象了：apply() 会停掉这个循环，这里先歇一下
                try? await Task.sleep(nanoseconds: 2_000_000_000)
            } catch let e as APIError where e.status == 404 {
                states[s.id] = .unsupported
                acks = []
                try? await Task.sleep(nanoseconds: 60_000_000_000)
            } catch {
                if Task.isCancelled { return }
                failures += 1
                acks = []
                states[s.id] = .failed(error.localizedDescription)
                let delay = UInt64(min(30, 1 << min(failures, 5)))
                try? await Task.sleep(nanoseconds: delay * 1_000_000_000)
            }
        }
    }

    /// 把服务器 s 上的 Bot 发出的消息送到目标服务器，返回回执。
    private func forward(_ items: [JSON], from s: ServerEntry) async -> [[String: String]] {
        var acks: [[String: String]] = []
        for it in items {
            let id = it["id"].str
            guard !id.isEmpty else { continue }
            let to = it["to"].str.dropFirst("relay:".count)
            let parts = to.split(separator: "/", maxSplits: 1).map(String.init)
            var ack = ["id": id]
            if parts.count == 2, let target = model?.servers.first(where: { Self.key($0) == parts[0] }),
               !participants(target).isEmpty {
                let body: [String: Any] = [
                    "client_id": clientID, "id": id, "from": "relay:\(Self.key(s))/\(it["from"].str)", "to": parts[1],
                    "text": it["text"].str, "request_id": it["request_id"].str, "reply_to": it["reply_to"].str,
                    "wants_reply": it["wants_reply"].bool,
                ]
                do {
                    _ = try await target.client.post("/api/relay/inbox", body: body, timeout: 8)
                } catch {
                    ack["error"] = "转交失败：\(error.localizedDescription)"
                }
            } else {
                ack["error"] = "对方不在线或没有打开互联"
            }
            acks.append(ack)
        }
        return acks
    }
}
