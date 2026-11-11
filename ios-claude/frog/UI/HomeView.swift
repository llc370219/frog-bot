import SwiftUI

/// 首页：每台服务器一组，下面是它（和它互联的设备）上的 Bot。
struct HomeView: View {
    @Environment(AppModel.self) private var model
    @Binding var path: [Nav]

    var body: some View {
        List {
            ForEach(model.servers) { s in
                ServerSection(server: s, path: $path)
            }
        }
        .listStyle(.insetGrouped)
        .navigationTitle("frog")
        .refreshable {
            await withTaskGroup(of: Void.self) { g in
                for s in model.servers { g.addTask { @MainActor in await model.refresh(s) } }
            }
        }
        .toolbar {
            ToolbarItem(placement: .topBarLeading) {
                Button { path.append(.settings) } label: { Image(systemName: "gearshape") }
                    .accessibilityLabel("设置")
            }
            ToolbarItem(placement: .topBarTrailing) {
                Menu {
                    Button { model.sheet = .addServer } label: { Label("添加服务器", systemImage: "server.rack") }
                    let online = model.servers.filter(\.isOnline)
                    if online.count == 1, let s = online.first {
                        Button { model.sheet = .newBot(server: s.id, device: "local") } label: { Label("新建 Bot", systemImage: "plus.bubble") }
                    } else if online.count > 1 {
                        Menu {
                            ForEach(online) { s in
                                Button(s.name) { model.sheet = .newBot(server: s.id, device: "local") }
                            }
                        } label: { Label("新建 Bot", systemImage: "plus.bubble") }
                    }
                } label: { Image(systemName: "plus") }
                .accessibilityLabel("添加")
            }
        }
    }
}

private struct ServerSection: View {
    @Environment(AppModel.self) private var model
    let server: ServerEntry
    @Binding var path: [Nav]

    var body: some View {
        Section {
            switch server.status {
            case .needsLogin:
                Button { model.sheet = .login(server.id) } label: {
                    Label("登录已过期，点这里重新登录", systemImage: "person.badge.key")
                }
            case .offline(let why):
                VStack(alignment: .leading, spacing: 4) {
                    Label("连不上这台服务器", systemImage: "wifi.exclamationmark").foregroundStyle(Theme.danger)
                    Text(why).font(.footnote).foregroundStyle(Theme.fg3).lineLimit(2)
                }
                botRows
            case .connecting:
                if server.devices.isEmpty {
                    HStack { ProgressView(); Text("正在连接…").foregroundStyle(Theme.fg3) }
                } else {
                    botRows
                }
            case .online:
                botRows
            }
        } header: {
            header
        }
    }

    @ViewBuilder
    private var botRows: some View {
        ForEach(server.devices) { g in
            if g.id != "local" {
                HStack(spacing: 6) {
                    Image(systemName: "link").font(.caption)
                    Text(g.name).font(.subheadline.weight(.medium))
                    if !g.online { Text("· 离线").font(.subheadline) }
                }
                .foregroundStyle(Theme.fg3)
                .listRowBackground(Color.clear)
                .listRowInsets(EdgeInsets(top: 10, leading: 20, bottom: 2, trailing: 20))
            }
            ForEach(g.bots) { b in
                let ref = BotRef(server: server.id, device: g.id, bot: b.id)
                NavigationLink(value: Nav.chat(ref)) {
                    BotRow(server: server, profile: b, ref: ref, deviceOnline: g.online)
                }
                .swipeActions(edge: .trailing) {
                    Button { path.append(.bot(ref)) } label: { Label("设置", systemImage: "slider.horizontal.3") }
                        .tint(Theme.fg3)
                }
                .contextMenu {
                    Button { path.append(.bot(ref)) } label: { Label("Bot 设置", systemImage: "slider.horizontal.3") }
                    Button { path.append(.scheduler(ref)) } label: { Label("自动调度器", systemImage: "calendar.badge.clock") }
                    Divider()
                    Button { path.append(.server(server.id)) } label: { Label("服务器设置", systemImage: "server.rack") }
                    if server.isOnline {
                        Button { model.sheet = .newBot(server: server.id, device: "local") } label: { Label("在这台服务器上新建 Bot", systemImage: "plus.bubble") }
                    }
                }
            }
            if g.id == "local" && g.bots.isEmpty {
                Text("这台服务器上还没有 Bot").foregroundStyle(Theme.fg3)
            }
        }
    }

