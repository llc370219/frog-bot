#!/usr/bin/env bash
# frog bot 服务器版一键安装 / 升级（Linux x86_64 / arm64，systemd）。
#
#   curl -fsSL https://raw.githubusercontent.com/llc370219/frog-bot/main/scripts/install.sh | sudo bash
#
# 可选环境变量（写在 sudo 后面，例如 curl … | sudo PORT=9000 bash）：
#   PORT=8080              监听端口
#   FROGBOT_USER=admin     登录用户名（只在第一次安装时使用）
#   FROGBOT_PASSWORD=…     登录密码（只在第一次安装时使用；不设就随机生成）
#   VERSION=v0.1.2-beta    安装指定版本（默认最新发布，包括测试版）
#   DOMAIN=bot.example.com 用 Caddy 自动申请 HTTPS 证书，frog bot 只监听本机
#
# 再次运行 = 升级：保留数据、账号和密码，只替换程序。
# 卸载：sudo bash install.sh uninstall    （保留数据；加 --purge 连数据一起删）
#
# 测试用（不会出现在正常安装里）：
#   FROGBOT_ARTIFACT_DIR=目录   不联网下载，直接用本地的 frogbot_*_linux_*.tar.gz 和 SHA256SUMS
#   FROGBOT_ROOT=目录           把 /opt、/var/lib、/etc 都装到这个目录下（不创建系统用户、不碰 systemd）
set -euo pipefail

REPO="llc370219/frog-bot"
PORT_GIVEN="${PORT:+1}"
PORT="${PORT:-8080}"
ADMIN_USER="${FROGBOT_USER:-admin}"
ROOT="${FROGBOT_ROOT:-}"
WORK_DIR=""
APP_DIR="$ROOT/opt/frogbot"
DATA_ROOT="$ROOT/var/lib/frogbot"
ETC_DIR="$ROOT/etc/frogbot"
ENV_FILE="$ETC_DIR/frogbot.env"
UNIT_FILE="$ROOT/etc/systemd/system/frogbot.service"
WRAPPER="$ROOT/usr/local/bin/frogbot"
SVC_USER="frogbot"

say()  { printf '\033[1;32m==>\033[0m %s\n' "$*" >&2; }
warn() { printf '\033[1;33m[注意]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[错误]\033[0m %s\n' "$*" >&2; exit 1; }

sandbox() { [ -n "$ROOT" ]; }   # 测试模式：装到 FROGBOT_ROOT 下

need_root() {
  if ! sandbox && [ "$(id -u)" -ne 0 ]; then
    die "请用 root 运行：curl -fsSL https://raw.githubusercontent.com/$REPO/main/scripts/install.sh | sudo bash"
  fi
}

have() { command -v "$1" >/dev/null 2>&1; }

fetch() { # fetch URL 输出文件
  if have curl; then curl -fsSL --retry 3 --connect-timeout 15 -o "$2" "$1"
  elif have wget; then wget -q -O "$2" "$1"
  else die "需要 curl 或 wget"; fi
}

detect_arch() {
  [ "$(uname -s)" = Linux ] || die "这个脚本只支持 Linux 服务器（macOS / Windows 请下载对应的发布包，本机运行）"
  case "$(uname -m)" in
    x86_64|amd64) echo amd64 ;;
    aarch64|arm64) echo arm64 ;;
    *) die "不支持的 CPU 架构：$(uname -m)（只有 x86_64 和 arm64）" ;;
  esac
}

latest_version() {
  local tmp; tmp="$(mktemp)"
  # /releases 列表包含测试版（/releases/latest 只返回正式版）；第一个就是最新的
  fetch "https://api.github.com/repos/$REPO/releases?per_page=5" "$tmp" || die "无法从 GitHub 读取发布列表"
  local v
  v="$(grep -o '"tag_name": *"[^"]*"' "$tmp" | head -n1 | sed 's/.*"\([^"]*\)"$/\1/')"
  rm -f "$tmp"
  [ -n "$v" ] || die "GitHub 上还没有可用的发布"
  echo "$v"
}

public_ip() {
  local ip=""
  if have curl; then
    for u in https://api.ipify.org https://ifconfig.me/ip https://ipv4.icanhazip.com; do
      ip="$(curl -fsS -4 --max-time 5 "$u" 2>/dev/null | tr -d '[:space:]')" && [ -n "$ip" ] && break
    done
  fi
  if [ -z "$ip" ] && have hostname; then ip="$(hostname -I 2>/dev/null | awk '{print $1}')"; fi
  echo "${ip:-<服务器IP>}"
}

