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
REMOTE_SPACEAWARE_DIR="/opt/spacedatanetwork/spaceaware"
REMOTE_SPACEAWARE_INDEX="${REMOTE_SPACEAWARE_DIR}/index.html"
REMOTE_BUILD_DIR="${REMOTE_SPACEAWARE_DIR}/Build"
REMOTE_ENV_FILE="/etc/default/spacedatanetwork"
REMOTE_PLUGIN_ROOT="/opt/data/license/plugins"
REMOTE_WASM_DIR="/opt/spacedatanetwork/wasm"

ORBPRO_BUILD_ROOT="${ORBPRO_BUILD_ROOT:-$ROOT_DIR/../OrbPro/Build}"
ORBPRO_MODULE_DIR="${ORBPRO_MODULE_DIR:-$ORBPRO_BUILD_ROOT/OrbPro}"
ORBPRO_BASE_DIR="${ORBPRO_BASE_DIR:-$ORBPRO_BUILD_ROOT/CesiumUnminified}"

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

if [[ ! -f "$ORBPRO_MODULE_DIR/OrbPro.esm.js" ]]; then
  echo "Missing OrbPro module at $ORBPRO_MODULE_DIR/OrbPro.esm.js" >&2
  exit 1
fi

if [[ ! -d "$ORBPRO_BASE_DIR/Workers" ]]; then
  echo "Missing OrbPro base assets at $ORBPRO_BASE_DIR (Workers directory required)" >&2
  exit 1
fi

log "Building SpaceAware single-file app"
npm --prefix "$ROOT_DIR/packages/spaceaware" run build

log "Ensuring remote directories exist on $SERVER"
ssh "${SSH_OPTS[@]}" "$SERVER" \
  "mkdir -p '$REMOTE_SRC' '$REMOTE_BIN_DIR' '$REMOTE_SPACEAWARE_DIR' '$REMOTE_BUILD_DIR/OrbPro' '$REMOTE_BUILD_DIR/CesiumUnminified' '$REMOTE_PLUGIN_ROOT' '$REMOTE_WASM_DIR'"

log "Syncing sdn-server source to $SERVER"
rsync -az --delete --exclude=.git \
  "$ROOT_DIR/sdn-server/" \
  "$SERVER:$REMOTE_SRC/"

log "Syncing SpaceAware landing page to $SERVER"
rsync -az \
  "$ROOT_DIR/packages/spaceaware/dist/index.html" \
  "$SERVER:$REMOTE_SPACEAWARE_INDEX"

log "Syncing OrbPro module and runtime assets to $SERVER"
rsync -az --delete \
  "$ORBPRO_MODULE_DIR/" \
  "$SERVER:$REMOTE_BUILD_DIR/OrbPro/"
rsync -az --delete \
  "$ORBPRO_BASE_DIR/" \
  "$SERVER:$REMOTE_BUILD_DIR/CesiumUnminified/"

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

