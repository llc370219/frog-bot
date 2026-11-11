<p align="center"><img src="web/public/frog.png" alt="frog bot" width="88"></p>

<h1 align="center">frog bot</h1>

<p align="center">一群能聊天、也能在后台替你干活的 AI 助理 —— 自托管、可审批、密钥不落进模型</p>

<p align="center">
  <a href="https://github.com/llc370219/frog-bot/releases">下载</a> ·
  <a href="#使用方法">使用方法</a> ·
  <a href="#架构逻辑">架构</a> ·
  <a href="docs/backend-and-clients.md">三端接入文档</a> ·
  <a href="https://t.me/+pKQYEaxzidU4ZjZh">Telegram 群</a>
</p>

---

## 项目介绍

frog bot 是一个**自托管的多 Bot AI 助理**。你在一台机器上运行它的服务端，然后用网页、Mac 或 Android 客户端和它对话。

每个 Bot 由两部分组成：

- **聊天层**：用一个快模型和你对话，负责理解需求、查证事实、派发任务、汇报结果。它回话很快，不会被后台任务卡住。
- **后台执行器**：真正动手干活的 Agent，可以读写文件、执行命令。你可以用内置 Agent（接任意 OpenAI 兼容 API），也可以直接接入你已经订阅的官方 CLI（Codex CLI、Claude Code、Pi、Grok CLI、Antigravity CLI）。

你可以创建最多 32 个 Bot（比如一个做代码审查、一个整理资料），每个 Bot 有独立的对话、任务队列、记忆、密钥库、定时计划和工作区。切换 Bot 时，其他 Bot 的后台任务照常运行。

所有危险操作都要你在界面上点「批准」才会执行；API Key 等密钥由你直接填进加密库，模型全程只看得到名字。

## 架构逻辑

<p align="center"><img src="docs/images/architecture.png" alt="frog bot 系统架构" width="900"></p>
<p align="center"><sub>可编辑矢量图：<a href="docs/images/architecture.svg">architecture.svg</a></sub></p>

### 整体结构

服务端是**一个 Go 二进制**，React 网页在编译时嵌入其中，不需要另外部署前端或数据库。网页、Mac、Android 三端都连同一个后端，共享 Bot、聊天、任务、审批和文件：

- **REST** 负责写操作：发消息、点审批、填密钥卡、操作任务（`/api/bots/{id}/…`）。
- **SSE** 负责推送：每个 Bot 一条事件流，断线后按 `Last-Event-ID` 续传，客户端无需轮询。

### 一个 Bot 内部

| 组件 | 代码位置 | 职责 |
|---|---|---|
| 聊天层 | `internal/bot/chat.go`、`chattools.go` | 快模型 + 管理工具。能自己查任务、读文件、写记忆，简单问题直接回答；复杂工作用 `start_task` 交给后台。**没有批准工具，也不能执行命令。** |
| 监工 Supervisor | `internal/bot/supervisor.go` | 纯 Go 代码，不调用模型。管理任务排队、暂停/继续、检查点；任务完成、失败或需要审批时唤醒聊天层，**每个任务只汇报一次**；长时间无进展（默认 12 分钟）判定卡住并停止。 |
| 执行器 | `internal/onefrog/` | 每个 Bot 二选一：内置 Agent（模型 ↔ 工具循环，工具有 `read_file`、`write_file`、`edit_file`、`list_dir`、`grep`、`shell`），或调用官方订阅 CLI。 |
| 审批守卫 | `internal/onefrog/approval.go` | 每次工具调用前执行，见下文「安全性」。 |
| 定时唤醒器 | `internal/scheduler/` | 支持 once / daily / weekly，到点把普通任务放进队列，本身不调用模型。 |
| 密钥库 | `internal/vault/` | AES-256-GCM 加密，执行前一刻注入，输出时脱敏。 |
| 事件日志 | `internal/bot/store.go` | `stream.jsonl` 只追加。所有状态都由事件重放得到（事件溯源），重启后消息、任务、审批全部恢复。 |

