import SwiftUI

/// 自动调度器：当前 Bot 的定时计划。创建和大改在聊天里说（要审批），这里可以暂停、恢复、立即执行、删除。
struct SchedulerView: View {
    let session: BotSession

    @State private var plans: [Schedule]?
    @State private var loadError: String?
    @State private var feedback: (text: String, error: Bool)?
    @State private var busy = false
    @State private var deleting: Schedule?

    var body: some View {
        List {
            if let loadError {
                Section {
                    Text(loadError).foregroundStyle(Theme.danger)
                    Button("重试") { Task { await load() } }
                }
            }
            if let feedback {
                Section { Text(feedback.text).foregroundStyle(feedback.error ? Theme.danger : Theme.ok) }
            }
            if let plans {
                Section {
                    if plans.isEmpty {
                        ContentUnavailableView("还没有定时计划", systemImage: "calendar.badge.clock")
                    }
                    ForEach(plans) { p in
                        PlanRow(plan: p, busy: busy) { act(p, $0) }
                            .swipeActions(edge: .trailing) {
                                Button(role: .destructive) { deleting = p } label: { Label("删除", systemImage: "trash") }
                                Button { act(p, "run") } label: { Label("立即执行", systemImage: "bolt.fill") }
                                    .tint(Theme.accent)
                            }
                    }
                } footer: {
                    Text("新建或修改计划：直接在聊天里告诉 \(session.profile.name)，例如「每天早上 9 点检查一次服务器」。创建前会先请你确认。左滑一条计划可以立即执行或删除。")
                }
            } else if loadError == nil {
                HStack { ProgressView(); Text("正在读取…").foregroundStyle(Theme.fg3) }
            }
        }
        .listStyle(.insetGrouped)
        .navigationTitle("自动调度器")
        .navigationBarTitleDisplayMode(.inline)
        .refreshable { await load() }
        .task(id: "\(session.ref.key)-\(session.scheduleRevision)") { await load() }
        .confirmationDialog("删除定时计划「\(deleting?.title ?? "")」？", isPresented: Binding(get: { deleting != nil }, set: { if !$0 { deleting = nil } }),
                            titleVisibility: .visible) {
            Button("删除", role: .destructive) {
                if let p = deleting { act(p, "delete") }
                deleting = nil
            }
        }
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
        let done = ["pause": "已暂停", "resume": "已恢复", "run": "已提交到任务队列", "delete": "已删除"][action] ?? "完成"
        busy = true
        feedback = nil
        Haptics.tap()
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

private struct PlanRow: View {
    let plan: Schedule
    let busy: Bool
    let act: (String) -> Void

    var body: some View {
        Toggle(isOn: Binding(get: { plan.enabled }, set: { on in act(on ? "resume" : "pause") })) {
            VStack(alignment: .leading, spacing: 3) {
                Text(plan.title)
                    .font(.system(size: 16, weight: .medium))
                    .foregroundStyle(plan.enabled ? Theme.fg : Theme.fg3)
                Text(detail)
                    .font(.footnote)
                    .foregroundStyle(Theme.fg3)
                    .lineLimit(3)
            }
        }
        .disabled(busy)
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