log "Ensuring license service environment on $SERVER"
ssh "${SSH_OPTS[@]}" "$SERVER" "
  set -e
  touch '$REMOTE_ENV_FILE'
  chmod 600 '$REMOTE_ENV_FILE'

  if ! grep -q '^SDN_PLUGIN_ROOT=' '$REMOTE_ENV_FILE'; then
    echo 'SDN_PLUGIN_ROOT=$REMOTE_PLUGIN_ROOT' >> '$REMOTE_ENV_FILE'
  fi

  if ! grep -q '^ORBPRO_KEY_BROKER_WASM_PATH=' '$REMOTE_ENV_FILE'; then
    echo 'ORBPRO_KEY_BROKER_WASM_PATH=$REMOTE_WASM_DIR/sdn-license-plugin.wasm' >> '$REMOTE_ENV_FILE'
  fi

  if ! grep -q '^ORBPRO_SERVER_PRIVATE_KEY_HEX=' '$REMOTE_ENV_FILE'; then
    new_key=\$(hexdump -n 32 -e '32/1 \"%02x\"' /dev/urandom)
    echo \"ORBPRO_SERVER_PRIVATE_KEY_HEX=\$new_key\" >> '$REMOTE_ENV_FILE'
    echo '[deploy-spaceaware] Generated new ORBPRO_SERVER_PRIVATE_KEY_HEX in $REMOTE_ENV_FILE'
  fi

  if ! grep -q '^DERIVATION_SECRET=' '$REMOTE_ENV_FILE'; then
    new_secret=\$(hexdump -n 48 -e '48/1 \"%02x\"' /dev/urandom)
    echo \"DERIVATION_SECRET=\$new_secret\" >> '$REMOTE_ENV_FILE'
    echo '[deploy-spaceaware] Generated new DERIVATION_SECRET in $REMOTE_ENV_FILE'
  fi

  if ! grep -q '^ORBPRO_KEYSERVER_ALLOWED_DOMAINS=' '$REMOTE_ENV_FILE'; then
    echo 'ORBPRO_KEYSERVER_ALLOWED_DOMAINS=spaceaware.io,www.spaceaware.io' >> '$REMOTE_ENV_FILE'
    echo '[deploy-spaceaware] Added default ORBPRO_KEYSERVER_ALLOWED_DOMAINS in $REMOTE_ENV_FILE'
  fi

  if ! grep -q '^SDN_WALLET_UI_PATH=' '$REMOTE_ENV_FILE'; then
    echo 'SDN_WALLET_UI_PATH=$REMOTE_WALLET_UI' >> '$REMOTE_ENV_FILE'
    echo '[deploy-spaceaware] Added SDN_WALLET_UI_PATH in $REMOTE_ENV_FILE'
  fi

  if ! grep -q '^HD_WALLET_WASM_PATH=' '$REMOTE_ENV_FILE'; then
    echo 'HD_WALLET_WASM_PATH=$REMOTE_WASM_DIR/hd-wallet.wasm' >> '$REMOTE_ENV_FILE'
    echo '[deploy-spaceaware] Added HD_WALLET_WASM_PATH in $REMOTE_ENV_FILE'
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

log "Verifying live endpoints"
health_code="$(ssh "${SSH_OPTS[@]}" "$SERVER" "curl -ksS -o /dev/null -w '%{http_code}' https://127.0.0.1/api/v1/data/health")"
license_code="$(ssh "${SSH_OPTS[@]}" "$SERVER" "curl -ksS -o /dev/null -w '%{http_code}' https://127.0.0.1/api/v1/license/verify")"
entitlements_code="$(ssh "${SSH_OPTS[@]}" "$SERVER" "token=\$(awk -F= '/^SDN_LICENSE_ADMIN_TOKEN=/{print \$2}' '$REMOTE_ENV_FILE' | tail -n 1); curl -ksS -o /dev/null -w '%{http_code}' -H \"X-License-Admin-Token: \$token\" 'https://127.0.0.1/api/v1/license/entitlements?xpub=__deploy_smoke__'")"
plugins_code="$(ssh "${SSH_OPTS[@]}" "$SERVER" "curl -ksS -o /dev/null -w '%{http_code}' https://127.0.0.1/api/v1/plugins/manifest")"
admin_code="$(ssh "${SSH_OPTS[@]}" "$SERVER" "curl -ksS -o /dev/null -w '%{http_code}' https://127.0.0.1/admin")"
keybroker_code="$(ssh "${SSH_OPTS[@]}" "$SERVER" "curl -ksS -o /dev/null -w '%{http_code}' https://127.0.0.1/orbpro-key-broker/v1/orbpro/public-key")"
login_code="$(ssh "${SSH_OPTS[@]}" "$SERVER" "curl -ksS -o /dev/null -w '%{http_code}' https://127.0.0.1/login")"

log "health=$health_code license_verify=$license_code entitlements_admin=$entitlements_code plugins_manifest=$plugins_code admin=$admin_code keybroker=$keybroker_code login=$login_code"

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
