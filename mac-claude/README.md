# Frog Bot Claude（Mac 面板）

Frog Bot 的原生 macOS 客户端（SwiftUI，macOS 14+）。可以同时连接多台 Frog Bot 服务器：聊天、看任务进度、批准操作、给每个 Bot 单独选模型、管理 API 渠道和 Agent、查看定时计划。

## 打包

```bash
cd mac-claude
bash scripts/build-app.sh      # 生成 build/Frog Bot Claude.app
open "build/Frog Bot Claude.app"
```

需要 Command Line Tools 或 Xcode（Swift 5.9+）。本机临时签名，对外分发需另配 Developer ID 和公证。

## 连接

设置（⌘,）› 添加服务器：填访问地址、用户名、密码。面板不保存密码，只保存 7 天有效的登录会话（`~/Library/Application Support/FrogBotClaude/sessions.json`，权限 0600）。

和项目里的 `mac/` 是两个独立的 App（Bundle ID 不同），可以同时安装。
