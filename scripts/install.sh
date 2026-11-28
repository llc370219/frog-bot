#!/usr/bin/env bash
# frog 服务器版一键安装 / 升级（Linux x86_64 / arm64，systemd）。
#
#   curl -fsSL https://raw.githubusercontent.com/llc370219/frog-bot/main/scripts/install.sh | sudo bash
#
# 默认直接运行在当前宿主用户环境（自动继承宿主用户的 HOME、PATH 以及本机已安装登录的 claude / codex / agy / pi 等 CLI），
# 同时由内置 Linux Landlock 沙箱保护 frog 自身的 data 密钥目录。
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
ROOTD_UNIT_FILE="$ROOT/etc/systemd/system/frogbot-rootd.service"
ROOTD_STATE="$ROOT/etc/frogbot/rootd.json"
ROOT_HELPER="${ROOT_HELPER:-1}"
WRAPPER="$ROOT/usr/local/bin/frogbot"
FROG_BIN="$ROOT/usr/local/bin/frog"
SVC_USER=""
SVC_GROUP=""
SVC_HOME=""
SVC_PATH=""

say()  { printf '\033[1;32m==>\033[0m %s\n' "$*" >&2; }
warn() { printf '\033[1;33m[注意]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[错误]\033[0m %s\n' "$*" >&2; exit 1; }

sandbox() { [ -n "$ROOT" ]; }

need_root() {
  if ! sandbox && [ "$(id -u)" -ne 0 ]; then
    die "请用 root 运行：curl -fsSL https://raw.githubusercontent.com/$REPO/main/scripts/install.sh | sudo bash"
  fi
}

have() { command -v "$1" >/dev/null 2>&1; }

fetch() {
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
  local channel="${CHANNEL:-beta}"
  if [ "$channel" = "stable" ]; then
    fetch "https://api.github.com/repos/$REPO/releases/latest" "$tmp" || die "无法从 GitHub 读取正式版信息"
  else
    fetch "https://api.github.com/repos/$REPO/releases?per_page=20" "$tmp" || die "无法从 GitHub 读取发布列表"
  fi
  local v
  v="$(grep -o '"tag_name": *"[^"]*"' "$tmp" | sed 's/.*"\([^"]*\)"$/\1/' | grep '^v[0-9]' | head -n1)"
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

