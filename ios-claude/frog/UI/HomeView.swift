import SwiftUI

enum HomeMode: String, Hashable {
    case bot, coding
}

/// 首页：参考 Grok Bot 极简纯白/纯黑无分割线排版 —— 顶部左右悬浮圆钮 + 中间大呼吸感会话行 + 底部悬浮模式切换圆钮与胶囊搜索框。
struct HomeView: View {
    @Environment(AppModel.self) private var model
    @Binding var path: [Nav]

    @AppStorage("home.mode") private var mode: HomeMode = .bot
    @AppStorage("home.server") private var selectedServerID = ""
    @State private var searchText = ""
    @State private var createSessionTick = 0
    @State private var createProjectTick = 0
    @FocusState private var searchFocused: Bool

    private var activeServer: ServerEntry? {
        model.servers.first { $0.id.uuidString == selectedServerID } ?? model.servers.first
    }

    var body: some View {
        ZStack {
            Theme.canvas.ignoresSafeArea()

            if let server = activeServer {
                VStack(spacing: 0) {
                    topHeader(server: server)

                    switch mode {
                    case .bot:
                        BotModeFeed(server: server, searchText: searchText, path: $path)
                            .id(server.id)
                    case .coding:
                        CodingWorkspaceView(
                            server: server,
                            path: $path,
                            searchText: searchText,
                            createSessionTick: createSessionTick,
                            createProjectTick: createProjectTick
                        )
                        .id(server.id)
                    }
                }
            } else {
                ContentUnavailableView("还没有连接主机", systemImage: "server.rack")
            }
        }
        .toolbar(.hidden, for: .navigationBar)
        .safeAreaInset(edge: .bottom, spacing: 0) {
            bottomFloatingBar
        }
    }

    // MARK: - 顶部悬浮设置按钮与新建按钮

    private func topHeader(server: ServerEntry) -> some View {
        HStack(spacing: 12) {
            if model.servers.count > 1 {
                Menu {
                    Button { path.append(.settings) } label: {
                        Label("全部设置", systemImage: "gearshape")
                    }
                    Divider()
                    Section("切换当前主机") {
                        ForEach(model.servers) { srv in
                            Button {
                                Haptics.tap()
                                selectedServerID = srv.id.uuidString
                            } label: {
                                if srv.id == server.id {
                                    Label(srv.name, systemImage: "checkmark")
                                } else {
                                    Text(srv.name)
                                }
                            }
                        }
                    }
                } label: {
                    settingsCircleButton(for: server)
                }
                .accessibilityLabel("设置与切换主机")
            } else {
                Button {
                    Haptics.tap()
                    path.append(.settings)
                } label: {
                    settingsCircleButton(for: server)
                }
                .buttonStyle(.plain)
                .contextMenu {
                    Button { path.append(.server(server.id)) } label: { Label("当前主机设置", systemImage: "server.rack") }
                    Button { model.sheet = .addServer } label: { Label("连接新主机", systemImage: "plus.circle") }
                }
                .accessibilityLabel("设置")
            }

            Spacer()

            if mode == .coding {
                HStack(spacing: 5) {
                    Image(systemName: "chevron.left.forwardslash.chevron.right")
                        .font(.system(size: 12, weight: .bold))
                        .foregroundStyle(Theme.accent)
                    Text("Coding")
                        .font(.system(size: 15, weight: .semibold))
                        .foregroundStyle(Theme.fg)
                }
                .padding(.horizontal, 12)
                .padding(.vertical, 6)
                .background(Theme.s2, in: Capsule())

                Spacer()
            }

            if mode == .bot {
                Menu {
                    if server.isOnline {
                        Button { model.sheet = .newBot(server: server.id, device: "local") } label: {
                            Label("新建 Bot", systemImage: "plus.bubble")
                        }
                    }
                    Button { model.sheet = .addServer } label: {
                        Label("连接新主机 / 扫码配对", systemImage: "qrcode.viewfinder")
                    }
                } label: {
                    circleIconButton(systemName: "plus", fontSize: 20, weight: .regular)
                } primaryAction: {
                    Haptics.tap()
                    if server.isOnline {
                        model.sheet = .newBot(server: server.id, device: "local")
                    } else {
                        model.sheet = .addServer
                    }
                }
                .accessibilityLabel("新建")
            } else {
                Menu {
                    if server.isOnline {
                        Button { createSessionTick += 1 } label: {
                            Label("新建 Coding 对话", systemImage: "plus.message")
                        }
                        Button { createProjectTick += 1 } label: {
                            Label("新建 / 关联项目文件夹", systemImage: "folder.badge.plus")
                        }
                        Divider()
                    }
                    Button { model.sheet = .addServer } label: {
                        Label("连接新主机 / 扫码配对", systemImage: "qrcode.viewfinder")
                    }
                } label: {
                    circleIconButton(systemName: "plus", fontSize: 20, weight: .regular)
                } primaryAction: {
                    Haptics.tap()
                    if server.isOnline {
                        createSessionTick += 1
                    } else {
                        model.sheet = .addServer
                    }
                }
                .accessibilityLabel("新建 Coding 对话")
            }
        }
        .padding(.horizontal, 20)
        .padding(.top, 8)
        .padding(.bottom, 10)
    }

