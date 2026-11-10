# 后端与网页 / Android / Mac 三端

保留现有 React 前端，以 Go 服务作为唯一数据源。网页直接访问服务；Android 首版是原生连接页 + WebView，复用同一前端；Mac 为已有的 SwiftUI 原生客户端。三端连接同一服务器时，共享 Bot、聊天、任务、审批和文件，不需要分别部署三个后端。

当前产品边界：单管理员，不是多租户 SaaS。还没有用户注册、多人权限隔离、离线模式、Android 系统推送或商店发布。模型和 CLI 在后端机器运行，手机和 Mac 不需要保存模型密钥。

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

首次启动在终端打印管理员名和随机初始密码。网页访问电脑的 8080 端口；Android 和 Mac 填同一台电脑的局域网地址。不要把 127.0.0.1 填进手机，它指向手机自己。Android 模拟器用 `http://10.0.2.2:8080`，明文只限可信网络的 Debug 调试。正式连接用 HTTPS。

在网页「设置 → API 渠道」配置接口、API Key 和模型，或在「连接」中选已安装且登录的 Agent CLI，之后才能正常调用模型。仅验证登录、消息持久化和接口无需模型。无登录本机模式不是三端共享部署方案。

开发网页热更新时可使用现有 `web/vite.config.ts` 的 /api 代理，但不要禁用服务端的同源写校验；服务器模式的登录联调应直接访问后端提供的网页。生产环境先构建网页，再编译 Go，网页资源将嵌入二进制。前端设计和现有路由不变。

## 客户端构建

- Android：见 [android/README.md](../android/README.md)。Java 17、Gradle 8.9、SDK 35，执行 `gradle :app:assembleDebug`。Release 禁止 HTTP，Debug 可显式选择 HTTP 并确认风险。支持系统文件上传和另存为下载；不申请全盘存储权限。
- Mac：见 [mac/README.md](../mac/README.md)。macOS 14+，在 `mac/` 运行 `bash scripts/build-app.sh`，输出 `mac/build/Frog Bot.app`。本机临时签名不等于 Apple 公证；对外分发另配 Developer ID。设置和自动调度器仍从浏览器管理。
- 网页：构建产物由后端统一提供，不需要另起静态文件服务器。

新增 GitHub Actions 配置 [.github/workflows/clients.yml](../.github/workflows/clients.yml)：Go 测试与竞态检查、网页构建、Mac 打包、Android 编译与 lint。需提交并推送到 GitHub 才会运行；配置存在不代表云端构建已通过，也不会自动发布商店。

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
| `GET /api/capabilities` | 登录后读取版本、三端协议与上传限制 |
| `GET/POST /api/bots` | 列表、创建 Bot |
| `GET /api/bots/{id}/stream` | SSE 实时事件；重连传 Last-Event-ID 或 ?after= |
| `POST /api/bots/{id}/messages` | JSON text、files，发送消息 |
| `POST /api/bots/{id}/uploads` | multipart 文件上传 |
| `GET /api/bots/{id}/files?path=…` | 受登录保护的工作区文件 |

任务、审批、设置、调度等沿用现有接口，详见主 README。会话 7 天过期，修改密码使旧会话失效；浏览器/WebView 自动携带 Cookie，Mac 显式带 Cookie；不为客户端另开匿名接口，也不开放任意跨域。网页写请求必须同源，原生客户端无需伪造 Origin。反向代理应覆盖外来转发头，并只把请求转发到内网后端。

JSON 接口在修改状态前校验完整对象、类型及大小（通常最多 1 MiB，个别接口更小）。附件单文件最多 25 MiB，每请求总计最多 26 MiB、16 个文件；中断或错误的 multipart 会回滚本次已写入文件。SSE 每 20 秒心跳，历史补发后有 replay.done 标记，三端不需要轮询聊天记录。

## 验证与限制

验证命令：

```bash
go test ./...
go test -race ./...
go vet ./...
cd web && npm run build
cd ../mac && swift build
```

新增 HTTP 集成测试覆盖 Cookie jar 与原生 Cookie 登录、三端协议共享消息、SSE 两种游标续传、改密码后旧会话失效；输入测试覆盖损坏 JSON、截断上传回滚。这是服务端协议测试，不是三台设备的 UI 自动化测试。

本次机器没有 JDK、Android SDK 和 Docker：Android APK / 真机交互及 Docker 镜像仍需在相应环境验证；未部署到公网、未执行云端 CI、未配置商店签名。不要把这些未验证项视为已上线。
