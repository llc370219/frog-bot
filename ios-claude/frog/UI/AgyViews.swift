import SwiftUI

/// Coding 工作区主视图：与 Grok Bot 首页统一的无分割线纯白/纯黑画布排版，支持底部搜索栏实时过滤会话。
struct CodingWorkspaceView: View {
    @Environment(AppModel.self) private var model
    let server: ServerEntry
    @Binding var path: [Nav]
    var searchText = ""
    var createSessionTick = 0
    var createProjectTick = 0

    @State private var store: AgyStore
    @State private var showingNewProject = false
    @State private var showingFiles = false

    init(server: ServerEntry, path: Binding<[Nav]>, searchText: String = "", createSessionTick: Int = 0, createProjectTick: Int = 0) {
        self.server = server
        self._path = path
        self.searchText = searchText
        self.createSessionTick = createSessionTick
        self.createProjectTick = createProjectTick
        self._store = State(initialValue: AgyStore(server: server))
    }

    private var filteredSessions: [AgySessionSummary] {
        let q = searchText.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        guard !q.isEmpty else { return store.sessions }
        return store.sessions.filter { s in
            s.title.lowercased().contains(q)
                || s.mode.lowercased().contains(q)
                || (s.model?.lowercased().contains(q) == true)
        }
    }

