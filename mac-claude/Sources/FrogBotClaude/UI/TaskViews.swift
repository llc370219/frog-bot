import AppKit
import SwiftUI

let quietMs: Double = 15_000 // 这么久没有新动作，就明确告诉用户“还在工作”

struct TaskStatusLine {
    var symbol: String
    var tone: Color
    var fill: Color
    var text: String
    var spinning = false

    init(_ t: BotTask, now: Double, pos: Int?) {
        let el = Fmt.dur(t.elapsed(now))
        switch t.status {
        case .completed:
            symbol = "checkmark"; tone = Theme.ok; fill = Theme.s2
            text = "已完成 · 用时 \(el) · \(t.steps.count) 步" + (t.files.isEmpty ? "" : " · 改动 \(t.files.count) 个文件")
        case .cancelled:
            symbol = "slash.circle"; tone = Theme.fg3; fill = Theme.s2
            text = (t.stopReason == "stall" ? "长时间没有进展，已自动停止" : "已停止") + " · \(t.steps.count) 步"
        case .failed:
            symbol = "xmark"; tone = Theme.danger; fill = Theme.dangerSoft
            text = "失败 · \(t.steps.count) 步 · \(el)"
        case .paused:
            symbol = "pause.fill"; tone = Theme.warn; fill = Theme.warnSoft
            let why: String
            switch t.stopReason {
            case "held": why = "待办 · 还没开始，等你说开始"
            case "restart": why = "服务重启后暂停 · 进度已保存"
            case "chat": why = "先暂停，等聊完再继续 · 进度已保存"
            default: why = "已暂停 · 进度已保存"
            }
            text = why + (t.steps.isEmpty ? "" : " · 已做 \(t.steps.count) 步")
        case .queued:
            symbol = "hourglass"; tone = Theme.fg3; fill = Theme.s2
            text = pos.map { "排队中 · 第 \($0) 位，前面的做完就自动开始" } ?? "马上开始…"
        case .running:
            symbol = "arrow.triangle.2.circlepath"; tone = Theme.accent; fill = Theme.accentSoft; spinning = true
            text = "\(Fmt.nowDoing(t)) · 已做 \(t.steps.count) 步 · \(el)"
        }
    }
}

@MainActor
struct StatusBadge: View {
    let line: TaskStatusLine
    var size: CGFloat = 16

    var body: some View {
        Group {
            if line.spinning {
                Spinner(size: size, tint: Theme.fg3)
            } else {
                Image(systemName: line.symbol)
                    .font(.system(size: size * 0.75, weight: .semibold))
                    .foregroundStyle(line.tone)
            }
        }
        .frame(width: size, height: size)
    }
}

/// 任务操作按钮：随状态变化（暂停/停止、继续、下一个做、重试）
@MainActor
struct TaskButtons: View {
    let session: BotSession
    let task: BotTask
    var pos: Int?
    var compact = true

    var body: some View {
        HStack(spacing: 4) {
            switch task.status {
            case .running:
                if !task.pausing { button("pause", "暂停", "pause.fill") }
                stopButton
            case .paused:
                button("resume", task.stopReason == "held" ? "开始" : "继续", "play.fill")
                stopButton
            case .queued:
                if (pos ?? 0) > 1 { button("top", "下一个做", "arrow.up.to.line") }
                button("pause", "挂起", "pause.fill")
                stopButton
            case .failed, .cancelled:
                button("retry", "重试", "arrow.counterclockwise")
            case .completed:
                EmptyView()
            }
        }
    }

    private var stopButton: some View { button("cancel", "停止", "stop.fill", danger: true) }

    private func button(_ action: String, _ label: String, _ symbol: String, danger: Bool = false) -> some View {
        Button {
            Task {
                do { try await session.task(task.id, action) } catch { NSSound.beep() }
            }
        } label: {
            if compact { Image(systemName: symbol) } else { Label(label, systemImage: symbol) }
        }
        .buttonStyle(IconButtonStyle(size: 24, tint: danger ? Theme.danger.opacity(0.85) : Theme.fg3))
        .help(label)
    }
}

