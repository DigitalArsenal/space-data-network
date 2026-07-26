#!/usr/bin/env bash
# stage-wallet-wasm.sh — stage the dashboard wallet sign-in assets (/wallet-wasm/*).
#
# The node status dashboard signs an Ed25519 auth challenge with a key derived
# in-browser by hd-wallet-wasm (graph task nst-node-admin-contract). The
# dashboard's CSP is default-src 'self', so every byte of the wallet — the
# emscripten loader, the runtime ES modules it imports relatively, and the
# standalone WASI artifact — is served SAME-ORIGIN by the node from config
# wallet_wasm.assets_dir (default <data-dir>/wallet-wasm). This script stages
# that directory, the same staged-file pattern as
# deployment/embedding/fetch-model.sh and deployment/geoip/fetch-geolite2.sh.
# Absent assets fail open: /wallet-wasm/* 404s and the dashboard reports
# sign-in as unavailable rather than reaching a CDN.
#
# Everything is COPIED from sdn-js/node_modules/hd-wallet-wasm/dist so the
# served wallet EXACTLY matches the pinned npm version (sdn-js/package.json)
# the contract was written against. Never fetch these from a CDN.
#
# The package layout is mirrored verbatim, because the modules import each
# other by relative path:
#   hd-wallet.js                  loader (wasm inlined as a data URI)
#   hd-wallet-wasi.wasm           standalone WASI artifact
#   runtime/index.mjs             ENTRY POINT — /wallet-wasm/runtime/index.mjs
#   runtime/*.mjs                 modules index.mjs imports (./aligned.mjs, …)
#   runtime/generated/**/*.mjs    generated struct definitions
#
# Usage:
#   deployment/wallet-wasm/stage-wallet-wasm.sh [dest-dir]   # default: this directory
#
# Prints each asset's sha256; record them in the deploy log on every re-stage.

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
DEST="${1:-$HERE}"
REPO_ROOT="$(cd "$HERE/../.." && pwd)"
WALLET_PKG="$REPO_ROOT/sdn-js/node_modules/hd-wallet-wasm"
WALLET_DIST="$WALLET_PKG/dist"

if [ ! -d "$WALLET_DIST" ]; then
  echo "[wallet-wasm] ERROR: $WALLET_DIST missing — run 'npm ci' in sdn-js first" >&2
  exit 1
fi

VERSION="$(node -e 'process.stdout.write(require(process.argv[1]).version)' "$WALLET_PKG/package.json" 2>/dev/null || echo unknown)"

mkdir -p "$DEST"

# Only the file types the node's /wallet-wasm/ handler will serve
# (walletWasmAssetExts in sdn-server/cmd/spacedatanetwork/conjunction_ui.go).
# .d.ts type declarations are build-time only and are deliberately not staged.
( cd "$WALLET_DIST" && find . \( -name '*.js' -o -name '*.mjs' -o -name '*.wasm' \) -type f -print0 ) |
  while IFS= read -r -d '' rel; do
    rel="${rel#./}"
    mkdir -p "$DEST/$(dirname "$rel")"
    cp "$WALLET_DIST/$rel" "$DEST/$rel"
  done

echo "[wallet-wasm] staged hd-wallet-wasm@$VERSION in $DEST:"
( cd "$DEST" && find . \( -name '*.js' -o -name '*.mjs' -o -name '*.wasm' \) -type f -print0 ) |
  while IFS= read -r -d '' rel; do
    rel="${rel#./}"
    SHA="$(shasum -a 256 "$DEST/$rel" | cut -d' ' -f1)"
    SIZE="$(du -h "$DEST/$rel" | cut -f1)"
    printf '[wallet-wasm]   %-44s %s  %s\n' "$rel" "$SHA" "$SIZE"
  done

if [ ! -f "$DEST/runtime/index.mjs" ] || [ ! -f "$DEST/hd-wallet.js" ]; then
  echo "[wallet-wasm] ERROR: staged tree is missing runtime/index.mjs or hd-wallet.js" >&2
  exit 1
fi

echo "[wallet-wasm] entry point: /wallet-wasm/runtime/index.mjs"
echo "[wallet-wasm] staged: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
