#!/usr/bin/env bash
# Rebuild testdata/ps-probe.wasm from ps-probe.c with the same wasi-threads
# toolchain the partition-store engine uses (clang --target
# wasm32-wasip1-threads, shared imported env.memory, max 256 MiB here).
#
# Toolchain: wasi-sdk, or Homebrew `wasi-libc` + `wasi-runtimes` + llvm. The
# SDN_WASI_* overrides match space-data-module-sdk's wasiThreadsToolchain.js.
# The committed ps-probe.wasm is built with wasi-sdk 25 in its container:
#   docker run --rm -v "$PWD":/w -w /w \
#     -e SDN_WASI_CLANG=/opt/wasi-sdk/bin/clang \
#     -e SDN_WASI_SYSROOT=/opt/wasi-sdk/share/wasi-sysroot \
#     ghcr.io/webassembly/wasi-sdk:wasi-sdk-25 bash build-ps-probe.sh
set -euo pipefail
cd "$(dirname "$0")"
CLANG="${SDN_WASI_CLANG:-wasm32-wasi-clang}"
SYSROOT="${SDN_WASI_SYSROOT:-/opt/homebrew/share/wasi-sysroot}"
RESOURCE_DIR="${SDN_WASI_RESOURCE_DIR:-/opt/homebrew/share/wasi-runtimes}"
args=(--target=wasm32-wasip1-threads --sysroot="$SYSROOT")
[[ -d "$RESOURCE_DIR" ]] && args+=(-resource-dir="$RESOURCE_DIR")
"$CLANG" "${args[@]}" -O2 -pthread -matomics -mbulk-memory -mexec-model=reactor \
  -fno-exceptions -Wall -Wextra -Werror -Wno-unused-parameter \
  -Wl,--import-memory -Wl,--shared-memory -Wl,--max-memory=268435456 \
  -Wl,--export=malloc -Wl,--export=free -Wl,--strip-all \
  -o ps-probe.wasm ps-probe.c
shasum -a 256 ps-probe.wasm
