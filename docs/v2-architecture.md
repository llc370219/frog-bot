# Frog Bot V2 架构与实现总纲

本文档归档 Frog Bot V2 的生产级重构架构与包映射，完整覆盖 8 大模块与 5 个演进阶段。

## 总体架构

单台服务端采用 **Host Daemon（宿主守护）+ Bot Actor（多助理实例）** 双层微内核架构：
- **Host 层 (`internal/host`)**：统一掌管标准化目录与权限、Host Doctor、资源互斥租约锁、Blind Vault、LocalCLIPool、QuotaManager、Playwright 浏览器池、单点总控网关与 FrogLink Mesh。
- **Bot Actor 层 (`internal/host/actor.go` + `internal/bot`)**：每个 Bot 维护单一顺序事件信箱（`UserMessage` / `TaskCallback` / `PeerMessage` / `ApprovalResult`）与无状态后台 Task Session 分流，并由自有 API 预算熔断器保护步数、外部事件令牌桶和每日 Token 上限。

## 模块与代码包映射

| 模块 | 核心包 / 入口 | 关键能力 |
|---|---|---|
| 模块一：Host/Bot 双层 Actor 内核与预算防爆 | `internal/host/`, `internal/bot/` | 标准化目录与 `config.yaml`、Host Doctor、Actor 事件信箱与无状态分流、TTL 互斥租约锁、步数/令牌桶/日 Token 熔断 |
| 模块二：Hermes 防爆记忆与技能库 | `internal/memory/` | 条目 ID 原子锁 `USER.md`(≤1400字)/`MEMORY.md`(≤2200字)、静态前缀冻结锁死 Prompt Cache、SQLite FTS5 (`trigram`) 中文归档与 `session_search`、>1200 字指针化与 `read_file_lines`、`SKILL.md` 闭环 |
| 模块三：Blind Vault、污点追踪与独立审查 | `internal/vault/`, `internal/security/`, `cmd/frog-exec/` | `vault.enc` AES-256-GCM 与 `secret://` 用途规则/白名单、多编码与高熵片段脱敏、全链路污点追踪（拦记忆/技能/定时投毒）、三态审查网关、`frog-exec` Unix Socket 受控执行桥 |
| 模块四：降权隔离官方 Coding CLI 池 | `internal/clipool/` | Auto/Plan 异步黑盒调度、`workspace_write:<bot>` 租约锁、Linux UID/`bwrap`/Landlock 降权、`logs/cli_<id>.log` 静默落盘、≤800 字浓缩回调与 `cancel_cli_task` |
| 模块五：无侵入 CLI 额度嗅探与熔断 | `internal/clipool/quota.go` | 管道正则实时嗅探 + 本地只读日志/账本估算、抢占式优先级信号量（用户 > 自愈 > 定时）、安全保留线与 `fallback_chain` 自动切换 |
| 模块六：Playwright 浏览器池与 24h 器官 | `internal/organs/` | 纯 Go IMAP IDLE 两级过滤漏斗、防风控随机抖动 Cron (`~15m`) 与早晚报 Digest Queue、按需冷启闲置即焚浏览器池（禁导出 Cookie/StorageState）、就绪重放 CDP 远程过验证与改版自愈 |
| 模块七：单点总控网关与离线唤醒 | `internal/gateway/`, `mac-claude/`, `android-claude/`, `web/` | `/api/v2/ws` 多路复用单点总控网关、`bot@node` 全网漫游、FCM/UnifiedPush/Bark/Telegram 可插拔离线唤醒、审批白名单卡片、CDP 触控过验证画布、实时 CLI 日志抽屉 |
| 模块八：FrogLink Mesh 与资产灾备 | `internal/mesh/`, `internal/ops/`, `cmd/frogctl/` | Ed25519 一键配对、公网自动 NAT 中继与端到端加密信封、不破缓存的静态通讯录 + 内存心跳、`send_peer_message` 跨服污点传递、每日磁盘 GC、`frogctl backup/restore` 一键加密迁移 |
