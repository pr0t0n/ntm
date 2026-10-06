#!/usr/bin/env bash
# NTM Agent installer for Linux.
# Installs the agent as a systemd service. During installation you provide the
# platform address and the installation token issued by your NTM platform.
#
# Usage:
#   sudo ./install.sh                # interactive
#   sudo NTM_PLATFORM_URL=https://ntm.corp:8443 NTM_INSTALL_TOKEN=xxxx ./install.sh
set -euo pipefail

need_root() { [ "$(id -u)" -eq 0 ] || { echo "Run as root (sudo)." >&2; exit 1; }; }
need_root

BIN_SRC="${BIN_SRC:-./ntm-agent}"
INSTALL_DIR="/opt/ntm"
STATE_DIR="/var/lib/ntm"

# --- gather configuration ---
PLATFORM_URL="${NTM_PLATFORM_URL:-}"
INSTALL_TOKEN="${NTM_INSTALL_TOKEN:-}"
RANGES="${NTM_RANGES:-10.0.0.0/8,172.16.0.0/12,192.168.0.0/16}"

if [ -z "$PLATFORM_URL" ]; then
  read -rp "Platform URL (e.g. https://10.0.0.5:8443): " PLATFORM_URL
fi
if [ -z "$INSTALL_TOKEN" ]; then
  read -rsp "Installation token: " INSTALL_TOKEN; echo
fi
[ -n "$PLATFORM_URL" ] && [ -n "$INSTALL_TOKEN" ] || { echo "Platform URL and token are required." >&2; exit 1; }

command -v nmap >/dev/null 2>&1 || {
  echo "nmap not found. Install it first (e.g. apt-get install -y nmap / yum install -y nmap)." >&2
  exit 1
}

# --- install binary ---
install -d "$INSTALL_DIR" "$STATE_DIR"
if [ -f "$BIN_SRC" ]; then
  install -m 0755 "$BIN_SRC" "$INSTALL_DIR/ntm-agent"
else
  echo "Agent binary not found at $BIN_SRC. Build it with: go build -o ntm-agent ." >&2
  exit 1
fi

# --- write environment file ---
cat > "$INSTALL_DIR/agent.env" <<EOF
NTM_PLATFORM_URL=$PLATFORM_URL
NTM_INSTALL_TOKEN=$INSTALL_TOKEN
NTM_RANGES=$RANGES
NTM_SCAN_INTERVAL=${NTM_SCAN_INTERVAL:-30m}
NTM_TLS_INSECURE=${NTM_TLS_INSECURE:-false}
NTM_STATE_PATH=$STATE_DIR/agent-state.json
NTM_AGENT_NAME=${NTM_AGENT_NAME:-$(hostname)}
EOF
chmod 0600 "$INSTALL_DIR/agent.env"

# --- systemd unit ---
cat > /etc/systemd/system/ntm-agent.service <<EOF
[Unit]
Description=NTM Network Inventory Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=$INSTALL_DIR/agent.env
ExecStart=$INSTALL_DIR/ntm-agent
Restart=on-failure
RestartSec=10
# nmap raw-socket scans need these capabilities; drop everything else.
AmbientCapabilities=CAP_NET_RAW CAP_NET_ADMIN
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=$STATE_DIR

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now ntm-agent.service
echo "NTM agent installed and started. Check logs with: journalctl -u ntm-agent -f"
