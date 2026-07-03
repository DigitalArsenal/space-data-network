#!/usr/bin/env bash
set -euo pipefail

SOURCE_ROOT="${SOURCE_ROOT:-/opt/spacedatanetwork/source}"
ASSET_DIR="${ASSET_DIR:-}"
if [ -z "${ASSET_DIR}" ]; then
  if [ -f "${SOURCE_ROOT}/celestrak/config.yaml" ]; then
    ASSET_DIR="${SOURCE_ROOT}/celestrak"
  else
    ASSET_DIR="${SOURCE_ROOT}/deployment/celestrak"
  fi
fi

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root" >&2
  exit 1
fi

if [ ! -f "${ASSET_DIR}/config.yaml" ]; then
  echo "missing ${ASSET_DIR}/config.yaml" >&2
  exit 1
fi

if ! id -u sdn >/dev/null 2>&1; then
  useradd --system --home /var/lib/spacedatanetwork --shell /usr/sbin/nologin sdn
fi
if ! id -u ipfs >/dev/null 2>&1; then
  useradd --system --home /var/lib/kubo --shell /usr/sbin/nologin ipfs
fi

mkdir -p \
  /opt/spacedatanetwork/bin \
  /opt/spacedatanetwork/admin-ui \
  /opt/spacedatanetwork/webui \
  /opt/spacedatanetwork/wasm \
  /etc/spacedatanetwork \
  /etc/systemd/system/spacedatanetwork.service.d \
  /var/lib/spacedatanetwork/data \
  /var/lib/spacedatanetwork/raw \
  /var/lib/spacedatanetwork/frontend \
  /var/lib/kubo

install -m 0644 "${ASSET_DIR}/config.yaml" /etc/spacedatanetwork/config.yaml
install -m 0644 "${ASSET_DIR}/kubo.service" /etc/systemd/system/kubo.service
install -m 0644 "${SOURCE_ROOT}/sdn-server/deploy/spacedatanetwork.service" /etc/systemd/system/spacedatanetwork.service

# Single-writer topology (loop C.6b): ingest now runs INSIDE the daemon
# (config.yaml `ingest.enabled: true`). The FlatSQL v2 store rejects a
# second writer process, so the legacy separate ingest unit must not run
# against the daemon's storage path — remove it on upgraded hosts.
if [ -f /etc/systemd/system/spacedatanetwork-ingest.service ]; then
  systemctl disable --now spacedatanetwork-ingest.service || true
  rm -f /etc/systemd/system/spacedatanetwork-ingest.service
fi

if [ -f "${SOURCE_ROOT}/sdn-js/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm" ]; then
  install -m 0644 "${SOURCE_ROOT}/sdn-js/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm" /opt/spacedatanetwork/wasm/hd-wallet-wasi.wasm
elif [ -f "${SOURCE_ROOT}/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm" ]; then
  install -m 0644 "${SOURCE_ROOT}/node_modules/hd-wallet-wasm/dist/hd-wallet-wasi.wasm" /opt/spacedatanetwork/wasm/hd-wallet-wasi.wasm
fi
if [ -f /opt/spacedatanetwork/wasm/hd-wallet-wasi.wasm ]; then
  cat > /etc/systemd/system/spacedatanetwork.service.d/hd-wallet.conf <<'EOF'
[Service]
Environment=HD_WALLET_WASM_PATH=/opt/spacedatanetwork/wasm/hd-wallet-wasi.wasm
EOF
fi

if [ -d "${SOURCE_ROOT}/sdn-js/ui/dist" ]; then
  rsync -a --delete "${SOURCE_ROOT}/sdn-js/ui/dist/" /opt/spacedatanetwork/admin-ui/
elif [ -d "${SOURCE_ROOT}/dist" ]; then
  rsync -a --delete "${SOURCE_ROOT}/dist/" /opt/spacedatanetwork/admin-ui/
fi
if [ -d "${SOURCE_ROOT}/webui/build" ]; then
  rsync -a --delete "${SOURCE_ROOT}/webui/build/" /opt/spacedatanetwork/webui/
elif [ -d "${SOURCE_ROOT}/build" ]; then
  rsync -a --delete "${SOURCE_ROOT}/build/" /opt/spacedatanetwork/webui/
fi

chown -R sdn:sdn /opt/spacedatanetwork /var/lib/spacedatanetwork
chown -R ipfs:ipfs /var/lib/kubo

if command -v ipfs >/dev/null 2>&1 && [ ! -f /var/lib/kubo/config ]; then
  runuser -u ipfs -- env IPFS_PATH=/var/lib/kubo ipfs init --profile=server
  runuser -u ipfs -- env IPFS_PATH=/var/lib/kubo ipfs config Addresses.API /ip4/127.0.0.1/tcp/5002
  runuser -u ipfs -- env IPFS_PATH=/var/lib/kubo ipfs config Addresses.Gateway /ip4/127.0.0.1/tcp/8081
  runuser -u ipfs -- env IPFS_PATH=/var/lib/kubo ipfs config --json Addresses.Swarm '["/ip4/0.0.0.0/tcp/4002","/ip4/0.0.0.0/udp/4002/quic-v1"]'
fi

systemctl daemon-reload
systemctl enable kubo spacedatanetwork
