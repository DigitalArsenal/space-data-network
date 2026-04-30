#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "${script_dir}/../.." && pwd)"
out_dir="${OUT_DIR:-${root}/dist/ipfs}"
pin_helper="${PIN_HELPER:-${script_dir}/pin-kubo.sh}"

mkdir -p "${out_dir}"

(
  cd "${root}/sdn-js"
  npm ci
  npm run build:ui
)

if [[ ! -f "${root}/webui/build/index.html" ]]; then
  (
    cd "${root}/webui"
    npm ci
    npm run build
  )
fi

sdn_admin_json="$("${pin_helper}" "${root}/sdn-js/ui/dist" "sdn-admin")"
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
cat "${out_dir}/ipfs-deployment.json"
