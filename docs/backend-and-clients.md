# 后端与网页 / 原生客户端

frog 的网页和四个原生客户端共用 Go 后端：Windows（Electron）、Mac（SwiftUI）、Android（Kotlin + Jetpack Compose）和 iOS / iPadOS（SwiftUI）。各客户端连接同一服务器时，共享 Bot、聊天、任务、审批和文件。

当前产品边界：单管理员，不是多租户 SaaS。还没有用户注册、多人权限隔离、离线模式、手机系统推送或商店发布。模型和 CLI 在后端机器运行，客户端不需要保存模型密钥。

## 本地启动后端

需要 Go 1.22+（本次使用 1.26 验证）、Node.js 22+ 和 npm。在仓库根目录依次执行：

```bash
cd web
npm ci
npm run build
cd ..
go build -o bin/frogbot ./cmd/frogbot
./bin/frogbot -server -no-browser -data ./data
```

首次启动在终端打印管理员名和随机初始密码。网页访问电脑的 8080 端口；同一台电脑上的 Windows / Mac 客户端可以填 `http://127.0.0.1:8080`，其他设备填这台电脑的局域网地址。不要把 127.0.0.1 填进手机，它指向手机自己。Android 模拟器用 `http://10.0.2.2:8080`，明文只限可信网络的 Debug 调试。正式连接用 HTTPS。

在网页「设置 → API 渠道」接入接口和 API Key（整台设备共用，`GET/POST /api/channels`），并在「设置 → 模型与 Agent」为每个 Bot 选择模型（`POST /api/bots/{id}/settings/routes`），或为 Agent 选择已安装且登录的官方订阅 CLI，之后才能正常调用模型。仅验证登录、消息持久化和接口无需模型。无登录本机模式只监听 127.0.0.1，不能给其他设备共享使用。

