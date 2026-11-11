# Frog Bot Claude（Android）

Frog Bot 的原生安卓客户端（Kotlin + Jetpack Compose，Material 3），Android 8.0 以上。可以同时连接多台 Frog Bot 服务器：聊天、看任务进度、批准操作、给每个 Bot 单独选模型、管理 API 渠道和 Agent、查看定时计划。

## 编译 APK

需要 JDK 17+ 和 Android SDK（compileSdk 35）。

```bash
cd android-claude
./gradlew :app:assembleRelease     # 输出 app/build/outputs/apk/release/app-release.apk
```

release 包用 debug 密钥签名，只适合自己装着用；上架商店需要另配签名。

## 安装到手机

把 APK 传到手机上点开安装，第一次要在系统里允许「安装未知应用」。

## 连接

设置 › 添加服务器：填访问地址、用户名、密码。手机不保存密码，只保存 7 天有效的登录会话。服务器要能从手机访问到（公网地址、同一个 Wi-Fi，或 Tailscale 这类组网工具）。

想在后台也及时收到审批和任务提醒，打开「设置 › 后台保持连接」（会有一条常驻通知）。

和项目里的 `android/` 是两个独立的 App（包名不同），可以同时安装。
