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
    case agy(UUID)
    case agyChat(server: UUID, sessionID: String, projectID: String, title: String)
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
                        .navigationDestination(for: Nav.self) {
                            destination($0)
                                .toolbar(.visible, for: .navigationBar)
                        }
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
        case .agy(let id):
            if let s = model.server(id) { AgyHomeView(server: s, path: $path) } else { Unavailable(text: "这台服务器已经移除了。") }
        case .agyChat(let id, let sid, let pid, let title):
            if let s = model.server(id) { AgyChatView(server: s, sessionID: sid, projectID: pid, title: title, path: $path) }
            else { Unavailable(text: "这台服务器已经移除了。") }
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

/// 还没有服务器时的欢迎页：主打内置一键扫码/配对码连接，无需配置外置 VPN。
struct FirstRunView: View {
    @Environment(AppModel.self) private var model
    @State private var showingScanner = false
    @State private var busy = false
    @State private var error: String?

    var body: some View {
        ScrollView {
            VStack(spacing: 0) {
                FrogMark(size: 92)
                    .padding(.top, 76)
                Text("连接你的 frog 主机")
                    .font(.system(size: 24, weight: .bold))
                    .foregroundStyle(Theme.fg)
                    .multilineTextAlignment(.center)
                    .padding(.top, 22)
                Text("在电脑端打开「设置 › 远程连接」，一键开启加密通道并生成配对码。无需开公网端口，也无需安装外部 VPN。")
                    .font(.system(size: 15))
                    .foregroundStyle(Theme.fg3)
                    .multilineTextAlignment(.center)
                    .lineSpacing(3)
                    .padding(.top, 10)

                VStack(spacing: 12) {
                    Button {
                        Haptics.tap()
                        showingScanner = true
                    } label: {
                        Label("扫码一键连接", systemImage: "qrcode.viewfinder")
                            .frame(maxWidth: .infinity)
                    }
                    .buttonStyle(PillButtonStyle(kind: .primary))
                    .disabled(busy)

                    Button {
                        pastePair()
                    } label: {
                        Label(busy ? "正在连接…" : "粘贴配对码连接", systemImage: "doc.on.clipboard")
                            .frame(maxWidth: .infinity)
                    }
                    .buttonStyle(PillButtonStyle(kind: .secondary))
                    .disabled(busy)

                    Button { model.sheet = .addServer } label: {
                        Text("手动输入服务器地址与账号")
                            .font(.system(size: 14, weight: .medium))
                            .foregroundStyle(Theme.fg3)
                            .padding(.top, 4)
                    }
                    .buttonStyle(.plain)
                    .disabled(busy)
                }
                .padding(.top, 28)

                if let error {
                    Text(error)
                        .font(.footnote)
                        .foregroundStyle(Theme.danger)
                        .multilineTextAlignment(.center)
                        .padding(.top, 14)
                }
            }
            .frame(maxWidth: 440)
            .padding(.horizontal, 24)
            .frame(maxWidth: .infinity)
        }
        .background(Theme.canvas)
        .sheet(isPresented: $showingScanner) {
            QRScannerSheet { code in
                showingScanner = false
                pair(code)
            }
        }
    }

    private func pastePair() {
        Haptics.tap()
        guard let clip = UIPasteboard.general.string, !clip.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            error = "剪贴板为空，请先在电脑端复制配对码"
            return
        }
        pair(clip)
    }

    private func pair(_ raw: String) {
        busy = true
        error = nil
        Task {
            do {
                try await model.connectWithPairCode(raw)
                Haptics.success()
            } catch {
                self.error = error.localizedDescription
                Haptics.error()
            }
            busy = false
        }
    }
}

@MainActor
enum DebugFlags {
    static var openTray = false
}
