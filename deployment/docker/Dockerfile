# SDN Node
# Single container image. Defaults to a full node; operators can override the
# command to run the edge relay binary from the same image.

# ── WasmEdge, built STATIC with LLVM inside ───────────────────────────────────
# This layer depends only on WASMEDGE_VERSION, so it is built once and reused
# until that changes. Building LLVM + WasmEdge from source is slow; keeping it
# out of the source-dependent layers is the difference between a cheap rebuild
# and a very expensive one.
FROM golang:1.25-bookworm AS wasmedge-static

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates git cmake ninja-build \
    clang-16 llvm-16-dev liblld-16-dev libpolly-16-dev \
    libzstd-dev zlib1g-dev \
  && rm -rf /var/lib/apt/lists/*

WORKDIR /build
COPY scripts/build-static-wasmedge.sh ./scripts/build-static-wasmedge.sh
ARG WASMEDGE_VERSION=0.16.4
RUN WASMEDGE_VERSION="${WASMEDGE_VERSION}" \
    WASMEDGE_STATIC_WORK=/opt/wasmedge-static \
    WASMEDGE_STATIC_PREFIX_ONLY=1 \
    bash scripts/build-static-wasmedge.sh

FROM golang:1.25-bookworm AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    bash \
    build-essential \
    ca-certificates \
    curl \
    libsqlite3-dev \
    llvm-16-dev libpolly-16-dev libzstd-dev zlib1g-dev libtinfo-dev \
    pkg-config \
  && rm -rf /var/lib/apt/lists/*

WORKDIR /build

# The runtime comes in as ARCHIVES, not a shared library: nothing is installed
# into the image and the produced binary carries WasmEdge inside it.
COPY --from=wasmedge-static /opt/wasmedge-static/prefix /opt/wasmedge-static/prefix

# Deliberately NO CGO_CFLAGS/CGO_LDFLAGS here. go-with-wasmedge.sh builds both
# from scratch for a static prefix, and an inherited CGO_LDFLAGS pointing at a
# lib directory is precisely how a binary ends up resolving -lwasmedge to a
# shared object instead of the archive.

COPY sdn-server/go.mod sdn-server/go.sum ./sdn-server/
COPY sdn-server/third_party/spacedatastandards-go/go.mod ./sdn-server/third_party/spacedatastandards-go/go.mod
WORKDIR /build/sdn-server
RUN go mod download

WORKDIR /build
COPY scripts/go-with-wasmedge.sh ./scripts/go-with-wasmedge.sh
COPY sdn-server/ ./sdn-server/

ARG SDN_RELEASE_TAG=""
# The link line is NOT written here. build-static-wasmedge.sh leaves link.flags
# beside the archives naming every one it built, in the order ld needs, and
# go-with-wasmedge.sh consumes it — the same path every other build product
# takes. This file used to carry its own hand-written copy of that list; it went
# stale the moment the prefix gained an archive (it was missing
# libwasmedgePluginWasiLogging.a and every lld archive), and the image failed to
# link while the CLI archives built fine. One source of truth or none.
RUN set -eux; \
    WASMEDGE_DIR=/opt/wasmedge-static/prefix \
    CGO_ENABLED=1 \
    SDN_GO_LDFLAGS="-s -w -X github.com/spacedatanetwork/sdn-server/internal/versioninfo.ReleaseTag=${SDN_RELEASE_TAG}" \
    bash /build/scripts/go-with-wasmedge.sh build -o /out/spacedatanetwork ./cmd/spacedatanetwork; \
    cd /build/sdn-server; \
    CGO_ENABLED=0 go build -tags edge -ldflags="-s -w" -o /out/spacedatanetwork-edge ./cmd/spacedatanetwork-edge; \
    ldd /out/spacedatanetwork | grep -qi wasmedge && { echo "binary still links libwasmedge"; exit 1; } || true

FROM debian:bookworm-slim

# NO libsqlite3-0. It is a leftover from the go-sqlite era the FlatSQL
# migration ended: the record store is the WASM engine now, nothing in the
# image links libsqlite3 (verified against the published v1.0.5-beta.69 image
# — `ldd` over every executable found none, and apt-cache rdepends reported no
# reverse dependency), and the daemon itself needs only libm and libc.
# curl and wget stay: they are how an operator probes a node from inside the
# container.
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    wget \
  && rm -rf /var/lib/apt/lists/*

RUN useradd --create-home --uid 1000 --shell /usr/sbin/nologin sdn

WORKDIR /app

COPY --from=builder /out/spacedatanetwork /app/spacedatanetwork
COPY --from=builder /out/spacedatanetwork-edge /app/spacedatanetwork-edge
# The node dashboard + fonts are compiled INTO the binary
# (sdn-server/cmd/spacedatanetwork/embedded, //go:embed in conjunction_ui.go).
# There is deliberately no external admin-ui copy here: sdn-js/ui/dist no longer
# exists in the tree, and copying it is what used to break this image build.
COPY webui/build/ /app/webui/
COPY config/full-docker.yaml /app/config/full-docker.yaml

# The pure-WASI HD wallet. NOT optional: without it n.hdwallet is nil, so
# loadOrCreateIdentityBundle fails ("hd wallet not available"), the node falls
# back to a random node.key with NO mnemonic, and root admin sign-in is dead
# ("Node root admin sign-in unavailable: node identity bundle not loaded").
# /usr/local/lib is the last entry of the node's search list (node.go:1478).
COPY sdn-js/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm /usr/local/lib/hd-wallet-wasi.wasm

# Same-origin wallet sign-in assets. The dashboard's CSP is default-src 'self'
# and owner law 2026-07-27 is "we do NOT load anything from a site", so these
# must be served BY the node. They are staged from the pinned npm packages by
# deployment/wallet-wasm/stage-wallet-wasm.sh before the image is built.
# Both directories are tracked (via .gitkeep) so an unstaged tree still builds:
# absent assets 404 and the dashboard reports sign-in unavailable (main.go:1826).
COPY deployment/wallet-wasm/staged/wallet-wasm/ /app/wallet-wasm/
COPY deployment/wallet-wasm/staged/wallet-ui/ /app/wallet-ui/

RUN mkdir -p /app/data && chown -R sdn:sdn /app

ENV SDN_WEBUI_PATH=/app/webui
ENV HOME=/home/sdn
# The AOT cache belongs in the VOLUME, not the image and not a container-local
# home. It holds native code compiled for this host's CPU, so it must survive
# container recreation (or every new container pays the compile again) and must
# never be shared between hosts through the image.
ENV XDG_CACHE_HOME=/app/data/cache

USER sdn

# NOT prewarmed here. An AOT artifact is native code for the CPU that compiled
# it, and this image is meant to run anywhere: built on a runner with AVX-512,
# the baked artifact killed the node with "SIGILL: illegal instruction" inside
# WasmEdge_VMExecuteRegistered on any host without it. The entrypoint compiles
# on first start instead, for the CPU actually present, into the data volume.

EXPOSE 5001 4001 4001/udp 8080 8081

# Everything the node owns lives under /app/data: the record store, the keys
# (identity), auth, flows. One volume keeps the identity across container
# recreation. Set SDN_KEY_PASSWORD_FILE (a mounted secret) to decouple the
# key-at-rest password from the container's machine fingerprint.
VOLUME /app/data

COPY deployment/docker/entrypoint.sh /app/entrypoint.sh

ENTRYPOINT ["/app/entrypoint.sh"]
CMD ["daemon", "--config", "/app/config/full-docker.yaml"]
