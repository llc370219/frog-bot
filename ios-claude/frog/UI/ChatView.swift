import SwiftUI

/// 和一个 Bot 的对话：时间线（消息、任务卡、审批卡）+ 底部工作条和输入框。
struct ChatView: View {
    @Environment(AppModel.self) private var model
    let session: BotSession
    @Binding var path: [Nav]

    @State private var error: String?
    @State private var trayOpen = false
    @State private var atBottom = true

    var body: some View {
        timeline
            .safeAreaInset(edge: .top, spacing: 0) {
                if !session.connected && (session.ready || !session.server.isOnline) { reconnectBanner }
            }
            .safeAreaInset(edge: .bottom, spacing: 0) { bottom }
            .background(Theme.canvas)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { toolbar }
            .toolbarBackground(.visible, for: .navigationBar)
            .sheet(isPresented: $trayOpen) {
                TaskTray(session: session)
                    .presentationDetents([.medium, .large])
                    .presentationDragIndicator(.visible)
            }
            .onAppear {
                if DebugFlags.openTray { DebugFlags.openTray = false; trayOpen = true }
                model.selection = session.ref
                model.route = .chat
                model.chatOnScreen = true
            }
            .onDisappear { model.chatOnScreen = false }
    }

    // MARK: 时间线

    private var timeline: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 14) {
                    if !session.ready {
                        HStack { Spacer(); TypingDots(); Spacer() }.padding(.top, 120)
                    } else if session.items.isEmpty {
                        WelcomeView(session: session) { quickSend($0) }
                    }
                    ForEach(session.items, id: \.self) { item in
                        row(item)
                    }
                    if session.busy { LiveTurn(session: session) }
                    Color.clear.frame(height: 1).id("bottom")
                }
                .frame(maxWidth: Theme.contentWidth)
                .padding(.horizontal, 14)
                .padding(.top, 12)
                .padding(.bottom, 10)
                .frame(maxWidth: .infinity)
            }
            .scrollDismissesKeyboard(.interactively)
            .defaultScrollAnchor(.bottom)
            .onChange(of: session.items.count) { scrollDown(proxy, animated: true) }
            .onChange(of: session.streaming) { if atBottom { scrollDown(proxy, animated: false) } }
            .onChange(of: session.busy) { if session.busy { scrollDown(proxy, animated: true) } }
            .onChange(of: session.ready) { scrollDown(proxy, animated: false) }
            .onScrollGeometryChange(for: Bool.self) { g in
                g.contentOffset.y + g.containerSize.height >= g.contentSize.height - 80
            } action: { _, near in atBottom = near }
            .overlay(alignment: .bottomTrailing) {
                if !atBottom && session.ready {
                    Button { scrollDown(proxy, animated: true) } label: {
                        Image(systemName: "arrow.down")
                            .font(.system(size: 14, weight: .semibold))
                            .foregroundStyle(Theme.fg2)
                            .frame(width: 36, height: 36)
                            .background(.regularMaterial, in: Circle())
                            .overlay(Circle().strokeBorder(Theme.line, lineWidth: 0.5))
                    }
                    .padding(.trailing, 14)
                    .padding(.bottom, 8)
                    .transition(.opacity)
                }
            }
        }
    }

    private func scrollDown(_ proxy: ScrollViewProxy, animated: Bool) {
        DispatchQueue.main.async {
            if animated {
                withAnimation(.easeOut(duration: 0.2)) { proxy.scrollTo("bottom", anchor: .bottom) }
            } else {
                proxy.scrollTo("bottom", anchor: .bottom)
            }
        }
    }

    @ViewBuilder
    private func row(_ item: TimelineItem) -> some View {
        switch item.kind {
        case .msg:
            if let m = session.msgs[item.id] {
                MessageRow(session: session, msg: m, canRegenerate: m.id == session.regenerateID) { error = $0 }
            }
        case .task:
            if let t = session.tasks[item.id] {
                TaskCard(session: session, task: t, pos: session.queuePositions[t.id])
                    .padding(.leading, 38)
            }
        case .approval:
            if let a = session.approvals[item.id] {
                ApprovalCardView(session: session, approval: a)
                    .padding(.leading, 38)
            }
        }
    }

    // MARK: 底部

    private var bottom: some View {
        VStack(spacing: 8) {
            TaskDock(session: session, trayOpen: $trayOpen)
            if let error {
                HStack(spacing: 8) {
                    Image(systemName: "exclamationmark.circle.fill")
                    Text(error).lineLimit(3)
                    Spacer(minLength: 4)
                    Button("知道了") { self.error = nil }.buttonStyle(.plain).foregroundStyle(Theme.fg3)
                }
                .font(.system(size: 13))
                .foregroundStyle(Theme.danger)
            }
            Composer(session: session) { error = $0 }
        }
        .frame(maxWidth: Theme.contentWidth)
        .padding(.horizontal, 12)
        .padding(.top, 8)
        .padding(.bottom, 8)
        .frame(maxWidth: .infinity)
        .background(.bar)
    }

    private var reconnectBanner: some View {
        HStack(spacing: 8) {
            ProgressView().controlSize(.mini)
            Text(session.server.isOnline ? "正在重新连接，连上后自动补齐消息" : "「\(session.server.name)」暂时连不上，已开始的任务会继续运行")
                .lineLimit(1)
        }
        .font(.system(size: 12.5))
        .foregroundStyle(Theme.fg3)
        .frame(maxWidth: .infinity)
        .padding(.vertical, 6)
        .background(.bar)
    }

    @ToolbarContentBuilder
    private var toolbar: some ToolbarContent {
        ToolbarItem(placement: .principal) {
            ModelTitleMenu(session: session) { path.append(.bot(session.ref)) }
        }
        ToolbarItemGroup(placement: .topBarTrailing) {
            Button { trayOpen = true } label: {
                ZStack(alignment: .topTrailing) {
                    Image(systemName: "list.bullet.rectangle")
                    if session.pendingApprovals > 0 {
                        Circle().fill(Theme.warnDot).frame(width: 9, height: 9).offset(x: 4, y: -3)
                    } else if session.working {
                        Circle().fill(Theme.accent).frame(width: 7, height: 7).offset(x: 3, y: -2)
                    }
                }
            }
            .accessibilityLabel(session.pendingApprovals > 0 ? "任务（\(session.pendingApprovals) 个等你确认）" : "任务")
            Menu {
                Button { path.append(.scheduler(session.ref)) } label: { Label("自动调度器", systemImage: "calendar.badge.clock") }
                Button { path.append(.bot(session.ref)) } label: { Label("Bot 设置", systemImage: "slider.horizontal.3") }
                if session.working {
                    Button(role: .destructive) { Task { try? await session.stopAll() } } label: {
                        Label("停止全部任务", systemImage: "stop.circle")
                    }
                }
                if let url = session.server.config.baseURL {
                    Link(destination: url) { Label("在浏览器中打开", systemImage: "safari") }
                }
            } label: {
                Image(systemName: "ellipsis.circle")
            }
        }
    }

    private func quickSend(_ text: String) {
        Task {
            do { try await session.send(text, files: []) } catch { self.error = error.localizedDescription }
        }
    }
}

