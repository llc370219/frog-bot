#!/bin/bash
# 双击编译并打开 frog 开发版，随后保持构建助手运行。
cd "$(dirname "$0")" || exit 1
mkdir -p .dev
echo build > .dev/request
exec bash scripts/dev-watch.sh
