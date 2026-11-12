import SwiftUI

private let sep = "\u{1F}"
private func cliName(_ id: String) -> String { ["codex": "Codex CLI", "claude": "Claude Code"][id] ?? id }

/// 「渠道 + 模型」二合一选择：按渠道分组列出可选模型
private struct RoutePicker: View {
    let title: String
    let settings: APISettings
    @Binding var selection: String

    var body: some View {
        let channels = settings.channels.filter(\.enabled)
        Picker(title, selection: $selection) {
            if channels.allSatisfy({ $0.visibleModels.isEmpty }) {
                Text("没有可选模型").tag(selection)
            }
            ForEach(channels) { ch in
                Section(ch.name) {
                    ForEach(ch.visibleModels, id: \.self) { m in Text(m).tag(ch.id + sep + m) }
                }
            }
        }
        .pickerStyle(.navigationLink)
    }
}

private func routeBinding(_ s: Binding<APISettings?>, chat: Bool) -> Binding<String> {
    Binding(
        get: {
            guard let v = s.wrappedValue else { return "" }
            return chat ? v.chatChannelID + sep + v.chatModel : v.agentChannelID + sep + v.agentModelID
        },
        set: { tag in
            let p = tag.components(separatedBy: sep)
            guard p.count == 2 else { return }
            if chat { s.wrappedValue?.default_provider_id = p[0]; s.wrappedValue?.model = p[1] }
            else { s.wrappedValue?.agent_provider_id = p[0]; s.wrappedValue?.agent_model = p[1] }
        })
}

// MARK: - 一个 Bot 的设置