/// 工作卡片：一个后台任务的标题、状态、步骤和结果。运行中自动展开。
@MainActor
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
        .id("task-\(task.id)")
    }

    private func content(now: Double) -> some View {
        let line = TaskStatusLine(task, now: now, pos: pos)
        let quiet = running && task.awaiting == 0 ? now - task.lastEvent : 0
        return VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 12) {
                Button {
                    withAnimation(.easeOut(duration: 0.15)) { userOpen = !open }
                } label: {
                    HStack(spacing: 12) {
                        StatusBadge(line: line)
                        VStack(alignment: .leading, spacing: 2) {
                            HStack(spacing: 6) {
                                Text(task.title)
                                    .font(.system(size: 13.5, weight: .medium))
                                    .foregroundStyle(Theme.fg)
                                    .lineLimit(1)
                                if task.awaiting > 0 {
                                    Chip(text: "等你确认", systemImage: "exclamationmark.shield.fill", tint: Theme.warn, fill: Theme.warnSoft)
                                }
                            }
                            Text(line.text)
                                .font(.system(size: 12))
                                .foregroundStyle(running ? Theme.fg2 : Theme.fg3)
                                .lineLimit(1)
                        }
                        Spacer(minLength: 4)
                        Image(systemName: "chevron.down")
                            .font(.system(size: 11, weight: .semibold))
                            .foregroundStyle(Theme.fg4)
                            .rotationEffect(.degrees(open ? 180 : 0))
                    }
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                TaskButtons(session: session, task: task, pos: pos)
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 11)

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
        .card(fill: Theme.s2, border: task.awaiting > 0 ? Theme.warn.opacity(0.45) : nil)
    }

    @ViewBuilder
    private func quietBanner(quiet: Double, now: Double) -> some View {
        let stalled = quiet >= session.stallMs && !task.steps.contains { $0.tool == "run_agent_cli" }
        if stalled {
            HStack(spacing: 8) {
                Image(systemName: "exclamationmark.triangle.fill")
                Text("已经 \(Fmt.ago(quiet).replacingOccurrences(of: "前", with: ""))没有新进展了。可能在等一个很慢的命令，也可能卡住了。")
                    .fixedSize(horizontal: false, vertical: true)
                Spacer(minLength: 4)
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
            .font(.system(size: 12.5))
            .foregroundStyle(Theme.warn)
            .padding(.horizontal, 14)
            .padding(.vertical, 9)
            .background(Theme.warnSoft)
        } else {
            HStack(spacing: 8) {
                TypingDots(size: 4.5)
                Text("仍在工作…（上次动作 \(Fmt.ago(quiet))，进程正常\(now - task.lastBeat < 40_000 ? "" : "，等待响应")）")
            }
            .font(.system(size: 12.5))
            .foregroundStyle(Theme.fg3)
            .padding(.horizontal, 14)
            .padding(.vertical, 8)
            .overlay(alignment: .top) { Rectangle().fill(Theme.line).frame(height: 1) }
        }
    }

    @ViewBuilder
    private func details(now: Double) -> some View {
        VStack(alignment: .leading, spacing: 9) {
            if !task.input.isEmpty && task.input != task.title {
                Text("要求：\(task.input)")
                    .font(.system(size: 12.5))
                    .foregroundStyle(Theme.fg4)
                    .lineLimit(2)
            }
            if task.steps.isEmpty && task.text.isEmpty {
                Label(task.status == .queued ? "等前面的任务做完…" : task.status == .paused ? "还没开始。" : "正在看要做什么…",
                      systemImage: "hourglass")
                    .font(.system(size: 13))
                    .foregroundStyle(Theme.fg3)
            }
            ForEach(Array(task.steps.enumerated()), id: \.offset) { _, step in
                StepRow(step: step, now: now)
            }
            if running && !task.thinking.isEmpty && task.text.isEmpty {
                (Text("思考中").foregroundColor(Theme.fg3) + Text(" · " + String(task.thinking.suffix(200))))
                    .font(.system(size: 12.5))
                    .italic()
                    .foregroundStyle(Theme.fg4)
                    .lineLimit(2)
            }
            if running && !task.text.isEmpty {
                (Text(String(task.text.suffix(400))) + Text(" ▍").foregroundColor(Theme.accent))
                    .font(.system(size: 13))
                    .foregroundStyle(Theme.fg2)
                    .lineSpacing(3)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if !task.isLive, let err = task.error, !err.isEmpty {
                Text(err)
                    .font(.system(size: 13))
                    .foregroundStyle(Theme.danger)
                    .textSelection(.enabled)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(Theme.dangerSoft, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
            }
            if task.status == .completed, (task.error ?? "").isEmpty {
                let result = (task.summary ?? task.output ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
                if !result.isEmpty {
                    Text(result.replacingOccurrences(of: "[*`#]", with: "", options: .regularExpression))
                        .font(.system(size: 13))
                        .foregroundStyle(Theme.fg3)
                        .lineSpacing(3)
                        .lineLimit(4)
                        .textSelection(.enabled)
                }
            }
        }
    }
}

@MainActor
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
                    .font(.system(size: 13))
                    .foregroundStyle(Theme.fg2)
                    .lineSpacing(3)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Button {
                if step.output != nil { withAnimation(.easeOut(duration: 0.12)) { open.toggle() } }
            } label: {
                HStack(spacing: 8) {
                    Image(systemName: Self.icons[step.tool] ?? "wrench.and.screwdriver")
                        .font(.system(size: 12))
                        .foregroundStyle(tone)
                        .frame(width: 16)
                    Text(d.verb)
                        .font(.system(size: 13, weight: step.done ? .regular : .medium))
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
                            .font(.system(size: 9, weight: .semibold))
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
                        .font(Theme.mono)
                        .foregroundStyle(Theme.fg2)
                        .lineSpacing(2)
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(10)
                }
                .frame(height: min(CGFloat(out.components(separatedBy: "\n").count) * 15.5 + 22, 220))
                .background(Theme.s1, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
            }
        }
    }

    @ViewBuilder
    private var trailing: some View {
        if !step.done {
            HStack(spacing: 4) {
                if now - step.at > 3000 {
                    Text("\(Int((now - step.at) / 1000))s").font(.system(size: 11)).monospacedDigit()
                }
                ProgressView().controlSize(.mini)
            }
            .foregroundStyle(Theme.accent)
        } else if step.denied {
            Text("未批准").font(.system(size: 11)).foregroundStyle(Theme.danger)
        } else if step.error {
            Text("出错").font(.system(size: 11)).foregroundStyle(Theme.warn)
        } else {
            Image(systemName: "checkmark").font(.system(size: 11, weight: .semibold)).foregroundStyle(Theme.ok)
        }
    }
}

/// 输入框上方常驻的“工作条”：只要有任务在跑/排队/暂停，就一直显示；点开是任务托盘。
@MainActor
struct TaskDock: View {
    let session: BotSession
    @State private var open = false

    var body: some View {
        let running = session.running
        let queued = session.queued
        let paused = session.paused
        if !running.isEmpty || !queued.isEmpty || !paused.isEmpty {
            TimelineView(.periodic(from: .now, by: 1)) { ctx in
                bar(now: ctx.date.timeIntervalSince1970 * 1000, running: running, queued: queued, paused: paused)
            }
            .popover(isPresented: $open, arrowEdge: .top) {
                TaskTray(session: session)
            }
        }
    }

    private func bar(now: Double, running: [BotTask], queued: [BotTask], paused: [BotTask]) -> some View {
        let awaiting = running.filter { $0.awaiting > 0 }
        let lead = awaiting.first ?? running.max(by: { $0.lastEvent < $1.lastEvent })
        let quiet = lead.map { $0.awaiting == 0 ? now - $0.lastEvent : 0 } ?? 0
        let counts = [
            running.count > 1 ? "\(running.count) 个在跑" : nil,
            queued.isEmpty ? nil : "\(queued.count) 个排队",
            paused.isEmpty ? nil : "\(paused.count) 个暂停/待办",
        ].compactMap { $0 }.joined(separator: " · ")
        let alert = !awaiting.isEmpty
        return Button { open.toggle() } label: {
            HStack(spacing: 9) {
                if alert {
                    Circle().fill(Theme.warnDot).frame(width: 7, height: 7)
                } else if lead != nil {
                    Spinner(size: 12)
                } else if !queued.isEmpty {
                    ProgressView().controlSize(.mini)
                } else {
                    Image(systemName: "pause.fill").font(.system(size: 10)).foregroundStyle(Theme.warn)
                }
                Group {
                    if let lead {
                        Text(lead.title).fontWeight(.medium).foregroundColor(Theme.fg)
                            + Text(" · " + (quiet >= quietMs ? "仍在工作…（上次动作 \(Fmt.ago(quiet))）" : Fmt.nowDoing(lead))).foregroundColor(Theme.fg3)
                    } else if !queued.isEmpty {
                        Text("\(queued.count) 个任务排队，马上开始").foregroundColor(Theme.fg2)
                    } else {
                        Text("\(paused.count) 个任务已暂停或待办，随时可以继续").foregroundColor(Theme.fg2)
                    }
                }
                .lineLimit(1)
                Spacer(minLength: 6)
                if let lead {
                    Text("已做 \(lead.steps.count) 步 · \(Fmt.clock(lead.elapsed(now)))")
                        .font(.system(size: 11.5, design: .monospaced))
                        .foregroundStyle(Theme.fg3)
                    if !counts.isEmpty { Text("· " + counts).foregroundStyle(Theme.fg3).lineLimit(1) }
                }
                Image(systemName: "chevron.up")
                    .font(.system(size: 10, weight: .semibold))
                    .foregroundStyle(Theme.fg4)
            }
            .font(.system(size: 12.5))
            .padding(.horizontal, 12)
            .frame(height: 32)
            .background(alert ? Theme.warnSoft : Theme.s2, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
            .contentShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
        }
        .buttonStyle(.plain)
    }
}

@MainActor
struct TaskTray: View {
    let session: BotSession

    var body: some View {
        TimelineView(.periodic(from: .now, by: 1)) { ctx in
            let now = ctx.date.timeIntervalSince1970 * 1000
            let pos = session.queuePositions
            let running = session.running
            let done = session.tasks.values
                .filter { $0.status.terminal && $0.ended != nil && now - ($0.ended ?? 0) < 30 * 60_000 }
                .sorted { ($0.ended ?? 0) > ($1.ended ?? 0) }
                .prefix(3)
            ScrollView {
                VStack(alignment: .leading, spacing: 4) {
                    group("等你确认", running.filter { $0.awaiting > 0 }, now: now, pos: pos)
                    group("运行中", running.filter { $0.awaiting == 0 }, now: now, pos: pos)
                    group("排队中", session.queued, now: now, pos: pos)
                    group("已暂停 / 待办", session.paused, now: now, pos: pos)
                    group("最近完成", Array(done), now: now, pos: pos)
                }
                .padding(8)
            }
            .frame(width: 440)
            .frame(maxHeight: 420)
        }
    }

    @ViewBuilder
    private func group(_ label: String, _ list: [BotTask], now: Double, pos: [String: Int]) -> some View {
        if !list.isEmpty {
            Text(label)
                .font(.system(size: 11, weight: .medium))
                .foregroundStyle(Theme.fg4)
                .padding(.horizontal, 8)
                .padding(.top, 6)
            ForEach(list) { t in
                let line = TaskStatusLine(t, now: now, pos: pos[t.id])
                HStack(spacing: 10) {
                    StatusBadge(line: line, size: 15)
                    VStack(alignment: .leading, spacing: 1) {
                        Text(t.title).font(.system(size: 13, weight: .medium)).foregroundStyle(Theme.fg).lineLimit(1)
                        Text(line.text).font(.system(size: 11.5)).foregroundStyle(Theme.fg3).lineLimit(1)
                    }
                    Spacer(minLength: 6)
                    TaskButtons(session: session, task: t, pos: pos[t.id])
                }
                .padding(.horizontal, 8)
                .padding(.vertical, 6)
            }
        }
    }
}
