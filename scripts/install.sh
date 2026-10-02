#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

# Run on the destination VPS with already-built binaries; no source compilation.
usage() { echo "用法：sudo bash scripts/install.sh panel|agent [二进制目录]"; }

generate_credentials() {
  local random_username
  random_username=$(openssl rand -hex 6) || return 1
  password=$(openssl rand -hex 24) || return 1
  [[ $random_username =~ ^[0-9a-f]{12}$ && $password =~ ^[0-9a-f]{48}$ ]] || return 1
  username="vx_$random_username"
}

wait_for_panel() {
  local deadline=$((SECONDS + ${1:-30})) status
  while (( SECONDS < deadline )); do
    status=$(curl --silent --noproxy '*' --output /dev/null --write-out '%{http_code}' \
      --connect-timeout 1 --max-time 2 http://127.0.0.1:8080/api/me) || status=''
    [[ $status == 401 ]] && return 0
    sleep 1
  done
  return 1
}

wait_for_https() {
  local site=$1 deadline=$((SECONDS + ${2:-90})) status
  while (( SECONDS < deadline )); do
    # Connect locally with the domain's SNI, while verifying the public certificate.
    status=$(curl --silent --noproxy '*' --output /dev/null --write-out '%{http_code}' \
      --connect-timeout 1 --max-time 2 --resolve "$site:443:127.0.0.1" \
      "https://$site/api/me") || status=''
    [[ $status == 401 ]] && return 0
    sleep 2
  done
  return 1
}

print_login() {
  printf '\n登录地址：https://%s\n管理员账号：%s\n管理员密码：%s\n' "$domain" "$username" "$password"
  echo "初始账号密码保存在 /etc/vibexui/panel.env（仅 root 可读）。"
}

main() {
[[ ${EUID} -eq 0 ]] || { echo "请使用 sudo 运行"; exit 1; }
mode=${1:-}; binary_dir=${2:-dist}; [[ $mode == panel || $mode == agent ]] || { usage; exit 1; }
command -v systemctl >/dev/null || { echo "需要使用 systemd 的 Linux 系统"; exit 1; }
[[ -f "$binary_dir/vibexui-$mode" ]] || { echo "找不到 $binary_dir/vibexui-$mode，请先 make linux 并上传对应架构的二进制"; exit 1; }
[[ ! -e /etc/vibexui/$mode.env && ! -e /etc/systemd/system/vibexui-$mode.service ]] || { echo "检测到已有安装。请先备份，再按 README 更新二进制；安装脚本不会覆盖配置。"; exit 1; }

if [[ $mode == panel ]]; then
  [[ ! -e /var/lib/vibexui/panel/panel.db ]] || { echo "已有面板数据库。请恢复原安装配置或参照 README 更新，不能用新随机账号覆盖已有登录凭据。"; exit 1; }
  read -r -p "面板域名（DNS 已指向本机，例如 panel.example.com）：" domain
  [[ $domain =~ ^[a-zA-Z0-9]([a-zA-Z0-9.-]*[a-zA-Z0-9])?$ && $domain == *.* && $domain != *..* ]] || { echo "域名格式无效"; exit 1; }
  local packages=() dependency
  for dependency in caddy openssl curl; do
    command -v "$dependency" >/dev/null || packages+=("$dependency")
  done
  if [[ ${#packages[@]} -gt 0 ]]; then
    command -v apt-get >/dev/null || { echo "请先安装 Caddy、OpenSSL 和 curl，再运行此脚本"; exit 1; }
    apt-get update
    apt-get install -y "${packages[@]}" ca-certificates
  fi
  [[ ! -e /etc/caddy/vibexui-panel.caddy ]] || { echo "已有 VibeXUI Caddy 站点，请先检查已有安装"; exit 1; }
  generate_credentials || { echo "随机账号密码生成失败，安装已停止"; exit 1; }
  id vibexui >/dev/null 2>&1 || useradd --system --home-dir /var/lib/vibexui --shell /usr/sbin/nologin vibexui
  install -d -m 0750 -o vibexui -g vibexui /var/lib/vibexui/panel
  install -d -m 0700 /etc/vibexui
  install -m 0755 "$binary_dir/vibexui-panel" /usr/local/bin/vibexui-panel
  local agent_arch source_agent
  for agent_arch in amd64 arm64; do
    source_agent="$binary_dir/../linux-$agent_arch/vibexui-agent"
    if [[ -f $source_agent ]]; then
      install -d -m 0755 "/var/lib/vibexui/downloads/linux-$agent_arch"
      install -m 0644 "$source_agent" "/var/lib/vibexui/downloads/linux-$agent_arch/vibexui-agent"
    fi
  done
  (umask 077; printf 'VIBEXUI_USERNAME=%s\nVIBEXUI_PASSWORD="%s"\n' "$username" "$password" >/etc/vibexui/panel.env)
  cat >/etc/systemd/system/vibexui-panel.service <<EOF
[Unit]
Description=VibeXUI Panel
After=network-online.target
Wants=network-online.target
[Service]
User=vibexui
Group=vibexui
EnvironmentFile=/etc/vibexui/panel.env
ExecStart=/usr/local/bin/vibexui-panel -listen 127.0.0.1:8080 -db /var/lib/vibexui/panel/panel.db -public-url https://$domain -downloads-dir /var/lib/vibexui/downloads
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/vibexui/panel
UMask=0077
[Install]
WantedBy=multi-user.target
EOF
  install -d -m 0755 /etc/caddy
  printf '%s {\n  reverse_proxy 127.0.0.1:8080\n}\n' "$domain" >/etc/caddy/vibexui-panel.caddy
  if [[ -f /etc/caddy/Caddyfile ]]; then
    cp -p /etc/caddy/Caddyfile /etc/caddy/Caddyfile.vibexui-backup
  else
    touch /etc/caddy/Caddyfile
    cp -p /etc/caddy/Caddyfile /etc/caddy/Caddyfile.vibexui-backup
  fi
  printf '\nimport /etc/caddy/vibexui-panel.caddy\n' >>/etc/caddy/Caddyfile
  if ! caddy validate --config /etc/caddy/Caddyfile; then
    cp -p /etc/caddy/Caddyfile.vibexui-backup /etc/caddy/Caddyfile
    echo "Caddy 配置校验失败，原配置已恢复。请手动检查站点和端口冲突。"
    exit 1
  fi
  systemctl daemon-reload
  systemctl enable --now vibexui-panel
  if ! wait_for_panel; then
    echo "面板尚未成功启动。账号密码已保存至 /etc/vibexui/panel.env；请运行 journalctl -u vibexui-panel -n 50 检查错误。"
    exit 1
  fi
  systemctl enable caddy
  systemctl restart caddy
  echo "正在等待 Caddy 自动申请证书并验证 HTTPS（最多约 90 秒）……"
  if wait_for_https "$domain"; then
    echo "安装完成，HTTPS 证书已验证，Caddy 将自动续期。"
    print_login
  else
    echo "面板已启动，但 HTTPS 尚未验证成功。请检查域名 A/AAAA 记录和 80/443 端口，Caddy 会继续尝试申请证书。"
    print_login
    echo "查看证书日志：journalctl -u caddy -n 50"
    exit 1
  fi
else
  [[ -x /usr/local/bin/xray ]] || { echo "请先从 XTLS/Xray-core 官方发布安装 Xray 到 /usr/local/bin/xray"; exit 1; }
  read -r -p "主面板地址（https://域名）：" panel
  [[ $panel =~ ^https://[a-zA-Z0-9.-]+(:[0-9]+)?$ ]] || { echo "面板地址格式无效"; exit 1; }
  read -r -p "服务器 ID：" server_id
  [[ $server_id =~ ^[a-zA-Z0-9_-]{16}$ ]] || { echo "服务器 ID 格式无效"; exit 1; }
  read -r -s -p "一次性注册令牌：" token; echo
  [[ $token =~ ^[a-zA-Z0-9_-]{43}$ ]] || { echo "注册令牌格式无效"; exit 1; }
  id vibexui >/dev/null 2>&1 || useradd --system --home-dir /var/lib/vibexui --shell /usr/sbin/nologin vibexui
  install -d -m 0750 -o vibexui -g vibexui /var/lib/vibexui/agent
  install -d -m 0700 /etc/vibexui
  install -m 0755 "$binary_dir/vibexui-agent" /usr/local/bin/vibexui-agent
  printf 'VIBEXUI_REGISTRATION_TOKEN=%s\n' "$token" >/etc/vibexui/agent.env
  chmod 0600 /etc/vibexui/agent.env
  cat >/etc/systemd/system/vibexui-agent.service <<EOF
[Unit]
Description=VibeXUI Agent
After=network-online.target
Wants=network-online.target
[Service]
User=vibexui
Group=vibexui
EnvironmentFile=/etc/vibexui/agent.env
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
  echo "Agent 已启动。确认服务器在线后，可清空 /etc/vibexui/agent.env 中的一次性令牌。"
fi
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
  main "$@"
fi
