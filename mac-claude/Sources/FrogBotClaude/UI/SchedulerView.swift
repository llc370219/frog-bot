import AppKit
import SwiftUI

/// 自动调度器：当前 Bot 的定时计划。创建和大改在聊天里说（要审批），这里可以暂停、恢复、立即执行、删除。
@MainActor
struct SchedulerView: View {
    @Environment(AppModel.self) private var model
    let session: BotSession

    @State private var plans: [Schedule]?
    @State private var loadError: String?
    @State private var feedback: (text: String, error: Bool)?
    @State private var busy = false

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                Text("自动调度器")
                    .font(.system(size: 22, weight: .semibold))
                    .foregroundStyle(Theme.fg)
                Text("「\(session.profile.name)」的定时计划 · \(session.deviceName)")
                    .font(.system(size: 13))
                    .foregroundStyle(Theme.fg3)
                    .padding(.top, 4)

                if let loadError {
                    HStack(spacing: 8) {
                        Text(loadError)
                        Button("重试") { Task { await load() } }.buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
                    }
                    .font(.system(size: 12.5))
                    .foregroundStyle(Theme.danger)
                    .padding(.top, 18)
                }
                if let feedback {
                    Text(feedback.text)
                        .font(.system(size: 12.5))
                        .foregroundStyle(feedback.error ? Theme.danger : Theme.ok)
                        .padding(.top, 14)
                }

                if let plans {
                    if plans.isEmpty {
                        empty
                    } else {
                        FormGroup {
                            ForEach(Array(plans.enumerated()), id: \.element.id) { i, p in
                                if i > 0 { RowDivider() }
                                PlanRow(plan: p, busy: busy) { action in act(p, action) }
                            }
                        }
                        .padding(.top, 20)
                    }
                    Text("新建或修改计划：直接在聊天里告诉 \(session.profile.name)，例如「每天早上 9 点检查一次服务器」。创建前会先请你确认。")
                        .font(.system(size: 11.5))
                        .foregroundStyle(Theme.fg4)
                        .fixedSize(horizontal: false, vertical: true)
                        .padding(.top, 10)
                } else if loadError == nil {
                    ProgressView().controlSize(.small).padding(.top, 30)
                }
            }
            .frame(maxWidth: 680, alignment: .leading)
            .padding(.horizontal, 32)
            .padding(.top, 36)
            .padding(.bottom, 30)
            .frame(maxWidth: .infinity)
        }
        .background(Theme.canvas)
        .navigationTitle("自动调度器")
        .navigationSubtitle("\(session.deviceName) · \(session.profile.name)")
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Button { model.route = .chat } label: { Label("回到聊天", systemImage: "bubble.left") }
                    .help("回到聊天")
            }
        }
        .task(id: "\(session.ref.key)-\(session.scheduleRevision)") { await load() }
    }

    private var empty: some View {
        VStack(spacing: 8) {
            Image(systemName: "calendar.badge.clock")
                .font(.system(size: 26))
                .foregroundStyle(Theme.fg4)
            Text("还没有定时计划").font(.system(size: 14, weight: .medium)).foregroundStyle(Theme.fg2)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 40)
        .card(fill: Theme.s2)
        .padding(.top, 20)
    }

    private func load() async {
        do {
            plans = try await session.schedules()
            loadError = nil
        } catch {
            loadError = "读不到定时计划：\(error.localizedDescription)"
        }
    }

    private func act(_ p: Schedule, _ action: String) {
        if action == "delete" {
            let alert = NSAlert()
            alert.messageText = "删除定时计划「\(p.title)」？"
            alert.addButton(withTitle: "删除")
            alert.addButton(withTitle: "取消")
            alert.buttons.first?.hasDestructiveAction = true
            guard alert.runModal() == .alertFirstButtonReturn else { return }
        }
        let done = ["pause": "已暂停", "resume": "已恢复", "run": "已提交到任务队列", "delete": "已删除"][action] ?? "完成"
        busy = true
        feedback = nil
        Task {
            do {
                try await session.schedule(p.id, action)
                feedback = ("「\(p.title)」\(done)", false)
            } catch {
                feedback = ("操作失败：\(error.localizedDescription)", true)
            }
            await load()
            busy = false
        }
    }
}

@MainActor
private struct PlanRow: View {
    let plan: Schedule
    let busy: Bool
    let act: (String) -> Void

    var body: some View {
        HStack(spacing: 12) {
            VStack(alignment: .leading, spacing: 2) {
                Text(plan.title)
                    .font(.system(size: 13, weight: .medium))
                    .foregroundStyle(plan.enabled ? Theme.fg : Theme.fg3)
                Text(detail)
                    .font(.system(size: 11.5))
                    .foregroundStyle(Theme.fg3)
                    .lineLimit(2)
            }
            Spacer(minLength: 8)
            Button { act("run") } label: { Image(systemName: "bolt") }
                .buttonStyle(IconButtonStyle(size: 24))
                .help("立即执行一次")
            Button { act("delete") } label: { Image(systemName: "trash") }
                .buttonStyle(IconButtonStyle(size: 24))
                .help("删除")
            Toggle("", isOn: Binding(get: { plan.enabled }, set: { on in act(on ? "resume" : "pause") }))
                .toggleStyle(.switch)
                .controlSize(.mini)
                .labelsHidden()
                .tint(Theme.accent)
                .help(plan.enabled ? "暂停" : "恢复")
        }
        .disabled(busy)
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
    }

    private var detail: String {
        var parts: [String] = []
        switch plan.kind ?? "" {
        case "once": parts.append("单次" + (plan.at_local.map { " · \($0)" } ?? ""))
        case "daily": parts.append("每天 " + time)
        case "weekly":
            let names = ["日", "一", "二", "三", "四", "五", "六"]
            let days = (plan.weekdays ?? []).compactMap { $0 >= 0 && $0 < 7 ? "周" + names[$0] : nil }.joined(separator: "、")
            parts.append((days.isEmpty ? "每周" : days) + " " + time)
        case "interval": parts.append("按间隔重复")
        default: if let k = plan.kind, !k.isEmpty { parts.append(k) }
        }
        if !plan.enabled {
            parts.append("已暂停")
        } else if let next = plan.next_run_local, !next.isEmpty {
            parts.append("下次：\(next)")
        }
        if let st = plan.last_status, !st.isEmpty { parts.append("上次：\(statusText(st))") }
        return parts.joined(separator: " · ")
    }

    private var time: String {
        guard let h = plan.hour else { return "" }
        return String(format: "%02d:%02d", h, plan.minute ?? 0)
    }

    private func statusText(_ s: String) -> String {
        ["ok": "成功", "succeeded": "成功", "completed": "成功", "failed": "失败", "expired": "过期", "needs_verify": "待确认", "skipped": "跳过"][s] ?? s
    }
}
