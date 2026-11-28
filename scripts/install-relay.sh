#!/usr/bin/env bash
# Frog Bot 公网 E2EE 零知识盲中继（relay-ws + Caddy 自动 HTTPS 443）一键安装 / 升级脚本。
#
# 用法（在 VMISS 等 Linux VPS 上执行，先将域名 A 记录解析到本机公网 IP，并放行 80/443 端口）：
#   curl -fsSL https://raw.githubusercontent.com/llc370219/frog-bot/main/scripts/install-relay.sh | sudo DOMAIN=relay.632219.xyz bash
#
# 可选环境变量：
#   DOMAIN           默认 relay.632219.xyz（Caddy 自动申请 HTTPS 证书）
#   RELAY_PORT       本机中继监听端口，默认 18443（仅绑定 127.0.0.1，由 Caddy 443 反代）
#   RELAY_TOKEN      可选访问令牌（公网开箱即用中继请留空；私有中继可填写）
#   MAX_CONNS_PER_IP 单 IP 最大并发连接数，默认 64
#   MAX_MB_PER_MIN   单连接每分钟最大转发流量（MB），默认 64
#   VERSION          指定发布版本（默认读取最新 release）
#   FROGBOT_BIN      使用本机已编译好的 frogbot 二进制文件路径（跳过 GitHub 下载）
#
# 卸载：
#   curl -fsSL https://raw.githubusercontent.com/llc370219/frog-bot/main/scripts/install-relay.sh | sudo bash -s uninstall
set -euo pipefail

REPO="llc370219/frog-bot"
DOMAIN="${DOMAIN:-relay.632219.xyz}"
RELAY_PORT="${RELAY_PORT:-18443}"
RELAY_TOKEN="${RELAY_TOKEN:-}"
MAX_CONNS_PER_IP="${MAX_CONNS_PER_IP:-64}"
MAX_MB_PER_MIN="${MAX_MB_PER_MIN:-128}"
APP_DIR="/opt/frogbot-relay"
ETC_DIR="/etc/frogbot"
ENV_FILE="$ETC_DIR/relay.env"
UNIT_FILE="/etc/systemd/system/frogbot-relay.service"
WORK_DIR=""

say()  { printf '\033[1;32m==>\033[0m %s\n' "$*" >&2; }
warn() { printf '\033[1;33m[注意]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[错误]\033[0m %s\n' "$*" >&2; exit 1; }

need_root() {
  [ "$(id -u)" -eq 0 ] || die "请用 root / sudo 运行此脚本"
}

have() { command -v "$1" >/dev/null 2>&1; }

fetch() {
  if have curl; then curl -fsSL --retry 3 --connect-timeout 15 -o "$2" "$1"
  elif have wget; then wget -q -O "$2" "$1"
  else die "需要 curl 或 wget"; fi
}

detect_arch() {
  [ "$(uname -s)" = Linux ] || die "仅支持 Linux 服务器"
  case "$(uname -m)" in
    x86_64|amd64) echo amd64 ;;
    aarch64|arm64) echo arm64 ;;
    *) die "不支持的 CPU 架构：$(uname -m)" ;;
  esac
}

latest_version() {
  local tmp; tmp="$(mktemp)"
  fetch "https://api.github.com/repos/$REPO/releases?per_page=20" "$tmp" || die "无法读取 GitHub 发布列表"
  local v
  v="$(grep -o '"tag_name": *"[^"]*"' "$tmp" | sed 's/.*"\([^"]*\)"$/\1/' | grep '^v[0-9]' | head -n1)"
  rm -f "$tmp"
  [ -n "$v" ] || die "GitHub 上未找到可用发布版本"
  echo "$v"
}

download_binary() {
  local ver="$1" arch="$2" dir="$3" name="frogbot_${1}_linux_${2}.tar.gz"
  local base="https://github.com/$REPO/releases/download/$ver"
  say "下载 $name"
  fetch "$base/$name" "$dir/$name" || die "下载失败：$base/$name"
  fetch "$base/SHA256SUMS" "$dir/SHA256SUMS" || die "下载校验文件失败"
  say "校验 SHA256"
  (cd "$dir" && grep " ${name}\$" SHA256SUMS > want.sum && [ -s want.sum ] && sha256sum -c --status want.sum) \
    || die "SHA256 校验失败，已停止安装"
  tar -C "$dir" -xzf "$dir/$name"
  [ -x "$dir/frogbot_${ver}_linux_${arch}/frogbot" ] || die "压缩包里缺少 frogbot 二进制"
  echo "$dir/frogbot_${ver}_linux_${arch}/frogbot"
}

install_caddy() {
  if have caddy; then
    return 0
  fi
  say "安装 Caddy（自动申请并续期 HTTPS 证书）"
  if have apt-get; then
    apt-get update -qq
    if ! apt-get install -y -qq caddy 2>/dev/null; then
      apt-get install -y -qq debian-keyring debian-archive-keyring apt-transport-https curl gnupg
      curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
      curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' > /etc/apt/sources.list.d/caddy-stable.list
      apt-get update -qq && apt-get install -y -qq caddy
    fi
  elif have dnf; then
    dnf install -y -q 'dnf-command(copr)' caddy 2>/dev/null || {
      dnf copr enable -y @caddy/caddy && dnf install -y -q caddy
    }
  else
    warn "未能自动安装 Caddy，请手动安装 Caddy 并配置 $DOMAIN -> 127.0.0.1:$RELAY_PORT"
    return 1
  fi
}

