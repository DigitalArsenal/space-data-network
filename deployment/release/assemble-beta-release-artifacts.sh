#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "${script_dir}/../.." && pwd)"
dist_dir="${DIST_DIR:-${root}/dist}"
release_dir="${RELEASE_DIR:-${dist_dir}/release}"
version="${VERSION:?VERSION is required}"
release_tag="${RELEASE_TAG:-v${version}}"
commit="${GITHUB_SHA:-$(git -C "${root}" rev-parse HEAD)}"

rm -rf "${release_dir}"
mkdir -p "${release_dir}"

copy_matches() {
  local pattern="$1"
  shopt -s nullglob
  local file
  for file in ${pattern}; do
    local release_name
    release_name="$(basename "${file}")"
    # GitHub release assets normalize "~" poorly in download names; do it
    # explicitly so manifests and checksums match the published filenames.
    release_name="${release_name//\~/.}"
    cp "${file}" "${release_dir}/${release_name}"
  done
  shopt -u nullglob
}

copy_matches "${dist_dir}/packages/*"
copy_matches "${dist_dir}/linux-vm/*.tar.gz"
copy_matches "${dist_dir}/container-images/*.tar.gz"
copy_matches "${dist_dir}/cli/*.tar.gz"
copy_matches "${dist_dir}/cli/*.zip"
copy_matches "${dist_dir}/desktop/*"
copy_matches "${dist_dir}/update-feed/*.tar.gz"
copy_matches "${dist_dir}/sdn-js/*.tgz"
copy_matches "${dist_dir}/sbom/*.json"

required_cli_artifacts=(
  "spacedatanetwork-${version}-darwin-amd64.tar.gz"
  "spacedatanetwork-${version}-darwin-arm64.tar.gz"
  "spacedatanetwork-${version}-linux-amd64.tar.gz"
  "spacedatanetwork-${version}-linux-arm64.tar.gz"
  "spacedatanetwork-${version}-windows-amd64.zip"
)

for required_cli_artifact in "${required_cli_artifacts[@]}"; do
  if [[ ! -f "${release_dir}/${required_cli_artifact}" ]]; then
    echo "missing required CLI release artifact: ${required_cli_artifact}" >&2
    exit 1
  fi
done

