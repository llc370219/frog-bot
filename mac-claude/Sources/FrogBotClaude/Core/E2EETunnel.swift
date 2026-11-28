import CryptoKit
import Foundation

/// 解析自 `froge2ee://` 配对码的端到端加密连接参数。
/// 格式：`froge2ee://<peer_id>:<psk_b64url_or_hex>@<host_id>?relay=<wss_url>&pub=<host_x25519_pub_b64url>[&lan=<ip:port>][&rt=<relay_token>]#<server_name>`
struct E2EEConfig: Sendable, Equatable {
    let peerID: String      // 16 hex chars (8 bytes), lowercased
    let psk: Data           // 32 bytes
    let hostID: String      // lowercased host ID
    let serverPub: Data     // 32 bytes X25519 public key
    let relayURL: String    // optional WSS blind relay URL
    let relayToken: String  // optional relay access token
    let lanAddr: String     // optional LAN direct ip:port
    let serverName: String  // optional friendly name

    static func parse(_ raw: String) throws -> E2EEConfig {
        var s = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let range = s.range(of: "froge2ee://", options: [.caseInsensitive]) else {
            throw APIError.invalid("未识别到 froge2ee:// 连接码")
        }
        s = String(s[range.lowerBound...].prefix { !$0.isWhitespace })
        guard let comps = URLComponents(string: s) else {
            throw APIError.invalid("无效的 E2EE 连接码格式")
        }
        guard let rawUser = comps.user?.trimmingCharacters(in: .whitespacesAndNewlines),
              let rawPass = comps.password?.trimmingCharacters(in: .whitespacesAndNewlines),
              !rawUser.isEmpty, !rawPass.isEmpty else {
            throw APIError.invalid("E2EE 连接码缺少设备密钥")
        }
        let peerID = rawUser.lowercased()
        guard let peerBytes = Data(hexString: peerID), peerBytes.count == 8 else {
            throw APIError.invalid("E2EE PeerID 无效")
        }
        let pskData: Data
        if let hexData = Data(hexString: rawPass), hexData.count == 32 {
            pskData = hexData
        } else if let b64Data = Data.decodeBase64Flexible(rawPass), b64Data.count == 32 {
            pskData = b64Data
        } else {
            throw APIError.invalid("E2EE PSK 密钥无效")
        }
        let hostID = (comps.host ?? "").trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        guard hostID.count >= 8 else {
            throw APIError.invalid("E2EE HostID 无效")
        }
        let query = Dictionary(uniqueKeysWithValues: (comps.queryItems ?? []).map { ($0.name, $0.value ?? "") })
        guard let pubStr = query["pub"]?.trimmingCharacters(in: .whitespacesAndNewlines),
              let pubData = Data.decodeBase64Flexible(pubStr), pubData.count == 32 else {
            throw APIError.invalid("E2EE 主机公钥无效")
        }
        let relayURL = (query["relay"] ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        let relayToken = (query["rt"] ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        let lanAddr = (query["lan"] ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        guard !relayURL.isEmpty || !lanAddr.isEmpty else {
            throw APIError.invalid("E2EE 连接码缺少中继或局域网地址")
        }
        let name = (comps.fragment ?? "").removingPercentEncoding?.trimmingCharacters(in: .whitespacesAndNewlines)
            ?? (comps.fragment ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        return E2EEConfig(
            peerID: peerID,
            psk: pskData,
            hostID: hostID,
            serverPub: pubData,
            relayURL: relayURL,
            relayToken: relayToken,
            lanAddr: lanAddr,
            serverName: name
        )
    }

    func buildRelayWSURL() -> URL? {
        var raw = relayURL.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !raw.isEmpty else { return nil }
        if raw.hasPrefix("https://") {
            raw = "wss://" + raw.dropFirst("https://".count)
        } else if raw.hasPrefix("http://") {
            raw = "ws://" + raw.dropFirst("http://".count)
        } else if !raw.contains("://") {
            raw = "wss://" + raw
        }
        guard var comps = URLComponents(string: raw) else { return nil }
        if comps.path.isEmpty || comps.path == "/" {
            comps.path = "/ws"
        }
        var items = comps.queryItems ?? []
        items.removeAll { ["role", "room", "peer", "token"].contains($0.name) }
        items.append(URLQueryItem(name: "role", value: "client"))
        items.append(URLQueryItem(name: "room", value: hostID))
        items.append(URLQueryItem(name: "peer", value: peerID))
        if !relayToken.isEmpty {
            items.append(URLQueryItem(name: "token", value: relayToken))
        }
        comps.queryItems = items
        return comps.url
    }

    func buildLANWSURL() -> URL? {
        let addr = lanAddr.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !addr.isEmpty else { return nil }
        return URL(string: "ws://\(addr)/ws")
    }
}

private struct E2EEEnvelope: Codable {
    var to: String?
    var from: String?
    var ep: String?
    var n: String?
    var c: String?
    var err: String?
}

private struct E2EEInnerFrame: Codable {
    var t: String
    var s: String?
    var m: String?
    var p: String?
    var h: [String: String]?
    var st: Int?
    var b: String?
    var eof: Bool?
    var err: String?
}

/// 零依赖纯 Apple CryptoKit + URLSessionWebSocketTask 的端到端加密隧道客户端（macOS）：
/// 优先 600ms 同局域网直连，超时自动回退至标准 443 端口 HTTPS/WSS 盲中继。
actor E2EETunnel {
    let config: E2EEConfig
    private let deviceName: String
    private let wsSession: URLSession

    private final class ActiveConn: @unchecked Sendable {
        let id = UUID()
        let ws: URLSessionWebSocketTask
        let sessionKey: SymmetricKey
        let ephPubB64: String
        let isLAN: Bool
        var readTask: Task<Void, Never>?
        var pingTask: Task<Void, Never>?

        init(ws: URLSessionWebSocketTask, sessionKey: SymmetricKey, ephPubB64: String, isLAN: Bool) {
            self.ws = ws
            self.sessionKey = sessionKey
            self.ephPubB64 = ephPubB64
            self.isLAN = isLAN
        }
    }

    private var conn: ActiveConn?
    private var connectingTask: Task<ActiveConn, Error>?
    private var streams: [String: AsyncThrowingStream<E2EEInnerFrame, Error>.Continuation] = [:]
    private var closed = false

    init(config: E2EEConfig, deviceName: String) {
        self.config = config
        self.deviceName = deviceName
        let cfg = URLSessionConfiguration.default
        cfg.httpCookieStorage = nil
        cfg.httpShouldSetCookies = false
        cfg.urlCache = nil
        cfg.requestCachePolicy = .reloadIgnoringLocalCacheData
        cfg.timeoutIntervalForRequest = 30
        cfg.waitsForConnectivity = false
        self.wsSession = URLSession(configuration: cfg)
    }

    static func defaultDeviceName() -> String {
        Host.current().localizedName ?? "Mac"
    }

    var routeDescription: String {
        if let c = conn { return c.isLAN ? "局域网直连" : "WSS 盲中继" }
        return config.relayURL.isEmpty ? "局域网" : "E2EE·WSS"
    }

    func close() {
        closed = true
        connectingTask?.cancel()
        connectingTask = nil
        if let c = conn {
            conn = nil
            c.readTask?.cancel()
            c.pingTask?.cancel()
            c.ws.cancel(with: .normalClosure, reason: nil)
        }
        let pending = streams
        streams.removeAll()
        for (_, cont) in pending {
            cont.finish(throwing: CancellationError())
        }
        wsSession.invalidateAndCancel()
    }

    // MARK: - HTTP Request over E2EE

    func request(
        method: String,
        path: String,
        headers: [String: String],
        body: Data?,
        timeout: TimeInterval = 30
    ) async throws -> (status: Int, headers: [String: String], body: Data) {
        if closed { throw CancellationError() }
        do {
            return try await requestOnce(method: method, path: path, headers: headers, body: body, timeout: timeout)
        } catch let e as APIError {
            if case .network(let msg) = e, !msg.contains("主机不在线") {
                resetCurrentConnection()
                return try await requestOnce(method: method, path: path, headers: headers, body: body, timeout: timeout)
            }
            throw e
        }
    }

    private func requestOnce(
        method: String,
        path: String,
        headers: [String: String],
        body: Data?,
        timeout: TimeInterval
    ) async throws -> (status: Int, headers: [String: String], body: Data) {
        let active = try await ensureConnected()
        let sid = Self.randomStreamID()
        let (frameStream, continuation) = AsyncThrowingStream<E2EEInnerFrame, Error>.makeStream()
        streams[sid] = continuation

        var merged = headers
        if merged["X-Frog-Device-Name"] == nil && !deviceName.isEmpty {
            merged["X-Frog-Device-Name"] = deviceName
        }
        let reqFrame = E2EEInnerFrame(
            t: "req",
            s: sid,
            m: method,
            p: path,
            h: merged,
            st: nil,
            b: body?.isEmpty == false ? body?.base64EncodedString() : nil,
            eof: nil,
            err: nil
        )
        do {
            try await sendEncrypted(reqFrame, over: active)
        } catch {
            streams.removeValue(forKey: sid)
            continuation.finish()
            handleDisconnect(connID: active.id, error: APIError.network("加密隧道发送失败：\(error.localizedDescription)"))
            throw APIError.network("加密隧道发送失败")
        }

        let timeoutNs = UInt64(max(timeout, 5) * 1_000_000_000)
        let deadlineTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: timeoutNs)
            guard !Task.isCancelled else { return }
            await self?.timeoutStream(sid: sid, conn: active)
        }
        defer {
            deadlineTask.cancel()
            streams.removeValue(forKey: sid)
        }

        var status = 200
        var respHeaders: [String: String] = [:]
        var out = Data()
        do {
            for try await fr in frameStream {
                if let errMsg = fr.err, !errMsg.isEmpty {
                    throw APIError.server(status: fr.st ?? 502, message: errMsg)
                }
                if let st = fr.st, st > 0 { status = st }
                if let h = fr.h { respHeaders.merge(h) { _, new in new } }
                if let b64 = fr.b, !b64.isEmpty, let chunk = Data(base64Encoded: b64) {
                    out.append(chunk)
                }
                if fr.eof == true || fr.t == "end" {
                    return (status, respHeaders, out)
                }
            }
            return (status, respHeaders, out)
        } catch is CancellationError {
            Task { [weak self] in
                await self?.sendCancel(sid: sid, over: active)
            }
            throw CancellationError()
        }
    }

    // MARK: - SSE Stream over E2EE

    func streamSSE(
        path: String,
        headers: [String: String]
    ) async throws -> (status: Int, lines: AsyncThrowingStream<String, Error>) {
        if closed { throw CancellationError() }
        let active = try await ensureConnected()
        let sid = Self.randomStreamID()
        let (frameStream, continuation) = AsyncThrowingStream<E2EEInnerFrame, Error>.makeStream()
        streams[sid] = continuation

        var merged = headers
        merged["Accept"] = "text/event-stream"
        if merged["X-Frog-Device-Name"] == nil && !deviceName.isEmpty {
            merged["X-Frog-Device-Name"] = deviceName
        }
        let reqFrame = E2EEInnerFrame(
            t: "req",
            s: sid,
            m: "GET",
            p: path,
            h: merged,
            st: nil,
            b: nil,
            eof: nil,
            err: nil
        )
        do {
            try await sendEncrypted(reqFrame, over: active)
        } catch {
            streams.removeValue(forKey: sid)
            continuation.finish()
            handleDisconnect(connID: active.id, error: APIError.network("加密隧道断开"))
            throw APIError.network("加密隧道发送失败")
        }

        let firstFrameDeadline = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 35_000_000_000)
            guard !Task.isCancelled else { return }
            await self?.timeoutStream(sid: sid, conn: active)
        }
        var iterator = frameStream.makeAsyncIterator()
        let first: E2EEInnerFrame
        do {
            guard let got = try await iterator.next() else {
                firstFrameDeadline.cancel()
                streams.removeValue(forKey: sid)
                throw APIError.network("事件流已关闭")
            }
            first = got
            firstFrameDeadline.cancel()
        } catch {
            firstFrameDeadline.cancel()
            streams.removeValue(forKey: sid)
            resetCurrentConnection()
            throw error
        }
        if let errMsg = first.err, !errMsg.isEmpty {
            streams.removeValue(forKey: sid)
            throw APIError.server(status: first.st ?? 502, message: errMsg)
        }
        let status = first.st ?? 200
        let initialData = (first.b.flatMap { Data(base64Encoded: $0) }) ?? Data()
        let firstEnded = (first.eof == true || first.t == "end")

        let (lineStream, lineCont) = AsyncThrowingStream<String, Error>.makeStream()
        let pumpTask = Task { [weak self] in
            var buf = Data()
            func emitLines() {
                while let nl = buf.firstIndex(of: 0x0A) {
                    var slice = buf[buf.startIndex..<nl]
                    if slice.last == 0x0D { slice = slice.dropLast() }
                    let line = String(decoding: slice, as: UTF8.self)
                    lineCont.yield(line)
                    buf.removeSubrange(buf.startIndex...nl)
                }
            }
            if !initialData.isEmpty {
                buf.append(initialData)
                emitLines()
            }
            if firstEnded {
                if !buf.isEmpty {
                    lineCont.yield(String(decoding: buf, as: UTF8.self))
                }
                await self?.removeStream(sid: sid)
                lineCont.finish()
                return
            }
            do {
                while let fr = try await iterator.next() {
                    if let b64 = fr.b, !b64.isEmpty, let chunk = Data(base64Encoded: b64) {
                        buf.append(chunk)
                        emitLines()
                    }
                    if fr.eof == true || fr.t == "end" {
                        break
                    }
                }
                if !buf.isEmpty {
                    lineCont.yield(String(decoding: buf, as: UTF8.self))
                }
                await self?.removeStream(sid: sid)
                lineCont.finish()
            } catch {
                await self?.removeStream(sid: sid)
                lineCont.finish(throwing: error)
            }
        }

        lineCont.onTermination = { [weak self] _ in
            pumpTask.cancel()
            Task {
                await self?.cancelRemoteStream(sid: sid, over: active)
            }
        }

        return (status, lineStream)
    }

