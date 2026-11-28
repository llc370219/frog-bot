#!/usr/bin/env bash
# 打发布包：构建前端 → 打包进 Go 程序 → 交叉编译 → 压缩 → SHA256SUMS。
# 用法：scripts/release.sh v0.1.2     输出在 dist/
set -euo pipefail
VERSION="${1:?用法: scripts/release.sh <版本号，例如 v0.1.2>}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT/dist"
cd "$ROOT"

(cd web && npm ci && npm run build)
test -f web/dist/index.html || { echo "前端没有构建成功" >&2; exit 1; }
touch web/dist/.gitkeep  # vite 会清空 dist，占位文件放回去（它被 git 跟踪）

rm -rf "$OUT" && mkdir -p "$OUT"
CHANNEL="正式版"; case "$VERSION" in *-*) CHANNEL="测试版" ;; esac
for target in windows/amd64 darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do
  GOOS="${target%/*}"; GOARCH="${target#*/}"
  name="frogbot_${VERSION}_${GOOS}_${GOARCH}"
  stage="$OUT/$name"; mkdir -p "$stage"
  bin="frogbot"; [ "$GOOS" = windows ] && bin="frogbot.exe"
  echo "→ $GOOS/$GOARCH"
  CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION" -o "$stage/$bin" ./cmd/frogbot
  if [ "$GOOS" != windows ]; then
    ln -s "$bin" "$stage/frog"
  fi
  cp LICENSE ATTRIBUTION.md "$stage/"
  mkdir -p "$stage/licenses" && cp licenses/* "$stage/licenses/"
  cat > "$stage/使用说明.txt" <<TXT
frog ${VERSION}（${CHANNEL}，MIT 许可证）

【推荐】一键安装到系统 PATH 后直接用 frog 命令：
- Windows PowerShell：
    irm https://raw.githubusercontent.com/llc370219/frog-bot/main/scripts/install.ps1 | iex
- macOS / Linux 服务器：
    curl -fsSL https://raw.githubusercontent.com/llc370219/frog-bot/main/scripts/install.sh | bash
    （Linux 服务器部署 systemd 请加 sudo：... | sudo bash）

【常用 CLI 指令】（本机与服务器完全统一，同时兼容 ${bin}）
  frog                 打开交互式状态面板与快捷菜单
  frog start           后台启动服务（内置端到端加密穿透，关闭终端不退出）
  frog pair            终端直接打印二维码与连接码，手机扫码即连
  frog status          查看运行状态、远程穿透与已装 Coding CLI
  frog logs -f         实时查看日志
  frog update          一键检查并升级（支持 frog update beta / stable）
  frog service install 一键开启开机自启
  frog passwd          重置登录密码
  frog stop / restart  停止或重启服务

注意：后台 agent 能在宿主机执行命令，高危操作默认会先请你确认；服务器模式请用专门的 VPS。
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
