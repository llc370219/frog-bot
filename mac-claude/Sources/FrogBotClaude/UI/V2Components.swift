import AppKit
import SwiftUI

/// Frog Bot V2 原生总控面板（Popover / Sheet）：
/// 集成三大核心交互组件：1) 审查审批卡（支持永久加白名单）2) 就绪重放过验证画布 3) CLI 实时日志抽屉 + 跨服 Mesh 状态。
@MainActor
struct V2ControlDrawerView: View {
    let server: ServerEntry
    @State private var tab = 0
    @State private var selectedTaskID: String?
    @State private var captchaText = ""
    @State private var dragStartNorm: CGPoint?

    private var gw: GatewayClient { server.gateway }

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Label("Frog V2 总控中心", systemImage: "shield.lefthalf.filled")
                    .font(.system(size: 13, weight: .semibold))
                Spacer()
                Circle()
                    .fill(gw.connected ? Color.green : Color.orange)
                    .frame(width: 7, height: 7)
                Text(gw.nodeID.isEmpty ? (gw.connected ? "WS 在线" : "重连中") : gw.nodeID)
                    .font(.system(size: 11))
                    .foregroundStyle(.secondary)
                Button {
                    Task { await gw.refreshStatusREST(server) }
                } label: {
                    Image(systemName: "arrow.clockwise")
                }
                .buttonStyle(.plain)
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 10)

            Picker("", selection: $tab) {
                Text("审批 (\(gw.pendingApprovals.count))").tag(0)
                Text("过验证 (\(gw.verifySessions.count))").tag(1)
                Text("CLI 日志 (\(gw.cliTasks.count))").tag(2)
                Text("Mesh (\(gw.meshPeers.count))").tag(3)
            }
            .pickerStyle(.segmented)
            .padding(.horizontal, 12)
            .padding(.bottom, 8)

            Divider()

            Group {
                switch tab {
                case 0: approvalsPane
                case 1: verifyPane
                case 2: cliLogsPane
                default: meshPane
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .frame(width: 540, height: 460)
        .onAppear {
            Task { await gw.refreshStatusREST(server) }
        }
    }

    // MARK: 1. 审查审批卡片（批准一次 / 批准并加白名单 / 拒绝）

    private var approvalsPane: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 10) {
                if gw.pendingApprovals.isEmpty {
                    ContentUnavailableView("暂无待审批安全项", systemImage: "checkmark.shield", description: Text("所有密钥访问与敏感动作均处于安全状态"))
                        .padding(.top, 60)
                } else {
                    ForEach(gw.pendingApprovals) { card in
                        VStack(alignment: .leading, spacing: 8) {
                            HStack {
                                Text(card.action)
                                    .font(.system(size: 12, weight: .semibold))
                                if card.tainted {
                                    Text("含外部污点")
                                        .font(.system(size: 10, weight: .medium))
                                        .padding(.horizontal, 6).padding(.vertical, 2)
                                        .background(Color.orange.opacity(0.18), in: Capsule())
                                        .foregroundStyle(.orange)
                                }
                                Spacer()
                                Text("来源: \(card.triggerSource)")
                                    .font(.system(size: 11))
                                    .foregroundStyle(.secondary)
                            }
                            if let dom = card.targetDomain, !dom.isEmpty {
                                Text("目标域名：\(dom)")
                                    .font(.system(size: 11.5, design: .monospaced))
                                    .foregroundStyle(.secondary)
                            }
                            Text(card.summary)
                                .font(.system(size: 12))
                            Text(card.reason)
                                .font(.system(size: 11))
                                .foregroundStyle(.secondary)
                            HStack(spacing: 8) {
                                Button("批准一次") {
                                    gw.decideApproval(cardID: card.id, choice: "allow_once")
                                }
                                .buttonStyle(.borderedProminent)
                                if card.canWhitelist, let dom = card.whitelistDomain {
                                    Button("批准并加白名单 (\(dom))") {
                                        gw.decideApproval(cardID: card.id, choice: "allow_whitelist", whitelistDomain: dom)
                                    }
                                    .buttonStyle(.bordered)
                                }
                                Spacer()
                                Button("拒绝", role: .destructive) {
                                    gw.decideApproval(cardID: card.id, choice: "deny")
                                }
                                .buttonStyle(.bordered)
                            }
                            .controlSize(.small)
                        }
                        .padding(12)
                        .background(Color(nsColor: .controlBackgroundColor), in: RoundedRectangle(cornerRadius: 10))
                    }
                }
            }
            .padding(12)
        }
    }

    // MARK: 2. 远程过验证触控/键鼠画布（就绪重放 Ready-to-Verify）

    private var verifyPane: some View {
        VStack(spacing: 10) {
            if let activeID = gw.activeVerifyID {
                VStack(spacing: 8) {
                    GeometryReader { geo in
                        ZStack {
                            Color.black.opacity(0.88)
                            if let img = gw.latestFrame {
                                Image(nsImage: img)
                                    .resizable()
                                    .scaledToFit()
                                    .gesture(
                                        DragGesture(minimumDistance: 0)
                                            .onEnded { val in
                                                let w = max(1, geo.size.width)
                                                let h = max(1, geo.size.height)
                                                let x0 = Double(val.startLocation.x / w)
                                                let y0 = Double(val.startLocation.y / h)
                                                let x1 = Double(val.location.x / w)
                                                let y1 = Double(val.location.y / h)
                                                let dist = hypot(val.translation.width, val.translation.height)
                                                if dist < 6 {
                                                    gw.sendVerifyClick(sessionID: activeID, normX: x1, normY: y1)
                                                } else {
                                                    gw.sendVerifyDrag(sessionID: activeID, x0: x0, y0: y0, x1: x1, y1: y1)
                                                }
                                            }
                                    )
                            } else {
                                ProgressView("正在冷启浏览器并连接 CDP 画面推流…")
                                    .foregroundStyle(.white)
                            }
                        }
                        .clipShape(RoundedRectangle(cornerRadius: 8))
                    }
                    .frame(height: 270)

                    HStack(spacing: 8) {
                        TextField("输入图形/短信验证码…", text: $captchaText)
                            .textFieldStyle(.roundedBorder)
                            .onSubmit {
                                guard !captchaText.isEmpty else { return }
                                gw.sendVerifyText(sessionID: activeID, text: captchaText)
                                captchaText = ""
                            }
                        Button("发送文本") {
                            guard !captchaText.isEmpty else { return }
                            gw.sendVerifyText(sessionID: activeID, text: captchaText)
                            captchaText = ""
                        }
                        Button("完成验证") {
                            gw.completeVerify(sessionID: activeID)
                        }
                        .buttonStyle(.borderedProminent)
                        Button("关闭") {
                            gw.cancelVerify(sessionID: activeID)
                        }
                    }
                    .controlSize(.small)
                }
                .padding(12)
            } else if gw.verifySessions.isEmpty {
                ContentUnavailableView("暂无待验证任务", systemImage: "hand.tap", description: Text("后台遇到滑块/登录过期时会释放内存挂起，等你点击「开始验证」再实时重放"))
            } else {
                ScrollView {
                    VStack(spacing: 8) {
                        ForEach(gw.verifySessions) { sess in
                            HStack {
                                VStack(alignment: .leading, spacing: 3) {
                                    Text(sess.reason).font(.system(size: 12.5, weight: .medium))
                                    Text("\(sess.botID) · \(sess.url)").font(.system(size: 11)).foregroundStyle(.secondary).lineLimit(1)
                                }
                                Spacer()
                                Button("我现在有空，开始验证") {
                                    gw.startVerify(sessionID: sess.id)
                                }
                                .buttonStyle(.borderedProminent)
                                .controlSize(.small)
                            }
                            .padding(10)
                            .background(Color(nsColor: .controlBackgroundColor), in: RoundedRectangle(cornerRadius: 8))
                        }
                    }
                    .padding(12)
                }
            }
        }
    }

    // MARK: 3. 后台任务与 CLI 实时日志抽屉

    private var cliLogsPane: some View {
        HSplitView {
            List(gw.cliTasks, selection: $selectedTaskID) { t in
                VStack(alignment: .leading, spacing: 2) {
                    HStack {
                        Text("\(t.cli) · \(t.mode)").font(.system(size: 11.5, weight: .semibold))
                        Spacer()
                        Text(t.status).font(.system(size: 10)).foregroundStyle(.secondary)
                    }
                    Text(t.promptBrief ?? t.id).font(.system(size: 11)).foregroundStyle(.secondary).lineLimit(1)
                }
                .tag(t.id)
            }
            .frame(minWidth: 165, maxWidth: 210)
            .onChange(of: selectedTaskID) { _, tid in
                if let tid { gw.subscribeCLILog(taskID: tid) }
            }

            VStack(spacing: 0) {
                if let tid = selectedTaskID {
                    HStack {
                        Text(tid).font(.system(size: 11, design: .monospaced))
                        Spacer()
                        Button("紧急叫停 Cancel", role: .destructive) {
                            gw.cancelCLITask(taskID: tid)
                        }
                        .buttonStyle(.bordered)
                        .controlSize(.small)
                    }
                    .padding(8)
                    Divider()
                    ScrollView {
                        Text(gw.cliLogs[tid] ?? "正在加载日志流…")
                            .font(.system(size: 11, design: .monospaced))
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(8)
                    }
                } else {
                    Text("选择左侧 CLI 任务查看实时滚动日志").font(.system(size: 12)).foregroundStyle(.secondary)
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    // MARK: 4. FrogLink Mesh 跨服节点漫游

    private var meshPane: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 8) {
                if gw.meshPeers.isEmpty {
                    ContentUnavailableView("暂未配对跨服节点", systemImage: "network", description: Text("使用 frogctl pair 生成配对码连接家用 NAS 或其他服务器"))
                        .padding(.top, 60)
                } else {
                    ForEach(gw.meshPeers) { peer in
                        HStack {
                            Circle().fill(peer.online ? Color.green : Color.gray).frame(width: 8, height: 8)
                            VStack(alignment: .leading, spacing: 2) {
                                Text("\(peer.nodeID) \(peer.networkTag ?? "")").font(.system(size: 12.5, weight: .semibold))
                                Text("Bots: \((peer.bots ?? []).joined(separator: ", "))").font(.system(size: 11)).foregroundStyle(.secondary)
                            }
                            Spacer()
                            Text(peer.connectedVia ?? (peer.online ? "在线" : "离线"))
                                .font(.system(size: 11)).foregroundStyle(.secondary)
                        }
                        .padding(10)
                        .background(Color(nsColor: .controlBackgroundColor), in: RoundedRectangle(cornerRadius: 8))
                    }
                }
            }
            .padding(12)
        }
    }
}