    // MARK: - Connection & LAN-First Auto-Routing

    private func ensureConnected() async throws -> ActiveConn {
        if closed { throw CancellationError() }
        if let c = conn { return c }
        if let t = connectingTask { return try await t.value }

        let task = Task<ActiveConn, Error> { [self] in
            try await self.dialBestRoute()
        }
        connectingTask = task
        do {
            let active = try await task.value
            connectingTask = nil
            if closed {
                active.ws.cancel(with: .normalClosure, reason: nil)
                throw CancellationError()
            }
            self.conn = active
            active.readTask = Task { [weak self] in
                await self?.readLoop(for: active)
            }
            active.pingTask = Task { [weak self] in
                await self?.pingLoop(for: active)
            }
            return active
        } catch {
            connectingTask = nil
            throw error
        }
    }

    private func dialBestRoute() async throws -> ActiveConn {
        var lastError: Error? = nil
        if let lanURL = config.buildLANWSURL() {
            do {
                return try await dialAndProbe(url: lanURL, timeoutNs: 600_000_000, isLAN: true)
            } catch {
                lastError = error
            }
        }
        if let relayURL = config.buildRelayWSURL() {
            do {
                return try await dialAndProbe(url: relayURL, timeoutNs: 10_000_000_000, isLAN: false)
            } catch {
                throw APIError.network("无法连接 E2EE 盲中继：\(error.localizedDescription)")
            }
        }
        if let err = lastError {
            throw APIError.network("局域网直连失败：\(err.localizedDescription)")
        }
        throw APIError.invalid("E2EE 连接码缺少可用的局域网或中继地址")
    }

