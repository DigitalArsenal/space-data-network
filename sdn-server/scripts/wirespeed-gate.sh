#!/usr/bin/env bash
# Loop C.5 — wirespeed gate (CI-runnable).
#
# Seeds a FlatSQL store through the normal ingest path, mounts the REAL
# data-retrieval flow bundle at /api/v1/data/, and measures:
#   (a) the flow-served streaming endpoint,
#   (b) a baseline handler streaming the same pre-materialized aligned bytes
#       over the same net/http stack,
#   (c) a raw-TCP wire_speed_probe reference.
# Prints the throughput table and exits NONZERO when (a) < 99% of (b).
#
# Known-miss override (CLEARLY LABELED): loop C.7 landed DIRECT engine
# linkage — query submission is in-wasm (zero storage hostcalls on any
# path), result bytes ride engine body-refs resolved under the store engine
# lock (warm: zero copies). libwasmedge 0.14 falsely traps AOT-compiled
# linked flows (docs/flatsql-component-linkage.md section 8.2), so linked
# mounts force-interpret the flow artifact THERE (~68% best / ~73% median).
# Loop C.9: libwasmedge 0.16.4 FIXES the AOT defects — linked mounts run
# AOT (version-gated, flatsqlrt.RuntimeHasLinkedAOTFix) and the gate
# measures ~97% best / ~93% median of the ~2.6 ms loopback baseline
# (flow ~3.2 GB/s warm; zero host dispatch remains — the residue is inside
# net/http, identical for flow and baseline handlers, so >=99% of a
# loopback baseline remains unmet; on a NIC-limited real wire the endpoint
# saturates). Run under WASMEDGE_ROOT=$HOME/.wasmedge-0.16.4 (or any
# >=0.16.4 install) for the linked+AOT numbers.
#   SDN_C5_ALLOW_BLOCKED=1 scripts/wirespeed-gate.sh
#
# Tunables (see wirespeed_gate_test.go): SDN_C5_OBJECTS, SDN_C5_EPOCHS,
# SDN_C5_LIMIT, SDN_C5_RUNS, SDN_C5_WARMUP, SDN_C5_POOL, SDN_C5_PAGES,
# SDN_C5_AOT, SDN_DATA_RETRIEVAL_FLOW_DIST.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SDN_SERVER_DIR="$(dirname "$SCRIPT_DIR")"

# WasmEdge CGO env (same conventions as the rest of the repo); respect
# anything the caller already exported.
WASMEDGE_ROOT="${WASMEDGE_ROOT:-$HOME/.wasmedge}"
export CGO_CFLAGS="${CGO_CFLAGS:--I$WASMEDGE_ROOT/include}"
export CGO_LDFLAGS="${CGO_LDFLAGS:--L$WASMEDGE_ROOT/lib -lwasmedge -Wl,-rpath,$WASMEDGE_ROOT/lib}"
if [[ "$(uname)" == "Darwin" ]]; then
  export DYLD_FALLBACK_LIBRARY_PATH="${DYLD_FALLBACK_LIBRARY_PATH:-$WASMEDGE_ROOT/lib}"
else
  export LD_LIBRARY_PATH="${LD_LIBRARY_PATH:-$WASMEDGE_ROOT/lib}"
fi

cd "$SDN_SERVER_DIR"
SDN_C5_WIRESPEED=1 exec go test ./internal/flowrt/ -run TestWirespeedGate -v -count=1 -timeout 60m "$@"