download() {
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

has_host_cli_traces() {
  local h="$1"
  [ -d "$h" ] || return 1
  local p
  for p in \
    "$h/.local/bin/agy" "$h/.local/bin/claude" "$h/.local/bin/codex" "$h/.local/bin/pi" "$h/.local/bin/gemini" "$h/.local/bin/opencode" \
    "$h/.antigravity" "$h/.claude" "$h/.codex" "$h/.gemini" "$h/.nvm" "$h/.bun" "$h/.npm-global"; do
    [ -e "$p" ] && return 0
  done
  return 1
}

resolve_host_user() {
  if sandbox; then
    SVC_USER="$(id -un)"
    SVC_GROUP="$(id -gn)"
    SVC_HOME="${HOME:-/tmp}"
    SVC_PATH="${PATH:-/usr/local/bin:/usr/bin:/bin}"
    return 0
  fi

  local chosen="${FROGBOT_RUN_USER:-}"
  if [ -z "$chosen" ] && [ -f "$ENV_FILE" ]; then
    chosen="$(sed -n 's/^FROGBOT_RUN_USER=//p' "$ENV_FILE" 2>/dev/null | head -n1 | tr -d '[:space:]')"
  fi
  if [ "$chosen" = "frogbot" ]; then chosen=""; fi

  if [ -z "$chosen" ] && [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ] && [ "$SUDO_USER" != "nobody" ] && [ "$SUDO_USER" != "frogbot" ]; then
    chosen="$SUDO_USER"
  fi

  if [ -z "$chosen" ]; then
    if has_host_cli_traces "/root"; then
      chosen="root"
    else
      local d u uid
      for d in /home/*; do
        [ -d "$d" ] || continue
        u="$(basename "$d")"
        [ "$u" = "frogbot" ] && continue
        uid="$(id -u "$u" 2>/dev/null || echo 0)"
        if [ "$uid" -ge 1000 ] && has_host_cli_traces "$d"; then
          chosen="$u"
          break
        fi
      done
    fi
  fi

  [ -n "$chosen" ] || chosen="root"
  id "$chosen" >/dev/null 2>&1 || chosen="root"

  SVC_USER="$chosen"
  SVC_GROUP="$(id -gn "$SVC_USER")"
  SVC_HOME="$(getent passwd "$SVC_USER" 2>/dev/null | cut -d: -f6 || true)"
  if [ -z "$SVC_HOME" ]; then
    if [ "$SVC_USER" = "root" ]; then SVC_HOME="/root"; else SVC_HOME="/home/$SVC_USER"; fi
  fi

  local login_p=""
  if [ "$SVC_USER" = "root" ]; then
    login_p="$(sh -lc 'printf %s "$PATH"' 2>/dev/null || true)"
  elif have runuser; then
    login_p="$(runuser -l "$SVC_USER" -c 'printf %s "$PATH"' 2>/dev/null || true)"
  else
    login_p="$(su - "$SVC_USER" -c 'printf %s "$PATH"' 2>/dev/null || true)"
  fi
  SVC_PATH="$SVC_HOME/.local/bin:$SVC_HOME/bin:$SVC_HOME/.antigravity/bin:$SVC_HOME/.npm-global/bin:$SVC_HOME/.bun/bin:$SVC_HOME/.cargo/bin:$SVC_HOME/.volta/bin:$SVC_HOME/.local/share/pnpm:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/snap/bin"
  if [ -n "$login_p" ]; then
    SVC_PATH="$SVC_PATH:$login_p"
  fi
  say "使用宿主用户环境：$SVC_USER（HOME=$SVC_HOME）"
}

own() { sandbox || chown -R "$SVC_USER:$SVC_GROUP" "$@"; }

as_svc() {
  if sandbox || [ "$SVC_USER" = "root" ]; then "$@"
  elif have runuser; then runuser -u "$SVC_USER" -- "$@"
  else su -s /bin/sh "$SVC_USER" -c "$(printf '%q ' "$@")"; fi
}

write_env() {
  local listen="0.0.0.0:$PORT"
  [ -n "${DOMAIN:-}" ] && listen="127.0.0.1:$PORT"
  mkdir -p "$ETC_DIR"
  if [ -f "$ENV_FILE" ]; then
    if [ -n "$PORT_GIVEN" ] || [ -n "${DOMAIN:-}" ]; then
      sed -i "s|^ADDR=.*|ADDR=$listen|" "$ENV_FILE"
    fi
    if grep -q '^FROGBOT_RUN_USER=' "$ENV_FILE"; then
      sed -i "s|^FROGBOT_RUN_USER=.*|FROGBOT_RUN_USER=$SVC_USER|" "$ENV_FILE"
    else
      printf '\nFROGBOT_RUN_USER=%s\n' "$SVC_USER" >> "$ENV_FILE"
    fi
    chmod 640 "$ENV_FILE"
    sandbox || chown "root:$SVC_GROUP" "$ENV_FILE"
    return 0
  fi
  cat > "$ENV_FILE" <<ENV
# frog 服务器版配置（改完执行 sudo systemctl restart frogbot）
FROGBOT_MODE=server
FROGBOT_RUN_USER=$SVC_USER
ADDR=$listen
DATA_DIR=$DATA_ROOT/data
WORKSPACE=$DATA_ROOT/workspace
FROGBOT_NO_BROWSER=1
MAX_CONCURRENT=4
ENV
  chmod 640 "$ENV_FILE"
  sandbox || chown "root:$SVC_GROUP" "$ENV_FILE"
}

write_unit() {
  sandbox && return 0
  mkdir -p "$(dirname "$UNIT_FILE")"
  cat > "$UNIT_FILE" <<UNIT
[Unit]
Description=frog (server mode)
Documentation=https://github.com/$REPO
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SVC_USER
Group=$SVC_GROUP
Environment="HOME=$SVC_HOME"
Environment="PATH=$SVC_PATH"
EnvironmentFile=$ENV_FILE
WorkingDirectory=$DATA_ROOT
ExecStart=$APP_DIR/frogbot -server
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
UNIT
}

write_rootd_unit() {
  sandbox && return 0
  cat > "$ROOTD_UNIT_FILE" <<UNIT
[Unit]
Description=frog 提权助手（用户输入登录密码授权后以 root 执行命令）
Documentation=https://github.com/$REPO
After=network.target

[Service]
Type=simple
User=root
ExecStart=$APP_DIR/frogbot rootd -unit frogbot.service -user $SVC_USER -state $ROOTD_STATE
RuntimeDirectory=frogbot-rootd
RuntimeDirectoryMode=0755
UMask=0077
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
UNIT
}

write_wrapper() {
  mkdir -p "$(dirname "$WRAPPER")"
  rm -f "$WRAPPER" "$FROG_BIN"
  ln -s "$APP_DIR/frogbot" "$WRAPPER"
  ln -s "$APP_DIR/frogbot" "$FROG_BIN"
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
    : > "$cf"
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
  say "停止并移除 frog 服务"
  if ! sandbox && have systemctl; then
    systemctl disable --now frogbot frogbot-rootd 2>/dev/null || true
    rm -f "$UNIT_FILE" "$ROOTD_UNIT_FILE"; systemctl daemon-reload || true
  fi
  rm -rf "$APP_DIR" "$WRAPPER" "$FROG_BIN"
  if [ "$purge" = "--purge" ]; then
    warn "删除所有数据：$DATA_ROOT 和 $ETC_DIR"
    rm -rf "$DATA_ROOT" "$ETC_DIR"
    sandbox || userdel frogbot 2>/dev/null || true
  else
    say "数据保留在 $DATA_ROOT，配置在 $ETC_DIR（彻底删除：curl -fsSL https://raw.githubusercontent.com/$REPO/main/scripts/install.sh | sudo bash -s uninstall --purge）"
  fi
  [ -n "${DOMAIN:-}" ] && warn "Caddy 配置没有自动删除：/etc/caddy/Caddyfile"
  say "已卸载"
}

install_macos() {
  local arch ver tmpdir name base bin_dir
  case "$(uname -m)" in
    arm64|aarch64) arch="arm64" ;;
    x86_64|amd64) arch="amd64" ;;
    *) die "不支持的 macOS 架构：$(uname -m)" ;;
  esac
  ver="${VERSION:-$(latest_version)}"
  name="frogbot_${ver}_darwin_${arch}.tar.gz"
  say "安装 frog CLI $ver（darwin/$arch）"
  tmpdir="$(mktemp -d)"; trap 'rm -rf "${tmpdir:-}"' EXIT
  if [ -n "${FROGBOT_ARTIFACT_DIR:-}" ]; then
    cp "$FROGBOT_ARTIFACT_DIR/$name" "$FROGBOT_ARTIFACT_DIR/SHA256SUMS" "$tmpdir/" || die "本地目录缺少发布包"
  else
    base="https://github.com/$REPO/releases/download/$ver"
    fetch "$base/$name" "$tmpdir/$name" || die "下载失败：$base/$name"
    fetch "$base/SHA256SUMS" "$tmpdir/SHA256SUMS" || die "下载校验文件失败"
  fi
  say "校验 SHA256"
  (cd "$tmpdir" && grep " ${name}\$" SHA256SUMS > want.sum && [ -s want.sum ] && shasum -a 256 -c want.sum >/dev/null) \
    || die "SHA256 校验失败，已停止安装"
  tar -C "$tmpdir" -xzf "$tmpdir/$name"
  bin_dir="${INSTALL_DIR:-$HOME/.local/bin}"
  mkdir -p "$bin_dir"
  install -m 755 "$tmpdir/frogbot_${ver}_darwin_${arch}/frogbot" "$bin_dir/frogbot"
  ln -sf "$bin_dir/frogbot" "$bin_dir/frog"
  xattr -d com.apple.quarantine "$bin_dir/frogbot" 2>/dev/null || true
  cat <<MACDONE

   ▄████▄▄▄▄████▄    frog CLI $ver 已安装到 $bin_dir/frog（兼容 frogbot）
   █▀▄▀██████▀▄▀█
  ████████████████   常用指令：
  ▀███▀▀▀▀▀▀▀▀███▀     frog                 打开交互式状态面板与菜单
   ▀▀▀        ▀▀▀      frog start           后台启动（内置加密穿透）
                       frog pair            终端直接打印二维码给手机扫码
                       frog status          查看运行与远程连接状态
                       frog logs -f         查看实时日志
                       frog service install 开启开机自启（LaunchAgent）
                       frog update          一键升级

MACDONE
  case ":$PATH:" in
    *":$bin_dir:"*) ;;
    *) warn "请将 $bin_dir 加入 PATH（例如写入 ~/.zshrc）：export PATH=\"$bin_dir:\$PATH\"" ;;
  esac
}

main() {
  if [ "$(uname -s)" = "Darwin" ] && ! sandbox; then
    install_macos
    exit 0
  fi
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
  say "$( [ -n "$upgrade" ] && echo 升级 || echo 安装 ) frog $ver（linux/$arch）"

  WORK_DIR="$(mktemp -d)"; trap 'rm -rf "${WORK_DIR:-}"' EXIT
  src="$(download "$ver" "$arch" "$WORK_DIR" | tail -n1)"

  resolve_host_user
  mkdir -p "$APP_DIR" "$DATA_ROOT/data" "$DATA_ROOT/workspace"
  chmod 750 "$DATA_ROOT"
  own "$DATA_ROOT"
  if [ -n "$upgrade" ] && ! sandbox; then systemctl stop frogbot 2>/dev/null || true; fi
  install -m 755 "$src/frogbot" "$APP_DIR/frogbot.new" && mv -f "$APP_DIR/frogbot.new" "$APP_DIR/frogbot"
  cp -f "$src/LICENSE" "$src/ATTRIBUTION.md" "$APP_DIR/" 2>/dev/null || true
  if [ -d "$src/licenses" ]; then rm -rf "$APP_DIR/licenses"; cp -R "$src/licenses" "$APP_DIR/licenses"; fi
  own "$APP_DIR"
  write_env
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

  local rootd_note=""
  if [ "$ROOT_HELPER" != 0 ] && ! sandbox; then
    write_rootd_unit
    mkdir -p "$ETC_DIR"
    if [ ! -f "$ROOTD_STATE" ]; then
      "$APP_DIR/frogbot" rootd sync-password -data "$DATA_ROOT/data" -state "$ROOTD_STATE" -enable >/dev/null \
        || warn "提权助手没能读取登录密码，之后可运行：sudo frogbot reset-password"
    fi
    rootd_note="提权：      已开启（Agent 需要 root 时弹卡片，输入登录密码授权；审计日志 /var/log/frogbot-rootd.log）"
  elif ! sandbox && [ -f "$ROOTD_UNIT_FILE" ]; then
    systemctl disable --now frogbot-rootd 2>/dev/null || true
    rm -f "$ROOTD_UNIT_FILE"
  fi

  if ! sandbox; then
    systemctl daemon-reload
    if [ -f "$ROOTD_UNIT_FILE" ]; then
      systemctl enable frogbot-rootd >/dev/null 2>&1
      systemctl restart frogbot-rootd || warn "提权助手启动失败：sudo journalctl -u frogbot-rootd"
    fi
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

   ▄████▄▄▄▄████▄    frog $ver 已$( [ -n "$upgrade" ] && echo 升级 || echo 安装 )，服务名 frogbot（宿主用户：$SVC_USER）
   █▀▄▀██████▀▄▀█    访问地址：$url
  ████████████████   用户名：  ${shown_user:-$ADMIN_USER}
  ▀███▀▀▀▀▀▀▀▀███▀
   ▀▀▀        ▀▀▀
DONE
  if [ -n "$password" ]; then
    echo "  初始密码：$password"
    echo "            ↑ 只显示这一次，请马上记下。登录后可在「设置 → 账号」修改。"
  else
    echo "  密码：    $user_line"
  fi
  cat <<DONE

${rootd_note:+  $rootd_note
}  快捷 CLI（直接输入 frog 即可打开交互式管理面板，同时兼容 frogbot）：
    sudo frog              交互式管理面板
    sudo frog status       查看服务与远程穿透状态
    sudo frog pair         终端直接打印手机配对二维码
    sudo frog logs -f      实时查看日志
    sudo frog restart      重启服务（支持 start / stop / restart）
    sudo frog update       一键升级（支持 beta / stable / 指定版本）
    sudo frog passwd       重置登录密码
  数据目录：$DATA_ROOT     配置：$ENV_FILE
  卸载：    curl -fsSL https://raw.githubusercontent.com/$REPO/main/scripts/install.sh | sudo bash -s uninstall

DONE
}

main "$@"