download() { # download 版本 架构 目录
  local ver="$1" arch="$2" dir="$3" name="frogbot_${1}_linux_${2}.tar.gz"
  if [ -n "${FROGBOT_ARTIFACT_DIR:-}" ]; then
    say "使用本地发布包：$FROGBOT_ARTIFACT_DIR/$name"
    cp "$FROGBOT_ARTIFACT_DIR/$name" "$FROGBOT_ARTIFACT_DIR/SHA256SUMS" "$dir/" || die "本地目录里没有 $name 或 SHA256SUMS"
  else
    local base="https://github.com/$REPO/releases/download/$ver"
    say "下载 $name"
    fetch "$base/$name" "$dir/$name" || die "下载失败：$base/$name"
    fetch "$base/SHA256SUMS" "$dir/SHA256SUMS" || die "下载校验文件失败：$base/SHA256SUMS"
  fi
  say "校验 SHA256"
  (cd "$dir" && grep " ${name}\$" SHA256SUMS > want.sum && [ -s want.sum ] && sha256sum -c --status want.sum) \
    || die "SHA256 校验失败，文件可能不完整或被篡改，已停止安装"
  tar -C "$dir" -xzf "$dir/$name"
  [ -x "$dir/frogbot_${ver}_linux_${arch}/frogbot" ] || die "发布包里没有 frogbot 程序"
  echo "$dir/frogbot_${ver}_linux_${arch}"
}

ensure_user() {
  sandbox && return 0
  if ! id "$SVC_USER" >/dev/null 2>&1; then
    say "创建系统用户 $SVC_USER（不能登录，没有 root 权限）"
    useradd --system --home-dir "$DATA_ROOT" --shell /usr/sbin/nologin "$SVC_USER" 2>/dev/null \
      || useradd -r -d "$DATA_ROOT" -s /sbin/nologin "$SVC_USER"
  fi
}

own() { sandbox || chown -R "$SVC_USER:$SVC_USER" "$@"; }

as_svc() { # 以服务用户身份运行
  if sandbox; then "$@"
  elif have runuser; then runuser -u "$SVC_USER" -- "$@"
  else su -s /bin/sh "$SVC_USER" -c "$(printf '%q ' "$@")"; fi
}

write_env() {
  local listen="0.0.0.0:$PORT"
  [ -n "${DOMAIN:-}" ] && listen="127.0.0.1:$PORT"
  mkdir -p "$ETC_DIR"
  if [ -f "$ENV_FILE" ]; then
    # 升级：保留用户改过的配置，只更新监听地址（如果这次显式给了 PORT / DOMAIN）
    if [ -n "$PORT_GIVEN" ] || [ -n "${DOMAIN:-}" ]; then
      sed -i "s|^ADDR=.*|ADDR=$listen|" "$ENV_FILE"
    fi
    return 0
  fi
  cat > "$ENV_FILE" <<ENV
# frog bot 服务器版配置（改完执行 sudo systemctl restart frogbot）
FROGBOT_MODE=server
ADDR=$listen
DATA_DIR=$DATA_ROOT/data
WORKSPACE=$DATA_ROOT/workspace
FROGBOT_NO_BROWSER=1
# 所有 Bot 合计同时运行的后台任务数
MAX_CONCURRENT=4
# 也可以在这里写模型配置（优先于网页设置），例如：
# BASE_URL=https://api.x.ai/v1
# API_KEY=...
# CHAT_MODEL=...
ENV
  chmod 640 "$ENV_FILE"
  sandbox || chown "root:$SVC_USER" "$ENV_FILE"
}

write_unit() {
  sandbox && return 0
  mkdir -p "$(dirname "$UNIT_FILE")"
  cat > "$UNIT_FILE" <<UNIT
[Unit]
Description=frog bot (server mode)
Documentation=https://github.com/$REPO
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SVC_USER
Group=$SVC_USER
EnvironmentFile=$ENV_FILE
WorkingDirectory=$DATA_ROOT
ExecStart=$APP_DIR/frogbot -server
Restart=on-failure
RestartSec=3
# 基本加固：后台 agent 的 shell 只能写 $DATA_ROOT，不能提权（这不是完整沙箱）
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=$DATA_ROOT
ProtectHome=true
PrivateTmp=true
ProtectKernelTunables=true
ProtectControlGroups=true

[Install]
WantedBy=multi-user.target
UNIT
}

