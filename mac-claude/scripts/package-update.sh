#!/usr/bin/env bash
# 将已构建的 frog.app 打包并生成签名的 Sparkle 更新源。
set -euo pipefail

TAG="${1:?请指定发布标签，如 v0.1.6}"
OUT="${2:?请指定发布文件输出目录}"
KEY_FILE="${FROG_SPARKLE_PRIVATE_KEY:?请指定 Sparkle 私钥文件路径}"
if ! [[ "$TAG" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "发布标签必须是 vMAJOR.MINOR.PATCH" >&2
  exit 1
fi
VERSION="${TAG#v}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
APP="$ROOT/build/frog.app"
INFO="$APP/Contents/Info.plist"
APP_VERSION="$(plutil -extract CFBundleShortVersionString raw "$INFO")"
BUILD_VERSION="$(plutil -extract CFBundleVersion raw "$INFO")"
MINIMUM_OS="$(plutil -extract LSMinimumSystemVersion raw "$INFO")"
PUBLIC_KEY="$(plutil -extract SUPublicEDKey raw "$INFO")"
SPARKLE_VERSION="$(plutil -extract CFBundleShortVersionString raw "$APP/Contents/Frameworks/Sparkle.framework/Resources/Info.plist")"
SIGN_UPDATE="$ROOT/build/Sparkle-$SPARKLE_VERSION/bin/sign_update"

if [ "$APP_VERSION" != "$VERSION" ] || [ "$BUILD_VERSION" != "$VERSION" ]; then
  echo "frog.app 的版本与 $TAG 不符" >&2
  exit 1
fi
codesign --verify --deep --strict "$APP"

mkdir -p "$OUT"
STAGE="$(mktemp -d "$OUT/.macos.XXXXXXXX")"
trap 'rm -rf "$STAGE"' EXIT
ARCHIVE="$STAGE/frog-macos.zip"
APPCAST="$STAGE/frog-macos-appcast.xml"
RELEASES=https://github.com/llc370219/frog-bot/releases

ditto -c -k --sequesterRsrc --keepParent "$APP" "$ARCHIVE"
SIGNATURE="$("$SIGN_UPDATE" --ed-key-file "$KEY_FILE" -p "$ARCHIVE")"
LENGTH="$(stat -f %z "$ARCHIVE")"
# Check the archive against the public key embedded in the distributed app.
swift - "$ARCHIVE" "$PUBLIC_KEY" "$SIGNATURE" <<'SWIFT'
import CryptoKit
import Foundation

let arguments = CommandLine.arguments
let archive = try Data(contentsOf: URL(fileURLWithPath: arguments[1]), options: .mappedIfSafe)
guard let keyData = Data(base64Encoded: arguments[2]),
      let signature = Data(base64Encoded: arguments[3]),
      let key = try? Curve25519.Signing.PublicKey(rawRepresentation: keyData),
      key.isValidSignature(signature, for: archive) else {
    FileHandle.standardError.write(Data("Sparkle 公钥与更新包签名不匹配\n".utf8))
    exit(1)
}
SWIFT

cat > "$APPCAST" <<XML
<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:sparkle="http://www.andymatuschak.org/xml-namespaces/sparkle">
  <channel>
    <title>frog macOS</title>
    <item>
      <title>frog $VERSION</title>
      <link>$RELEASES/tag/$TAG</link>
      <sparkle:version>$BUILD_VERSION</sparkle:version>
      <sparkle:shortVersionString>$APP_VERSION</sparkle:shortVersionString>
      <sparkle:minimumSystemVersion>$MINIMUM_OS</sparkle:minimumSystemVersion>
      <enclosure url="$RELEASES/download/$TAG/frog-macos.zip" sparkle:edSignature="$SIGNATURE" length="$LENGTH" type="application/octet-stream" />
    </item>
  </channel>
</rss>
XML
"$SIGN_UPDATE" --ed-key-file "$KEY_FILE" "$APPCAST"
"$SIGN_UPDATE" --ed-key-file "$KEY_FILE" --verify "$APPCAST"
mv -f "$ARCHIVE" "$APPCAST" "$OUT/"
