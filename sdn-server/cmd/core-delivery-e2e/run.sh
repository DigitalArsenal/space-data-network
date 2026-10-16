#!/usr/bin/env bash
# core-delivery E2E: the SEC-001 hotfixes (root sign-in on the box only, the
# leaked identity's publish and sealed-RPC denylist) and the core-delivery
# admission rules (open bucket, per-connection cap, version retirement),
# against a live loopback node running the typed licensing WASM.
#
#   LICENSING_WASM=<typed licensing/core module.wasm> sdn-server/cmd/core-delivery-e2e/run.sh
#
# NODE_BIN / E2E_BIN run prebuilt binaries (a release build) instead of
# building from this checkout.
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SDN="$REPO/sdn-server"
export WASMEDGE_DIR="${WASMEDGE_DIR:-$HOME/.local/share/spacedatanetwork/wasmedge-sdk/0.16.4-darwin-arm64}"
export SDN_GO_MODULE_DIR="$SDN"

LICENSING_WASM="${LICENSING_WASM:-$REPO/../space-data-network-modules/licensing/core/dist/isomorphic/module.wasm}"
[[ -f "$LICENSING_WASM" ]] || { echo "CORE-DELIVERY-E2E FAIL: no licensing WASM at $LICENSING_WASM (set LICENSING_WASM)"; exit 1; }

WORK="$(mktemp -d "${TMPDIR:-/tmp}/core-delivery-e2e.XXXXXX")"
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

NODE="${NODE_BIN:-}"
E2E="${E2E_BIN:-}"
if [[ -z "$NODE" ]]; then
  NODE="$WORK/spacedatanetwork"
  ( cd "$SDN" && "$REPO/scripts/go-with-wasmedge.sh" build -o "$NODE" ./cmd/spacedatanetwork ) >"$WORK/build-node.log" 2>&1 \
    || { echo "CORE-DELIVERY-E2E FAIL: node build"; tail -8 "$WORK/build-node.log"; exit 1; }
fi
if [[ -z "$E2E" ]]; then
  E2E="$WORK/core-delivery-e2e"
  ( cd "$SDN" && "$REPO/scripts/go-with-wasmedge.sh" build -o "$E2E" ./cmd/core-delivery-e2e ) >"$WORK/build-e2e.log" 2>&1 \
    || { echo "CORE-DELIVERY-E2E FAIL: e2e build"; tail -8 "$WORK/build-e2e.log"; exit 1; }
fi
echo "[run] node $(shasum -a 256 "$NODE" 2>/dev/null | cut -c1-16 || sha256sum "$NODE" | cut -c1-16)…  licensing $(shasum -a 256 "$LICENSING_WASM" 2>/dev/null | cut -c1-16 || sha256sum "$LICENSING_WASM" | cut -c1-16)…"

AOT="$HOME/Library/Caches/flatsql-aot"
if [[ -d "$AOT" ]]; then mkdir -p "$WORK/Library/Caches" && ln -s "$AOT" "$WORK/Library/Caches/flatsql-aot" 2>/dev/null || true; fi

PORT="$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')"
DATA="$WORK/data"
MNEMONIC="$WORK/fixture-mnemonic"
mkdir -p "$DATA"
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
update:
  enabled: false
setup:
  data_path: "$DATA"
YAML

"$E2E" --prepare --storage "$DATA" --licensing-wasm "$LICENSING_WASM" --mnemonic-file "$MNEMONIC" --hd-wallet-out "$WORK/hd-wallet-wasi.wasm" \
  || { echo "CORE-DELIVERY-E2E FAIL: fixture"; exit 1; }

HOME="$WORK" HD_WALLET_WASM_PATH="$WORK/hd-wallet-wasi.wasm" "$NODE" key import --config "$WORK/config.yaml" --format mnemonic --input "$MNEMONIC" >"$WORK/key-import.log" 2>&1 \
  || { echo "CORE-DELIVERY-E2E FAIL: fixture identity import"; grep -iv 'phrase\|mnemonic:' "$WORK/key-import.log" | tail -5; exit 1; }

echo "[run] starting node (admin 127.0.0.1:$PORT)"
cd "$WORK"
HOME="$WORK" HD_WALLET_WASM_PATH="$WORK/hd-wallet-wasi.wasm" ORBPRO_LICENSING_WASM_PATH="$LICENSING_WASM" \
  "$NODE" daemon --allow-multi-daemon --config "$WORK/config.yaml" >"$WORK/daemon.log" 2>&1 &
NODE_PID=$!
cd "$SDN"
BASE="http://127.0.0.1:$PORT"
up=""
for _ in $(seq 1 360); do
  kill -0 "$NODE_PID" 2>/dev/null || { echo "CORE-DELIVERY-E2E FAIL: node exited before health"; tail -12 "$WORK/daemon.log"; exit 1; }
  if [[ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "$BASE/api/v1/health" 2>/dev/null)" == "200" ]] \
     && grep -q 'Unified licensing module registered' "$WORK/daemon.log"; then up=1; break; fi
  sleep 0.5
done
[[ -n "$up" ]] || { echo "CORE-DELIVERY-E2E FAIL: no healthy node with licensing"; tail -12 "$WORK/daemon.log"; exit 1; }
echo "[run] node healthy, licensing up; running core-delivery E2E"
"$E2E" --admin "$BASE" --daemon-log "$WORK/daemon.log" --mnemonic-file "$MNEMONIC"
rc=$?
if [[ $rc -ne 0 ]]; then echo "[run] daemon tail:"; grep -v 'mnemonic' "$WORK/daemon.log" | tail -25; fi
exit $rc
