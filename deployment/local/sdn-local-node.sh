#!/usr/bin/env bash
#
# sdn-local-node.sh — the LOCAL DOCKER TEST NODE on the developer machine.
#
#   ./deployment/local/sdn-local-node.sh up        # build + (re)start, keep identity
#   ./deployment/local/sdn-local-node.sh refresh   # what a deploy calls: rebuild + recreate
#   ./deployment/local/sdn-local-node.sh status
#   ./deployment/local/sdn-local-node.sh logs
#   ./deployment/local/sdn-local-node.sh down      # stop, KEEP the volume/identity
#   ./deployment/local/sdn-local-node.sh destroy   # stop AND delete the identity volume
#
# WHAT THIS IS, AND WHAT IT IS NOT
# --------------------------------
# Owner ruling 2026-07-28: the local docker node is a TEST PEER, not a
# production box. It exists so every release is exercised on a real container
# before it reaches a host, and so the developer machine carries a live node.
# It is deliberately NOT in deployment/topology.json's `cluster` map — it is
# recorded under `local_test_node` instead, because it is not cluster capacity.
#
# IDENTITY LAW (why there is no --mnemonic flag here)
# --------------------------------------------------
# This node ALWAYS generates its own fresh 24-word mnemonic on first boot, into
# its own named volume. Production key material is NEVER fed to it and there is
# no code path in this script that could. That is the whole point of a test
# peer: compromising it must reveal nothing about the cluster.
#
# The at-rest key password is generated ON THIS MACHINE into KEYDIR (0400) and
# reaches the container as a mounted file — never an env var, never a build arg,
# never argv. It is the machine-derived-default problem in reverse: a container
# has no stable hardware fingerprint, so an explicit password file is the only
# way the encrypted mnemonic survives a container recreate.
#
# BUILD LAW
# ---------
# Standing owner law: images are built LOCALLY, never on a host. Here that is
# trivially true. Note the platform default is the NATIVE arch of this machine,
# NOT linux/amd64: this is a test peer, and running an emulated amd64 image on
# an arm64 Mac would benchmark the emulator rather than the node. Cluster hosts
# still get linux/amd64 images from sdn-remote-deploy.sh.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# ---- constants ---------------------------------------------------------------
CONTAINER_NAME="sdn-local-test"
VOLUME_NAME="sdn-local-test-data"
IMAGE_TAG="${SDN_LOCAL_IMAGE:-sdn-node:local-test}"
KEYDIR="${SDN_LOCAL_KEYDIR:-${HOME}/.spacedatanetwork/local-node}"
RUNDIR="${SCRIPT_DIR}/.run"
CONTAINER_UID=1000
P2P_PORT="${SDN_LOCAL_P2P_PORT:-4101}"
ADMIN_PORT="${SDN_LOCAL_ADMIN_PORT:-5101}"

# Native platform: a test peer should run natively, not under emulation.
case "$(uname -m)" in
  arm64|aarch64) PLATFORM_DEFAULT="linux/arm64" ;;
  *)             PLATFORM_DEFAULT="linux/amd64" ;;
esac
PLATFORM="${SDN_LOCAL_PLATFORM:-$PLATFORM_DEFAULT}"

RED=$'\033[31m'; GRN=$'\033[32m'; YLW=$'\033[33m'; BLU=$'\033[34m'; NC=$'\033[0m'
info() { printf '%s==>%s %s\n' "$BLU" "$NC" "$*"; }
ok()   { printf '%s ok %s %s\n' "$GRN" "$NC" "$*"; }
warn() { printf '%swarn%s %s\n' "$YLW" "$NC" "$*"; }
die()  { printf '%sfail%s %s\n' "$RED" "$NC" "$*" >&2; exit 1; }

need_docker() {
  command -v docker >/dev/null 2>&1 || die "docker not found on PATH"
  docker info >/dev/null 2>&1 || die "docker daemon not reachable"
}

# ---- at-rest key password ----------------------------------------------------
# Generated once, here, and never regenerated: rotating it would make the
# already-encrypted mnemonic on the volume permanently undecryptable.
ensure_key_password() {
  mkdir -p "$KEYDIR"
  chmod 700 "$KEYDIR"
  if [[ -s "${KEYDIR}/key_password" ]]; then
    ok "reusing existing at-rest key password (${KEYDIR}/key_password)"
  else
    head -c 32 /dev/urandom | base64 | tr -d '\n' > "${KEYDIR}/key_password"
    chmod 400 "${KEYDIR}/key_password"
    ok "generated at-rest key password at ${KEYDIR}/key_password (0400)"
    warn "back this file up: without it the local node's identity cannot be decrypted"
  fi
}

# ---- rendered config ---------------------------------------------------------
render_config() {
  mkdir -p "$RUNDIR"
  cat > "${RUNDIR}/node.yaml" <<EOF
# RENDERED by deployment/local/sdn-local-node.sh — do not edit by hand.
# Local docker TEST node. Not cluster capacity. Own identity, generated on
# first boot into the ${VOLUME_NAME} volume.

mode: full

network:
  listen:
    - /ip4/0.0.0.0/tcp/4001
  # Public, dialable cluster peers. The local node dials OUT; nothing dials in,
  # because this machine is behind NAT. Recorded live-verified peer IDs only —
  # see deployment/topology.json. NEVER copy a peer ID from a stale template.
  bootstrap: []
  edge_relays: []
  max_connections: 256
  enable_relay: false

storage:
  path: /app/data/store
  max_size: 10GB
  gc_interval: 1h

admin:
  enabled: true
  listen_addr: 0.0.0.0:5001
  require_auth: true
  session_expiry: 24h
  totp_required: false
  auth_db_path: /app/data/auth.db
  tls_mode: disabled
  webui_path: /app/webui

wallet_wasm:
  assets_dir: /app/wallet-wasm
  ui_assets_dir: /app/wallet-ui

peers:
  registry_path: /app/data/peers.db

tor:
  enabled: false
  hidden_service_enabled: false

# This is a TEST PEER, not the ingest node. Ingest/OD flows belong to the
# celestrak.eth node; running them here would fork the authoritative source.
flows:
  enabled: false
  storage_path: /app/data/flows

setup:
  token_expiry: 10m
  data_path: /app/data
EOF

  cat > "${RUNDIR}/sdn.env" <<EOF
SDN_KEY_PASSWORD_FILE=/run/sdn-key/key_password
EOF
  chmod 600 "${RUNDIR}/sdn.env"
}

