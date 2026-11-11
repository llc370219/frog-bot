import SwiftUI

private let riskLabel = ["danger": "高风险", "exec": "执行命令", "write": "写入文件", "read": "只读"]
private let toolLabel = [
    "shell": "执行命令", "run_agent_cli": "启动外部 Agent", "write_file": "写入文件", "edit_file": "修改文件",
    "read_file": "读取文件", "list_dir": "查看目录", "grep": "搜索", "delete_secret": "删除密钥",
]

/// 审批卡片：原样展示要执行的命令或要改的文件，你点按钮才放行。
struct ApprovalCardView: View {
    let session: BotSession
    let approval: Approval

    @State private var busy = false
    @State private var error: String?

    private var a: Approval { approval }
    private var taskTitle: String? { session.tasks[a.taskID]?.title }
    private var tool: String { toolLabel[a.toolName] ?? a.toolName }

    var body: some View {
        if a.status != "pending" { decided } else { pending }
    }

    private var decided: some View {
        let (symbol, tone, text): (String, Color, String) = {
            switch a.status {
            case "allow": return ("checkmark.shield.fill", Theme.ok, a.scope == "session" ? "已批准（本任务内都放行）" : "已批准一次")
            case "deny": return ("xmark.shield.fill", Theme.danger, "已拒绝")
            default: return ("timer", Theme.fg3, "已失效")
            }
        }()
        let target = a.args["cli"].string ?? a.args["command"].string ?? a.args["path"].string ?? a.args["name"].str
        return HStack(spacing: 8) {
            Image(systemName: symbol).foregroundStyle(tone)
            Text(text).foregroundStyle(tone)
            Text("\(tool) · \(target)")
                .font(Theme.mono)
                .foregroundStyle(Theme.fg3)
                .lineLimit(1)
                .truncationMode(.middle)
            Spacer(minLength: 0)
        }
        .font(.system(size: 13))
        .padding(.vertical, 4)
    }

    private var question: String {
        switch a.toolName {
        case "shell": return "允许执行这条命令吗？"
        case "run_agent_cli": return "允许启动外部 Agent 吗？"
        case "write_file": return "允许写入这个文件吗？"
        case "edit_file": return "允许修改这个文件吗？"
        case "delete_secret": return "允许删除这个密钥吗？"
        default: return "允许这个操作吗？"
        }
    }

    private var pending: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .top, spacing: 10) {
                Image(systemName: "exclamationmark.shield")
                    .font(.system(size: 20))
                    .foregroundStyle(Theme.warn)
                VStack(alignment: .leading, spacing: 2) {
                    Text(question).font(.system(size: 15.5, weight: .semibold)).foregroundStyle(Theme.fg)
                    Text(subtitle)
                        .font(.system(size: 13.5))
                        .foregroundStyle(Theme.fg2)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            if session.isLocalDevice {
                ApprovalDetail(approval: a)
                Text(a.fromChat ? "只放行这一条 · 10 分钟内不处理会自动拒绝" : "只放行这一条 · 参数变了要重新确认 · 10 分钟内不处理会自动拒绝")
                    .font(.system(size: 12))
                    .foregroundStyle(Theme.fg3)
                    .fixedSize(horizontal: false, vertical: true)
                HStack(spacing: 8) {
                    Button("拒绝") { decide("deny") }
                        .buttonStyle(PillButtonStyle(kind: .danger))
                    Spacer(minLength: 0)
                    if busy { ProgressView() }
                    if !a.fromChat {
                        Button("本任务都允许") { decide("session") }
                            .buttonStyle(PillButtonStyle(kind: .secondary))
                    }
                    Button("允许一次") { decide("once") }
                        .buttonStyle(PillButtonStyle(kind: .primary))
                }
                .disabled(busy)
                if let error { Text(error).font(.system(size: 13)).foregroundStyle(Theme.danger) }
            } else {
                Text("这个操作发生在「\(session.deviceName)」上，请在那台设备的 frog 界面查看详情并审批。")
                    .font(.system(size: 13.5))
                    .foregroundStyle(Theme.fg2)
            }
        }
        .padding(14)
        .card(fill: Theme.warnSoft, radius: 18)
    }

    private var subtitle: String {
        var parts: [String] = []
        if let taskTitle { parts.append("「\(taskTitle)」") } else if a.fromChat { parts.append("聊天里直接\(tool)") }
        if !a.reason.isEmpty { parts.append(a.reason) }
        if let r = riskLabel[a.risk] { parts.append(r) }
        return parts.joined(separator: " · ")
    }

    private func decide(_ d: String) {
        busy = true
        error = nil
        Haptics.tap()
        Task {
            do { try await session.decide(a.id, d) } catch { self.error = error.localizedDescription; Haptics.error() }
            busy = false
        }
    }
}

