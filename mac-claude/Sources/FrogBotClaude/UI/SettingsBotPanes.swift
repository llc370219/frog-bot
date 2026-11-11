import AppKit
import SwiftUI

// MARK: - API 渠道（属于某个 Bot）

@MainActor
struct ChannelsPane: View {
    let session: BotSession

    @State private var draft: APISettings?
    @State private var saved: APISettings?
    @State private var editing: String?
    @State private var busy = false
    @State private var message: (String, Bool)?

    private var dirty: Bool { draft != nil && draft != saved }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            if let draft {
                if let id = editing, draft.channel(id) != nil {
                    ChannelEditor(session: session, settings: Binding(get: { self.draft ?? draft }, set: { self.draft = $0 }),
                                  channelID: id, onDone: { editing = nil }, onRemove: { remove(id) })
                } else {
                    overview(draft)
                }
                footer
            } else if let message, message.1 {
                Caption(text: message.0, tone: Theme.danger).padding(.top, 20)
            } else {
                ProgressView().controlSize(.small).padding(.top, 30)
            }
        }
        .task { await load() }
    }

    private func overview(_ s: APISettings) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(alignment: .bottom) {
                PaneTitle(title: "API 渠道", desc: "「\(session.deviceName)」上所有 Bot 共用这些渠道，密钥加密保存在那台设备上。下面的聊天模型和任务模型只对「\(session.profile.name)」生效。")
                Spacer(minLength: 12)
                Button("添加渠道") { add() }
                    .buttonStyle(PillButtonStyle(kind: .primary))
            }
            if s.env_override == true {
                Caption(text: "当前模型由启动时的环境变量（BASE_URL / API_KEY / CHAT_MODEL）指定，优先于这里的设置；这里保存的内容要等去掉环境变量、重启后才生效。", tone: Theme.warn)
            }
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text("\(s.channels.count) 个渠道").font(.system(size: 12, weight: .semibold)).foregroundStyle(Theme.fg3)
                Text("保存后，下一轮对话和任务生效").font(.system(size: 11)).foregroundStyle(Theme.fg4)
            }
            .padding(.top, 18)
            .padding(.bottom, 10)
            if s.channels.isEmpty {
                VStack(spacing: 8) {
                    Image(systemName: "powerplug").font(.system(size: 22)).foregroundStyle(Theme.fg4)
                    Text("还没有 API 渠道").font(.system(size: 13, weight: .medium))
                    Text("添加服务商或本地模型接口，然后选聊天和任务用的模型。").font(.system(size: 12)).foregroundStyle(Theme.fg3)
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, 30)
                .card(fill: Theme.groupFill, border: Theme.line)
            } else {
                LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: 10), count: 3), spacing: 10) {
                    ForEach(s.channels) { ch in
                        ChannelCard(channel: ch, isDefault: ch.id == s.chatChannelID,
                                    toggle: { setEnabled(ch.id, $0) }, edit: { editing = ch.id })
                    }
                }
                routes(s)
            }
        }
    }

    @ViewBuilder
    private func routes(_ s: APISettings) -> some View {
        GroupHeader(text: "「\(session.profile.name)」用的模型")
        FormGroup {
            FormRow(title: "聊天模型") { RoutePicker(settings: s, chat: true, draft: $draft) }
            RowDivider()
            FormRow(title: "任务模型（Agent API）") { RoutePicker(settings: s, chat: false, draft: $draft) }
        }
        Caption(text: "聊天模型负责回话和派活；任务模型给后台 Agent 用。如果在「Agent」里选了 Codex CLI 或 Claude Code，任务会改用 CLI 自己的账户和模型。接口由服务器访问，localhost 指的是「\(session.deviceName)」本身。")
    }

    private var footer: some View {
        HStack(spacing: 8) {
            Image(systemName: "lock").font(.system(size: 11)).foregroundStyle(Theme.fg4)
            Text(message?.0 ?? (dirty ? "有未保存的修改" : "配置已保存 · 密钥加密存储"))
                .font(.system(size: 12))
                .foregroundStyle(message.map { $0.1 ? Theme.danger : Theme.ok } ?? Theme.fg3)
                .lineLimit(2)
            Spacer()
            if busy { ProgressView().controlSize(.small) }
            if dirty {
                Button("还原") { draft = saved; editing = nil; message = nil }
                    .buttonStyle(PillButtonStyle(kind: .secondary))
            }
            Button("保存设置") { save() }
                .buttonStyle(PillButtonStyle(kind: .primary))
                .disabled(busy || !dirty || !valid)
        }
        .padding(.top, 18)
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
        editing = id
        message = nil
    }

    private func setEnabled(_ id: String, _ on: Bool) {
        guard var d = draft, var list = d.providers, let i = list.firstIndex(where: { $0.id == id }) else { return }
        list[i].enabled = on
        d.providers = list
        draft = d
        message = nil
    }

    private func remove(_ id: String) {
        guard var d = draft else { return }
        let rest = d.channels.filter { $0.id != id }
        d.providers = rest
        let next = rest.first { $0.enabled }
        if d.default_provider_id == id { d.default_provider_id = next?.id; d.model = next?.visibleModels.first }
        if d.agent_provider_id == id { d.agent_provider_id = next?.id; d.agent_model = next?.visibleModels.first }
        draft = d
        editing = nil
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
                editing = nil
                message = ("已加密保存，下一轮对话和任务使用新配置。", false)
            } catch {
                message = (error.localizedDescription, true)
            }
            busy = false
        }
    }
}