    var body: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 0) {
                if server.status == .needsLogin {
                    Button { model.sheet = .login(server.id) } label: {
                        Label("「\(server.name)」授权已过期，点这里重新连接", systemImage: "person.badge.key.fill")
                            .font(.system(size: 14, weight: .medium))
                            .foregroundStyle(Theme.warn)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(.horizontal, 16).padding(.vertical, 12)
                            .background(Theme.warnSoft, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
                            .padding(.horizontal, 20).padding(.bottom, 10)
                    }
                    .buttonStyle(.plain)
                } else if case .offline(let why) = server.status {
                    VStack(alignment: .leading, spacing: 3) {
                        Label("连不上「\(server.name)」", systemImage: "wifi.exclamationmark")
                            .font(.system(size: 14, weight: .semibold)).foregroundStyle(Theme.danger)
                        Text(why).font(.system(size: 12.5)).foregroundStyle(Theme.fg3).lineLimit(2)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, 16).padding(.vertical, 12)
                    .background(Theme.dangerSoft, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
                    .padding(.horizontal, 20).padding(.bottom, 10)
                }

                if let st = store.status, (!st.installed || (st.error ?? "").isEmpty == false) {
                    Label(st.error ?? "当前服务器未配置可用的 ACP 后端（如 agy acp），请检查登录或环境", systemImage: "exclamationmark.triangle.fill")
                        .font(.footnote)
                        .foregroundStyle(Theme.warn)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal, 16).padding(.vertical, 12)
                        .background(Theme.warnSoft, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
                        .padding(.horizontal, 20).padding(.bottom, 10)
                }

                // 顶部项目胶囊条
                VStack(alignment: .leading, spacing: 8) {
                    ScrollView(.horizontal, showsIndicators: false) {
                        HStack(spacing: 8) {
                            ForEach(store.projects) { p in
                                let active = p.id == store.activeProject?.id
                                Button {
                                    Haptics.tap()
                                    store.selectProject(p.id)
                                    Task { await store.refreshSessions() }
                                } label: {
                                    HStack(spacing: 5) {
                                        Image(systemName: active ? "folder.fill" : "folder")
                                            .font(.system(size: 12, weight: .semibold))
                                        Text(p.name).lineLimit(1)
                                        if !p.managed {
                                            Text("外").font(.system(size: 10, weight: .medium))
                                                .padding(.horizontal, 4).padding(.vertical, 1)
                                                .background(active ? Theme.onAccent.opacity(0.2) : Theme.s3, in: Capsule())
                                        }
                                    }
                                    .font(.system(size: 14, weight: active ? .semibold : .medium))
                                    .foregroundStyle(active ? Theme.onAccent : Theme.fg2)
                                    .padding(.horizontal, 13).padding(.vertical, 8)
                                    .background(active ? Theme.accent : Theme.s2, in: Capsule())
                                }
                                .buttonStyle(.plain)
                                .contextMenu {
                                    Button {
                                        store.selectProject(p.id)
                                        showingFiles = true
                                    } label: { Label("浏览文件", systemImage: "doc.text.magnifyingglass") }
                                    if store.projects.count > 1 {
                                        Button(role: .destructive) {
                                            Task { try? await store.deleteProject(p, deleteFiles: false) }
                                        } label: { Label("从列表移除", systemImage: "trash") }
                                    }
                                }
                            }

                            Button { showingNewProject = true } label: {
                                Label("项目", systemImage: "folder.badge.plus")
                                    .font(.system(size: 13.5, weight: .medium))
                                    .foregroundStyle(Theme.accent)
                                    .padding(.horizontal, 12).padding(.vertical, 8)
                                    .background(Theme.accentSoft, in: Capsule())
                            }
                            .buttonStyle(.plain)
                        }
                        .padding(.horizontal, 20)
                    }

                    if let proj = store.activeProject {
                        HStack(spacing: 8) {
                            Text(proj.path)
                                .font(.system(size: 12, design: .monospaced))
                                .foregroundStyle(Theme.fg4)
                                .lineLimit(1)
                                .truncationMode(.middle)
                            Spacer(minLength: 4)
                            if let st = store.status, st.installed {
                                Text("ACP · agy")
                                    .font(.system(size: 10.5, weight: .medium, design: .monospaced))
                                    .foregroundStyle(Theme.fg3)
                                    .padding(.horizontal, 6).padding(.vertical, 2)
                                    .background(Theme.s2, in: Capsule())
                            }
                            Button { showingFiles = true } label: {
                                Label("文件", systemImage: "doc.text")
                                    .font(.system(size: 12.5, weight: .medium))
                                    .foregroundStyle(Theme.fg2)
                            }
                            .buttonStyle(.plain)
                        }
                        .padding(.horizontal, 20)
                    }
                }
                .padding(.bottom, 10)

                if let err = store.error {
                    Text(err)
                        .font(.footnote)
                        .foregroundStyle(Theme.danger)
                        .padding(.horizontal, 20)
                        .padding(.bottom, 8)
                }

                let list = filteredSessions
                if list.isEmpty && !store.loading {
                    VStack(spacing: 14) {
                        Text(searchText.isEmpty ? "「\(store.activeProject?.name ?? "当前项目")」还没有对话" : "没有匹配的 Coding 对话")
                            .font(.system(size: 16, weight: .medium))
                            .foregroundStyle(Theme.fg3)
                        if searchText.isEmpty {
                            Button { startNewConversation() } label: {
                                Label("新建 Coding 对话", systemImage: "plus.message.fill")
                            }
                            .buttonStyle(PillButtonStyle(kind: .primary, compact: true))
                            .disabled(store.activeProject == nil || store.status?.installed == false)
                        }
                    }
                    .frame(maxWidth: .infinity)
                    .padding(.top, 64)
                } else {
                    ForEach(list) { s in
                        Button {
                            path.append(.agyChat(server: server.id, sessionID: s.id, projectID: s.project_id, title: s.title))
                        } label: {
                            HStack(spacing: 15) {
                                ZStack(alignment: .bottomTrailing) {
                                    RoundedRectangle(cornerRadius: 20, style: .continuous)
                                        .fill(Theme.accentSoft)
                                        .frame(width: 52, height: 52)
                                        .overlay {
                                            Image(systemName: "chevron.left.forwardslash.chevron.right")
                                                .font(.system(size: 19, weight: .semibold))
                                                .foregroundStyle(Theme.accent)
                                        }

                                    if s.pending_permissions > 0 {
                                        Circle()
                                            .fill(Theme.warnDot)
                                            .frame(width: 16, height: 16)
                                            .overlay(Circle().strokeBorder(Theme.canvas, lineWidth: 2))
                                            .offset(x: 1, y: 1)
                                    } else if s.busy {
                                        Circle()
                                            .fill(Theme.accent)
                                            .frame(width: 15, height: 15)
                                            .overlay(Circle().strokeBorder(Theme.canvas, lineWidth: 2.5))
                                            .offset(x: 1, y: 1)
                                    }
                                }

                                VStack(alignment: .leading, spacing: 5) {
                                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                                        Text(s.title.isEmpty ? "新对话" : s.title)
                                            .font(.system(size: 17, weight: .semibold))
                                            .foregroundStyle(Theme.fg)
                                            .lineLimit(1)
                                        Spacer(minLength: 4)
                                        Text(Fmt.isoListTime(s.updated_at))
                                            .font(.system(size: 13.5))
                                            .foregroundStyle(Theme.fg4)
                                    }
                                    HStack(spacing: 6) {
                                        Text(s.mode + ((s.model?.isEmpty == false) ? " · \(s.model!)" : ""))
                                            .font(.system(size: 15))
                                            .foregroundStyle(s.pending_permissions > 0 ? Theme.warn : Theme.fg3)
                                            .lineLimit(1)
                                        Spacer(minLength: 4)
                                    }
                                }
                            }
                            .padding(.horizontal, 20)
                            .padding(.vertical, 13)
                        }
                        .buttonStyle(RowPressStyle())
                        .contextMenu {
                            Button(role: .destructive) {
                                Task { try? await store.deleteSession(s.id) }
                            } label: { Label("删除对话", systemImage: "trash") }
                        }
                    }
                }
            }
            .padding(.top, 2)
            .padding(.bottom, 16)
        }
        .scrollDismissesKeyboard(.interactively)
        .refreshable { await store.loadAll() }
        .sheet(isPresented: $showingNewProject) {
            AgyNewProjectSheet(store: store)
        }
        .sheet(isPresented: $showingFiles) {
            if let proj = store.activeProject {
                AgyFilesSheet(server: server, project: proj)
            }
        }
        .task { await store.loadAll() }
        .onChange(of: server.status) { _, st in
            if st == .online { Task { await store.loadAll() } }
        }
        .onChange(of: createSessionTick) { _, _ in
            startNewConversation()
        }
        .onChange(of: createProjectTick) { _, _ in
            showingNewProject = true
        }
    }

    private func startNewConversation() {
        guard store.activeProject != nil, store.status?.installed != false else { return }
        Haptics.tap()
        Task {
            do {
                let created = try await store.createSession()
                path.append(.agyChat(server: server.id, sessionID: created.id, projectID: created.project_id, title: created.title))
            } catch {
                store.error = error.localizedDescription
            }
        }
    }
}

