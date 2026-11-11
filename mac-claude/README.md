# frog（macOS）

frog 的原生 macOS 客户端（SwiftUI，macOS 14+）。可以同时连接多台 frog 服务器，聊天、看任务进度、批准操作、给每个 Bot 单独选模型、管理 API 渠道和 Agent、查看定时计划。

## 打包

```bash
cd mac-claude
bash scripts/build-app.sh
open build/frog.app
```

需要 Command Line Tools 或 Xcode（Swift 5.9+）。打包脚本会获取并校验 Sparkle，生成临时签名的 `build/frog.app`。正式版安装包支持 Apple 芯片和 Intel Mac，首次打开时可能需要在「系统设置 › 隐私与安全性」确认打开。

## 更新客户端

将正式版 `frog.app` 移到「应用程序」。客户端会在后台检查 GitHub 正式版、下载已签名的更新，并在退出时安装。也可以在「frog › 检查客户端更新…」手动检查，或在「设置 › 通用」关闭自动检查。首次从旧版升级时，请在 GitHub Releases 下载 `frog-macos.zip`。

发布构建使用 `FROG_VERSION`、`FROG_MAC_UNIVERSAL=1` 和 `FROG_SPARKLE_PUBLIC_KEY`。构建后设置 `FROG_SPARKLE_PRIVATE_KEY` 为私钥文件路径，运行 `bash scripts/package-update.sh <tag> <output-dir>`，生成 `frog-macos.zip` 和签名更新源 `frog-macos-appcast.xml`。后续正式版须使用相同的 Sparkle 密钥对。

## 连接

设置（⌘,）› 添加服务器：填访问地址、用户名、密码。面板不保存密码，只保存 7 天有效的登录会话（`~/Library/Application Support/FrogBotClaude/sessions.json`，权限 0600）。
