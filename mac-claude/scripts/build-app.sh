#!/usr/bin/env bash
# 打包 Frog Bot Claude：编译 release，组装成 build/Frog Bot Claude.app，并做本机（ad-hoc）签名。
# 需要 Xcode 或 Command Line Tools（Swift 5.9 以上，macOS 14 SDK）。
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT="$(pwd)"
APP="$ROOT/build/Frog Bot Claude.app"

echo "→ 编译（release）…"
swift build -c release 2>&1
BIN_DIR="$(swift build -c release --show-bin-path)"

echo "→ 组装 $APP"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$BIN_DIR/FrogBotClaude" "$APP/Contents/MacOS/FrogBotClaude"
cp "$ROOT/Support/Info.plist" "$APP/Contents/Info.plist"

ICON_SRC="$ROOT/../web/public/frog.png"
if [ -f "$ICON_SRC" ] && command -v iconutil >/dev/null 2>&1; then
  ICONSET="$ROOT/build/AppIcon.iconset"
  rm -rf "$ICONSET" && mkdir -p "$ICONSET"
  for size in 16 32 128 256 512; do
    sips -z "$size" "$size" "$ICON_SRC" --out "$ICONSET/icon_${size}x${size}.png" >/dev/null
    double=$((size * 2))
    sips -z "$double" "$double" "$ICON_SRC" --out "$ICONSET/icon_${size}x${size}@2x.png" >/dev/null
  done
  iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/AppIcon.icns"
  rm -rf "$ICONSET"
fi
# 网页版的 Bot 头像与 Agent 图标一起打包（离线也能显示）
for d in bot-avatars agent-icons; do
  if [ -d "$ROOT/../web/public/assets/$d" ]; then
    mkdir -p "$APP/Contents/Resources/$d"
    cp "$ROOT/../web/public/assets/$d/"* "$APP/Contents/Resources/$d/" 2>/dev/null || true
  fi
done

codesign --force --deep --sign - "$APP" >/dev/null 2>&1 || echo "（跳过签名：codesign 不可用）"
echo "✓ 完成：$APP"