    private func dialAndProbe(url: URL, timeoutNs: UInt64, isLAN: Bool) async throws -> ActiveConn {
        let (sessionKey, ephPubB64) = try deriveSessionKey()
        let ws = wsSession.webSocketTask(with: url)
        ws.maximumMessageSize = 16 * 1024 * 1024
        ws.resume()

        do {
            try await withThrowingTaskGroup(of: Void.self) { group in
                group.addTask {
                    try await ws.send(.string("ping"))
                    while true {
                        let msg = try await ws.receive()
                        let text: String?
                        switch msg {
                        case .string(let s): text = s
                        case .data(let d): text = String(data: d, encoding: .utf8)
                        @unknown default: text = nil
                        }
                        if text?.trimmingCharacters(in: .whitespacesAndNewlines) == "pong" {
                            return
                        }
                    }
                }
                group.addTask {
                    try await Task.sleep(nanoseconds: timeoutNs)
                    throw URLError(.timedOut)
                }
                _ = try await group.next()
                group.cancelAll()
            }
            return ActiveConn(ws: ws, sessionKey: sessionKey, ephPubB64: ephPubB64, isLAN: isLAN)
        } catch {
            ws.cancel(with: .goingAway, reason: nil)
            throw error
        }
    }

    private func readLoop(for active: ActiveConn) async {
        while !Task.isCancelled {
            do {
                let msg = try await active.ws.receive()
                let rawText: String
                switch msg {
                case .string(let s): rawText = s
                case .data(let d):
                    guard let s = String(data: d, encoding: .utf8) else { continue }
                    rawText = s
                @unknown default: continue
                }
                let trimmed = rawText.trimmingCharacters(in: .whitespacesAndNewlines)
                if trimmed == "pong" { continue }
                if trimmed == "ping" {
                    try? await active.ws.send(.string("pong"))
                    continue
                }
                guard let rawData = rawText.data(using: .utf8),
                      let env = try? JSONDecoder().decode(E2EEEnvelope.self, from: rawData) else {
                    continue
                }
                if let relayErr = env.err, !relayErr.isEmpty {
                    let msg = relayErr == "host_offline"
                        ? "主机不在线，请确认电脑上的 Frog Bot 正在运行"
                        : "中继错误：\(relayErr)"
                    failAllStreams(APIError.network(msg))
                    continue
                }
                guard let plain = try? openEnvelope(env, key: active.sessionKey),
                      let frame = try? JSONDecoder().decode(E2EEInnerFrame.self, from: plain),
                      let sid = frame.s, !sid.isEmpty else {
                    continue
                }
                if let cont = streams[sid] {
                    cont.yield(frame)
                    if frame.eof == true || frame.t == "end" {
                        streams.removeValue(forKey: sid)
                        cont.finish()
                    }
                }
            } catch {
                handleDisconnect(connID: active.id, error: APIError.network("加密隧道已断开"))
                return
            }
        }
    }