@MainActor
private struct ChannelCard: View {
    let channel: APIChannel
    let isDefault: Bool
    let toggle: (Bool) -> Void
    let edit: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 7) {
                Image(systemName: "powerplug").font(.system(size: 13)).foregroundStyle(Theme.fg3)
                Text(channel.name).font(.system(size: 13, weight: .semibold)).lineLimit(1)
                Spacer(minLength: 2)
                if isDefault {
                    OutlinePill(text: "默认", tint: Theme.accent)
                } else if !channel.enabled {
                    OutlinePill(text: "已停用")
                }
            }
            Text((channel.base_url.isEmpty ? "还没填接口地址" : channel.base_url) + "\n\(channel.visibleModels.count) 个可选模型 · " + (channel.auth_required ? "API Key" : "无需密钥"))
                .font(.system(size: 11.5))
                .foregroundStyle(Theme.fg3)
                .lineSpacing(2)
                .lineLimit(3)
                .padding(.top, 6)
            Spacer(minLength: 10)
            HStack {
                Toggle("", isOn: Binding(get: { channel.enabled }, set: toggle))
                    .toggleStyle(.switch).controlSize(.mini).labelsHidden()
                Spacer()
                Button("设置", action: edit).buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
            }
        }
        .padding(12)
        .frame(maxWidth: .infinity, minHeight: 116, alignment: .topLeading)
        .opacity(channel.enabled ? 1 : 0.6)
        .card(fill: Theme.groupFill, border: Theme.line)
    }
}

/// 「渠道 + 模型」二合一选择：按渠道分组列出可选模型
@MainActor
private struct RoutePicker: View {
    let settings: APISettings
    let chat: Bool
    @Binding var draft: APISettings?

    var body: some View {
        let channels = settings.channels.filter(\.enabled)
        Picker("", selection: Binding(
            get: { chat ? settings.chatChannelID + "\u{1F}" + settings.chatModel : settings.agentChannelID + "\u{1F}" + settings.agentModelID },
            set: { tag in
                let p = tag.components(separatedBy: "\u{1F}")
                guard p.count == 2 else { return }
                if chat { draft?.default_provider_id = p[0]; draft?.model = p[1] } else { draft?.agent_provider_id = p[0]; draft?.agent_model = p[1] }
            })) {
            if channels.allSatisfy({ $0.visibleModels.isEmpty }) {
                Text("没有可选模型").tag(chat ? settings.chatChannelID + "\u{1F}" + settings.chatModel : settings.agentChannelID + "\u{1F}" + settings.agentModelID)
            }
            ForEach(channels) { ch in
                Section(ch.name) {
                    ForEach(ch.visibleModels, id: \.self) { m in Text(m).tag(ch.id + "\u{1F}" + m) }
                }
            }
        }
        .labelsHidden()
        .fixedSize()
    }
}

// MARK: - 编辑一个渠道

@MainActor
private struct ChannelEditor: View {
    let session: BotSession
    @Binding var settings: APISettings
    let channelID: String
    let onDone: () -> Void
    let onRemove: () -> Void

    @State private var newModel = ""
    @State private var fetching = false
    @State private var testModel = ""
    @State private var testing = false
    @State private var note: (String, Bool)?
    @State private var showKey = false
    @State private var confirmRemove = false

    private var index: Int? { settings.providers?.firstIndex { $0.id == channelID } }

