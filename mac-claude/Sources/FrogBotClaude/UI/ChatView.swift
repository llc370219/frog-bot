import AppKit
import SwiftUI
import UniformTypeIdentifiers

@MainActor
struct ChatView: View {
    @Environment(AppModel.self) private var model
    let session: BotSession

    @State private var error: String?
    @State private var dropping = false
    @State private var trayOpen = false

    var body: some View {
        VStack(spacing: 0) {
            if !session.connected && (session.ready || !session.server.isOnline) {
                reconnectBanner
            }
            timeline
            bottom
        }
        .background(Theme.canvas)
        .overlay { if dropping { dropOverlay } }
        .onDrop(of: [.fileURL], isTargeted: $dropping) { providers in
            accept(providers)
            return true
        }
        .navigationTitle(session.profile.name)
        .navigationSubtitle(subtitle)
        .toolbar { toolbar }
    }

    private var subtitle: String {
        session.server.config.isInsecure ? "\(session.deviceName) · HTTP 未加密" : session.deviceName
    }

    // MARK: 时间线

    private var timeline: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 16) {
                    if !session.ready {
                        HStack { Spacer(); TypingDots(); Spacer() }.padding(.top, 140)
                    } else if session.items.isEmpty {
                        WelcomeView(session: session) { text in quickSend(text) }
                    }
                    ForEach(session.items, id: \.self) { item in
                        row(item)
                    }
                    if session.busy { LiveTurn(session: session) }
                    Color.clear.frame(height: 1).id("bottom")
                }
                .frame(maxWidth: Theme.contentWidth)
                .padding(.horizontal, 28)
                .padding(.top, 22)
                .padding(.bottom, 16)
                .frame(maxWidth: .infinity)
            }
            .scrollContentBackground(.hidden)
            .defaultScrollAnchor(.bottom)
            .onChange(of: session.items.count) { scrollDown(proxy, animated: true) }
            .onChange(of: session.streaming) { scrollDown(proxy, animated: false) }
            .onChange(of: session.busy) { if session.busy { scrollDown(proxy, animated: true) } }
            .onChange(of: session.ready) { scrollDown(proxy, animated: false) }
            .onAppear { scrollDown(proxy, animated: false) }
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
                    .padding(.leading, 37)
            }
        case .approval:
            if let a = session.approvals[item.id] {
                ApprovalCardView(session: session, approval: a)
                    .padding(.leading, 37)
            }
        }
    }

    // MARK: 底部

    private var bottom: some View {
        VStack(spacing: 8) {
            TaskDock(session: session)
            if let error {
                HStack(spacing: 8) {
                    Image(systemName: "exclamationmark.circle.fill")
                    Text(error).lineLimit(2)
                    Spacer(minLength: 4)
                    Button("知道了") { self.error = nil }.buttonStyle(.plain).foregroundStyle(Theme.fg3)
                }
                .font(.system(size: 12))
                .foregroundStyle(Theme.danger)
            }
            Composer(session: session) { error = $0 }
        }
        .frame(maxWidth: Theme.contentWidth)
        .padding(.horizontal, 28)
        .padding(.top, 8)
        .padding(.bottom, 14)
        .frame(maxWidth: .infinity)
    }

    private var reconnectBanner: some View {
        HStack(spacing: 8) {
            ProgressView().controlSize(.mini)
            Text("正在重新连接「\(session.deviceName)」。已开始的任务会继续运行，连上后自动补齐消息。")
        }
        .font(.system(size: 12))
        .foregroundStyle(Theme.fg3)
        .frame(maxWidth: .infinity)
        .padding(.vertical, 6)
        .background(Theme.s2)
        .overlay(alignment: .bottom) { Rectangle().fill(Theme.line).frame(height: 0.5) }
    }

    private var dropOverlay: some View {
        RoundedRectangle(cornerRadius: 14, style: .continuous)
            .strokeBorder(Theme.accent, style: StrokeStyle(lineWidth: 1.5, dash: [6, 5]))
            .background(Theme.accentSoft, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            .overlay {
                Label("松开即可添加附件", systemImage: "arrow.down.doc")
                    .font(.system(size: 14, weight: .medium))
                    .foregroundStyle(Theme.accent)
            }
            .padding(12)
            .allowsHitTesting(false)
    }

    @ToolbarContentBuilder
    private var toolbar: some ToolbarContent {
        ToolbarItemGroup(placement: .primaryAction) {
            ModelMenu(session: session)
            if session.busy {
                Button { Task { try? await session.stopReply() } } label: {
                    Label("停止回复", systemImage: "stop.circle")
                }
                .keyboardShortcut(.escape, modifiers: [])
                .help("停止正在写的回复（Esc）")
            }
            if session.pendingApprovals > 0 {
                Button { scrollToApproval() } label: {
                    HStack(spacing: 6) {
                        Circle().fill(Theme.warnDot).frame(width: 6, height: 6)
                        Text("\(session.pendingApprovals) 个操作等你确认")
                    }
                    .font(.system(size: 12, weight: .medium))
                    .foregroundStyle(Theme.warn)
                    .padding(.horizontal, 10)
                    .frame(height: 24)
                    .background(Theme.warnSoft, in: Capsule())
                }
                .buttonStyle(.plain)
            }
            Button { trayOpen.toggle() } label: {
                Label("任务", systemImage: "list.bullet")
            }
            .help("任务")
            .popover(isPresented: $trayOpen, arrowEdge: .bottom) {
                TaskTray(session: session)
            }
            Button {
                if let url = session.server.config.baseURL { NSWorkspace.shared.open(url) }
            } label: {
                Label("在浏览器中打开", systemImage: "safari")
            }
            .help("在浏览器中打开这台服务器")
        }
    }

    // MARK: 动作

    private func scrollToApproval() {
        trayOpen = true
    }

    private func quickSend(_ text: String) {
        Task {
            do { try await session.send(text, files: []) } catch { self.error = error.localizedDescription }
        }
    }

    private func accept(_ providers: [NSItemProvider]) {
        for p in providers {
            _ = p.loadObject(ofClass: URL.self) { url, _ in
                guard let url, url.isFileURL else { return }
                Task { @MainActor in session.attach([url]) }
            }
        }
    }
}