/// 把“到底要做什么”原样展示：命令、文件路径和内容、修改前后。{{secret:NAME}} 只显示名字。
struct ApprovalDetail: View {
    let approval: Approval

    var body: some View {
        let g = approval.args
        VStack(alignment: .leading, spacing: 2) {
            switch approval.toolName {
            case "shell":
                (Text("$ ").foregroundColor(Theme.fg4) + secretText(g["command"].str))
                    .lineLimit(16)
            case "run_agent_cli":
                Text(g["cli"].str).foregroundStyle(Theme.fg3)
                Text(g["task"].str).lineLimit(16)
            case "delete_secret":
                Text("删除密钥 ") + Text(g["name"].str).foregroundColor(Theme.accent) + Text("（删除后无法恢复）")
            case "edit_file":
                Text(g["path"].str).foregroundStyle(Theme.fg3).padding(.bottom, 2)
                ForEach(Array(lines(g["old_string"].str).enumerated()), id: \.offset) { _, l in
                    Text("- " + l).foregroundStyle(Theme.danger)
                }
                ForEach(Array(lines(g["new_string"].str).enumerated()), id: \.offset) { _, l in
                    Text("+ " + l).foregroundStyle(Theme.ok)
                }
            case "write_file":
                Text(g["path"].str).foregroundStyle(Theme.fg3).padding(.bottom, 2)
                secretText(lines(g["content"].str, max: 14).joined(separator: "\n"))
            default:
                Text(g.pretty).lineLimit(24)
            }
        }
        .font(.system(size: 13, design: .monospaced))
        .foregroundStyle(Theme.fg)
        .lineSpacing(2)
        .textSelection(.enabled)
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
        .background(Theme.canvas, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 12, style: .continuous).strokeBorder(Theme.line, lineWidth: 0.5))
    }

    private func lines(_ s: String, max: Int = 40) -> [String] {
        let all = s.components(separatedBy: "\n")
        guard all.count > max else { return all }
        return Array(all.prefix(max)) + ["…（还有 \(all.count - max) 行）"]
    }

    private func secretText(_ s: String) -> Text {
        var out = Text("")
        var rest = Substring(s)
        while let r = rest.range(of: #"\{\{secret:[A-Z][A-Z0-9_]*\}\}"#, options: .regularExpression) {
            out = out + Text(String(rest[rest.startIndex..<r.lowerBound]))
            let name = rest[r].dropFirst(9).dropLast(2)
            out = out + Text("🔑" + name).foregroundColor(Theme.accent)
            rest = rest[r.upperBound...]
        }
        return out + Text(String(rest))
    }
}

/// 选项卡片：助理让你从几个选项里选一个
struct OptionCardView: View {
    let session: BotSession
    let card: Card
    @State private var busy = false

    var body: some View {
        let pending = card.status == "pending"
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .top) {
                Text(card.question)
                    .font(.system(size: 16, weight: .medium))
                    .foregroundStyle(Theme.fg)
                    .fixedSize(horizontal: false, vertical: true)
                Spacer(minLength: 8)
                if pending {
                    Button { act { try await session.dismiss(card.id) } } label: {
                        Image(systemName: "xmark")
                            .font(.system(size: 13, weight: .semibold))
                            .foregroundStyle(Theme.fg3)
                            .frame(width: 28, height: 28)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("关闭（不选择）")
                }
            }
            FlowLayout(spacing: 8) {
                ForEach(card.options, id: \.self) { o in
                    let chosen = card.answer == o
                    Button { act { try await session.answer(card.id, o) } } label: {
                        HStack(spacing: 5) {
                            if chosen { Image(systemName: "checkmark").font(.system(size: 12, weight: .bold)) }
                            Text(o).multilineTextAlignment(.leading)
                        }
                        .font(.system(size: 15))
                        .padding(.horizontal, 14)
                        .padding(.vertical, 8)
                        .foregroundStyle(chosen ? Theme.accent : pending ? Theme.fg : Theme.fg4)
                        .background(chosen ? Theme.accentSoft : Theme.canvas, in: Capsule())
                        .overlay(Capsule().strokeBorder(chosen ? Theme.accent : pending ? Theme.line2 : Theme.line, lineWidth: 1))
                        .contentShape(Capsule())
                    }
                    .buttonStyle(.plain)
                    .disabled(!pending || busy)
                }
            }
            if card.status == "dismissed" {
                Text("已关闭，未选择").font(.system(size: 13)).foregroundStyle(Theme.fg4)
            }
        }
        .padding(16)
        .card(fill: Theme.s2, border: pending ? Theme.accent.opacity(0.35) : nil, radius: 18)
        .opacity(pending ? 1 : 0.85)
    }

    private func act(_ f: @escaping () async throws -> Void) {
        busy = true
        Haptics.tap()
        Task { try? await f(); busy = false }
    }
}

