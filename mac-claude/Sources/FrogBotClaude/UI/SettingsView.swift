import AppKit
import ServiceManagement
import SwiftUI

/// 设置窗口：左边是「通用」「通知」和服务器列表，右边是对应的设置页。
/// API 渠道整台设备共用；模型和 Agent 每个 Bot 各自选，在服务器页里先选 Bot 再管理。
enum SettingsPane: Hashable {
    case general
    case notify
    case relay
    case server(UUID)
    case channels(BotRef)
    case agents(BotRef)
    case account(UUID)

    var serverID: UUID? {
        switch self {
        case .server(let id), .account(let id): return id
        case .channels(let r), .agents(let r): return r.server
        default: return nil
        }
    }
}

@MainActor
struct SettingsView: View {
    @Environment(AppModel.self) private var model
    @State private var pane: SettingsPane = .general
    @State private var adding = false

    var body: some View {
        HStack(spacing: 0) {
            sidebar
                .frame(width: 200)
            Rectangle().fill(Theme.line2).frame(width: 0.5)
            VStack(spacing: 0) {
                header
                ScrollView {
                    content
                        .padding(.horizontal, 22)
                        .padding(.top, 6)
                        .padding(.bottom, 22)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(Theme.settingsBG)
        }
        .frame(width: 780, height: 600)
        .sheet(isPresented: $adding) { ServerSheet(mode: .add) }
        .onAppear(perform: syncFromModel)
        .onChange(of: model.settingsServer) { syncFromModel() }
    }

    private func syncFromModel() {
        if let id = model.settingsServer, model.server(id) != nil {
            pane = .server(id)
            model.settingsServer = nil
        } else if case .server(let id) = pane, model.server(id) == nil {
            pane = .general
        } else if pane == .general, let first = model.servers.first, model.servers.count == 1 {
            pane = .server(first.id)
        }
    }

    // MARK: 侧栏

    private var sidebar: some View {
        List(selection: Binding(get: { sidebarSelection }, set: { if let p = $0 { pane = p } })) {
            Label("通用", systemImage: "gearshape").tag(SettingsPane.general)
            Label("通知", systemImage: "bell").tag(SettingsPane.notify)
            Label("Bot 互联", systemImage: "point.3.connected.trianglepath.dotted").tag(SettingsPane.relay)
            Section("服务器") {
                ForEach(model.servers) { s in
                    HStack(spacing: 6) {
                        Label(s.name, systemImage: "server.rack").lineLimit(1)
                        Spacer(minLength: 2)
                        Circle().fill(dot(s)).frame(width: 6, height: 6)
                    }
                    .tag(SettingsPane.server(s.id))
                }
                Button { adding = true } label: {
                    Label("添加服务器…", systemImage: "plus")
                        .foregroundStyle(Theme.accent)
                }
                .buttonStyle(.plain)
                .selectionDisabled()
            }
        }
        .listStyle(.sidebar)
    }

    /// 子页面（渠道、Agent、账号）在侧栏里高亮它所属的服务器
    private var sidebarSelection: SettingsPane? {
        if let id = pane.serverID { return .server(id) }
        return pane
    }

    private func dot(_ s: ServerEntry) -> Color {
        switch s.status {
        case .online: return Theme.ok
        case .connecting: return Theme.fg4
        case .needsLogin: return Theme.warnDot
        case .offline: return Theme.danger
        }
    }

    // MARK: 标题栏

    private var header: some View {
        HStack(spacing: 4) {
            if let back = parent {
                Button { pane = back } label: { Image(systemName: "chevron.left") }
                    .buttonStyle(IconButtonStyle(size: 24))
                    .help("返回")
            }
            Text(title).font(.system(size: 14, weight: .semibold)).padding(.leading, parent == nil ? 8 : 2)
            Spacer()
        }
        .padding(.horizontal, 12)
        .frame(height: 48)
        .overlay(alignment: .bottom) { Rectangle().fill(Theme.line).frame(height: 0.5) }
    }

    private var parent: SettingsPane? {
        switch pane {
        case .channels(let r), .agents(let r): return .server(r.server)
        case .account(let id): return .server(id)
        default: return nil
        }
    }

    private var title: String {
        switch pane {
        case .general: return "通用"
        case .notify: return "通知"
        case .relay: return "Bot 互联"
        case .server(let id): return model.server(id)?.name ?? "服务器"
        case .channels(let r), .agents(let r): return botName(r)
        case .account: return "账号与登录"
        }
    }

    private func botName(_ r: BotRef) -> String {
        let s = model.server(r.server)
        let bot = s?.devices.first { $0.id == r.device }?.bots.first { $0.id == r.bot }
        return [s?.name, bot?.name].compactMap { $0 }.joined(separator: " › ")
    }

    @ViewBuilder
    private var content: some View {
        switch pane {
        case .general:
            GeneralPane()
        case .notify:
            NotifyPane()
        case .relay:
            RelayPane()
        case .server(let id):
            if let s = model.server(id) {
                ServerPane(server: s, go: { pane = $0 })
            }
        case .channels(let r):
            if let session = model.session(r) {
                ChannelsPane(session: session).id(r)
            } else {
                unavailable
            }
        case .agents(let r):
            if let session = model.session(r) {
                AgentsPane(session: session).id(r)
            } else {
                unavailable
            }
        case .account(let id):
            if let s = model.server(id) { AccountPane(server: s) }
        }
    }

    private var unavailable: some View {
        Text("这个 Bot 现在连不上，稍后再试。")
            .font(.system(size: 13))
            .foregroundStyle(Theme.fg3)
            .padding(.top, 30)
    }
}

// MARK: - 小部件

@MainActor
struct GroupHeader: View {
    let text: String
    var body: some View {
        Text(text)
            .font(.system(size: 12, weight: .semibold))
            .foregroundStyle(Theme.fg3)
            .padding(.leading, 2)
            .padding(.top, 16)
            .padding(.bottom, 6)
    }
}

@MainActor
struct Caption: View {
    let text: String
    var tone: Color = Theme.fg4
    var body: some View {
        Text(text)
            .font(.system(size: 11.5))
            .foregroundStyle(tone)
            .lineSpacing(2)
            .fixedSize(horizontal: false, vertical: true)
            .padding(.horizontal, 2)
            .padding(.top, 6)
    }
}

@MainActor
struct PaneTitle: View {
    let title: String
    let desc: String
    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title).font(.system(size: 20, weight: .semibold))
            Text(desc)
                .font(.system(size: 12.5))
                .foregroundStyle(Theme.fg3)
                .lineSpacing(2)
                .fixedSize(horizontal: false, vertical: true)
        }
        .padding(.top, 12)
    }
}

