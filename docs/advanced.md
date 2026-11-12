# 进阶配置

## Docker 部署

```bash
docker compose up -d --build
docker compose logs frogbot   # 首次启动的随机密码只在日志里打印一次
```

Compose 只把 8080 映射到宿主机 `127.0.0.1`，请用宿主机 Caddy 反代并开启 HTTPS（参考 [deploy/Caddyfile](../deploy/Caddyfile)）。数据在命名卷 `frogbot-data`。镜像只含 Go 服务、curl、git，需要 Node / Python / 各家 CLI 时请自行扩展。更多见 [后端与三端接入](backend-and-clients.md)。

## 设备互联

在每台设备的数据目录创建 `devices.json`（或 `frogbot -devices 路径`），双方互相添加，使用同一个随机令牌（`openssl rand -hex 32`），每对设备用不同令牌：

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

互联端口独立于管理界面；对端必须是 HTTPS（HTTP 只允许回环地址），可用反向代理或在配置里写 `tls_cert` / `tls_key`。**只添加信任的设备**：配对设备可以操作 Bot 会话、工作区文件、任务和模型配置。

## 从源码运行

需要 Go 1.22+、Node 20+。

```bash
cd web && npm install && npm run build && cd ..
go run ./cmd/frogbot                     # 本机模式
go run ./cmd/frogbot -server -port 8080  # 服务器模式

# 不用 API Key 看效果：假模型 + 写死的剧本
go run ./cmd/fakeai &
BASE_URL=http://127.0.0.1:9911 CHAT_MODEL=fake-model FROGBOT_DEMO=1 go run ./cmd/frogbot

# 测试（全部用假模型）
go vet ./... && go test -race ./...
```

前端热更新：后端 `go run ./cmd/frogbot`，另开终端 `cd web && npm run dev`，打开 `http://localhost:5173`。打发布包：`scripts/release.sh v0.1.x`。

## 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `BASE_URL` / `API_KEY` | — | 初始 API 地址和 Key（`API_KEY` 也认 `OPENAI_API_KEY`、`XAI_API_KEY` 等） |
| `CHAT_MODEL` / `AGENT_MODEL` | 互为默认 | 设置后所有 Bot 的 API 模型优先用环境变量（不写盘） |
| `REVIEWER_MODEL` | 空 | 审核模型，只在 `POLICY=auto` 时使用 |
| `POLICY` | `on-request` | 后台任务审批：`untrusted` / `on-request` / `auto` / `never` |
| `CHAT_POLICY` | `untrusted` | 聊天层写文件的审批模式 |
| `FROGBOT_MODE` | 空 | 设为 `server` 等同 `-server` |
| `ADDR` / `PORT` | 本机 `127.0.0.1:8080`，服务器 `0.0.0.0:8080` | 监听地址 / 端口 |
| `DATA_DIR` / `WORKSPACE` | 见下 | 数据目录 / Agent 工作区 |
| `FROGBOT_USER` / `FROGBOT_PASSWORD` | `admin` / 随机 | 服务器模式首次启动创建的账号 |
| `FROGBOT_MASTER_KEY` | 空 | 密钥库主密钥（32 字节 hex / base64）；未设置时自动生成 `master.key`（0600） |
| `MAX_CONCURRENT` | `4` | 所有 Bot 合计的后台任务并发上限 |
| `TASK_MAX_MINUTES` | `30` | 单个任务最长时间 |
| `CONTEXT_TOKENS` | `60000` | 长任务压缩阈值 |
| `FROGBOT_ROOTD_SOCKET` | `/run/frogbot-rootd/rootd.sock` | 提权助手的 socket |
| `FROGBOT_NODE_MIRROR` | `https://nodejs.org/dist` | 一键安装 CLI 时下载 Node.js 的地址（国内可用 `https://npmmirror.com/mirrors/node`）；npm 包地址用 npm 自己的 `npm_config_registry` |

数据目录默认：当前目录有 `./data` 就用它；否则 macOS `~/Library/Application Support/frogbot`、Linux `~/.config/frogbot`；安装脚本用 `/var/lib/frogbot`。备份时同时保留数据目录和工作区。

## 安装 Agent CLI

「设置 → 模型与 Agent → 官方订阅」里，Codex CLI、Claude Code、Pi 可以一键安装或更新（Linux / macOS，x86_64 / arm64）。frog 下载 Node.js 22 官方压缩包、按 `SHASUMS256.txt` 校验后装到 `<数据目录>/tools/node`，再用它的 npm 把 CLI 装到 `<数据目录>/tools/clis`，不需要 root，也不改系统目录。frog 装的版本优先于 PATH 里的同名 CLI。

检测 CLI 时除了 PATH，还会找服务用户家目录里的 `~/.local/bin`、`~/bin`、`~/.npm-global/bin`、`~/.npmrc` 的 `prefix`、nvm 的各个 Node 版本、Volta、Bun、pnpm。启动时会把 CLI 所在目录放到 PATH 最前面，所以 `#!/usr/bin/env node` 的 CLI 能找到同目录的 node。

安装脚本的 systemd 服务设置了 `NoNewPrivileges`，后台任务自己拿不到 sudo；需要 root 时走下面的提权。

## Agent 沙箱与提权

**沙箱**（Linux 5.13+，内核启用 Landlock 时自动开启）：Agent 的 shell 命令、外部 CLI、一键安装时的 npm 都在 Landlock 沙箱里运行，读写不了 frog 的数据目录（登录数据和会话签名密钥、密钥库、事件日志），只开放 `<数据目录>/tools`（只读）和工作区。frog 主进程设为不可调试，Agent 也不能 ptrace 它或读它的内存。启动时会实际跑一次自检，通过了才算启用；启动信息和「设置 → 模型与 Agent → 提权到 root」里能看到状态。

**提权**（`frogbot-rootd`，安装脚本默认安装，`ROOT_HELPER=0` 可以不装）：内置 Agent 有一个 `run_as_root` 工具，调用时弹出红色卡片，显示完整命令；你输入登录密码后，由以 root 运行的提权助手执行。

- 每条 root 命令都要你批准；可以勾选「之后 10 分钟内免输密码」（最长 30 分钟），窗口内每条仍要点批准，可以在设置里随时结束窗口或关闭提权。
- 提权助手只接受 frogbot 服务主进程的连接（核对 unix socket 对端的 PID 和 UID），自己保存一份只有 root 能读的密码哈希（`/etc/frogbot/rootd.json`），密码连续输错 5 次锁 15 分钟。网页上改密码、`sudo frogbot reset-password` 都会同步过去。
- 每条命令写进 `/var/log/frogbot-rootd.log`（只有 root 能读）。
- 只在服务器模式（有登录）、沙箱通过自检时可用。外部 CLI（Codex、Claude Code、Pi）用它们自己的工具，调用不了 `run_as_root`。

你批准的命令就是真的以 root 运行，请看清再批准。服务器仍然建议专机专用。

> `master.key` 与 `secrets.json` 在同一目录，等于钥匙和锁放在一起。正式使用请通过 `FROGBOT_MASTER_KEY` 注入主密钥并妥善备份。
