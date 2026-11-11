import AppKit
import SwiftUI
import UserNotifications

@main
@MainActor
struct FrogBotClaudeApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @State private var model = AppModel.shared

    var body: some Scene {
        WindowGroup("Frog Bot Claude", id: "main") {
            RootView()
                .environment(model)
                .frame(minWidth: 860, minHeight: 560)
        }
        .defaultSize(width: 1180, height: 780)
        .windowToolbarStyle(.unified)
        .commands {
            CommandGroup(replacing: .newItem) {
                Button("新建 Bot…") {
                    if let s = model.servers.first(where: \.isOnline) { model.sheet = .newBot(server: s.id, device: "local") }
                }
                .keyboardShortcut("n", modifiers: [.command])
                .disabled(!model.servers.contains(where: \.isOnline))
                Button("添加服务器…") { model.sheet = .addServer }
                    .keyboardShortcut("n", modifiers: [.command, .shift])
            }
            CommandMenu("Bot") {
                Button("停止当前回复") {
                    if let ref = model.selection { Task { try? await model.session(ref)?.stopReply() } }
                }
                .disabled(model.selection == nil)
                Button("自动调度器") { model.route = .scheduler }
                    .keyboardShortcut("j", modifiers: [.command])
                    .disabled(model.selection == nil)
                Divider()
                Button("刷新服务器列表") {
                    for s in model.servers { Task { await model.refresh(s) } }
                }
                .keyboardShortcut("r", modifiers: [.command])
            }
        }

        Settings {
            SettingsView()
                .environment(model)
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
        if let ref = model.selection, let session = model.session(ref) {
            if model.route == .scheduler {
                SchedulerView(session: session).id("sched-\(ref.key)")
            } else {
                ChatView(session: session).id(ref)
            }
        } else if model.servers.isEmpty {
            FirstRunView()
        } else {
            VStack(spacing: 10) {
                if model.servers.contains(where: { $0.status == .connecting }) {
                    ProgressView().controlSize(.small)
                    Text("正在连接服务器…").foregroundStyle(Theme.fg3)
                } else {
                    Text("在左边选一个 Bot").foregroundStyle(Theme.fg3)
                }
            }
            .font(.system(size: 13))
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(Theme.canvas)
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
        }
    }
}

/// 还没有服务器时的欢迎页
@MainActor
struct FirstRunView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        VStack(spacing: 0) {
            FrogMark(size: 72)
            Text("连接你的第一台 Frog Bot")
                .font(.system(size: 24, weight: .semibold))
                .foregroundStyle(Theme.fg)
                .padding(.top, 20)
            Text("在服务器上装好 Frog Bot 后，把安装时打印的访问地址、用户名和密码填进来。\n可以连接任意多台，在这里统一聊天、派任务、批准操作。")
                .font(.system(size: 13))
                .foregroundStyle(Theme.fg3)
                .multilineTextAlignment(.center)
                .lineSpacing(3)
                .padding(.top, 8)
            Button("添加服务器…") { model.sheet = .addServer }
                .buttonStyle(PillButtonStyle(kind: .primary))
                .controlSize(.large)
                .padding(.top, 22)

            VStack(alignment: .leading, spacing: 6) {
                Text("还没装？在 Linux 服务器上运行：")
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
            .padding(.top, 40)
        }
        .padding(40)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Theme.canvas)
    }

    private let installCommand = "curl -fsSL https://raw.githubusercontent.com/llc370219/frog-bot/main/scripts/install.sh | sudo bash"
}

/// App 里的青蛙标志：打包后用 App 图标，开发时退回网页版的青蛙图
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