/// 兼容历史深链跳转的 Coding 页面容器
struct AgyHomeView: View {
    let server: ServerEntry
    @Binding var path: [Nav]
    @State private var createSessionTick = 0

    var body: some View {
        CodingWorkspaceView(server: server, path: $path, createSessionTick: createSessionTick)
            .navigationTitle("Coding")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button { createSessionTick += 1 } label: { Image(systemName: "square.and.pencil") }
                        .accessibilityLabel("新建对话")
                }
            }
    }
}

/// 直连单个 agy 会话的聊天页面
struct AgyChatView: View {
    let server: ServerEntry
    @Binding var path: [Nav]

    @State private var chat: AgyChatSession
    @State private var error: String?
    @State private var sending = false
    @State private var atBottom = true
    @FocusState private var focused: Bool

    init(server: ServerEntry, sessionID: String, projectID: String, title: String, path: Binding<[Nav]>) {
        self.server = server
        self._path = path
        self._chat = State(initialValue: AgyChatSession(server: server, sessionID: sessionID, projectID: projectID, initialTitle: title))
    }

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 14) {
                    if !chat.ready {
                        HStack { Spacer(); TypingDots(); Spacer() }.padding(.top, 100)
                    } else if chat.messages.isEmpty && !chat.busy {
                        emptyPromptView
                    }

                    ForEach(chat.messages) { m in
                        messageRow(m)
                    }

                    ForEach(chat.pendingPermissions) { perm in
                        permCard(perm)
                    }

                    if chat.busy {
                        liveTurnView
                    }

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
            .onChange(of: chat.messages.count) { scrollDown(proxy, animated: true) }
            .onChange(of: chat.streaming) { if atBottom { scrollDown(proxy, animated: false) } }
            .onChange(of: chat.busy) { if chat.busy { scrollDown(proxy, animated: true) } }
            .onScrollGeometryChange(for: Bool.self) { g in
                g.contentOffset.y + g.containerSize.height >= g.contentSize.height - 80
            } action: { _, near in atBottom = near }
        }
        .safeAreaInset(edge: .bottom, spacing: 0) { composerBar }
        .background(Theme.canvas)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar { toolbarContent }
        .toolbarBackground(.visible, for: .navigationBar)
        .onAppear { chat.start() }
        .onDisappear { chat.stop() }
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

    private var emptyPromptView: some View {
        VStack(spacing: 12) {
            Image(systemName: "terminal.fill")
                .font(.system(size: 32))
                .foregroundStyle(Theme.accent)
                .frame(width: 60, height: 60)
                .background(Theme.s2, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            Text("Coding 工作区")
                .font(.system(size: 20, weight: .semibold))
                .foregroundStyle(Theme.fg)
            if !chat.cwd.isEmpty {
                Text(chat.cwd)
                    .font(.system(size: 12, design: .monospaced))
                    .foregroundStyle(Theme.fg4)
                    .multilineTextAlignment(.center)
            }
            Text("在当前项目目录中与 Coding Agent 协同读写代码、运行命令与查看工具输出。点击顶部标题可切换执行模式与模型。")
                .font(.system(size: 13.5))
                .foregroundStyle(Theme.fg3)
                .multilineTextAlignment(.center)
                .padding(.horizontal, 20)
        }
        .frame(maxWidth: .infinity)
        .padding(.top, 60)
    }

    @ViewBuilder
    private func messageRow(_ m: AgyMessage) -> some View {
        if m.role == "user" {
            VStack(alignment: .trailing, spacing: 4) {
                Text(m.text)
                    .font(.system(size: 16))
                    .foregroundStyle(Theme.onAccent)
                    .lineSpacing(2)
                    .padding(.horizontal, 14)
                    .padding(.vertical, 8)
                    .background(Theme.accent, in: UnevenRoundedRectangle(topLeadingRadius: 18, bottomLeadingRadius: 18, bottomTrailingRadius: 5, topTrailingRadius: 18, style: .continuous))
                    .contextMenu {
                        Button { copy(m.text) } label: { Label("复制", systemImage: "doc.on.doc") }
                    }
                    .padding(.leading, 44)
            }
            .frame(maxWidth: .infinity, alignment: .trailing)
        } else {
            VStack(alignment: .leading, spacing: 8) {
                if let t = m.thinking, !t.isEmpty {
                    ThinkingBlock(text: t, ms: m.thinkingMs)
                }
                if !m.tools.isEmpty {
                    toolChips(m.tools)
                }
                if !m.text.isEmpty {
                    MarkdownView(text: m.text)
                        .padding(.horizontal, 14)
                        .padding(.vertical, 10)
                        .background(Theme.s2, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                        .contextMenu {
                            Button { copy(m.text) } label: { Label("复制", systemImage: "doc.on.doc") }
                        }
                }
                if let err = m.error, !err.isEmpty {
                    Text(err)
                        .font(.footnote)
                        .foregroundStyle(Theme.danger)
                        .padding(10)
                        .background(Theme.dangerSoft, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private func toolChips(_ tools: [AgyToolCall]) -> some View {
        AgyToolCallGroup(tools: tools)
    }

    private func permCard(_ perm: AgyPermission) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            Label("请求操作确认：\(perm.title)", systemImage: "exclamationmark.shield.fill")
                .font(.system(size: 14.5, weight: .semibold))
                .foregroundStyle(Theme.warn)
            if !perm.detail.isEmpty {
                Text(perm.detail)
                    .font(.system(size: 12.5, design: .monospaced))
                    .foregroundStyle(Theme.fg2)
                    .padding(8)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(Theme.code, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
            }
            FlowLayout(spacing: 8) {
                ForEach(perm.options) { opt in
                    let isReject = opt.optionId.contains("reject") || opt.kind.contains("reject")
                    Button(opt.name) {
                        Haptics.tap()
                        Task {
                            do { try await chat.decide(permID: perm.id, optionID: opt.optionId) }
                            catch { self.error = error.localizedDescription }
                        }
                    }
                    .buttonStyle(PillButtonStyle(kind: isReject ? .danger : .primary, compact: true))
                }
            }
        }
        .padding(14)
        .card(fill: Theme.s1, border: Theme.warn.opacity(0.45), radius: 16)
    }

    private var liveTurnView: some View {
        VStack(alignment: .leading, spacing: 8) {
            if !chat.thinking.isEmpty {
                ThinkingBlock(text: chat.thinking, live: true, startedAt: chat.thinkStart, endedAt: chat.thinkEnd)
            }
            if !chat.liveTools.isEmpty {
                toolChips(chat.liveTools)
            }
            if !chat.streaming.isEmpty {
                MarkdownView(text: chat.streaming, streaming: true)
                    .padding(.horizontal, 14)
                    .padding(.vertical, 10)
                    .background(Theme.s2, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            } else if chat.thinking.isEmpty || chat.thinkEnd > 0 {
                HStack(spacing: 8) {
                    TypingDots(size: 5)
                    Text("Agent 正在执行…").font(.system(size: 13)).foregroundStyle(Theme.fg3)
                }
                .padding(.vertical, 4)
            }
        }
    }

    private var composerBar: some View {
        VStack(spacing: 6) {
            if let error {
                HStack(spacing: 6) {
                    Image(systemName: "exclamationmark.circle.fill")
                    Text(error).lineLimit(2)
                    Spacer()
                    Button("关闭") { self.error = nil }.buttonStyle(.plain).foregroundStyle(Theme.fg3)
                }
                .font(.system(size: 12.5))
                .foregroundStyle(Theme.danger)
            }
            if !chat.queue.isEmpty {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 6) {
                        ForEach(chat.queue) { q in
                            HStack(spacing: 5) {
                                Image(systemName: q.behavior == "steer" ? "bolt.fill" : "clock")
                                    .font(.system(size: 10.5))
                                    .foregroundStyle(q.behavior == "steer" ? Theme.warn : Theme.accent)
                                Text(q.text)
                                    .font(.system(size: 12))
                                    .foregroundStyle(Theme.fg2)
                                    .lineLimit(1)
                                    .frame(maxWidth: 160, alignment: .leading)
                                Button {
                                    Task { try? await chat.cancelQueued(q.id) }
                                } label: {
                                    Image(systemName: "xmark.circle.fill")
                                        .font(.system(size: 12))
                                        .foregroundStyle(Theme.fg4)
                                }
                                .buttonStyle(.plain)
                            }
                            .padding(.horizontal, 9).padding(.vertical, 5)
                            .background(Theme.s2, in: Capsule())
                        }
                    }
                }
            }
            HStack(alignment: .bottom, spacing: 8) {
                if !chat.commands.isEmpty {
                    Menu {
                        ForEach(chat.commands) { cmd in
                            Button {
                                chat.draft = "/\(cmd.name)" + (cmd.input_hint == nil ? "" : " ")
                                focused = true
                            } label: {
                                if let desc = cmd.description, !desc.isEmpty {
                                    Text("/\(cmd.name) — \(desc)")
                                } else {
                                    Text("/\(cmd.name)")
                                }
                            }
                        }
                    } label: {
                        Image(systemName: "slash.circle")
                            .font(.system(size: 20))
                            .foregroundStyle(Theme.fg3)
                            .frame(width: 34, height: 38)
                    }
                    .accessibilityLabel("斜杠命令")
                }

                TextField(chat.busy ? "执行中，发送将自动排队…" : "输入 Coding 指令，或点 / 查看命令…", text: $chat.draft, axis: .vertical)
                    .font(.system(size: 16))
                    .lineLimit(1...6)
                    .focused($focused)
                    .padding(.horizontal, 14)
                    .padding(.vertical, 9)
                    .background(Theme.field, in: RoundedRectangle(cornerRadius: 20, style: .continuous))
                    .overlay(RoundedRectangle(cornerRadius: 20, style: .continuous).strokeBorder(Theme.line2, lineWidth: 0.5))

                let trimmed = chat.draft.trimmingCharacters(in: .whitespacesAndNewlines)
                if chat.busy && trimmed.isEmpty {
                    Button {
                        Haptics.tap()
                        Task { try? await chat.stopPrompt() }
                    } label: {
                        Image(systemName: "stop.fill")
                            .font(.system(size: 14, weight: .bold))
                            .foregroundStyle(Theme.canvas)
                            .frame(width: 38, height: 38)
                            .background(Theme.fg, in: Circle())
                    }
                    .accessibilityLabel("停止")
                } else {
                    if chat.busy && !trimmed.isEmpty {
                        Button { sendNow(behavior: "steer") } label: {
                            Image(systemName: "bolt.fill")
                                .font(.system(size: 14, weight: .bold))
                                .foregroundStyle(Theme.warn)
                                .frame(width: 36, height: 36)
                                .background(Theme.warnSoft, in: Circle())
                        }
                        .disabled(sending)
                        .accessibilityLabel("打断插话")
                    }
                    Button { sendNow(behavior: chat.busy ? "follow_up" : nil) } label: {
                        Group {
                            if sending { ProgressView().tint(Theme.onAccent) }
                            else if chat.busy { Image(systemName: "clock.arrow.circlepath").font(.system(size: 15, weight: .bold)) }
                            else { Image(systemName: "arrow.up").font(.system(size: 17, weight: .bold)) }
                        }
                        .foregroundStyle(Theme.onAccent)
                        .frame(width: 38, height: 38)
                        .background(!trimmed.isEmpty && !sending ? Theme.accent : Theme.fg4.opacity(0.5), in: Circle())
                    }
                    .disabled(trimmed.isEmpty || sending)
                    .accessibilityLabel(chat.busy ? "排队发送" : "发送")
                }
            }
        }
        .frame(maxWidth: Theme.contentWidth)
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .frame(maxWidth: .infinity)
        .background(.bar)
    }

    @ToolbarContentBuilder
    private var toolbarContent: some ToolbarContent {
        ToolbarItem(placement: .principal) {
            Menu {
                if !chat.modes.isEmpty {
                    Section("执行模式") {
                        ForEach(chat.modes) { m in
                            Button {
                                Task { try? await chat.setConfig(mode: m.id) }
                            } label: {
                                if chat.mode == m.id { Label(m.name, systemImage: "checkmark") }
                                else { Text(m.name) }
                            }
                        }
                    }
                }
                if !chat.models.isEmpty {
                    Section("模型") {
                        ForEach(chat.models) { m in
                            Button {
                                Task { try? await chat.setConfig(model: m.id) }
                            } label: {
                                if chat.model == m.id { Label(m.name, systemImage: "checkmark") }
                                else { Text(m.name) }
                            }
                        }
                    }
                }
                if chat.usedTokens > 0 {
                    Section {
                        Text("上下文：\(chat.usedTokens / 1000)k" + (chat.maxTokens > 0 ? " / \(chat.maxTokens / 1000)k" : ""))
                    }
                }
            } label: {
                VStack(spacing: 1) {
                    Text(chat.title.isEmpty ? "新对话" : chat.title)
                        .font(.system(size: 15.5, weight: .semibold))
                        .foregroundStyle(Theme.fg)
                        .lineLimit(1)
                    HStack(spacing: 3) {
                        Text(chat.mode + (chat.model.isEmpty ? "" : " · \(chat.model)"))
                            .lineLimit(1)
                        Image(systemName: "chevron.down").font(.system(size: 8, weight: .bold))
                    }
                    .font(.system(size: 11))
                    .foregroundStyle(Theme.fg3)
                }
                .frame(maxWidth: 230)
            }
        }
        ToolbarItem(placement: .topBarTrailing) {
            Button { newSiblingSession() } label: { Image(systemName: "plus.bubble") }
                .accessibilityLabel("新建对话")
        }
    }

    private func sendNow(behavior: String? = nil) {
        let text = chat.draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty, !sending else { return }
        let keep = chat.draft
        chat.draft = ""
        sending = true
        error = nil
        Haptics.tap()
        Task {
            do {
                try await chat.send(text, behavior: behavior)
            } catch {
                chat.draft = keep
                self.error = error.localizedDescription
            }
            sending = false
        }
    }

    private func newSiblingSession() {
        Haptics.tap()
        Task {
            do {
                let res = try await server.client.post("/api/agy/sessions", body: [
                    "project_id": chat.projectID,
                    "mode": chat.mode,
                    "model": chat.model,
                ])
                let sid = res["session"]["id"].str
                if !sid.isEmpty {
                    if !path.isEmpty { path.removeLast() }
                    path.append(.agyChat(server: server.id, sessionID: sid, projectID: chat.projectID, title: "新对话"))
                }
            } catch {
                self.error = error.localizedDescription
            }
        }
    }
}

/// 新建或关联项目目录弹窗
private struct AgyNewProjectSheet: View {
    let store: AgyStore
    @Environment(\.dismiss) private var dismiss

    @State private var linkExisting = false
    @State private var name = ""
    @State private var customPath = ""
    @State private var saving = false
    @State private var error: String?

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    Picker("方式", selection: $linkExisting) {
                        Text("新建项目文件夹").tag(false)
                        Text("关联服务器已有路径").tag(true)
                    }
                    .pickerStyle(.segmented)
                }

                if linkExisting {
                    Section("服务器上的目录路径") {
                        TextField("~/my-repo 或 /srv/project", text: $customPath)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                            .font(.system(.body, design: .monospaced))
                        TextField("显示名称（可选）", text: $name)
                    }
                } else {
                    Section {
                        TextField("文件夹名称（如 my-app）", text: $name)
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                    } header: {
                        Text("新建空项目文件夹")
                    } footer: {
                        if let root = store.status?.projects_root, !root.isEmpty {
                            Text("将创建在：\(root)/\(name.isEmpty ? "<名称>" : name)")
                        }
                    }
                }

                if let error {
                    Section { Text(error).foregroundStyle(Theme.danger) }
                }
            }
            .navigationTitle("添加项目文件夹")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("取消") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button("确定") { save() }
                        .disabled(saving || (linkExisting ? customPath.trimmingCharacters(in: .whitespaces).isEmpty : name.trimmingCharacters(in: .whitespaces).isEmpty))
                }
            }
        }
    }

    private func save() {
        saving = true
        error = nil
        Task {
            do {
                _ = try await store.createProject(name: name, path: linkExisting ? customPath : "")
                Haptics.success()
                dismiss()
            } catch {
                self.error = error.localizedDescription
            }
            saving = false
        }
    }
}

