# Frog Bot 免费云端 E2EE 盲中继（Cloudflare Workers Free Plan）

只需 **3 步、$0/月**（无需绑定信用卡，基于 Cloudflare Workers Free Plan 的 SQLite Durable Objects + WebSocket Hibernation API，每天免费 10 万条入站消息、休眠期 0 算力消耗）：

1. 登录 Cloudflare（免费账号即可）：
   ```bash
   cd deploy/cf-relay
   npx wrangler login
   ```
2. 一键部署：
   ```bash
   npx wrangler deploy
   ```
   部署完成后会得到你的专属标准 HTTPS/WSS 443 地址，例如：
   `wss://frog-e2ee-relay.<你的子域>.workers.dev/ws`
3. （可选）给中继加一把访问令牌，防止他人蹭用：
   ```bash
   npx wrangler secret put RELAY_TOKEN
   ```

然后回到家里 Win / Mac 的 Frog Bot 网页端 **「设置 → 远程连接」**：
- 模式选 **「E2EE 盲中继（推荐）」**；
- 填入上面的 `wss://.../ws` 和令牌并保存；
- 点击 **「添加设备」** 生成二维码，用 iOS / Android / macOS 客户端扫码即可实现 **同 Wi-Fi 局域网直连 + 出门 4G/5G 无感切 WSS 443 端到端加密穿透**！

> **安全说明**：所有聊天、审批、文件、密钥请求均在你的电脑和手机之间通过 **X25519 + PSK + HKDF-SHA256 + AES-256-GCM** 端到端加密，Cloudflare Worker 仅转发乱码密文，绝对无法查看任何明文内容。