/// 标题旁边的模型按钮：给这个 Bot 单独选聊天模型（渠道整台设备共用，模型每个 Bot 各自选）。
@MainActor
struct ModelMenu: View {
    @Environment(AppModel.self) private var model
    let session: BotSession

    @State private var settings: APISettings?
    @State private var cli = ""
    @State private var saving = false

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
                                Toggle(isOn: Binding(
                                    get: { settings.chatChannelID == ch.id && settings.chatModel == m },
                                    set: { _ in pick(ch.id, m) })) {
                                    Text(m)
                                }
                            }
                        }
                    }
                }
                if !cli.isEmpty {
                    Divider()
                    Text("后台任务由 \(cliName) 执行")
                }
            } else {
                Text("正在读取模型…")
            }
            Divider()
            Button("编辑 Bot 和任务模型…") { model.sheet = .editBot(session.ref) }
        } label: {
            HStack(spacing: 4) {
                if saving { ProgressView().controlSize(.mini) }
                Text(current).lineLimit(1)
            }
        }
        .menuStyle(.button)
        .buttonStyle(.bordered)
        .controlSize(.small)
        .fixedSize()
        .help("选择这个 Bot 的聊天模型")
        .task(id: session.ref) { await load() }
        .onChange(of: session.info?.chat_model) { Task { await load() } }
    }

    private var cliName: String { ["codex": "Codex CLI", "claude": "Claude Code"][cli] ?? cli }

    private func load() async {
        settings = try? await session.loadSettings()
        cli = (try? await session.loadConnections())?.cli ?? ""
    }

    private func pick(_ channel: String, _ m: String) {
        saving = true
        Task {
            do {
                try await session.setChatModel(channel: channel, model: m)
                await load()
            } catch {
                let alert = NSAlert()
                alert.messageText = "换模型失败"
                alert.informativeText = error.localizedDescription
                alert.runModal()
            }
            saving = false
        }
    }
}

/// 聊天层正在进行的这一轮：思考、流式回复、或者“正在查看 xx…”
@MainActor
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
                    .font(.system(size: 12))
                    .foregroundStyle(Theme.fg3)
            }
        }
    }
}

/// 新 Bot 的空对话
@MainActor
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
            BotAvatar(client: session.client, profile: session.profile, size: 56)
            Text("你好，我是 \(session.profile.name)")
                .font(.system(size: 22, weight: .semibold))
                .foregroundStyle(Theme.fg)
                .padding(.top, 16)
            Text(intro)
                .font(.system(size: 13))
                .foregroundStyle(Theme.fg3)
                .multilineTextAlignment(.center)
                .lineSpacing(3)
                .frame(maxWidth: 420)
                .padding(.top, 6)
            LazyVGrid(columns: [GridItem(.flexible(), spacing: 10), GridItem(.flexible(), spacing: 10)], spacing: 10) {
                ForEach(ideas, id: \.1) { idea in
                    IdeaCard(symbol: idea.0, title: idea.1, text: idea.2) { onPick(idea.2) }
                }
            }
            .frame(maxWidth: 520)
            .padding(.top, 26)
        }
        .frame(maxWidth: .infinity)
        .padding(.top, 70)
    }

    private var intro: String {
        if let ins = session.profile.instructions?.trimmingCharacters(in: .whitespacesAndNewlines), !ins.isEmpty {
            return ins.count > 90 ? String(ins.prefix(88)) + "…" : ins
        }
        return "能在「\(session.deviceName)」的工作区里写代码、跑命令、管服务器。复杂的事我会拆成任务在后台做。"
    }
}

@MainActor
private struct IdeaCard: View {
    let symbol: String
    let title: String
    let text: String
    let action: () -> Void
    @State private var hover = false

    var body: some View {
        Button(action: action) {
            VStack(alignment: .leading, spacing: 4) {
                HStack(spacing: 7) {
                    Image(systemName: symbol).font(.system(size: 13)).foregroundStyle(Theme.fg3)
                    Text(title).font(.system(size: 13, weight: .semibold)).foregroundStyle(Theme.fg)
                }
                Text(text)
                    .font(.system(size: 12))
                    .foregroundStyle(Theme.fg3)
                    .multilineTextAlignment(.leading)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .padding(12)
            .frame(maxWidth: .infinity, minHeight: 78, alignment: .topLeading)
            .background(hover ? Theme.s3 : Theme.s2, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
            .contentShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
        }
        .buttonStyle(.plain)
        .onHover { hover = $0 }
    }
}