Windows 客户端的「本机使用」会自动下载正式版 `frogbot.exe`，按 `SHA256SUMS` 校验后以本机模式运行（`-addr 127.0.0.1:<端口> -no-browser`，端口在 8080–8099 中选空闲的），并添加为服务器。它装在 `%APPDATA%\frog\local-server\`，日志写到同目录的 `frogbot.log`，数据仍在默认的 `%APPDATA%\frogbot`。8080–8099 上已有免登录的 frog 时，客户端直接复用它；开了登录的服务器模式实例不会被当作本机。

开发网页热更新时可使用现有 `web/vite.config.ts` 的 /api 代理，但不要禁用服务端的同源写校验；服务器模式的登录联调应直接访问后端提供的网页。生产环境先构建网页，再编译 Go，网页资源将嵌入二进制。前端设计和现有路由不变。

## 客户端构建

- Windows 支持 Windows 10 / 11（x64），需要 Node.js 22+。在 `windows-claude/` 执行 `npm ci`，再执行 `npx electron-builder --win nsis --x64 --publish never`，输出 `dist/frog-windows-setup.exe`。开发时用 `npm start` 直接运行。每台服务器的登录会话保存在独立的 Electron session（`persist:frog-server-<id>`）里。安装程序未做代码签名。
- Android 使用 JDK 17 和 Android SDK 35。在 `android-claude/` 执行 `./gradlew :app:assembleRelease`，输出 `app/build/outputs/apk/release/app-release.apk`。正式版 APK 使用固定的发布密钥签名。参见 [Android 构建说明](../android-claude/README.md)。
- Mac 支持 macOS 14 及更新版本。在 `mac-claude/` 运行 `bash scripts/build-app.sh`，输出 `mac-claude/build/frog.app`。设置（API 渠道、模型、Agent、账号）和自动调度器都在 App 内管理。参见 [macOS 构建说明](../mac-claude/README.md)。
- iOS / iPadOS 支持 18 及更新版本。用 Xcode 打开 `ios-claude/frog.xcodeproj`，选自己的开发者账号签名后装到设备上，没有安装包。参见 [iOS 构建说明](../ios-claude/README.md)。
- 网页：构建产物由后端统一提供，不需要另起静态文件服务器。

每次推送会运行 Go 测试和竞态检查，并构建网页、Mac、Android 客户端和 iOS 模拟器版本。Windows 客户端只在发布时构建。参见 [构建工作流](../.github/workflows/clients.yml)。

从 `v0.1.6` 起，推送正式版标签 `vX.Y.Z` 后，GitHub Actions 会等服务端和 Mac、Android 客户端构建通过，再将安装包一并发布；从 `v0.1.8` 起还要等 Windows 安装程序 `frog-windows-setup.exe` 构建通过。原生客户端自动检查 GitHub 正式版，Android 安装由系统界面确认，Windows 提示后下载新的安装程序，iOS 只提示有新版本。参见 [发布工作流](../.github/workflows/release.yml)。

发布前在 GitHub 仓库的「Settings → Secrets and variables → Actions」配置签名材料。

| 类型 | 名称 | 内容 |
| --- | --- | --- |
| Variable | `FROG_SPARKLE_PUBLIC_KEY` | Ed25519 公钥的 Base64 |
| Secret | `FROG_SPARKLE_PRIVATE_KEY` | 对应的 32 字节私钥种子的 Base64 |
| Secret | `FROG_ANDROID_KEYSTORE_B64` | Android 发布用 PKCS#12 密钥库的 Base64。密钥别名为 `frog`，密钥库与密钥使用相同密码 |
| Secret | `FROG_ANDROID_KEYSTORE_PASSWORD` | Android 密钥库密码 |

Windows 安装程序目前不签名，不需要额外配置。

安全备份签名密钥，后续发布沿用同一套密钥，已安装的客户端才能验证并接受更新。

## Docker + HTTPS 部署

已提供多阶段 `Dockerfile`、`compose.yaml` 和宿主机 Caddy 示例。先安装 Docker Compose；在仓库根目录执行：

```bash
docker compose up -d --build
docker compose logs frogbot
```

首次启动的随机密码只在日志中打印一次，应妥善保存，登录后修改。也可启动前设置 `FROGBOT_USER` / `FROGBOT_PASSWORD`，仅在首次创建账号时使用，升级不会覆盖现有密码。不要把密码提交到 Git。

Compose 默认只将 8080 映射到宿主机 `127.0.0.1`，避免把 HTTP 管理面板直接暴露公网。将域名解析到服务器，放行 80/443，把 [deploy/Caddyfile](../deploy/Caddyfile) 中的域名替换后合并进宿主机 Caddy 配置。示例的反代地址针对 Caddy 运行在宿主机，不适用于另一个容器内的 127.0.0.1。代理需要保持 SSE 流式响应，不要缓存或设置短响应超时。

容器以非 root 运行，未挂载宿主机目录或 Docker socket。它仍不是强隔离的 Agent 沙箱：shell 可在容器内执行、可联网，危险操作仍需审批；请用专门实例，不要与重要数据混用。容器只自带 Go 服务、curl、git，不包含 Node/Python/各家 Agent CLI；需要这些执行环境时扩展镜像。

命名卷 `frogbot-data` 挂载整个 `/var/lib/frogbot`，保存 `data/`、`workspace/`、`workspace-bots/`。备份需完整覆盖这些内容（包括 auth、master.key、密钥库及日志），备份包含敏感数据，必须加密并限制访问。普通 `docker compose down` 保留卷，`down -v` 会删除它。恢复应先停服务再替换完整数据。

## 共用接口约定

| 接口 | 用途 |
| --- | --- |
| `GET /api/health` | 公开健康检查，返回 ok、version、api_version |
| `POST /api/auth/login` | JSON username/password，返回会话 Cookie |
| `GET /api/auth/me` | 当前账号与登录模式 |
| `POST /api/auth/logout`、`/api/auth/password` | 登出、修改密码 |
| `GET /api/capabilities` | 登录后读取版本、客户端协议与上传限制 |
| `GET/POST /api/bots` | 列表、创建 Bot |
| `GET /api/bots/{id}/stream` | SSE 实时事件；重连传 Last-Event-ID 或 ?after= |
| `POST /api/bots/{id}/messages` | JSON text、files，发送消息 |
| `POST /api/bots/{id}/uploads` | multipart 文件上传 |
| `GET /api/bots/{id}/files?path=…` | 受登录保护的工作区文件 |

任务、审批、设置、调度等沿用现有接口，详见主 README。会话 7 天过期，修改密码使旧会话失效；浏览器、WebView 和 Windows 客户端自动携带 Cookie，Mac 显式带 Cookie；不为客户端另开匿名接口，也不开放任意跨域。网页写请求必须同源，原生客户端无需伪造 Origin。反向代理应覆盖外来转发头，并只把请求转发到内网后端。

JSON 接口在修改状态前校验完整对象、类型及大小（通常最多 1 MiB，个别接口更小）。附件单文件最多 25 MiB，每请求总计最多 26 MiB、16 个文件；中断或错误的 multipart 会回滚本次已写入文件。SSE 每 20 秒心跳，历史补发后有 replay.done 标记，客户端不需要轮询聊天记录。

## 客户端 Bot 互联（Windows / Mac / Android / iOS）

一个客户端里登录了几台服务器时，可以让这些服务器上的聊天 Bot 互相发文字：问对方记得什么，或者托付一件事，由对方在它自己的机器上决定做不做、怎么做。网页版不参与。

- 只在客户端开着、「设置 › Bot 互联」总开关打开时工作；每台服务器、每个 Bot 还有各自的开关。默认关闭。
- 服务器之间不直接连接，也不保存互联配置。客户端对每台参与的服务器长轮询 `POST /api/relay/sync`（上报 `client_id`、本机参与的 Bot、其他服务器上可联系的 Bot，取走待转发消息，带回执），再把消息转给目标服务器的 `POST /api/relay/inbox`。
- 对方地址形如 `relay:<服务器代号>/<BotID>`，出现在 `list_agents` 里，用 `ask_agent` / `message_agent` 发送。
- 不排队：12 秒内没转交成功就直接报错；客户端关闭开关或退出时立刻通知服务器，异常退出则 45 秒后自动失效。
- 只认本机（local）上的 Bot；每个客户端各自独立，同一台服务器可以同时被几个客户端接入。
- iOS 切到后台后互联暂停（会马上通知服务器），回到前台自动恢复。

## 验证与限制

验证命令：

```bash
go test ./...
go test -race ./...
go vet ./...
cd web && npm run build
cd ../mac-claude && swift build
cd ../windows-claude && npm ci && node --check src/main.js
```

新增 HTTP 集成测试覆盖 Cookie jar 与原生 Cookie 登录、多客户端协议共享消息、SSE 两种游标续传、改密码后旧会话失效；输入测试覆盖损坏 JSON、截断上传回滚。这是服务端协议测试，不是各客户端的 UI 自动化测试。

GitHub Actions 构建 APK、Mac App、iOS 模拟器版本和 Windows 安装程序。真机安装和交互验证由设备端完成。
