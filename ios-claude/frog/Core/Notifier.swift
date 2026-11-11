import UIKit
import UserNotifications

/// 系统通知：新回复、任务结束、等你确认。只有打包成 .app 运行时才可用（`swift run` 没有 Bundle ID）。
enum Notifier {
    static var available: Bool { Bundle.main.bundleIdentifier != nil }

    static func setup(delegate: UNUserNotificationCenterDelegate) {
        guard available else { return }
        let center = UNUserNotificationCenter.current()
        center.delegate = delegate
        center.requestAuthorization(options: [.alert, .sound, .badge]) { _, _ in }
    }

    static func post(title: String, body: String, ref: BotRef) {
        guard available else { return }
        let content = UNMutableNotificationContent()
        content.title = title
        content.body = String(body.replacingOccurrences(of: "\n", with: " ").prefix(160))
        content.sound = .default
        content.threadIdentifier = ref.key
        content.userInfo = ["ref": ref.key]
        let req = UNNotificationRequest(identifier: UUID().uuidString, content: content, trigger: nil)
        UNUserNotificationCenter.current().add(req) { _ in }
    }
}
