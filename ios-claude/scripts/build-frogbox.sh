#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
OUT_DIR="${ROOT_DIR}/ios-claude/Frameworks/FrogBox.xcframework"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/frogbox-xcf.XXXXXX")"
trap 'rm -rf "$WORK_DIR"' EXIT

IOS_MIN="18.0"
IPHONEOS_SDK="$(xcrun --sdk iphoneos --show-sdk-path)"
IPHONESIM_SDK="$(xcrun --sdk iphonesimulator --show-sdk-path)"
CLANG_BIN="$(xcrun --find clang)"

mkdir -p "$WORK_DIR/headers" "$WORK_DIR/ios-arm64" "$WORK_DIR/sim-arm64" "$WORK_DIR/sim-amd64" "$WORK_DIR/sim-universal"

cat > "$WORK_DIR/headers/frogbox.h" <<'EOF'
#ifndef FROGBOX_H
#define FROGBOX_H

#ifdef __cplusplus
extern "C" {
#endif

char* FrogBoxStart(const char* uri, const char* deviceName);
void FrogBoxStop(int handle);
void FrogBoxFree(char* ptr);

#ifdef __cplusplus
}
#endif

#endif
EOF

cat > "$WORK_DIR/headers/module.modulemap" <<'EOF'
module FrogBox {
    header "frogbox.h"
    export *
}
EOF

cd "$ROOT_DIR"

echo "==> Building FrogBox (iphoneos arm64)..."
CGO_ENABLED=1 GOOS=ios GOARCH=arm64 \
  CC="$CLANG_BIN" \
  CGO_CFLAGS="-isysroot ${IPHONEOS_SDK} -miphoneos-version-min=${IOS_MIN} -fembed-bitcode=off" \
  CGO_LDFLAGS="-isysroot ${IPHONEOS_SDK} -miphoneos-version-min=${IOS_MIN}" \
  go build -trimpath -ldflags="-s -w" -buildmode=c-archive \
  -o "$WORK_DIR/ios-arm64/libfrogbox.a" ./cmd/frogbox

echo "==> Building FrogBox (iphonesimulator arm64)..."
CGO_ENABLED=1 GOOS=ios GOARCH=arm64 \
  CC="$CLANG_BIN" \
  CGO_CFLAGS="-isysroot ${IPHONESIM_SDK} -target arm64-apple-ios${IOS_MIN}-simulator" \
  CGO_LDFLAGS="-isysroot ${IPHONESIM_SDK} -target arm64-apple-ios${IOS_MIN}-simulator" \
  go build -trimpath -ldflags="-s -w" -buildmode=c-archive \
  -o "$WORK_DIR/sim-arm64/libfrogbox.a" ./cmd/frogbox

echo "==> Building FrogBox (iphonesimulator x86_64)..."
CGO_ENABLED=1 GOOS=ios GOARCH=amd64 \
  CC="$CLANG_BIN" \
  CGO_CFLAGS="-isysroot ${IPHONESIM_SDK} -target x86_64-apple-ios${IOS_MIN}-simulator" \
  CGO_LDFLAGS="-isysroot ${IPHONESIM_SDK} -target x86_64-apple-ios${IOS_MIN}-simulator" \
  go build -trimpath -ldflags="-s -w" -buildmode=c-archive \
  -o "$WORK_DIR/sim-amd64/libfrogbox.a" ./cmd/frogbox

lipo -create \
  "$WORK_DIR/sim-arm64/libfrogbox.a" \
  "$WORK_DIR/sim-amd64/libfrogbox.a" \
  -output "$WORK_DIR/sim-universal/libfrogbox.a"

rm -rf "$OUT_DIR"
mkdir -p "$(dirname "$OUT_DIR")"

xcodebuild -create-xcframework \
  -library "$WORK_DIR/ios-arm64/libfrogbox.a" \
  -headers "$WORK_DIR/headers" \
  -library "$WORK_DIR/sim-universal/libfrogbox.a" \
  -headers "$WORK_DIR/headers" \
  -output "$OUT_DIR"

echo "==> FrogBox.xcframework ready at $OUT_DIR"
