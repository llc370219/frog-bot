import Foundation
import UniformTypeIdentifiers

/// 一台 frog 服务器的连接设置（保存在本机）。不保存密码。
struct ServerConfig: Codable, Hashable, Identifiable {
    var id: UUID
    var name: String
    var url: String
    var username: String

    var baseURL: URL? { URL(string: url) }

    /// 明文 HTTP 且不是本机地址：密码和聊天内容在网络上不加密。
    var isInsecure: Bool {
        guard let u = baseURL else { return false }
        return u.scheme?.lowercased() == "http" && !ServerConfig.isLoopback(u.host ?? "")
    }

    static func isLoopback(_ host: String) -> Bool {
        let h = host.lowercased()
        return h == "localhost" || h.hasSuffix(".localhost") || h.hasPrefix("127.") || h == "::1" || h == "[::1]"
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
        case .unauthorized: return "登录已过期，请重新登录"
        case .server(_, let m): return m
        case .network(let m): return m
        case .invalid(let m): return m
        }
    }

    var status: Int? { if case .server(let s, _) = self { return s }; return nil }
}

/// 会话 Cookie 按服务器分别保存在「应用程序支持/FrogBotClaude/sessions.json」（仅本人可读，权限 0600），
/// 这样同一台机器不同端口的两个 frog 也不会互相覆盖登录。
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

/// 和一台服务器说话：普通请求、登录、上传、下载。
/// 浏览器之外的请求不带 Origin 头，服务器的跨站检查自然放行；登录靠服务器发的会话 Cookie。
@MainActor
final class ServerClient {
    static let cookieName = "frogbot_session"

    private(set) var config: ServerConfig
    private(set) var cookie: String?
    let session: URLSession

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
        session = URLSession(configuration: c)
    }

    var hasSession: Bool { cookie != nil }

    func update(config: ServerConfig) { self.config = config }

    func forgetSession() {
        cookie = nil
        SessionVault.set(nil, for: config.id)
    }

    // MARK: 请求

    func url(_ path: String, query: [URLQueryItem] = []) -> URL {
        let base = config.baseURL ?? URL(string: "http://127.0.0.1")!
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

    /// 发请求，检查状态码；返回响应体。401 抛 `.unauthorized`，其他错误带上服务器给的说明。
    func send(_ req: URLRequest) async throws -> (Data, HTTPURLResponse) {
        let data: Data, resp: URLResponse
        do {
            (data, resp) = try await session.data(for: req)
        } catch let e as URLError {
            throw APIError.network(Self.describe(e))
        }
        guard let http = resp as? HTTPURLResponse else { throw APIError.network("服务器没有正常响应") }
        if http.statusCode == 401 { throw APIError.unauthorized }
        guard (200..<300).contains(http.statusCode) else {
            throw APIError.server(status: http.statusCode, message: Self.message(from: data, status: http.statusCode))
        }
        return (data, http)
    }

    func get<T: Decodable>(_ path: String, query: [URLQueryItem] = [], as type: T.Type = T.self) async throws -> T {
        let (data, _) = try await send(makeRequest("GET", path, query: query))
        do {
            return try JSONDecoder().decode(T.self, from: data)
        } catch {
            throw APIError.invalid("服务器返回的数据看不懂，可能版本太旧或地址不是 frog")
        }
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
        // 部分接口出错时仍返回 200 + {"error": "..."}
        if let msg = out["error"].string, !msg.isEmpty { throw APIError.server(status: 400, message: msg) }
        return out
    }

    /// 发送 Codable 请求体，按类型解析返回值。
    func send<B: Encodable, T: Decodable>(_ method: String = "POST", _ path: String, body: B, as type: T.Type = T.self) async throws -> T {
        var r = makeRequest(method, path)
        r.setValue("application/json", forHTTPHeaderField: "Content-Type")
        r.httpBody = try JSONEncoder().encode(body)
        let (data, _) = try await send(r)
        if let j = JSON.parse(data), let msg = j["error"].string, !msg.isEmpty { throw APIError.server(status: 400, message: msg) }
        do {
            return try JSONDecoder().decode(T.self, from: data)
        } catch {
            throw APIError.invalid("服务器返回的数据看不懂，可能版本太旧")
        }
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
            return AuthInfo(enabled: false, authenticated: true, username: nil) // 很老的版本没有这个接口
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
        guard let http = resp as? HTTPURLResponse else { throw APIError.network("服务器没有正常响应") }
        guard (200..<300).contains(http.statusCode) else {
            throw APIError.server(status: http.statusCode, message: Self.message(from: data, status: http.statusCode))
        }
        guard let value = Self.sessionCookie(from: http, url: r.url!) else {
            throw APIError.invalid("登录成功了，但服务器没有给出会话，请确认地址指向 frog")
        }
        cookie = value
        SessionVault.set(value, for: config.id)
    }

    // MARK: 上传

    func upload(_ path: String, file: URL) async throws -> Attachment {
        let access = file.startAccessingSecurityScopedResource()
        defer { if access { file.stopAccessingSecurityScopedResource() } }
        let content: Data
        do { content = try Data(contentsOf: file) } catch { throw APIError.invalid("读不了文件「\(file.lastPathComponent)」") }
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
        // 兜底：自己解析 Set-Cookie
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
        case 403: return "服务器拒绝了请求（403）"
        case 404: return "找不到这个接口（404），请确认地址指向 frog 并且版本够新"
        case 429: return "尝试太频繁，请稍后再试"
        case 502, 503, 504: return "服务器暂时不可用（\(status)）"
        default: return "请求失败（\(status)）"
        }
    }

    static func describe(_ e: URLError) -> String {
        switch e.code {
        case .cannotConnectToHost: return "连不上服务器：端口没开或服务没启动"
        case .cannotFindHost, .dnsLookupFailed: return "找不到这个地址，请检查域名或 IP"
        case .timedOut: return "连接超时：请检查网络、防火墙或安全组"
        case .notConnectedToInternet, .networkConnectionLost: return "网络断开了"
        case .secureConnectionFailed, .serverCertificateUntrusted, .serverCertificateHasBadDate,
             .serverCertificateNotYetValid, .serverCertificateHasUnknownRoot:
            return "HTTPS 证书有问题，无法建立安全连接"
        case .appTransportSecurityRequiresSecureConnection: return "系统拦截了明文 HTTP 连接"
        default: return "网络错误：\(e.localizedDescription)"
        }
    }
}