    private func settingsCircleButton(for server: ServerEntry) -> some View {
        ZStack(alignment: .topTrailing) {
            circleIconButton(systemName: "gearshape", fontSize: 18, weight: .regular)
            if !server.isOnline {
                Circle()
                    .fill(statusDot(for: server))
                    .frame(width: 10, height: 10)
                    .overlay(Circle().strokeBorder(Theme.canvas, lineWidth: 2))
                    .offset(x: 1, y: -1)
            }
        }
    }

    private func circleIconButton(systemName: String, fontSize: CGFloat, weight: Font.Weight) -> some View {
        Image(systemName: systemName)
            .font(.system(size: fontSize, weight: weight))
            .foregroundStyle(Theme.fg)
            .frame(width: 44, height: 44)
            .background(
                Circle()
                    .fill(Theme.canvas)
                    .shadow(color: .black.opacity(0.06), radius: 8, x: 0, y: 2)
            )
            .overlay(Circle().strokeBorder(Theme.line, lineWidth: 1))
    }

    private func statusDot(for s: ServerEntry) -> Color {
        switch s.status {
        case .online: return Theme.ok
        case .connecting: return Theme.fg4
        case .needsLogin: return Theme.warnDot
        case .offline: return Theme.danger
        }
    }

    // MARK: - 底部悬浮栏（左侧 </> 代码切换圆钮 + 右侧胶囊搜索框）

    private var bottomFloatingBar: some View {
        HStack(spacing: 12) {
            Button {
                Haptics.tap()
                withAnimation(.snappy(duration: 0.22)) {
                    mode = (mode == .bot) ? .coding : .bot
                }
            } label: {
                Image(systemName: "chevron.left.forwardslash.chevron.right")
                    .font(.system(size: 16.5, weight: .semibold))
                    .foregroundStyle(mode == .coding ? Theme.onAccent : Theme.fg)
                    .frame(width: 48, height: 48)
                    .background(
                        Circle()
                            .fill(mode == .coding ? Theme.accent : Theme.canvas.opacity(0.95))
                            .shadow(color: .black.opacity(0.07), radius: 10, x: 0, y: 3)
                    )
                    .overlay(Circle().strokeBorder(mode == .coding ? Color.clear : Theme.line, lineWidth: 1))
            }
            .buttonStyle(.plain)
            .contextMenu {
                Button {
                    mode = .bot
                } label: {
                    if mode == .bot { Label("Bot 聊天模式", systemImage: "checkmark") }
                    else { Label("切换到 Bot 聊天模式", systemImage: "bubble.left.and.bubble.right") }
                }
                Button {
                    mode = .coding
                } label: {
                    if mode == .coding { Label("Coding 工作区模式", systemImage: "checkmark") }
                    else { Label("切换到 Coding 工作区模式", systemImage: "terminal") }
                }
            }
            .accessibilityLabel(mode == .bot ? "切换到 Coding 模式" : "切换到 Bot 模式")

            HStack(spacing: 8) {
                Image(systemName: "magnifyingglass")
                    .font(.system(size: 16, weight: .medium))
                    .foregroundStyle(Theme.fg2)
                TextField(mode == .bot ? "搜索" : "搜索项目或对话", text: $searchText)
                    .font(.system(size: 16))
                    .foregroundStyle(Theme.fg)
                    .focused($searchFocused)
                    .submitLabel(.search)
                if !searchText.isEmpty {
                    Button {
                        searchText = ""
                    } label: {
                        Image(systemName: "xmark.circle.fill")
                            .font(.system(size: 16))
                            .foregroundStyle(Theme.fg4)
                    }
                    .buttonStyle(.plain)
                }
            }
            .padding(.horizontal, 16)
            .frame(height: 48)
            .background(
                Capsule()
                    .fill(Theme.canvas.opacity(0.95))
                    .shadow(color: .black.opacity(0.06), radius: 10, x: 0, y: 3)
            )
            .overlay(Capsule().strokeBorder(Theme.line, lineWidth: 1))
        }
        .padding(.horizontal, 20)
        .padding(.top, 8)
        .padding(.bottom, 8)
        .background(
            LinearGradient(
                colors: [Theme.canvas.opacity(0), Theme.canvas.opacity(0.92), Theme.canvas],
                startPoint: .top,
                endPoint: .bottom
            )
            .ignoresSafeArea()
        )
    }
}