### 一条消息的完整流程

1. 你发消息 → `POST /api/bots/{id}/messages` → 写入事件日志。
2. 聊天层被唤醒，先回一句确认；需要动手时调用 `start_task`。
3. 监工检查全局并发上限（`MAX_CONCURRENT`，所有 Bot 合计，默认 4）：有空位就启动执行器，没有就排队并告诉你排第几。
4. 执行器每走一步都写成事件，**通过 SSE 直接推到界面的工作卡片**，不经过聊天层，所以聊天层不需要守着任务。
5. 遇到需要确认的工具调用，审批守卫发出审批卡片，等你点按钮。
6. 任务结束后，监工把结构化总结（状态、结论、步数、改动文件、用时）写进日志，唤醒聊天层用自己的话汇报一次。

### 关键实现细节

- **安全点暂停**：暂停只在两步之间生效。进行中的模型请求会被取消（没有副作用），工具调用一定会执行完，不会写到一半。每一步之后保存检查点（`checkpoints/<任务ID>.json`），继续时从快照接着做，服务重启后任务也能续跑。
- **长任务压缩**：对话估算超过 `CONTEXT_TOKENS`（默认 6 万）时，把旧步骤压缩成摘要；模型摘要失败时退回机械摘要，并且不会拆开一次工具调用和它的结果。
- **确定性停止**：点 ■、按 Esc 或发「停 / stop / 取消」，由代码立即停止全部任务，不经过模型。
- **调度防重复**：每个触发时刻先原子认领再派发，重启后不会重复派同一次；去向不明的任务标为 `needs_verify` 请你核实，不会自动重放。
- **设备共享资源**：API 渠道池（`api-channels.json`）整台设备只配一次，所有 Bot 共用；每个 Bot 只记录「用哪个渠道的哪个模型」。
- **Bot 互联**：聊天层可以用 `list_agents` / `ask_agent` / `message_agent` 与其他 Bot 协作，范围覆盖本机 Bot、通过设备互联端口配对的远程设备，以及经 Mac / Android 客户端中转的其他服务器。

## 项目亮点

### 🔒 安全性

- **三层审批**：① 写死的规则（越出工作区直接拒绝；`rm -r`、`sudo`、`curl | sh`、联网命令、读 `.ssh` / `.env` 等危险操作升级给你）→ ② 可选的审核模型（`POLICY=auto` 时启用）→ ③ 你本人在卡片上点按钮。
- **审批不能伪造**：批准记录绑定「任务 + 工具 + 参数哈希」，只能由你点击界面按钮产生（`POST /api/approvals/{id}`）。在聊天里说「同意」不算数，聊天层也没有批准工具。
- **密钥对模型不可见**：模型只能写 `{{secret:NAME}}` 占位符，密钥值在执行前一刻替换；`shell` 通过环境变量传入，不拼进命令。工具输出、错误、事件、SSE 和日志里的密钥值（包括 base64、URL 编码、JSON 转义等变体）都会替换成 `[secret:NAME]`。在聊天里误贴的 `sk-…`、`ghp_…` 等也会被自动隐藏。
- **登录与传输**：服务器模式使用 bcrypt 密码、HttpOnly + SameSite=Strict Cookie（HTTPS 下加 Secure）。同一 IP 连续输错 5 次锁定 1 分钟并逐次翻倍，全局 15 分钟内错 30 次暂停所有登录；跨站写请求一律拒绝。本机模式只接受发往 127.0.0.1 的请求。
- **部署加固**：安装脚本创建非 root 的 `frogbot` 用户，systemd 开启 `ProtectSystem=strict`、`NoNewPrivileges` 等限制，只允许写数据目录。Docker 镜像同样以非 root 运行，并去掉全部 capabilities。

> [!WARNING]
> 审批不等于沙箱。`shell` 工具和接入的官方 CLI 都能在服务端机器上执行命令，也有能力把数据发到网络上。请把服务端部署在**专门的 Linux 服务器**上，不要和重要的服务或数据放在一起。