// MARK: - 通用

@MainActor
private struct GeneralPane: View {
    @AppStorage(Prefs.keepRunning) private var keepRunning = true
    @AppStorage(Prefs.appearance) private var appearance = "system"
    @State private var loginItem = SMAppService.mainApp.status == .enabled
    @State private var loginError: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            FormGroup {
                FormRow(title: "关闭窗口后继续在后台运行") {
                    Toggle("", isOn: $keepRunning).toggleStyle(.switch).controlSize(.small).labelsHidden()
                }
                RowDivider()
                FormRow(title: "登录 Mac 时打开 Frog Bot Claude") {
                    Toggle("", isOn: Binding(get: { loginItem }, set: setLoginItem)).toggleStyle(.switch).controlSize(.small).labelsHidden()
                }
                RowDivider()
                FormRow(title: "外观") {
                    Picker("", selection: $appearance) {
                        Text("跟随系统").tag("system")
                        Text("浅色").tag("light")
                        Text("深色").tag("dark")
                    }
                    .labelsHidden()
                    .fixedSize()
                }
            }
            .padding(.top, 16)
            Caption(text: "关掉窗口后，后台任务完成或需要你确认时仍会通知你。")
            if let loginError { Caption(text: loginError, tone: Theme.danger) }
        }
        .onChange(of: appearance) { Prefs.applyAppearance() }
    }

    private func setLoginItem(_ on: Bool) {
        do {
            if on { try SMAppService.mainApp.register() } else { try SMAppService.mainApp.unregister() }
            loginError = nil
        } catch {
            loginError = "设置登录项失败：\(error.localizedDescription)"
        }
        loginItem = SMAppService.mainApp.status == .enabled
    }
}

// MARK: - 通知

@MainActor
private struct NotifyPane: View {
    @Environment(AppModel.self) private var model
    @AppStorage(Prefs.notifyApproval) private var approval = true
    @AppStorage(Prefs.notifyTask) private var task = true
    @AppStorage(Prefs.notifyReply) private var reply = true
    @AppStorage(Prefs.dockBadge) private var badge = true

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            GroupHeader(text: "提醒我")
            FormGroup {
                row("有操作需要我确认", $approval)
                RowDivider()
                row("任务完成或失败", $task)
                RowDivider()
                row("新回复", $reply)
                RowDivider()
                row("在 Dock 图标上显示未读数", $badge)
            }
            Caption(text: "正在看的那个 Bot 不会弹通知。通知样式在「系统设置 › 通知 › Frog Bot Claude」里调整。")
        }
        .onChange(of: badge) {
            NSApp.dockTile.badgeLabel = badge && model.totalUnread > 0 ? "\(model.totalUnread)" : nil
        }
    }

    private func row(_ title: String, _ value: Binding<Bool>) -> some View {
        FormRow(title: title) {
            Toggle("", isOn: value).toggleStyle(.switch).controlSize(.small).labelsHidden()
        }
    }
}

