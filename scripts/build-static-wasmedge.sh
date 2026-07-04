#!/usr/bin/env bash
# Build WasmEdge (default 0.16.4, override WASMEDGE_VERSION) as a STATIC,
# no-AOT library and static-link the spacedatanetwork daemon into a single
# self-contained binary.
#
# NOTE (loop C.9): 0.16.4 is the verified runtime — it fixes BOTH libwasmedge
# 0.14 AOT defects (nested AOT-in-AOT executor-state corruption, C.5b; and
# the linked-drain false trap, C.7), so engine-linked flow mounts run AOT
# there. Static no-LLVM builds cannot AOT-compile: deploys must stage
# precompiled cache artifacts named
# <prefix>-<sha256[:8]>-we<runtime-version>.aot.wasm — the cache key includes
# the runtime version, so artifacts compiled under 0.14.0 are ignored (and
# pruned) by a 0.16.4 daemon.
#
# The default build (scripts/go-with-wasmedge.sh) dynamically links
# libwasmedge.so, which requires a WasmEdge install (~/.wasmedge or
# /opt/.../.wasmedge) present on every host at runtime. This script instead
# embeds WasmEdge into the binary so it ships as one file with no local
# WasmEdge install required. AOT/LLVM is disabled because the server runs
# modules interpreted (internal/wasmrt configures only THREADS,
# EXCEPTION_HANDLING, WASI — no Compiler/AOT), which keeps the static lib
# LLVM-free and small (~10 MB).
#
# Produces: $OUT (default: sdn-server/spacedatanetwork-static).
# ldd on the result shows only libstdc++/libm/libgcc_s/libc — no libwasmedge.
#
# Requires: git, cmake, a C++17 compiler (g++), Go with CGO. Linux/amd64.
set -euo pipefail

WASMEDGE_VERSION="${WASMEDGE_VERSION:-0.16.4}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="${WASMEDGE_STATIC_WORK:-${ROOT}/.wasmedge-static-build}"
SRC="${WORK}/WasmEdge"
STATIC="${WORK}/prefix"          # holds lib/*.a + include/wasmedge/*.h
OUT="${OUT:-${ROOT}/sdn-server/spacedatanetwork-static}"

mkdir -p "$WORK"

# 1. WasmEdge source
if [[ ! -d "$SRC" ]]; then
  git clone --branch "$WASMEDGE_VERSION" --depth 1 --recursive \
    https://github.com/WasmEdge/WasmEdge.git "$SRC"
fi

# 2. Configure + build the static library (LLVM/AOT OFF).
if [[ ! -f "$SRC/build/lib/api/libwasmedge.a" ]]; then
  cmake -S "$SRC" -B "$SRC/build" -DCMAKE_BUILD_TYPE=Release \
    -DWASMEDGE_USE_LLVM=OFF \
    -DWASMEDGE_BUILD_AOT_RUNTIME=OFF \
    -DWASMEDGE_BUILD_SHARED_LIB=OFF \
    -DWASMEDGE_BUILD_STATIC_LIB=ON \
    -DWASMEDGE_BUILD_TOOLS=OFF \
    -DWASMEDGE_BUILD_PLUGINS=OFF
  cmake --build "$SRC/build" -j"$(nproc)"
fi

# 3. Stage archives + headers. The merged libwasmedge.a is INCOMPLETE (the
#    component-model loader lives in the sublibraries), so we link the full set
#    inside a --start-group; ld pulls only the members it needs, no duplicates.
rm -rf "$STATIC"; mkdir -p "$STATIC/lib" "$STATIC/include"
cp "$SRC"/build/lib/*/libwasmedge*.a          "$STATIC/lib/"
cp "$SRC"/build/lib/host/wasi/*.a             "$STATIC/lib/" 2>/dev/null || true
cp "$SRC"/build/_deps/fmt-build/libfmt.a      "$STATIC/lib/"
cp "$SRC"/build/_deps/spdlog-build/libspdlog.a "$STATIC/lib/"
cp -r "$SRC"/include/api/wasmedge             "$STATIC/include/"
cp -r "$SRC"/build/include/api/wasmedge/*     "$STATIC/include/wasmedge/" 2>/dev/null || true

# 4. Build the daemon. -extldflags places the C++ runtime + WasmEdge archives
#    LAST on the external link line (after libwasmedge.a from the binding's
#    -lwasmedge), so static symbols resolve. libstdc++/libc stay dynamic — they
#    are base system libraries present on every Ubuntu host.
GRP="$STATIC/lib/libwasmedge.a"
for a in Common Loader LoaderFileMgr Validator Executor VM HostModuleWasi PO Driver System Plugin; do
  [[ -f "$STATIC/lib/libwasmedge${a}.a" ]] && GRP="$GRP $STATIC/lib/libwasmedge${a}.a"
done
GRP="$GRP $STATIC/lib/libspdlog.a $STATIC/lib/libfmt.a"

cd "$ROOT/sdn-server"
CGO_ENABLED=1 \
CGO_CFLAGS="-I$STATIC/include" \
CGO_LDFLAGS="-L$STATIC/lib" \
go build -ldflags "-linkmode external -extldflags \"-Wl,--start-group $GRP -lstdc++ -Wl,--end-group -lm -ldl -lpthread\"" \
  -o "$OUT" ./cmd/spacedatanetwork

echo "built: $OUT"
if ldd "$OUT" 2>/dev/null | grep -qi wasmedge; then
  echo "WARNING: binary still references libwasmedge (not self-contained)" >&2
  exit 1
fi
echo "self-contained OK (no libwasmedge dependency):"
ldd "$OUT" || true
