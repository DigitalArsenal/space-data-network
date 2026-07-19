#!/usr/bin/env bash
# build-threads-bits.sh — produce the toolchain-v2 wasi-threads bake bits into a
# staged flowcc home, reproducibly, via the wasi-sdk-24 Docker image (the SAME
# sdk the OD/provider module guest-links are built with — ABI-exact).
#
# It writes two things into <home>:
#   template/flow_runtime.threads.o   the flow-agnostic flow runtime, natively
#                                     compiled for wasm32-wasip1-threads
#                                     (flow_runtime.cpp + flow_noexcept_stubs.cpp,
#                                     wasm-ld -r). The box's clang-16 CANNOT
#                                     compile flow_runtime.cpp's libc++ includes
#                                     against wasi-sdk-24 headers, so this object
#                                     ships as staged data (isomorphic: the SAME
#                                     prebuilt .o is used on server AND client;
#                                     only the per-flow descriptor.o + the LINK
#                                     stay in the box).
#   sysroot-wasi-threads/             the wasi-sdk-24 wasm32-wasip1-threads
#                                     sysroot (include + lib) WITH the box's
#                                     clang-16 builtin headers (stddef.h/stdarg.h
#                                     etc.) grafted in, so the box compiles the
#                                     (libc++-free) per-flow descriptor.cpp for
#                                     the threads target.
#
# Usage:
#   build-threads-bits.sh --home <staged-home> --kubo <kubo-repo-root>
#     [--wasi-image ghcr.io/webassembly/wasi-sdk:wasi-sdk-24]
#
#   --home  A staged flowcc home (has sysroot/ = the emscripten sysroot, whose
#           top-level include/*.h are the box's clang-16 builtin headers we graft)
#           + template/. This script ADDS the v2 bits to it in place.
#   --kubo  The kubo repo root (for sdn/flowcc/runtime-src/{flow_runtime.cpp,
#           flow_noexcept_stubs.cpp,flow_descriptor_abi.h} + toolchain/template).
set -euo pipefail

HOME_DIR=""; KUBO=""; WASI_IMAGE="ghcr.io/webassembly/wasi-sdk:wasi-sdk-24"
while [ $# -gt 0 ]; do
  case "$1" in
    --home) HOME_DIR="$2"; shift 2 ;;
    --kubo) KUBO="$2"; shift 2 ;;
    --wasi-image) WASI_IMAGE="$2"; shift 2 ;;
    -h|--help) sed -n '2,40p' "$0"; exit 0 ;;
    *) echo "build-threads-bits.sh: unknown arg: $1" >&2; exit 2 ;;
  esac
done
[ -n "$HOME_DIR" ] || { echo "--home required" >&2; exit 2; }
[ -n "$KUBO" ] || { echo "--kubo required" >&2; exit 2; }
RTSRC="$KUBO/sdn/flowcc/runtime-src"
TPL="$KUBO/sdn/flowcc/toolchain/template"
EM_SYSROOT="$HOME_DIR/sysroot"
[ -f "$RTSRC/flow_runtime.cpp" ] || { echo "missing $RTSRC/flow_runtime.cpp" >&2; exit 4; }
[ -f "$RTSRC/flow_noexcept_stubs.cpp" ] || { echo "missing $RTSRC/flow_noexcept_stubs.cpp" >&2; exit 4; }
[ -d "$EM_SYSROOT/include" ] || { echo "missing emscripten sysroot include at $EM_SYSROOT (needed for the builtin-header graft)" >&2; exit 4; }

echo "==> (1) native flow_runtime.threads.o via $WASI_IMAGE" >&2
docker run --rm -v "$KUBO":/kubo -v "$HOME_DIR":/home ghcr.io/webassembly/wasi-sdk:wasi-sdk-24 bash -lc '
set -e
CC=/opt/wasi-sdk/bin/clang++
CFLAGS="--target=wasm32-wasip1-threads -std=c++17 -O3 -fignore-exceptions -fno-rtti -fvisibility=hidden -pthread -matomics -mbulk-memory -DNDEBUG"
$CC $CFLAGS -c /kubo/sdn/flowcc/runtime-src/flow_runtime.cpp -I/kubo/sdn/flowcc/runtime-src -I/kubo/sdn/flowcc/toolchain/template -o /tmp/frt.o
$CC $CFLAGS -c /kubo/sdn/flowcc/runtime-src/flow_noexcept_stubs.cpp -o /tmp/stubs.o
/opt/wasi-sdk/bin/wasm-ld -r /tmp/frt.o /tmp/stubs.o -o /home/template/flow_runtime.threads.o
' 
echo "    -> $HOME_DIR/template/flow_runtime.threads.o ($(wc -c < "$HOME_DIR/template/flow_runtime.threads.o") bytes)" >&2

echo "==> (2) extract wasm32-wasip1-threads sysroot from $WASI_IMAGE" >&2
WT="$HOME_DIR/sysroot-wasi-threads"; rm -rf "$WT"; mkdir -p "$WT/lib"
CID="$(docker create "$WASI_IMAGE")"
docker cp "$CID:/opt/wasi-sdk/share/wasi-sysroot/include" "$WT/" >/dev/null
docker cp "$CID:/opt/wasi-sdk/share/wasi-sysroot/lib/wasm32-wasip1-threads" "$WT/lib/" >/dev/null
docker rm "$CID" >/dev/null

echo "==> (3) graft the box's clang-16 builtin headers into the threads sysroot" >&2
# The wasi-sdk libc++ stddef/stdarg wrappers #include_next the compiler builtin
# header; the box's clang-16 provides its builtins via the emscripten sysroot's
# TOP-LEVEL include/*.h. Graft those so the box resolves the chain for the
# descriptor.cpp compile.
for h in iso646.h stdalign.h stdarg.h stdbool.h stddef.h stdnoreturn.h tgmath.h \
         __stddef_max_align_t.h; do
  [ -f "$EM_SYSROOT/include/$h" ] && cp "$EM_SYSROOT/include/$h" "$WT/include/$h" || true
done
echo "    grafted builtins into $WT/include" >&2
echo "OK. toolchain-v2 bits written into $HOME_DIR (sysroot-wasi-threads/ + template/flow_runtime.threads.o)" >&2
