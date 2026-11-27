import AppKit
import SwiftUI

// MARK: - macOS 原生 Coding 工作台（无气泡文档流：左侧绿条指令块 + 全宽平坦 Markdown + 垂直连线 Tool Timeline）

@MainActor
struct CodingWorkspaceView: View {
    @Environment(AppModel.self) private var model
    let server: ServerEntry
    let selection: CodingSelection

    var body: some View {
        if let chat = model.agySession(for: selection) {
            CodingChatCanvas(server: server, chat: chat)
                .id(selection.key)
        } else {
            CodingEmptyView(server: server)
        }
    }
}

@MainActor
struct CodingEmptyView: View {
    @Environment(AppModel.self) private var model
    let server: ServerEntry?
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        let store = server.map { model.agyStore(for: $0) }
        VStack(spacing: 14) {
            Image(systemName: "chevron.left.forwardslash.chevron.right")
                .font(.system(size: 28, weight: .semibold))
                .foregroundStyle(Theme.accent)
                .frame(width: 58, height: 58)
                .background(Theme.s2, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: 16, style: .continuous).strokeBorder(Theme.line, lineWidth: 0.7))

            Text(store?.activeProject?.name ?? "Coding 工作台")
                .font(.system(size: 20, weight: .semibold))
                .foregroundStyle(Theme.fg)

            if let path = store?.activeProject?.path, !path.isEmpty {
                Text(path)
                    .font(.system(size: 12, design: .monospaced))
                    .foregroundStyle(Theme.fg4)
                    .lineLimit(1)
                    .truncationMode(.middle)
            } else {
                Text("直连本机或远端主机上的官方 Coding CLI（Codex / Claude Code / Gemini / Grok / Antigravity），以无气泡文档工作台流高效编码。")
                    .font(.system(size: 13))
                    .foregroundStyle(Theme.fg3)
                    .multilineTextAlignment(.center)
                    .frame(maxWidth: 440)
            }

            if let error {
                Text(error)
                    .font(.system(size: 12))
                    .foregroundStyle(Theme.danger)
            }

            HStack(spacing: 10) {
                if let server, let store {
                    if store.activeProject != nil {
                        Button {
                            startQuickSession(server: server, store: store)
                        } label: {
                            HStack(spacing: 6) {
                                if busy { ProgressView().controlSize(.small) }
                                Image(systemName: "plus")
                                Text("新建 Coding 会话")
                            }
                        }
                        .buttonStyle(PillButtonStyle(kind: .primary))
                        .disabled(busy || !server.isOnline)
                    } else {
                        Button("新建或关联项目目录…") {
                            model.sheet = .newProject(server.id)
                        }
                        .buttonStyle(PillButtonStyle(kind: .primary))
                        .disabled(!server.isOnline)
                    }
                    Button("官方 CLI 登录…") {
                        model.sheet = .cliAuth(server.id)
                    }
                    .buttonStyle(PillButtonStyle(kind: .secondary))
                    .disabled(!server.isOnline)
                } else {
                    Button("扫描本机 Frog Bot") {
                        Task { await model.discoverLocalHost(manual: true) }
                    }
                    .buttonStyle(PillButtonStyle(kind: .primary))
                }
            }
            .padding(.top, 4)
        }
        .padding(36)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Theme.canvas)
        .navigationTitle("Coding 工作台")
        .navigationSubtitle(server?.name ?? "")
    }

    private func startQuickSession(server: ServerEntry, store: AgyStore) {
        busy = true
        error = nil
        Task {
            do {
                let s = try await store.createSession()
                model.selectCodingSession(server: server, summary: s)
            } catch {
                self.error = error.localizedDescription
            }
            busy = false
        }
    }
}

// MARK: - 无气泡 Coding 实时画布

@MainActor
private struct CodingChatCanvas: View {
    @Environment(AppModel.self) private var model
    let server: ServerEntry
    @Bindable var chat: AgyChatSession

    @State private var sending = false
    @State private var error: String?
    @State private var showCommands = false
    @State private var pinned = true

