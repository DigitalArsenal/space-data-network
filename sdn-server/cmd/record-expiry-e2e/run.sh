#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../../.."
# The release runtime, including the thread-stop and AOT fixes. Callers on
# other hosts can supply their own patched prefix.
export WASMEDGE_DIR="${WASMEDGE_DIR:-$HOME/.sdn-wasmedge-static-macos-patched/prefix}"
exec ./scripts/go-with-wasmedge.sh run ./cmd/record-expiry-e2e "$@"
