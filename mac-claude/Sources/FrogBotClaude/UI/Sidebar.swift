import AppKit
import SwiftUI

/// 现代双核侧栏：
/// 顶部主机切换器（带本机自动识别 / E2EE 徽标）+ Bot / Coding 双模式切换；
/// Bot 模式保留多主机对话列表与调度器入口；
/// Coding 模式直连官方 CLI、项目目录与无气泡会话列表。
@MainActor
struct SidebarView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.openSettings) private var openSettings

    var body: some View {
        VStack(spacing: 0) {
            topControls
            Rectangle().fill(Theme.line).frame(height: 0.5)

            if model.workspaceMode == .coding {
                CodingSidebarBody()
            } else {
                botList
            }
        }
        .safeAreaInset(edge: .bottom, spacing: 0) { footer }
        .navigationSplitViewColumnWidth(min: 224, ideal: 256, max: 340)
        .toolbar {
            ToolbarItem(placement: .automatic) {
                if model.workspaceMode == .bot {
                    newBotButton
                } else {
                    newCodingSessionButton
                }
            }
        }
    }

    // MARK: 侧栏顶栏：左侧极简主机切换 + 右上角 Bot / Coding 紧凑图标切换

    private var topControls: some View {
        VStack(spacing: 6) {
            HStack(spacing: 8) {
                hostSwitcher
                Spacer(minLength: 4)
                WorkspaceModeSwitcher()
            }
            if let msg = model.localScanMessage {
                HStack(spacing: 5) {
                    Text(msg)
                        .font(.system(size: 11))
                        .foregroundStyle(Theme.fg3)
                        .lineLimit(2)
                    Spacer(minLength: 2)
                    Button { model.localScanMessage = nil } label: {
                        Image(systemName: "xmark").font(.system(size: 9))
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(Theme.fg4)
                }
                .padding(.horizontal, 8)
                .padding(.vertical, 5)
                .background(Theme.s2, in: RoundedRectangle(cornerRadius: 7, style: .continuous))
            }
        }
        .padding(.horizontal, 10)
        .padding(.top, 6)
        .padding(.bottom, 8)
    }

    private var hostSwitcher: some View {
        Menu {
            if !model.servers.isEmpty {
                Section("切换主机") {
                    ForEach(model.servers) { s in
                        Button {
                            model.selectServer(s)
                        } label: {
                            let labelText = hostMenuTitle(for: s)
                            if model.activeServer?.id == s.id {
                                Label(labelText, systemImage: "checkmark")
                            } else {
                                Text(labelText)
                            }
                        }
                    }
                }
                Divider()
            }
            Button {
                Task { await model.discoverLocalHost(manual: true) }
            } label: {
                Label("扫描本机 Frog Bot", systemImage: "desktopcomputer.and.arrow.down")
            }
            Button {
                model.sheet = .addServer
            } label: {
                Label("添加主机 / 粘贴连接码…", systemImage: "plus")
            }
            if let s = model.activeServer {
                Divider()
                Button {
                    Task { await model.refresh(s) }
                } label: {
                    Label("刷新当前主机", systemImage: "arrow.clockwise")
                }
                if s.authEnabled {
                    Button {
                        model.sheet = .login(s.id)
                    } label: {
                        Label("重新登录…", systemImage: "key")
                    }
                }
                Button {
                    model.sheet = .cliAuth(s.id)
                } label: {
                    Label("官方 CLI 账号登录…", systemImage: "terminal")
                }
                .disabled(!s.isOnline)
                Button {
                    model.sheet = .proxyGateway(s.id)
                } label: {
                    Label("前置 Bot 账号反代网关…", systemImage: "arrow.triangle.branch")
                }
                .disabled(!s.isOnline)
                if !s.config.isTunneled, let url = s.config.baseURL {
                    Button {
                        NSWorkspace.shared.open(url)
                    } label: {
                        Label("在浏览器中打开", systemImage: "safari")
                    }
                }
                Button {
                    model.settingsServer = s.id
                    openSettings()
                } label: {
                    Label("当前主机设置…", systemImage: "slider.horizontal.3")
                }
            }
        } label: {
            HStack(spacing: 6) {
                Circle()
                    .fill(activeHostDotColor)
                    .frame(width: 6.5, height: 6.5)
                Text(model.activeServer?.name ?? "未连接")
                    .font(.system(size: 13, weight: .semibold))
                    .foregroundStyle(Theme.fg)
                    .lineLimit(1)
                    .truncationMode(.tail)
                if model.scanningLocal {
                    ProgressView().controlSize(.mini)
                } else {
                    Image(systemName: "chevron.down")
                        .font(.system(size: 9, weight: .semibold))
                        .foregroundStyle(Theme.fg4)
                }
            }
            .padding(.horizontal, 6)
            .frame(height: 26)
            .contentShape(RoundedRectangle(cornerRadius: 6, style: .continuous))
        }
        .menuStyle(.button)
        .buttonStyle(.plain)
        .menuIndicator(.hidden)
        .help(activeHostTooltip)
    }

    private func hostMenuTitle(for s: ServerEntry) -> String {
        var tags: [String] = []
        if s.config.isLocalHost && s.name != "本机" {
            tags.append("本机")
        } else if let badge = s.config.tunnelBadge {
            tags.append(badge)
        }
        switch s.status {
        case .online: tags.append("在线")
        case .connecting: tags.append("连接中")
        case .needsLogin: tags.append("需登录")
        case .offline: tags.append("离线")
        }
        return tags.isEmpty ? s.name : "\(s.name)（\(tags.joined(separator: " · "))）"
    }

    private var activeHostDotColor: Color {
        guard let s = model.activeServer else { return Theme.fg4 }
        switch s.status {
        case .online: return Theme.ok
        case .connecting: return Theme.fg4
        case .needsLogin: return Theme.warnDot
        case .offline: return Theme.danger
        }
    }

    private var activeHostTooltip: String {
        guard let s = model.activeServer else { return "点击扫描本机或添加主机" }
        switch s.status {
        case .online: return "\(s.name) · 在线"
        case .connecting: return "\(s.name) · 正在连接"
        case .needsLogin: return "\(s.name) · 需要登录"
        case .offline: return "\(s.name) · 暂时离线"
        }
    }

    // MARK: Bot 模式列表（仅展示当前选中主机的 Bot，不再把服务器名列成分组列表）

    @ViewBuilder
    private var botList: some View {
        if let server = model.activeServer {
            switch server.status {
            case .needsLogin:
                VStack(spacing: 10) {
                    Text("「\(server.name)」登录已过期")
                        .font(.system(size: 12.5))
                        .foregroundStyle(Theme.fg3)
                    Button("重新登录") { model.sheet = .login(server.id) }
                        .buttonStyle(PillButtonStyle(kind: .primary, compact: true))
                }
                .padding(16)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            case .offline(let e) where server.devices.isEmpty:
                VStack(spacing: 8) {
                    Text("当前主机离线")
                        .font(.system(size: 12.5, weight: .medium))
                        .foregroundStyle(Theme.fg2)
                    Text(e)
                        .font(.system(size: 11.5))
                        .foregroundStyle(Theme.fg4)
                        .multilineTextAlignment(.center)
                        .lineLimit(3)
                    Button("重试连接") { Task { await model.refresh(server) } }
                        .buttonStyle(PillButtonStyle(kind: .primary, compact: true))
                        .padding(.top, 2)
                }
                .padding(16)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            case .connecting where server.devices.isEmpty:
                VStack(spacing: 8) {
                    ProgressView().controlSize(.small)
                    Text("正在连接「\(server.name)」…")
                        .font(.system(size: 12))
                        .foregroundStyle(Theme.fg3)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            default:
                let localGroup = server.devices.first(where: { $0.id == "local" })
                let remoteGroups = server.devices.filter { $0.id != "local" }
                let hasAnyBot = server.devices.contains { !$0.bots.isEmpty }
                if !hasAnyBot && remoteGroups.isEmpty {
                    VStack(spacing: 10) {
                        Text("这台主机还没有 Bot")
                            .font(.system(size: 12.5))
                            .foregroundStyle(Theme.fg3)
                        Button("新建 Bot…") {
                            model.sheet = .newBot(server: server.id, device: "local")
                        }
                        .buttonStyle(PillButtonStyle(kind: .primary, compact: true))
                        .disabled(!server.isOnline)
                    }
                    .padding(16)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else {
                    List(selection: listSelection) {
                        if let localGroup {
                            ForEach(localGroup.bots) { bot in
                                BotRow(server: server, group: localGroup, bot: bot)
                                    .tag(BotRef(server: server.id, device: localGroup.id, bot: bot.id))
                            }
                        }
                        ForEach(remoteGroups) { group in
                            Section {
                                if group.bots.isEmpty {
                                    Text(group.online ? "没有 Bot" : "暂时连不上")
                                        .font(.system(size: 11.5))
                                        .foregroundStyle(Theme.fg4)
                                        .selectionDisabled()
                                } else {
                                    ForEach(group.bots) { bot in
                                        BotRow(server: server, group: group, bot: bot)
                                            .tag(BotRef(server: server.id, device: group.id, bot: bot.id))
                                    }
                                }
                            } header: {
                                HStack(spacing: 5) {
                                    Text(group.name)
                                    Text(group.online ? "互联" : "离线").foregroundStyle(Theme.fg4)
                                }
                                .contextMenu {
                                    Button("在「\(group.name)」新建 Bot…") {
                                        model.sheet = .newBot(server: server.id, device: group.id)
                                    }
                                    .disabled(!group.online)
                                }
                            }
                        }
                    }
                    .listStyle(.sidebar)
                }
            }
        } else {
            VStack(spacing: 10) {
                Text("请先连接本机或远端主机")
                    .font(.system(size: 12))
                    .foregroundStyle(Theme.fg3)
                Button("扫描本机 Frog Bot") {
                    Task { await model.discoverLocalHost(manual: true) }
                }
                .buttonStyle(PillButtonStyle(kind: .primary, compact: true))
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    /// 打开自动调度器时，Bot 不显示为选中
    private var listSelection: Binding<BotRef?> {
        Binding(
            get: { model.route == .chat ? model.selection : nil },
            set: { ref in
                if let ref {
                    model.selection = ref
                    model.route = .chat
                    model.workspaceMode = .bot
                }
            })
    }

    private var newBotButton: some View {
        Button {
            if let s = model.activeServer, s.isOnline {
                model.sheet = .newBot(server: s.id, device: "local")
            } else if let s = model.servers.first(where: \.isOnline) {
                model.sheet = .newBot(server: s.id, device: "local")
            }
        } label: {
            Label("新建 Bot", systemImage: "plus")
        }
        .disabled(model.activeServer?.isOnline != true && !model.servers.contains(where: \.isOnline))
        .help("新建 Bot")
    }

    private var newCodingSessionButton: some View {
        Button {
            guard let s = model.activeServer, s.isOnline else { return }
            let store = model.agyStore(for: s)
            Task {
                if store.activeProject == nil {
                    model.sheet = .newProject(s.id)
                } else if let summary = try? await store.createSession() {
                    model.selectCodingSession(server: s, summary: summary)
                }
            }
        } label: {
            Label("新建 Coding 会话", systemImage: "plus")
        }
        .disabled(model.activeServer?.isOnline != true)
        .help("新建 Coding 会话")
    }

    private var footer: some View {
        VStack(spacing: 1) {
            Rectangle().fill(Theme.line).frame(height: 0.5).padding(.bottom, 6)
            if model.workspaceMode == .bot {
                FooterRow(title: "自动调度器", symbol: "clock", shortcut: "⌘J", selected: model.route == .scheduler) {
                    model.route = .scheduler
                }
                .disabled(model.selection == nil)
            }
            FooterRow(title: "设置", symbol: "gearshape", shortcut: "⌘,", selected: false) {
                model.settingsServer = model.activeServer?.id
                openSettings()
            }
        }
        .padding(.horizontal, 10)
        .padding(.bottom, 10)
    }
}

// MARK: - Coding 模式侧栏（CLI 选择 + 项目目录 + 会话列表）

@MainActor
private struct CodingSidebarBody: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        if let server = model.activeServer {
            CodingServerSidebar(server: server, store: model.agyStore(for: server))
                .id(server.id)
        } else {
            VStack(spacing: 10) {
                Text("请先连接本机或远端主机")
                    .font(.system(size: 12))
                    .foregroundStyle(Theme.fg3)
                Button("扫描本机 Frog Bot") {
                    Task { await model.discoverLocalHost(manual: true) }
                }
                .buttonStyle(PillButtonStyle(kind: .primary, compact: true))
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }
}

@MainActor
private struct CodingServerSidebar: View {
    @Environment(AppModel.self) private var model
    let server: ServerEntry
    @Bindable var store: AgyStore
    @State private var creatingSession = false

    var body: some View {
        VStack(spacing: 0) {
            if !server.isOnline {
                VStack(spacing: 8) {
                    Text(server.status == .needsLogin ? "主机需要重新登录" : "当前主机离线")
                        .font(.system(size: 12))
                        .foregroundStyle(Theme.fg3)
                    Button(server.status == .needsLogin ? "重新登录" : "重试连接") {
                        if server.status == .needsLogin {
                            model.sheet = .login(server.id)
                        } else {
                            Task { await model.refresh(server) }
                        }
                    }
                    .buttonStyle(PillButtonStyle(kind: .primary, compact: true))
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                // CLI 引擎快捷切换条
                if !store.installedAgents.isEmpty {
                    ScrollView(.horizontal, showsIndicators: false) {
                        HStack(spacing: 5) {
                            ForEach(store.installedAgents) { cli in
                                let active = cli.id == store.activeAgentID
                                Button {
                                    Task { await store.selectAgent(cli.id) }
                                } label: {
                                    Text(cli.name)
                                        .font(.system(size: 11, weight: active ? .semibold : .regular))
                                        .foregroundStyle(active ? Theme.accent : Theme.fg3)
                                        .padding(.horizontal, 8)
                                        .padding(.vertical, 3.5)
                                        .background(active ? Theme.accentSoft : Theme.s2, in: Capsule())
                                }
                                .buttonStyle(.plain)
                            }
                        }
                        .padding(.horizontal, 10)
                        .padding(.vertical, 8)
                    }
                    Rectangle().fill(Theme.line).frame(height: 0.5)
                }

                // 项目目录切换条
                HStack(spacing: 6) {
                    Menu {
                        ForEach(store.projects) { p in
                            Button {
                                store.selectProject(p.id)
                                Task { await store.refreshSessions() }
                            } label: {
                                if store.activeProject?.id == p.id {
                                    Label(p.name, systemImage: "checkmark")
                                } else {
                                    Text(p.name)
                                }
                            }
                        }
                        if !store.projects.isEmpty { Divider() }
                        Button {
                            model.sheet = .newProject(server.id)
                        } label: {
                            Label("新建或关联项目目录…", systemImage: "folder.badge.plus")
                        }
                        if let active = store.activeProject {
                            Button {
                                model.sheet = .projectFiles(server: server.id, project: active)
                            } label: {
                                Label("浏览当前项目文件", systemImage: "doc.text.magnifyingglass")
                            }
                            Divider()
                            Button(role: .destructive) {
                                Task { try? await store.deleteProject(active, deleteFiles: false) }
                            } label: {
                                Label("移除项目关联", systemImage: "trash")
                            }
                        }
                    } label: {
                        HStack(spacing: 6) {
                            Image(systemName: "folder.fill")
                                .font(.system(size: 11.5))
                                .foregroundStyle(Theme.accent)
                            Text(store.activeProject?.name ?? "选择项目目录")
                                .font(.system(size: 12, weight: .medium))
                                .foregroundStyle(Theme.fg)
                                .lineLimit(1)
                            Spacer(minLength: 2)
                            Image(systemName: "chevron.down")
                                .font(.system(size: 9, weight: .semibold))
                                .foregroundStyle(Theme.fg4)
                        }
                        .padding(.horizontal, 8)
                        .frame(height: 28)
                        .background(Theme.s2.opacity(0.75), in: RoundedRectangle(cornerRadius: 7, style: .continuous))
                        .contentShape(RoundedRectangle(cornerRadius: 7, style: .continuous))
                    }
                    .menuStyle(.button)
                    .buttonStyle(.plain)
                    .menuIndicator(.hidden)

                    Button {
                        model.sheet = .newProject(server.id)
                    } label: {
                        Image(systemName: "folder.badge.plus")
                    }
                    .buttonStyle(IconButtonStyle(size: 26))
                    .help("添加或关联项目")
                }
                .padding(.horizontal, 10)
                .padding(.vertical, 8)

                // 会话列表
                List {
                    Section {
                        Button {
                            createNewSession()
                        } label: {
                            HStack(spacing: 6) {
                                if creatingSession {
                                    ProgressView().controlSize(.mini)
                                } else {
                                    Image(systemName: "plus.circle.fill")
                                        .foregroundStyle(Theme.accent)
                                }
                                Text("新建 Coding 会话")
                                    .font(.system(size: 12.5, weight: .medium))
                                    .foregroundStyle(Theme.accent)
                                Spacer()
                            }
                            .padding(.vertical, 2)
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .disabled(creatingSession || store.activeProject == nil)

                        if store.loading && store.sessions.isEmpty {
                            HStack(spacing: 6) {
                                ProgressView().controlSize(.small)
                                Text("加载会话…").font(.system(size: 12)).foregroundStyle(Theme.fg3)
                            }
                        } else if store.sessions.isEmpty {
                            Text(store.activeProject == nil ? "先添加项目文件夹" : "暂无会话，点击上方新建")
                                .font(.system(size: 11.5))
                                .foregroundStyle(Theme.fg4)
                        }

                        ForEach(store.sessions) { s in
                            let selected = model.codingSelection?.server == server.id && model.codingSelection?.sessionID == s.id
                            Button {
                                model.selectCodingSession(server: server, summary: s)
                            } label: {
                                HStack(spacing: 7) {
                                    if s.pending_permissions > 0 {
                                        Circle().fill(Theme.warnDot).frame(width: 6, height: 6)
                                    } else if s.busy {
                                        PulseDot(size: 6)
                                    } else {
                                        Image(systemName: "doc.plaintext")
                                            .font(.system(size: 11))
                                            .foregroundStyle(selected ? Theme.accent : Theme.fg4)
                                    }
                                    VStack(alignment: .leading, spacing: 2) {
                                        Text(s.title.isEmpty ? "新对话" : s.title)
                                            .font(.system(size: 12.5, weight: selected ? .semibold : .regular))
                                            .foregroundStyle(Theme.fg)
                                            .lineLimit(1)
                                        HStack(spacing: 4) {
                                            if let an = s.agent_name ?? s.agent, !an.isEmpty {
                                                Text(an)
                                            }
                                            Text("· \(s.mode)")
                                        }
                                        .font(.system(size: 10.5))
                                        .foregroundStyle(Theme.fg4)
                                        .lineLimit(1)
                                    }
                                    Spacer(minLength: 2)
                                }
                                .padding(.horizontal, 6)
                                .padding(.vertical, 4)
                                .background(selected ? Theme.s3 : .clear, in: RoundedRectangle(cornerRadius: 6, style: .continuous))
                                .contentShape(Rectangle())
                            }
                            .buttonStyle(.plain)
                            .contextMenu {
                                Button("删除会话", role: .destructive) {
                                    Task {
                                        try? await store.deleteSession(s.id)
                                        if model.codingSelection?.sessionID == s.id {
                                            model.codingSelection = nil
                                        }
                                    }
                                }
                            }
                        }
                    } header: {
                        HStack {
                            Text("会话")
                            Spacer()
                            Button { Task { await store.loadAll() } } label: {
                                Image(systemName: "arrow.clockwise").font(.system(size: 10))
                            }
                            .buttonStyle(.plain)
                            .foregroundStyle(Theme.fg4)
                            .help("刷新项目与会话")
                        }
                    }
                }
                .listStyle(.sidebar)
            }
        }
        .task(id: server.id) {
            guard server.isOnline else { return }
            await store.loadAll()
            if model.codingSelection == nil || model.codingSelection?.server != server.id,
               let first = store.sessions.first {
                model.selectCodingSession(server: server, summary: first)
            }
        }
    }

    private func createNewSession() {
        creatingSession = true
        Task {
            if let s = try? await store.createSession() {
                model.selectCodingSession(server: server, summary: s)
            }
            creatingSession = false
        }
    }
}

@MainActor
private struct FooterRow: View {
    let title: String
    let symbol: String
    var shortcut: String? = nil
    let selected: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 8) {
                Image(systemName: symbol)
                    .font(.system(size: 13))
                    .foregroundStyle(Theme.fg3)
                    .frame(width: 18)
                Text(title).font(.system(size: 13)).foregroundStyle(Theme.fg)
                Spacer(minLength: 4)
                if let shortcut { Text(shortcut).font(.system(size: 11)).foregroundStyle(Theme.fg4) }
            }
            .padding(.horizontal, 8)
            .frame(height: 28)
            .background(selected ? Theme.s3 : .clear, in: RoundedRectangle(cornerRadius: 6, style: .continuous))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }
}

@MainActor
private struct BotRow: View {
    @Environment(AppModel.self) private var model
    let server: ServerEntry
    let group: DeviceGroup
    let bot: BotProfile

    private var ref: BotRef { BotRef(server: server.id, device: group.id, bot: bot.id) }

    var body: some View {
        let session = model.session(ref)
        HStack(spacing: 8) {
            BotAvatar(client: server.client, profile: bot, size: 20)
            Text(bot.name).lineLimit(1)
            Spacer(minLength: 4)
            if let session {
                if session.pendingApprovals > 0 {
                    Circle().fill(Theme.warnDot).frame(width: 7, height: 7)
                        .help("\(session.pendingApprovals) 个操作等你确认")
                } else if session.busy || session.working {
                    PulseDot(size: 6).help("正在工作")
                }
            }
            if let n = model.unread[ref], n > 0 {
                Text("\(n)")
                    .font(.system(size: 11, weight: .semibold))
                    .monospacedDigit()
                    .foregroundStyle(Theme.accent)
                    .padding(.horizontal, 6)
                    .padding(.vertical, 1)
                    .background(Theme.accentSoft, in: Capsule())
            }
        }
        .contextMenu {
            Button("编辑 Bot…") { model.sheet = .editBot(ref) }
            if !server.config.isTunneled {
                Button("在浏览器中打开服务器") {
                    if let url = server.config.baseURL { NSWorkspace.shared.open(url) }
                }
            }
        }
    }
}
