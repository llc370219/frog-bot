import Foundation
import FrogBox
import UIKit

/// 进程内内置 sing-box 加密隧道管理器（免系统级 NetworkExtension 签名，不抢占系统其他 VPN）。
@MainActor
final class FrogBoxManager {
    static let shared = FrogBoxManager()

    struct ActiveTunnel: Equatable {
        let handle: Int32
        let port: Int
        let mode: String
        let serverName: String
    }

    private struct RawStartResult: Decodable {
        var handle: Int32?
        var port: Int?
        var mode: String?
        var server_name: String?
        var error: String?
    }

    private var active: [String: ActiveTunnel] = [:]

    private init() {}

    func acquire(uri: String) throws -> ActiveTunnel {
        let clean = uri.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !clean.isEmpty else {
            throw APIError.invalid("空的加密隧道链接")
        }
        if let existing = active[clean] {
            return existing
        }

        let devName = UIDevice.current.name
        guard let cPtr = FrogBoxStart(clean, devName) else {
            throw APIError.invalid("内置 sing-box 核心启动失败")
        }
        defer { FrogBoxFree(cPtr) }
        let jsonStr = String(cString: cPtr)

        guard let data = jsonStr.data(using: .utf8),
              let res = try? JSONDecoder().decode(RawStartResult.self, from: data) else {
            throw APIError.invalid("内置隧道返回状态异常")
        }
        if let err = res.error, !err.isEmpty {
            throw APIError.invalid(err)
        }
        guard let handle = res.handle, let port = res.port, port > 0 else {
            throw APIError.invalid("未能分配本地加密通道端口")
        }

        let tun = ActiveTunnel(
            handle: handle,
            port: port,
            mode: res.mode ?? "reality",
            serverName: res.server_name ?? ""
        )
        active[clean] = tun
        return tun
    }

    func sync(keeping uris: Set<String>) {
        let cleanKeep = Set(uris.map { $0.trimmingCharacters(in: .whitespacesAndNewlines) })
        let obsolete = active.keys.filter { !cleanKeep.contains($0) }
        for key in obsolete {
            if let tun = active.removeValue(forKey: key) {
                FrogBoxStop(tun.handle)
            }
        }
    }
}
