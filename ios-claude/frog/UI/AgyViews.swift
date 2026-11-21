import SwiftUI

/// 服务器上的 Antigravity CLI 工作台首页：切换/新建项目文件夹 + 浏览或新建对话。
struct AgyHomeView: View {
    let server: ServerEntry
    @Binding var path: [Nav]

    @State private var store: AgyStore
    @State private var showingNewProject = false
    @State private var showingFiles = false

    init(server: ServerEntry, path: Binding<[Nav]>) {
        self.server = server
        self._path = path
        self._store = State(initialValue: AgyStore(server: server))
    }

    var body: some View {
        List {
            if let st = store.status, (!st.installed || (st.error ?? "").isEmpty == false) {
                Section {
                    Label(st.error ?? "未检测到 agy CLI，请在服务器上安装并运行 agy acp --login", systemImage: "exclamationmark.triangle.fill")
                        .font(.footnote)
                        .foregroundStyle(Theme.warn)
                }
            }

            Section {
                ForEach(store.projects) { p in
                    Button {
                        Haptics.tap()
                        store.selectProject(p.id)
                        Task { await store.refreshSessions() }
                    } label: {
                        HStack(spacing: 10) {
                            Image(systemName: p.id == store.activeProject?.id ? "folder.fill" : "folder")
                                .foregroundStyle(p.id == store.activeProject?.id ? Theme.accent : Theme.fg3)
                            VStack(alignment: .leading, spacing: 2) {
                                HStack(spacing: 6) {
                                    Text(p.name)
                                        .font(.system(size: 15.5, weight: p.id == store.activeProject?.id ? .semibold : .regular))
                                        .foregroundStyle(Theme.fg)
                                    if !p.managed {
                                        Text("外部").font(.system(size: 10.5, weight: .medium))
                                            .foregroundStyle(Theme.fg3)
                                            .padding(.horizontal, 5).padding(.vertical, 1)
                                            .background(Theme.s2, in: Capsule())
                                    }
                                }
                                Text(p.path)
                                    .font(.system(size: 11.5, design: .monospaced))
                                    .foregroundStyle(Theme.fg4)
                                    .lineLimit(1)
                            }
                            Spacer()
                            if p.id == store.activeProject?.id {
                                Image(systemName: "checkmark").font(.system(size: 13, weight: .semibold)).foregroundStyle(Theme.accent)
                            }
                        }
                    }
                    .swipeActions(edge: .trailing) {
                        if store.projects.count > 1 {
                            Button(role: .destructive) {
                                Task { try? await store.deleteProject(p, deleteFiles: false) }
                            } label: { Label("移除", systemImage: "trash") }
                        }
                    }
                }
                Button { showingNewProject = true } label: {
                    Label("新建或关联项目文件夹…", systemImage: "folder.badge.plus")
                        .foregroundStyle(Theme.accent)
                }
                if store.activeProject != nil {
                    Button { showingFiles = true } label: {
                        Label("浏览当前项目文件", systemImage: "doc.text.magnifyingglass")
                            .foregroundStyle(Theme.fg2)
                    }
                }
            } header: {
                Text("项目文件夹")
            }

            Section {
                Button { startNewConversation() } label: {
                    Label("在「\(store.activeProject?.name ?? "当前项目")」新建对话", systemImage: "plus.message.fill")
                        .font(.system(size: 15.5, weight: .medium))
                        .foregroundStyle(Theme.accent)
                }
                .disabled(store.activeProject == nil || store.status?.installed == false)

                if store.sessions.isEmpty && !store.loading {
                    Text("该项目下还没有对话，点上方即可新建。")
                        .font(.footnote)
                        .foregroundStyle(Theme.fg4)
                }

                ForEach(store.sessions) { s in
                    NavigationLink(value: Nav.agyChat(server: server.id, sessionID: s.id, projectID: s.project_id, title: s.title)) {
                        HStack(spacing: 10) {
                            VStack(alignment: .leading, spacing: 4) {
                                HStack(spacing: 6) {
                                    if s.pending_permissions > 0 {
                                        Circle().fill(Theme.warnDot).frame(width: 8, height: 8)
                                    } else if s.busy {
                                        Circle().fill(Theme.accent).frame(width: 8, height: 8)
                                    }
                                    Text(s.title.isEmpty ? "新对话" : s.title)
                                        .font(.system(size: 15.5, weight: .medium))
                                        .foregroundStyle(Theme.fg)
                                        .lineLimit(1)
                                }
                                HStack(spacing: 6) {
                                    Text(s.mode).font(.system(size: 12, design: .monospaced)).foregroundStyle(Theme.fg3)
                                    if let m = s.model, !m.isEmpty {
                                        Text("· \(m)").font(.system(size: 12)).foregroundStyle(Theme.fg4).lineLimit(1)
                                    }
                                }
                            }
                            Spacer()
                        }
                        .padding(.vertical, 2)
                    }
                    .swipeActions(edge: .trailing) {
                        Button(role: .destructive) {
                            Task { try? await store.deleteSession(s.id) }
                        } label: { Label("删除", systemImage: "trash") }
                    }
                }
            } header: {
                HStack {
                    Text("对话列表")
                    if store.loading { ProgressView().controlSize(.mini) }
                }
            }

            if let err = store.error {
                Section { Text(err).font(.footnote).foregroundStyle(Theme.danger) }
            }
        }
        .listStyle(.insetGrouped)
        .navigationTitle("Antigravity CLI")
        .navigationBarTitleDisplayMode(.inline)
        .refreshable { await store.loadAll() }
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button { startNewConversation() } label: { Image(systemName: "square.and.pencil") }
                    .disabled(store.activeProject == nil || store.status?.installed == false)
                    .accessibilityLabel("新建对话")
            }
        }
        .sheet(isPresented: $showingNewProject) {
            AgyNewProjectSheet(store: store)
        }
        .sheet(isPresented: $showingFiles) {
            if let proj = store.activeProject {
                AgyFilesSheet(server: server, project: proj)
            }
        }
        .task { await store.loadAll() }
    }

    private func startNewConversation() {
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
            Text("直连 Antigravity CLI")
                .font(.system(size: 20, weight: .semibold))
                .foregroundStyle(Theme.fg)
            if !chat.cwd.isEmpty {
                Text(chat.cwd)
                    .font(.system(size: 12, design: .monospaced))
                    .foregroundStyle(Theme.fg4)
                    .multilineTextAlignment(.center)
            }
            Text("点击顶部标题可随时切换执行模式（Auto / Accept Edits / Plan）与模型。")
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
        FlowLayout(spacing: 6) {
            ForEach(tools) { tc in
                let running = tc.status == "in_progress" || tc.status == "pending"
                Chip(
                    text: tc.title.isEmpty ? tc.name : tc.title,
                    systemImage: running ? "hourglass" : "checkmark.circle.fill",
                    tint: running ? Theme.accent : Theme.fg3
                )
            }
        }
    }

    private func permCard(_ perm: AgyPermission) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            Label("agy 请求确认：\(perm.title)", systemImage: "exclamationmark.shield.fill")
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
                    Text("agy 正在执行…").font(.system(size: 13)).foregroundStyle(Theme.fg3)
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
            HStack(alignment: .bottom, spacing: 8) {
                TextField("直接发指令给 agy…", text: $chat.draft, axis: .vertical)
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
                    Button(action: sendNow) {
                        Group {
                            if sending { ProgressView().tint(Theme.onAccent) }
                            else { Image(systemName: "arrow.up").font(.system(size: 17, weight: .bold)) }
                        }
                        .foregroundStyle(Theme.onAccent)
                        .frame(width: 38, height: 38)
                        .background(!trimmed.isEmpty && !sending ? Theme.accent : Theme.fg4.opacity(0.5), in: Circle())
                    }
                    .disabled(trimmed.isEmpty || sending)
                    .accessibilityLabel("发送")
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

    private func sendNow() {
        let text = chat.draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty, !sending else { return }
        let keep = chat.draft
        chat.draft = ""
        sending = true
        error = nil
        Haptics.tap()
        Task {
            do {
                try await chat.send(text)
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
