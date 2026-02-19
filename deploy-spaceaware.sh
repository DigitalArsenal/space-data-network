#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVER_IP="${SERVER_IP:-159.203.150.8}"
SERVER_USER="${SERVER_USER:-root}"
SERVER="${SERVER_USER}@${SERVER_IP}"
SSH_OPTS=(-o StrictHostKeyChecking=accept-new)

REMOTE_SRC="/opt/spacedatanetwork/src/sdn-server"
REMOTE_BIN_DIR="/opt/spacedatanetwork/bin"
REMOTE_BIN="${REMOTE_BIN_DIR}/spacedatanetwork"
REMOTE_FRONTEND_DIR="/opt/spacedatanetwork/frontend"
REMOTE_ENV_FILE="/etc/default/spacedatanetwork"
REMOTE_PLUGIN_ROOT="/opt/data/license/plugins"
REMOTE_WASM_DIR="/opt/spacedatanetwork/wasm"

# Kubo (IPFS) — API on 5002 to avoid conflict with SDN admin on 5001
KUBO_VERSION="${KUBO_VERSION:-v0.34.1}"
KUBO_API_PORT=5002
KUBO_GATEWAY_PORT=8081
KUBO_SWARM_PORT=4002

log() {
  printf "[deploy-spaceaware] %s\n" "$*"
}

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Missing required command: $1" >&2
    exit 1
  fi
}

require_cmd ssh
require_cmd rsync
require_cmd npm

log "Building WebUI (React SPA)"
npm --prefix "$ROOT_DIR/webui" install
npm --prefix "$ROOT_DIR/webui" run build

log "Ensuring remote directories exist on $SERVER"
ssh "${SSH_OPTS[@]}" "$SERVER" \
  "mkdir -p '$REMOTE_SRC' '$REMOTE_BIN_DIR' '$REMOTE_FRONTEND_DIR' '$REMOTE_PLUGIN_ROOT' '$REMOTE_WASM_DIR' '/opt/spacedatanetwork/webui'"

log "Syncing sdn-server source to $SERVER"
rsync -az --delete --exclude=.git \
  "$ROOT_DIR/sdn-server/" \
  "$SERVER:$REMOTE_SRC/"

log "Syncing WebUI build to $SERVER"
rsync -az --delete \
  "$ROOT_DIR/webui/build/" \
  "$SERVER:/opt/spacedatanetwork/webui/"

LICENSE_WASM="$ROOT_DIR/packages/sdn-license-plugin/build-wasi/sdn-license-plugin.wasm"
if [[ -f "$LICENSE_WASM" ]]; then
  log "Syncing key broker WASM plugin to $SERVER"
  rsync -az "$LICENSE_WASM" "$SERVER:$REMOTE_WASM_DIR/sdn-license-plugin.wasm"
else
  log "Warning: key broker WASM not found at $LICENSE_WASM (build with: cd packages/sdn-license-plugin && mkdir -p build-wasi && cd build-wasi && cmake .. -DCMAKE_TOOLCHAIN_FILE=../cmake/wasi-sdk.cmake && make)"
fi

HD_WALLET_WASM="$ROOT_DIR/../hd-wallet-wasm/build-wasi/wasm/hd-wallet-wasi.wasm"
if [[ -f "$HD_WALLET_WASM" ]]; then
  log "Syncing HD wallet WASM to $SERVER"
  rsync -az "$HD_WALLET_WASM" "$SERVER:$REMOTE_WASM_DIR/hd-wallet.wasm"
else
  log "Warning: HD wallet WASM not found at $HD_WALLET_WASM (build with: cd ../hd-wallet-wasm && mkdir -p build-wasi && cd build-wasi && cmake .. -DCMAKE_TOOLCHAIN_FILE=../cmake/wasi-sdk.cmake && make)"
fi

WALLET_UI_DIST="$ROOT_DIR/../hd-wallet-wasm/wallet-ui/dist"
REMOTE_WALLET_UI="/opt/spacedatanetwork/wallet-ui"
if [[ -d "$WALLET_UI_DIST" ]]; then
  log "Syncing wallet-ui dist to $SERVER"
  ssh "${SSH_OPTS[@]}" "$SERVER" "mkdir -p '$REMOTE_WALLET_UI'"
  rsync -az --delete "$WALLET_UI_DIST/" "$SERVER:$REMOTE_WALLET_UI/"
