// swift-tools-version:5.9
// Frog Bot Claude：Frog Bot 的原生 macOS 面板（SwiftUI），连接一台或多台 Frog Bot 服务器。
// 开发：swift run    打包：bash scripts/build-app.sh（生成 build/Frog Bot Claude.app）
import PackageDescription

let package = Package(
    name: "FrogBotClaude",
    defaultLocalization: "zh-Hans",
    platforms: [.macOS(.v14)],
    targets: [
        .executableTarget(
            name: "FrogBotClaude",
            path: "Sources/FrogBotClaude"
        ),
    ]
)