write_wrapper() {
  mkdir -p "$(dirname "$WRAPPER")"
  cat > "$WRAPPER" <<'WRAP'
#!/bin/sh
# 由 frog bot 安装脚本生成：管理命令（服务本身由 systemd 运行）。
ENV_FILE="__ENV_FILE__"
APP="__APP_DIR__/frogbot"
case "${1:-}" in
  reset-password)
    shift
    DATA_DIR="$(sed -n 's/^DATA_DIR=//p' "$ENV_FILE" 2>/dev/null)"
    if [ "$(id -u)" = 0 ] && id frogbot >/dev/null 2>&1; then
      exec runuser -u frogbot -- "$APP" reset-password -data "$DATA_DIR" "$@"
    fi
    exec "$APP" reset-password -data "$DATA_DIR" "$@" ;;
  version|-version|--version) exec "$APP" -version ;;
  *)
    echo "frog bot 由 systemd 运行：sudo systemctl status|restart|stop frogbot"
    echo "查看日志：sudo journalctl -u frogbot -f"
    echo "重置密码：sudo frogbot reset-password"
    echo "版本：    frogbot version" ;;
esac
WRAP
  sed -i "s|__ENV_FILE__|$ENV_FILE|; s|__APP_DIR__|$APP_DIR|" "$WRAPPER"
  chmod 755 "$WRAPPER"
}

setup_caddy() {
  [ -n "${DOMAIN:-}" ] || return 0
  sandbox && { say "（测试模式）跳过 Caddy"; return 0; }
  if ! have caddy; then
    if have apt-get; then
      say "安装 Caddy（自动申请 HTTPS 证书）"
      if ! { apt-get update -qq && apt-get install -y -qq caddy; }; then warn "apt 安装 Caddy 失败"; fi
    elif have dnf; then dnf install -y -q caddy || warn "dnf 安装 Caddy 失败"
    fi
  fi
  if ! have caddy; then
    warn "没装上 Caddy。请手动安装（https://caddyserver.com/docs/install），然后在 /etc/caddy/Caddyfile 写入："
    printf '\n  %s {\n      reverse_proxy 127.0.0.1:%s\n  }\n\n' "$DOMAIN" "$PORT" >&2
    return 0
  fi
  local cf=/etc/caddy/Caddyfile
  if [ -f "$cf" ] && grep -q "frogbot" "$cf"; then
    sed -i "/# frogbot-begin/,/# frogbot-end/d" "$cf"
  elif [ -f "$cf" ] && grep -qvE '^\s*(#|$)' "$cf" && ! grep -q '^:80' "$cf"; then
    warn "$cf 里已经有别的站点配置，没有改动它。请手动加入：$DOMAIN { reverse_proxy 127.0.0.1:$PORT }"
    return 0
  else
    : > "$cf"   # Caddy 自带的默认欢迎页配置，直接替换
  fi
  cat >> "$cf" <<CADDY
# frogbot-begin
$DOMAIN {
    reverse_proxy 127.0.0.1:$PORT
}
# frogbot-end
CADDY
  systemctl enable caddy >/dev/null 2>&1 || true
  systemctl reload caddy 2>/dev/null || systemctl restart caddy || warn "Caddy 启动失败：sudo journalctl -u caddy"
}

wait_health() {
  sandbox && return 0
  local _
  for _ in $(seq 1 30); do
    if curl -fsS --max-time 2 "http://127.0.0.1:$PORT/api/health" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  warn "服务 30 秒内没有就绪，请查看日志：sudo journalctl -u frogbot -n 50"
}

uninstall() {
  need_root
  local purge="${1:-}"
  say "停止并移除 frog bot 服务"
  if ! sandbox && have systemctl; then
    systemctl disable --now frogbot 2>/dev/null || true
    rm -f "$UNIT_FILE"; systemctl daemon-reload || true
  fi
  rm -rf "$APP_DIR" "$WRAPPER"
  if [ "$purge" = "--purge" ]; then
    warn "删除所有数据：$DATA_ROOT 和 $ETC_DIR"
    rm -rf "$DATA_ROOT" "$ETC_DIR"
    sandbox || userdel "$SVC_USER" 2>/dev/null || true
  else
    say "数据保留在 $DATA_ROOT，配置在 $ETC_DIR（彻底删除：curl -fsSL https://raw.githubusercontent.com/$REPO/main/scripts/install.sh | sudo bash -s uninstall --purge）"
  fi
  [ -n "${DOMAIN:-}" ] && warn "Caddy 配置没有自动删除：/etc/caddy/Caddyfile"
  say "已卸载"
}