struct BotSettingsView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let ref: BotRef

    @State private var name = ""
    @State private var instructions = ""
    @State private var settings: APISettings?
    @State private var original: APISettings?
    @State private var cli = ""
    @State private var loading = true
    @State private var busy = false
    @State private var error: String?
    @State private var confirmDelete = false

    private var server: ServerEntry? { model.server(ref.server) }
    private var session: BotSession? { model.session(ref) }
    private var profile: BotProfile? {
        server?.devices.first { $0.id == ref.device }?.bots.first { $0.id == ref.bot }
    }
    private var editable: Bool { ref.device == "local" }
    private var dirty: Bool {
        guard let profile else { return false }
        let p = editable && (name.trimmingCharacters(in: .whitespaces) != profile.name || instructions != (profile.instructions ?? ""))
        return p || (settings != nil && settings != original)
    }

    var body: some View {
        Form {
            if let profile, let server {
                Section {
                    HStack(spacing: 14) {
                        BotAvatar(client: server.client, profile: profile, size: 56)
                        VStack(alignment: .leading, spacing: 3) {
                            Text(profile.name).font(.title3.weight(.semibold))
                            Text(session?.deviceName ?? server.name).font(.subheadline).foregroundStyle(Theme.fg3)
                        }
                    }
                    .padding(.vertical, 4)
                }
                Section {
                    TextField("名称", text: $name).disabled(!editable)
                    TextField("职责：这个 Bot 负责什么、说话风格、注意事项…", text: $instructions, axis: .vertical)
                        .lineLimit(3...10)
                        .disabled(!editable)
                } header: {
                    Text("名称和职责")
                } footer: {
                    if !editable { Text("这个 Bot 在互联设备上，名字和职责请在那台设备上修改；模型可以在这里改。") }
                }
                modelSection
                if let session {
                    Section {
                        NavigationLink(value: Nav.agents(ref)) {
                            LabeledContent("Agent", value: cli.isEmpty ? "Frog（内置）" : cliName(cli))
                        }
                        NavigationLink(value: Nav.channels(ref)) { Text("API 渠道") }
                        NavigationLink(value: Nav.scheduler(ref)) { Text("自动调度器") }
                    } footer: {
                        Text("API 渠道是「\(session.deviceName)」上所有 Bot 共用的；Agent 和模型只影响这个 Bot。")
                    }
                }
                if let error {
                    Section { Label(error, systemImage: "exclamationmark.circle.fill").foregroundStyle(Theme.danger) }
                }
                if profile.id != "main" && editable {
                    Section {
                        Button("删除 Bot…", role: .destructive) { confirmDelete = true }
                    }
                }
            } else {
                Text("找不到这个 Bot，可能已经删除了。").foregroundStyle(Theme.fg3)
            }
        }
        .navigationTitle("Bot 设置")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                if busy { ProgressView() } else {
                    Button("保存", action: save).disabled(!dirty || name.trimmingCharacters(in: .whitespaces).isEmpty)
                }
            }
        }
        .onAppear {
            if name.isEmpty { name = profile?.name ?? "" }
            if instructions.isEmpty { instructions = profile?.instructions ?? "" }
        }
        .task { await load() }
        .confirmationDialog("删除「\(profile?.name ?? "")」？", isPresented: $confirmDelete, titleVisibility: .visible) {
            Button("从列表里移除") { delete(purge: false) }
            Button("彻底删除（无法恢复）", role: .destructive) { delete(purge: true) }
        } message: {
            Text("会停止它的任务和定时计划。「从列表里移除」会保留聊天记录、记忆、密钥和工作区文件；「彻底删除」会把这些一起删掉。")
        }
    }

    @ViewBuilder
    private var modelSection: some View {
        Section {
            if let s = settings {
                if s.channels.filter(\.enabled).isEmpty {
                    Text("还没有可用的 API 渠道，先到「API 渠道」里添加。").foregroundStyle(Theme.fg3)
                } else {
                    RoutePicker(title: "聊天模型", settings: s, selection: routeBinding($settings, chat: true))
                    if cli.isEmpty {
                        RoutePicker(title: "任务模型", settings: s, selection: routeBinding($settings, chat: false))
                    } else {
                        LabeledContent("任务模型", value: "由 \(cliName(cli)) 执行")
                    }
                }
            } else if loading {
                HStack { ProgressView(); Text("正在读取模型…").foregroundStyle(Theme.fg3) }
            } else {
                Text("读不到模型设置").foregroundStyle(Theme.fg3)
            }
        } header: {
            Text("模型")
        } footer: {
            Text("聊天模型负责回话和派活，可以选快一点的；任务模型给后台 Agent 干活用。")
        }
    }

    private func load() async {
        guard let session else { loading = false; return }
        loading = true
        if var s = try? await session.loadSettings() {
            if s.agent_provider_id == nil { s.agent_provider_id = s.chatChannelID }
            if s.agent_model == nil { s.agent_model = s.chatModel }
            settings = s
            original = s
        }
        cli = (try? await session.loadConnections())?.cli ?? ""
        loading = false
    }

    private func save() {
        guard let server, let profile else { return }
        busy = true; error = nil
        Task {
            do {
                let trimmed = name.trimmingCharacters(in: .whitespaces)
                if editable && (trimmed != profile.name || instructions != (profile.instructions ?? "")) {
                    try await model.saveBot(server: server, device: ref.device, existing: profile, name: trimmed, instructions: instructions)
                }
                if let session, let s = settings, s != original {
                    try await session.saveRoutes(chatChannel: s.chatChannelID, chatModel: s.chatModel,
                                                 agentChannel: s.agentChannelID, agentModel: s.agentModelID)
                    original = s
                }
                Haptics.success()
            } catch { self.error = error.localizedDescription }
            busy = false
        }
    }

    private func delete(purge: Bool) {
        busy = true; error = nil
        Task {
            do {
                try await model.deleteBot(ref, purge: purge)
                dismiss()
            } catch { self.error = error.localizedDescription }
            busy = false
        }
    }
}

// MARK: - API 渠道（整台设备共用）

struct ChannelsView: View {
    let session: BotSession

    @State private var draft: APISettings?
    @State private var saved: APISettings?
    @State private var busy = false
    @State private var message: (String, Bool)?
    @State private var newChannel: String?

    private var dirty: Bool { draft != nil && draft != saved }

