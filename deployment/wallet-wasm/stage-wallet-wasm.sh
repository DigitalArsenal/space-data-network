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
# It also stages hd-wallet-ui (the wallet sign-in EXPERIENCE the dashboard
# mounts in-page) into a sibling directory served at /wallet-ui/*:
#   compat/index.js               thin controller: createWalletUI()/init()
#   wallet-origin/index.js        the wallet application itself
#   client/style.css              reference stylesheet (the dashboard overrides it)
#
# OWNER LAW 2026-07-27: "we do NOT load anything from a site." hd-wallet-ui also
# ships a registered-site client (dist/client, dist/client/sdn) that opens
# https://wallet.spacedatanetwork.org; that flow is INADMISSIBLE and its modules
# are deliberately NOT staged. dist/wallet-origin-host (a standalone wallet
# DOCUMENT) is also not staged — serving operator-staged HTML from this origin
# is a separate decision (see §11 of graph/tasks/nst-node-admin-contract.md).
#
# The two packages MUST be the same version: hd-wallet-ui's modules import the
# bare specifier "hd-wallet-wasm", which the dashboard resolves via an import
# map to /wallet-wasm/runtime/index.mjs. Staging a mismatch would silently pair
# a UI with a different wallet core, so this script refuses to.
#
# Usage:
#   deployment/wallet-wasm/stage-wallet-wasm.sh [dest-dir] [ui-dest-dir]
#     defaults: this directory, and <this directory>/../wallet-ui
#
# Prints each asset's sha256; record them in the deploy log on every re-stage.

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
DEST="${1:-$HERE}"
UI_DEST="${2:-$HERE/../wallet-ui}"
REPO_ROOT="$(cd "$HERE/../.." && pwd)"
# SDN_WALLET_NODE_MODULES names the node_modules tree holding the pinned
# hd-wallet-wasm + hd-wallet-ui packages (the release workflow installs them
# at the repo root; developers have them under sdn-js).
WALLET_NODE_MODULES="${SDN_WALLET_NODE_MODULES:-$REPO_ROOT/sdn-js/node_modules}"
WALLET_PKG="$WALLET_NODE_MODULES/hd-wallet-wasm"
WALLET_DIST="$WALLET_PKG/dist"
UI_PKG="$WALLET_NODE_MODULES/hd-wallet-ui"
UI_DIST="$UI_PKG/dist"

if [ ! -d "$WALLET_DIST" ]; then
  echo "[wallet-wasm] ERROR: $WALLET_DIST missing — run 'npm ci' in sdn-js first" >&2
  exit 1
fi
if [ ! -d "$UI_DIST" ]; then
  echo "[wallet-wasm] ERROR: $UI_DIST missing — run 'npm ci' in sdn-js first" >&2
  exit 1
fi

pkg_version() {
  node -e 'process.stdout.write(require(process.argv[1]).version)' "$1/package.json" 2>/dev/null || echo unknown
}
VERSION="$(pkg_version "$WALLET_PKG")"
UI_VERSION="$(pkg_version "$UI_PKG")"

if [ "$VERSION" = "unknown" ] || [ "$UI_VERSION" = "unknown" ]; then
  echo "[wallet-wasm] ERROR: could not read package versions" >&2
  exit 1
fi
if [ "$VERSION" != "$UI_VERSION" ]; then
  echo "[wallet-wasm] ERROR: version mismatch — hd-wallet-wasm@$VERSION vs hd-wallet-ui@$UI_VERSION." >&2
  echo "[wallet-wasm]        The UI imports the bare specifier 'hd-wallet-wasm', resolved by import map" >&2
  echo "[wallet-wasm]        to /wallet-wasm/runtime/index.mjs. Staging a mismatch pairs a UI with a" >&2
  echo "[wallet-wasm]        different wallet core. Align the pins in sdn-js/package.json first." >&2
  exit 1
fi

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

# ---------------------------------------------------------------------------
# hd-wallet-ui -> /wallet-ui/*
# ---------------------------------------------------------------------------
mkdir -p "$UI_DEST"

