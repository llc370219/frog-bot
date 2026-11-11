import Foundation

/// 文字格式化：时长、相对时间、把工具调用翻译成人话。移植自 web/src/lib/format.ts。
enum Fmt {
    static func dur(_ ms: Double) -> String {
        let s = max(0, Int((ms / 1000).rounded()))
        if s < 60 { return "\(s) 秒" }
        let m = s / 60
        if m < 60 { return "\(m) 分 \(s % 60) 秒" }
        return "\(m / 60) 小时 \(m % 60) 分"
    }

    static func clock(_ ms: Double) -> String {
        let s = max(0, Int(ms / 1000))
        return "\(s / 60):" + String(format: "%02d", s % 60)
    }

    static func ago(_ ms: Double) -> String {
        ms < 60_000 ? "\(max(1, Int((ms / 1000).rounded()))) 秒前" : "\(Int((ms / 60_000).rounded())) 分钟前"
    }

    static func size(_ n: Int) -> String {
        if n < 1024 { return "\(n) B" }
        if n < 1 << 20 { return "\(n / 1024) KB" }
        return String(format: "%.1f MB", Double(n) / Double(1 << 20))
    }

    static func base(_ p: String) -> String {
        p.split(separator: "/").last.map(String.init) ?? p
    }

    private static let timeOnly: DateFormatter = {
        let f = DateFormatter(); f.dateFormat = "HH:mm"; return f
    }()
    private static let dayTime: DateFormatter = {
        let f = DateFormatter(); f.dateFormat = "M月d日 HH:mm"; return f
    }()

    static func localTime(_ ms: Double) -> String {
        let d = Date(timeIntervalSince1970: ms / 1000)
        return Calendar.current.isDateInToday(d) ? timeOnly.string(from: d) : dayTime.string(from: d)
    }

    /// 把一次工具调用翻译成人话，例如“读取 README.md”“运行测试”
    static func describe(_ tool: String, _ args: JSON) -> (verb: String, target: String) {
        switch tool {
        case "run_agent_cli": return ("运行", args["cli"].string ?? "外部 Agent")
        case "read_file": return ("读取", base(args["path"].str))
        case "write_file": return ("写入", base(args["path"].str))
        case "edit_file": return ("修改", base(args["path"].str))
        case "list_dir":
            let p = args["path"].str
            return ("查看目录", p.isEmpty || p == "." ? "工作区" : p)
        case "grep": return ("搜索", "「\(args["pattern"].str)」")
        case "shell":
            let c = args["command"].str.trimmingCharacters(in: .whitespacesAndNewlines)
            let kinds: [(String, String)] = [
                (#"\b(go test|npm (run )?test|pytest|cargo test|jest|vitest)\b"#, "运行测试"),
                (#"\b(go build|npm run build|make|cargo build|tsc)\b"#, "构建项目"),
                (#"\b(go run|node|python3?|\./)\S*"#, "运行程序"),
                (#"\b(npm|pnpm|yarn|pip3?|go get|apt(-get)?) (i|install|add)\b"#, "安装依赖"),
                (#"^git\b"#, "Git 操作"),
                (#"\b(curl|wget)\b"#, "访问网络"),
                (#"^(ls|tree|find)\b"#, "查看文件"),
                (#"^(cat|head|tail|less|wc)\b"#, "查看内容"),
                (#"^(rm|mv|cp|mkdir)\b"#, "整理文件"),
            ]
            let verb = kinds.first(where: { c.range(of: $0.0, options: .regularExpression) != nil })?.1 ?? "执行命令"
            return (verb, c.count > 60 ? String(c.prefix(57)) + "…" : c)
        default:
            return (tool, "")
        }
    }

    static func stepText(_ s: Step) -> String {
        let d = describe(s.tool, s.args)
        return d.verb + (!d.target.isEmpty && s.tool != "shell" ? " " + d.target : "")
    }

    static func nowDoing(_ t: BotTask) -> String {
        if t.awaiting > 0 { return "等你确认一个操作" }
        if t.pausing { return "正在暂停（做完这一步就停）" }
        if let cur = t.steps.last(where: { !$0.done }) { return "正在" + stepText(cur) }
        if !t.text.isEmpty { return "正在整理结果" }
        if !t.thinking.isEmpty { return "正在思考下一步" }
        return t.steps.isEmpty ? "正在看要做什么" : "正在想下一步"
    }

    /// 聊天层的工具标签：“查看了 xx” → “正在查看 xx…”
    static func liveToolLabel(_ label: String) -> String {
        for p in ["查看了", "读取了", "搜索了", "请求", "派出了", "写入了", "停止了", "暂停了", "恢复了", "排上了"] where label.hasPrefix(p) {
            let verb = p.replacingOccurrences(of: "了", with: "")
            let rest = String(label.dropFirst(p.count))
            return "正在\(verb)\(rest)…"
        }
        return label + "…"
    }
}
