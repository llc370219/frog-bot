import QuickLook
import SwiftUI
import UIKit
import UserNotifications

@main
struct FrogApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @State private var model = AppModel.shared
    @Environment(\.scenePhase) private var phase

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(model)
        }
        .onChange(of: phase) { _, p in
            switch p {
            case .active: model.becameActive()
            case .background: model.enterBackground()
            default: break
            }
        }
    }
}

/// 通知：App 在前台时也显示横幅（除非正看着那个对话）；点通知直接打开对应的 Bot。
final class AppDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    func application(_ application: UIApplication, didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil) -> Bool {
        #if DEBUG
        // 截图检查界面时不要弹通知授权框
        if ProcessInfo.processInfo.arguments.contains("-frogScreen") {
            UNUserNotificationCenter.current().delegate = self
            return true
        }
        #endif
        Notifier.setup(delegate: self)
        return true
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification) async -> UNNotificationPresentationOptions {
        [.banner, .sound, .list]
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
        let key = response.notification.request.content.userInfo["ref"] as? String
        await MainActor.run {
            if let key, let ref = BotRef(key: key) { AppModel.shared.open(ref) }
        }
    }
}

/// App 里的页面（导航栈的路径）
enum Nav: Hashable {
    case chat(BotRef)
    case scheduler(BotRef)
    case bot(BotRef)
    case channels(BotRef)
    case agents(BotRef)
    case settings
    case server(UUID)
    case relay
    case notifications
}

struct RootView: View {
    @Environment(AppModel.self) private var model
    @AppStorage(Prefs.appearance) private var appearance = "system"
    @State private var path: [Nav] = []
    @State private var preview = FilePreview.shared

    var body: some View {
        @Bindable var model = model
        Group {
            if model.servers.isEmpty {
                FirstRunView()
            } else {
                NavigationStack(path: $path) {
                    HomeView(path: $path)
                        .navigationDestination(for: Nav.self) { destination($0) }
                }
            }
        }
        .tint(Theme.accent)
        .preferredColorScheme(appearance == "light" ? .light : appearance == "dark" ? .dark : nil)
        .sheet(item: $model.sheet) { sheet in
            switch sheet {
            case .addServer: ServerSheet(mode: .add)
            case .login(let id): ServerSheet(mode: .login, server: model.server(id))
            case .editServer(let id): ServerSheet(mode: .edit, server: model.server(id))
            case .newBot(let s, let d):
                if let server = model.server(s) { NewBotSheet(server: server, device: d) { ref in path = [.chat(ref)] } }
            case .editBot(let ref): NavigationStack { BotSettingsView(ref: ref) }
            }
        }
        .quickLookPreview(Binding(get: { preview.url }, set: { preview.url = $0 }))
        .alert("打不开文件", isPresented: Binding(get: { preview.error != nil }, set: { if !$0 { preview.error = nil } })) {
            Button("好", role: .cancel) {}
        } message: {
            Text(preview.error ?? "")
        }
        .task { await debugScreen() }
        .onChange(of: model.pendingOpen) {
            guard let ref = model.pendingOpen else { return }
            model.pendingOpen = nil
            model.sheet = nil
            path = [.chat(ref)]
        }
    }

    /// 调试用：`-frogScreen chat|tasks|settings|relay|bot|channels|scheduler` 启动后直接打开某一页（截图检查界面）
    private func debugScreen() async {
        #if DEBUG
        let args = ProcessInfo.processInfo.arguments
        guard let i = args.firstIndex(of: "-frogScreen"), i + 1 < args.count else { return }
        let screen = args[i + 1]
        for _ in 0..<60 {
            if let s = model.servers.first, s.isOnline, let g = s.devices.first(where: { $0.id == "local" }),
               let b = g.bots.first, model.session(BotRef(server: s.id, device: g.id, bot: b.id))?.ready == true {
                let ref = BotRef(server: s.id, device: g.id, bot: b.id)
                switch screen {
                case "chat": path = [.chat(ref)]
                case "tasks": DebugFlags.openTray = true; path = [.chat(ref)]
                case "settings": path = [.settings]
                case "relay": path = [.settings, .relay]
                case "bot": path = [.chat(ref), .bot(ref)]
                case "channels": path = [.server(s.id), .channels(ref)]
                case "scheduler": path = [.chat(ref), .scheduler(ref)]
                default: break
                }
                return
            }
            try? await Task.sleep(nanoseconds: 250_000_000)
        }
        #endif
    }