### ⚙️ 自动化

- **后台并行**：多个任务可以同时运行，超出并发上限时自动排队；你随时可以继续发消息、插话补充要求、调整顺序或暂停。
- **定时任务**：在对话里说「每天上午 9 点整理 README」，聊天层会创建计划（需要你批准），到点自动派活。也可以在「自动调度器」页面暂停、恢复、立即执行或删除计划。
- **重启不丢活**：事件日志加检查点，服务重启后未完成的任务会变成「暂停（重启）」，可以从断点继续。
- **主动汇报**：任务完成、失败、卡住或需要审批时，聊天层会主动告诉你；平时进度只在工作卡片和工作条里显示，不刷屏。

### 🤖 Bot 与 Agent 的自定义

- **多 Bot**：每个 Bot 有自己的名字、职责、头像、对话、记忆（分为私有和所有 Bot 共享两种）、密钥和工作区。
- **按 Bot 选模型**：聊天模型和 Agent 模型可以分开配置，分别使用不同渠道的任意 OpenAI 兼容服务（OpenAI、xAI、DeepSeek、OpenRouter、本地模型等）。
- **接入官方订阅 CLI**：Agent 可以改用设备上已登录的 Codex CLI、Claude Code、Pi、Grok CLI 或 Antigravity CLI，直接使用你的订阅额度。默认会去掉 `OPENAI_API_KEY`、`ANTHROPIC_API_KEY` 等环境变量，避免意外按量计费。
- **可调审批策略**：后台任务可设为 `untrusted` / `on-request` / `auto` / `never`；聊天层写文件默认每次都需要确认（`CHAT_POLICY=untrusted`）。
- **跨设备协作**：多台设备可以配对，在侧栏里统一管理各台设备上的 Bot，任务在目标设备上执行，审批也留在目标设备。

## 使用方法

### 服务端和客户端分别是什么

| | 是什么 | 运行在哪里 |
|---|---|---|
| **服务端** | `frogbot` 程序本身：所有 Bot、模型调用、Agent 执行、数据存储都在这里 | 推荐一台专门的 Linux 服务器（x86_64 / arm64） |
| **网页客户端** | 服务端内置，浏览器打开服务端地址即可 | 任何浏览器 |
| **Mac 客户端** | SwiftUI 原生 App，可同时连接多台服务端 | macOS 14+ |
| **Android 客户端** | Jetpack Compose 原生 App，可后台保持连接、接收审批提醒 | Android 8.0+ |

模型 API Key 和 CLI 登录都只保存在服务端，手机和电脑上的客户端只保存 7 天有效的登录会话，不保存密码。

> [!IMPORTANT]
> **不建议在 Windows 上安装服务端。** 服务器模式会监听 `0.0.0.0` 并对外暴露端口，再加上 Agent 能在本机执行命令，放在日常使用的 Windows 电脑上风险很高。另外，systemd 加固和一键安装脚本只支持 Linux，Windows 版服务端目前只完成了编译，没有在真机上验证。Windows 用户请把服务端装在 Linux 服务器上，然后用浏览器访问。

### 1. 安装服务端（Linux 服务器，推荐）

```bash
curl -fsSL https://raw.githubusercontent.com/llc370219/frog-bot/main/scripts/install.sh | sudo bash
```

这个脚本会：

- 自动识别 x86_64 / arm64，从 Releases 下载最新版并校验 SHA256；
- 安装到 `/opt/frogbot`，数据放在 `/var/lib/frogbot`；
- 创建非 root 用户 `frogbot`，注册开机自启、出错自动重启的 systemd 服务；
- 最后打印访问地址、用户名 `admin` 和初始密码（**只显示这一次**）。

**强烈建议配域名启用 HTTPS**（不配域名时，密码和聊天内容都是明文传输）：

```bash
curl -fsSL https://raw.githubusercontent.com/llc370219/frog-bot/main/scripts/install.sh | sudo DOMAIN=bot.example.com bash
```

