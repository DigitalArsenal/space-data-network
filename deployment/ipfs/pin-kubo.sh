#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 2 ]]; then
  echo "usage: $0 <path> <name>" >&2
  exit 64
fi

target="$1"
name="$2"

if [[ ! -e "${target}" ]]; then
  echo "IPFS target does not exist: ${target}" >&2
  exit 66
fi

if ! command -v ipfs >/dev/null 2>&1; then
  echo "ipfs CLI is required" >&2
  exit 69
fi

cid="$(ipfs add --cid-version=1 --quieter --recursive --pin=true "${target}" | tail -n 1)"
if [[ -z "${cid}" ]]; then
  echo "ipfs add did not return a CID for ${target}" >&2
  exit 70
fi

printf '{"name":"%s","cid":"%s","path":"/ipfs/%s"}\n' "${name}" "${cid}" "${cid}"