    private func field<T>(_ kp: WritableKeyPath<APIChannel, T>, _ fallback: T) -> Binding<T> {
        Binding(
            get: { index.map { settings.providers![$0][keyPath: kp] } ?? fallback },
            set: { v in if let i = index { settings.providers![i][keyPath: kp] = v } })
    }

    private var channel: APIChannel? { index.map { settings.providers![$0] } }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            PaneTitle(title: channel?.name ?? "渠道", desc: "OpenAI 兼容的接口（/v1）。API Key 加密保存在服务器上，这里不会显示。")
            FormGroup {
                FormRow(title: "启用") {
                    Toggle("", isOn: field(\.enabled, false)).toggleStyle(.switch).controlSize(.small).labelsHidden()
                }
                RowDivider()
                FormRow(title: "名称") { TextField("", text: field(\.name, "")).textFieldStyle(.roundedBorder).frame(width: 300) }
                RowDivider()
                FormRow(title: "接口地址") {
                    TextField("", text: field(\.base_url, ""), prompt: Text("https://api.example.com/v1")).textFieldStyle(.roundedBorder).frame(width: 300)
                }
                RowDivider()
                FormRow(title: "需要 API Key") {
                    Toggle("", isOn: field(\.auth_required, true)).toggleStyle(.switch).controlSize(.small).labelsHidden()
                }
                if channel?.auth_required == true {
                    RowDivider()
                    FormRow(title: "API Key") {
                        Group {
                            if showKey {
                                TextField("", text: keyBinding, prompt: Text(keyPrompt))
                            } else {
                                SecureField("", text: keyBinding, prompt: Text(keyPrompt))
                            }
                        }
                        .textFieldStyle(.roundedBorder)
                        .frame(width: 270)
                        Button { showKey.toggle() } label: { Image(systemName: showKey ? "eye.slash" : "eye") }
                            .buttonStyle(IconButtonStyle(size: 22))
                    }
                }
            }
            .padding(.top, 14)

            HStack {
                Text("可选模型 · 共 \(channel?.models?.count ?? 0) 个，显示 \(channel?.visibleModels.count ?? 0) 个")
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundStyle(Theme.fg3)
                Spacer()
                if fetching { ProgressView().controlSize(.small) }
                Button("拉取模型") { fetch() }
                    .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
                    .disabled(fetching || (channel?.base_url ?? "").isEmpty)
            }
            .padding(.top, 18)
            .padding(.bottom, 6)
            FormGroup {
                ForEach(Array((channel?.models ?? []).enumerated()), id: \.element) { i, m in
                    if i > 0 { RowDivider() }
                    HStack(spacing: 8) {
                        Text(m).font(.system(size: 12, design: .monospaced)).lineLimit(1).truncationMode(.middle)
                        Spacer()
                        Toggle("", isOn: Binding(get: { !(channel?.hidden_models ?? []).contains(m) }, set: { setVisible(m, $0) }))
                            .toggleStyle(.switch).controlSize(.mini).labelsHidden()
                            .help("在模型列表里显示")
                    }
                    .padding(.horizontal, 12)
                    .frame(minHeight: 34)
                }
                if !(channel?.models ?? []).isEmpty { RowDivider() }
                HStack(spacing: 8) {
                    TextField("", text: $newModel, prompt: Text("手动添加模型 ID"))
                        .textFieldStyle(.plain)
                        .font(.system(size: 12, design: .monospaced))
                        .onSubmit(addModel)
                    Button("添加", action: addModel)
                        .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
                        .disabled(newModel.trimmingCharacters(in: .whitespaces).isEmpty)
                }
                .padding(.horizontal, 12)
                .frame(minHeight: 36)
            }

