# frog（Android）

frog 的原生安卓客户端（Kotlin + Jetpack Compose，Material 3），Android 8.0 以上。可以同时连接多台 frog 服务器：聊天、看任务进度、批准操作、给每个 Bot 单独选模型、管理 API 渠道和 Agent、查看定时计划。

## 编译 APK

需要 JDK 17+ 和 Android SDK（compileSdk 35）。

```bash
cd android-claude
./gradlew :app:assembleRelease -PfrogVersion=0.1.6
```

APK 输出到 `app/build/outputs/apk/release/app-release.apk`。正式包使用固定的 PKCS#12 签名密钥，由 `FROG_ANDROID_KEYSTORE`、`FROG_ANDROID_KEYSTORE_PASSWORD`、`FROG_ANDROID_KEY_ALIAS` 和 `FROG_ANDROID_KEY_PASSWORD` 配置。本地测试包默认使用 debug 密钥。

## 安装到手机

从 [GitHub Releases](https://github.com/llc370219/frog-bot/releases/latest) 下载 `frog-android.apk`。以前使用 debug 密钥签名的测试版需要卸载后安装正式版，并重新添加服务器。

正式版打开时会自动检查 GitHub 上的正式发布，下载并校验新版 APK。更新就绪后点「安装更新」，在 Android 安装界面确认。首次在 App 内安装时，系统会要求允许 frog 安装未知应用。也可以在「设置 › 检查客户端更新」手动检查。

## 连接

设置 › 添加服务器：填访问地址、用户名、密码。手机不保存密码，只保存 7 天有效的登录会话。服务器要能从手机访问到（公网地址、同一个 Wi-Fi，或 Tailscale 这类组网工具）。

想在后台也及时收到审批和任务提醒，打开「设置 › 后台保持连接」（会有一条常驻通知）。
