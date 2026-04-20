#!/usr/bin/env bash
set -euo pipefail

BUNDLE_PATH="${1:-}"
if [ -z "${BUNDLE_PATH}" ] || [ ! -f "${BUNDLE_PATH}" ]; then
  echo "usage: $0 /path/to/spacedatanetwork-linux-vm-<version>.tar.gz" >&2
  exit 1
fi

if [ "$(id -u)" -ne 0 ]; then
  echo "run as root" >&2
  exit 1
fi

if ! id -u sdn >/dev/null 2>&1; then
  useradd --system --home /var/lib/spacedatanetwork --shell /usr/sbin/nologin sdn
fi

mkdir -p /opt/spacedatanetwork /etc/spacedatanetwork /var/lib/spacedatanetwork/frontend /var/lib/spacedatanetwork/data
tar -xzf "${BUNDLE_PATH}" -C /

chown -R sdn:sdn /opt/spacedatanetwork /var/lib/spacedatanetwork

systemctl daemon-reload
systemctl enable spacedatanetwork
echo "bundle installed. start with: systemctl restart spacedatanetwork"
