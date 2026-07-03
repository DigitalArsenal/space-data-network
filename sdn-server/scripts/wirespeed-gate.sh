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
# Known-miss override (CLEARLY LABELED): loop C.5c landed body-reference
# egress (stream bytes never enter the flow), the flatsqlrt raw-stream
# mirror (warm queries: zero engine execution, zero copies), the in-wasm
# linked drain loop, and drain/ingress host-crossing reduction; a warm
# 8.6 MB request measures ~1.08-1.13 ms (~8.0 GB/s, faster than the raw
# TCP probe) vs ~0.91-1.06 ms baseline -- ~85% best / ~94% median, noise-
# dominated. The ~130 us residue is a handful of host<->wasm round-trips;
# >=99% of a ~1 ms baseline allows <=10 us TOTAL added latency, beyond any
# host-mediated dispatch (docs/flatsql-component-linkage.md section 7).
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
