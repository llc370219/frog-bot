import SwiftUI

let quietMs: Double = 15_000 // 这么久没有新动作，就明确告诉用户“还在工作”

struct TaskStatusLine {
    var symbol: String
    var tone: Color
    var text: String
    var spinning = false

    init(_ t: BotTask, now: Double, pos: Int?) {
        let el = Fmt.dur(t.elapsed(now))
        switch t.status {
        case .completed:
            symbol = "checkmark"; tone = Theme.ok
            text = "已完成 · 用时 \(el) · \(t.steps.count) 步" + (t.files.isEmpty ? "" : " · 改动 \(t.files.count) 个文件")
        case .cancelled:
            symbol = "slash.circle"; tone = Theme.fg3
            text = (t.stopReason == "stall" ? "长时间没有进展，已自动停止" : "已停止") + " · \(t.steps.count) 步"
        case .failed:
            symbol = "xmark"; tone = Theme.danger
            text = "失败 · \(t.steps.count) 步 · \(el)"
        case .paused:
            symbol = "pause.fill"; tone = Theme.warn
            let why: String
            switch t.stopReason {
            case "held": why = "待办 · 等你说开始"
            case "restart": why = "服务重启后暂停 · 进度已保存"
            case "chat": why = "先暂停，等聊完再继续"
            default: why = "已暂停 · 进度已保存"
            }
            text = why + (t.steps.isEmpty ? "" : " · 已做 \(t.steps.count) 步")
        case .queued:
            symbol = "hourglass"; tone = Theme.fg3
            text = pos.map { "排队中 · 第 \($0) 位" } ?? "马上开始…"
        case .running:
            symbol = "arrow.triangle.2.circlepath"; tone = Theme.accent; spinning = true
            text = "\(Fmt.nowDoing(t)) · \(t.steps.count) 步 · \(el)"
        }
    }
}

struct StatusBadge: View {
    let line: TaskStatusLine
    var size: CGFloat = 18

    var body: some View {
        Group {
            if line.spinning {
                Spinner(size: size, tint: Theme.accent)
            } else {
                Image(systemName: line.symbol)
                    .font(.system(size: size * 0.72, weight: .semibold))
                    .foregroundStyle(line.tone)
            }
        }
        .frame(width: size, height: size)
    }
}

/// 任务操作：随状态变化（暂停/停止、继续、下一个做、重试）。手机上收进一个「…」菜单，省地方。
struct TaskMenu: View {
    let session: BotSession
    let task: BotTask
    var pos: Int?

    private struct Item: Identifiable {
        let action: String, label: String, symbol: String
        var danger = false
        var id: String { action }
    }

    private var items: [Item] {
        switch task.status {
        case .running:
            return (task.pausing ? [] : [Item(action: "pause", label: "暂停", symbol: "pause.fill")])
                + [Item(action: "cancel", label: "停止", symbol: "stop.fill", danger: true)]
        case .paused:
            return [Item(action: "resume", label: task.stopReason == "held" ? "开始" : "继续", symbol: "play.fill"),
                    Item(action: "cancel", label: "停止", symbol: "stop.fill", danger: true)]
        case .queued:
            return ((pos ?? 0) > 1 ? [Item(action: "top", label: "下一个做", symbol: "arrow.up.to.line")] : [])
                + [Item(action: "pause", label: "挂起", symbol: "pause.fill"),
                   Item(action: "cancel", label: "停止", symbol: "stop.fill", danger: true)]
        case .failed, .cancelled:
            return [Item(action: "retry", label: "重试", symbol: "arrow.counterclockwise")]
        case .completed:
            return []
        }
    }

    var body: some View {
        let list = items
        if !list.isEmpty {
            Menu {
                ForEach(list) { it in
                    Button(role: it.danger ? .destructive : nil) { run(it.action) } label: {
                        Label(it.label, systemImage: it.symbol)
                    }
                }
            } label: {
                Image(systemName: "ellipsis")
                    .font(.system(size: 15, weight: .semibold))
                    .foregroundStyle(Theme.fg3)
                    .frame(width: 34, height: 34)
                    .contentShape(Rectangle())
            }
        }
    }

    private func run(_ action: String) {
        Haptics.tap()
        Task {
            do { try await session.task(task.id, action) } catch { Haptics.error() }
        }
    }
}

/// 工作卡片：一个后台任务的标题、状态、步骤和结果。运行中自动展开。
struct TaskCard: View {
    let session: BotSession
    let task: BotTask
    var pos: Int?

    @State private var userOpen: Bool?

    private var running: Bool { task.status == .running }
    private var open: Bool { userOpen ?? running }

    var body: some View {
        TimelineView(.periodic(from: .now, by: task.isLive ? 1 : 3600)) { ctx in
            content(now: ctx.date.timeIntervalSince1970 * 1000)
        }
    }

