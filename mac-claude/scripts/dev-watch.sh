#!/bin/bash
# 构建助手：只认三个固定指令（build / open / quit），不执行任意命令。
# 将 .dev/request 中的指令执行结果写到 .dev/log.txt 和 .dev/status。
cd "$(dirname "$0")/.." || exit 1
mkdir -p .dev
APP="build/frog.app"
echo "frog 构建助手已启动，保持这个窗口开着即可；按 Ctrl+C 退出。"
echo "工具链：$(swift --version 2>&1 | head -1)"
swift --version > .dev/toolchain.txt 2>&1
[ -f .dev/request ] || echo ready > .dev/status
while true; do
  if [ -f .dev/request ]; then
    cmd=$(head -c 16 .dev/request | tr -cd 'a-z'); rm -f .dev/request
    echo "[$(date +%H:%M:%S)] $cmd"
    echo running > .dev/status
    case "$cmd" in
      build) bash scripts/build-app.sh > .dev/log.txt 2>&1; code=$?; echo "exit $code" > .dev/status
             tail -5 .dev/log.txt
             if [ $code -eq 0 ]; then pkill -x "frog" 2>/dev/null; sleep 0.5; open "$APP"; fi ;;
      open)  pkill -x "frog" 2>/dev/null; sleep 0.5; open "$APP" > .dev/log.txt 2>&1; echo "exit $?" > .dev/status ;;
      quit)  pkill -x "frog" > .dev/log.txt 2>&1; echo "exit 0" > .dev/status ;;
      *)     echo "unknown" > .dev/status ;;
    esac
    echo "[$(date +%H:%M:%S)] $(cat .dev/status)"
  fi
  sleep 1
done
