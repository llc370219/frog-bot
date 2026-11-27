import AppKit
import Foundation
import Observation

// MARK: - Frog Bot V2 Gateway Models

struct V2TaintSource: Codable, Hashable {
    let kind: String
    let origin: String
}

struct V2ApprovalCard: Codable, Identifiable, Hashable {
    let id: String
    let botID: String
    let action: String
    let triggerSource: String
    let tainted: Bool
    let taintSources: [V2TaintSource]?
    let targetDomain: String?
    let recipient: String?
    let summary: String
    let secretHandles: [String]?
    let reason: String
    let canWhitelist: Bool
    let whitelistSecret: String?
    let whitelistDomain: String?

    enum CodingKeys: String, CodingKey {
        case id, action, tainted, recipient, summary, reason
        case botID = "bot_id"
        case triggerSource = "trigger_source"
        case taintSources = "taint_sources"
        case targetDomain = "target_domain"
        case secretHandles = "secret_handles"
        case canWhitelist = "can_whitelist"
        case whitelistSecret = "whitelist_secret"
        case whitelistDomain = "whitelist_domain"
    }
}

struct V2VerifySession: Codable, Identifiable, Hashable {
    let id: String
    let botID: String
    let url: String
    let reason: String
    let status: String
    let viewportWidth: Int
    let viewportHeight: Int

    enum CodingKeys: String, CodingKey {
        case id, url, reason, status
        case botID = "bot_id"
        case viewportWidth = "viewport_width"
        case viewportHeight = "viewport_height"
    }
}

struct V2CLITask: Codable, Identifiable, Hashable {
    let id: String
    let botID: String
    let cli: String
    let mode: String
    let status: String
    let promptBrief: String?
    let summary: String?
    let changedFiles: [String]?

    enum CodingKeys: String, CodingKey {
        case id, cli, mode, status, summary
        case botID = "bot_id"
        case promptBrief = "prompt_brief"
        case changedFiles = "changed_files"
    }
}

struct V2MeshPeer: Codable, Identifiable, Hashable {
    var id: String { nodeID }
    let nodeID: String
    let name: String
    let networkTag: String?
    let online: Bool
    let connectedVia: String?
    let bots: [String]?

    enum CodingKeys: String, CodingKey {
        case name, online, bots
        case nodeID = "node_id"
        case networkTag = "network_tag"
        case connectedVia = "connected_via"
    }
}

private struct WSOutboundFrame: Encodable {
    let type: String
    let reqID: String?
    let target: String?
    let payload: AnyEncodable?

    enum CodingKeys: String, CodingKey {
        case type, target, payload
        case reqID = "req_id"
    }
}

private struct AnyEncodable: Encodable {
    private let encodeFn: (Encoder) throws -> Void
    init<T: Encodable>(_ value: T) { encodeFn = value.encode }
    func encode(to encoder: Encoder) throws { try encodeFn(encoder) }
}

/// Frog Bot V2 单点总控 WebSocket 网关客户端：
/// 连上一台主节点（Home Node）即可漫游操控全网 `bot@node`，并实时接收审查审批卡、
/// Ready-to-Verify CDP 验证推流帧与 Coding CLI 实时日志流。
@MainActor
@Observable
final class GatewayClient {
    var connected = false
    var nodeID: String = ""
    var pendingApprovals: [V2ApprovalCard] = []
    var verifySessions: [V2VerifySession] = []
    var cliTasks: [V2CLITask] = []
    var cliLogs: [String: String] = [:]
    var meshPeers: [V2MeshPeer] = []
    var latestFrame: NSImage?
    var activeVerifyID: String?
    var lastAlert: String?

    @ObservationIgnored private var wsTask: URLSessionWebSocketTask?
    @ObservationIgnored private var loopTask: Task<Void, Never>?
    @ObservationIgnored private weak var serverEntry: ServerEntry?

    var attentionCount: Int {
        pendingApprovals.count + verifySessions.filter { $0.status == "WAITING_HUMAN" || $0.status == "VERIFYING" }.count
    }

    func attach(_ server: ServerEntry) {
        serverEntry = server
        connect()
    }

    func disconnect() {
        loopTask?.cancel()
        loopTask = nil
        wsTask?.cancel(with: .normalClosure, reason: nil)
        wsTask = nil
        connected = false
    }

