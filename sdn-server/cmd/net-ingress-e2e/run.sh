#!/usr/bin/env bash
# The ONE E2E for sdn-security-network-20261015. The gauntlet runs it as
# `cmd sdn-server/cmd/net-ingress-e2e/run.sh` from the candidate checkout.
#
# It builds the candidate node and this E2E through the WasmEdge wrapper,
# boots the node loopback-isolated with a throwaway HOME/store/config, waits
# for health, then runs net-ingress-e2e against it as a second libp2p peer.
# The node process group it starts is stopped by exact PID. Exit 0 PASS.
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SDN="$REPO/sdn-server"
export WASMEDGE_DIR="${WASMEDGE_DIR:-$HOME/.local/share/spacedatanetwork/wasmedge-sdk/0.16.4-darwin-arm64}"
# The wrapper drives sdn-server unless pointed elsewhere; keep it explicit.
export SDN_GO_MODULE_DIR="$SDN"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/net-ingress-e2e.XXXXXX")"
NODE_PID=""

cleanup() {
  if [[ -n "$NODE_PID" ]] && kill -0 "$NODE_PID" 2>/dev/null; then
    kill -TERM "$NODE_PID" 2>/dev/null
    for _ in $(seq 1 40); do kill -0 "$NODE_PID" 2>/dev/null || break; sleep 0.25; done
    kill -0 "$NODE_PID" 2>/dev/null && kill -KILL "$NODE_PID" 2>/dev/null
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "[run] building node and e2e via WasmEdge wrapper"
( cd "$SDN" && "$REPO/scripts/go-with-wasmedge.sh" build -o "$WORK/spacedatanetwork" ./cmd/spacedatanetwork ) >"$WORK/build-node.log" 2>&1 \
  || { echo "NET-INGRESS-E2E FAIL: node build"; tail -8 "$WORK/build-node.log"; exit 1; }
( cd "$SDN" && "$REPO/scripts/go-with-wasmedge.sh" build -o "$WORK/net-ingress-e2e" ./cmd/net-ingress-e2e ) >"$WORK/build-e2e.log" 2>&1 \
  || { echo "NET-INGRESS-E2E FAIL: e2e build"; tail -8 "$WORK/build-e2e.log"; exit 1; }

# Share the compiled FlatSQL AOT cache so the throwaway HOME does not recompile it.
AOT="$HOME/Library/Caches/flatsql-aot"
if [[ -d "$AOT" ]]; then mkdir -p "$WORK/Library/Caches" && ln -s "$AOT" "$WORK/Library/Caches/flatsql-aot" 2>/dev/null || true; fi

# A free admin port.
PORT="$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')"
DATA="$WORK/data"
cat >"$WORK/config.yaml" <<YAML
mode: full
network:
  listen:
    - /ip4/127.0.0.1/tcp/0
  bootstrap: []
  edge_relays: []
  enable_relay: false
storage:
  path: "$DATA"
  max_size: 1GB
peers:
  strict_mode: false
  enable_dht: false
  enable_mdns: false
admin:
  enabled: true
  listen_addr: "127.0.0.1:$PORT"
  require_auth: true
  tls_mode: disabled
setup:
  data_path: "$DATA"
YAML

echo "[run] starting node (admin 127.0.0.1:$PORT)"
# Background the daemon ITSELF (not a `cd && …` subshell, whose child would
# reparent and outlive the exact-PID stop below). macOS has no setsid; the
# node does not daemonize, so NODE_PID is the process to stop.
cd "$WORK"
HOME="$WORK" "$WORK/spacedatanetwork" daemon --allow-multi-daemon --config "$WORK/config.yaml" >"$WORK/daemon.log" 2>&1 &
NODE_PID=$!
cd "$SDN"

BASE="http://127.0.0.1:$PORT"
up=""
for _ in $(seq 1 240); do
  kill -0 "$NODE_PID" 2>/dev/null || { echo "NET-INGRESS-E2E FAIL: node exited before health"; tail -12 "$WORK/daemon.log"; exit 1; }
  if [[ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "$BASE/api/v1/health" 2>/dev/null)" == "200" ]]; then up=1; break; fi
  sleep 0.5
done
[[ -n "$up" ]] || { echo "NET-INGRESS-E2E FAIL: no health 200"; tail -12 "$WORK/daemon.log"; exit 1; }
echo "[run] node healthy; running ingress E2E"

"$WORK/net-ingress-e2e" --admin "$BASE"
rc=$?
if [[ $rc -ne 0 ]]; then echo "[run] daemon tail:"; tail -20 "$WORK/daemon.log"; fi
exit $rc