    var body: some View {
        Form {
            if let d = draft {
                if d.env_override == true {
                    Section {
                        Text("当前模型由启动时的环境变量（BASE_URL / API_KEY / CHAT_MODEL）指定，优先于这里的设置；这里保存的内容要等去掉环境变量、重启后才生效。")
                            .font(.footnote).foregroundStyle(Theme.warn)
                    }
                }
                Section {
                    if d.channels.isEmpty {
                        Text("还没有 API 渠道。添加服务商或本地模型接口（OpenAI 兼容，/v1），然后选聊天和任务用的模型。")
                            .foregroundStyle(Theme.fg3)
                    }
                    ForEach(d.channels) { ch in
                        NavigationLink {
                            ChannelEditor(session: session, settings: Binding(get: { draft ?? d }, set: { draft = $0 }),
                                          channelID: ch.id) { remove(ch.id) }
                        } label: {
                            channelRow(ch, isDefault: ch.id == d.chatChannelID)
                        }
                    }
                    Button { add() } label: { Label("添加渠道", systemImage: "plus") }
                } header: {
                    Text("\(d.channels.count) 个渠道")
                } footer: {
                    Text("「\(session.deviceName)」上的所有 Bot 共用这些渠道，密钥加密保存在那台设备上。接口由服务器访问，localhost 指的是服务器本身。")
                }
                if !d.channels.isEmpty {
                    Section {
                        RoutePicker(title: "聊天模型", settings: d, selection: routeBinding($draft, chat: true))
                        RoutePicker(title: "任务模型", settings: d, selection: routeBinding($draft, chat: false))
                    } header: {
                        Text("「\(session.profile.name)」用的模型")
                    } footer: {
                        Text("只对「\(session.profile.name)」生效。")
                    }
                }
                if let message {
                    Section { Text(message.0).foregroundStyle(message.1 ? Theme.danger : Theme.ok) }
                }
            } else if let message, message.1 {
                Text(message.0).foregroundStyle(Theme.danger)
            } else {
                HStack { ProgressView(); Text("正在读取…").foregroundStyle(Theme.fg3) }
            }
        }
        .navigationTitle("API 渠道")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                if busy { ProgressView() } else {
                    Button("保存", action: save).disabled(!dirty || !valid)
                }
            }
        }
        .navigationDestination(item: $newChannel) { id in
            ChannelEditor(session: session, settings: Binding(get: { draft ?? APISettings() }, set: { draft = $0 }),
                          channelID: id) { remove(id) }
        }
        .task { if draft == nil { await load() } }
    }

    private func channelRow(_ ch: APIChannel, isDefault: Bool) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack(spacing: 6) {
                Text(ch.name).foregroundStyle(ch.enabled ? Theme.fg : Theme.fg3)
                if isDefault { OutlinePill(text: "默认", tint: Theme.accent) }
                if !ch.enabled { OutlinePill(text: "已停用") }
            }
            Text((ch.base_url.isEmpty ? "还没填接口地址" : ch.base_url) + " · \(ch.visibleModels.count) 个模型")
                .font(.footnote)
                .foregroundStyle(Theme.fg3)
                .lineLimit(1)
        }
    }

    private var valid: Bool {
        guard let d = draft else { return false }
        if d.channels.isEmpty { return true }
        guard d.channels.allSatisfy({ !$0.name.trimmingCharacters(in: .whitespaces).isEmpty && !$0.base_url.trimmingCharacters(in: .whitespaces).isEmpty }) else { return false }
        guard let chat = d.channel(d.chatChannelID), chat.enabled, chat.visibleModels.contains(d.chatModel) else { return false }
        guard let agent = d.channel(d.agentChannelID), agent.enabled, agent.visibleModels.contains(d.agentModelID) else { return false }
        return true
    }

    private func load() async {
        do {
            var s = try await session.loadSettings()
            if s.agent_provider_id == nil { s.agent_provider_id = s.chatChannelID }
            if s.agent_model == nil { s.agent_model = s.chatModel }
            if s.default_provider_id == nil { s.default_provider_id = s.chatChannelID }
            draft = s
            saved = s
        } catch {
            message = ("读不到渠道设置：\(error.localizedDescription)", true)
        }
    }

    private func add() {
        guard var d = draft else { return }
        var n = d.channels.count + 1
        var id = "channel-\(n)"
        while d.channel(id) != nil { n += 1; id = "channel-\(n)" }
        var list = d.channels
        list.append(APIChannel(id: id, name: "新渠道", base_url: "", enabled: true, auth_required: true,
                               api_key: nil, has_api_key: false, models: [], hidden_models: []))
        d.providers = list
        if d.channels.count == 1 { d.default_provider_id = id; d.agent_provider_id = id }
        draft = d
        message = nil
        newChannel = id
    }

    private func remove(_ id: String) {
        guard var d = draft else { return }
        let rest = d.channels.filter { $0.id != id }
        d.providers = rest
        let next = rest.first { $0.enabled }
        if d.default_provider_id == id { d.default_provider_id = next?.id; d.model = next?.visibleModels.first }
        if d.agent_provider_id == id { d.agent_provider_id = next?.id; d.agent_model = next?.visibleModels.first }
        draft = d
        message = nil
    }

    private func save() {
        guard let d = draft else { return }
        busy = true
        message = nil
        Task {
            do {
                var s = try await session.saveSettings(d)
                if s.agent_provider_id == nil { s.agent_provider_id = s.chatChannelID }
                if s.agent_model == nil { s.agent_model = s.chatModel }
                draft = s
                saved = s
                message = ("已加密保存，下一轮对话和任务使用新配置。", false)
                Haptics.success()
            } catch {
                message = (error.localizedDescription, true)
            }
            busy = false
        }
    }
}