    func connect() {
        loopTask?.cancel()
        guard let srv = serverEntry, !srv.config.isTunneled else {
            connected = false
            return
        }
        loopTask = Task { [weak self] in
            while !Task.isCancelled {
                guard let self, let srv = self.serverEntry, !srv.config.isTunneled else { return }
                await self.runOnce(srv)
                self.connected = false
                try? await Task.sleep(nanoseconds: 3_000_000_000)
            }
        }
    }

    private func runOnce(_ server: ServerEntry) async {
        guard let base = server.config.baseURL,
              var comps = URLComponents(url: base.appendingPathComponent("api/v2/ws"), resolvingAgainstBaseURL: false) else {
            return
        }
        comps.scheme = (base.scheme?.lowercased() == "https") ? "wss" : "ws"
        guard let wsURL = comps.url else { return }

        var req = URLRequest(url: wsURL)
        if let cookie = SessionVault.get(server.id), !cookie.isEmpty {
            req.setValue("frogbot_session=\(cookie)", forHTTPHeaderField: "Cookie")
        }

        let session = URLSession(configuration: .ephemeral)
        let task = session.webSocketTask(with: req)
        wsTask = task
        task.resume()
        connected = true
        await refreshStatusREST(server)

        while !Task.isCancelled {
            do {
                let message = try await task.receive()
                switch message {
                case .string(let text):
                    if let data = text.data(using: .utf8) {
                        handleFrame(data)
                    }
                case .data(let data):
                    handleFrame(data)
                @unknown default:
                    break
                }
            } catch {
                break
            }
        }
        task.cancel(with: .goingAway, reason: nil)
    }

    func refreshStatusREST(_ server: ServerEntry) async {
        guard let base = server.config.baseURL else { return }
        var req = URLRequest(url: base.appendingPathComponent("api/v2/status"))
        if let cookie = SessionVault.get(server.id), !cookie.isEmpty {
            req.setValue("frogbot_session=\(cookie)", forHTTPHeaderField: "Cookie")
        }
        guard let (data, resp) = try? await URLSession.shared.data(for: req),
              (resp as? HTTPURLResponse)?.statusCode == 200,
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return }