# Only the in-page surfaces. dist/client/* (registered-site relay to
# wallet.spacedatanetwork.org) and dist/browser/* + dist/wallet-origin-host/*
# are deliberately excluded per the owner law quoted above.
for rel in compat/index.js wallet-origin/index.js client/style.css; do
  if [ ! -f "$UI_DIST/$rel" ]; then
    echo "[wallet-ui] ERROR: $UI_DIST/$rel missing — unexpected hd-wallet-ui@$UI_VERSION layout" >&2
    exit 1
  fi
  mkdir -p "$UI_DEST/$(dirname "$rel")"
  cp "$UI_DIST/$rel" "$UI_DEST/$rel"
done

# Owner law 2026-07-27: "we do NOT load anything from a site." Enforced on what
# was actually staged.
#
# What the audit of hd-wallet-ui@2.0.28 found, so the next auditor starts here:
#   * The in-page app performs NO cross-document or push egress: no window.open,
#     no postMessage, no WebSocket/EventSource/sendBeacon/XHR/importScripts, and
#     it injects no <script>. This guard keeps that true across upgrades.
#   * It DOES use fetch — but only through its DEFAULT relay, and only against
#     the RELATIVE paths /relay/v1/transactions/{id}[/result]. Relative means
#     same-origin, i.e. this node. The node deliberately does not implement that
#     endpoint (a transaction store with result tokens is application logic, and
#     this host is connectors only), so the dashboard MUST inject its own relay;
#     doing so means the default is never constructed and no fetch ever happens.
#   * It contains absolute https:// URLs as DATA — the baked client registry's
#     requestOrigin/callbackUri values, and a WebAuthn rp.id. Data, not loads.
for rel in compat/index.js wallet-origin/index.js; do
  if grep -Eq 'window\.open|postMessage\(|XMLHttpRequest|importScripts|EventSource|WebSocket|sendBeacon|createElement\(["'"'"']script' "$UI_DEST/$rel"; then
    echo "[wallet-ui] ERROR: staged module $rel contains a cross-document or push-egress primitive." >&2
    echo "[wallet-ui]        Owner law 2026-07-27 forbids loading anything from a site." >&2
    echo "[wallet-ui]        Re-audit this hd-wallet-ui version before staging it." >&2
    exit 1
  fi
done

# The relay must remain RELATIVE-only. An absolute relay base would be an
# external load no matter what the dashboard injects.
if ! grep -q '/relay/v1/transactions/' "$UI_DEST/wallet-origin/index.js"; then
  echo "[wallet-ui] ERROR: relay transaction path not found — the app's transport changed shape." >&2
  echo "[wallet-ui]        Re-audit before staging." >&2
  exit 1
fi
if grep -Eq 'https?://[A-Za-z0-9.-]+/relay/v1' "$UI_DEST/wallet-origin/index.js"; then
  echo "[wallet-ui] ERROR: the relay uses an ABSOLUTE base URL — that is an external load." >&2
  exit 1
fi

# Absolute URLs that are present as data. Printed, never silently accepted, so
# an upgrade that turns one of them into a load is visible in the deploy log.
echo "[wallet-ui] NOTE: absolute URLs present as DATA (registry origins + WebAuthn rp.id), not loaded:"
grep -o 'https\?://[A-Za-z0-9./-]*' "$UI_DEST/wallet-origin/index.js" | sort -u |
  while IFS= read -r url; do printf '[wallet-ui]         %s\n' "$url"; done
echo "[wallet-ui] NOTE: WebAuthn rp.id is wallet.spacedatanetwork.org — the passkey 'remember me'"
echo "[wallet-ui]       path is INERT on a node origin. The dashboard injects no credentials capability."

echo "[wallet-ui] staged hd-wallet-ui@$UI_VERSION in $UI_DEST:"
( cd "$UI_DEST" && find . \( -name '*.js' -o -name '*.mjs' -o -name '*.css' -o -name '*.wasm' \) -type f -print0 ) |
  while IFS= read -r -d '' rel; do
    rel="${rel#./}"
    SHA="$(shasum -a 256 "$UI_DEST/$rel" | cut -d' ' -f1)"
    SIZE="$(du -h "$UI_DEST/$rel" | cut -f1)"
    printf '[wallet-ui]   %-44s %s  %s\n' "$rel" "$SHA" "$SIZE"
  done

echo "[wallet-wasm] entry point: /wallet-wasm/runtime/index.mjs"
echo "[wallet-ui]   entry point: /wallet-ui/compat/index.js  (import map: hd-wallet-wasm -> /wallet-wasm/runtime/index.mjs)"
echo "[wallet-wasm] staged: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
