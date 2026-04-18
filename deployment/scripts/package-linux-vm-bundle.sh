#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(dirname "$SCRIPT_DIR")"
PROJECT_ROOT="$(dirname "$DEPLOY_DIR")"
OUT_DIR="${OUT_DIR:-${PROJECT_ROOT}/dist/linux-vm}"
STAGE_DIR="${OUT_DIR}/stage"
VERSION="${VERSION:-$(git -C "${PROJECT_ROOT}" describe --tags --always --dirty)}"
ARCHIVE_PATH="${OUT_DIR}/spacedatanetwork-linux-vm-${VERSION}.tar.gz"

if [ "$(uname -s)" != "Linux" ]; then
  echo "linux VM bundles must be built on Linux so the packaged binary matches the target OS" >&2
  echo "use the linux-vm-bundle GitHub Actions workflow artifact or run this script on a Linux builder" >&2
  exit 1
fi

rm -rf "${STAGE_DIR}"
mkdir -p \
  "${STAGE_DIR}/opt/spacedatanetwork/bin" \
  "${STAGE_DIR}/opt/spacedatanetwork/admin-ui" \
  "${STAGE_DIR}/opt/spacedatanetwork/webui" \
  "${STAGE_DIR}/etc/spacedatanetwork" \
  "${STAGE_DIR}/etc/systemd/system" \
  "${STAGE_DIR}/var/lib/spacedatanetwork/frontend"

(
  cd "${PROJECT_ROOT}/sdn-js"
  npm run build:ui
)

if [ ! -f "${PROJECT_ROOT}/webui/build/index.html" ]; then
  echo "webui/build is missing; build the IPFS WebUI before packaging" >&2
  exit 1
fi

(
  cd "${PROJECT_ROOT}/sdn-server"
  ../scripts/go-with-wasmedge.sh build -o "${STAGE_DIR}/opt/spacedatanetwork/bin/spacedatanetwork" ./cmd/spacedatanetwork
)

cp -R "${PROJECT_ROOT}/sdn-js/ui/dist/." "${STAGE_DIR}/opt/spacedatanetwork/admin-ui/"
cp -R "${PROJECT_ROOT}/webui/build/." "${STAGE_DIR}/opt/spacedatanetwork/webui/"
cp "${PROJECT_ROOT}/config/full-vm.yaml" "${STAGE_DIR}/etc/spacedatanetwork/config.yaml"
cp "${PROJECT_ROOT}/sdn-server/deploy/spacedatanetwork.service" "${STAGE_DIR}/etc/systemd/system/spacedatanetwork.service"

cat > "${STAGE_DIR}/opt/spacedatanetwork/INSTALL.txt" <<'EOF'
Extract this archive at / and then run:

  sudo deployment/scripts/install-vm-bundle.sh /path/to/spacedatanetwork-linux-vm-<version>.tar.gz

The bundle installs:
  /opt/spacedatanetwork/bin/spacedatanetwork
  /opt/spacedatanetwork/admin-ui
  /opt/spacedatanetwork/webui
  /etc/spacedatanetwork/config.yaml
  /etc/systemd/system/spacedatanetwork.service
EOF

mkdir -p "${OUT_DIR}"
tar -C "${STAGE_DIR}" -czf "${ARCHIVE_PATH}" .
echo "${ARCHIVE_PATH}"