require_match() {
  local pattern="$1"
  shopt -s nullglob
  local matches=( "${release_dir}"/${pattern} )
  shopt -u nullglob
  if [[ ${#matches[@]} -eq 0 ]]; then
    echo "missing required desktop release artifact matching: ${pattern}" >&2
    exit 1
  fi
}

required_desktop_artifact_patterns=(
  "space-data-network-desktop-*-mac.dmg"
  "space-data-network-desktop-*-squirrel.zip"
  "space-data-network-desktop-setup-*-windows-*.exe"
  "space-data-network-desktop-portable-*-windows-*.exe"
  "space-data-network-desktop-*-linux-*.AppImage"
  "space-data-network-desktop-*-linux-*.deb"
  "space-data-network-desktop-*-linux-*.rpm"
)

for required_desktop_artifact_pattern in "${required_desktop_artifact_patterns[@]}"; do
  require_match "${required_desktop_artifact_pattern}"
done

if [[ -n "${SDN_UPDATE_SIGNING_KEY_PEM:-}" ]]; then
  update_key_id="${SDN_UPDATE_KEY_ID:-sdn-beta-release}"
  update_sequence="${SDN_UPDATE_SEQUENCE:-${GITHUB_RUN_NUMBER:-}}"
  if [[ -z "${update_sequence}" ]]; then
    echo "SDN_UPDATE_SEQUENCE or GITHUB_RUN_NUMBER is required to build the CLI update feed" >&2
    exit 1
  fi
  update_created_at="${SDN_UPDATE_FEED_GENERATED_AT:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
  update_payload_dir="${dist_dir}/update-payloads"
  rm -rf "${update_payload_dir}" "${release_dir}/update-feed"
  mkdir -p "${update_payload_dir}"

  feed_args=(--out-dir "${release_dir}/update-feed")
  cli_update_targets=(
    "darwin amd64 tar.gz"
    "darwin arm64 tar.gz"
    "linux amd64 tar.gz"
    "linux arm64 tar.gz"
    "windows amd64 zip"
  )
  for cli_update_target in "${cli_update_targets[@]}"; do
    read -r target_os target_arch target_ext <<< "${cli_update_target}"
    archive_path="${release_dir}/spacedatanetwork-${version}-${target_os}-${target_arch}.${target_ext}"
    payload_out_dir="${update_payload_dir}/${target_os}-${target_arch}"
    node "${root}/deployment/release/build-cli-update-payload.mjs" \
      --bundle-archive "${archive_path}" \
      --version "${version}" \
      --sequence "${update_sequence}" \
      --channel beta \
      --platform "${target_os}" \
      --arch "${target_arch}" \
      --key-id "${update_key_id}" \
      --created-at "${update_created_at}" \
      --out-dir "${payload_out_dir}"
    feed_args+=(--entry "${payload_out_dir}/manifest.json:${payload_out_dir}/update.wasm")
  done
  node "${root}/deployment/release/build-sdn-update-feed.js" "${feed_args[@]}"
  tar -czf "${release_dir}/spacedatanetwork-update-feed-${version}.tar.gz" -C "${release_dir}" update-feed
fi

if [[ -f "${dist_dir}/ipfs/ipfs-deployment.json" ]]; then
  cp "${dist_dir}/ipfs/ipfs-deployment.json" "${release_dir}/"
fi

if [[ -f "${dist_dir}/container-digests.json" ]]; then
  cp "${dist_dir}/container-digests.json" "${release_dir}/"
fi

cat > "${release_dir}/SDN-BETA-RELEASE.md" <<EOF
# Space Data Network ${release_tag} Beta

These artifacts are beta builds. Use the release number \`${release_tag}\` when reporting issues or pinning deployments.

## Included artifacts
EOF

while IFS= read -r artifact; do
  printf -- '- `%s`\n' "${artifact}" >> "${release_dir}/SDN-BETA-RELEASE.md"
done < <(find "${release_dir}" -maxdepth 1 -type f ! -name 'SDN-BETA-RELEASE.md' -exec basename {} \; | sort)

cat >> "${release_dir}/SDN-BETA-RELEASE.md" <<'EOF'

## Container images

- `dockerdigitalarsenal/space-data-network:<beta-version>`

The same image defaults to a full node. Operators who need edge-relay mode can override the container command.

Downloadable Docker image tarballs are also included as `spacedatanetwork-container-<native-package-version>-linux-amd64.tar.gz`.
Load them with `docker load --input <file>`.

Verify downloaded files with `spacedatanetwork-checksums.txt`.
EOF

node - "${release_dir}" "${version}" "${release_tag}" "${commit}" <<'NODE'
import { createHash } from 'node:crypto';
import { readdirSync, readFileSync, statSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const [releaseDir, version, releaseTag, commit] = process.argv.slice(2);
const artifacts = readdirSync(releaseDir)
  .filter((name) => name !== 'spacedatanetwork-checksums.txt')
  .filter((name) => statSync(join(releaseDir, name)).isFile())
  .sort()
  .map((name) => {
    const contents = readFileSync(join(releaseDir, name));
    return {
      name,
      bytes: contents.length,
      sha256: createHash('sha256').update(contents).digest('hex')
    };
  });

writeFileSync(
  join(releaseDir, 'spacedatanetwork-beta-manifest.json'),
  `${JSON.stringify({
    releaseTag,
    version,
    channel: 'beta',
    commit,
    generatedAt: new Date().toISOString(),
    artifacts
  }, null, 2)}\n`
);
NODE

(
  cd "${release_dir}"
  rm -f spacedatanetwork-checksums.txt
  while IFS= read -r artifact; do
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum "${artifact}"
    else
      shasum -a 256 "${artifact}"
    fi
  done < <(find . -maxdepth 1 -type f ! -name 'spacedatanetwork-checksums.txt' -exec basename {} \; | sort) > spacedatanetwork-checksums.txt
)

find "${release_dir}" -maxdepth 1 -type f -exec basename {} \; | sort