configure_caddy() {
  have caddy || return 0
  local cf="/etc/caddy/Caddyfile"
  mkdir -p /etc/caddy
  if [ -f "$cf" ] && grep -q "# frogbot-relay-begin" "$cf"; then
    sed -i "/# frogbot-relay-begin/,/# frogbot-relay-end/d" "$cf"
  elif [ -f "$cf" ] && grep -q '^:80' "$cf"; then
    : > "$cf"
  fi
  cat >> "$cf" <<CADDY

# frogbot-relay-begin
$DOMAIN {
    reverse_proxy 127.0.0.1:$RELAY_PORT {
        flush_interval -1
    }
}
# frogbot-relay-end
CADDY
  caddy fmt --overwrite "$cf" >/dev/null 2>&1 || true
  systemctl enable caddy >/dev/null 2>&1 || true
  systemctl reload caddy 2>/dev/null || systemctl restart caddy || warn "Caddy 启动失败：sudo journalctl -u caddy -n 50"
}

write_service() {
  mkdir -p "$ETC_DIR"
  cat > "$ENV_FILE" <<ENV
# Frog Bot E2EE 盲中继配置（改完执行 sudo systemctl restart frogbot-relay）
RELAY_LISTEN=127.0.0.1:$RELAY_PORT
RELAY_TOKEN=$RELAY_TOKEN
ENV
  chmod 600 "$ENV_FILE"

  # 优先使用内置限流参数；旧版二进制如不支持新 flag 自动回退兼容模式
  local extra_flags=""
  if "$APP_DIR/frogbot" relay-ws -h 2>&1 | grep -q -- "-max-conns-per-ip"; then
    extra_flags="-max-conns-per-ip $MAX_CONNS_PER_IP -max-mb-per-min $MAX_MB_PER_MIN"
  fi

  cat > "$UNIT_FILE" <<UNIT
[Unit]
Description=Frog Bot E2EE WSS Blind Relay ($DOMAIN)
Documentation=https://github.com/$REPO
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=$ENV_FILE
ExecStart=$APP_DIR/frogbot relay-ws $extra_flags
Restart=always
RestartSec=2
LimitNOFILE=65536
NoNewPrivileges=true
ProtectSystem=strict
PrivateTmp=true

[Install]
WantedBy=multi-user.target
UNIT
}

uninstall() {
  need_root
  say "停止并卸载 Frog Bot 盲中继服务"
  systemctl disable --now frogbot-relay 2>/dev/null || true
  rm -f "$UNIT_FILE" "$ENV_FILE"
  rm -rf "$APP_DIR"
  systemctl daemon-reload || true
  if [ -f /etc/caddy/Caddyfile ] && grep -q "# frogbot-relay-begin" /etc/caddy/Caddyfile; then
    sed -i "/# frogbot-relay-begin/,/# frogbot-relay-end/d" /etc/caddy/Caddyfile
    systemctl reload caddy 2>/dev/null || true
  fi
  say "已卸载完成"
}

main() {
  if [ "${1:-}" = uninstall ]; then uninstall; exit 0; fi
  need_root
  have systemctl && [ -d /run/systemd/system ] || die "需要 systemd 环境"

  local bin_src=""
  if [ -n "${FROGBOT_BIN:-}" ]; then
    [ -x "$FROGBOT_BIN" ] || die "找不到可执行文件：$FROGBOT_BIN"
    bin_src="$FROGBOT_BIN"
    say "使用本地二进制：$bin_src"
  else
    have sha256sum || die "需要 sha256sum"
    have tar || die "需要 tar"
    local arch ver
    arch="$(detect_arch)"
    ver="${VERSION:-$(latest_version)}"
    say "安装 Frog Bot 盲中继 $ver（linux/$arch），域名：$DOMAIN"
    WORK_DIR="$(mktemp -d)"; trap 'rm -rf "${WORK_DIR:-}"' EXIT
    bin_src="$(download_binary "$ver" "$arch" "$WORK_DIR" | tail -n1)"
  fi

  mkdir -p "$APP_DIR"
  systemctl stop frogbot-relay 2>/dev/null || true
  install -m 755 "$bin_src" "$APP_DIR/frogbot.new"
  mv -f "$APP_DIR/frogbot.new" "$APP_DIR/frogbot"

  write_service
  systemctl daemon-reload
  systemctl enable frogbot-relay >/dev/null 2>&1
  systemctl restart frogbot-relay

  # 等待本机端口就绪
  local ready=0
  for _ in $(seq 1 20); do
    if curl -fsS --max-time 2 "http://127.0.0.1:$RELAY_PORT/health" >/dev/null 2>&1; then
      ready=1
      break
    fi
    sleep 0.5
  done
  [ "$ready" -eq 1 ] || warn "本机中继健康检查未通过，请查看：sudo journalctl -u frogbot-relay -n 50"

  install_caddy || true
  configure_caddy || true

  local health_json
  health_json="$(curl -fsS --max-time 2 "http://127.0.0.1:$RELAY_PORT/health" 2>/dev/null || echo '未响应')"

  cat <<DONE

  🐸 Frog Bot E2EE 盲中继已启动！

  中继地址： wss://$DOMAIN/ws
  健康检查： https://$DOMAIN/health
  本机状态： $health_json

  请确认两件事（否则 HTTPS 证书无法自动签发）：
    1. 域名 DNS：为 $DOMAIN 添加 A 记录指向本机公网 IP（不要开 Cloudflare 橙色代理云朵，直连更稳）
    2. 防火墙放行 TCP 80 和 443 端口（ufw allow 80,443/tcp）

  常用运维命令：
    查看中继状态： sudo systemctl status frogbot-relay
    查看实时日志： sudo journalctl -u frogbot-relay -f
    查看证书日志： sudo journalctl -u caddy -n 50
    测试外网连通： curl -fsS https://$DOMAIN/health

DONE
}

main "$@"