    private func pingLoop(for active: ActiveConn) async {
        while !Task.isCancelled {
            try? await Task.sleep(nanoseconds: 25_000_000_000)
            if Task.isCancelled { return }
            do {
                try await active.ws.send(.string("ping"))
            } catch {
                handleDisconnect(connID: active.id, error: APIError.network("心跳超时，正在重连"))
                return
            }
        }
    }

    private func resetCurrentConnection() {
        if let c = conn {
            handleDisconnect(connID: c.id, error: APIError.network("重连加密隧道"))
        }
    }

    private func handleDisconnect(connID: UUID, error: Error) {
        guard conn?.id == connID else { return }
        if let c = conn {
            conn = nil
            c.readTask?.cancel()
            c.pingTask?.cancel()
            c.ws.cancel(with: .goingAway, reason: nil)
        }
        failAllStreams(error)
    }

    private func failAllStreams(_ error: Error) {
        let pending = streams
        streams.removeAll()
        for (_, cont) in pending {
            cont.finish(throwing: error)
        }
    }

    private func removeStream(sid: String) {
        streams.removeValue(forKey: sid)
    }

    private func timeoutStream(sid: String, conn active: ActiveConn) {
        if let cont = streams.removeValue(forKey: sid) {
            cont.finish(throwing: APIError.network("请求超时"))
            Task { await self.sendCancel(sid: sid, over: active) }
        }
    }

