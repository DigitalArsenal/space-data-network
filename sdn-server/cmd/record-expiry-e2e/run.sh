#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../../.."
# The release runtime, including the thread-stop and AOT fixes. Callers on
# other hosts can supply their own patched prefix.
export WASMEDGE_DIR="${WASMEDGE_DIR:-$HOME/.sdn-wasmedge-static-macos-patched/prefix}"
# The shared wrapper adds the static runtime's link flags to build/test;
# go run does not use that branch. Build the E2E with the release link path.
e2e_dir="$(mktemp -d "${TMPDIR:-/tmp}/record-expiry-e2e.XXXXXX")"
trap 'rm -f "$e2e_dir/record-expiry-e2e"; rmdir "$e2e_dir"' EXIT
./scripts/go-with-wasmedge.sh build -o "$e2e_dir/record-expiry-e2e" ./cmd/record-expiry-e2e
"$e2e_dir/record-expiry-e2e" "$@"
