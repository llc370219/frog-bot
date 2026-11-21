import SwiftUI
import UIKit
import UserNotifications

/// 设置首页：主机、Bot 互联、通知、外观、版本
struct SettingsView: View {
    @Environment(AppModel.self) private var model
    @Binding var path: [Nav]
    @AppStorage(Prefs.appearance) private var appearance = "system"
    @AppStorage("client.update.channel") private var clientChannel = "stable"

    @State private var checking = false
    @State private var latestTag: String?
    @State private var updateAvailable = false
    @State private var updateURL: URL?
    @State private var checkError: String?

    var body: some View {
        List {
            Section("主机") {
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
                Button { model.sheet = .addServer } label: { Label("连接新主机", systemImage: "plus") }
            }

            Section {
                NavigationLink(value: Nav.relay) {
                    LabeledContent {
                        Text(model.relay.enabled ? "已开启" : "已关闭")
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

            Section("关于") {
                HStack(spacing: 14) {
                    FrogMark(size: 46)
                    VStack(alignment: .leading, spacing: 2) {
                        Text("frog").font(.headline)
                        Text("v\(version)").font(.subheadline).monospacedDigit().foregroundStyle(Theme.fg3)
                    }
                    Spacer()
                    Picker("通道", selection: $clientChannel) {
                        Text("正式版").tag("stable")
                        Text("Beta").tag("beta")
                    }
                    .pickerStyle(.segmented)
                    .frame(width: 132)
                    .onChange(of: clientChannel) { _, _ in checkClientUpdate() }
                }
                .padding(.vertical, 2)

                HStack {
                    Text(clientChannel == "beta" ? "最新 Beta" : "最新正式版")
                    Spacer()
                    if checking {
                        ProgressView().controlSize(.small)
                    } else if let latestTag {
                        Text(latestTag)
                            .monospacedDigit()
                            .foregroundStyle(updateAvailable ? Theme.accent : Theme.fg3)
                    } else {
                        Button("检查") { checkClientUpdate() }
                            .font(.subheadline)
                    }
                }

                if updateAvailable, let url = updateURL {
                    Link(destination: url) {
                        Label("查看 \(latestTag ?? "新版本")", systemImage: "arrow.up.right.circle")
                    }
                }
                if let checkError {
                    Text(checkError).font(.footnote).foregroundStyle(Theme.danger)
                }
                Link(destination: URL(string: "https://github.com/llc370219/frog-bot")!) {
                    Label("GitHub", systemImage: "arrow.up.right.square")
                }
            }
        }
        .listStyle(.insetGrouped)
        .navigationTitle("设置")
        .toolbar { EditButton() }
        .task { checkClientUpdate() }
    }

    private var version: String {
        Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "?"
    }

    private func checkClientUpdate() {
        checking = true
        checkError = nil
        let isBeta = clientChannel == "beta"
        Task {
            defer { checking = false }
            do {
                let endpoint = isBeta
                    ? "https://api.github.com/repos/llc370219/frog-bot/releases?per_page=20"
                    : "https://api.github.com/repos/llc370219/frog-bot/releases/latest"
                var req = URLRequest(url: URL(string: endpoint)!)
                req.setValue("application/vnd.github+json", forHTTPHeaderField: "Accept")
                req.timeoutInterval = 12
                let (data, _) = try await URLSession.shared.data(for: req)
                let parsed = JSON.parse(data) ?? .null
                let chosen: JSON
                if isBeta {
                    chosen = parsed.array.first(where: { !$0["draft"].bool && $0["tag_name"].str.hasPrefix("v") }) ?? .null
                } else {
                    chosen = parsed
                }
                let tag = chosen["tag_name"].str
                guard !tag.isEmpty else {
                    latestTag = "—"
                    updateAvailable = false
                    return
                }
                latestTag = tag
                updateURL = URL(string: chosen["html_url"].str)
                let cleanLatest = tag.hasPrefix("v") ? String(tag.dropFirst()) : tag
                updateAvailable = cleanLatest != version && cleanLatest.compare(version, options: .numeric) == .orderedDescending
            } catch {
                checkError = error.localizedDescription
            }
        }
    }
}

@MainActor
func statusText(_ s: ServerEntry) -> String {
    switch s.status {
    case .online:
        if let badge = s.config.tunnelBadge {
            return "\(badge) · \(s.config.displayEndpoint)"
        }
        return s.config.displayEndpoint
    case .connecting: return "连接中…"
    case .needsLogin: return "需要验证"
    case .offline: return "离线"
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

// MARK: - 单台主机设置

struct ServerSettingsView: View {
    @Environment(AppModel.self) private var model
    let server: ServerEntry
    @Binding var path: [Nav]

    @State private var name = ""
    @State private var channel = "stable"
    @State private var update: UpdateStatus?
    @State private var checking = false
    @State private var upgrading = false
    @State private var upgradeNote: String?
    @State private var updateError: String?
    @State private var askUpgradePassword = false
    @State private var upgradePassword = ""
    @State private var changingPassword = false
    @State private var confirmLogout = false
    @State private var confirmRemove = false

    private var localBots: [BotProfile] { server.devices.first { $0.id == "local" }?.bots ?? [] }
    private var mainRef: BotRef? {
        localBots.first.map { BotRef(server: server.id, device: "local", bot: $0.id) }
    }

    var body: some View {
        List {
            Section("连接") {
                TextField("名称", text: $name)
                    .onSubmit { model.rename(server, to: name) }
                if let badge = server.config.tunnelBadge {
                    LabeledContent("协议") {
                        Label(badge, systemImage: "lock.shield.fill")
                            .font(.subheadline)
                            .foregroundStyle(Theme.accent)
                    }
                }
                LabeledContent("节点") {
                    HStack(spacing: 5) {
                        if server.config.isInsecure {
                            Image(systemName: "lock.open.fill")
                                .font(.caption2)
                                .foregroundStyle(Theme.warn)
                        }
                        Text(server.config.displayEndpoint)
                            .font(.system(.subheadline, design: .monospaced))
                            .foregroundStyle(Theme.fg3)
                            .lineLimit(1)
                    }
                }
                LabeledContent("状态") {
                    Text(server.isOnline ? "在线" : statusText(server)).foregroundStyle(statusColor(server))
                }
                Button("更换连接…") { model.sheet = .editServer(server.id) }
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
                if let mainRef, server.isOnline {
                    NavigationLink(value: Nav.channels(mainRef)) { Label("API 渠道", systemImage: "powerplug") }
                }
            }

            Section("版本") {
                HStack {
                    Text("更新通道")
                    Spacer()
                    Picker("更新通道", selection: $channel) {
                        Text("正式版").tag("stable")
                        Text("Beta").tag("beta")
                    }
                    .pickerStyle(.segmented)
                    .frame(width: 140)
                    .disabled(!server.isOnline || checking || upgrading)
                    .onChange(of: channel) { _, next in
                        if update?.channel != next {
                            checkServerUpdate(targetChannel: next)
                        }
                    }
                }

                LabeledContent("当前版本", value: update?.current ?? "—")
                LabeledContent(channel == "beta" ? "最新 Beta" : "最新正式版") {
                    if checking {
                        ProgressView().controlSize(.small)
                    } else {
                        Text(update?.latest ?? "—").foregroundStyle(Theme.fg3)
                    }
                }

                if update?.state == "available", let latest = update?.latest, !latest.isEmpty {
                    if update?.can_apply == true {
                        Button {
                            triggerUpgrade(password: "")
                        } label: {
                            HStack {
                                Label(upgrading ? "正在升级…" : "升级至 \(latest)", systemImage: "arrow.down.circle.fill")
                                    .fontWeight(.medium)
                                if upgrading { Spacer(); ProgressView() }
                            }
                        }
                        .disabled(upgrading || !server.isOnline)
                    } else if let s = update?.release_url, let url = URL(string: s) {
                        Link("查看 \(latest)", destination: url)
                    }
                } else {
                    Button("刷新版本信息") { checkServerUpdate(targetChannel: channel) }
                        .disabled(checking || upgrading || !server.isOnline)
                }

                if let upgradeNote {
                    Text(upgradeNote).font(.footnote).foregroundStyle(Theme.ok)
                }
                if let updateError {
                    Text(updateError).font(.footnote).foregroundStyle(Theme.danger)
                }
            }

            if server.authEnabled && !server.config.isTunneled {
                Section("账号") {
                    LabeledContent("用户", value: server.config.username)
                    Button("修改密码…") { changingPassword = true }
                        .disabled(!server.isOnline)
                    Button("退出登录", role: .destructive) { confirmLogout = true }
                }
            }

            Section {
                Button("移除主机", role: .destructive) { confirmRemove = true }
            }
        }
        .listStyle(.insetGrouped)
        .navigationTitle(server.name)
        .navigationBarTitleDisplayMode(.inline)
        .onAppear {
            name = server.config.name
            if server.isOnline && update == nil {
                checkServerUpdate(targetChannel: nil)
            }
        }
        .onDisappear { if name != server.config.name { model.rename(server, to: name) } }
        .sheet(isPresented: $changingPassword) { PasswordSheet(server: server) }
        .alert("输入管理员密码升级", isPresented: $askUpgradePassword) {
            SecureField("密码", text: $upgradePassword)
            Button("取消", role: .cancel) { upgradePassword = "" }
            Button("升级") {
                let pw = upgradePassword
                upgradePassword = ""
                triggerUpgrade(password: pw)
            }
        }
        .confirmationDialog("退出「\(server.name)」？", isPresented: $confirmLogout, titleVisibility: .visible) {
            Button("退出登录", role: .destructive) { Task { await model.logout(server) } }
        }
        .confirmationDialog("移除「\(server.name)」？", isPresented: $confirmRemove, titleVisibility: .visible) {
            Button("移除", role: .destructive) {
                path.removeAll()
                model.remove(server)
            }
        }
    }

    private func checkServerUpdate(targetChannel: String?) {
        checking = true
        updateError = nil
        upgradeNote = nil
        Task {
            do {
                let res = try await model.checkUpdate(server, channel: targetChannel)
                update = res
                if let ch = res.channel, !ch.isEmpty {
                    channel = ch
                }
            } catch {
                updateError = error.localizedDescription
            }
            checking = false
        }
    }

    private func triggerUpgrade(password: String) {
        upgrading = true
        updateError = nil
        upgradeNote = nil
        Task {
            do {
                let out = try await model.applyUpdate(server, channel: channel, password: password)
                if out["need_password"].bool {
                    upgrading = false
                    askUpgradePassword = true
                    return
                }
                Haptics.success()
                upgradeNote = "正在重启更新，几秒后自动重连…"
                try? await Task.sleep(nanoseconds: 4_000_000_000)
                await model.refresh(server)
                checkServerUpdate(targetChannel: channel)
            } catch {
                updateError = error.localizedDescription
                Haptics.error()
            }
            upgrading = false
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
                    SecureField("确认新密码", text: $again).textContentType(.newPassword)
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
                Toggle("开启跨主机 Bot 互联", isOn: Binding(get: { relay.enabled }, set: { relay.enabled = $0 }))
            }
            if model.servers.count < 2 {
                Section { Text("需连接两台及以上主机").foregroundStyle(Theme.fg3) }
            }
            ForEach(model.servers) { s in
                Section {
                    Toggle(isOn: Binding(get: { relay.isOn(s) }, set: { relay.set(s, on: $0) })) {
                        VStack(alignment: .leading, spacing: 2) {
                            Text("接入此主机")
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
        case .off: return s.isOnline ? nil : ("离线", Theme.fg4)
        case .connecting: return ("连接中", Theme.fg4)
        case .waiting: return ("待机", Theme.fg4)
        case .active: return ("已接入", Theme.ok)
        case .unsupported: return ("需更新服务端", Theme.warn)
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
                    Button("前往系统设置开启通知") {
                        if let url = URL(string: UIApplication.openNotificationSettingsURLString) { UIApplication.shared.open(url) }
                    }
                }
            }
            Section {
                Toggle("操作确认", isOn: $approval)
                Toggle("任务完成", isOn: $task)
                Toggle("新消息", isOn: $reply)
            }
            Section {
                Toggle("图标未读角标", isOn: $badge)
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