    private func cancelRemoteStream(sid: String, over active: ActiveConn) async {
        streams.removeValue(forKey: sid)
        await sendCancel(sid: sid, over: active)
    }

    private func sendCancel(sid: String, over active: ActiveConn) async {
        let fr = E2EEInnerFrame(t: "cancel", s: sid, m: nil, p: nil, h: nil, st: nil, b: nil, eof: nil, err: nil)
        try? await sendEncrypted(fr, over: active)
    }

    // MARK: - Crypto (Matches Go `internal/remote/e2ee_crypto.go` byte-for-byte)

    private func deriveSessionKey() throws -> (SymmetricKey, String) {
        let ephPriv = Curve25519.KeyAgreement.PrivateKey()
        let hostPub = try Curve25519.KeyAgreement.PublicKey(rawRepresentation: config.serverPub)
        let sharedSecret = try ephPriv.sharedSecretFromKeyAgreement(with: hostPub)
        let sharedData = sharedSecret.withUnsafeBytes { Data($0) }

        var ikm = Data(capacity: 64)
        ikm.append(sharedData)
        ikm.append(config.psk)

        let salt = Data("frog-e2ee-v1".utf8)
        let info = Data("\(config.hostID)|\(config.peerID)".utf8)
        let key = HKDF<SHA256>.deriveKey(
            inputKeyMaterial: SymmetricKey(data: ikm),
            salt: salt,
            info: info,
            outputByteCount: 32
        )
        let ephPubB64 = ephPriv.publicKey.rawRepresentation.base64RawURLEncodedString()
        return (key, ephPubB64)
    }