// MARK: - 纯净无边框 Bot 流（Grok Bot 排版）

private struct BotModeFeed: View {
    @Environment(AppModel.self) private var model
    let server: ServerEntry
    let searchText: String
    @Binding var path: [Nav]

    private struct FlatBotItem: Identifiable {
        let ref: BotRef
        let profile: BotProfile
        let deviceName: String
        let isPeer: Bool
        let online: Bool
        var id: String { ref.key }
    }

    private var filteredBots: [FlatBotItem] {
        var all: [FlatBotItem] = []
        for g in server.devices {
            for b in g.bots {
                let ref = BotRef(server: server.id, device: g.id, bot: b.id)
                all.append(FlatBotItem(
                    ref: ref,
                    profile: b,
                    deviceName: g.name,
                    isPeer: g.id != "local",
                    online: g.online
                ))
            }
        }
        let q = searchText.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        guard !q.isEmpty else { return all }
        return all.filter { item in
            if item.profile.name.lowercased().contains(q) { return true }
            if let ins = item.profile.instructions?.lowercased(), ins.contains(q) { return true }
            if let sess = model.session(item.ref) {
                for tl in sess.items.reversed().prefix(5) where tl.kind == .msg {
                    if let m = sess.msgs[tl.id], m.text.lowercased().contains(q) { return true }
                }
            }
            return false
        }
    }

    var body: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 0) {
                statusBanner

                let items = filteredBots
                if items.isEmpty {
                    emptyView
                } else {
                    ForEach(items) { item in
                        Button {
                            path.append(.chat(item.ref))
                        } label: {
                            GrokBotRow(
                                server: server,
                                profile: item.profile,
                                ref: item.ref,
                                peerDeviceName: item.isPeer ? item.deviceName : nil,
                                deviceOnline: item.online
                            )
                        }
                        .buttonStyle(RowPressStyle())
                        .contextMenu {
                            Button { path.append(.bot(item.ref)) } label: { Label("Bot 设置", systemImage: "slider.horizontal.3") }
                            Button { path.append(.scheduler(item.ref)) } label: { Label("自动调度器", systemImage: "calendar.badge.clock") }
                            Divider()
                            Button { path.append(.server(server.id)) } label: { Label("主机设置", systemImage: "server.rack") }
                        }
                    }
                }
            }
            .padding(.top, 4)
            .padding(.bottom, 16)
        }
        .scrollDismissesKeyboard(.interactively)
        .refreshable { await model.refresh(server) }
    }

    @ViewBuilder
    private var statusBanner: some View {
        switch server.status {
        case .needsLogin:
            Button { model.sheet = .login(server.id) } label: {
                HStack(spacing: 10) {
                    Image(systemName: "person.badge.key.fill").foregroundStyle(Theme.warn)
                    Text("「\(server.name)」授权已过期，点击重新验证")
                        .font(.system(size: 14, weight: .medium))
                        .foregroundStyle(Theme.fg)
                    Spacer()
                    Image(systemName: "chevron.right").font(.caption).foregroundStyle(Theme.fg4)
                }
                .padding(.horizontal, 16)
                .padding(.vertical, 12)
                .background(Theme.warnSoft, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
                .padding(.horizontal, 20)
                .padding(.bottom, 10)
            }
            .buttonStyle(.plain)

        case .offline(let why):
            Button { model.sheet = .editServer(server.id) } label: {
                VStack(alignment: .leading, spacing: 3) {
                    Label("无法连接「\(server.name)」", systemImage: "wifi.exclamationmark")
                        .font(.system(size: 14, weight: .semibold))
                        .foregroundStyle(Theme.danger)
                    Text(why).font(.system(size: 12.5)).foregroundStyle(Theme.fg3).lineLimit(2)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 16)
                .padding(.vertical, 12)
                .background(Theme.dangerSoft, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
                .padding(.horizontal, 20)
                .padding(.bottom, 10)
            }
            .buttonStyle(.plain)

        case .connecting where server.devices.isEmpty:
            HStack(spacing: 10) {
                ProgressView()
                Text("正在连接…").font(.system(size: 14)).foregroundStyle(Theme.fg3)
            }
            .padding(.horizontal, 20)
            .padding(.vertical, 24)

        default:
            EmptyView()
        }
    }

    @ViewBuilder
    private var emptyView: some View {
        if !searchText.trimmingCharacters(in: .whitespaces).isEmpty {
            VStack(spacing: 8) {
                Text("没有匹配的 Bot")
                    .font(.system(size: 16, weight: .medium))
                    .foregroundStyle(Theme.fg3)
            }
            .frame(maxWidth: .infinity)
            .padding(.top, 80)
        } else if server.isOnline {
            VStack(spacing: 14) {
                Text("还没有 Bot")
                    .font(.system(size: 17, weight: .semibold))
                    .foregroundStyle(Theme.fg2)
                Button {
                    model.sheet = .newBot(server: server.id, device: "local")
                } label: {
                    Label("新建第一个 Bot", systemImage: "plus")
                }
                .buttonStyle(PillButtonStyle(kind: .primary, compact: true))
            }
            .frame(maxWidth: .infinity)
            .padding(.top, 80)
        }
    }
}

struct RowPressStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .background(configuration.isPressed ? Theme.s2.opacity(0.85) : Color.clear)
            .contentShape(Rectangle())
    }
}