/// 导航栏中间：Bot 名字 + 当前聊天模型，点开直接换模型（渠道整台设备共用，模型每个 Bot 各自选）。
struct ModelTitleMenu: View {
    let session: BotSession
    var openSettings: () -> Void

    @State private var settings: APISettings?
    @State private var cli = ""
    @State private var saving = false
    @State private var error: String?

    private var current: String {
        let m = session.info?.chat_model ?? settings?.chatModel ?? ""
        return m.isEmpty ? "未选模型" : m
    }

    var body: some View {
        Menu {
            if let settings {
                let channels = settings.channels.filter(\.enabled)
                if channels.isEmpty {
                    Text("还没有可用的 API 渠道")
                } else {
                    ForEach(channels) { ch in
                        Section(ch.name) {
                            ForEach(ch.visibleModels, id: \.self) { m in
                                Button { pick(ch.id, m) } label: {
                                    if settings.chatChannelID == ch.id && settings.chatModel == m {
                                        Label(m, systemImage: "checkmark")
                                    } else {
                                        Text(m)
                                    }
                                }
                            }
                        }
                    }
                }
                if !cli.isEmpty {
                    Section { Text("后台任务由 \(cliName) 执行") }
                }
            } else {
                Text("正在读取模型…")
            }
            Divider()
            Button(action: openSettings) { Label("Bot 设置和任务模型…", systemImage: "slider.horizontal.3") }
        } label: {
            VStack(spacing: 1) {
                Text(session.profile.name)
                    .font(.system(size: 16, weight: .semibold))
                    .foregroundStyle(Theme.fg)
                    .lineLimit(1)
                HStack(spacing: 3) {
                    if saving { ProgressView().controlSize(.mini) }
                    if session.server.config.isInsecure {
                        Image(systemName: "lock.open.fill").font(.system(size: 9)).foregroundStyle(Theme.warn)
                    }
                    Text(subtitle).lineLimit(1)
                    Image(systemName: "chevron.down").font(.system(size: 8, weight: .bold))
                }
                .font(.system(size: 11.5))
                .foregroundStyle(error == nil ? Theme.fg3 : Theme.danger)
            }
            .frame(maxWidth: 240)
        }
        .task(id: session.ref) { await load() }
        .onChange(of: session.info?.chat_model) { Task { await load() } }
    }