    private func content(now: Double) -> some View {
        let line = TaskStatusLine(task, now: now, pos: pos)
        let quiet = running && task.awaiting == 0 ? now - task.lastEvent : 0
        return VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 10) {
                Button {
                    withAnimation(.easeOut(duration: 0.15)) { userOpen = !open }
                } label: {
                    HStack(spacing: 11) {
                        StatusBadge(line: line)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(task.title)
                                .font(.system(size: 15, weight: .medium))
                                .foregroundStyle(Theme.fg)
                                .lineLimit(2)
                                .multilineTextAlignment(.leading)
                            HStack(spacing: 6) {
                                if task.awaiting > 0 {
                                    Chip(text: "等你确认", systemImage: "exclamationmark.shield.fill", tint: Theme.warn, fill: Theme.warnSoft)
                                }
                                Text(line.text)
                                    .font(.system(size: 12.5))
                                    .foregroundStyle(running ? Theme.fg2 : Theme.fg3)
                                    .lineLimit(1)
                            }
                        }
                        Spacer(minLength: 4)
                        Image(systemName: "chevron.down")
                            .font(.system(size: 12, weight: .semibold))
                            .foregroundStyle(Theme.fg4)
                            .rotationEffect(.degrees(open ? 180 : 0))
                    }
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                TaskMenu(session: session, task: task, pos: pos)
            }
            .padding(.leading, 14)
            .padding(.trailing, 6)
            .padding(.vertical, 10)

            if running && quiet >= quietMs {
                quietBanner(quiet: quiet, now: now)
            }

            if open {
                Rectangle().fill(Theme.line).frame(height: 0.5)
                details(now: now)
                    .padding(.horizontal, 14)
                    .padding(.vertical, 12)
            }

            if !task.files.isEmpty && !running {
                Rectangle().fill(Theme.line).frame(height: 0.5)
                FlowLayout(spacing: 8) {
                    ForEach(task.files, id: \.self) { f in FileChip(session: session, path: f) }
                }
                .padding(12)
            }
        }
        .card(fill: Theme.s2, border: task.awaiting > 0 ? Theme.warn.opacity(0.45) : nil, radius: 16)
    }

    @ViewBuilder
    private func quietBanner(quiet: Double, now: Double) -> some View {
        let stalled = quiet >= session.stallMs && !task.steps.contains { $0.tool == "run_agent_cli" }
        if stalled {
            VStack(alignment: .leading, spacing: 8) {
                Label("已经 \(Fmt.ago(quiet).replacingOccurrences(of: "前", with: ""))没有新进展了。可能在等一个很慢的命令，也可能卡住了。",
                      systemImage: "exclamationmark.triangle.fill")
                    .fixedSize(horizontal: false, vertical: true)
                HStack(spacing: 8) {
                    Button("停止") { Task { try? await session.task(task.id, "cancel") } }
                        .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
                    Button("停止并重试") {
                        Task {
                            try? await session.task(task.id, "cancel")
                            try? await Task.sleep(nanoseconds: 400_000_000)
                            try? await session.task(task.id, "retry")
                        }
                    }
                    .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
                }
            }
            .font(.system(size: 13))
            .foregroundStyle(Theme.warn)
            .padding(.horizontal, 14)
            .padding(.vertical, 10)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Theme.warnSoft)
        } else {
            HStack(spacing: 8) {
                TypingDots(size: 5)
                Text("仍在工作…（上次动作 \(Fmt.ago(quiet))\(now - task.lastBeat < 40_000 ? "" : "，等待响应")）")
            }
            .font(.system(size: 13))
            .foregroundStyle(Theme.fg3)
            .padding(.horizontal, 14)
            .padding(.vertical, 8)
            .frame(maxWidth: .infinity, alignment: .leading)
            .overlay(alignment: .top) { Rectangle().fill(Theme.line).frame(height: 0.5) }
        }
    }

    @ViewBuilder
    private func details(now: Double) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            if !task.input.isEmpty && task.input != task.title {
                Text("要求：\(task.input)")
                    .font(.system(size: 13))
                    .foregroundStyle(Theme.fg4)
                    .lineLimit(3)
            }
            if task.steps.isEmpty && task.text.isEmpty {
                Label(task.status == .queued ? "等前面的任务做完…" : task.status == .paused ? "还没开始。" : "正在看要做什么…",
                      systemImage: "hourglass")
                    .font(.system(size: 14))
                    .foregroundStyle(Theme.fg3)
            }
            ForEach(Array(task.steps.enumerated()), id: \.offset) { _, step in
                StepRow(step: step, now: now)
            }
            if running && !task.thinking.isEmpty && task.text.isEmpty {
                (Text("思考中").foregroundColor(Theme.fg3) + Text(" · " + String(task.thinking.suffix(200))))
                    .font(.system(size: 13))
                    .italic()
                    .foregroundStyle(Theme.fg4)
                    .lineLimit(3)
            }
            if running && !task.text.isEmpty {
                (Text(String(task.text.suffix(400))) + Text(" ▍").foregroundColor(Theme.accent))
                    .font(.system(size: 14))
                    .foregroundStyle(Theme.fg2)
                    .lineSpacing(3)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if !task.isLive, let err = task.error, !err.isEmpty {
                Text(err)
                    .font(.system(size: 14))
                    .foregroundStyle(Theme.danger)
                    .textSelection(.enabled)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(Theme.dangerSoft, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
            }
            if task.status == .completed, (task.error ?? "").isEmpty {
                let result = (task.summary ?? task.output ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
                if !result.isEmpty {
                    Text(result.replacingOccurrences(of: "[*`#]", with: "", options: .regularExpression))
                        .font(.system(size: 14))
                        .foregroundStyle(Theme.fg3)
                        .lineSpacing(3)
                        .lineLimit(6)
                        .textSelection(.enabled)
                }
            }
        }
    }
}

