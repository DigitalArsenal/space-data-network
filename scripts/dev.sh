#!/usr/bin/env bash
# Start kubo + the SDN admin dev server (which serves the SDN UI compiled into
# the desktop app at http://127.0.0.1:5173/). On Ctrl+C, also stop kubo.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

IPFS_BIN="${repo_root}/desktop/node_modules/kubo/kubo/ipfs"
LOG_DIR="${repo_root}/.tmp"
KUBO_LOG="${LOG_DIR}/kubo-dev.log"
KUBO_PID=""

# Non-default ports for the dev daemon so we coexist with IPFS Desktop / any
# other kubo that is already holding 4001/5001/8080.
DEV_SWARM_PORT="14001"
DEV_API_PORT="5101"
DEV_GATEWAY_PORT="8181"
KUBO_API_URL="http://127.0.0.1:${DEV_API_PORT}"

mkdir -p "${LOG_DIR}"

if [[ ! -x "${IPFS_BIN}" ]]; then
  echo "kubo binary not found at: ${IPFS_BIN}" >&2
  echo "Run 'npm run install:desktop' first." >&2
  exit 1
fi

cleanup() {
  local code=$?
  if [[ -n "${KUBO_PID}" ]] && kill -0 "${KUBO_PID}" 2>/dev/null; then
    echo
    echo "[dev] stopping kubo daemon (pid ${KUBO_PID})..."
    kill "${KUBO_PID}" 2>/dev/null || true
    wait "${KUBO_PID}" 2>/dev/null || true
  fi
  exit "${code}"
}
trap cleanup EXIT INT TERM

# Fast path: if a kubo is already answering on the *default* RPC port,
# reuse it — that's the zero-config experience.
if curl -fsS -X POST -o /dev/null --max-time 2 http://127.0.0.1:5001/api/v0/version 2>/dev/null; then
  echo "[dev] kubo already responding on :5001 — reusing it."
  KUBO_API_URL="http://127.0.0.1:5001"
else
  # Slow path: start our own kubo on a private repo + non-default ports.
  export IPFS_PATH="${IPFS_PATH:-${repo_root}/.tmp/ipfs-repo}"
  mkdir -p "${IPFS_PATH}"

  if [[ ! -f "${IPFS_PATH}/config" ]]; then
    echo "[dev] initializing IPFS repo at ${IPFS_PATH} ..."
    "${IPFS_BIN}" init >/dev/null
  fi

  # Re-apply config on every run — defaults could have been written previously
  # before we knew to pick non-default ports.
  "${IPFS_BIN}" config --json Addresses.Swarm \
    "[\"/ip4/0.0.0.0/tcp/${DEV_SWARM_PORT}\",\"/ip6/::/tcp/${DEV_SWARM_PORT}\",\"/ip4/0.0.0.0/udp/${DEV_SWARM_PORT}/quic-v1\",\"/ip6/::/udp/${DEV_SWARM_PORT}/quic-v1\"]" \
    >/dev/null
  "${IPFS_BIN}" config Addresses.API     "/ip4/127.0.0.1/tcp/${DEV_API_PORT}"     >/dev/null
  "${IPFS_BIN}" config Addresses.Gateway "/ip4/127.0.0.1/tcp/${DEV_GATEWAY_PORT}" >/dev/null
  "${IPFS_BIN}" config --json API.HTTPHeaders.Access-Control-Allow-Origin \
    "[\"http://localhost:5173\", \"http://127.0.0.1:5173\", \"${KUBO_API_URL}\"]" >/dev/null
  "${IPFS_BIN}" config --json API.HTTPHeaders.Access-Control-Allow-Methods \
    '["PUT", "POST"]' >/dev/null

  echo "[dev] starting kubo daemon (API :${DEV_API_PORT}, swarm :${DEV_SWARM_PORT}, gateway :${DEV_GATEWAY_PORT})"
  echo "[dev]   logs: ${KUBO_LOG}"
  : > "${KUBO_LOG}"
  # --migrate=true auto-runs fs-repo migrations when the kubo npm package
  # bumps and the repo schema is older than the binary expects.
  "${IPFS_BIN}" daemon --migrate=true >>"${KUBO_LOG}" 2>&1 &
  KUBO_PID=$!

  echo -n "[dev] waiting for kubo RPC on ${KUBO_API_URL} "
  for _ in $(seq 1 60); do
    if curl -fsS -X POST -o /dev/null "${KUBO_API_URL}/api/v0/version" 2>/dev/null; then
      echo " ready."
      break
    fi
    if ! kill -0 "${KUBO_PID}" 2>/dev/null; then
      echo
      echo "[dev] kubo daemon exited before becoming ready. Tail of log:" >&2
      tail -n 40 "${KUBO_LOG}" >&2
      exit 1
    fi
    echo -n "."
    sleep 1
  done

  if ! curl -fsS -X POST -o /dev/null "${KUBO_API_URL}/api/v0/version" 2>/dev/null; then
    echo
    echo "[dev] kubo RPC never came up after 60s. Tail of log:" >&2
    tail -n 40 "${KUBO_LOG}" >&2
    exit 1
  fi
fi

# Kill stray dev-server listeners from earlier sessions so the browser
# doesn't get confused by a leftover on 3117 (webui) or 5173 (prior run).
for stale_port in 3117 5173; do
  stale_pid="$(lsof -nP -iTCP:${stale_port} -sTCP:LISTEN -t 2>/dev/null || true)"
  if [[ -n "${stale_pid}" ]]; then
    echo "[dev] killing stale listener on :${stale_port} (pid ${stale_pid})"
    kill "${stale_pid}" 2>/dev/null || true
  fi
done

# Tell admin-dev.sh which kubo to talk to (it defaults to probing :5001).
export SDN_DEV_IPFS_API_CANDIDATES="${KUBO_API_URL}"

echo "[dev] handing off to admin:dev:http (SDN UI → http://127.0.0.1:5173/)"
echo "[dev]   kubo RPC: ${KUBO_API_URL}"
exec npm run admin:dev:http
