import Foundation
import UniformTypeIdentifiers

/// 一台 frog 主机的连接配置（支持内置 sing-box VLESS-Reality / P2P 隧道或常规 HTTP/HTTPS）。
struct ServerConfig: Codable, Hashable, Identifiable {
    var id: UUID
    var name: String
    var url: String
    var username: String
    var tunnelURI: String? = nil

    var isTunneled: Bool {
        !(tunnelURI ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    var isE2EE: Bool {
        (tunnelURI ?? "").trimmingCharacters(in: .whitespacesAndNewlines).lowercased().hasPrefix("froge2ee://")
    }

    var tunnelBadge: String? {
        guard let raw = tunnelURI?.lowercased() else { return nil }
        if raw.hasPrefix("froge2ee://") { return raw.contains("relay=") ? "E2EE·WSS" : "E2EE·局域网" }
        if raw.hasPrefix("vless://") { return "VLESS-Reality" }
        if raw.hasPrefix("frogp2p://") { return raw.contains("r=") ? "P2P·中继" : "P2P" }
        return "加密隧道"
    }

    var displayEndpoint: String {
        if let raw = tunnelURI, let u = URL(string: raw) {
            if let comps = URLComponents(url: u, resolvingAgainstBaseURL: false) {
                if raw.lowercased().hasPrefix("froge2ee://") {
                    if let relay = comps.queryItems?.first(where: { $0.name == "relay" })?.value,
                       let ru = URL(string: relay), let h = ru.host, !h.isEmpty {
                        return h
                    }
                    if let lan = comps.queryItems?.first(where: { $0.name == "lan" })?.value, !lan.isEmpty {
                        return lan
                    }
                }
                if let r = comps.queryItems?.first(where: { $0.name == "r" })?.value, !r.isEmpty {
                    return r
                }
            }
            if let h = u.host, !h.isEmpty {
                if let p = u.port { return "\(h):\(p)" }
                return h
            }
        }
        return baseURL?.host ?? url
    }

    var baseURL: URL? { URL(string: url) }

    /// 走内置加密隧道时全程密文直达内网，永不触发明文警告。
    var isInsecure: Bool {
        if isTunneled { return false }
        guard let u = baseURL else { return false }
        return u.scheme?.lowercased() == "http" && !ServerConfig.isLoopback(u.host ?? "")
    }

    static func extractTunnelURI(_ raw: String) -> (uri: String, serverName: String?)? {
        let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        for prefix in ["froge2ee://", "vless://", "frogp2p://"] {
            if let r = trimmed.range(of: prefix, options: [.caseInsensitive]) {
                let tail = trimmed[r.lowerBound...]
                let candidate = String(tail.prefix { !$0.isWhitespace })
                var srvName: String? = nil
                if let hashIdx = candidate.firstIndex(of: "#") {
                    let frag = String(candidate[candidate.index(after: hashIdx)...])
                    let decoded = frag.removingPercentEncoding ?? frag
                    if !decoded.trimmingCharacters(in: .whitespaces).isEmpty {
                        srvName = decoded.trimmingCharacters(in: .whitespaces)
                    }
                }
                return (candidate, srvName)
            }
        }
        return nil
    }

    static func isLoopback(_ host: String) -> Bool {
        let h = host.lowercased()
        return h == "localhost" || h.hasSuffix(".localhost") || h.hasPrefix("127.") || h == "::1" || h == "[::1]" || h == "frog.internal"
    }

    /// 把用户输入的地址整理成完整 URL：没写协议时，IP、带端口或本机地址默认 http，域名默认 https。
    static func normalize(_ raw: String) -> URL? {
        var s = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        while s.hasSuffix("/") { s.removeLast() }
        guard !s.isEmpty else { return nil }
        if !s.contains("://") {
            let hostPort = s.split(separator: "/").first.map(String.init) ?? s
            let host = hostPort.split(separator: ":").first.map(String.init) ?? hostPort
            let isIP = !host.isEmpty && host.allSatisfy { $0.isNumber || $0 == "." }
            let plain = hostPort.contains(":") || isIP || isLoopback(host)
            s = (plain ? "http://" : "https://") + s
        }
        guard let u = URL(string: s), let scheme = u.scheme?.lowercased(),
              scheme == "http" || scheme == "https", let host = u.host, !host.isEmpty else { return nil }
        return u
    }
}

enum APIError: LocalizedError, Equatable {
    case unauthorized
    case server(status: Int, message: String)
    case network(String)
    case invalid(String)

    var errorDescription: String? {
        switch self {
        case .unauthorized: return "授权已失效，请重新验证"
        case .server(_, let m): return m
        case .network(let m): return m
        case .invalid(let m): return m
        }
    }

    var status: Int? { if case .server(let s, _) = self { return s }; return nil }
}

enum SessionVault {
    private static var dir: URL {
        FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("FrogBotClaude", isDirectory: true)
    }
    private static var file: URL { dir.appendingPathComponent("sessions.json") }

    static func load() -> [String: String] {
        guard let data = try? Data(contentsOf: file),
              let map = try? JSONDecoder().decode([String: String].self, from: data) else { return [:] }
        return map
    }

    static func set(_ value: String?, for id: UUID) {
        var map = load()
        map[id.uuidString] = value
        let fm = FileManager.default
        try? fm.createDirectory(at: dir, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        guard let data = try? JSONEncoder().encode(map) else { return }
        try? data.write(to: file, options: [.atomic])
        try? fm.setAttributes([.posixPermissions: 0o600], ofItemAtPath: file.path)
    }

    static func get(_ id: UUID) -> String? { load()[id.uuidString] }
}

extension Error {
    /// 判断是否为 Swift Concurrency 或 URLSession 生命周期取消（非业务错误，不应提示给用户）。
    var isCancellation: Bool {
        if self is CancellationError { return true }
        if let u = self as? URLError, u.code == .cancelled { return true }
        let ns = self as NSError
        return ns.domain == NSURLErrorDomain && ns.code == NSURLErrorCancelled
    }
}

@MainActor
final class ServerClient {
    static let cookieName = "frogbot_session"

    private(set) var config: ServerConfig
    private(set) var cookie: String?
    private let tunnelBootError: String?
    let e2eeTunnel: E2EETunnel?
    let session: URLSession
    let streamSession: URLSession

    init(config: ServerConfig) {
        self.config = config
        self.cookie = SessionVault.get(config.id)

        let c = URLSessionConfiguration.default
        c.httpCookieStorage = nil
        c.httpShouldSetCookies = false
        c.httpCookieAcceptPolicy = .never
        c.requestCachePolicy = .reloadIgnoringLocalCacheData
        c.urlCache = nil
        c.timeoutIntervalForRequest = 30
        c.waitsForConnectivity = false

        var bootErr: String? = nil
        var e2ee: E2EETunnel? = nil
        if let uri = config.tunnelURI?.trimmingCharacters(in: .whitespacesAndNewlines), !uri.isEmpty {
            if uri.lowercased().hasPrefix("froge2ee://") {
                do {
                    let parsed = try E2EEConfig.parse(uri)
                    e2ee = E2EETunnel(config: parsed, deviceName: E2EETunnel.defaultDeviceName())
                } catch {
                    bootErr = error.localizedDescription
                }
            } else {
                do {
                    let tun = try FrogBoxManager.shared.acquire(uri: uri)
                    c.connectionProxyDictionary = [
                        "HTTPEnable": 1,
                        "HTTPProxy": "127.0.0.1",
                        "HTTPPort": tun.port,
                        "HTTPSEnable": 1,
                        "HTTPSProxy": "127.0.0.1",
                        "HTTPSPort": tun.port,
                    ]
                } catch {
                    bootErr = error.localizedDescription
                }
            }
        }
        self.tunnelBootError = bootErr
        self.e2eeTunnel = e2ee

        session = URLSession(configuration: c)
        let sc = c.copy() as! URLSessionConfiguration
        sc.httpMaximumConnectionsPerHost = 64
        streamSession = URLSession(configuration: sc)
    }

    func close() {
        if let e2ee = e2eeTunnel {
            Task { await e2ee.close() }
        }
    }

    var hasSession: Bool { config.isTunneled || cookie != nil }

    func update(config: ServerConfig) { self.config = config }

    func forgetSession() {
        cookie = nil
        SessionVault.set(nil, for: config.id)
    }

    // MARK: 请求

    func url(_ path: String, query: [URLQueryItem] = []) -> URL {
        let base = config.baseURL ?? URL(string: "http://frog.internal")!
        var comps = URLComponents(url: base, resolvingAgainstBaseURL: false)!
        var prefix = comps.path
        while prefix.hasSuffix("/") { prefix.removeLast() }
        comps.path = prefix + path
        comps.queryItems = query.isEmpty ? nil : query
        return comps.url ?? base
    }

    func makeRequest(_ method: String, _ path: String, query: [URLQueryItem] = []) -> URLRequest {
        var r = URLRequest(url: url(path, query: query))
        r.httpMethod = method
        r.setValue("application/json", forHTTPHeaderField: "Accept")
        if let cookie { r.setValue("\(Self.cookieName)=\(cookie)", forHTTPHeaderField: "Cookie") }
        return r
    }

    private static func relativePathAndQuery(from url: URL?) -> String {
        guard let url, let comps = URLComponents(url: url, resolvingAgainstBaseURL: false) else { return "/" }
        let path = comps.percentEncodedPath.isEmpty ? "/" : comps.percentEncodedPath
        if let q = comps.percentEncodedQuery, !q.isEmpty {
            return "\(path)?\(q)"
        }
        return path
    }

    func send(_ req: URLRequest) async throws -> (Data, HTTPURLResponse) {
        if let tunnelBootError {
            throw APIError.invalid(tunnelBootError)
        }
        if let e2eeTunnel {
            let path = Self.relativePathAndQuery(from: req.url)
            let method = req.httpMethod ?? "GET"
            let headers = req.allHTTPHeaderFields ?? [:]
            let timeout = req.timeoutInterval > 0 ? req.timeoutInterval : 30
            let (status, respHeaders, data) = try await e2eeTunnel.request(
                method: method,
                path: path,
                headers: headers,
                body: req.httpBody,
                timeout: timeout
            )
            let targetURL = req.url ?? URL(string: "http://frog.internal")!
            let http = HTTPURLResponse(url: targetURL, statusCode: status, httpVersion: "HTTP/1.1", headerFields: respHeaders)
                ?? HTTPURLResponse(url: targetURL, statusCode: status, httpVersion: nil, headerFields: nil)!
            if http.statusCode == 401 { throw APIError.unauthorized }
            guard (200..<300).contains(http.statusCode) else {
                throw APIError.server(status: http.statusCode, message: Self.message(from: data, status: http.statusCode))
            }
            return (data, http)
        }
        let data: Data, resp: URLResponse
        do {
            (data, resp) = try await session.data(for: req)
        } catch let e as URLError {
            if e.code == .cancelled { throw CancellationError() }
            throw APIError.network(Self.describe(e))
        }
        guard let http = resp as? HTTPURLResponse else { throw APIError.network("服务器没有响应") }
        if http.statusCode == 401 { throw APIError.unauthorized }
        guard (200..<300).contains(http.statusCode) else {
            throw APIError.server(status: http.statusCode, message: Self.message(from: data, status: http.statusCode))
        }
        return (data, http)
    }

    /// 统一 SSE 行流接口：自动适配原生 E2EE WSS 隧道、内置 FrogBox 隧道与常规 HTTP/HTTPS。
    func streamLines(for req: URLRequest) async throws -> (Int, AsyncThrowingStream<String, Error>) {
        if let tunnelBootError {
            throw APIError.invalid(tunnelBootError)
        }
        if let e2eeTunnel {
            let path = Self.relativePathAndQuery(from: req.url)
            let headers = req.allHTTPHeaderFields ?? [:]
            return try await e2eeTunnel.streamSSE(path: path, headers: headers)
        }
        let (bytes, resp) = try await streamSession.bytes(for: req)
        guard let http = resp as? HTTPURLResponse else { throw APIError.network("服务器没有响应") }
        let stream = AsyncThrowingStream<String, Error> { continuation in
            let task = Task {
                do {
                    for try await line in bytes.lines {
                        continuation.yield(line)
                    }
                    continuation.finish()
                } catch {
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
        return (http.statusCode, stream)
    }

    func get<T: Decodable>(_ path: String, query: [URLQueryItem] = [], as type: T.Type = T.self) async throws -> T {
        let (data, _) = try await send(makeRequest("GET", path, query: query))
        let decoder = JSONDecoder()
        if let decoded = try? decoder.decode(T.self, from: data) {
            return decoded
        }
        // 兼容 Go 后端 nil slice 返回字面量 null 的情况
        if String(data: data, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines) == "null",
           let emptyArr = try? decoder.decode(T.self, from: Data("[]".utf8)) {
            return emptyArr
        }
        throw APIError.invalid("返回数据无法解析")
    }

    @discardableResult
    func post(_ path: String, body: [String: Any] = [:], method: String = "POST", query: [URLQueryItem] = [],
              timeout: TimeInterval? = nil) async throws -> JSON {
        var r = makeRequest(method, path, query: query)
        if let timeout { r.timeoutInterval = timeout }
        r.setValue("application/json", forHTTPHeaderField: "Content-Type")
        r.httpBody = try JSONSerialization.data(withJSONObject: body)
        let (data, _) = try await send(r)
        let out = JSON.parse(data) ?? .null
        if let msg = out["error"].string, !msg.isEmpty { throw APIError.server(status: 400, message: msg) }
        return out
    }

    func send<B: Encodable, T: Decodable>(_ method: String = "POST", _ path: String, body: B, as type: T.Type = T.self) async throws -> T {
        var r = makeRequest(method, path)
        r.setValue("application/json", forHTTPHeaderField: "Content-Type")
        r.httpBody = try JSONEncoder().encode(body)
        let (data, _) = try await send(r)
        if let j = JSON.parse(data), let msg = j["error"].string, !msg.isEmpty { throw APIError.server(status: 400, message: msg) }
        let decoder = JSONDecoder()
        if let decoded = try? decoder.decode(T.self, from: data) {
            return decoded
        }
        if String(data: data, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines) == "null",
           let emptyArr = try? decoder.decode(T.self, from: Data("[]".utf8)) {
            return emptyArr
        }
        throw APIError.invalid("返回数据无法解析")
    }

    func data(_ path: String, query: [URLQueryItem] = []) async throws -> Data {
        var r = makeRequest("GET", path, query: query)
        r.setValue("*/*", forHTTPHeaderField: "Accept")
        return try await send(r).0
    }

    // MARK: 登录

    func me() async throws -> AuthInfo {
        do {
            return try await get("/api/auth/me")
        } catch let e as APIError where e.status == 404 {
            return AuthInfo(enabled: false, authenticated: true, username: nil)
        }
    }

    func login(username: String, password: String) async throws {
        var r = makeRequest("POST", "/api/auth/login")
        r.setValue(nil, forHTTPHeaderField: "Cookie")
        r.setValue("application/json", forHTTPHeaderField: "Content-Type")
        r.httpBody = try JSONSerialization.data(withJSONObject: ["username": username, "password": password])
        let data: Data, resp: URLResponse
        do {
            (data, resp) = try await session.data(for: r)
        } catch let e as URLError {
            throw APIError.network(Self.describe(e))
        }
        guard let http = resp as? HTTPURLResponse else { throw APIError.network("服务器没有响应") }
        guard (200..<300).contains(http.statusCode) else {
            throw APIError.server(status: http.statusCode, message: Self.message(from: data, status: http.statusCode))
        }
        guard let value = Self.sessionCookie(from: http, url: r.url!) else {
            throw APIError.invalid("服务器未返回登录会话")
        }
        cookie = value
        SessionVault.set(value, for: config.id)
    }

    // MARK: 上传

    func upload(_ path: String, file: URL) async throws -> Attachment {
        let access = file.startAccessingSecurityScopedResource()
        defer { if access { file.stopAccessingSecurityScopedResource() } }
        let content: Data
        do { content = try Data(contentsOf: file) } catch { throw APIError.invalid("无法读取「\(file.lastPathComponent)」") }
        return try await upload(path, name: file.lastPathComponent, data: content)
    }

    func upload(_ path: String, name: String, data content: Data) async throws -> Attachment {
        let boundary = "frogbot-\(UUID().uuidString)"
        let mime = UTType(filenameExtension: (name as NSString).pathExtension)?.preferredMIMEType ?? "application/octet-stream"
        var body = Data()
        let safeName = name.replacingOccurrences(of: "\"", with: "_")
        body.append("--\(boundary)\r\nContent-Disposition: form-data; name=\"file\"; filename=\"\(safeName)\"\r\nContent-Type: \(mime)\r\n\r\n".data(using: .utf8)!)
        body.append(content)
        body.append("\r\n--\(boundary)--\r\n".data(using: .utf8)!)
        var r = makeRequest("POST", path)
        r.setValue("multipart/form-data; boundary=\(boundary)", forHTTPHeaderField: "Content-Type")
        r.httpBody = body
        r.timeoutInterval = 300
        let (data, _) = try await send(r)
        let out = JSON.parse(data) ?? .null
        if let msg = out["error"].string { throw APIError.server(status: 400, message: msg) }
        guard let first = out.array.first else { throw APIError.invalid("上传失败") }
        return Attachment(first)
    }

    // MARK: 工具

    private static func sessionCookie(from http: HTTPURLResponse, url: URL) -> String? {
        var headers: [String: String] = [:]
        for (k, v) in http.allHeaderFields {
            if let k = k as? String, let v = v as? String { headers[k] = v }
        }
        if let c = HTTPCookie.cookies(withResponseHeaderFields: headers, for: url).first(where: { $0.name == cookieName }) {
            return c.value
        }
        guard let raw = http.value(forHTTPHeaderField: "Set-Cookie"),
              let range = raw.range(of: "\(cookieName)=") else { return nil }
        let rest = raw[range.upperBound...]
        let value = rest.prefix { $0 != ";" && $0 != "," }
        return value.isEmpty ? nil : String(value)
    }

    static func message(from data: Data, status: Int) -> String {
        if let j = JSON.parse(data), let e = j["error"].string, !e.isEmpty { return e }
        if let s = String(data: data, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines), !s.isEmpty, !s.hasPrefix("<") {
            return String(s.prefix(200))
        }
        switch status {
        case 403: return "请求被拒绝（403）"
        case 404: return "接口不存在（404）"
        case 429: return "请求过于频繁"
        case 502, 503, 504: return "隧道或主机不可达（\(status)）"
        default: return "请求失败（\(status)）"
        }
    }

    static func describe(_ e: URLError) -> String {
        switch e.code {
        case .cannotConnectToHost: return "无法连接主机"
        case .cannotFindHost, .dnsLookupFailed: return "找不到主机地址"
        case .timedOut: return "连接超时"
        case .notConnectedToInternet, .networkConnectionLost: return "网络已断开"
        case .secureConnectionFailed, .serverCertificateUntrusted, .serverCertificateHasBadDate,
             .serverCertificateNotYetValid, .serverCertificateHasUnknownRoot:
            return "TLS 握手失败"
        case .appTransportSecurityRequiresSecureConnection: return "系统拦截了明文 HTTP"
        default: return e.localizedDescription
        }
    }
}
