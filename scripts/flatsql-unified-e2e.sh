#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
export SDN_WASM_REQUIRE_PATCHED=1
# With no WASMEDGE_DIR, use the newest static prefix that carries the runtime
# patch series this checkout links against (internal/wasmrt/patch_series.go).
# The fixed default, ~/.sdn-wasmedge-static-macos-patched/prefix, is the
# 2026-10-02 prefix without series 10, so every link of it failed with
# "_WasmEdge_SDNPatchSeries10" undefined.
if [[ -z "${WASMEDGE_DIR:-}" ]]; then
  series="$(grep -oE 'WasmEdge_SDNPatchSeries[0-9]+' sdn-server/internal/wasmrt/patch_series.go | head -n 1)"
  for candidate in $(ls -d "$HOME"/.sdn-wasmedge-static-macos-patched/work-*/prefix 2>/dev/null | sort -r) \
    "$HOME/.sdn-wasmedge-static-macos-patched/prefix"; do
    if [[ -f "$candidate/link.flags" && -f "$candidate/lib/libwasmedge.a" ]] &&
      nm -g "$candidate/lib/libwasmedge.a" 2>/dev/null | grep "_*${series}\$" >/dev/null; then
      WASMEDGE_DIR="$candidate"
      break
    fi
  done
  if [[ -z "${WASMEDGE_DIR:-}" ]]; then
    echo "flatsql-unified-e2e: no static WasmEdge prefix under ~/.sdn-wasmedge-static-macos-patched carries ${series}; build one (scripts/build-static-wasmedge.sh) or set WASMEDGE_DIR" >&2
    exit 1
  fi
  echo "flatsql-unified-e2e: WASMEDGE_DIR=$WASMEDGE_DIR (${series})"
fi
export WASMEDGE_DIR
export GOFLAGS="${GOFLAGS:--p=6}"
result="$(mktemp "${TMPDIR:-/tmp}/flatsql-wasmedge.XXXXXX")"
trap 'rm -f "$result"' EXIT
scripts/go-with-wasmedge.sh test ./internal/storage/format4 -run '^TestUnifiedFlatBufferQueryReopen$' -count=1 -v -timeout=5m 2>&1 | tee "$result"
grep -q '^--- PASS: TestUnifiedFlatBufferQueryReopen' "$result"
! grep -q '^--- SKIP:' "$result"

scripts/go-with-wasmedge.sh test ./internal/storage -run '^(TestUnifiedStorageCanonicalFieldsReopen|TestFormat4WritesMatchFormat1)$' -count=1 -v -timeout=3m 2>&1 | tee "$result"
grep -q '^--- PASS: TestUnifiedStorageCanonicalFieldsReopen' "$result"
grep -q '^--- PASS: TestFormat4WritesMatchFormat1' "$result"
! grep -q '^--- SKIP:' "$result"
