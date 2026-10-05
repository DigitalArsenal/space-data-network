#!/usr/bin/env bash
# Start the SDN admin dev server (which serves the SDN UI compiled into the
# desktop app at http://127.0.0.1:5173/). Kubo runs inside the node, so there
# is no separate daemon to start or stop.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

# Non-default ports for the node's Kubo RPC API and gateway so the dev node
# coexists with IPFS Desktop or any other node already holding 5001/5002/8080.
DEV_API_PORT="5101"
DEV_GATEWAY_PORT="8181"

# Kill stray dev-server listeners from earlier sessions so the browser
# doesn't get confused by a leftover on 3117 (webui) or 5173 (prior run).
for stale_port in 3117 5173; do
  stale_pid="$(lsof -nP -iTCP:${stale_port} -sTCP:LISTEN -t 2>/dev/null || true)"
  if [[ -n "${stale_pid}" ]]; then
    echo "[dev] killing stale listener on :${stale_port} (pid ${stale_pid})"
    kill "${stale_pid}" 2>/dev/null || true
  fi
done

export SDN_DEV_IPFS_API_URL="${SDN_DEV_IPFS_API_URL:-http://127.0.0.1:${DEV_API_PORT}}"
export SDN_DEV_IPFS_GATEWAY_URL="${SDN_DEV_IPFS_GATEWAY_URL:-http://127.0.0.1:${DEV_GATEWAY_PORT}}"

echo "[dev] handing off to admin:dev:http (SDN UI → http://127.0.0.1:5173/)"
echo "[dev]   the node's Kubo RPC: ${SDN_DEV_IPFS_API_URL}"
exec npm run admin:dev:http
