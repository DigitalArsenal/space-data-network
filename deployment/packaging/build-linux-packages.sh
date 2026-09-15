#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "${script_dir}/../.." && pwd)"
out_dir="${OUT_DIR:-${root}/dist/packages}"
work_dir="${WORK_DIR:-${root}/dist/package-root}"
version="${VERSION:-$(git -C "${root}" describe --tags --always --dirty)}"
arch="${ARCH:-amd64}"
wasmedge_dir="${WASMEDGE_DIR:?WASMEDGE_DIR must point at a WasmEdge prefix (static or dynamic)}"
export WASMEDGE_DIR="${wasmedge_dir}"

if [[ "$(uname -s)" != "Linux" ]]; then
  echo "RPM/DEB packages must be built on Linux" >&2
  exit 1
fi

if ! command -v nfpm >/dev/null 2>&1; then
  echo "nfpm is required to build RPM/DEB packages" >&2
  exit 69
fi

# A STATIC prefix ships nothing: the runtime is already inside the binary, and
# staging a copy beside it would put a second, unused WasmEdge in the package
# that the daemon might then prefer. link.flags is the marker
# build-static-wasmedge.sh leaves to say which kind of prefix this is.
copy_wasmedge_runtime() {
  local target="$1"

  if [[ -f "${wasmedge_dir}/link.flags" ]]; then
    rm -rf "${target}"
    echo "static WasmEdge prefix: nothing staged at ${target}"
    return 0
  fi

  if [[ ! -x "${wasmedge_dir}/bin/wasmedge" || ! -d "${wasmedge_dir}/lib" ]]; then
    echo "WasmEdge runtime is missing at ${wasmedge_dir}; run scripts/install-wasmedge.sh before packaging" >&2
    exit 1
  fi

  rm -rf "${target}"
  mkdir -p "$(dirname "${target}")"
  cp -R "${wasmedge_dir}" "${target}"
}

rm -rf "${work_dir}" "${out_dir}"
mkdir -p \
  "${work_dir}/full/opt/spacedatanetwork/bin" \
  "${work_dir}/full/opt/spacedatanetwork/admin-ui" \
  "${work_dir}/full/opt/spacedatanetwork/webui" \
  "${work_dir}/full/etc/spacedatanetwork" \
  "${work_dir}/full/etc/systemd/system" \
  "${work_dir}/edge/opt/spacedatanetwork/bin" \
  "${work_dir}/edge/etc/systemd/system" \
  "${out_dir}"

(
  cd "${root}/sdn-server"
  ../scripts/go-with-wasmedge.sh build -o "${work_dir}/full/opt/spacedatanetwork/bin/spacedatanetwork" ./cmd/spacedatanetwork
  ../scripts/go-with-wasmedge.sh build -tags edge -o "${work_dir}/edge/opt/spacedatanetwork/bin/spacedatanetwork-edge" ./cmd/spacedatanetwork-edge
)

copy_wasmedge_runtime "${work_dir}/full/opt/spacedatanetwork/.wasmedge"
cp -R "${root}/sdn-js/ui/dist/." "${work_dir}/full/opt/spacedatanetwork/admin-ui/"
cp -R "${root}/webui/build/." "${work_dir}/full/opt/spacedatanetwork/webui/"
cp "${root}/config/full-vm.yaml" "${work_dir}/full/etc/spacedatanetwork/config.yaml"
cp "${root}/sdn-server/deploy/spacedatanetwork.service" "${work_dir}/full/etc/systemd/system/spacedatanetwork.service"
cp "${root}/sdn-server/deploy/spacedatanetwork-edge.service" "${work_dir}/edge/etc/systemd/system/spacedatanetwork-edge.service"

full_cfg="${work_dir}/nfpm-full.yaml"
edge_cfg="${work_dir}/nfpm-edge.yaml"

cat > "${full_cfg}" <<YAML
name: spacedatanetwork-full
arch: ${arch}
platform: linux
version: ${version}
section: net
priority: optional
maintainer: Space Data Network <release@spacedatanetwork.org>
description: Space Data Network full node with hosted admin and WebUI assets.
contents:
  - src: ${work_dir}/full/opt/spacedatanetwork
    dst: /opt/spacedatanetwork
  - src: ${work_dir}/full/etc/spacedatanetwork/config.yaml
    dst: /etc/spacedatanetwork/config.yaml
    type: config
  - src: ${work_dir}/full/etc/systemd/system/spacedatanetwork.service
    dst: /etc/systemd/system/spacedatanetwork.service
YAML

cat > "${edge_cfg}" <<YAML
name: spacedatanetwork-edge
arch: ${arch}
platform: linux
version: ${version}
section: net
priority: optional
maintainer: Space Data Network <release@spacedatanetwork.org>
description: Space Data Network edge relay.
contents:
  - src: ${work_dir}/edge/opt/spacedatanetwork
    dst: /opt/spacedatanetwork
  - src: ${work_dir}/edge/etc/systemd/system/spacedatanetwork-edge.service
    dst: /etc/systemd/system/spacedatanetwork-edge.service
YAML

nfpm pkg --packager rpm --config "${full_cfg}" --target "${out_dir}"
nfpm pkg --packager deb --config "${full_cfg}" --target "${out_dir}"
nfpm pkg --packager rpm --config "${edge_cfg}" --target "${out_dir}"
nfpm pkg --packager deb --config "${edge_cfg}" --target "${out_dir}"

find "${out_dir}" -type f -maxdepth 1 -print | sort
