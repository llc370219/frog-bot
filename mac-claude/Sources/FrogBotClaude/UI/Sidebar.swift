import AppKit
import SwiftUI

/// 侧栏：每台服务器一组，组里是它的 Bot；配了设备互联的，互联设备的 Bot 分在小标题下面。
/// 底部固定「自动调度器」和「设置」。
@MainActor
struct SidebarView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.openSettings) private var openSettings

    var body: some View {
        List(selection: listSelection) {
            ForEach(model.servers) { server in
                Section {
                    ServerRows(server: server)
                } header: {
                    ServerHeader(server: server)
                }
            }
        }
        .listStyle(.sidebar)
        .safeAreaInset(edge: .bottom, spacing: 0) { footer }
        .navigationSplitViewColumnWidth(min: 200, ideal: 236, max: 320)
        .toolbar {
            ToolbarItem(placement: .automatic) {
                newBotButton
            }
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
                }
            })
    }

    @ViewBuilder
    private var newBotButton: some View {
        let online = model.servers.filter(\.isOnline)
        if online.count > 1 {
            Menu {
                ForEach(online) { s in
                    Button("在「\(s.name)」新建…") { model.sheet = .newBot(server: s.id, device: "local") }
                }
            } label: {
                Label("新建 Bot", systemImage: "plus")
            }
            .help("新建 Bot")
        } else {
            Button {
                if let s = online.first ?? model.servers.first { model.sheet = .newBot(server: s.id, device: "local") }
            } label: {
                Label("新建 Bot", systemImage: "plus")
            }
            .disabled(online.isEmpty)
            .help("新建 Bot")
        }
    }

    private var footer: some View {
        VStack(spacing: 1) {
            Rectangle().fill(Theme.line).frame(height: 0.5).padding(.bottom, 6)
            FooterRow(title: "自动调度器", symbol: "clock", selected: model.route == .scheduler) {
                model.route = .scheduler
            }
            .disabled(model.selection == nil)
            FooterRow(title: "设置", symbol: "gearshape", shortcut: "⌘,", selected: false) {
                model.settingsServer = nil
                openSettings()
            }
        }
        .padding(.horizontal, 10)
        .padding(.bottom, 10)
    }
}

@MainActor
private struct FooterRow: View {
    let title: String
    let symbol: String
    var shortcut: String? = nil
    let selected: Bool
    let action: () -> Void
    @State private var hover = false

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
            .background(selected ? Theme.s3 : hover ? Theme.hover : .clear, in: RoundedRectangle(cornerRadius: 6, style: .continuous))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .onHover { hover = $0 }
    }
}

@MainActor
private struct ServerHeader: View {
    @Environment(AppModel.self) private var model
    @Environment(\.openSettings) private var openSettings
    let server: ServerEntry

    var body: some View {
        HStack(spacing: 6) {
            Text(server.name).lineLimit(1)
            Circle().fill(dotColor).frame(width: 5, height: 5)
            if server.config.isInsecure {
                Text("HTTP")
                    .font(.system(size: 9.5, weight: .medium))
                    .foregroundStyle(Theme.warn)
                    .help("明文 HTTP 连接：密码和聊天内容在网络上不加密")
            }
            Spacer(minLength: 4)
        }
        .contextMenu { ServerMenu(server: server, openSettings: { open() }) }
        .help(helpText)
    }

    private func open() {
        model.settingsServer = server.id
        openSettings()
    }

    private var dotColor: Color {
        switch server.status {
        case .online: return Theme.ok
        case .connecting: return Theme.fg4
        case .needsLogin: return Theme.warnDot
        case .offline: return Theme.danger
        }
    }

    private var helpText: String {
        switch server.status {
        case .online: return "\(server.config.url) · 在线"
        case .connecting: return "\(server.config.url) · 正在连接"
        case .needsLogin: return "\(server.config.url) · 需要重新登录"
        case .offline(let e): return "\(server.config.url) · 离线：\(e)"
        }
    }
}

@MainActor
private struct ServerMenu: View {
    @Environment(AppModel.self) private var model
    let server: ServerEntry
    var openSettings: () -> Void

    var body: some View {
        Button("新建 Bot…") { model.sheet = .newBot(server: server.id, device: "local") }
            .disabled(!server.isOnline)
        Button("刷新") { Task { await model.refresh(server) } }
        Divider()
        if server.authEnabled {
            Button("重新登录…") { model.sheet = .login(server.id) }
        }
        Button("服务器设置…") { openSettings() }
        Button("在浏览器中打开") {
            if let url = server.config.baseURL { NSWorkspace.shared.open(url) }
        }
        Button("复制地址") { copy(server.config.url) }
    }
}

@MainActor
private struct ServerRows: View {
    @Environment(AppModel.self) private var model
    let server: ServerEntry

    var body: some View {
        switch server.status {
        case .needsLogin:
            notice(text: "登录已过期", action: "重新登录") { model.sheet = .login(server.id) }
        case .offline(let e) where server.devices.isEmpty:
            notice(text: e, action: "重试") { Task { await model.refresh(server) } }
        case .connecting where server.devices.isEmpty:
            HStack(spacing: 8) {
                ProgressView().controlSize(.small)
                Text("正在连接…").foregroundStyle(Theme.fg3)
            }
            .font(.system(size: 12))
            .selectionDisabled()
        default:
            ForEach(server.devices) { group in
                if group.id != "local" {
                    HStack(spacing: 5) {
                        Text(group.name)
                        Text(group.online ? "互联" : "离线").foregroundStyle(Theme.fg4)
                    }
                    .font(.system(size: 11, weight: .medium))
                    .foregroundStyle(Theme.fg3)
                    .padding(.top, 6)
                    .selectionDisabled()
                    .contextMenu {
                        Button("在「\(group.name)」新建 Bot…") { model.sheet = .newBot(server: server.id, device: group.id) }
                            .disabled(!group.online)
                    }
                    if group.bots.isEmpty {
                        Text(group.online ? "没有 Bot" : "暂时连不上")
                            .font(.system(size: 11.5))
                            .foregroundStyle(Theme.fg4)
                            .selectionDisabled()
                    }
                }
                ForEach(group.bots) { bot in
                    BotRow(server: server, group: group, bot: bot)
                        .tag(BotRef(server: server.id, device: group.id, bot: bot.id))
                }
            }
        }
    }

    private func notice(text: String, action: String, perform: @escaping () -> Void) -> some View {
        HStack(spacing: 6) {
            Text(text).lineLimit(2).foregroundStyle(Theme.fg3)
            Spacer(minLength: 4)
            Button(action, action: perform)
                .buttonStyle(.plain)
                .foregroundStyle(Theme.accent)
                .fontWeight(.medium)
        }
        .font(.system(size: 12))
        .selectionDisabled()
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
                    .font(.system(size: 12))
                    .monospacedDigit()
                    .foregroundStyle(Theme.fg3)
            }
        }
        .contextMenu {
            Button("编辑 Bot…") { model.sheet = .editBot(ref) }
            Button("在浏览器中打开服务器") {
                if let url = server.config.baseURL { NSWorkspace.shared.open(url) }
            }
        }
    }
}
