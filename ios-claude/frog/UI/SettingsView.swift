import SwiftUI
import UIKit
import UserNotifications

/// 设置首页：服务器、Bot 互联、通知、外观、关于
struct SettingsView: View {
    @Environment(AppModel.self) private var model
    @Binding var path: [Nav]
    @AppStorage(Prefs.appearance) private var appearance = "system"

    var body: some View {
        List {
            Section("服务器") {
                ForEach(model.servers) { s in
                    NavigationLink(value: Nav.server(s.id)) {
                        HStack(spacing: 12) {
                            Image(systemName: "server.rack")
                                .font(.system(size: 15))
                                .foregroundStyle(Theme.accent)
                                .frame(width: 30, height: 30)
                                .background(Theme.accentSoft, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
                            VStack(alignment: .leading, spacing: 2) {
                                Text(s.name).foregroundStyle(Theme.fg)
                                Text(statusText(s)).font(.footnote).foregroundStyle(statusColor(s)).lineLimit(1)
                            }
                        }
                    }
                }
                .onMove { model.move(from: $0, to: $1) }
                Button { model.sheet = .addServer } label: { Label("添加服务器", systemImage: "plus") }
            }
            Section {
                NavigationLink(value: Nav.relay) {
                    LabeledContent {
                        Text(model.relay.enabled ? "已打开" : "已关闭")
                    } label: {
                        Label("Bot 互联", systemImage: "point.3.connected.trianglepath.dotted")
                    }
                }
                NavigationLink(value: Nav.notifications) {
                    Label("通知", systemImage: "bell.badge")
                }
                Picker(selection: $appearance) {
                    Text("跟随系统").tag("system")
                    Text("浅色").tag("light")
                    Text("深色").tag("dark")
                } label: {
                    Label("外观", systemImage: "circle.lefthalf.filled")
                }
            }
            Section {
                HStack(spacing: 14) {
                    FrogMark(size: 52)
                    VStack(alignment: .leading, spacing: 2) {
                        Text("frog").font(.headline)
                        Text("版本 \(version)").font(.subheadline).monospacedDigit().foregroundStyle(Theme.fg3)
                    }
                    Spacer()
                    updateButton
                }
                .padding(.vertical, 4)
                if let note = updateNote {
                    Text(note.0).font(.footnote).foregroundStyle(note.1)
                }
                if let url = updateURL {
                    Link(destination: url) { Label("查看新版本", systemImage: "arrow.down.circle") }
                }
                Link(destination: URL(string: "https://github.com/llc370219/frog-bot")!) {
                    Label("项目主页", systemImage: "arrow.up.right.square")
                }
            } header: {
                Text("客户端")
            } footer: {
                Text("iOS 版用 Xcode 安装，不能在 App 里自动更新；有新版本时用 Xcode 重新安装一次即可，数据会保留。")
            }
        }
        .listStyle(.insetGrouped)
        .navigationTitle("设置")
        .toolbar { EditButton() }
    }

    @State private var checking = false
    @State private var updateNote: (String, Color)?
    @State private var updateURL: URL?

    private var version: String {
        Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "?"
    }

    private var updateButton: some View {
        Button(action: checkUpdate) {
            HStack(spacing: 5) {
                if checking {
                    ProgressView().controlSize(.small)
                } else {
                    Image(systemName: "arrow.triangle.2.circlepath")
                }
                Text("检查更新")
            }
        }
        .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
        .disabled(checking)
    }

    /// 对比 GitHub 上最新正式版的版本号
    private func checkUpdate() {
        checking = true
        updateNote = nil
        updateURL = nil
        Task {
            defer { checking = false }
            do {
                var req = URLRequest(url: URL(string: "https://api.github.com/repos/llc370219/frog-bot/releases/latest")!)
                req.setValue("application/vnd.github+json", forHTTPHeaderField: "Accept")
                req.timeoutInterval = 15
                let (data, _) = try await URLSession.shared.data(for: req)
                let j = JSON.parse(data) ?? .null
                let tag = j["tag_name"].str
                let latest = tag.hasPrefix("v") ? String(tag.dropFirst()) : tag
                guard !latest.isEmpty else { throw APIError.invalid("没读到最新版本号") }
                if latest.compare(version, options: .numeric) == .orderedDescending {
                    updateNote = ("有新版本 \(latest)，用 Xcode 重新安装即可更新。", Theme.accent)
                    updateURL = URL(string: j["html_url"].str)
                } else {
                    updateNote = ("已是最新版本", Theme.ok)
                }
                Haptics.tap()
            } catch {
                updateNote = ("检查失败：\(error.localizedDescription)", Theme.danger)
            }
        }
    }
}

@MainActor
func statusText(_ s: ServerEntry) -> String {
    switch s.status {
    case .online: return (s.config.isInsecure ? "在线 · HTTP 未加密 · " : "在线 · ") + (s.config.baseURL?.host ?? s.config.url)
    case .connecting: return "正在连接…"
    case .needsLogin: return "需要重新登录"
    case .offline(let e): return "离线：\(e)"
    }
}

@MainActor
func statusColor(_ s: ServerEntry) -> Color {
    switch s.status {
    case .online: return Theme.fg3
    case .connecting: return Theme.fg4
    case .needsLogin: return Theme.warn
    case .offline: return Theme.danger
    }
}

// MARK: - 一台服务器

struct ServerSettingsView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let server: ServerEntry
    @Binding var path: [Nav]

