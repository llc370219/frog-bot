#!/usr/bin/env bash
# 打发布包：构建前端 → 打包进 Go 程序 → 交叉编译 → 压缩 → SHA256SUMS。
# 用法：scripts/release.sh v0.1.2-beta     输出在 dist/
set -euo pipefail
VERSION="${1:?用法: scripts/release.sh <版本号，例如 v0.1.1-beta>}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT/dist"
cd "$ROOT"

(cd web && npm ci && npm run build)
test -f web/dist/index.html || { echo "前端没有构建成功" >&2; exit 1; }
touch web/dist/.gitkeep  # vite 会清空 dist，占位文件放回去（它被 git 跟踪）

rm -rf "$OUT" && mkdir -p "$OUT"
for target in windows/amd64 darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do
  GOOS="${target%/*}"; GOARCH="${target#*/}"
  name="frogbot_${VERSION}_${GOOS}_${GOARCH}"
  stage="$OUT/$name"; mkdir -p "$stage"
  bin="frogbot"; [ "$GOOS" = windows ] && bin="frogbot.exe"
  echo "→ $GOOS/$GOARCH"
  CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION" -o "$stage/$bin" ./cmd/frogbot
  cp LICENSE ATTRIBUTION.md "$stage/"
  mkdir -p "$stage/licenses" && cp licenses/* "$stage/licenses/"
  cat > "$stage/使用说明.txt" <<TXT
frog bot $VERSION（测试版）

【本机版】自己电脑上用
1. 运行 $bin（Windows 双击；macOS / Linux 在终端里执行 ./$bin）。命令行窗口别关，关掉就退出了。
2. 浏览器会自动打开管理页面（默认 http://127.0.0.1:8080）。
3. 在「设置 → API 渠道」填模型的接口地址、API Key、模型名，保存后就能聊天。
本机版只允许从这台电脑访问，没有登录功能。

【服务器版】放在 Linux 服务器上，从外网用用户名和密码登录
一行命令安装（自动下载、校验、创建 systemd 服务，最后打印访问地址、用户名和初始密码）：
  curl -fsSL https://raw.githubusercontent.com/llc370219/frog-bot/main/scripts/install.sh | sudo bash
也可以手动运行：./frogbot -server（第一次启动时在终端打印初始密码）。
HTTP 是明文，强烈建议配域名 + HTTPS：在安装命令里加 DOMAIN=你的域名（自动配置 Caddy）。

注意：后台 agent 的 shell 工具没有沙箱，有风险的操作默认会先请你确认；服务器版请用专门的 VPS。
macOS 提示无法验证开发者：系统设置 → 隐私与安全性 → 仍要打开，或执行 xattr -d com.apple.quarantine ./frogbot
Windows SmartScreen 提示：更多信息 → 仍要运行
项目主页：https://github.com/llc370219/frog-bot
TXT
  if [ "$GOOS" = windows ]; then
    (cd "$OUT" && python3 -c 'import shutil,sys; shutil.make_archive(sys.argv[1], "zip", ".", sys.argv[1])' "$name")
  else
    tar -C "$OUT" -czf "$OUT/$name.tar.gz" "$name"
  fi
  rm -rf "$stage"
done
(cd "$OUT" && sha256sum frogbot_* > SHA256SUMS)
ls -la "$OUT"