/// 编辑一个渠道：地址、密钥、可选模型、测试连接
private struct ChannelEditor: View {
    @Environment(\.dismiss) private var dismiss
    let session: BotSession
    @Binding var settings: APISettings
    let channelID: String
    let onRemove: () -> Void

    @State private var newModel = ""
    @State private var fetching = false
    @State private var testModel = ""
    @State private var testing = false
    @State private var note: (String, Bool)?
    @State private var confirmRemove = false

    private var index: Int? { settings.providers?.firstIndex { $0.id == channelID } }
    private var channel: APIChannel? { index.map { settings.providers![$0] } }

    private func field<T>(_ kp: WritableKeyPath<APIChannel, T>, _ fallback: T) -> Binding<T> {
        Binding(
            get: { index.map { settings.providers![$0][keyPath: kp] } ?? fallback },
            set: { v in if let i = index { settings.providers![i][keyPath: kp] = v } })
    }

    var body: some View {
        Form {
            Section {
                Toggle("启用", isOn: field(\.enabled, false))
                TextField("名称", text: field(\.name, ""))
                TextField("https://api.example.com/v1", text: field(\.base_url, ""))
                    .keyboardType(.URL)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
            } footer: {
                Text("OpenAI 兼容的接口（/v1）。")
            }
            Section {
                Toggle("需要 API Key", isOn: field(\.auth_required, true))
                if channel?.auth_required == true {
                    SecureField(channel?.has_api_key == true ? "已加密保存，留空保留" : "粘贴 API Key", text: keyBinding)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                }
            } footer: {
                Text("API Key 加密保存在服务器上，这里不会显示。")
            }
            Section {
                ForEach(channel?.models ?? [], id: \.self) { m in
                    Toggle(isOn: Binding(get: { !(channel?.hidden_models ?? []).contains(m) }, set: { setVisible(m, $0) })) {
                        Text(m).font(.system(size: 14, design: .monospaced)).lineLimit(1).truncationMode(.middle)
                    }
                }
                HStack {
                    TextField("手动添加模型 ID", text: $newModel)
                        .font(.system(size: 14, design: .monospaced))
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .onSubmit(addModel)
                    Button("添加", action: addModel).disabled(newModel.trimmingCharacters(in: .whitespaces).isEmpty)
                }
                Button { fetch() } label: {
                    HStack {
                        Text("从接口拉取模型列表")
                        if fetching { Spacer(); ProgressView() }
                    }
                }
                .disabled(fetching || (channel?.base_url ?? "").isEmpty)
            } header: {
                Text("可选模型 · 共 \(channel?.models?.count ?? 0) 个，显示 \(channel?.visibleModels.count ?? 0) 个")
            } footer: {
                Text("关掉的模型不会出现在模型列表里。")
            }
            Section {
                Picker("测试用的模型", selection: $testModel) {
                    ForEach(channel?.visibleModels ?? [], id: \.self) { Text($0).tag($0) }
                }
                .disabled((channel?.visibleModels ?? []).isEmpty)
                Button { test() } label: {
                    HStack {
                        Text("测试连接")
                        if testing { Spacer(); ProgressView() }
                    }
                }
                .disabled(testing || testModel.isEmpty)
                if let note { Text(note.0).foregroundStyle(note.1 ? Theme.danger : Theme.ok) }
            }
            Section {
                Button("移除这个渠道", role: .destructive) { confirmRemove = true }
            } footer: {
                Text("所有修改要回到上一页点「保存」才会生效。")
            }
        }
        .navigationTitle(channel?.name ?? "渠道")
        .navigationBarTitleDisplayMode(.inline)
        .onAppear { if testModel.isEmpty { testModel = channel?.visibleModels.first ?? "" } }
        .confirmationDialog("移除渠道「\(channel?.name ?? "")」？", isPresented: $confirmRemove, titleVisibility: .visible) {
            Button("移除", role: .destructive) {
                dismiss()
                onRemove()
            }
        } message: {
            Text("保存设置后生效，密钥会一起删除。")
        }
    }

