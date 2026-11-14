#!/usr/bin/env bash
set -euo pipefail

version=""
platform=""
archive=""
output_dir=""
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
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if [[ -z "${version}" || -z "${platform}" || -z "${archive}" || -z "${output_dir}" ]]; then
  echo "usage: $0 --version VERSION --platform PLATFORM --archive tar.gz|zip --output-dir DIR" >&2
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

extract_archive() {
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

    if validate_archive; then
      extract_archive
      echo "Extracted Kubo ${version} for ${platform} to ${output_dir}"
      exit 0
    fi

    echo "Downloaded file is not a valid ${archive} archive" >&2
  fi

  if [[ "${attempt}" -lt "${attempts}" ]]; then
    sleep_seconds=$((attempt * 5))
    echo "Retrying Kubo download in ${sleep_seconds}s" >&2
    sleep "${sleep_seconds}"
  fi
done

echo "failed to download a valid Kubo archive from ${url}; trying ${mirror_url}" >&2

sha512_of() {
  if command -v sha512sum >/dev/null 2>&1; then
    sha512sum "$1" | cut -d' ' -f1
  else
    shasum -a 512 "$1" | cut -d' ' -f1
  fi
}

rm -f "${archive_path}"
if curl -fL --retry 3 --retry-delay 5 ${retry_all_errors} --connect-timeout 30 --max-time 300 -o "${archive_path}" "${mirror_url}" \
  && curl -fL --retry 3 --retry-delay 5 ${retry_all_errors} --connect-timeout 30 --max-time 60 -o "${archive_path}.sha512" "${mirror_url}.sha512"; then
  expected="$(tr -s '[:space:]' ' ' < "${archive_path}.sha512" | cut -d' ' -f1 | tr '[:upper:]' '[:lower:]')"
  actual="$(sha512_of "${archive_path}")"
  if [[ -n "${expected}" && "${expected}" == "${actual}" ]] && validate_archive; then
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
