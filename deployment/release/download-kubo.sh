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

url="https://dist.ipfs.tech/kubo/${version}/kubo_${version}_${platform}.${archive}"
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

for attempt in $(seq 1 "${attempts}"); do
  rm -f "${archive_path}"
  echo "Downloading Kubo ${version} for ${platform} (${archive}), attempt ${attempt}/${attempts}"

  if curl -fL --retry 3 --retry-delay 5 --retry-all-errors --connect-timeout 30 --max-time 300 -o "${archive_path}" "${url}"; then
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

echo "failed to download a valid Kubo archive from ${url}" >&2
if [[ -f "${archive_path}" ]]; then
  echo "last response preview:" >&2
  head -c 200 "${archive_path}" >&2 || true
  echo >&2
fi
exit 1
