// swift-tools-version:5.9
// frog：可连接多台服务器的原生 macOS 客户端。
import PackageDescription

let package = Package(
    name: "frog",
    defaultLocalization: "zh-Hans",
    platforms: [.macOS(.v14)],
    targets: [
        .executableTarget(
            name: "frog",
            path: "Sources/FrogBotClaude"
        ),
    ]
)
