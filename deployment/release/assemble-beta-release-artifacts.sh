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

# The four platforms whose builds are proven. A release without one of these is
# a broken release and must not be published.
required_cli_artifacts=(
  "spacedatanetwork-${version}-darwin-amd64.tar.gz"
  "spacedatanetwork-${version}-darwin-arm64.tar.gz"
  "spacedatanetwork-${version}-linux-amd64.tar.gz"
  "spacedatanetwork-${version}-linux-arm64.tar.gz"
)

# Windows is EXPECTED, not required — the same policy the wasmedge-static, cli
# and desktop jobs already carry (matrix.experimental + continue-on-error).
# Requiring it here quietly undid all of that: the Windows leg was allowed to
# fail, every other artifact built, and then this script refused to assemble
# anything, so a Windows toolchain problem meant nobody got a release at all.
# Zero artifacts is worse than four, and the absence stays visible — it is
# logged here and stated in the notes rather than passed over.
optional_cli_artifacts=(
  "spacedatanetwork-${version}-windows-amd64.zip"
)

for required_cli_artifact in "${required_cli_artifacts[@]}"; do
  if [[ ! -f "${release_dir}/${required_cli_artifact}" ]]; then
    echo "missing required CLI release artifact: ${required_cli_artifact}" >&2
    exit 1
  fi
done

missing_optional=()
for optional_cli_artifact in "${optional_cli_artifacts[@]}"; do
  if [[ ! -f "${release_dir}/${optional_cli_artifact}" ]]; then
    missing_optional+=( "${optional_cli_artifact}" )
    echo "NOTE: ${optional_cli_artifact} is absent; publishing without it" >&2
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

# These must match what the desktop job is CONFIGURED to build — see
# builder_args in the desktop matrix: "--mac dmg zip", "--linux AppImage deb
# tar.xz". Three of these patterns never matched anything electron-builder
# produces ("*-mac.dmg" against a real "-mac-arm64.dmg", a squirrel.zip that is
# a Windows updater format, and an rpm the Linux leg is not asked to build).
# Nobody noticed because the publish job had never run far enough to check.
required_desktop_artifact_patterns=(
  "space-data-network-desktop-*-mac-*.dmg"
  "space-data-network-desktop-*-mac-*.zip"
  "space-data-network-desktop-*-linux-*.AppImage"
  "space-data-network-desktop-*-linux-*.deb"
  "space-data-network-desktop-*-linux-*.tar.xz"
)

# The Windows desktop app now BUILDS (run 35095153877 produced both installers),
# but its job is still continue-on-error, so absence must stay tolerated rather
# than fail the release — promoting these to required would let a soft-failing
# leg hard-fail publication.
#
# These patterns were wrong and could not have been noticed until Windows
# actually produced something: electron-builder emits
#   space-data-network-desktop-<version>-win-x64.exe            (nsis setup)
#   space-data-network-desktop-portable-<version>-win-x64.exe   (portable)
# i.e. "win", not "windows", and no "setup" infix on the installer. Since
# copy_matches dist/desktop/* publishes whatever is there, the mismatch did not
# drop the files — it listed them under "Not in this release" while they were
# attached to it, which is worse than missing. The [0-9] class anchors the
# setup pattern to the version so it cannot also swallow the portable build.
optional_desktop_artifact_patterns=(
  "space-data-network-desktop-[0-9]*-win-*.exe"
  "space-data-network-desktop-portable-*-win-*.exe"
)

for required_desktop_artifact_pattern in "${required_desktop_artifact_patterns[@]}"; do
  require_match "${required_desktop_artifact_pattern}"
done

for optional_desktop_artifact_pattern in "${optional_desktop_artifact_patterns[@]}"; do
  shopt -s nullglob
  optional_matches=( "${release_dir}"/${optional_desktop_artifact_pattern} )
  shopt -u nullglob
  if [[ ${#optional_matches[@]} -eq 0 ]]; then
    missing_optional+=( "${optional_desktop_artifact_pattern}" )
    echo "NOTE: no artifact matching ${optional_desktop_artifact_pattern}; publishing without it" >&2
  fi
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
    # release, not beta: beta is the internal fleet dev lane (see
    # build-self-contained-cli.mjs) and must stay invisible to public installs.
    node "${root}/deployment/release/build-cli-update-payload.mjs" \
      --bundle-archive "${archive_path}" \
      --version "${version}" \
      --sequence "${update_sequence}" \
      --channel release \
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

# Requirements, MEASURED from the artifacts rather than written down.
#
# The binaries link WasmEdge, libstdc++ and libgcc statically, so glibc is the
# only thing a Linux host still has to provide — and its required version is
# decided by whichever runner built the archive, not by anything in this repo.
# A number typed into these notes would be wrong the first time that runner
# image changed, which is exactly how the rest of this release's documentation
# went stale. Read it off the binary instead.
#
# Best-effort throughout: a missing tool or an unreadable archive drops the
# section, it never fails the release.
glibc_floor=""
for linux_archive in "${release_dir}"/spacedatanetwork-*-linux-*.tar.gz; do
  [[ -f "${linux_archive}" ]] || continue
  found="$(tar -xzOf "${linux_archive}" --wildcards '*/runtime/sdn/spacedatanetwork' 2>/dev/null \
    | grep -ao 'GLIBC_2\.[0-9]\+' 2>/dev/null \
    | sort -uV | tail -1 || true)"
  if [[ -n "${found}" ]]; then
    if [[ -z "${glibc_floor}" ]] || [[ "$(printf '%s\n%s\n' "${glibc_floor}" "${found}" | sort -V | tail -1)" == "${found}" ]]; then
      glibc_floor="${found}"
    fi
  fi
done

if [[ -n "${glibc_floor}" ]]; then
  cat >> "${release_dir}/SDN-BETA-RELEASE.md" <<EOF

## Requirements

The node ships as a single binary: WasmEdge, libstdc++ and libgcc are linked in,
so nothing has to be installed alongside it and no package manager is involved.

On Linux the one remaining system dependency is glibc, and these builds need
**${glibc_floor#GLIBC_} or newer**. Check yours with \`ldd --version\`. Hosts
older than that should run the container image, which carries its own.

macOS builds depend only on libraries that ship with the OS. Windows builds are
self-contained.
EOF
fi

if [[ ${#missing_optional[@]} -gt 0 ]]; then
  {
    printf '\n## Not in this release\n\n'
    for absent in "${missing_optional[@]}"; do
      printf -- '- `%s` — this platform did not build for this version.\n' "${absent}"
    done
  } >> "${release_dir}/SDN-BETA-RELEASE.md"
fi

cat >> "${release_dir}/SDN-BETA-RELEASE.md" <<'EOF'

## Container images

- `dockerdigitalarsenal/space-data-network:<beta-version>`

The same image defaults to a full node. Operators who need edge-relay mode can override the container command.

The published image is a multi-architecture manifest, so `docker pull` resolves to linux/amd64 or linux/arm64 automatically. Downloadable tarballs of each are also included as `spacedatanetwork-container-<native-package-version>-linux-amd64.tar.gz` and `...-linux-arm64.tar.gz`.
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