/// 单个 Bot 行：52pt 纯白圆角线条头像 + 右下角状态小角标 + 17pt 粗体标题 + 智能日期 + 15pt 灰色摘要 + 右侧未读圆点
private struct GrokBotRow: View {
    @Environment(AppModel.self) private var model
    let server: ServerEntry
    let profile: BotProfile
    let ref: BotRef
    let peerDeviceName: String?
    let deviceOnline: Bool

    var body: some View {
        let session = model.session(ref)
        let unread = model.unread[ref] ?? 0
        let pending = session?.pendingApprovals ?? 0
        let isWorking = (session?.busy == true) || (session?.running.isEmpty == false)

        HStack(spacing: 15) {
            ZStack(alignment: .bottomTrailing) {
                BotAvatar(client: server.client, profile: profile, size: 52)
                    .opacity(deviceOnline ? 1 : 0.5)

                if pending > 0 {
                    Circle()
                        .fill(Theme.warnDot)
                        .frame(width: 18, height: 18)
                        .overlay {
                            Image(systemName: "star.fill")
                                .font(.system(size: 9, weight: .bold))
                                .foregroundStyle(.white)
                        }
                        .overlay(Circle().strokeBorder(Theme.canvas, lineWidth: 2))
                        .offset(x: 2, y: 2)
                } else if isWorking {
                    Circle()
                        .fill(Theme.accent)
                        .frame(width: 15, height: 15)
                        .overlay(Circle().strokeBorder(Theme.canvas, lineWidth: 2.5))
                        .offset(x: 1, y: 1)
                }
            }

            VStack(alignment: .leading, spacing: 5) {
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    Text(profile.name)
                        .font(.system(size: 17, weight: .semibold))
                        .foregroundStyle(Theme.fg)
                        .lineLimit(1)

                    if let peerDeviceName {
                        Text(peerDeviceName)
                            .font(.system(size: 11, weight: .medium))
                            .foregroundStyle(Theme.fg4)
                            .padding(.horizontal, 5)
                            .padding(.vertical, 1)
                            .background(Theme.s2, in: Capsule())
                    }

                    Spacer(minLength: 4)

                    if let t = last(session)?.created, t > 0 {
                        Text(Fmt.chatListTime(t))
                            .font(.system(size: 13.5))
                            .foregroundStyle(Theme.fg4)
                    }
                }

                HStack(spacing: 8) {
                    subtitle(session)
                    Spacer(minLength: 4)
                    if unread > 0 {
                        Circle()
                            .fill(Color(red: 0.18, green: 0.49, blue: 0.96))
                            .frame(width: 9, height: 9)
                    }
                }
            }
        }
        .padding(.horizontal, 20)
        .padding(.vertical, 13)
    }

    @ViewBuilder
    private func subtitle(_ session: BotSession?) -> some View {
        if let session, session.pendingApprovals > 0 {
            Text("\(session.pendingApprovals) 个操作等你确认")
                .font(.system(size: 15, weight: .medium))
                .foregroundStyle(Theme.warn)
                .lineLimit(1)
        } else if let session, session.busy {
            Text("正在回复…")
                .font(.system(size: 15))
                .foregroundStyle(Theme.accent)
                .lineLimit(1)
        } else if let session, let lead = session.running.first {
            Text("正在执行：\(lead.title)")
                .font(.system(size: 15))
                .foregroundStyle(Theme.fg2)
                .lineLimit(1)
        } else if !deviceOnline {
            Text("设备离线")
                .font(.system(size: 15))
                .foregroundStyle(Theme.fg4)
        } else {
            Text(preview(session))
                .font(.system(size: 15))
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
            return session?.ready == false ? "正在同步…" : "开始新对话…"
        }
        let text = m.text.replacingOccurrences(of: "\n", with: " ").replacingOccurrences(of: "[*`#>]", with: "", options: .regularExpression)
        let body = text.isEmpty ? (m.files.isEmpty ? "…" : "[附件]") : text
        return body
    }
}