这会自动安装 Caddy 并申请证书，frog bot 改为只监听本机，访问地址变成 `https://bot.example.com`（需要放行 80 / 443 端口）。

常用操作：

| 操作 | 命令 |
|---|---|
| 升级 | 重新运行安装命令（数据、账号、密码都会保留） |
| 指定端口 / 用户 / 版本 | `… \| sudo PORT=9000 FROGBOT_USER=me VERSION=v0.1.5 bash` |
| 忘记密码 | `sudo frogbot reset-password`（旧登录全部失效，不用重启） |
| 卸载 | `… \| sudo bash -s uninstall`（保留数据；加 `--purge` 一并删除） |

安装脚本**不会修改防火墙**，云服务器请在安全组里放行对应端口。

<details>
<summary>用 Docker 部署</summary>

```bash
docker compose up -d --build
docker compose logs frogbot   # 首次启动的随机密码只在日志里打印一次
```

Compose 默认只把 8080 映射到宿主机的 `127.0.0.1`，请用宿主机上的 Caddy 反代并配置 HTTPS（参考 [deploy/Caddyfile](deploy/Caddyfile)）。数据保存在命名卷 `frogbot-data` 里。镜像只包含 Go 服务、curl 和 git；需要 Node、Python 或各家 CLI 时请自行扩展镜像。详见 [后端与三端接入](docs/backend-and-clients.md)。

</details>

### 2. 配置模型

登录网页后：

1. **设置 → API 渠道**：填写接口地址和 API Key，拉取模型列表。整台设备只需配置一次，Key 加密保存。
2. **设置 → 模型与 Agent**：为每个 Bot 选择聊天模型和 Agent 模型。
3. （可选）想用官方订阅时，在 Agent 卡片里选「官方订阅」→「重新检测」，再选择 CLI。CLI 需要装在服务端机器上，并用服务账户完成登录。

然后点左侧 Bots 旁的「＋」创建更多 Bot。

### 3. 连接客户端