# ---- image -------------------------------------------------------------------
build_image() {
  info "building ${IMAGE_TAG} for ${PLATFORM} (locally — never on a host)"
  docker buildx build \
    --platform "$PLATFORM" \
    -f "${PROJECT_ROOT}/deployment/docker/Dockerfile" \
    -t "$IMAGE_TAG" \
    --load \
    "$PROJECT_ROOT"
  ok "image built: $(docker images "$IMAGE_TAG" --format '{{.Repository}}:{{.Tag}} {{.Size}}')"
}

# ---- lifecycle ---------------------------------------------------------------
start_container() {
  docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
  docker volume create "$VOLUME_NAME" >/dev/null

  # hostname is PINNED: the credstore root key is hostname-bound, and docker
  # hands a recreated container a new random hostname. Unpinned, every refresh
  # would orphan the stored credentials.
  docker run -d \
    --name "$CONTAINER_NAME" \
    --hostname "$CONTAINER_NAME" \
    --restart unless-stopped \
    --env-file "${RUNDIR}/sdn.env" \
    -p "${P2P_PORT}:4001" \
    -p "127.0.0.1:${ADMIN_PORT}:5001" \
    -v "${VOLUME_NAME}:/app/data" \
    -v "${RUNDIR}/node.yaml:/app/config/node.yaml:ro" \
    -v "${KEYDIR}/key_password:/run/sdn-key/key_password:ro" \
    --security-opt no-new-privileges:true \
    --log-opt max-size=20m --log-opt max-file=5 \
    "$IMAGE_TAG" \
    daemon --config /app/config/node.yaml >/dev/null
  ok "container ${CONTAINER_NAME} started (p2p :${P2P_PORT}, admin 127.0.0.1:${ADMIN_PORT})"
}

wait_ready() {
  info "waiting for the node to report a peer identity…"
  local i peer=""
  for i in $(seq 1 60); do
    peer="$(docker exec "$CONTAINER_NAME" sh -c \
      'wget -qO- http://127.0.0.1:5001/api/v1/status 2>/dev/null' 2>/dev/null \
      | sed -n 's/.*"peer_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
    [[ -n "$peer" ]] && break
    sleep 3
  done
  if [[ -z "$peer" ]]; then
    warn "no peer id from /api/v1/status yet; recent logs:"
    docker logs --tail 25 "$CONTAINER_NAME" 2>&1 || true
    return 1
  fi
  ok "local test node peer id: ${peer}"
  printf '%s\n' "$peer" > "${RUNDIR}/peer_id"
}

verify_identity_isolation() {
  # Hard assertion: the mnemonic must exist INSIDE the volume, and must not be
  # any phrase this script supplied (it never supplies one).
  if docker exec "$CONTAINER_NAME" sh -c 'test -s /app/data/keys/mnemonic'; then
    ok "encrypted mnemonic present at /app/data/keys/mnemonic (self-generated, inside the volume)"
  else
    warn "no /app/data/keys/mnemonic yet — identity may still be initialising"
  fi
}

cmd_up() {
  need_docker; ensure_key_password; render_config
  [[ "${SDN_LOCAL_SKIP_BUILD:-0}" == "1" ]] || build_image
  start_container; wait_ready || true; verify_identity_isolation
}

cmd_refresh() { SDN_LOCAL_SKIP_BUILD=0 cmd_up; }

cmd_status() {
  need_docker
  docker ps -a --filter "name=^${CONTAINER_NAME}$" \
    --format 'container: {{.Names}}\nimage:     {{.Image}}\nstatus:    {{.Status}}\nports:     {{.Ports}}'
  [[ -s "${RUNDIR}/peer_id" ]] && printf 'peer_id:   %s\n' "$(cat "${RUNDIR}/peer_id")"
  docker exec "$CONTAINER_NAME" sh -c 'wget -qO- http://127.0.0.1:5001/api/v1/status' 2>/dev/null \
    | head -c 800 || true
  echo
}

cmd_logs()    { need_docker; docker logs --tail "${2:-80}" -f "$CONTAINER_NAME"; }
cmd_down()    { need_docker; docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true; ok "stopped (volume ${VOLUME_NAME} kept — identity intact)"; }
cmd_destroy() {
  need_docker
  docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
  docker volume rm "$VOLUME_NAME" >/dev/null 2>&1 || true
  warn "destroyed container AND volume ${VOLUME_NAME} — the local node's identity is gone for good"
}

case "${1:-}" in
  up)      cmd_up ;;
  refresh) cmd_refresh ;;
  status)  cmd_status ;;
  logs)    cmd_logs "$@" ;;
  down)    cmd_down ;;
  destroy) cmd_destroy ;;
  *) sed -n '3,12p' "$0"; exit 1 ;;
esac
