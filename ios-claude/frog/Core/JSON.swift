import Foundation

/// 后端事件的数据形状很多（消息、任务、审批、agent 步骤……），统一解析成这个轻量的 JSON 值，
/// 再按需取字段。取不到的字段返回空值而不是崩溃，和网页版 `d.xxx` 的宽松读取一致。
enum JSON: Equatable, Hashable {
    case null
    case boolean(Bool)
    case number(Double)
    case text(String)
    case list([JSON])
    case dict([String: JSON])

    init(any: Any?) {
        switch any {
        case nil:
            self = .null
        case is NSNull:
            self = .null
        case let n as NSNumber:
            if CFGetTypeID(n) == CFBooleanGetTypeID() {
                self = .boolean(n.boolValue)
            } else {
                self = .number(n.doubleValue)
            }
        case let s as String:
            self = .text(s)
        case let a as [Any]:
            self = .list(a.map { JSON(any: $0) })
        case let d as [String: Any]:
            self = .dict(d.mapValues { JSON(any: $0) })
        default:
            self = .null
        }
    }

    static func parse(_ data: Data) -> JSON? {
        guard let obj = try? JSONSerialization.jsonObject(with: data, options: [.fragmentsAllowed]) else { return nil }
        return JSON(any: obj)
    }

    subscript(key: String) -> JSON {
        if case .dict(let d) = self { return d[key] ?? .null }
        return .null
    }

    var isNull: Bool { if case .null = self { return true }; return false }

    /// 字符串值；数字和布尔也转成文字（审批卡片里显示参数时用得到）。
    var string: String? {
        switch self {
        case .text(let s): return s
        case .number(let n):
            if n == n.rounded(), abs(n) < 1e15 { return String(Int64(n)) }
            return String(n)
        case .boolean(let b): return b ? "true" : "false"
        default: return nil
        }
    }

    /// 取不到时返回空字符串。
    var str: String { string ?? "" }

    var double: Double? {
        switch self {
        case .number(let n): return n
        case .text(let s): return Double(s)
        default: return nil
        }
    }

    var int: Int? {
        guard let d = double, d.isFinite, abs(d) < 9e15 else { return nil }
        return Int(d)
    }

    var bool: Bool {
        switch self {
        case .boolean(let b): return b
        case .number(let n): return n != 0
        case .text(let s): return !s.isEmpty
        default: return false
        }
    }

    var array: [JSON] { if case .list(let a) = self { return a }; return [] }
    var object: [String: JSON] { if case .dict(let d) = self { return d }; return [:] }

    /// 还原成 Foundation 对象，用于序列化。
    var foundation: Any {
        switch self {
        case .null: return NSNull()
        case .boolean(let b): return b
        case .number(let n): return n
        case .text(let s): return s
        case .list(let a): return a.map { $0.foundation }
        case .dict(let d): return d.mapValues { $0.foundation }
        }
    }

    /// 缩进好的 JSON 文本（审批卡片显示未知工具的参数时用）。
    var pretty: String {
        let obj = foundation
        guard JSONSerialization.isValidJSONObject(obj),
              let data = try? JSONSerialization.data(withJSONObject: obj, options: [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]),
              let s = String(data: data, encoding: .utf8) else { return str }
        return s
    }
}

/// 时间工具：后端用 Go 的 RFC3339Nano（小数秒最多 9 位），这里统一换成毫秒时间戳，和网页版的计算方式一致。
enum Stamp {
    static var now: Double { Date().timeIntervalSince1970 * 1000 }

    private static let iso: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        return f
    }()

    static func date(_ s: String?) -> Date? {
        guard var s, !s.isEmpty else { return nil }
        var frac = 0.0
        if let r = s.range(of: #"\.\d+"#, options: .regularExpression) {
            frac = Double("0" + s[r]) ?? 0
            s = s.replacingCharacters(in: r, with: "")
        }
        return iso.date(from: s)?.addingTimeInterval(frac)
    }

    /// ISO 时间 → 毫秒；解析不了就当作“现在”（和网页版 `ts()` 一样）。
    static func ms(_ s: String?) -> Double {
        guard let d = date(s) else { return now }
        return d.timeIntervalSince1970 * 1000
    }
}
