import AppKit
import SwiftUI
import UserNotifications
#if canImport(Sparkle)
import Sparkle
#endif

@main
@MainActor
struct FrogApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @State private var model = AppModel.shared
#if canImport(Sparkle)
    private let updaterController: SPUStandardUpdaterController? =
        Bundle.main.object(forInfoDictionaryKey: "SUPublicEDKey") is String
        ? SPUStandardUpdaterController(startingUpdater: true, updaterDelegate: nil, userDriverDelegate: nil)
        : nil
#endif

    var body: some Scene {
        WindowGroup("frog", id: "main") {
            RootView()
                .environment(model)
                .frame(minWidth: 860, minHeight: 560)
        }
        .defaultSize(width: 1180, height: 780)
        .windowToolbarStyle(.unified)
        .commands {
#if canImport(Sparkle)
            if let updater = updaterController?.updater {
                CommandGroup(after: .appInfo) {
                    ClientUpdateMenuItem(updater: updater)
                }
            }
#endif
            CommandGroup(replacing: .newItem) {
                Button("新建 Bot…") {
                    if let s = model.activeServer, s.isOnline {
                        model.sheet = .newBot(server: s.id, device: "local")
                    } else if let s = model.servers.first(where: \.isOnline) {
                        model.sheet = .newBot(server: s.id, device: "local")
                    }
                }
                .keyboardShortcut("n", modifiers: [.command])
                .disabled(!model.servers.contains(where: \.isOnline))
                Button("添加服务器…") { model.sheet = .addServer }
                    .keyboardShortcut("n", modifiers: [.command, .shift])
            }
            CommandMenu("工作区") {
                Button("切换到 Bot 对话模式") { model.workspaceMode = .bot }
                    .keyboardShortcut("1", modifiers: [.command])
                Button("切换到 Coding 工作台") { model.workspaceMode = .coding }
                    .keyboardShortcut("2", modifiers: [.command])
                Divider()
                Button("停止当前回复") {
                    if let ref = model.selection { Task { try? await model.session(ref)?.stopReply() } }
                }
                .disabled(model.selection == nil)
                Button("自动调度器") {
                    model.workspaceMode = .bot
                    model.route = .scheduler
                }
                .keyboardShortcut("j", modifiers: [.command])
                .disabled(model.selection == nil)
                Divider()
                Button("扫描本机 Frog Bot") {
                    Task { await model.discoverLocalHost(manual: true) }
                }
                Button("刷新全部主机") {
                    for s in model.servers { Task { await model.refresh(s) } }
                }
                .keyboardShortcut("r", modifiers: [.command])
            }
        }

        Settings {
            SettingsView()
                .environment(model)
#if canImport(Sparkle)
                .environment(\.clientUpdater, updaterController?.updater)
#endif
        }
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate, UNUserNotificationCenterDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        // `swift run` 直接跑可执行文件时，也要像正常 App 一样出现在 Dock 并到前台
        NSApp.setActivationPolicy(.regular)
        NSApp.activate(ignoringOtherApps: true)
        Notifier.setup(delegate: self)
        MainActor.assumeIsolated { Prefs.applyAppearance() }
    }

    // 退出前告诉各服务器：这个客户端不再转交互联消息（对方的通讯录马上清掉，而不是等 45 秒过期）
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        MainActor.assumeIsolated {
            let relay = AppModel.shared.relay
            guard relay.isRunning else { return .terminateNow }
            Task { @MainActor in
                await relay.shutdown()
                NSApp.reply(toApplicationShouldTerminate: true)
            }
            return .terminateLater
        }
    }

    // 默认关掉窗口后继续在后台运行：后台任务完成、需要审批时照样通知
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        !UserDefaults.standard.bool(forKey: Prefs.keepRunning)
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification,
                                withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        completionHandler([.banner, .sound])
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse,
                                withCompletionHandler completionHandler: @escaping () -> Void) {
        let key = response.notification.request.content.userInfo["ref"] as? String
        Task { @MainActor in
            if let key, let ref = BotRef(key: key) { AppModel.shared.open(ref) }
            completionHandler()
        }
    }
}

@MainActor
struct RootView: View {
    @Environment(AppModel.self) private var model
    @AppStorage(Prefs.dockBadge) private var dockBadge = true
    @State private var columns = NavigationSplitViewVisibility.all

    var body: some View {
        @Bindable var model = model
        NavigationSplitView(columnVisibility: $columns) {
            SidebarView()
        } detail: {
            detail
        }
        .toolbarBackground(.hidden, for: .windowToolbar)
        .sheet(item: $model.sheet) { sheet in
            sheetView(sheet)
        }
        .onChange(of: model.totalUnread) { updateBadge() }
        .onChange(of: dockBadge) { updateBadge() }
        .onReceive(NotificationCenter.default.publisher(for: NSApplication.didBecomeActiveNotification)) { _ in
            if model.route == .chat { model.clearUnreadForSelection() }
        }
    }

