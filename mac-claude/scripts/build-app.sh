#!/usr/bin/env bash
# 编译、打包并临时签名 build/frog.app。需要 Xcode 或 Command Line Tools。
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT="$(pwd)"
APP="$ROOT/build/frog.app"
VERSION="${FROG_VERSION:-$(plutil -extract CFBundleShortVersionString raw Support/Info.plist)}"
if ! [[ "$VERSION" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "FROG_VERSION 必须是 MAJOR.MINOR.PATCH" >&2
  exit 1
fi

SPARKLE_VERSION=2.10.1
SPARKLE_SHA256=1a856d2a9edb8e628de88121d7be04cf0e2167f8641650b78fca00b14244cf27
SPARKLE_DIR="$ROOT/build/Sparkle-$SPARKLE_VERSION"
if [ ! -f "$SPARKLE_DIR/Sparkle.framework/Sparkle" ] || [ ! -x "$SPARKLE_DIR/bin/sign_update" ]; then
  mkdir -p "$ROOT/build"
  DOWNLOAD="$(mktemp -d "$ROOT/build/.sparkle.XXXXXXXX")"
  trap 'rm -rf "$DOWNLOAD"' EXIT
  curl -fsSL --retry 2 --output "$DOWNLOAD/Sparkle.tar.xz" \
    "https://github.com/sparkle-project/Sparkle/releases/download/$SPARKLE_VERSION/Sparkle-$SPARKLE_VERSION.tar.xz"
  (cd "$DOWNLOAD" && printf '%s  Sparkle.tar.xz\n' "$SPARKLE_SHA256" | shasum -a 256 -c -)
  mkdir "$DOWNLOAD/extracted"
  tar -xf "$DOWNLOAD/Sparkle.tar.xz" -C "$DOWNLOAD/extracted"
  rm -rf "$SPARKLE_DIR"
  mv "$DOWNLOAD/extracted" "$SPARKLE_DIR"
fi

build_arch() {
  swift build -c release --arch "$1" -Xswiftc "-F$SPARKLE_DIR" \
    -Xlinker "-F$SPARKLE_DIR" -Xlinker -rpath -Xlinker '@executable_path/../Frameworks' >&2 || return
  swift build -c release --arch "$1" --show-bin-path
}

echo "→ 编译（release）…"
HOST_ARCH="$(uname -m)"
BIN_DIR="$(build_arch "$HOST_ARCH")"
if [ "${FROG_MAC_UNIVERSAL:-0}" = 1 ]; then
  OTHER_ARCH=x86_64
  if [ "$HOST_ARCH" = x86_64 ]; then OTHER_ARCH=arm64; fi
  OTHER_BIN_DIR="$(build_arch "$OTHER_ARCH")"
fi

echo "→ 组装 $APP"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources" "$APP/Contents/Frameworks"
if [ "${FROG_MAC_UNIVERSAL:-0}" = 1 ]; then
  lipo -create "$BIN_DIR/frog" "$OTHER_BIN_DIR/frog" -output "$APP/Contents/MacOS/frog"
else
  cp "$BIN_DIR/frog" "$APP/Contents/MacOS/frog"
fi
ditto "$SPARKLE_DIR/Sparkle.framework" "$APP/Contents/Frameworks/Sparkle.framework"
cp "$SPARKLE_DIR/LICENSE" "$APP/Contents/Resources/Sparkle-LICENSE.txt"

INFO="$APP/Contents/Info.plist"
cp "$ROOT/Support/Info.plist" "$INFO"
plutil -replace CFBundleShortVersionString -string "$VERSION" "$INFO"
plutil -replace CFBundleVersion -string "$VERSION" "$INFO"
if [ -n "${FROG_SPARKLE_PUBLIC_KEY:-}" ]; then
  if ! [[ "$FROG_SPARKLE_PUBLIC_KEY" =~ ^[A-Za-z0-9+/]{43}=$ ]]; then
    echo "FROG_SPARKLE_PUBLIC_KEY 必须是 Base64 格式的 Ed25519 公钥" >&2
    exit 1
  fi
  plutil -insert SUPublicEDKey -string "$FROG_SPARKLE_PUBLIC_KEY" "$INFO"
fi

ICON_SRC="$ROOT/../web/public/frog.png"
ICONSET="$ROOT/build/AppIcon.iconset"
rm -rf "$ICONSET"
mkdir -p "$ICONSET"
for size in 16 32 128 256 512; do
  sips -z "$size" "$size" "$ICON_SRC" --out "$ICONSET/icon_${size}x${size}.png" >/dev/null
  double=$((size * 2))
  sips -z "$double" "$double" "$ICON_SRC" --out "$ICONSET/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/AppIcon.icns"
rm -rf "$ICONSET"
# 网页版的 Bot 头像与 Agent 图标一起打包（离线也能显示）
for d in bot-avatars agent-icons; do
  if [ -d "$ROOT/../web/public/assets/$d" ]; then
    mkdir -p "$APP/Contents/Resources/$d"
    cp "$ROOT/../web/public/assets/$d/"* "$APP/Contents/Resources/$d/" 2>/dev/null || true
  fi
done

codesign --force --sign - "$APP"
codesign --verify --deep --strict "$APP"
echo "✓ 完成：$APP"
