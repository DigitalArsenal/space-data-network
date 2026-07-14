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
RUN CGO_ENABLED=1 go build -ldflags="-s -w" -o /out/spacedatanetwork ./cmd/spacedatanetwork \
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
COPY sdn-js/ui/dist/ /app/admin-ui/
COPY webui/build/ /app/webui/
COPY config/full-docker.yaml /app/config/full-docker.yaml

RUN mkdir -p /app/data /app/keys && chown -R sdn:sdn /app

USER sdn

EXPOSE 5001 4001 4001/udp 8080 8081

ENV WASMEDGE_DIR=/opt/wasmedge
ENV LD_LIBRARY_PATH=/opt/wasmedge/lib
ENV SDN_ADMIN_UI_PATH=/app/admin-ui
ENV SDN_WEBUI_PATH=/app/webui

VOLUME /app/data

ENTRYPOINT ["/app/spacedatanetwork"]
CMD ["daemon", "--config", "/app/config/full-docker.yaml"]
