#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "${script_dir}/../.." && pwd)"
out_dir="${OUT_DIR:-${root}/dist/ipfs}"
pin_helper="${PIN_HELPER:-${script_dir}/pin-kubo.sh}"

export PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD="${PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD:-1}"
export PUPPETEER_SKIP_DOWNLOAD="${PUPPETEER_SKIP_DOWNLOAD:-1}"
export CYPRESS_INSTALL_BINARY="${CYPRESS_INSTALL_BINARY:-0}"

log() {
  printf '[ipfs-deploy] %s\n' "$*" >&2
}

run_with_timeout() {
  local seconds="$1"
  shift

  if command -v timeout >/dev/null 2>&1; then
    timeout "${seconds}" "$@"
  else
    "$@"
  fi
}

mkdir -p "${out_dir}"

(
  cd "${root}/sdn-js"
  log "Installing SDN UI dependencies"
  run_with_timeout "${SDN_UI_NPM_CI_TIMEOUT_SECONDS:-600}" npm ci --no-audit --fund=false
  log "Building SDN UI"
  run_with_timeout "${SDN_UI_BUILD_TIMEOUT_SECONDS:-600}" npm run build:ui
)

if [[ ! -f "${root}/webui/build/index.html" ]]; then
  (
    cd "${root}/webui"
    log "Installing IPFS WebUI dependencies"
    run_with_timeout "${WEBUI_NPM_CI_TIMEOUT_SECONDS:-900}" npm ci --no-audit --fund=false
    log "Building IPFS WebUI"
    run_with_timeout "${WEBUI_BUILD_TIMEOUT_SECONDS:-900}" npm run build
  )
else
  log "Using existing IPFS WebUI build at ${root}/webui/build"
fi

log "Pinning SDN admin UI"
sdn_admin_json="$("${pin_helper}" "${root}/sdn-js/ui/dist" "sdn-admin")"
log "Pinning IPFS WebUI"
ipfs_webui_json="$("${pin_helper}" "${root}/webui/build" "ipfs-webui")"

cat > "${out_dir}/ipfs-deployment.json" <<JSON
{
  "generatedAt": "$(date -u +"%Y-%m-%dT%H:%M:%SZ")",
  "sdnAdmin": ${sdn_admin_json},
  "ipfsWebui": ${ipfs_webui_json}
}
JSON

cp -R "${root}/sdn-js/ui/dist" "${out_dir}/sdn-admin"
cp -R "${root}/webui/build" "${out_dir}/ipfs-webui"
log "Wrote IPFS deployment metadata to ${out_dir}/ipfs-deployment.json"
cat "${out_dir}/ipfs-deployment.json"
