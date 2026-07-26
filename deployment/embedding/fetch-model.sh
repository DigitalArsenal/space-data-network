#!/usr/bin/env bash
# fetch-model.sh — stage the dashboard semantic-search assets (/embedding/*).
#
# The node status dashboard's embedding search (graph task
# nst-dashboard-table) runs a quantized all-MiniLM-L6-v2 sentence-embedding
# model fully in-browser via onnxruntime-web's WASM backend. Everything it
# loads at runtime is served SAME-ORIGIN by the node from
# config embedding.assets_dir (default <data-dir>/embedding) — this script
# stages that directory, the same staged-file pattern as
# deployment/geoip/fetch-geolite2.sh. Absent assets fail open: the dashboard
# keeps substring search.
#
# Assets:
#   model.onnx  — sentence-transformers/all-MiniLM-L6-v2, int8-quantized ONNX
#                 (Xenova/all-MiniLM-L6-v2 onnx/model_quantized.onnx,
#                  Apache-2.0)
#   vocab.txt   — the model's BERT WordPiece vocabulary (same repo)
#   ort-wasm-simd-threaded.wasm/.mjs — onnxruntime-web runtime artifacts,
#                 copied from sdn-js/node_modules/onnxruntime-web/dist so the
#                 served runtime EXACTLY matches the ort JS API bundled into
#                 the dashboard page (never fetch these from a CDN).
#
# Usage:
#   deployment/embedding/fetch-model.sh [dest-dir]   # default: this directory
#
# Prints each asset's sha256; record them in the deploy log on every re-fetch.

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
DEST="${1:-$HERE}"
REPO_ROOT="$(cd "$HERE/../.." && pwd)"
ORT_DIST="$REPO_ROOT/sdn-js/node_modules/onnxruntime-web/dist"
HF="https://huggingface.co/Xenova/all-MiniLM-L6-v2/resolve/main"

mkdir -p "$DEST"

fetch() {
  local url="$1" out="$2"
  echo "[embedding] fetching $(basename "$out")"
  curl -fsSL --retry 3 -o "$out.tmp" "$url"
  mv "$out.tmp" "$out"
}

fetch "$HF/onnx/model_quantized.onnx" "$DEST/model.onnx"
fetch "$HF/vocab.txt" "$DEST/vocab.txt"

for f in ort-wasm-simd-threaded.wasm ort-wasm-simd-threaded.mjs; do
  if [ ! -f "$ORT_DIST/$f" ]; then
    echo "[embedding] ERROR: $ORT_DIST/$f missing — run 'npm ci' in sdn-js first" >&2
    exit 1
  fi
  cp "$ORT_DIST/$f" "$DEST/$f"
done

echo "[embedding] staged in $DEST:"
for f in model.onnx vocab.txt ort-wasm-simd-threaded.wasm ort-wasm-simd-threaded.mjs; do
  SHA="$(shasum -a 256 "$DEST/$f" | cut -d' ' -f1)"
  SIZE="$(du -h "$DEST/$f" | cut -f1)"
  printf '[embedding]   %-28s %s  %s\n' "$f" "$SHA" "$SIZE"
done
echo "[embedding] fetched: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