/// 密钥卡片：值只发给服务器加密保存，不进聊天记录，助理也看不到。
struct SecretCardView: View {
    let session: BotSession
    let request: SecretRequest

    @State private var value = ""
    @State private var reveal = false
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        switch request.status {
        case "saved":
            HStack(spacing: 8) {
                Image(systemName: "checkmark.shield.fill").foregroundStyle(Theme.ok)
                Text("已安全保存").foregroundStyle(Theme.ok)
                Text(request.name).font(Theme.mono).foregroundStyle(Theme.fg)
                Spacer()
                Image(systemName: "lock.fill").font(.system(size: 11)).foregroundStyle(Theme.fg4)
            }
            .font(.system(size: 13.5))
            .padding(.horizontal, 12).padding(.vertical, 10)
            .card(fill: Theme.s2, radius: 14)
        case "cancelled":
            HStack(spacing: 8) {
                Image(systemName: "xmark")
                Text("已取消，未保存")
                Text(request.name).font(Theme.mono)
                Spacer()
            }
            .font(.system(size: 13.5))
            .foregroundStyle(Theme.fg3)
            .padding(.horizontal, 12).padding(.vertical, 10)
            .card(fill: Theme.s2, radius: 14)
        default:
            if session.isLocalDevice { form } else {
                Label("请在「\(session.deviceName)」的 frog 界面填写 \(request.name)。", systemImage: "key.fill")
                    .font(.system(size: 14))
                    .foregroundStyle(Theme.fg3)
                    .padding(12)
                    .card(fill: Theme.s2, radius: 14)
            }
        }
    }

    private var form: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .top, spacing: 12) {
                Image(systemName: "key.fill")
                    .font(.system(size: 15))
                    .foregroundStyle(Theme.accent)
                    .frame(width: 34, height: 34)
                    .background(Theme.canvas, in: Circle())
                VStack(alignment: .leading, spacing: 4) {
                    HStack(spacing: 8) {
                        Text(request.exists ? "更新密钥" : "保存密钥").font(.system(size: 15.5, weight: .semibold))
                        Text(request.name)
                            .font(.system(size: 13, design: .monospaced))
                            .padding(.horizontal, 6).padding(.vertical, 1)
                            .background(Theme.canvas, in: RoundedRectangle(cornerRadius: 5))
                    }
                    if let d = request.description, !d.isEmpty {
                        Text(d).font(.system(size: 13.5)).foregroundStyle(Theme.fg3).fixedSize(horizontal: false, vertical: true)
                    }
                }
            }
            HStack(spacing: 6) {
                Group {
                    if reveal { TextField("粘贴密钥…", text: $value) } else { SecureField("粘贴密钥…", text: $value) }
                }
                .textFieldStyle(.plain)
                .font(.system(size: 15, design: .monospaced))
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .submitLabel(.done)
                .onSubmit(save)
                Button { reveal.toggle() } label: {
                    Image(systemName: reveal ? "eye.slash" : "eye")
                        .foregroundStyle(Theme.fg3)
                        .frame(width: 32, height: 32)
                }
                .buttonStyle(.plain)
            }
            .padding(.leading, 12)
            .padding(.trailing, 4)
            .frame(height: 46)
            .background(Theme.canvas, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 12, style: .continuous).strokeBorder(Theme.line2, lineWidth: 0.5))
            Label("直接加密保存在服务器上，不会进入聊天记录，助理也看不到；使用时只按名字引用，每次都会先请你确认。", systemImage: "lock.fill")
                .font(.system(size: 12))
                .foregroundStyle(Theme.fg4)
                .fixedSize(horizontal: false, vertical: true)
            if let error { Text(error).font(.system(size: 13)).foregroundStyle(Theme.danger) }
            HStack(spacing: 8) {
                Button("取消") { cancel() }
                    .buttonStyle(PillButtonStyle(kind: .secondary))
                    .disabled(busy)
                Spacer()
                Button(action: save) { Label("保存", systemImage: "checkmark.shield") }
                    .buttonStyle(PillButtonStyle(kind: .primary))
                    .disabled(busy || value.isEmpty)
            }
        }
        .padding(16)
        .card(fill: Theme.s2, radius: 18)
    }

    private func save() {
        guard !value.isEmpty, !busy else { return }
        busy = true; error = nil
        let v = value
        Task {
            do { try await session.saveSecret(request.id, v); value = ""; Haptics.success() } catch { self.error = error.localizedDescription }
            busy = false
        }
    }

    private func cancel() {
        busy = true; error = nil
        Task {
            do { try await session.cancelSecret(request.id); value = "" } catch { self.error = error.localizedDescription }
            busy = false
        }
    }
}
