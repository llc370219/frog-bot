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

数据目录默认：当前目录有 `./data` 就用它；否则 macOS `~/Library/Application Support/frogbot`、Linux `~/.config/frogbot`；安装脚本用 `/var/lib/frogbot`。备份时同时保留数据目录和工作区。

> `master.key` 与 `secrets.json` 在同一目录，等于钥匙和锁放在一起。正式使用请通过 `FROGBOT_MASTER_KEY` 注入主密钥并妥善备份。