    private var store: AgyStore { model.agyStore(for: server) }

    var body: some View {
        VStack(spacing: 0) {
            topBar
            Rectangle().fill(Theme.line).frame(height: 0.5)

            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 18) {
                        if chat.ready && chat.messages.isEmpty && !chat.busy {
                            quickPromptsView
                        }
                        if !chat.ready {
                            HStack(spacing: 8) {
                                TypingDots(size: 5)
                                Text("正在同步会话…")
                                    .font(.system(size: 12.5))
                                    .foregroundStyle(Theme.fg3)
                            }
                            .frame(maxWidth: .infinity)
                            .padding(.top, 90)
                        }

                        ForEach(Array(chat.messages.enumerated()), id: \.element.id) { idx, msg in
                            CodingTurnBlock(message: msg, showDivider: idx > 0 && msg.role == "user")
                        }

                        ForEach(chat.pendingPermissions) { perm in
                            MacPermCallout(perm: perm) { optID in
                                Task {
                                    do { try await chat.decide(permID: perm.id, optionID: optID) }
                                    catch { self.error = error.localizedDescription }
                                }
                            }
                        }

                        if chat.busy {
                            liveStreamBlock
                        }

                        Color.clear.frame(height: 12).id("coding-bottom")
                    }
                    .frame(maxWidth: 780, alignment: .leading)
                    .padding(.horizontal, 28)
                    .padding(.top, 22)
                    .padding(.bottom, 18)
                    .frame(maxWidth: .infinity)
                }
                .onChange(of: chat.messages.count) { scrollToBottom(proxy, animated: true) }
                .onChange(of: chat.streaming.count) { if pinned { scrollToBottom(proxy, animated: false) } }
                .onChange(of: chat.liveTools.count) { if pinned { scrollToBottom(proxy, animated: true) } }
                .onChange(of: chat.pendingPermissions.count) { scrollToBottom(proxy, animated: true) }
                .onChange(of: chat.ready) { if chat.ready { scrollToBottom(proxy, animated: false) } }
            }

            Rectangle().fill(Theme.line).frame(height: 0.5)
            composerDock
        }
        .background(Theme.canvas)
        .navigationTitle(chat.title.isEmpty ? "新对话" : chat.title)
        .navigationSubtitle(store.activeProject?.name ?? server.name)
        .onAppear { chat.start() }
    }

    private func scrollToBottom(_ proxy: ScrollViewProxy, animated: Bool) {
        if animated {
            withAnimation(.easeOut(duration: 0.16)) { proxy.scrollTo("coding-bottom", anchor: .bottom) }
        } else {
            proxy.scrollTo("coding-bottom", anchor: .bottom)
        }
    }

    // MARK: 顶部工作台工具栏

    private var topBar: some View {
        HStack(spacing: 10) {
            HStack(spacing: 6) {
                Image(systemName: "folder")
                    .font(.system(size: 12, weight: .medium))
                    .foregroundStyle(Theme.accent)
                Text(store.activeProject?.name ?? (chat.cwd.isEmpty ? "Coding" : Fmt.base(chat.cwd)))
                    .font(.system(size: 13, weight: .semibold))
                    .foregroundStyle(Theme.fg)
                    .lineLimit(1)
                Text("/")
                    .foregroundStyle(Theme.fg4)
                Text(chat.title.isEmpty ? "新对话" : chat.title)
                    .font(.system(size: 13))
                    .foregroundStyle(Theme.fg2)
                    .lineLimit(1)
            }

            if let proj = store.activeProject {
                Button {
                    model.sheet = .projectFiles(server: server.id, project: proj)
                } label: {
                    Image(systemName: "sidebar.right")
                }
                .buttonStyle(IconButtonStyle(size: 24))
                .help("浏览项目文件")
            }

            Spacer(minLength: 8)

            // 权限模式切换
            if !chat.modes.isEmpty {
                Menu {
                    ForEach(chat.modes) { m in
                        Button {
                            Task { try? await chat.setConfig(mode: m.id) }
                        } label: {
                            if chat.mode == m.id { Label(m.name, systemImage: "checkmark") }
                            else { Text(m.name) }
                        }
                    }
                } label: {
                    HStack(spacing: 4) {
                        Image(systemName: "shield.lefthalf.filled")
                            .font(.system(size: 10.5))
                            .foregroundStyle(Theme.accent)
                        Text(chat.modes.first(where: { $0.id == chat.mode })?.name ?? chat.mode)
                            .font(.system(size: 11.5, weight: .medium))
                        Image(systemName: "chevron.down")
                            .font(.system(size: 8.5, weight: .semibold))
                            .foregroundStyle(Theme.fg4)
                    }
                    .foregroundStyle(Theme.fg2)
                    .padding(.horizontal, 9)
                    .frame(height: 24)
                    .background(Theme.s2, in: Capsule())
                    .overlay(Capsule().strokeBorder(Theme.line, lineWidth: 0.6))
                    .contentShape(Capsule())
                }
                .menuStyle(.button)
                .buttonStyle(.plain)
                .menuIndicator(.hidden)
                .fixedSize()
                .help("执行权限模式")
            }

            // 模型切换
            if !chat.models.isEmpty {
                Menu {
                    ForEach(chat.models) { m in
                        Button {
                            Task { try? await chat.setConfig(model: m.id) }
                        } label: {
                            if chat.model == m.id { Label(m.name, systemImage: "checkmark") }
                            else { Text(m.name) }
                        }
                    }
                } label: {
                    HStack(spacing: 4) {
                        Text(chat.models.first(where: { $0.id == chat.model })?.name ?? (chat.model.isEmpty ? "默认模型" : chat.model))
                            .font(.system(size: 11.5, design: .monospaced))
                            .lineLimit(1)
                        Image(systemName: "chevron.down")
                            .font(.system(size: 8.5, weight: .semibold))
                            .foregroundStyle(Theme.fg4)
                    }
                    .foregroundStyle(Theme.fg2)
                    .padding(.horizontal, 9)
                    .frame(height: 24)
                    .background(Theme.s2, in: Capsule())
                    .overlay(Capsule().strokeBorder(Theme.line, lineWidth: 0.6))
                    .contentShape(Capsule())
                }
                .menuStyle(.button)
                .buttonStyle(.plain)
                .menuIndicator(.hidden)
                .fixedSize()
                .help("切换 Coding 模型")
            }

            if chat.usedTokens > 0 {
                Text("\(max(1, chat.usedTokens / 1000))k" + (chat.maxTokens > 0 ? " / \(chat.maxTokens / 1000)k" : ""))
                    .font(.system(size: 11, design: .monospaced))
                    .foregroundStyle(Theme.fg4)
                    .padding(.horizontal, 8)
                    .frame(height: 22)
                    .background(Theme.s2, in: Capsule())
                    .help("当前会话上下文 Token 用量")
            }

            Button {
                createSiblingSession()
            } label: {
                Image(systemName: "square.and.pencil")
            }
            .buttonStyle(IconButtonStyle(size: 26))
            .help("在此项目新建会话")
        }
        .padding(.horizontal, 18)
        .frame(height: 44)
        .background(Theme.s1)
    }

    private var quickPromptsView: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(spacing: 10) {
                Image(systemName: "terminal")
                    .font(.system(size: 18, weight: .semibold))
                    .foregroundStyle(Theme.accent)
                    .frame(width: 36, height: 36)
                    .background(Theme.accentSoft, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                VStack(alignment: .leading, spacing: 2) {
                    Text(store.activeProject?.name ?? "Coding 工作台")
                        .font(.system(size: 17, weight: .semibold))
                        .foregroundStyle(Theme.fg)
                    if !chat.cwd.isEmpty {
                        Text(chat.cwd)
                            .font(.system(size: 11.5, design: .monospaced))
                            .foregroundStyle(Theme.fg4)
                    }
                }
            }
            HStack(spacing: 8) {
                ForEach(["梳理当前项目结构", "运行测试并排查问题", "规划新功能改动方案"], id: \.self) { prompt in
                    Button(prompt) {
                        send(text: prompt)
                    }
                    .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
                }
            }
        }
        .padding(.top, 44)
    }

    private var liveStreamBlock: some View {
        VStack(alignment: .leading, spacing: 12) {
            if !chat.thinking.isEmpty {
                ThinkingBlock(text: chat.thinking, live: true, startedAt: chat.thinkStart, endedAt: chat.thinkEnd)
            }
            if !chat.liveTools.isEmpty {
                MacToolTimeline(tools: chat.liveTools)
            }
            if !chat.streaming.isEmpty {
                MarkdownView(text: chat.streaming, streaming: true, fontSize: 14.5)
                    .frame(maxWidth: .infinity, alignment: .leading)
            } else if chat.thinking.isEmpty || chat.thinkEnd > 0 {
                HStack(spacing: 8) {
                    Spinner(size: 13, tint: Theme.accent)
                    Text("Agent 正在执行…")
                        .font(.system(size: 12.5))
                        .foregroundStyle(Theme.fg3)
                }
                .padding(.vertical, 2)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .rise()
    }

    // MARK: 底部输入栏

    private var composerDock: some View {
        VStack(spacing: 8) {
            if let error {
                HStack(spacing: 6) {
                    Image(systemName: "exclamationmark.circle.fill")
                    Text(error).lineLimit(2)
                    Spacer()
                    Button("关闭") { self.error = nil }
                        .buttonStyle(.plain)
                        .foregroundStyle(Theme.fg3)
                }
                .font(.system(size: 12))
                .foregroundStyle(Theme.danger)
                .frame(maxWidth: 780)
            }

            if !chat.queue.isEmpty {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 6) {
                        ForEach(chat.queue) { q in
                            HStack(spacing: 5) {
                                Image(systemName: q.behavior == "steer" ? "bolt.fill" : "clock")
                                    .font(.system(size: 10))
                                    .foregroundStyle(q.behavior == "steer" ? Theme.warn : Theme.accent)
                                Text(q.behavior == "steer" ? "插话" : "排队")
                                    .font(.system(size: 10.5, weight: .medium))
                                    .foregroundStyle(Theme.fg4)
                                Text(q.text)
                                    .font(.system(size: 11.5))
                                    .foregroundStyle(Theme.fg2)
                                    .lineLimit(1)
                                    .frame(maxWidth: 220, alignment: .leading)
                                Button {
                                    Task { try? await chat.cancelQueued(q.id) }
                                } label: {
                                    Image(systemName: "xmark")
                                        .font(.system(size: 9.5, weight: .bold))
                                        .foregroundStyle(Theme.fg4)
                                }
                                .buttonStyle(.plain)
                            }
                            .padding(.horizontal, 9)
                            .padding(.vertical, 4)
                            .background(Theme.s2, in: Capsule())
                            .overlay(Capsule().strokeBorder(Theme.line, lineWidth: 0.6))
                        }
                    }
                }
                .frame(maxWidth: 780)
            }

            if showCommands && !chat.commands.isEmpty {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 6) {
                        ForEach(chat.commands) { cmd in
                            Button {
                                chat.draft = "/\(cmd.name)" + (cmd.input_hint == nil ? "" : " ")
                                showCommands = false
                            } label: {
                                HStack(spacing: 5) {
                                    Text("/\(cmd.name)")
                                        .font(.system(size: 11.5, weight: .semibold, design: .monospaced))
                                        .foregroundStyle(Theme.accent)
                                    if let d = cmd.description, !d.isEmpty {
                                        Text(d)
                                            .font(.system(size: 11))
                                            .foregroundStyle(Theme.fg3)
                                            .lineLimit(1)
                                            .frame(maxWidth: 160, alignment: .leading)
                                    }
                                }
                                .padding(.horizontal, 9)
                                .padding(.vertical, 5)
                                .background(Theme.s2, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
                                .overlay(RoundedRectangle(cornerRadius: 8, style: .continuous).strokeBorder(Theme.line, lineWidth: 0.6))
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }
                .frame(maxWidth: 780)
            }

            HStack(alignment: .bottom, spacing: 8) {
                if !chat.commands.isEmpty {
                    Button {
                        showCommands.toggle()
                    } label: {
                        Image(systemName: "slash.circle")
                            .font(.system(size: 16))
                    }
                    .buttonStyle(IconButtonStyle(size: 28, tint: showCommands ? Theme.accent : Theme.fg3))
                    .help("斜杠命令")
                }

                TextField(
                    chat.busy ? "执行中：Enter 排队追问，点闪电立即插话…" : "输入 Coding 指令…（Return 发送）",
                    text: $chat.draft,
                    axis: .vertical
                )
                .textFieldStyle(.plain)
                .font(.system(size: 14))
                .lineLimit(1...6)
                .padding(.vertical, 5)
                .onSubmit {
                    send(text: nil, behavior: chat.busy ? "follow_up" : nil)
                }

                let trimmed = chat.draft.trimmingCharacters(in: .whitespacesAndNewlines)
                if chat.busy && trimmed.isEmpty {
                    Button {
                        Task { try? await chat.stopPrompt() }
                    } label: {
                        Image(systemName: "stop.fill")
                            .font(.system(size: 11))
                            .foregroundStyle(Theme.canvas)
                            .frame(width: 28, height: 28)
                            .background(Theme.fg, in: Circle())
                    }
                    .buttonStyle(.plain)
                    .help("停止当前执行")
                } else {
                    if chat.busy && !trimmed.isEmpty {
                        Button {
                            send(text: nil, behavior: "steer")
                        } label: {
                            Image(systemName: "bolt.fill")
                                .font(.system(size: 12, weight: .bold))
                                .foregroundStyle(Theme.warn)
                                .frame(width: 28, height: 28)
                                .background(Theme.warnSoft, in: Circle())
                        }
                        .buttonStyle(.plain)
                        .disabled(sending)
                        .help("打断当前步骤并立刻执行（Steer）")
                    }
                    Button {
                        send(text: nil, behavior: chat.busy ? "follow_up" : nil)
                    } label: {
                        Group {
                            if sending {
                                ProgressView().controlSize(.mini)
                            } else if chat.busy {
                                Image(systemName: "clock")
                                    .font(.system(size: 12, weight: .bold))
                            } else {
                                Image(systemName: "arrow.up")
                                    .font(.system(size: 13, weight: .bold))
                            }
                        }
                        .foregroundStyle(Theme.onAccent)
                        .frame(width: 28, height: 28)
                        .background(!trimmed.isEmpty && !sending ? Theme.accent : Theme.fg4.opacity(0.4), in: Circle())
                    }
                    .buttonStyle(.plain)
                    .disabled(trimmed.isEmpty || sending)
                    .help(chat.busy ? "排队追问" : "发送指令")
                }
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 7)
            .background(Theme.composer, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 14, style: .continuous).strokeBorder(Theme.line2, lineWidth: 0.8))
            .frame(maxWidth: 780)
        }
        .padding(.horizontal, 24)
        .padding(.vertical, 12)
        .frame(maxWidth: .infinity)
        .background(Theme.canvas)
    }

    private func send(text overrideText: String?, behavior: String? = nil) {
        let raw = (overrideText ?? chat.draft).trimmingCharacters(in: .whitespacesAndNewlines)
        guard !raw.isEmpty, !sending else { return }
        let keep = chat.draft
        if overrideText == nil { chat.draft = "" }
        sending = true
        error = nil
        Task {
            do {
                try await chat.send(raw, behavior: behavior)
            } catch {
                if overrideText == nil { chat.draft = keep }
                self.error = error.localizedDescription
            }
            sending = false
        }
    }

    private func createSiblingSession() {
        Task {
            do {
                let s = try await store.createSession(mode: chat.mode, model: chat.model, agent: chat.agent)
                model.selectCodingSession(server: server, summary: s)
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

// MARK: - 无气泡消息渲染（左侧青蛙绿竖条指令块 + 全宽平坦 Markdown）

@MainActor
private struct CodingTurnBlock: View {
    let message: AgyMessage
    let showDivider: Bool

    var body: some View {
        if message.role == "user" {
            VStack(alignment: .leading, spacing: 0) {
                if showDivider {
                    Rectangle()
                        .fill(Theme.line)
                        .frame(height: 0.5)
                        .padding(.bottom, 16)
                }
                HStack(alignment: .top, spacing: 10) {
                    RoundedRectangle(cornerRadius: 1.5, style: .continuous)
                        .fill(Theme.accent)
                        .frame(width: 2.5)
                    Text(">")
                        .font(.system(size: 13, weight: .bold, design: .monospaced))
                        .foregroundStyle(Theme.accent)
                        .padding(.top, 1)
                    Text(message.text)
                        .font(.system(size: 14.5, weight: .medium))
                        .foregroundStyle(Theme.fg)
                        .lineSpacing(3)
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    Button { copy(message.text) } label: {
                        Image(systemName: "doc.on.doc")
                    }
                    .buttonStyle(IconButtonStyle(size: 22))
                    .opacity(0.6)
                    .help("复制指令")
                }
                .fixedSize(horizontal: false, vertical: true)
                .padding(.vertical, 10)
                .padding(.horizontal, 12)
                .background(Theme.s2.opacity(0.65), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .rise()
        } else {
            VStack(alignment: .leading, spacing: 12) {
                if let t = message.thinking, !t.isEmpty {
                    ThinkingBlock(text: t, ms: message.thinkingMs)
                }
                if !message.tools.isEmpty {
                    MacToolTimeline(tools: message.tools)
                }
                if !message.text.isEmpty {
                    VStack(alignment: .leading, spacing: 6) {
                        MarkdownView(text: message.text, fontSize: 14.5)
                            .frame(maxWidth: .infinity, alignment: .leading)
                        HStack(spacing: 4) {
                            Button { copy(message.text) } label: {
                                Label("复制", systemImage: "doc.on.doc")
                                    .font(.system(size: 11))
                            }
                            .buttonStyle(PillButtonStyle(kind: .quiet, compact: true))
                        }
                        .opacity(0.65)
                    }
                }
                if let err = message.error, !err.isEmpty {
                    HStack(alignment: .top, spacing: 8) {
                        RoundedRectangle(cornerRadius: 1.5)
                            .fill(Theme.danger)
                            .frame(width: 2.5)
                        Text(err)
                            .font(.system(size: 12.5))
                            .foregroundStyle(Theme.danger)
                            .textSelection(.enabled)
                    }
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(10)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(Theme.dangerSoft, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .rise()
        }
    }
}

// MARK: - 垂直连线工具调用时间线

@MainActor
private struct MacToolTimeline: View {
    let tools: [AgyToolCall]
    @State private var expanded: Set<String> = []

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            ForEach(tools) { tc in
                let running = tc.status == "in_progress" || tc.status == "pending"
                let failed = tc.status == "failed" || tc.status == "error"
                let hasExtra = !tc.detail.isEmpty || !tc.output.isEmpty
                let isOpen = expanded.contains(tc.id)

                VStack(alignment: .leading, spacing: 5) {
                    Button {
                        guard hasExtra else { return }
                        withAnimation(.easeInOut(duration: 0.14)) {
                            if isOpen { expanded.remove(tc.id) } else { expanded.insert(tc.id) }
                        }
                    } label: {
                        HStack(spacing: 7) {
                            if running {
                                Spinner(size: 11, tint: Theme.accent)
                            } else {
                                Image(systemName: failed ? "xmark.circle.fill" : "checkmark.circle.fill")
                                    .font(.system(size: 11.5))
                                    .foregroundStyle(failed ? Theme.danger : Theme.ok)
                            }
                            Text(tc.title.isEmpty ? tc.name : tc.title)
                                .font(.system(size: 12, design: .monospaced))
                                .foregroundStyle(failed ? Theme.danger : Theme.fg2)
                                .lineLimit(1)
                            Spacer(minLength: 4)
                            if hasExtra {
                                Image(systemName: isOpen ? "chevron.up" : "chevron.down")
                                    .font(.system(size: 9.5, weight: .semibold))
                                    .foregroundStyle(Theme.fg4)
                            }
                        }
                        .padding(.horizontal, 6)
                        .padding(.vertical, 3)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)

                    if isOpen && hasExtra {
                        VStack(alignment: .leading, spacing: 5) {
                            if !tc.detail.isEmpty {
                                Text(tc.detail)
                                    .font(.system(size: 11.5, weight: .medium, design: .monospaced))
                                    .foregroundStyle(Theme.fg)
                                    .textSelection(.enabled)
                            }
                            if !tc.output.isEmpty {
                                ScrollView {
                                    Text(tc.output)
                                        .font(.system(size: 11, design: .monospaced))
                                        .foregroundStyle(Theme.fg3)
                                        .textSelection(.enabled)
                                        .frame(maxWidth: .infinity, alignment: .leading)
                                }
                                .frame(maxHeight: 160)
                            }
                        }
                        .padding(9)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(Theme.code, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
                        .overlay(RoundedRectangle(cornerRadius: 8, style: .continuous).strokeBorder(Theme.line, lineWidth: 0.6))
                        .padding(.leading, 6)
                        .padding(.bottom, 4)
                    }
                }
            }
        }
        .padding(.leading, 10)
        .overlay(alignment: .leading) {
            Rectangle()
                .fill(Theme.line2)
                .frame(width: 1.5)
                .padding(.vertical, 4)
        }
    }
}

// MARK: - 内联权限确认块

@MainActor
private struct MacPermCallout: View {
    let perm: AgyPermission
    let onDecide: (String) -> Void

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            RoundedRectangle(cornerRadius: 1.5, style: .continuous)
                .fill(Theme.warn)
                .frame(width: 3)
            VStack(alignment: .leading, spacing: 10) {
                Label("请求操作确认：\(perm.title)", systemImage: "exclamationmark.shield.fill")
                    .font(.system(size: 13.5, weight: .semibold))
                    .foregroundStyle(Theme.warn)
                if !perm.detail.isEmpty {
                    Text(perm.detail)
                        .font(.system(size: 12, design: .monospaced))
                        .foregroundStyle(Theme.fg2)
                        .textSelection(.enabled)
                        .padding(8)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(Theme.code, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
                }
                HStack(spacing: 8) {
                    ForEach(perm.options) { opt in
                        let reject = opt.optionId.contains("reject") || opt.kind.contains("reject")
                        Button(opt.name) { onDecide(opt.optionId) }
                            .buttonStyle(PillButtonStyle(kind: reject ? .danger : .primary, compact: true))
                    }
                }
            }
        }
        .fixedSize(horizontal: false, vertical: true)
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.warnSoft.opacity(0.65), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 10, style: .continuous).strokeBorder(Theme.warn.opacity(0.3), lineWidth: 0.7))
        .rise()
    }
}

// MARK: - 新建 / 关联项目目录 Sheet

@MainActor
struct MacNewProjectSheet: View {
    @Environment(\.dismiss) private var dismiss
    let store: AgyStore

    @State private var linkExisting = false
    @State private var name = ""
    @State private var path = ""
    @State private var saving = false
    @State private var error: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("添加 Coding 项目目录")
                .font(.system(size: 15, weight: .semibold))
            Picker("", selection: $linkExisting) {
                Text("新建空项目目录").tag(false)
                Text("关联已有目录").tag(true)
            }
            .pickerStyle(.segmented)
            .labelsHidden()

            Form {
                if !linkExisting {
                    TextField("文件夹名称", text: $name, prompt: Text("例如 my-webapp"))
                } else {
                    TextField("主机绝对路径", text: $path, prompt: Text("例如 /Users/ll/frog-bot"))
                    TextField("显示名称（可选）", text: $name, prompt: Text("留空自动使用目录名"))
                }
            }
            .formStyle(.columns)

            if !linkExisting, let root = store.status?.projects_root, !root.isEmpty {
                Text("将创建于：\(root)/\(name.isEmpty ? "<名称>" : name)")
                    .font(.system(size: 11.5, design: .monospaced))
                    .foregroundStyle(Theme.fg4)
            }
            if let error {
                Text(error).font(.system(size: 12)).foregroundStyle(Theme.danger)
            }

            HStack {
                Spacer()
                Button("取消") { dismiss() }.keyboardShortcut(.cancelAction)
                Button(saving ? "处理中…" : "确认") { submit() }
                    .keyboardShortcut(.defaultAction)
                    .disabled(saving || (linkExisting ? path.trimmingCharacters(in: .whitespaces).isEmpty : name.trimmingCharacters(in: .whitespaces).isEmpty))
            }
        }
        .padding(22)
        .frame(width: 440)
    }

    private func submit() {
        saving = true
        error = nil
        Task {
            do {
                _ = try await store.createProject(
                    name: name.trimmingCharacters(in: .whitespaces),
                    path: linkExisting ? path.trimmingCharacters(in: .whitespaces) : ""
                )
                dismiss()
            } catch {
                self.error = error.localizedDescription
            }
            saving = false
        }
    }
}

// MARK: - 项目文件浏览器 Sheet

@MainActor
struct MacProjectFilesSheet: View {
    @Environment(\.dismiss) private var dismiss
    let server: ServerEntry
    let project: AgyProject

    @State private var subDir = ""
    @State private var entries: [AgyFileEntry] = []
    @State private var error: String?

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                Image(systemName: "folder.fill").foregroundStyle(Theme.accent)
                Text(subDir.isEmpty ? project.name : "\(project.name)/\(subDir)")
                    .font(.system(size: 13.5, weight: .semibold))
                    .lineLimit(1)
                Spacer()
                if !subDir.isEmpty {
                    Button("上一级") {
                        var parts = subDir.split(separator: "/").map(String.init)
                        _ = parts.popLast()
                        subDir = parts.joined(separator: "/")
                    }
                    .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
                }
                Button("关闭") { dismiss() }
                    .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 12)
            Rectangle().fill(Theme.line).frame(height: 0.5)

            List {
                if let error {
                    Text(error).foregroundStyle(Theme.danger)
                } else if entries.isEmpty {
                    Text("目录为空").foregroundStyle(Theme.fg4)
                }
                ForEach(entries) { e in
                    let rel = subDir.isEmpty ? e.name : "\(subDir)/\(e.name)"
                    if e.is_dir {
                        Button {
                            subDir = rel
                        } label: {
                            HStack(spacing: 8) {
                                Image(systemName: "folder.fill").foregroundStyle(Theme.accent)
                                Text(e.name + "/")
                                Spacer()
                                Image(systemName: "chevron.right").font(.caption2).foregroundStyle(Theme.fg4)
                            }
                        }
                        .buttonStyle(.plain)
                    } else {
                        HStack(spacing: 8) {
                            Image(systemName: "doc.text").foregroundStyle(Theme.fg3)
                            Text(e.name)
                            Spacer()
                            if let sz = e.size {
                                Text(Fmt.size(sz))
                                    .font(.system(size: 11.5, design: .monospaced))
                                    .foregroundStyle(Theme.fg4)
                            }
                        }
                    }
                }
            }
            .listStyle(.inset)
        }
        .frame(width: 520, height: 460)
        .task(id: subDir) { await load() }
    }

    private func load() async {
        error = nil
        var q: [URLQueryItem] = []
        if !subDir.isEmpty { q.append(URLQueryItem(name: "dir", value: subDir)) }
        do {
            entries = try await server.client.get("/api/agy/projects/\(project.id)/files", query: q)
        } catch where error.isCancellation {
            return
        } catch {
            self.error = error.localizedDescription
        }
    }
}
