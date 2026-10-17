#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../../.."
export WASMEDGE_DIR="${WASMEDGE_DIR:-$HOME/.sdn-wasmedge-static-macos-patched/prefix}"
e2e_dir="$(mktemp -d "${TMPDIR:-/tmp}/wxf-ingest-amplification-e2e.XXXXXX")"
trap 'rm -f "$e2e_dir/wxf-ingest-amplification-e2e"; rmdir "$e2e_dir"' EXIT
./scripts/go-with-wasmedge.sh build -o "$e2e_dir/wxf-ingest-amplification-e2e" ./cmd/wxf-ingest-amplification-e2e
"$e2e_dir/wxf-ingest-amplification-e2e" "$@"
