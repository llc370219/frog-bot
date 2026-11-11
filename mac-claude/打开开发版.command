#!/bin/bash
# 双击运行：编译 Frog Bot Claude 开发版并打开；之后保持构建助手运行（只认 build / open / quit 三个指令）。
cd "$(dirname "$0")" || exit 1
mkdir -p .dev
echo build > .dev/request
exec bash scripts/dev-watch.sh