else
  log "Warning: wallet-ui dist not found at $WALLET_UI_DIST (build with: cd ../hd-wallet-wasm/wallet-ui && npm run build)"
fi

log "Installing/configuring Kubo (IPFS) on $SERVER"
ssh "${SSH_OPTS[@]}" "$SERVER" "
  set -e

  # Install Kubo if missing or wrong version
  WANTED='$KUBO_VERSION'
  CURRENT=\$(ipfs --version 2>/dev/null | awk '{print \"v\"\$3}' || echo none)
  if [ \"\$CURRENT\" != \"\$WANTED\" ]; then
    echo \"[deploy-spaceaware] Installing Kubo \$WANTED (current: \$CURRENT)\"
    cd /tmp
    ARCH=\$(uname -m)
    case \$ARCH in
      x86_64)  ARCH=amd64 ;;
      aarch64) ARCH=arm64 ;;
    esac
    curl -fsSL \"https://dist.ipfs.tech/kubo/\${WANTED}/kubo_\${WANTED}_linux-\${ARCH}.tar.gz\" -o kubo.tar.gz
    tar xzf kubo.tar.gz
    cd kubo
    bash install.sh
    rm -rf /tmp/kubo /tmp/kubo.tar.gz
    echo \"[deploy-spaceaware] Kubo \$WANTED installed\"
  else
    echo \"[deploy-spaceaware] Kubo \$WANTED already installed\"
  fi

  # Create ipfs user if needed
  if ! id -u ipfs >/dev/null 2>&1; then
    useradd -r -m -d /var/lib/ipfs -s /usr/sbin/nologin ipfs
    echo '[deploy-spaceaware] Created ipfs system user'
  fi

  # Init IPFS repo if needed
  export IPFS_PATH=/var/lib/ipfs
  if [ ! -f \$IPFS_PATH/config ]; then
    sudo -u ipfs IPFS_PATH=\$IPFS_PATH ipfs init --profile=server
    echo '[deploy-spaceaware] Initialized IPFS repo'
  fi

  # Configure ports to avoid conflicts with SDN (admin=5001, p2p=4001, ws=8080)
  sudo -u ipfs IPFS_PATH=\$IPFS_PATH ipfs config Addresses.API /ip4/127.0.0.1/tcp/$KUBO_API_PORT
  sudo -u ipfs IPFS_PATH=\$IPFS_PATH ipfs config Addresses.Gateway /ip4/127.0.0.1/tcp/$KUBO_GATEWAY_PORT
  sudo -u ipfs IPFS_PATH=\$IPFS_PATH ipfs config --json Addresses.Swarm '[\"/ip4/0.0.0.0/tcp/$KUBO_SWARM_PORT\", \"/ip4/0.0.0.0/udp/$KUBO_SWARM_PORT/quic-v1\", \"/ip4/0.0.0.0/udp/$KUBO_SWARM_PORT/quic-v1/webtransport\", \"/ip6/::/tcp/$KUBO_SWARM_PORT\", \"/ip6/::/udp/$KUBO_SWARM_PORT/quic-v1\"]'

  # CORS — SDN strips Origin when proxying, but configure anyway for direct access
  sudo -u ipfs IPFS_PATH=\$IPFS_PATH ipfs config --json API.HTTPHeaders.Access-Control-Allow-Origin '[\"https://spaceaware.io\", \"http://localhost:3000\", \"http://127.0.0.1:$KUBO_API_PORT\", \"https://webui.ipfs.io\"]'
  sudo -u ipfs IPFS_PATH=\$IPFS_PATH ipfs config --json API.HTTPHeaders.Access-Control-Allow-Methods '[\"PUT\", \"POST\"]'

  # Create systemd service
  cat > /etc/systemd/system/ipfs.service <<'UNIT'
[Unit]
Description=IPFS Kubo Daemon
After=network.target

[Service]
Type=simple
User=ipfs
Environment=IPFS_PATH=/var/lib/ipfs
ExecStart=/usr/local/bin/ipfs daemon --migrate=true
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT

  systemctl daemon-reload
  systemctl enable ipfs
  systemctl restart ipfs
  sleep 2
  if systemctl is-active ipfs >/dev/null 2>&1; then
    echo '[deploy-spaceaware] IPFS daemon running'
  else
    echo '[deploy-spaceaware] WARNING: IPFS daemon failed to start'
    journalctl -u ipfs --no-pager -n 10
  fi
