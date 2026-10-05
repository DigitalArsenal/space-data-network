#!/usr/bin/env bash
# Prints the Kubo release tag SDN links, e.g. "v0.43.1".
#
# Kubo is linked into the node binary, so its version is the
# github.com/ipfs/kubo requirement in sdn-server/go.mod, and nothing else is a
# source for it. Two things still need it as a tag:
#
#   - the CI jobs that download the ipfs CLI as a build tool, to compute and
#     pin the UI asset CIDs (env KUBO_VERSION, which scripts/check-kubo-pin.js
#     holds equal to this);
#   - scripts that want to name the linked release.
#
# Usage:
#   KUBO_VERSION="$(scripts/kubo-version.sh)"

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GO_MOD="$ROOT/sdn-server/go.mod"

if [[ ! -f "$GO_MOD" ]]; then
  echo "kubo-version: $GO_MOD not found" >&2
  exit 1
fi

version="$(sed -n 's/^[[:space:]]*github\.com\/ipfs\/kubo[[:space:]]\{1,\}\(v[^[:space:]]*\)[[:space:]]*$/\1/p' "$GO_MOD" | head -1)"

if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "kubo-version: sdn-server/go.mod does not require github.com/ipfs/kubo at a release version (got '${version}')" >&2
  exit 1
fi

printf '%s\n' "$version"
