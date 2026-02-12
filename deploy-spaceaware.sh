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
  "mkdir -p '$REMOTE_SRC' '$REMOTE_BIN_DIR' '$REMOTE_SPACEAWARE_DIR' '$REMOTE_BUILD_DIR/OrbPro' '$REMOTE_BUILD_DIR/CesiumUnminified'"

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
plugins_code="$(ssh "${SSH_OPTS[@]}" "$SERVER" "curl -ksS -o /dev/null -w '%{http_code}' https://127.0.0.1/api/v1/plugins/manifest")"
admin_code="$(ssh "${SSH_OPTS[@]}" "$SERVER" "curl -ksS -o /dev/null -w '%{http_code}' https://127.0.0.1/admin")"

log "health=$health_code license_verify=$license_code plugins_manifest=$plugins_code admin=$admin_code"

if [[ "$health_code" != "200" ]]; then
  echo "Health check failed: expected 200, got $health_code" >&2
  exit 1
fi

if [[ "$license_code" == "404" ]]; then
  echo "License endpoint missing: expected non-404, got $license_code" >&2
  exit 1
fi

if [[ "$plugins_code" == "404" ]]; then
  echo "Plugin manifest endpoint missing: expected non-404, got $plugins_code" >&2
  exit 1
fi

log "Redeploy complete for $SERVER"
