#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
export WASMEDGE_DIR="${WASMEDGE_DIR:-$HOME/.local/share/spacedatanetwork/wasmedge-sdk/0.16.4-darwin-arm64}"
exec "$ROOT/scripts/go-with-wasmedge.sh" run ./cmd/cron-robustness-e2e