    @State private var name = ""
    @State private var update: UpdateStatus?
    @State private var checking = false
    @State private var updateError: String?
    @State private var changingPassword = false
    @State private var confirmLogout = false
    @State private var confirmRemove = false

    private var localBots: [BotProfile] { server.devices.first { $0.id == "local" }?.bots ?? [] }
    private var mainRef: BotRef? {
        localBots.first.map { BotRef(server: server.id, device: "local", bot: $0.id) }
    }

    var body: some View {
        List {
            Section {
                TextField("名称", text: $name)
                    .onSubmit { model.rename(server, to: name) }
                LabeledContent("地址", value: server.config.url)
                LabeledContent("状态") {
                    Text(statusText(server)).foregroundStyle(statusColor(server)).multilineTextAlignment(.trailing)
                }
                Button("编辑连接…") { model.sheet = .editServer(server.id) }
            } header: {
                Text("连接")
            } footer: {
                if server.config.isInsecure {
                    Text("这是明文 HTTP 连接，密码和聊天内容在网络上不加密。建议给服务器配一个域名并开启 HTTPS。")
                        .foregroundStyle(Theme.warn)
                }
            }

            Section("Bot") {
                ForEach(localBots) { b in
                    NavigationLink(value: Nav.bot(BotRef(server: server.id, device: "local", bot: b.id))) {
                        HStack(spacing: 12) {
                            BotAvatar(client: server.client, profile: b, size: 30)
                            Text(b.name)
                        }
                    }
                }
                if server.isOnline {
                    Button { model.sheet = .newBot(server: server.id, device: "local") } label: {
                        Label("新建 Bot", systemImage: "plus")
                    }
                }
            }

            if let mainRef, server.isOnline {
                Section {
                    NavigationLink(value: Nav.channels(mainRef)) { Label("API 渠道", systemImage: "powerplug") }
                } footer: {
                    Text("这台服务器上的所有 Bot 共用这些渠道；每个 Bot 用哪个模型、哪个 Agent，在 Bot 设置里各自选。")
                }
            }

            if server.authEnabled {
                Section("账号") {
                    LabeledContent("用户名", value: server.config.username)
                    Button("修改密码…") { changingPassword = true }
                        .disabled(!server.isOnline)
                    Button("退出登录", role: .destructive) { confirmLogout = true }
                }
            }

            Section {
                if let update {
                    LabeledContent("当前版本", value: update.current ?? "?")
                    if let latest = update.latest, !latest.isEmpty {
                        LabeledContent("最新版本", value: latest)
                    }
                    if update.state == "available", let s = update.release_url, let url = URL(string: s) {
                        Link("查看新版本", destination: url)
                    }
                }
                Button {
                    checkUpdate()
                } label: {
                    HStack {
                        Text("检查服务器更新")
                        if checking { Spacer(); ProgressView() }
                    }
                }
                .disabled(checking || !server.isOnline)
                if let updateError { Text(updateError).font(.footnote).foregroundStyle(Theme.danger) }
            } header: {
                Text("服务器版本")
            } footer: {
                Text("升级：在服务器上重新运行安装命令，数据会保留。")
            }

            Section {
                Button("从 App 中移除这台服务器", role: .destructive) { confirmRemove = true }
            } footer: {
                Text("只是从这台手机上移除，服务器上的 Bot、对话和任务都不受影响。")
            }
        }
        .listStyle(.insetGrouped)
        .navigationTitle(server.name)
        .navigationBarTitleDisplayMode(.inline)
        .onAppear { name = server.config.name }
        .onDisappear { if name != server.config.name { model.rename(server, to: name) } }
        .sheet(isPresented: $changingPassword) { PasswordSheet(server: server) }
        .confirmationDialog("退出「\(server.name)」的登录？", isPresented: $confirmLogout, titleVisibility: .visible) {
            Button("退出登录", role: .destructive) { Task { await model.logout(server) } }
        }
        .confirmationDialog("从 App 中移除「\(server.name)」？", isPresented: $confirmRemove, titleVisibility: .visible) {
            Button("移除", role: .destructive) {
                path.removeAll()
                model.remove(server)
            }
        } message: {
            Text("服务器上的数据不受影响，之后可以重新添加。")
        }
    }

    private func checkUpdate() {
        checking = true
        updateError = nil
        Task {
            do { update = try await model.checkUpdate(server) } catch { updateError = error.localizedDescription }
            checking = false
        }
    }
}