    private func updateBadge() {
        NSApp.dockTile.badgeLabel = dockBadge && model.totalUnread > 0 ? "\(model.totalUnread)" : nil
    }

    @ViewBuilder
    private var detail: some View {
        if model.servers.isEmpty {
            FirstRunView()
        } else if model.workspaceMode == .coding {
            if let sel = model.codingSelection, let s = model.server(sel.server) {
                CodingWorkspaceView(server: s, selection: sel)
            } else {
                CodingEmptyView(server: model.activeServer)
            }
        } else if let ref = model.selection, let session = model.session(ref) {
            if model.route == .scheduler {
                SchedulerView(session: session).id("sched-\(ref.key)")
            } else {
                ChatView(session: session).id(ref)
            }
        } else {
            VStack(spacing: 10) {
                if let s = model.activeServer {
                    switch s.status {
                    case .connecting:
                        ProgressView().controlSize(.small)
                        Text("正在连接「\(s.name)」…").foregroundStyle(Theme.fg3)
                    case .needsLogin:
                        Text("「\(s.name)」需要重新登录").foregroundStyle(Theme.fg3)
                        Button("重新登录") { model.sheet = .login(s.id) }
                            .buttonStyle(PillButtonStyle(kind: .primary, compact: true))
                    case .offline(let e):
                        Text("「\(s.name)」暂时离线").foregroundStyle(Theme.fg2).fontWeight(.medium)
                        Text(e).font(.system(size: 12)).foregroundStyle(Theme.fg4)
                        Button("重试连接") { Task { await model.refresh(s) } }
                            .buttonStyle(PillButtonStyle(kind: .secondary, compact: true))
                    case .online:
                        Text("在左侧选择一个 Bot，或点击侧栏右上角切换 Coding 工作台").foregroundStyle(Theme.fg3)
                    }
                } else {
                    Text("在左侧选择一个 Bot，或点击侧栏右上角切换 Coding 工作台").foregroundStyle(Theme.fg3)
                }
            }
            .font(.system(size: 13))
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(Theme.canvas)
            .navigationTitle(model.activeServer?.name ?? "frog")
        }
    }

    @ViewBuilder
    private func sheetView(_ sheet: ActiveSheet) -> some View {
        switch sheet {
        case .addServer:
            ServerSheet(mode: .add)
        case .login(let id):
            ServerSheet(mode: .login, server: model.server(id))
        case .editServer(let id):
            ServerSheet(mode: .edit, server: model.server(id))
        case .newBot(let id, let device):
            if let s = model.server(id) { BotEditorSheet(server: s, device: device) }
        case .editBot(let ref):
            if let s = model.server(ref.server) {
                BotEditorSheet(server: s, device: ref.device,
                               profile: s.devices.first(where: { $0.id == ref.device })?.bots.first(where: { $0.id == ref.bot }))
            }
        case .cliAuth(let id):
            if let s = model.server(id) { CLIAuthModalSheet(server: s) }
        case .proxyGateway(let id):
            if let s = model.server(id) { ProxyGatewayModalSheet(server: s) }
        case .newProject(let id):
            if let s = model.server(id) { MacNewProjectSheet(store: model.agyStore(for: s)) }
        case .projectFiles(let sid, let proj):
            if let s = model.server(sid) { MacProjectFilesSheet(server: s, project: proj) }
        }
    }
}

/// 侧边栏右上角紧凑模式切换（类似 Claude Desktop 的 Cowork / Code 图标切换）。
@MainActor
struct WorkspaceModeSwitcher: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        HStack(spacing: 2) {
            iconButton(
                mode: .bot,
                symbol: "bubble.left.and.bubble.right",
                help: "Bot 对话模式（⌘1）"
            )
            iconButton(
                mode: .coding,
                symbol: "chevron.left.forwardslash.chevron.right",
                help: "Coding 工作台（⌘2）"
            )
        }
        .padding(2)
        .background(Theme.s2.opacity(0.95), in: Capsule())
        .overlay(Capsule().strokeBorder(Theme.line, lineWidth: 0.6))
    }

    private func iconButton(mode: WorkspaceMode, symbol: String, help: String) -> some View {
        let active = model.workspaceMode == mode
        return Button {
            withAnimation(.easeInOut(duration: 0.14)) {
                model.workspaceMode = mode
            }
        } label: {
            Image(systemName: symbol)
                .font(.system(size: 11, weight: active ? .semibold : .regular))
                .foregroundStyle(active ? Theme.fg : Theme.fg3)
                .frame(width: 26, height: 22)
                .background(active ? Theme.s1 : .clear, in: Capsule())
                .shadow(color: active ? Theme.shadow.opacity(0.45) : .clear, radius: 1, y: 0.5)
                .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .help(help)
    }
}