    @ViewBuilder
    private func destination(_ nav: Nav) -> some View {
        switch nav {
        case .chat(let ref):
            if let s = model.session(ref) {
                ChatView(session: s, path: $path)
            } else {
                Unavailable(text: "这个 Bot 现在连不上。服务器离线或 Bot 已删除。")
            }
        case .scheduler(let ref):
            if let s = model.session(ref) { SchedulerView(session: s) } else { Unavailable(text: "这个 Bot 现在连不上。") }
        case .bot(let ref): BotSettingsView(ref: ref)
        case .channels(let ref):
            if let s = model.session(ref) { ChannelsView(session: s) } else { Unavailable(text: "这台服务器现在连不上。") }
        case .agents(let ref):
            if let s = model.session(ref) { AgentsView(session: s) } else { Unavailable(text: "这台服务器现在连不上。") }
        case .settings: SettingsView(path: $path)
        case .server(let id):
            if let s = model.server(id) { ServerSettingsView(server: s, path: $path) } else { Unavailable(text: "这台服务器已经移除了。") }
        case .relay: RelaySettingsView()
        case .notifications: NotificationSettingsView()
        }
    }
}

struct Unavailable: View {
    let text: String
    var body: some View {
        ContentUnavailableView(text, systemImage: "wifi.exclamationmark")
    }
}

/// 还没有服务器时的欢迎页
struct FirstRunView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        ScrollView {
            VStack(spacing: 0) {
                FrogMark(size: 96)
                    .padding(.top, 80)
                Text("连接你的第一台 frog 服务器")
                    .font(.system(size: 24, weight: .bold))
                    .foregroundStyle(Theme.fg)
                    .multilineTextAlignment(.center)
                    .padding(.top, 24)
                Text("在服务器上装好 frog 后，把安装时打印的访问地址、用户名和密码填进来。可以连接任意多台，在这里统一聊天、派任务、批准操作。")
                    .font(.system(size: 15))
                    .foregroundStyle(Theme.fg3)
                    .multilineTextAlignment(.center)
                    .lineSpacing(3)
                    .padding(.top, 10)
                Button { model.sheet = .addServer } label: {
                    Text("添加服务器").frame(maxWidth: .infinity)
                }
                .buttonStyle(PillButtonStyle(kind: .primary))
                .padding(.top, 30)

                VStack(alignment: .leading, spacing: 8) {
                    Text("还没装？在 Linux 服务器上运行：")
                        .font(.system(size: 13, weight: .medium))
                        .foregroundStyle(Theme.fg3)
                    Text(installCommand)
                        .font(.system(size: 12, design: .monospaced))
                        .foregroundStyle(Theme.fg2)
                        .textSelection(.enabled)
                        .padding(12)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(Theme.code, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                    Button { copy(installCommand) } label: {
                        Label("复制命令", systemImage: "doc.on.doc").font(.system(size: 13))
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(Theme.accent)
                }
                .padding(.top, 40)
            }
            .frame(maxWidth: 460)
            .padding(.horizontal, 24)
            .frame(maxWidth: .infinity)
        }
        .background(Theme.canvas)
    }

    private let installCommand = "curl -fsSL https://raw.githubusercontent.com/llc370219/frog-bot/main/scripts/install.sh | sudo bash"
}

@MainActor
enum DebugFlags {
    static var openTray = false
}