struct StepRow: View {
    let step: Step
    let now: Double
    @State private var open = false

    private static let icons: [String: String] = [
        "read_file": "doc.text", "write_file": "square.and.pencil", "edit_file": "square.and.pencil",
        "list_dir": "folder", "grep": "magnifyingglass", "shell": "terminal", "run_agent_cli": "terminal",
    ]

    var body: some View {
        let d = Fmt.describe(step.tool, step.args)
        let tone = step.denied ? Theme.danger : step.error ? Theme.warn : step.done ? Theme.fg4 : Theme.accent
        VStack(alignment: .leading, spacing: 5) {
            if !step.thought.isEmpty {
                Text(Markdown.inline(step.thought))
                    .font(.system(size: 14))
                    .foregroundStyle(Theme.fg2)
                    .lineSpacing(3)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Button {
                if step.output != nil { withAnimation(.easeOut(duration: 0.12)) { open.toggle() } }
            } label: {
                HStack(spacing: 8) {
                    Image(systemName: Self.icons[step.tool] ?? "wrench.and.screwdriver")
                        .font(.system(size: 13))
                        .foregroundStyle(tone)
                        .frame(width: 18)
                    Text(d.verb)
                        .font(.system(size: 14, weight: step.done ? .regular : .medium))
                        .foregroundStyle(step.done ? Theme.fg3 : Theme.fg)
                    Text(d.target)
                        .font(Theme.mono)
                        .foregroundStyle(Theme.fg2)
                        .lineLimit(1)
                        .truncationMode(.middle)
                    Spacer(minLength: 4)
                    trailing
                    if step.output != nil {
                        Image(systemName: "chevron.down")
                            .font(.system(size: 10, weight: .semibold))
                            .foregroundStyle(Theme.fg4)
                            .rotationEffect(.degrees(open ? 180 : 0))
                    }
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            if open, let out = step.output {
                ScrollView {
                    Text(out)
                        .font(.system(size: 12, design: .monospaced))
                        .foregroundStyle(Theme.fg2)
                        .lineSpacing(2)
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(10)
                }
                .frame(height: min(CGFloat(out.components(separatedBy: "\n").count) * 16 + 22, 240))
                .background(Theme.canvas, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
            }
        }
    }

    @ViewBuilder
    private var trailing: some View {
        if !step.done {
            HStack(spacing: 4) {
                if now - step.at > 3000 {
                    Text("\(Int((now - step.at) / 1000))s").font(.system(size: 12)).monospacedDigit()
                }
                ProgressView().controlSize(.mini)
            }
            .foregroundStyle(Theme.accent)
        } else if step.denied {
            Text("未批准").font(.system(size: 12)).foregroundStyle(Theme.danger)
        } else if step.error {
            Text("出错").font(.system(size: 12)).foregroundStyle(Theme.warn)
        } else {
            Image(systemName: "checkmark").font(.system(size: 12, weight: .semibold)).foregroundStyle(Theme.ok)
        }
    }
}

/// 输入框上方常驻的“工作条”：只要有任务在跑/排队/暂停就显示；点开是任务托盘。
struct TaskDock: View {
    let session: BotSession
    @Binding var trayOpen: Bool

    var body: some View {
        let running = session.running
        let queued = session.queued
        let paused = session.paused
        if !running.isEmpty || !queued.isEmpty || !paused.isEmpty {
            TimelineView(.periodic(from: .now, by: 1)) { ctx in
                bar(now: ctx.date.timeIntervalSince1970 * 1000, running: running, queued: queued, paused: paused)
            }
        }
    }

    private func bar(now: Double, running: [BotTask], queued: [BotTask], paused: [BotTask]) -> some View {
        let awaiting = running.filter { $0.awaiting > 0 }
        let lead = awaiting.first ?? running.max(by: { $0.lastEvent < $1.lastEvent })
        let quiet = lead.map { $0.awaiting == 0 ? now - $0.lastEvent : 0 } ?? 0
        let extra = [
            running.count > 1 ? "+\(running.count - 1) 个在跑" : nil,
            queued.isEmpty ? nil : "\(queued.count) 个排队",
            paused.isEmpty ? nil : "\(paused.count) 个暂停",
        ].compactMap { $0 }.joined(separator: " · ")
        let alert = !awaiting.isEmpty
        return Button { trayOpen = true } label: {
            HStack(spacing: 10) {
                if alert {
                    Circle().fill(Theme.warnDot).frame(width: 8, height: 8)
                } else if lead != nil {
                    Spinner(size: 14, tint: Theme.accent)
                } else if !queued.isEmpty {
                    ProgressView().controlSize(.mini)
                } else {
                    Image(systemName: "pause.fill").font(.system(size: 11)).foregroundStyle(Theme.warn)
                }
                VStack(alignment: .leading, spacing: 1) {
                    if let lead {
                        Text(lead.title).font(.system(size: 13.5, weight: .medium)).foregroundStyle(Theme.fg).lineLimit(1)
                        Text((quiet >= quietMs ? "仍在工作…（上次动作 \(Fmt.ago(quiet))）" : Fmt.nowDoing(lead))
                             + " · \(Fmt.clock(lead.elapsed(now)))" + (extra.isEmpty ? "" : " · " + extra))
                            .font(.system(size: 12))
                            .foregroundStyle(alert ? Theme.warn : Theme.fg3)
                            .lineLimit(1)
                    } else if !queued.isEmpty {
                        Text("\(queued.count) 个任务排队，马上开始").font(.system(size: 13.5)).foregroundStyle(Theme.fg2)
                    } else {
                        Text("\(paused.count) 个任务已暂停或待办").font(.system(size: 13.5)).foregroundStyle(Theme.fg2)
                    }
                }
                Spacer(minLength: 6)
                Image(systemName: "chevron.up")
                    .font(.system(size: 11, weight: .semibold))
                    .foregroundStyle(Theme.fg4)
            }
            .padding(.horizontal, 14)
            .frame(minHeight: 44)
            .background(alert ? Theme.warnSoft : Theme.s2, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            .contentShape(RoundedRectangle(cornerRadius: 14, style: .continuous))
        }
        .buttonStyle(.plain)
    }
}

/// 任务托盘（底部弹出）：等你确认 / 运行中 / 排队 / 暂停 / 最近完成
struct TaskTray: View {
    let session: BotSession
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            TimelineView(.periodic(from: .now, by: 1)) { ctx in
                let now = ctx.date.timeIntervalSince1970 * 1000
                let pos = session.queuePositions
                let running = session.running
                let done = session.tasks.values
                    .filter { $0.status.terminal && $0.ended != nil && now - ($0.ended ?? 0) < 60 * 60_000 }
                    .sorted { ($0.ended ?? 0) > ($1.ended ?? 0) }
                    .prefix(5)
                let empty = running.isEmpty && session.queued.isEmpty && session.paused.isEmpty && done.isEmpty
                List {
                    if empty {
                        Text("现在没有任务。在聊天里让 \(session.profile.name) 做件事，它会在后台开工。")
                            .font(.subheadline)
                            .foregroundStyle(Theme.fg3)
                    }
                    group("等你确认", running.filter { $0.awaiting > 0 }, now: now, pos: pos)
                    group("运行中", running.filter { $0.awaiting == 0 }, now: now, pos: pos)
                    group("排队中", session.queued, now: now, pos: pos)
                    group("已暂停 / 待办", session.paused, now: now, pos: pos)
                    group("最近完成", Array(done), now: now, pos: pos)
                }
                .listStyle(.insetGrouped)
            }
            .navigationTitle("任务")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) { Button("完成") { dismiss() } }
            }
        }
    }

    @ViewBuilder
    private func group(_ label: String, _ list: [BotTask], now: Double, pos: [String: Int]) -> some View {
        if !list.isEmpty {
            Section(label) {
                ForEach(list) { t in
                    let line = TaskStatusLine(t, now: now, pos: pos[t.id])
                    HStack(spacing: 12) {
                        StatusBadge(line: line, size: 18)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(t.title).font(.system(size: 15, weight: .medium)).foregroundStyle(Theme.fg).lineLimit(2)
                            Text(line.text).font(.system(size: 12.5)).foregroundStyle(Theme.fg3).lineLimit(2)
                        }
                        Spacer(minLength: 6)
                        TaskMenu(session: session, task: t, pos: pos[t.id])
                    }
                }
            }
        }
    }
}