/// 还没有主机时的欢迎页：自动检测本机 Frog Bot，同时支持粘贴 froge2ee:// 连接码或远端 URL。
@MainActor
struct FirstRunView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        VStack(spacing: 0) {
            FrogMark(size: 72)
            Text("欢迎使用 Frog Bot")
                .font(.system(size: 24, weight: .semibold))
                .foregroundStyle(Theme.fg)
                .padding(.top, 20)
            Text("双核个人 AI 工作台：Bot 模式对话派任务，Coding 模式无气泡沉浸写代码。\n本机运行的 Frog Bot 会自动识别，也可通过 froge2ee:// 连接码安全接入远端主机。")
                .font(.system(size: 13))
                .foregroundStyle(Theme.fg3)
                .multilineTextAlignment(.center)
                .lineSpacing(3)
                .padding(.top, 8)

            if let local = model.detectedLocal {
                HStack(spacing: 12) {
                    Circle().fill(Theme.ok).frame(width: 8, height: 8)
                    VStack(alignment: .leading, spacing: 2) {
                        Text("发现本机 Frog Bot（\(local.url)）")
                            .font(.system(size: 13, weight: .semibold))
                        Text(local.authRequired ? "需要管理员密码登录" : "本机免密直连")
                            .font(.system(size: 11.5))
                            .foregroundStyle(Theme.fg3)
                    }
                    Spacer()
                    Button("立即接入") {
                        Task { await model.discoverLocalHost(manual: true) }
                    }
                    .buttonStyle(PillButtonStyle(kind: .primary))
                }
                .padding(14)
                .frame(maxWidth: 440)
                .card(fill: Theme.s2, border: Theme.accent.opacity(0.4), radius: 12)
                .padding(.top, 20)
            }

            HStack(spacing: 10) {
                Button("添加主机 / 连接码…") { model.sheet = .addServer }
                    .buttonStyle(PillButtonStyle(kind: .primary))
                    .controlSize(.large)
                Button {
                    Task { await model.discoverLocalHost(manual: true) }
                } label: {
                    HStack(spacing: 6) {
                        if model.scanningLocal { ProgressView().controlSize(.small) }
                        Text("扫描本机 Frog Bot")
                    }
                }
                .buttonStyle(PillButtonStyle(kind: .secondary))
                .controlSize(.large)
                .disabled(model.scanningLocal)
            }
            .padding(.top, 22)

            if let msg = model.localScanMessage {
                Text(msg)
                    .font(.system(size: 12))
                    .foregroundStyle(Theme.fg3)
                    .padding(.top, 10)
            }

            VStack(alignment: .leading, spacing: 6) {
                Text("还没在服务器安装？在 Linux / macOS 终端运行：")
                    .font(.system(size: 12))
                    .foregroundStyle(Theme.fg3)
                HStack {
                    Text(installCommand)
                        .font(.system(size: 11.5, design: .monospaced))
                        .foregroundStyle(Theme.fg2)
                        .textSelection(.enabled)
                        .lineLimit(1)
                        .truncationMode(.middle)
                    Button { copy(installCommand) } label: { Image(systemName: "doc.on.doc") }
                        .buttonStyle(IconButtonStyle(size: 22))
                        .help("复制")
                }
                .padding(.horizontal, 12)
                .padding(.vertical, 8)
                .background(Theme.s2, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
            }
            .frame(maxWidth: 520)
            .padding(.top, 36)
        }
        .padding(40)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Theme.canvas)
    }

    private let installCommand = "curl -fsSL https://raw.githubusercontent.com/llc370219/frog-bot/main/scripts/install.sh | sudo bash"
}

/// 使用打包时嵌入的客户端图标。
@MainActor
struct FrogMark: View {
    var size: CGFloat

    var body: some View {
        Group {
            if Bundle.main.bundleURL.pathExtension == "app", let icon = NSApp.applicationIconImage {
                Image(nsImage: icon).resizable().interpolation(.high)
            } else {
                Image(systemName: "bubble.left.and.bubble.right")
                    .font(.system(size: size * 0.4))
                    .foregroundStyle(Theme.fg3)
                    .frame(width: size, height: size)
                    .background(Theme.s2, in: RoundedRectangle(cornerRadius: size * 0.24, style: .continuous))
            }
        }
        .frame(width: size, height: size)
    }
}