    private var keyBinding: Binding<String> {
        Binding(
            get: { channel?.api_key ?? "" },
            set: { v in if let i = index { settings.providers![i].api_key = v.isEmpty ? nil : v } })
    }

    private var probe: ChannelProbe {
        ChannelProbe(provider_id: channelID, base_url: channel?.base_url ?? "",
                     api_key: channel?.auth_required == false ? "" : channel?.api_key, model: nil)
    }

    private func setVisible(_ m: String, _ on: Bool) {
        guard let i = index else { return }
        var hidden = settings.providers![i].hidden_models ?? []
        hidden.removeAll { $0 == m }
        if !on { hidden.append(m) }
        settings.providers![i].hidden_models = hidden
    }

    private func addModel() {
        let m = newModel.trimmingCharacters(in: .whitespaces)
        guard !m.isEmpty, let i = index else { return }
        var list = settings.providers![i].models ?? []
        if !list.contains(m) { list.append(m) }
        settings.providers![i].models = list
        newModel = ""
        if testModel.isEmpty { testModel = m }
    }

    private func fetch() {
        fetching = true
        note = nil
        let p = probe
        Task {
            do {
                let models = try await session.fetchModels(p)
                if let i = index {
                    let old = Set(settings.providers![i].models ?? [])
                    var merged = settings.providers![i].models ?? []
                    for m in models where !old.contains(m) { merged.append(m) }
                    settings.providers![i].models = merged
                    // 已经有模型时，新拉到的默认不显示，避免列表太长
                    if !old.isEmpty {
                        var hidden = settings.providers![i].hidden_models ?? []
                        for m in models where !old.contains(m) { hidden.append(m) }
                        settings.providers![i].hidden_models = hidden
                    }
                }
                note = ("拉到 \(models.count) 个模型", false)
                if testModel.isEmpty { testModel = channel?.visibleModels.first ?? "" }
            } catch {
                note = (error.localizedDescription, true)
            }
            fetching = false
        }
    }

    private func test() {
        testing = true
        note = nil
        var p = probe
        p.model = testModel
        Task {
            do {
                let ms = try await session.testChannel(p)
                note = ("正常 · \(ms) ms", false)
            } catch {
                note = (error.localizedDescription, true)
            }
            testing = false
        }
    }
}

// MARK: - Agent

struct AgentsView: View {
    let session: BotSession

    @State private var conn: AgentConnections?
    @State private var busy = false
    @State private var message: (String, Bool)?