    // 分组标题只放名字和状态点：在 List 的分组标题里放菜单或弹性布局会让 iOS 反复测量、卡死在启动画面
    private var header: some View {
        HStack(spacing: 6) {
            Circle().fill(dot).frame(width: 7, height: 7)
            Text(server.name).textCase(nil)
            if server.config.isInsecure {
                Image(systemName: "lock.open.fill").font(.caption2).foregroundStyle(Theme.warn)
                    .accessibilityLabel("HTTP 未加密")
            }
        }
    }

    private var dot: Color {
        switch server.status {
        case .online: return Theme.ok
        case .connecting: return Theme.fg4
        case .needsLogin: return Theme.warnDot
        case .offline: return Theme.danger
        }
    }
}

/// 列表里的一个 Bot：头像、名字、最后一句话、状态（在干活 / 等你确认）、未读数
private struct BotRow: View {
    @Environment(AppModel.self) private var model
    let server: ServerEntry
    let profile: BotProfile
    let ref: BotRef
    let deviceOnline: Bool

    var body: some View {
        let session = model.session(ref)
        let unread = model.unread[ref] ?? 0
        HStack(spacing: 12) {
            BotAvatar(client: server.client, profile: profile, size: 44)
                .opacity(deviceOnline ? 1 : 0.5)
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 6) {
                    Text(profile.name)
                        .font(.system(size: 16, weight: .semibold))
                        .foregroundStyle(Theme.fg)
                        .lineLimit(1)
                    Spacer(minLength: 4)
                    if let t = last(session)?.created, t > 0 {
                        Text(Fmt.localTime(t)).font(.system(size: 12)).foregroundStyle(Theme.fg4).monospacedDigit()
                    }
                }
                HStack(spacing: 6) {
                    status(session)
                    Spacer(minLength: 4)
                    if unread > 0 {
                        Text(unread > 99 ? "99+" : "\(unread)")
                            .font(.system(size: 12, weight: .semibold))
                            .monospacedDigit()
                            .foregroundStyle(Theme.onAccent)
                            .padding(.horizontal, 6)
                            .frame(minWidth: 20, minHeight: 20)
                            .background(Theme.accent, in: Capsule())
                    }
                }
            }
        }
        .padding(.vertical, 3)
    }

    @ViewBuilder
    private func status(_ session: BotSession?) -> some View {
        if let session, session.pendingApprovals > 0 {
            Label("\(session.pendingApprovals) 个操作等你确认", systemImage: "exclamationmark.shield.fill")
                .font(.system(size: 13.5, weight: .medium))
                .foregroundStyle(Theme.warn)
                .lineLimit(1)
        } else if let session, session.busy {
            HStack(spacing: 5) { TypingDots(size: 4.5); Text("正在回复…") }
                .font(.system(size: 13.5))
                .foregroundStyle(Theme.accent)
        } else if let session, let lead = session.running.first {
            HStack(spacing: 5) {
                Spinner(size: 11, tint: Theme.accent)
                Text(lead.title).lineLimit(1)
            }
            .font(.system(size: 13.5))
            .foregroundStyle(Theme.fg2)
        } else if !deviceOnline {
            Text("设备离线").font(.system(size: 13.5)).foregroundStyle(Theme.fg4)
        } else {
            Text(preview(session))
                .font(.system(size: 13.5))
                .foregroundStyle(Theme.fg3)
                .lineLimit(1)
        }
    }

    private func last(_ session: BotSession?) -> Msg? {
        guard let session else { return nil }
        for item in session.items.reversed() where item.kind == .msg {
            if let m = session.msgs[item.id] { return m }
        }
        return nil
    }

    private func preview(_ session: BotSession?) -> String {
        guard let m = last(session) else {
            if let ins = profile.instructions?.trimmingCharacters(in: .whitespacesAndNewlines), !ins.isEmpty { return ins }
            return session?.ready == false ? "正在连接…" : "还没有对话"
        }
        let text = m.text.replacingOccurrences(of: "\n", with: " ").replacingOccurrences(of: "[*`#>]", with: "", options: .regularExpression)
        let body = text.isEmpty ? (m.files.isEmpty ? "…" : "[附件]") : text
        return m.role == "user" ? "你：" + body : body
    }
}