private struct PasswordSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let server: ServerEntry

    @State private var old = ""
    @State private var new = ""
    @State private var again = ""
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    SecureField("当前密码", text: $old).textContentType(.password)
                    SecureField("新密码（至少 8 位）", text: $new).textContentType(.newPassword)
                    SecureField("再输一次新密码", text: $again).textContentType(.newPassword)
                } footer: {
                    Text("修改后，其他设备上的登录都会失效，需要用新密码重新登录。")
                }
                if let error {
                    Section { Label(error, systemImage: "exclamationmark.circle.fill").foregroundStyle(Theme.danger) }
                }
            }
            .navigationTitle("修改密码")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("取消") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    if busy { ProgressView() } else {
                        Button("保存", action: save).disabled(old.isEmpty || new.count < 8 || new != again)
                    }
                }
            }
        }
    }

    private func save() {
        busy = true; error = nil
        Task {
            do {
                try await model.changePassword(server, old: old, new: new)
                Haptics.success()
                dismiss()
            } catch { self.error = error.localizedDescription }
            busy = false
        }
    }
}

// MARK: - Bot 互联

struct RelaySettingsView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let relay = model.relay
        List {
            Section {
                Toggle("允许这些服务器上的 Bot 互相发消息", isOn: Binding(get: { relay.enabled }, set: { relay.enabled = $0 }))
            } footer: {
                Text("聊天 Bot 之间只能互相发文字：问问对方记得什么，或者托付一件事让对方在它自己的机器上做。只在这个 App 开着、在前台时有效，消息由 App 转交，不排队；切到后台或关掉开关，对方马上联系不上。")
            }
            if model.servers.count < 2 {
                Section { Text("至少要添加两台服务器才能互联。").foregroundStyle(Theme.fg3) }
            }
            ForEach(model.servers) { s in
                Section {
                    Toggle(isOn: Binding(get: { relay.isOn(s) }, set: { relay.set(s, on: $0) })) {
                        VStack(alignment: .leading, spacing: 2) {
                            Text("这台服务器参与互联")
                            if let st = stateText(s) {
                                Text(st.0).font(.footnote).foregroundStyle(st.1)
                            }
                        }
                    }
                    ForEach(relay.localBots(s)) { b in
                        Toggle(isOn: Binding(get: { relay.isOn(s, bot: b.id) }, set: { relay.set(s, bot: b.id, on: $0) })) {
                            HStack(spacing: 10) {
                                BotAvatar(client: s.client, profile: b, size: 26)
                                Text(b.name)
                            }
                        }
                        .disabled(!relay.isOn(s))
                    }
                } header: {
                    Text(s.name)
                }
                .disabled(!relay.enabled)
            }
        }
        .listStyle(.insetGrouped)
        .navigationTitle("Bot 互联")
        .navigationBarTitleDisplayMode(.inline)
    }

    private func stateText(_ s: ServerEntry) -> (String, Color)? {
        let relay = model.relay
        guard relay.enabled, relay.isOn(s) else { return nil }
        switch relay.states[s.id] ?? .off {
        case .off: return s.isOnline ? nil : ("服务器不在线", Theme.fg4)
        case .connecting: return ("正在连接", Theme.fg4)
        case .waiting: return ("没有可联系的对象", Theme.fg4)
        case .active: return ("已接入", Theme.ok)
        case .unsupported: return ("服务器版本太旧，请先更新", Theme.warn)
        case .failed(let e): return (e, Theme.danger)
        }
    }
}

// MARK: - 通知

struct NotificationSettingsView: View {
    @Environment(AppModel.self) private var model
    @AppStorage(Prefs.notifyApproval) private var approval = true
    @AppStorage(Prefs.notifyTask) private var task = true
    @AppStorage(Prefs.notifyReply) private var reply = true
    @AppStorage(Prefs.appBadge) private var badge = true
    @State private var status: UNAuthorizationStatus?

    var body: some View {
        List {
            if status == .denied {
                Section {
                    Button("到系统设置里打开通知") {
                        if let url = URL(string: UIApplication.openNotificationSettingsURLString) { UIApplication.shared.open(url) }
                    }
                } footer: {
                    Text("系统设置里关掉了 frog 的通知，下面的选项暂时不起作用。")
                }
            }
            Section {
                Toggle("有操作等你确认", isOn: $approval)
                Toggle("后台任务结束", isOn: $task)
                Toggle("新回复", isOn: $reply)
            } footer: {
                Text("App 开着（包括刚切到后台的一小会儿）时提醒；正看着的那个对话不会重复提醒。iOS 会挂起后台的 App，所以完全退到后台后收不到提醒，回到 App 会自动补齐消息。")
            }
            Section {
                Toggle("App 图标上显示未读数", isOn: $badge)
                    .onChange(of: badge) { model.updateBadge() }
            }
        }
        .listStyle(.insetGrouped)
        .navigationTitle("通知")
        .navigationBarTitleDisplayMode(.inline)
        .task {
            status = await UNUserNotificationCenter.current().notificationSettings().authorizationStatus
        }
    }
}