    var body: some View {
        List {
            Section {
                if let conn {
                    row(id: "", name: "Frog（内置）", detail: "用 API 渠道里的任务模型", installed: true, current: conn.cli.isEmpty)
                    ForEach(conn.clis ?? []) { c in
                        row(id: c.id, name: c.name, detail: Self.detail(c),
                            installed: c.installed, current: conn.cli == c.id, cli: c)
                    }
                } else if message == nil {
                    HStack { ProgressView(); Text("正在检测…").foregroundStyle(Theme.fg3) }
                }
            } header: {
                Text("「\(session.profile.name)」的后台任务用")
            } footer: {
                Text("只影响这个 Bot；聊天回复仍用聊天模型。检测的是「\(session.deviceName)」上的安装情况；「安装」会把 Node.js 和 CLI 装进 frog 的数据目录，不需要 root。CLI 要用运行 frog 的账户在那台机器上登录好；每次启动外部 CLI 前，都要在任务卡片里批准。")
            }
            if let message {
                Section { Text(message.0).foregroundStyle(message.1 ? Theme.danger : Theme.ok) }
            }
            if session.isLocalDevice { PrivilegeSection(session: session) }
        }
        .listStyle(.insetGrouped)
        .navigationTitle("Agent")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .confirmationAction) {
                if busy { ProgressView() } else {
                    Button { Task { await load() } } label: { Image(systemName: "arrow.clockwise") }
                        .accessibilityLabel("重新检测")
                }
            }
        }
        .task { await load() }
        // 安装在服务器后台进行：装的时候每 1.5 秒刷新一次，装完自动停。
        .task(id: installing) {
            while installing && !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 1_500_000_000)
                if let c = try? await session.loadConnections() { conn = c }
            }
        }
    }

    private var installing: Bool { conn?.clis?.contains { $0.installing } ?? false }

    static func detail(_ c: CLIStatus) -> String {
        if let job = c.install {
            switch job.state {
            case "running": return "正在安装：\(job.step ?? "准备")…"
            case "failed": return "安装失败：\(job.error ?? "未知错误")"
            default: break
            }
        }
        guard c.installed else { return "没有检测到" }
        return c.managed == true ? "已由 frog 安装" + (c.install?.version.map { " \($0)" } ?? "") : "已安装 · 用 CLI 自己的账户和模型"
    }

    private func row(id: String, name: String, detail: String, installed: Bool, current: Bool, cli: CLIStatus? = nil) -> some View {
        let canInstall = session.isLocalDevice && cli?.installable == true
        return HStack(spacing: 12) {
            Button { if installed && !current { use(id) } } label: {
                HStack(spacing: 12) {
                    if id.isEmpty { FrogAgentAvatar(size: 32) } else { AgentIcon(id: id, client: session.client, size: 32) }
                    VStack(alignment: .leading, spacing: 2) {
                        Text(name).foregroundStyle(Theme.fg)
                        Text(detail).font(.footnote).foregroundStyle(cli?.install?.state == "failed" ? Theme.danger : Theme.fg3)
                    }
                    Spacer()
                    if current { Image(systemName: "checkmark").font(.body.weight(.semibold)).foregroundStyle(Theme.accent) }
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .disabled(!installed || busy)
            .opacity(installed || canInstall ? 1 : 0.5)
            if let cli, canInstall {
                if cli.installing {
                    ProgressView()
                } else {
                    Button(cli.managed == true ? "更新" : "安装") { install(cli.id) }
                        .buttonStyle(.bordered)
                        .controlSize(.small)
                        .disabled(busy || installing)
                }
            }
        }
    }

    private func install(_ id: String) {
        message = nil
        Task {
            do {
                conn = try await session.installCLI(id)
            } catch {
                message = (error.localizedDescription, true)
            }
        }
    }

    private func load() async {
        busy = true
        do {
            conn = try await session.loadConnections()
            if message?.1 == true { message = nil }
        } catch {
            message = ("读不到 Agent 设置：\(error.localizedDescription)", true)
        }
        busy = false
    }

    private func use(_ id: String) {
        busy = true
        message = nil
        Task {
            do {
                conn = try await session.saveConnection(id)
                let name = id.isEmpty ? "Frog（内置）" : (conn?.clis?.first { $0.id == id }?.name ?? id)
                message = ("新任务将使用 \(name)，正在进行的任务不受影响。", false)
                Haptics.success()
            } catch {
                message = (error.localizedDescription, true)
            }
            busy = false
        }
    }
}

/// 提权到 root：Agent 需要 root 时弹卡片，输入登录密码授权。这里看状态、开关提权、结束免密窗口。
struct PrivilegeSection: View {
    let session: BotSession

    @State private var st: PrivilegeStatus?
    @State private var password = ""
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        Section {
            if let st {
                VStack(alignment: .leading, spacing: 2) {
                    Text(st.sandbox.available ? "Agent 沙箱已启用" : "Agent 沙箱不可用").foregroundStyle(Theme.fg)
                    Text(st.summary).font(.footnote).foregroundStyle(st.available ? Theme.fg3 : Theme.danger)
                }
                if st.available, (st.status?.grant_left_sec ?? 0) > 0 {
                    Button("结束免密窗口") { act("revoke") }.disabled(busy)
                }
                if st.available {
                    Button("关闭提权", role: .destructive) { act("disable") }.disabled(busy)
                }
                if !st.available, st.status?.configured == true, st.status?.enabled == false {
                    SecureField("登录密码", text: $password).textContentType(.password)
                    Button("开启提权") { act("enable") }.disabled(busy || password.isEmpty)
                }
            } else {
                HStack { ProgressView(); Text("正在读取…").foregroundStyle(Theme.fg3) }
            }
            if let error { Text(error).foregroundStyle(Theme.danger) }
        } header: {
            Text("提权到 root")
        } footer: {
            Text("Agent 需要 root 时（例如装系统软件）会弹出卡片，你输入登录密码后由「\(session.deviceName)」上的提权助手执行，并记入审计日志。")
        }
        .task { st = try? await session.loadPrivilege() }
    }

    private func act(_ action: String) {
        busy = true
        error = nil
        Task {
            do {
                st = try await session.privilegeAction(action, password: action == "enable" ? password : nil)
                password = ""
            } catch { self.error = error.localizedDescription }
            busy = false
        }
    }
}
