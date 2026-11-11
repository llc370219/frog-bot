# frog iOS 客户端（ios-claude）

frog 的原生 iOS / iPadOS 客户端，SwiftUI，iOS 18 以上。可以同时连接多台 frog 服务器，在手机上聊天、派任务、批准操作、管定时计划，设置里有 Bot 互联、API 渠道、每个 Bot 的模型和 Agent。

- `frog/Core/`：和服务器说话的部分（登录会话、SSE 事件流、时间线合并、客户端互联），从 Mac 版 `mac-claude/Sources/FrogBotClaude/Core` 拷过来改成 UIKit，两边各自维护。
- `frog/UI/`：手机界面。
- `frog/App/`：入口、导航、通知。
- 构建时把 `web/public/assets` 里的 Bot 头像和 Agent 图标打包进 App（构建阶段 “Bundle bot avatars”）。

## 装到自己的 iPhone

1. 用 Xcode 打开 `ios-claude/frog.xcodeproj`。
2. 选中 frog target → Signing & Capabilities，Team 选你自己的 Apple ID（免费账号也行，7 天后要重新装一次）。
3. 连上 iPhone，选它做运行目标，点运行。第一次要在手机「设置 › 通用 › VPN 与设备管理」里信任开发者，并打开「开发者模式」。

命令行：

```bash
xcodebuild -project ios-claude/frog.xcodeproj -scheme frog -destination 'generic/platform=iOS' \
  -allowProvisioningUpdates DEVELOPMENT_TEAM=你的TeamID build
```

## 和 Mac / 安卓的不同

- iOS 会挂起后台的 App：切到后台后事件流和 Bot 互联都会暂停（互联会马上告诉服务器），回到前台自动重连并补齐消息。提醒只在 App 开着时有效，没有远程推送。
- 允许 `http://IP:端口` 的明文连接，界面会标出「未加密」；连接局域网地址时系统会询问「本地网络」权限。