/// 浏览项目文件夹内容
private struct AgyFilesSheet: View {
    let server: ServerEntry
    let project: AgyProject
    @Environment(\.dismiss) private var dismiss

    @State private var subDir = ""
    @State private var entries: [AgyFileEntry] = []
    @State private var error: String?

    var body: some View {
        NavigationStack {
            List {
                if !subDir.isEmpty {
                    Button {
                        var parts = subDir.split(separator: "/").map(String.init)
                        _ = parts.popLast()
                        subDir = parts.joined(separator: "/")
                    } label: {
                        Label("返回上级目录", systemImage: "arrow.turn.left.up")
                    }
                }
                if let error {
                    Text(error).font(.footnote).foregroundStyle(Theme.danger)
                } else if entries.isEmpty {
                    Text("目录为空").font(.footnote).foregroundStyle(Theme.fg4)
                }
                ForEach(entries) { e in
                    let rel = subDir.isEmpty ? e.name : "\(subDir)/\(e.name)"
                    if e.is_dir {
                        Button { subDir = rel } label: {
                            Label(e.name + "/", systemImage: "folder.fill")
                        }
                    } else {
                        HStack {
                            Label(e.name, systemImage: "doc.text")
                                .foregroundStyle(Theme.fg)
                            Spacer()
                            if let sz = e.size {
                                Text(Fmt.size(sz)).font(.caption).foregroundStyle(Theme.fg4)
                            }
                        }
                    }
                }
            }
            .navigationTitle(subDir.isEmpty ? project.name : subDir)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) { Button("完成") { dismiss() } }
            }
            .task(id: subDir) { await load() }
        }
    }

    private func load() async {
        error = nil
        var q: [URLQueryItem] = []
        if !subDir.isEmpty { q.append(URLQueryItem(name: "dir", value: subDir)) }
        do {
            entries = try await server.client.get("/api/agy/projects/\(project.id)/files", query: q)
        } catch {
            self.error = error.localizedDescription
        }
    }
}