// MARK: - Bot 互联

@MainActor
private struct RelayPane: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let relay = model.relay
        VStack(alignment: .leading, spacing: 0) {
            FormGroup {
                FormRow(title: "允许这些服务器上的 Bot 互相发消息") {
                    Toggle("", isOn: Binding(get: { relay.enabled }, set: { relay.enabled = $0 }))
                        .toggleStyle(.switch).controlSize(.small).labelsHidden()
                }
            }
            .padding(.top, 16)
            Caption(text: "聊天 Bot 之间只能互相发文字：问问对方记得什么，或者托付一件事让对方在它自己的机器上做。只在这个 App 开着时有效，消息由 App 转交，不排队；关掉 App 或开关后，对方立刻联系不上。")

            if model.servers.count < 2 {
                Caption(text: "至少要添加两台服务器才能互联。", tone: Theme.fg3)
            }
            ForEach(model.servers) { s in
                GroupHeader(text: s.name)
                FormGroup {
                    FormRow(title: "这台服务器参与互联") {
                        HStack(spacing: 8) {
                            stateText(s)
                            Toggle("", isOn: Binding(get: { relay.isOn(s) }, set: { relay.set(s, on: $0) }))
                                .toggleStyle(.switch).controlSize(.small).labelsHidden()
                        }
                    }
                    ForEach(relay.localBots(s)) { b in
                        RowDivider()
                        FormRow(title: b.name) {
                            Toggle("", isOn: Binding(get: { relay.isOn(s, bot: b.id) }, set: { relay.set(s, bot: b.id, on: $0) }))
                                .toggleStyle(.switch).controlSize(.small).labelsHidden()
                        }
                        .padding(.leading, 14)
                    }
                }
                .disabled(!relay.enabled)
                .opacity(relay.enabled ? 1 : 0.55)
            }
        }
    }

    @ViewBuilder
    private func stateText(_ s: ServerEntry) -> some View {
        let (text, color): (String, Color) = {
            guard model.relay.enabled, model.relay.isOn(s) else { return ("", Theme.fg4) }
            switch model.relay.states[s.id] ?? .off {
            case .off: return (s.isOnline ? "" : "服务器不在线", Theme.fg4)
            case .connecting: return ("正在连接", Theme.fg4)
            case .waiting: return ("没有可联系的对象", Theme.fg4)
            case .active: return ("已接入", Theme.ok)
            case .unsupported: return ("服务器版本太旧，请先更新", Theme.warn)
            case .failed(let e): return (e, Theme.danger)
            }
        }()
        if !text.isEmpty {
            Text(text).font(.system(size: 11.5)).foregroundStyle(color).lineLimit(1).truncationMode(.tail)
                .frame(maxWidth: 260, alignment: .trailing)
        }
    }
}

// MARK: - 服务器

@MainActor
private struct ServerPane: View {
    @Environment(AppModel.self) private var model
    let server: ServerEntry
    let go: (SettingsPane) -> Void

    @State private var botKey: String = ""
    @State private var settings: APISettings?
    @State private var cli = ""
    @State private var update: UpdateStatus?
    @State private var checking = false
    @State private var updateError: String?
    @State private var relogin = false

    private var bots: [(ref: BotRef, name: String)] {
        server.devices.filter(\.online).flatMap { g in
            g.bots.map { (BotRef(server: server.id, device: g.id, bot: $0.id), g.id == "local" ? $0.name : "\($0.name)（\(g.name)）") }
        }
    }

