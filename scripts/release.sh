#!/usr/bin/env bash
# 打发布包：构建前端 → 打包进 Go 程序 → 交叉编译 → 压缩 → SHA256SUMS。
# 用法：scripts/release.sh v0.1.1-beta     输出在 dist/
set -euo pipefail
VERSION="${1:?用法: scripts/release.sh <版本号，例如 v0.1.1-beta>}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT/dist"
cd "$ROOT"

(cd web && npm ci && npm run build)
test -f web/dist/index.html || { echo "前端没有构建成功" >&2; exit 1; }

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
frog bot $VERSION（本机测试版）

1. 运行 $bin（Windows 双击；macOS / Linux 在终端里执行 ./$bin）。命令行窗口别关，关掉就退出了。
2. 浏览器会自动打开管理页面（默认 http://127.0.0.1:8080）。
3. 第一次会让你填模型的 API 地址、API Key、模型名，保存后就能聊天。

注意：只在本机使用，没有登录功能，不要暴露到公网；shell 工具没有沙箱；有风险的操作默认会先请你确认。
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