        let dec = JSONDecoder()
        if let nid = obj["node_id"] as? String { nodeID = nid }
        if let raw = obj["pending_approvals"], let d = try? JSONSerialization.data(withJSONObject: raw),
           let list = try? dec.decode([V2ApprovalCard].self, from: d) {
            pendingApprovals = list
        }
        if let raw = obj["pending_verifications"], let d = try? JSONSerialization.data(withJSONObject: raw),
           let list = try? dec.decode([V2VerifySession].self, from: d) {
            verifySessions = list
        }
        if let raw = obj["cli_tasks"], let d = try? JSONSerialization.data(withJSONObject: raw),
           let list = try? dec.decode([V2CLITask].self, from: d) {
            cliTasks = list
        }
        if let raw = obj["peers"], let d = try? JSONSerialization.data(withJSONObject: raw),
           let list = try? dec.decode([V2MeshPeer].self, from: d) {
            meshPeers = list
        }
    }

    private func handleFrame(_ data: Data) {
        guard let root = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let type = root["type"] as? String else { return }
        let dec = JSONDecoder()

        switch type {
        case "hello":
            if let p = root["payload"] as? [String: Any] {
                if let nid = p["node_id"] as? String { nodeID = nid }
                if let raw = p["pending_approvals"], let d = try? JSONSerialization.data(withJSONObject: raw),
                   let list = try? dec.decode([V2ApprovalCard].self, from: d) {
                    pendingApprovals = list
                }
                if let raw = p["pending_verifications"], let d = try? JSONSerialization.data(withJSONObject: raw),
                   let list = try? dec.decode([V2VerifySession].self, from: d) {
                    verifySessions = list
                }
            }
        case "approval.pending":
            if let raw = root["payload"], let d = try? JSONSerialization.data(withJSONObject: raw),
               let card = try? dec.decode(V2ApprovalCard.self, from: d) {
                if !pendingApprovals.contains(where: { $0.id == card.id }) {
                    pendingApprovals.insert(card, at: 0)
                }
            }
        case "verify.waiting":
            if let raw = root["payload"], let d = try? JSONSerialization.data(withJSONObject: raw),
               let sess = try? dec.decode(V2VerifySession.self, from: d) {
                verifySessions.removeAll { $0.id == sess.id }
                verifySessions.insert(sess, at: 0)
            }
        case "verify.frame":
            if let p = root["payload"] as? [String: Any],
               let b64 = p["data_base64"] as? String,
               let imgData = Data(base64Encoded: b64),
               let img = NSImage(data: imgData) {
                latestFrame = img
            }
        case "cli.log.snapshot":
            if let p = root["payload"] as? [String: Any],
               let tid = p["task_id"] as? String,
               let content = p["content"] as? String {
                cliLogs[tid] = content
            }
        case "cli.log.chunk":
            if let p = root["payload"] as? [String: Any],
               let tid = p["task_id"] as? String,
               let chunk = p["chunk"] as? String {
                cliLogs[tid, default: ""] += chunk
            }
        case "alert.security", "alert.budget":
            if let raw = root["payload"], let d = try? JSONSerialization.data(withJSONObject: raw),
               let txt = String(data: d, encoding: .utf8) {
                lastAlert = txt
            }
        default:
            break
        }
    }

    private func sendFrame<T: Encodable>(_ type: String, target: String? = nil, payload: T) {
        let frame = WSOutboundFrame(type: type, reqID: UUID().uuidString, target: target, payload: AnyEncodable(payload))
        guard let data = try? JSONEncoder().encode(frame),
              let str = String(data: data, encoding: .utf8) else { return }
        wsTask?.send(.string(str)) { _ in }
    }

    func setForeground(_ fg: Bool) {
        sendFrame("presence", payload: ["foreground": fg])
    }

    func sendRoamChat(target: String, text: String) {
        sendFrame("chat.send", target: target, payload: ["text": text])
    }

    func decideApproval(cardID: String, choice: String, whitelistDomain: String? = nil) {
        var body: [String: String] = ["card_id": cardID, "choice": choice]
        if let d = whitelistDomain { body["whitelist_domain"] = d }
        sendFrame("approval.decide", payload: body)
        pendingApprovals.removeAll { $0.id == cardID }
    }

    func subscribeCLILog(taskID: String) {
        sendFrame("cli.log.subscribe", payload: ["task_id": taskID])
    }

    func cancelCLITask(taskID: String) {
        sendFrame("cli.cancel", payload: ["task_id": taskID])
    }

    func startVerify(sessionID: String) {
        activeVerifyID = sessionID
        latestFrame = nil
        sendFrame("verify.ready", payload: ["session_id": sessionID])
    }

    func sendVerifyClick(sessionID: String, normX: Double, normY: Double) {
        struct P: Encodable {
            let session_id: String
            let event: E
            struct E: Encodable { let type = "click"; let x: Double; let y: Double; let normalized = true }
        }
        sendFrame("verify.input", payload: P(session_id: sessionID, event: .init(x: normX, y: normY)))
    }

    func sendVerifyDrag(sessionID: String, x0: Double, y0: Double, x1: Double, y1: Double) {
        struct P: Encodable {
            let session_id: String
            let event: E
            struct E: Encodable { let type = "drag"; let x: Double; let y: Double; let end_x: Double; let end_y: Double; let normalized = true; let duration_ms = 240 }
        }
        sendFrame("verify.input", payload: P(session_id: sessionID, event: .init(x: x0, y: y0, end_x: x1, end_y: y1)))
    }

    func sendVerifyText(sessionID: String, text: String) {
        struct P: Encodable {
            let session_id: String
            let event: E
            struct E: Encodable { let type = "text"; let text: String }
        }
        sendFrame("verify.input", payload: P(session_id: sessionID, event: .init(text: text)))
    }

    func completeVerify(sessionID: String) {
        sendFrame("verify.complete", payload: ["session_id": sessionID])
        verifySessions.removeAll { $0.id == sessionID }
        if activeVerifyID == sessionID { activeVerifyID = nil; latestFrame = nil }
    }

    func cancelVerify(sessionID: String) {
        sendFrame("verify.cancel", payload: ["session_id": sessionID])
        if activeVerifyID == sessionID { activeVerifyID = nil; latestFrame = nil }
    }
}