    private var selectedRef: BotRef? {
        bots.first { $0.ref.key == botKey }?.ref ?? bots.first?.ref
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            GroupHeader(text: "连接")
            FormGroup {
                FormRow(title: "状态") {
                    Circle().fill(statusColor).frame(width: 6, height: 6)
                    Text(statusText).foregroundStyle(Theme.fg3)
                }
                RowDivider()
                FormRow(title: "地址") {
                    Text(server.config.url).foregroundStyle(Theme.fg3).textSelection(.enabled).lineLimit(1)
                }
                if server.config.isInsecure {
                    RowDivider()
                    HStack(spacing: 8) {
                        Image(systemName: "exclamationmark.triangle")
                        Text("明文 HTTP：密码和聊天内容在网络上不加密，建议配域名开启 HTTPS。")
                            .fixedSize(horizontal: false, vertical: true)
                        Spacer(minLength: 0)
                    }
                    .font(.system(size: 12))
                    .foregroundStyle(Theme.warn)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 10)
                }
                RowDivider()
                FormRow(title: "账号") {
                    Text(server.config.username).foregroundStyle(Theme.fg3)
                    if server.status == .needsLogin {
                        Button("重新登录…") { relogin = true }
                            .buttonStyle(PillButtonStyle(kind: .primary, compact: true))
                    } else if server.authEnabled {
                        Button("修改密码…") { go(.account(server.id)) }
                            .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
                        Button("退出登录") { Task { await model.logout(server) } }
                            .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
                    }
                }
            }

            GroupHeader(text: "Bot 的模型和 Agent")
            if bots.isEmpty {
                Caption(text: server.status == .needsLogin ? "重新登录后才能读取这台服务器的设置。" : "这台服务器上还没有可设置的 Bot。")
            } else {
                FormGroup {
                    FormRow(title: "设置哪个 Bot") {
                        Picker("", selection: Binding(get: { selectedRef?.key ?? "" }, set: { botKey = $0 })) {
                            ForEach(bots, id: \.ref.key) { b in Text(b.name).tag(b.ref.key) }
                        }
                        .labelsHidden()
                        .fixedSize()
                    }
                    RowDivider()
                    FormRow(title: "聊天模型") {
                        Text(chatLabel).foregroundStyle(Theme.fg3).lineLimit(1)
                    }
                    RowDivider()
                    FormRow(title: "任务模型") {
                        Text(agentLabel).foregroundStyle(Theme.fg3).lineLimit(1)
                    }
                    RowDivider()
                    FormRow(title: "API 渠道") {
                        Text(settings.map { "\($0.channels.count) 个渠道" } ?? "—").foregroundStyle(Theme.fg3)
                        Button("管理…") { if let r = selectedRef { go(.channels(r)) } }
                            .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
                    }
                    RowDivider()
                    FormRow(title: "Agent") {
                        Text(cli.isEmpty ? "Frog（内置）" : cliName).foregroundStyle(Theme.fg3)
                        Button("管理…") { if let r = selectedRef { go(.agents(r)) } }
                            .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
                    }
                }
                Caption(text: "API 渠道整台设备共用；聊天模型、任务模型和 Agent 每个 Bot 各自选，互不影响。也可以在聊天窗口顶部直接换聊天模型。")
            }