            HStack(spacing: 8) {
                Picker("", selection: $testModel) {
                    ForEach(channel?.visibleModels ?? [], id: \.self) { Text($0).tag($0) }
                }
                .labelsHidden()
                .fixedSize()
                .disabled((channel?.visibleModels ?? []).isEmpty)
                Button("测试连接") { test() }
                    .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
                    .disabled(testing || testModel.isEmpty)
                if testing { ProgressView().controlSize(.small) }
                if let note {
                    Text(note.0).font(.system(size: 12)).foregroundStyle(note.1 ? Theme.danger : Theme.ok).lineLimit(2)
                }
                Spacer()
                Button("移除渠道…") { confirmRemove = true }
                    .buttonStyle(.plain)
                    .foregroundStyle(Theme.danger)
                    .font(.system(size: 12.5))
                Button("完成", action: onDone)
                    .buttonStyle(PillButtonStyle(kind: .secondary))
            }
            .padding(.top, 14)
        }
        .onAppear { testModel = channel?.visibleModels.first ?? "" }
        .confirmationDialog("移除渠道「\(channel?.name ?? "")」？", isPresented: $confirmRemove) {
            Button("移除", role: .destructive, action: onRemove)
        } message: {
            Text("保存设置后生效，密钥会一起删除。")
        }
    }

    private var keyPrompt: String { channel?.has_api_key == true ? "已加密保存，留空保留" : "粘贴 API Key" }

    private var keyBinding: Binding<String> {
        Binding(
            get: { channel?.api_key ?? "" },
            set: { v in if let i = index { settings.providers![i].api_key = v.isEmpty ? nil : v } })
    }

    private var probe: ChannelProbe {
        ChannelProbe(provider_id: channelID, base_url: channel?.base_url ?? "", api_key: channel?.auth_required == false ? "" : channel?.api_key, model: nil)
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
                    // 新拉到的模型默认不显示，避免列表太长；已有的保持原样
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

@MainActor
struct AgentsPane: View {
    let session: BotSession

    @State private var conn: AgentConnections?
    @State private var busy = false
    @State private var message: (String, Bool)?

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            PaneTitle(title: "Agent", desc: "「\(session.profile.name)」的后台任务用哪个 Agent 执行，只影响这个 Bot。聊天回复仍用 API 渠道里的聊天模型。")
            HStack {
                Text("Agent 工具").font(.system(size: 12, weight: .semibold)).foregroundStyle(Theme.fg3)
                Spacer()
                if busy { ProgressView().controlSize(.small) }
                Button("重新检测") { Task { await load() } }
                    .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
                    .disabled(busy)
            }
            .padding(.top, 18)
            .padding(.bottom, 10)
            if let conn {
                LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: 10), count: 3), spacing: 10) {
                    AgentCard(id: "", name: "Frog", detail: "内置 · 用 API 渠道里的任务模型", installed: true,
                              current: conn.cli.isEmpty, client: session.client) { use("") }
                    ForEach(conn.clis ?? []) { c in
                        AgentCard(id: c.id, name: c.name, detail: "\(c.id) · " + (c.installed ? "已安装 · 用 CLI 自己的账户" : "没有检测到"),
                                  installed: c.installed, current: conn.cli == c.id, client: session.client) { use(c.id) }
                    }
                }
            } else if message == nil {
                ProgressView().controlSize(.small).padding(.top, 20)
            }
            if let message { Caption(text: message.0, tone: message.1 ? Theme.danger : Theme.ok) }
            Caption(text: "检测的是「\(session.deviceName)」上的安装情况。CLI 要用运行 Frog Bot 的账户在那台机器上登录好；每次启动外部 CLI 前，都要在任务卡片里批准。")
                .padding(.top, 6)
        }
        .task { await load() }
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
                message = ("「\(session.profile.name)」的新任务将使用 \(name)，正在进行的任务不受影响。", false)
            } catch {
                message = (error.localizedDescription, true)
            }
            busy = false
        }
    }
}

@MainActor
private struct AgentCard: View {
    let id: String
    let name: String
    let detail: String
    let installed: Bool
    let current: Bool
    let client: ServerClient
    let use: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 8) {
                if id.isEmpty {
                    FrogAgentAvatar(size: 24)
                } else {
                    AgentIcon(id: id, client: client, size: 22)
                }
                Text(name).font(.system(size: 13, weight: .semibold)).lineLimit(1)
            }
            Text(detail)
                .font(.system(size: 11.5))
                .foregroundStyle(Theme.fg3)
                .lineSpacing(2)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.top, 6)
            Spacer(minLength: 10)
            HStack {
                if current {
                    OutlinePill(text: "当前选择", systemImage: "checkmark", tint: Theme.accent)
                } else if installed {
                    Button("使用", action: use).buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
                } else {
                    Text("未检测到").font(.system(size: 11.5)).foregroundStyle(Theme.fg4)
                }
                Spacer()
            }
        }
        .padding(12)
        .frame(maxWidth: .infinity, minHeight: 112, alignment: .topLeading)
        .opacity(installed ? 1 : 0.55)
        .card(fill: Theme.groupFill, border: current ? Theme.accent : Theme.line)
    }
}
