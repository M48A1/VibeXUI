#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

die() { echo "错误：$*" >&2; exit 1; }
architecture() {
  case "$1" in
    x86_64|amd64) echo amd64;;
    aarch64|arm64) echo arm64;;
    *) return 1;;
  esac
}
verify_sha256() {
  local expected=$1 file=$2 actual
  [[ $expected =~ ^[0-9a-f]{64}$ ]] || return 1
  actual=$(sha256sum "$file") || return 1
  [[ ${actual%% *} == "$expected" ]]
}
download() { curl -q -fLsS --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 300 "$@"; }

main() {
  [[ $EUID -eq 0 ]] || die "请以 root 运行面板生成的安装命令"
  [[ $(uname -s) == Linux ]] || die "只支持 Linux"
  command -v systemctl >/dev/null || die "需要 systemd"
  command -v apt-get >/dev/null || die "一键安装目前支持 Debian / Ubuntu"
  local panel=${VIBEXUI_PANEL:-} server_id=${VIBEXUI_SERVER_ID:-} token=${VIBEXUI_REGISTRATION_TOKEN:-}
  [[ $panel =~ ^https://[a-zA-Z0-9.-]+(:[0-9]+)?$ ]] || die "主面板地址无效"
  [[ $server_id =~ ^[a-zA-Z0-9_-]{16}$ ]] || die "服务器 ID 无效"
  [[ $token =~ ^[a-zA-Z0-9_-]{43}$ ]] || die "注册令牌无效"
  [[ ! -e /etc/systemd/system/vibexui-agent.service && ! -e /var/lib/vibexui/agent/agent.json ]] || die "已有 Agent 安装。请按 README 重新注册或更新，不会覆盖现有凭据"
  local arch work checksum xray_asset xray_checksum cleanup_command
  arch=$(architecture "$(uname -m)") || die "仅支持 amd64 / arm64"
  echo "[1/5] 安装下载与校验工具"
  apt-get update
  apt-get install -y curl ca-certificates unzip coreutils util-linux
  work=$(mktemp -d)
  printf -v cleanup_command 'rm -rf -- %q' "$work"
  trap "$cleanup_command" EXIT
  echo "[2/5] 下载并校验 Agent"
  checksum=$(download -H "Authorization: Bearer $token" -H "X-VibeXUI-Server: $server_id" "$panel/api/agent/download/$arch/sha256") || die "下载校验值失败，请检查主面板的发行文件或注册令牌"
  download -H "Authorization: Bearer $token" -H "X-VibeXUI-Server: $server_id" "$panel/api/agent/download/$arch" -o "$work/vibexui-agent"
  verify_sha256 "$checksum" "$work/vibexui-agent" || die "Agent SHA256 校验失败"
  echo "[3/5] 安装 Xray-core"
  if [[ ! -e /usr/local/bin/xray ]]; then
    case "$arch" in
      amd64) xray_asset=Xray-linux-64.zip; xray_checksum=23cd9af937744d97776ee35ecad4972cf4b2109d1e0fe6be9930467608f7c8ae;;
      arm64) xray_asset=Xray-linux-arm64-v8a.zip; xray_checksum=4d30283ae614e3057f730f67cd088a42be6fdf91f8639d82cb69e48cde80413c;;
    esac
    download "https://github.com/XTLS/Xray-core/releases/download/v26.3.27/$xray_asset" -o "$work/xray.zip"
    verify_sha256 "$xray_checksum" "$work/xray.zip" || die "Xray SHA256 校验失败"
    unzip -q "$work/xray.zip" xray -d "$work"
    install -m 0755 "$work/xray" /usr/local/bin/xray
  fi
  [[ -x /usr/local/bin/xray ]] || die "现有 Xray 文件不可执行，未覆盖该文件"
  /usr/local/bin/xray version
  # Existing independent Xray services remain untouched. The Agent owns its own child process.
  id vibexui >/dev/null 2>&1 || useradd --system --home-dir /var/lib/vibexui --shell /usr/sbin/nologin vibexui
  install -d -m 0750 -o vibexui -g vibexui /var/lib/vibexui/agent
  install -m 0755 "$work/vibexui-agent" /usr/local/bin/vibexui-agent
  echo "[4/5] 注册服务器"
  runuser -u vibexui -- env VIBEXUI_REGISTRATION_TOKEN="$token" \
    /usr/local/bin/vibexui-agent -register-only -panel "$panel" -server-id "$server_id" \
    -data-dir /var/lib/vibexui/agent -xray /usr/local/bin/xray
  unset VIBEXUI_REGISTRATION_TOKEN token
  echo "[5/5] 配置开机启动"
  cat >/etc/systemd/system/vibexui-agent.service <<EOF
[Unit]
Description=VibeXUI Agent
After=network-online.target
Wants=network-online.target
[Service]
User=vibexui
Group=vibexui
ExecStart=/usr/local/bin/vibexui-agent -panel $panel -server-id $server_id -data-dir /var/lib/vibexui/agent -xray /usr/local/bin/xray
Restart=on-failure
RestartSec=5
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/vibexui/agent
UMask=0077
[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable --now vibexui-agent
  systemctl is-active --quiet vibexui-agent || die "Agent 尚未启动，请运行 journalctl -u vibexui-agent -n 50"
  echo "安装完成，服务器已注册，Agent 已启用开机启动。"
  echo "回到主面板查看在线状态，并创建入站、分配用户。"
  echo "日志：journalctl -u vibexui-agent -f"
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then main "$@"; fi
