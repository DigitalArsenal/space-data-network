#!/usr/bin/env bash
set -euo pipefail

version=""
platform=""
archive=""
output_dir=""
# --sha512: the archive's expected sha512. Given, an archive from either host
# is accepted only when it has exactly this hash.
# --keep-archive: where to copy the accepted archive (for a cache that the
# caller checks against the same hash on every run).
pinned_sha512=""
keep_archive=""
attempts="${KUBO_DOWNLOAD_ATTEMPTS:-5}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)
      version="${2:-}"
      shift 2
      ;;
    --platform)
      platform="${2:-}"
      shift 2
      ;;
    --archive)
      archive="${2:-}"
      shift 2
      ;;
    --output-dir)
      output_dir="${2:-}"
      shift 2
      ;;
    --sha512)
      pinned_sha512="$(printf '%s' "${2:-}" | tr '[:upper:]' '[:lower:]')"
      shift 2
      ;;
    --keep-archive)
      keep_archive="${2:-}"
      shift 2
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if [[ -z "${version}" || -z "${platform}" || -z "${archive}" || -z "${output_dir}" ]]; then
  echo "usage: $0 --version VERSION --platform PLATFORM --archive tar.gz|zip --output-dir DIR [--sha512 HEX] [--keep-archive PATH]" >&2
  exit 2
fi
if [[ -n "${pinned_sha512}" && ! "${pinned_sha512}" =~ ^[0-9a-f]{128}$ ]]; then
  echo "--sha512 must be 128 hex characters" >&2
  exit 2
fi

case "${archive}" in
  tar.gz|zip)
    ;;
  *)
    echo "unsupported Kubo archive type: ${archive}" >&2
    exit 2
    ;;
esac

asset="kubo_${version}_${platform}.${archive}"
url="https://dist.ipfs.tech/kubo/${version}/${asset}"
# The same archive from Kubo's own GitHub release, used when dist.ipfs.tech does
# not answer (it was down on 2026-10-02/03 and again on 2026-11-14, when the
# v1.1.0 release could not start). Each GitHub asset has a published .sha512
# beside it, and the fallback is accepted only when it matches.
mirror_url="${KUBO_MIRROR_BASE_URL:-https://github.com/ipfs/kubo/releases/download}/${version}/${asset}"
work_dir="$(mktemp -d)"
archive_path="${work_dir}/kubo.${archive}"

cleanup() {
  rm -rf "${work_dir}"
}
trap cleanup EXIT

validate_archive() {
  case "${archive}" in
    zip)
      unzip -tq "${archive_path}" >/dev/null
      ;;
    tar.gz)
      tar -tzf "${archive_path}" >/dev/null
      ;;
  esac
}

sha512_of() {
  if command -v sha512sum >/dev/null 2>&1; then
    sha512sum "$1" | cut -d' ' -f1
  else
    shasum -a 512 "$1" | cut -d' ' -f1
  fi
}

# With --sha512, the archive's hash must be the pinned one (from whichever host
# it came); the actual hash is printed either way.
matches_pin() {
  local actual
  actual="$(sha512_of "${archive_path}")"
  echo "archive sha512: ${actual}"
  if [[ -n "${pinned_sha512}" && "${actual}" != "${pinned_sha512}" ]]; then
    echo "the archive's sha512 is not the pinned ${pinned_sha512}" >&2
    # For whoever sets the pin: the hashes both hosts publish beside the asset.
    for published in "${url}.sha512" "${mirror_url}.sha512"; do
      echo "published at ${published}: $(curl -fsSL --connect-timeout 30 --max-time 60 "${published}" 2>/dev/null | tr -s '[:space:]' ' ' | cut -d' ' -f1)" >&2
    done
    return 1
  fi
}

extract_archive() {
  if [[ -n "${keep_archive}" ]]; then
    mkdir -p "$(dirname "${keep_archive}")"
    cp "${archive_path}" "${keep_archive}"
  fi
  rm -rf "${output_dir}/kubo"
  mkdir -p "${output_dir}"
  case "${archive}" in
    zip)
      unzip -q "${archive_path}" -d "${output_dir}"
      ;;
    tar.gz)
      tar -xzf "${archive_path}" -C "${output_dir}"
      ;;
  esac
}

# --retry-all-errors landed in curl 7.71. The Linux release artifacts are built
# on ubuntu:20.04 so the binaries do not demand a glibc newer than most hosts
# have, and focal ships curl 7.68, which rejects the flag outright rather than
# ignoring it. Probed once instead of assumed: --retry on its own still covers
# the transient network failures this loop exists for.
retry_all_errors=""
if curl --help all 2>/dev/null | grep -q -- '--retry-all-errors'; then
  retry_all_errors="--retry-all-errors"
fi

for attempt in $(seq 1 "${attempts}"); do
  rm -f "${archive_path}"
  echo "Downloading Kubo ${version} for ${platform} (${archive}), attempt ${attempt}/${attempts}"

  if curl -fL --retry 3 --retry-delay 5 ${retry_all_errors} --connect-timeout 30 --max-time 300 -o "${archive_path}" "${url}"; then
    byte_count="$(wc -c < "${archive_path}" | tr -d '[:space:]')"
    echo "Downloaded ${byte_count} bytes from ${url}"

    if validate_archive && matches_pin; then
      extract_archive
      echo "Extracted Kubo ${version} for ${platform} to ${output_dir}"
      exit 0
    fi

    echo "Downloaded file is not a valid ${archive} archive with the pinned sha512" >&2
  fi

  if [[ "${attempt}" -lt "${attempts}" ]]; then
    sleep_seconds=$((attempt * 5))
    echo "Retrying Kubo download in ${sleep_seconds}s" >&2
    sleep "${sleep_seconds}"
  fi
done

echo "failed to download a valid Kubo archive from ${url}; trying ${mirror_url}" >&2

rm -f "${archive_path}"
if curl -fL --retry 3 --retry-delay 5 ${retry_all_errors} --connect-timeout 30 --max-time 300 -o "${archive_path}" "${mirror_url}" \
  && curl -fL --retry 3 --retry-delay 5 ${retry_all_errors} --connect-timeout 30 --max-time 60 -o "${archive_path}.sha512" "${mirror_url}.sha512"; then
  expected="$(tr -s '[:space:]' ' ' < "${archive_path}.sha512" | cut -d' ' -f1 | tr '[:upper:]' '[:lower:]')"
  actual="$(sha512_of "${archive_path}")"
  if [[ -n "${expected}" && "${expected}" == "${actual}" ]] && validate_archive && matches_pin; then
    echo "Downloaded $(wc -c < "${archive_path}" | tr -d '[:space:]') bytes from ${mirror_url} (sha512 matches ${mirror_url}.sha512)"
    extract_archive
    echo "Extracted Kubo ${version} for ${platform} to ${output_dir}"
    exit 0
  fi
  echo "the archive from ${mirror_url} does not match its published sha512 (expected ${expected:-none}, got ${actual})" >&2
fi

echo "failed to download a valid Kubo archive from ${url} or ${mirror_url}" >&2
if [[ -f "${archive_path}" ]]; then
  echo "last response preview:" >&2
  head -c 200 "${archive_path}" >&2 || true
  echo >&2
fi
exit 1