    private func sendEncrypted(_ frame: E2EEInnerFrame, over active: ActiveConn) async throws {
        let plaintext = try JSONEncoder().encode(frame)
        let from = "client:\(config.peerID)"
        let to = "host"
        let aad = Data("\(from)|\(to)|\(active.ephPubB64)".utf8)
        let nonce = AES.GCM.Nonce()
        let sealed = try AES.GCM.seal(plaintext, using: active.sessionKey, nonce: nonce, authenticating: aad)

        var ctAndTag = Data(capacity: sealed.ciphertext.count + sealed.tag.count)
        ctAndTag.append(sealed.ciphertext)
        ctAndTag.append(sealed.tag)

        let env = E2EEEnvelope(
            to: to,
            from: from,
            ep: active.ephPubB64,
            n: Data(nonce).base64RawURLEncodedString(),
            c: ctAndTag.base64RawURLEncodedString(),
            err: nil
        )
        let wire = try JSONEncoder().encode(env)
        guard let wireStr = String(data: wire, encoding: .utf8) else {
            throw APIError.invalid("编码密文信封失败")
        }
        try await active.ws.send(.string(wireStr))
    }

    private func openEnvelope(_ env: E2EEEnvelope, key: SymmetricKey) throws -> Data {
        guard let from = env.from, let to = env.to, let ep = env.ep,
              let nStr = env.n, let cStr = env.c,
              let nonceData = Data.decodeBase64Flexible(nStr), nonceData.count == 12,
              let ctAndTag = Data.decodeBase64Flexible(cStr), ctAndTag.count >= 16 else {
            throw APIError.invalid("无效的 E2EE 密文信封")
        }
        let nonce = try AES.GCM.Nonce(data: nonceData)
        let ct = ctAndTag.prefix(ctAndTag.count - 16)
        let tag = ctAndTag.suffix(16)
        let box = try AES.GCM.SealedBox(nonce: nonce, ciphertext: ct, tag: tag)
        let aad = Data("\(from)|\(to)|\(ep)".utf8)
        return try AES.GCM.open(box, using: key, authenticating: aad)
    }

    private static func randomStreamID() -> String {
        String(UUID().uuidString.replacingOccurrences(of: "-", with: "").lowercased().prefix(12))
    }
}

private extension Data {
    init?(hexString: String) {
        let s = hexString.trimmingCharacters(in: .whitespacesAndNewlines)
        guard s.count % 2 == 0, !s.isEmpty else { return nil }
        var out = Data(capacity: s.count / 2)
        var idx = s.startIndex
        while idx < s.endIndex {
            let next = s.index(idx, offsetBy: 2)
            guard let byte = UInt8(s[idx..<next], radix: 16) else { return nil }
            out.append(byte)
            idx = next
        }
        self = out
    }

    static func decodeBase64Flexible(_ raw: String) -> Data? {
        var s = raw.trimmingCharacters(in: .whitespacesAndNewlines)
            .replacingOccurrences(of: "-", with: "+")
            .replacingOccurrences(of: "_", with: "/")
        let rem = s.count % 4
        if rem != 0 {
            s += String(repeating: "=", count: 4 - rem)
        }
        return Data(base64Encoded: s)
    }

    func base64RawURLEncodedString() -> String {
        base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }
}
