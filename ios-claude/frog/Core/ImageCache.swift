import UIKit

/// 从服务器取图片（Bot 头像、聊天里的图片附件），带登录 Cookie，内存里缓存。
@MainActor
final class ImageCache {
    static let shared = ImageCache()

    private var images: [String: UIImage] = [:]
    private var failed: Set<String> = []
    private var inflight: [String: Task<UIImage?, Never>] = [:]

    func cached(_ key: String) -> UIImage? { images[key] }

    func load(_ client: ServerClient, path: String, query: [URLQueryItem] = []) async -> UIImage? {
        let key = client.url(path, query: query).absoluteString
        if let img = images[key] { return img }
        if failed.contains(key) { return nil }
        if let t = inflight[key] { return await t.value }
        let t = Task<UIImage?, Never> {
            guard let data = try? await client.data(path, query: query) else { return nil }
            return UIImage(data: data)
        }
        inflight[key] = t
        let img = await t.value
        inflight[key] = nil
        if let img { images[key] = img } else { failed.insert(key) }
        return img
    }

    func key(_ client: ServerClient, path: String, query: [URLQueryItem] = []) -> String {
        client.url(path, query: query).absoluteString
    }
}
