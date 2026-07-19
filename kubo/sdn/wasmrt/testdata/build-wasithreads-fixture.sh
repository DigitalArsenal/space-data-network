#!/usr/bin/env bash
# Reproducibly (re)build wasithreads_fixture.wasm from wasithreads_fixture.c
# using a hermetic wasi-sdk in Docker. NEVER build on prod. Run from anywhere:
#
#   ./build-wasithreads-fixture.sh
#
# The fixture is the proof artifact for the wasmrt wasi-threads host: a generic
# pthreads program compiled for the wasm32-wasip1-threads target with an
# IMPORTED shared memory (env.memory) — the exact shape a real wasi-threads
# module (e.g. the OD flow module) presents to the runtime.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WASI_SDK_VER="${WASI_SDK_VER:-24}"
WASI_SDK_MAJOR="${WASI_SDK_MAJOR:-24.0}"
# wasi-sdk arch tag: arm64|x86_64 linux. Match your Docker host.
SDKARCH="${SDKARCH:-arm64}"
PLATFORM="${PLATFORM:-linux/${SDKARCH/x86_64/amd64}}"

docker buildx build --platform "$PLATFORM" -o "type=local,dest=$HERE" - "$HERE" <<DOCKER
FROM debian:bookworm-slim AS build
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates xz-utils && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL "https://github.com/WebAssembly/wasi-sdk/releases/download/wasi-sdk-${WASI_SDK_VER}/wasi-sdk-${WASI_SDK_MAJOR}-${SDKARCH}-linux.tar.gz" -o /tmp/wasi.tgz \
 && mkdir -p /opt/wasi-sdk && tar -C /opt/wasi-sdk --strip-components=1 -xzf /tmp/wasi.tgz
WORKDIR /w
COPY wasithreads_fixture.c .
# wasm32-wasip1-threads + -pthread auto-adds --shared-memory and links wasi-libc
# pthreads. --import-memory makes the shared memory IMPORTED (env.memory) so the
# host owns the single shared instance every thread instance links against.
RUN /opt/wasi-sdk/bin/clang \
      --target=wasm32-wasip1-threads \
      --sysroot=/opt/wasi-sdk/share/wasi-sysroot \
      -pthread -O2 -mexec-model=reactor \
      -Wl,--export=run,--export=get_marker,--export=get_peak,--export=get_joined \
      -Wl,--import-memory,--export-memory,--max-memory=1073741824 \
      wasithreads_fixture.c -o wasithreads_fixture.wasm
FROM scratch AS out
COPY --from=build /w/wasithreads_fixture.wasm /
DOCKER
echo "rebuilt $HERE/wasithreads_fixture.wasm"
shasum -a256 "$HERE/wasithreads_fixture.wasm" 2>/dev/null || sha256sum "$HERE/wasithreads_fixture.wasm"