- **网页**：直接打开服务端地址。
- **Mac / Android**：从 [Releases](https://github.com/llc370219/frog-bot/releases) 下载 `clients-v*` 版本，在「设置 → 添加服务器」里填写地址、用户名和密码。手机需要能访问到服务端（公网域名、同一 Wi-Fi 或 Tailscale 等组网工具）。

<details>
<summary>本机模式（macOS / Linux 桌面快速试用）</summary>

下载对应系统的包，解压后运行 `frogbot`，浏览器会自动打开 `http://127.0.0.1:8080`。本机模式只接受来自这台电脑的访问，没有登录，也不能被手机或其他电脑连接；不要为了远程访问而在个人电脑上加 `-server`。

</details>

<details>
<summary>设备互联配置</summary>

在每台设备的数据目录里创建 `devices.json`（或用 `frogbot -devices 路径` 指定），双方互相添加对端，并使用同一个随机令牌（`openssl rand -hex 32`），每对设备使用不同的令牌：

```json
{
  "id": "desk",
  "name": "台式机",
  "listen": "127.0.0.1:8081",
  "peers": [
    { "id": "laptop", "name": "笔记本", "url": "https://frog-peer-laptop.example.com", "token": "<随机令牌>" }
  ]
}
```

互联端口独立于管理界面。对端地址必须是 HTTPS（HTTP 只允许回环地址）；可以用反向代理，也可以在配置里写 `tls_cert` / `tls_key` 直接监听。**只添加你信任的设备**：配对设备可以操作 Bot 会话、工作区文件、任务和模型配置。

</details>

<details>
<summary>从源码运行 / 演示模式 / 测试</summary>

需要 Go 1.22+、Node 20+。

```bash
cd web && npm install && npm run build && cd ..
go run ./cmd/frogbot                     # 本机模式
go run ./cmd/frogbot -server -port 8080  # 服务器模式

# 不用 API Key 先看效果：假模型 + 写死的剧本
go run ./cmd/fakeai &
BASE_URL=http://127.0.0.1:9911 CHAT_MODEL=fake-model FROGBOT_DEMO=1 go run ./cmd/frogbot

# 测试（全部用假模型，不需要 API Key）
go vet ./... && go test -race ./...
```

前端热更新：后端 `go run ./cmd/frogbot`，另开终端 `cd web && npm run dev`，打开 `http://localhost:5173`。打发布包：`scripts/release.sh v0.1.x`。

</details>

<details>
<summary>环境变量</summary>

| 变量 | 默认 | 说明 |
|---|---|---|
| `BASE_URL` / `API_KEY` | — | 初始 API 地址和 Key（`API_KEY` 也认 `OPENAI_API_KEY`、`XAI_API_KEY` 等） |
| `CHAT_MODEL` / `AGENT_MODEL` | 互为默认 | 设置后所有 Bot 的 API 模型优先使用环境变量（不写盘） |
| `REVIEWER_MODEL` | 空 | 审核模型，只在 `POLICY=auto` 时使用 |
| `POLICY` | `on-request` | 后台任务审批模式：`untrusted` / `on-request` / `auto` / `never` |
| `CHAT_POLICY` | `untrusted` | 聊天层写文件的审批模式 |
| `FROGBOT_MODE` | 空 | 设为 `server` 等同于 `-server` |
| `ADDR` / `PORT` | 本机 `127.0.0.1:8080`，服务器 `0.0.0.0:8080` | 监听地址 / 端口 |
| `DATA_DIR` / `WORKSPACE` | 见下 | 数据目录 / Agent 工作区 |
| `FROGBOT_USER` / `FROGBOT_PASSWORD` | `admin` / 随机 | 服务器模式首次启动时创建的账号 |
| `FROGBOT_MASTER_KEY` | 空 | 密钥库主密钥（32 字节 hex / base64）；未设置时自动生成 `master.key`（0600） |
| `MAX_CONCURRENT` | `4` | 所有 Bot 合计的后台任务并发上限 |
| `TASK_MAX_MINUTES` | `30` | 单个任务的最长时间 |
| `CONTEXT_TOKENS` | `60000` | 长任务压缩阈值 |

数据目录默认：当前目录下有 `./data` 就用它；否则 macOS 为 `~/Library/Application Support/frogbot`，Linux 为 `~/.config/frogbot`；安装脚本使用 `/var/lib/frogbot`。备份时请同时保留数据目录和工作区。

> `master.key` 和 `secrets.json` 放在同一个目录，相当于钥匙和锁放在一起。正式使用请通过 `FROGBOT_MASTER_KEY` 从系统密钥管理注入主密钥，并妥善备份。

</details>

## 已知限制

- 单管理员账号，不支持多用户和权限分级。
- 审批不是沙箱：有 shell 的 Agent 仍可能以脱敏识别不了的方式外发数据；真正的隔离需要放进无外网或只放行特定域名的容器。
- 目前只用假模型做过完整测试，真实模型下聊天层的表现取决于提示词；macOS / Windows 版服务端只完成了交叉编译，程序没有签名。
- Mac / Android 客户端仍在早期阶段，没有上架商店，也没有离线推送。
- 事件日志只追加不压缩，长期使用会持续变大。
- 并行任务同时修改同一个文件时没有加锁。

## 社区与许可证

- 交流：[Telegram 群](https://t.me/+pKQYEaxzidU4ZjZh)（与 [OneFrog](https://github.com/llc370219/onefrog) 共用）· LINUX DO 社区
- 许可证：[MIT](LICENSE)（自 v0.1.2 起；v0.1.2-beta 及更早版本按 AGPL-3.0 发布）。界面、logo、头像和 Markdown 渲染器沿用自同一作者的 [Onefrog](https://github.com/llc370219/onefrog) v0.1.31，详见 [ATTRIBUTION.md](ATTRIBUTION.md)；第三方组件许可见 [THIRD_PARTY_NOTICES](licenses/THIRD_PARTY_NOTICES.txt)。