            HStack(spacing: 8) {
                Text(versionText)
                    .font(.system(size: 12))
                    .foregroundStyle(updateError == nil ? Theme.fg3 : Theme.danger)
                    .lineLimit(2)
                Spacer()
                if checking { ProgressView().controlSize(.small) }
                if update?.state == "available", let s = update?.release_url, let url = URL(string: s) {
                    Button("查看新版本") { NSWorkspace.shared.open(url) }
                        .buttonStyle(PillButtonStyle(kind: .primary, compact: true))
                }
                Button("检查更新") { check() }
                    .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
                    .disabled(checking || !server.isOnline)
                Button("在浏览器中打开") { if let u = server.config.baseURL { NSWorkspace.shared.open(u) } }
                    .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
                Button("移除…") { confirmRemove() }
                    .buttonStyle(PillButtonStyle(kind: .danger, compact: true))
            }
            .padding(.top, 18)
        }
        .task(id: selectedRef?.key ?? "") { await loadBot() }
        .sheet(isPresented: $relogin) { ServerSheet(mode: .login, server: server) }
    }

    private var cliName: String { ["codex": "Codex CLI", "claude": "Claude Code"][cli] ?? cli }

    private var chatLabel: String {
        guard let s = settings else { return "—" }
        if s.env_override == true { return "由环境变量指定" }
        let ch = s.channel(s.chatChannelID)?.name
        return s.chatModel.isEmpty ? "未选择" : [ch, s.chatModel].compactMap { $0 }.joined(separator: " · ")
    }

    private var agentLabel: String {
        if !cli.isEmpty { return "由 \(cliName) 执行" }
        guard let s = settings else { return "—" }
        let ch = s.channel(s.agentChannelID)?.name
        return s.agentModelID.isEmpty ? "未选择" : [ch, s.agentModelID].compactMap { $0 }.joined(separator: " · ")
    }

    private var statusColor: Color {
        switch server.status {
        case .online: return Theme.ok
        case .connecting: return Theme.fg4
        case .needsLogin: return Theme.warnDot
        case .offline: return Theme.danger
        }
    }

    private var statusText: String {
        let botCount = server.devices.reduce(0) { $0 + $1.bots.count }
        let peers = server.devices.filter { $0.id != "local" }.count
        switch server.status {
        case .online: return "在线 · \(botCount) 个 Bot" + (peers > 0 ? " · 互联 \(peers) 台设备" : "")
        case .connecting: return "正在连接"
        case .needsLogin: return "登录已过期"
        case .offline(let e): return "离线：\(e)"
        }
    }

    private var versionText: String {
        if let updateError { return updateError }
        let current = update?.current ?? model.sessions(of: server).first?.info?.version
        var t = "Frog Bot " + (current.map { $0.hasPrefix("v") ? $0 : "v" + $0 } ?? "版本未知")
        switch update?.state {
        case "available": t += " · 有新版本 \(update?.latest ?? "")"
        case "current": t += " · 已是最新"
        case "no_release": t += " · 还没有发布版本"
        default: break
        }
        return t
    }

    private func loadBot() async {
        settings = nil
        cli = ""
        guard let r = selectedRef, let session = model.session(r) else { return }
        settings = try? await session.loadSettings()
        cli = (try? await session.loadConnections())?.cli ?? ""
    }

    private func check() {
        checking = true
        updateError = nil
        Task {
            do { update = try await model.checkUpdate(server) } catch { updateError = "检查更新失败：\(error.localizedDescription)" }
            checking = false
        }
    }

    private func confirmRemove() {
        let alert = NSAlert()
        alert.messageText = "从面板移除「\(server.name)」？"
        alert.informativeText = "只是从这台 Mac 的面板里移除并退出登录，服务器上的 Bot、任务和数据都不受影响。"
        alert.addButton(withTitle: "移除")
        alert.addButton(withTitle: "取消")
        alert.buttons.first?.hasDestructiveAction = true
        if alert.runModal() == .alertFirstButtonReturn { model.remove(server) }
    }
}

// MARK: - 账号

@MainActor
private struct AccountPane: View {
    @Environment(AppModel.self) private var model
    let server: ServerEntry

    @State private var old = ""
    @State private var new = ""
    @State private var again = ""
    @State private var busy = false
    @State private var message: (String, Bool)?

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            GroupHeader(text: "账号")
            FormGroup {
                FormRow(title: "用户名") { Text(server.config.username).foregroundStyle(Theme.fg3) }
                RowDivider()
                FormRow(title: "这台 Mac 的登录") { Text("7 天有效，到期后重新登录").foregroundStyle(Theme.fg3) }
            }
            GroupHeader(text: "修改密码")
            FormGroup {
                FormRow(title: "当前密码") { SecureField("", text: $old).textFieldStyle(.roundedBorder).frame(width: 240) }
                RowDivider()
                FormRow(title: "新密码") { SecureField("", text: $new, prompt: Text("至少 8 位")).textFieldStyle(.roundedBorder).frame(width: 240) }
                RowDivider()
                FormRow(title: "再输一次") { SecureField("", text: $again).textFieldStyle(.roundedBorder).frame(width: 240) }
            }
            Caption(text: "修改后，浏览器和其他设备上的登录都会失效，要用新密码重新登录。这台 Mac 会自动换成新的会话。")
            if let message { Caption(text: message.0, tone: message.1 ? Theme.danger : Theme.ok) }
            HStack {
                Spacer()
                if busy { ProgressView().controlSize(.small) }
                Button("修改密码") { change() }
                    .buttonStyle(PillButtonStyle(kind: .primary))
                    .disabled(busy || old.isEmpty || new.count < 8 || new != again)
            }
            .padding(.top, 14)
        }
    }

    private func change() {
        busy = true
        message = nil
        Task {
            do {
                try await model.changePassword(server, old: old, new: new)
                old = ""; new = ""; again = ""
                message = ("密码已修改，这台 Mac 已用新密码重新登录。", false)
            } catch {
                message = (error.localizedDescription, true)
            }
            busy = false
        }
    }
}

extension AppModel {
    func sessions(of s: ServerEntry) -> [BotSession] {
        s.devices.flatMap { g in g.bots.compactMap { session(BotRef(server: s.id, device: g.id, bot: $0.id)) } }
    }
}