main() {
  if [ "${1:-}" = uninstall ]; then shift; uninstall "${1:-}"; exit 0; fi
  need_root
  case "$PORT" in ''|*[!0-9]*) die "PORT 必须是数字";; esac
  have sha256sum || die "需要 sha256sum（coreutils）"
  have tar || die "需要 tar"
  if ! sandbox; then have systemctl && [ -d /run/systemd/system ] || die "需要 systemd（这台机器没有运行 systemd）"; fi

  local arch ver src upgrade=""
  arch="$(detect_arch)"
  ver="${VERSION:-$(latest_version)}"
  [ -x "$APP_DIR/frogbot" ] && upgrade=1
  say "$( [ -n "$upgrade" ] && echo 升级 || echo 安装 ) frog bot $ver（linux/$arch）"

  WORK_DIR="$(mktemp -d)"; trap 'rm -rf "${WORK_DIR:-}"' EXIT
  src="$(download "$ver" "$arch" "$WORK_DIR" | tail -n1)"

  ensure_user
  mkdir -p "$APP_DIR" "$DATA_ROOT/data" "$DATA_ROOT/workspace"
  chmod 750 "$DATA_ROOT"
  own "$DATA_ROOT"
  if [ -n "$upgrade" ] && ! sandbox; then systemctl stop frogbot 2>/dev/null || true; fi
  install -m 755 "$src/frogbot" "$APP_DIR/frogbot.new" && mv -f "$APP_DIR/frogbot.new" "$APP_DIR/frogbot"
  cp -f "$src/LICENSE" "$src/ATTRIBUTION.md" "$APP_DIR/" 2>/dev/null || true
  write_env
  # 升级时端口以配置文件为准
  local addr_line
  addr_line="$(sed -n 's/^ADDR=//p' "$ENV_FILE" | head -n1)"
  [ -n "$addr_line" ] && PORT="${addr_line##*:}"
  write_wrapper
  write_unit

  local password="" user_line=""
  if [ -f "$DATA_ROOT/data/auth.json" ]; then
    user_line="（沿用原来的账号和密码）"
  else
    password="${FROGBOT_PASSWORD:-}"
    if [ -n "$password" ]; then
      printf '%s\n' "$password" | as_svc "$APP_DIR/frogbot" reset-password -data "$DATA_ROOT/data" -user "$ADMIN_USER" -password-stdin -quiet \
        || die "设置密码失败（密码至少 8 位）"
    else
      password="$(as_svc "$APP_DIR/frogbot" reset-password -data "$DATA_ROOT/data" -user "$ADMIN_USER" -quiet)" || die "生成密码失败"
    fi
  fi

  if ! sandbox; then
    systemctl daemon-reload
    systemctl enable frogbot >/dev/null 2>&1
    systemctl restart frogbot
    wait_health
  fi
  setup_caddy

  local url
  if [ -n "${DOMAIN:-}" ]; then url="https://$DOMAIN"; else url="http://$(public_ip):$PORT"; fi
  local shown_user
  shown_user="$(sed -n 's/.*"username": *"\([^"]*\)".*/\1/p' "$DATA_ROOT/data/auth.json" 2>/dev/null | head -n1)"
  cat <<DONE

  🐸 frog bot $ver 已$( [ -n "$upgrade" ] && echo 升级 || echo 安装 )，服务名 frogbot（开机自启，出错自动重启）

  访问地址：$url
  用户名：  ${shown_user:-$ADMIN_USER}
DONE
  if [ -n "$password" ]; then
    echo "  初始密码：$password"
    echo "            ↑ 只显示这一次，请马上记下。登录后可在「设置 → 账号与登录」修改。"
  else
    echo "  密码：    $user_line"
  fi
  cat <<DONE

  忘记密码：sudo frogbot reset-password
  查看状态：sudo systemctl status frogbot     日志：sudo journalctl -u frogbot -f
  数据目录：$DATA_ROOT     配置：$ENV_FILE
  升级：    再运行一次这条安装命令（保留数据和密码）
  卸载：    curl -fsSL https://raw.githubusercontent.com/$REPO/main/scripts/install.sh | sudo bash -s uninstall

DONE
  if [ -z "${DOMAIN:-}" ]; then
    warn "现在是 HTTP 明文：密码和聊天内容在网络上不加密。建议用域名 + HTTPS，重新运行："
    echo "        curl -fsSL https://raw.githubusercontent.com/$REPO/main/scripts/install.sh | sudo DOMAIN=你的域名 bash" >&2
    warn "脚本没有改防火墙。云服务器请在安全组 / 防火墙里放行 TCP $PORT，例如：sudo ufw allow $PORT/tcp"
  else
    warn "请确认域名 $DOMAIN 已解析到这台服务器，并在防火墙放行 TCP 80 和 443。"
  fi
  warn "后台 agent 能在这台服务器上执行命令（没有完整沙箱），请用专门的 VPS，不要和重要服务放在一起。"
}

main "$@"