"

log "Ensuring license service environment on $SERVER"
ssh "${SSH_OPTS[@]}" "$SERVER" "
  set -e
  touch '$REMOTE_ENV_FILE'
  chmod 600 '$REMOTE_ENV_FILE'

  if ! grep -q '^SDN_PLUGIN_ROOT=' '$REMOTE_ENV_FILE'; then
    echo 'SDN_PLUGIN_ROOT=$REMOTE_PLUGIN_ROOT' >> '$REMOTE_ENV_FILE'
  fi

  if ! grep -q '^PLUGIN_KEY_BROKER_WASM_PATH=' '$REMOTE_ENV_FILE'; then
    echo 'PLUGIN_KEY_BROKER_WASM_PATH=$REMOTE_WASM_DIR/sdn-license-plugin.wasm' >> '$REMOTE_ENV_FILE'
  fi
  if ! grep -q '^PLUGIN_SERVER_PRIVATE_KEY_HEX=' '$REMOTE_ENV_FILE'; then
    new_key=\$(hexdump -n 32 -e '32/1 \"%02x\"' /dev/urandom)
    echo \"PLUGIN_SERVER_PRIVATE_KEY_HEX=\$new_key\" >> '$REMOTE_ENV_FILE'
    echo '[deploy-spaceaware] Generated new PLUGIN_SERVER_PRIVATE_KEY_HEX in $REMOTE_ENV_FILE'
  fi

  if ! grep -q '^DERIVATION_SECRET=' '$REMOTE_ENV_FILE'; then
    new_secret=\$(hexdump -n 48 -e '48/1 \"%02x\"' /dev/urandom)
    echo \"DERIVATION_SECRET=\$new_secret\" >> '$REMOTE_ENV_FILE'
    echo '[deploy-spaceaware] Generated new DERIVATION_SECRET in $REMOTE_ENV_FILE'
  fi

  if ! grep -q '^PLUGIN_KEYSERVER_ALLOWED_DOMAINS=' '$REMOTE_ENV_FILE'; then
    echo 'PLUGIN_KEYSERVER_ALLOWED_DOMAINS=spaceaware.io,www.spaceaware.io' >> '$REMOTE_ENV_FILE'
    echo '[deploy-spaceaware] Added default PLUGIN_KEYSERVER_ALLOWED_DOMAINS in $REMOTE_ENV_FILE'
  fi

  if ! grep -q '^SDN_WALLET_UI_PATH=' '$REMOTE_ENV_FILE'; then
    echo 'SDN_WALLET_UI_PATH=$REMOTE_WALLET_UI' >> '$REMOTE_ENV_FILE'
    echo '[deploy-spaceaware] Added SDN_WALLET_UI_PATH in $REMOTE_ENV_FILE'
  fi

  if ! grep -q '^SDN_WEBUI_PATH=' '$REMOTE_ENV_FILE'; then
    echo 'SDN_WEBUI_PATH=/opt/spacedatanetwork/webui' >> '$REMOTE_ENV_FILE'
    echo '[deploy-spaceaware] Added SDN_WEBUI_PATH in $REMOTE_ENV_FILE'
  fi

  if ! grep -q '^HD_WALLET_WASM_PATH=' '$REMOTE_ENV_FILE'; then
    echo 'HD_WALLET_WASM_PATH=$REMOTE_WASM_DIR/hd-wallet.wasm' >> '$REMOTE_ENV_FILE'
    echo '[deploy-spaceaware] Added HD_WALLET_WASM_PATH in $REMOTE_ENV_FILE'
  fi

  if ! grep -q '^SDN_IPFS_API_URL=' '$REMOTE_ENV_FILE'; then
    echo 'SDN_IPFS_API_URL=http://127.0.0.1:$KUBO_API_PORT' >> '$REMOTE_ENV_FILE'
    echo '[deploy-spaceaware] Added SDN_IPFS_API_URL in $REMOTE_ENV_FILE'
  fi

  if ! grep -q '^SDN_IPFS_GATEWAY_URL=' '$REMOTE_ENV_FILE'; then
    echo 'SDN_IPFS_GATEWAY_URL=http://127.0.0.1:$KUBO_GATEWAY_PORT' >> '$REMOTE_ENV_FILE'
    echo '[deploy-spaceaware] Added SDN_IPFS_GATEWAY_URL in $REMOTE_ENV_FILE'
  fi

  current_token=\$(awk -F= '/^SDN_LICENSE_ADMIN_TOKEN=/{print \$2}' '$REMOTE_ENV_FILE' | tail -n 1)
  if [ -z \"\$current_token\" ] || [ \"\$current_token\" = 'replace-with-long-random-secret' ]; then
    new_token=\$(hexdump -n 32 -e '32/1 \"%02x\"' /dev/urandom)
    if grep -q '^SDN_LICENSE_ADMIN_TOKEN=' '$REMOTE_ENV_FILE'; then
      sed -i \"s/^SDN_LICENSE_ADMIN_TOKEN=.*/SDN_LICENSE_ADMIN_TOKEN=\$new_token/\" '$REMOTE_ENV_FILE'
    else
      echo \"SDN_LICENSE_ADMIN_TOKEN=\$new_token\" >> '$REMOTE_ENV_FILE'
    fi
    echo '[deploy-spaceaware] Generated new SDN_LICENSE_ADMIN_TOKEN in $REMOTE_ENV_FILE'
  fi
"

log "Building server binary and restarting service on $SERVER"
ssh "${SSH_OPTS[@]}" "$SERVER" "
  set -e
  cd '$REMOTE_SRC'
  export GOTOOLCHAIN=auto
  go build -o '$REMOTE_BIN' ./cmd/spacedatanetwork
  systemctl restart spacedatanetwork
  sleep 1
  systemctl is-active spacedatanetwork
"

probe_code() {
  local cmd="$1"
  local max_tries=30
  local code="000"

  for ((i = 1; i <= max_tries; i++)); do
    code="$(ssh "${SSH_OPTS[@]}" "$SERVER" "$cmd" || echo 000)"
    if [[ "$code" =~ ^[0-9]{3}$ ]]; then
      echo "$code"
      return
    fi
    sleep 1
  done

  echo "$code"
}

log "Verifying live endpoints"
health_code="$(probe_code "curl -ksS -o /dev/null -w '%{http_code}' https://127.0.0.1/api/v1/data/health 2>/dev/null")"
license_code="$(probe_code "curl -ksS -o /dev/null -w '%{http_code}' https://127.0.0.1/api/v1/license/verify 2>/dev/null")"
entitlements_code="$(probe_code "token=\$(awk -F= '/^SDN_LICENSE_ADMIN_TOKEN=/{print \$2}' '$REMOTE_ENV_FILE' | tail -n 1); curl -ksS -o /dev/null -w '%{http_code}' -H \"X-License-Admin-Token: \$token\" 'https://127.0.0.1/api/v1/license/entitlements?xpub=__deploy_smoke__' 2>/dev/null")"
plugins_code="$(probe_code "curl -ksS -o /dev/null -w '%{http_code}' https://127.0.0.1/api/v1/plugins/manifest 2>/dev/null")"
admin_code="$(probe_code "curl -ksS -o /dev/null -w '%{http_code}' https://127.0.0.1/admin 2>/dev/null")"
login_code="$(probe_code "curl -ksS -o /dev/null -w '%{http_code}' https://127.0.0.1/login 2>/dev/null")"
ipfs_code="$(probe_code "curl -sS -o /dev/null -w '%{http_code}' -X POST http://127.0.0.1:${KUBO_API_PORT}/api/v0/id 2>/dev/null")"

# Key broker no longer exposes HTTP endpoints — key exchange uses libp2p streams.
log "health=$health_code license_verify=$license_code entitlements_admin=$entitlements_code plugins_manifest=$plugins_code admin=$admin_code login=$login_code ipfs_api=$ipfs_code"

if [[ "$health_code" != "200" ]]; then
  echo "Health check failed: expected 200, got $health_code" >&2
  exit 1
fi

if [[ "$license_code" == "404" ]]; then
  echo "License endpoint missing: expected non-404, got $license_code" >&2
  exit 1
fi

if [[ "$entitlements_code" != "200" && "$entitlements_code" != "404" ]]; then
  echo "Entitlements admin check failed: expected 200 or 404, got $entitlements_code" >&2
  exit 1
fi

if [[ "$plugins_code" == "404" ]]; then
  echo "Plugin manifest endpoint missing: expected non-404, got $plugins_code" >&2
  exit 1
fi

log "Redeploy complete for $SERVER"
