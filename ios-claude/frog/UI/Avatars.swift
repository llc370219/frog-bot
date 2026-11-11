import SwiftUI
import UIKit

/// 打包进 App 的图片（网页版的 Bot 头像、Agent 图标）。没有就从服务器取。
@MainActor
enum BundledImage {
    private static var cache: [String: UIImage] = [:]
    private static var missing: Set<String> = []

    static func named(_ relative: String) -> UIImage? {
        if let img = cache[relative] { return img }
        if missing.contains(relative) { return nil }
        guard let base = Bundle.main.resourceURL,
              let img = UIImage(contentsOfFile: base.appendingPathComponent(relative).path) else {
            missing.insert(relative)
            return nil
        }
        cache[relative] = img
        return img
    }
}

/// Bot 头像：和网页版同一套 224 张线条涂鸦。
struct BotAvatar: View {
    let client: ServerClient
    let profile: BotProfile
    var size: CGFloat = 32

    private static let count = 224

    static func file(_ profile: BotProfile) -> String {
        let n = (profile.avatarSlot ?? slot(profile.id)) % count
        return "avatar-" + String(format: "%03d", n + 1) + ".webp"
    }

    /// FNV-1a，和网页版的 slot() 一样
    static func slot(_ id: String) -> Int {
        var h: UInt32 = 0x811c9dc5
        for u in id.utf16 { h = (h ^ UInt32(u)) &* 0x01000193 }
        return Int(h % UInt32(count))
    }

    var body: some View {
        let file = Self.file(profile)
        Group {
            if let img = BundledImage.named("bot-avatars/" + file) {
                Image(uiImage: img).resizable().interpolation(.high).aspectRatio(contentMode: .fit)
            } else {
                RemoteImage(client: client, path: "/assets/bot-avatars/" + file) {
                    Text(String(profile.name.prefix(1)))
                        .font(.system(size: size * 0.45, weight: .semibold))
                        .foregroundStyle(Color.black.opacity(0.55))
                }
            }
        }
        .padding(size * 0.07)
        .frame(width: size, height: size)
        .background(Color.white, in: RoundedRectangle(cornerRadius: size * 0.3, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: size * 0.3, style: .continuous).strokeBorder(Theme.line2, lineWidth: 0.5))
    }
}

/// 内置 Frog Agent 的头像（网页版用的 avatar-003）
struct FrogAgentAvatar: View {
    var size: CGFloat = 28

    var body: some View {
        Group {
            if let img = BundledImage.named("bot-avatars/avatar-003.webp") {
                Image(uiImage: img).resizable().interpolation(.high).aspectRatio(contentMode: .fit)
            } else {
                Image(systemName: "sparkles").foregroundStyle(Theme.accent)
            }
        }
        .padding(size * 0.08)
        .frame(width: size, height: size)
        .background(Color.white, in: RoundedRectangle(cornerRadius: size * 0.3, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: size * 0.3, style: .continuous).strokeBorder(Theme.line2, lineWidth: 0.5))
    }
}

/// Agent CLI 的图标（codex / claude；其他的没有位图就用终端图标）
struct AgentIcon: View {
    let id: String
    var client: ServerClient?
    var size: CGFloat = 28

    private var file: String { ["codex": "codex.png", "claude": "claude.png"][id] ?? "\(id).png" }

    var body: some View {
        Group {
            if let img = BundledImage.named("agent-icons/" + file) {
                Image(uiImage: img).resizable().interpolation(.high).aspectRatio(contentMode: .fit)
            } else {
                Image(systemName: "terminal")
                    .font(.system(size: size * 0.5))
                    .foregroundStyle(Theme.fg3)
                    .frame(width: size, height: size)
                    .background(Theme.s2)
            }
        }
        .frame(width: size, height: size)
        .clipShape(RoundedRectangle(cornerRadius: size * 0.24, style: .continuous))
    }
}

/// App 图标里的那只猫（首次打开、关于页）
struct FrogMark: View {
    var size: CGFloat = 72

    var body: some View {
        Group {
            if let icon = UIImage(named: "FrogMark") {
                Image(uiImage: icon).resizable().aspectRatio(contentMode: .fit)
            } else {
                Image(systemName: "shippingbox").font(.system(size: size * 0.5)).foregroundStyle(Theme.fg3)
            }
        }
        .frame(width: size, height: size)
        .clipShape(RoundedRectangle(cornerRadius: size * 0.225, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: size * 0.225, style: .continuous).strokeBorder(Theme.line, lineWidth: 0.5))
    }
}

/// 带登录 Cookie 的图片加载（AsyncImage 不能加请求头）
struct RemoteImage<Placeholder: View>: View {
    let client: ServerClient
    let path: String
    var query: [URLQueryItem] = []
    var contentMode: ContentMode = .fit
    @ViewBuilder var placeholder: () -> Placeholder

    @State private var image: UIImage?

    var body: some View {
        Group {
            if let image = image ?? ImageCache.shared.cached(ImageCache.shared.key(client, path: path, query: query)) {
                Image(uiImage: image)
                    .resizable()
                    .interpolation(.high)
                    .aspectRatio(contentMode: contentMode)
            } else {
                placeholder()
            }
        }
        .task(id: path) {
            image = await ImageCache.shared.load(client, path: path, query: query)
        }
    }
}
