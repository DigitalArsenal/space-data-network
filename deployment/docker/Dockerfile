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

ENV CGO_CFLAGS="-I/opt/wasmedge-static/prefix/include"
ENV CGO_LDFLAGS="-L/opt/wasmedge-static/prefix/lib"

COPY sdn-server/go.mod sdn-server/go.sum ./sdn-server/
COPY sdn-server/third_party/spacedatastandards-go/go.mod ./sdn-server/third_party/spacedatastandards-go/go.mod
WORKDIR /build/sdn-server
RUN go mod download

WORKDIR /build
COPY sdn-server/ ./sdn-server/

WORKDIR /build/sdn-server
ARG SDN_RELEASE_TAG=""
# --start-group lets ld resolve the cycles between the WasmEdge sublibraries and
# LLVM's archives; it pulls only the members it needs. libstdc++/libc stay
# dynamic — they are base system libraries present on every host.
RUN set -eux; \
    P=/opt/wasmedge-static/prefix; \
    GRP="$P/lib/libwasmedge.a"; \
    for a in Common Loader LoaderFileMgr Validator Executor VM HostModuleWasi PO Driver System Plugin AOT LLVM; do \
      [ -f "$P/lib/libwasmedge${a}.a" ] && GRP="$GRP $P/lib/libwasmedge${a}.a"; \
    done; \
    GRP="$GRP $P/lib/libspdlog.a $P/lib/libfmt.a"; \
    LLVMLIBS="$(llvm-config-16 --link-static --libfiles | tr '\n' ' ')"; \
    CGO_ENABLED=1 go build \
      -ldflags="-s -w -X github.com/spacedatanetwork/sdn-server/internal/versioninfo.ReleaseTag=${SDN_RELEASE_TAG} -linkmode external -extldflags \"-Wl,--start-group $GRP $LLVMLIBS -lstdc++ -Wl,--end-group -lm -ldl -lpthread -lz -lzstd -ltinfo\"" \
      -o /out/spacedatanetwork ./cmd/spacedatanetwork; \
    CGO_ENABLED=0 go build -tags edge -ldflags="-s -w" -o /out/spacedatanetwork-edge ./cmd/spacedatanetwork-edge; \
    ldd /out/spacedatanetwork | grep -qi wasmedge && { echo "binary still links libwasmedge"; exit 1; } || true

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    libsqlite3-0 \
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

USER sdn

# A container has no AOT cache. Without a precompiled artifact the daemon opens
# the FlatSQL engine in the interpreter (~100x slower for every query) and says
# so once at boot. Prewarm at build time, as the same user and HOME the daemon
# runs under, so the running container finds a native artifact.
RUN /app/spacedatanetwork prewarm-aot

EXPOSE 5001 4001 4001/udp 8080 8081

# Everything the node owns lives under /app/data: the record store, the keys
# (identity), auth, flows. One volume keeps the identity across container
# recreation. Set SDN_KEY_PASSWORD_FILE (a mounted secret) to decouple the
# key-at-rest password from the container's machine fingerprint.
VOLUME /app/data

ENTRYPOINT ["/app/spacedatanetwork"]
CMD ["daemon", "--config", "/app/config/full-docker.yaml"]
