# SDN Node
# Single container image. Defaults to a full node; operators can override the
# command to run the edge relay binary from the same image.

FROM golang:1.25-bookworm AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    bash \
    build-essential \
    ca-certificates \
    curl \
    libsqlite3-dev \
    pkg-config \
  && rm -rf /var/lib/apt/lists/*

WORKDIR /build

COPY scripts/install-wasmedge.sh ./scripts/install-wasmedge.sh
RUN chmod +x ./scripts/install-wasmedge.sh && ./scripts/install-wasmedge.sh

ENV WASMEDGE_DIR=/root/.wasmedge
ENV CGO_CFLAGS="-I/root/.wasmedge/include"
ENV CGO_LDFLAGS="-L/root/.wasmedge/lib -Wl,-rpath,/root/.wasmedge/lib"

COPY sdn-server/go.mod sdn-server/go.sum ./sdn-server/
COPY sdn-server/third_party/spacedatastandards-go/go.mod ./sdn-server/third_party/spacedatastandards-go/go.mod
WORKDIR /build/sdn-server
RUN go mod download

WORKDIR /build
COPY sdn-server/ ./sdn-server/

WORKDIR /build/sdn-server
ARG SDN_RELEASE_TAG=""
RUN CGO_ENABLED=1 go build -ldflags="-s -w -X github.com/spacedatanetwork/sdn-server/internal/versioninfo.ReleaseTag=${SDN_RELEASE_TAG}" -o /out/spacedatanetwork ./cmd/spacedatanetwork \
  && CGO_ENABLED=0 go build -tags edge -ldflags="-s -w" -o /out/spacedatanetwork-edge ./cmd/spacedatanetwork-edge

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
COPY --from=builder /root/.wasmedge /opt/wasmedge
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

RUN mkdir -p /app/data /app/keys && chown -R sdn:sdn /app

USER sdn

EXPOSE 5001 4001 4001/udp 8080 8081

ENV WASMEDGE_DIR=/opt/wasmedge
ENV LD_LIBRARY_PATH=/opt/wasmedge/lib
ENV SDN_WEBUI_PATH=/app/webui

VOLUME /app/data

ENTRYPOINT ["/app/spacedatanetwork"]
CMD ["daemon", "--config", "/app/config/full-docker.yaml"]