    private var subtitle: String {
        if let error { return error }
        return session.isLocalDevice ? current : "\(session.deviceName) · \(current)"
    }

    private var cliName: String { ["codex": "Codex CLI", "claude": "Claude Code"][cli] ?? cli }

    private func load() async {
        settings = try? await session.loadSettings()
        cli = (try? await session.loadConnections())?.cli ?? ""
    }

    private func pick(_ channel: String, _ m: String) {
        saving = true
        error = nil
        Task {
            do {
                try await session.setChatModel(channel: channel, model: m)
                await load()
                Haptics.success()
            } catch {
                self.error = "换模型失败"
                Haptics.error()
            }
            saving = false
        }
    }
}

/// 聊天层正在进行的这一轮：思考、流式回复、或者“正在查看 xx…”
struct LiveTurn: View {
    let session: BotSession

    var body: some View {
        AssistantRow(session: session) {
            if !session.thinking.isEmpty {
                ThinkingBlock(text: session.thinking, live: true, startedAt: session.thinkStart, endedAt: session.thinkEnd)
            }
            if !session.streaming.isEmpty {
                AssistantBubbles(text: session.streaming, streaming: true)
            } else if session.thinking.isEmpty || session.thinkEnd > 0 {
                AssistantBubbles(text: "", streaming: true)
            }
        } extras: {
            if session.streaming.isEmpty && !session.toolLabel.isEmpty {
                Text(Fmt.liveToolLabel(session.toolLabel))
                    .font(.system(size: 13))
                    .foregroundStyle(Theme.fg3)
            }
        }
    }
}

/// 新 Bot 的空对话
struct WelcomeView: View {
    let session: BotSession
    var onPick: (String) -> Void

    private let ideas: [(String, String, String)] = [
        ("folder", "整理工作区", "看看工作区里有什么，写一份 notes.md 总结"),
        ("terminal", "写代码并运行", "写一个 hello.go 并运行它"),
        ("checklist", "先记个待办", "帮我记个待办：明天整理 README，先别开始"),
        ("alarm", "定个提醒", "帮我创建一个明天上午 9 点的单次提醒：整理 README"),
    ]

    var body: some View {
        VStack(spacing: 0) {
            BotAvatar(client: session.client, profile: session.profile, size: 64)
            Text("你好，我是 \(session.profile.name)")
                .font(.system(size: 22, weight: .semibold))
                .foregroundStyle(Theme.fg)
                .padding(.top, 16)
            Text(intro)
                .font(.system(size: 14.5))
                .foregroundStyle(Theme.fg3)
                .multilineTextAlignment(.center)
                .lineSpacing(3)
                .padding(.horizontal, 12)
                .padding(.top, 6)
            LazyVGrid(columns: [GridItem(.flexible(), spacing: 10), GridItem(.flexible(), spacing: 10)], spacing: 10) {
                ForEach(ideas, id: \.1) { idea in
                    Button { onPick(idea.2) } label: {
                        VStack(alignment: .leading, spacing: 5) {
                            HStack(spacing: 7) {
                                Image(systemName: idea.0).font(.system(size: 14)).foregroundStyle(Theme.accent)
                                Text(idea.1).font(.system(size: 14.5, weight: .semibold)).foregroundStyle(Theme.fg)
                            }
                            Text(idea.2)
                                .font(.system(size: 13))
                                .foregroundStyle(Theme.fg3)
                                .multilineTextAlignment(.leading)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                        .padding(12)
                        .frame(maxWidth: .infinity, minHeight: 92, alignment: .topLeading)
                        .background(Theme.s2, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
                    }
                    .buttonStyle(.plain)
                }
            }
            .padding(.top, 26)
        }
        .frame(maxWidth: 520)
        .frame(maxWidth: .infinity)
        .padding(.top, 50)
    }

    private var intro: String {
        if let ins = session.profile.instructions?.trimmingCharacters(in: .whitespacesAndNewlines), !ins.isEmpty {
            return ins.count > 90 ? String(ins.prefix(88)) + "…" : ins
        }
        return "能在「\(session.deviceName)」的工作区里写代码、跑命令、管服务器。复杂的事我会拆成任务在后台做。"
    }
}