/// 可折叠展开查看入参和执行输出的工具调用组
private struct AgyToolCallGroup: View {
    let tools: [AgyToolCall]
    @State private var expandedIDs: Set<String> = []

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            FlowLayout(spacing: 6) {
                ForEach(tools) { tc in
                    let running = tc.status == "in_progress" || tc.status == "pending"
                    let failed = tc.status == "failed" || tc.status == "error"
                    let hasExtra = !tc.detail.isEmpty || !tc.output.isEmpty
                    Button {
                        if hasExtra {
                            if expandedIDs.contains(tc.id) { expandedIDs.remove(tc.id) }
                            else { expandedIDs.insert(tc.id) }
                        }
                    } label: {
                        Chip(
                            text: tc.title.isEmpty ? tc.name : tc.title,
                            systemImage: running ? "hourglass" : failed ? "xmark.circle.fill" : "checkmark.circle.fill",
                            tint: failed ? Theme.danger : running ? Theme.accent : Theme.fg3
                        )
                    }
                    .buttonStyle(.plain)
                }
            }
            ForEach(tools.filter { expandedIDs.contains($0.id) && (!$0.detail.isEmpty || !$0.output.isEmpty) }) { tc in
                VStack(alignment: .leading, spacing: 4) {
                    if !tc.detail.isEmpty {
                        Text(tc.detail)
                            .font(.system(size: 11.5, weight: .medium, design: .monospaced))
                            .foregroundStyle(Theme.fg2)
                            .lineLimit(3)
                    }
                    if !tc.output.isEmpty {
                        ScrollView(.vertical) {
                            Text(tc.output)
                                .font(.system(size: 11, design: .monospaced))
                                .foregroundStyle(Theme.fg3)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .frame(maxHeight: 140)
                    }
                }
                .padding(8)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Theme.code, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
            }
        }
    }
}
