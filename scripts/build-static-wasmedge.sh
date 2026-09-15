#!/usr/bin/env bash
# Build WasmEdge (default 0.16.4, override WASMEDGE_VERSION) as a STATIC
# library WITH LLVM linked in, and static-link the spacedatanetwork daemon into
# a single self-contained binary — the way kubo ships one file.
#
# The result depends only on base system libraries (libstdc++, libm, libgcc_s,
# libc). No ~/.wasmedge install, no WASMEDGE_DIR, no LD_LIBRARY_PATH, and
# nothing per-host that can drift from the version the binary was built against.
# That drift is not hypothetical: the AOT cache key includes the libwasmedge
# runtime version, a miss does not fail, and production has twice fallen back to
# the roughly 100x slower interpreter because a host's install did not match.
#
# AOT IS RETAINED, and that is the whole point of linking LLVM statically.
# An earlier revision of this script built with WASMEDGE_USE_LLVM=OFF on the
# premise that "the server runs modules interpreted". That is wrong for the
# FlatSQL engine, which loads a precompiled artifact and is ~100x slower
# without one. In 0.16.4 WASMEDGE_BUILD_AOT_RUNTIME is DEPRECATED AND ALIASED
# to WASMEDGE_USE_LLVM (CMakeLists.txt:69-72), so there is no load-only mode:
# turning LLVM off removes the ability to load an AOT artifact, not just to
# compile one. Verified on the built binary: `prewarm-aot` compiles both
# artifacts in-process and reports we0.16.4, the same cache key the fleet uses.
#
# Cost: the binary is ~200 MB because LLVM is inside it. The upstream shared
# library is 175 MB for exactly the same reason.
#
# Built with clang, not gcc: WasmEdge adds -Werror unconditionally
# (cmake/Helper.cmake:43-48) and gcc-12 fails its own sources on
# -Wmaybe-uninitialized (ast/component/component_type.cpp) and -Warray-bounds
# (executor/coredump.cpp). Upstream builds with clang.
#
# Produces: $OUT (default: sdn-server/spacedatanetwork-static).
#
# Requires: git, cmake, ninja, clang, and the STATIC LLVM archives —
# on Debian/Ubuntu: llvm-16-dev liblld-16-dev libpolly-16-dev libzstd-dev
# zlib1g-dev clang-16. libPolly.a ships separately and the final archive merge
# fails without it.
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
  CC="${CC:-clang-16}" CXX="${CXX:-clang++-16}" \
  cmake -S "$SRC" -B "$SRC/build" -G Ninja -DCMAKE_BUILD_TYPE=Release \
    -DWASMEDGE_USE_LLVM=ON \
    -DWASMEDGE_LINK_LLVM_STATIC=ON \
    -DWASMEDGE_BUILD_SHARED_LIB=OFF \
    -DWASMEDGE_BUILD_STATIC_LIB=ON \
    -DWASMEDGE_BUILD_TOOLS=OFF \
    -DWASMEDGE_BUILD_PLUGINS=OFF \
    -DLLVM_DIR="${LLVM_DIR:-/usr/lib/llvm-16/lib/cmake/llvm}" \
    -DLLD_DIR="${LLD_DIR:-/usr/lib/llvm-16/lib/cmake/lld}"
  cmake --build "$SRC/build" -j"$(nproc)"
fi

# 3. Stage archives + headers. The merged libwasmedge.a is INCOMPLETE (the
#    component-model loader lives in the sublibraries), so we link the full set
#    inside a --start-group; ld pulls only the members it needs, no duplicates.
rm -rf "$STATIC"; mkdir -p "$STATIC/lib" "$STATIC/include"
find "$SRC/build" -name "libwasmedge*.a" -exec cp {} "$STATIC/lib/" \;
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
for a in Common Loader LoaderFileMgr Validator Executor VM HostModuleWasi PO Driver System Plugin AOT LLVM; do
  [[ -f "$STATIC/lib/libwasmedge${a}.a" ]] && GRP="$GRP $STATIC/lib/libwasmedge${a}.a"
done
GRP="$GRP $STATIC/lib/libspdlog.a $STATIC/lib/libfmt.a"

# LLVM's own archives, in the order llvm-config gives them.
LLVM_CONFIG="${LLVM_CONFIG:-llvm-config-16}"
LLVMLIBS="$($LLVM_CONFIG --link-static --libfiles | tr '\n' ' ')"

cd "$ROOT/sdn-server"
CGO_ENABLED=1 \
CGO_CFLAGS="-I$STATIC/include" \
CGO_LDFLAGS="-L$STATIC/lib" \
go build -ldflags "-linkmode external -extldflags \"-Wl,--start-group $GRP $LLVMLIBS -lstdc++ -Wl,--end-group -lm -ldl -lpthread -lz -lzstd -ltinfo\"" \
  -o "$OUT" ./cmd/spacedatanetwork

echo "built: $OUT"
if ldd "$OUT" 2>/dev/null | grep -qi wasmedge; then
  echo "WARNING: binary still references libwasmedge (not self-contained)" >&2
  exit 1
fi
echo "self-contained OK (no libwasmedge dependency):"
ldd "$OUT" || true
