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
copy_matches "${dist_dir}/sdn-js/*.tgz"
copy_matches "${dist_dir}/sbom/*.json"

if [[ -f "${dist_dir}/ipfs/ipfs-deployment.json" ]]; then
  cp "${dist_dir}/ipfs/ipfs-deployment.json" "${release_dir}/"
fi

if [[ -f "${dist_dir}/container-digests.json" ]]; then
  cp "${dist_dir}/container-digests.json" "${release_dir}/"
fi

cat > "${release_dir}/SDN-BETA-RELEASE.md" <<EOF
# Space Data Network ${release_tag} Beta

Beta channel release for local testing, browser-node clients, and early operator feedback.

These artifacts are beta builds. Use the release number \`${release_tag}\` when reporting issues or pinning deployments.

## Included artifacts
EOF

while IFS= read -r artifact; do
  printf -- '- `%s`\n' "${artifact}" >> "${release_dir}/SDN-BETA-RELEASE.md"
done < <(find "${release_dir}" -maxdepth 1 -type f ! -name 'SDN-BETA-RELEASE.md' -exec basename {} \; | sort)

cat >> "${release_dir}/SDN-BETA-RELEASE.md" <<'EOF'

## Container images

- `ghcr.io/digitalarsenal/space-data-network-full:<beta-version>`
- `ghcr.io/digitalarsenal/space-data-network-edge:<beta-version>`

Downloadable Docker image tarballs are also included as `spacedatanetwork-container-*-linux-amd64.tar.gz`.
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
