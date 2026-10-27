#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
export SDN_WASM_REQUIRE_PATCHED=1
export WASMEDGE_DIR="${WASMEDGE_DIR:-$HOME/.sdn-wasmedge-static-macos-patched/prefix}"
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
