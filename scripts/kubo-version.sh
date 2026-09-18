#!/usr/bin/env bash
# Prints the Kubo release tag this suite SHIPS, e.g. "v0.39.0".
#
# suite.versions.json kubo.shipped is the single source of truth. Before this
# script the same tag was copy-pasted into five places that had no way to
# disagree out loud:
#
#   .github/workflows/beta-release-artifacts.yml   env KUBO_VERSION
#   .github/workflows/live-dht-cross-platform.yml  env KUBO_VERSION
#   .github/workflows/release-deploy.yml           a hardcoded dist.ipfs.tech URL
#   deployment/release/build-local-node-bundle.sh  KUBO_VERSION default
#   deployment/release/cut-release-local.sh        KUBO_VERSION default
#
# and a SIXTH, unrelated string — kubo/version.go CurrentVersionNumber — was
# what the node actually reported as its `kubo_version` over the API. It said
# 0.40.0-dev for months while every one of the five shipped v0.39.0.
#
# Usage:
#   KUBO_VERSION="$(scripts/kubo-version.sh)"
#
# In a GitHub workflow, export it once for the whole job:
#   echo "KUBO_VERSION=$(scripts/kubo-version.sh)" >> "$GITHUB_ENV"

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MANIFEST="$ROOT/suite.versions.json"

if [[ ! -f "$MANIFEST" ]]; then
  echo "kubo-version: $MANIFEST not found" >&2
  exit 1
fi

version=""

# node is the parser of record: it is what generate-suite-version-info.js uses
# to read the very same field, so the two cannot disagree about JSON. Every
# lane that calls this already sets Node up.
if command -v node >/dev/null 2>&1; then
  version="$(node -e '
    const fs = require("fs");
    const m = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
    const v = m.kubo && m.kubo.shipped;
    if (typeof v !== "string") { process.exit(1); }
    process.stdout.write(v.trim());
  ' "$MANIFEST" 2>/dev/null || true)"
fi

# Fallback for a lane that runs before Node exists (a bare-bones runner, a
# container mid-bootstrap). Deliberately anchored to the two-line shape the
# manifest is committed in, and the result is validated below either way, so a
# reformatted manifest fails loudly here instead of silently yielding "".
if [[ -z "$version" ]]; then
  version="$(sed -n '/"kubo"[[:space:]]*:/,/}/s/.*"shipped"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$MANIFEST" | head -1)"
fi

if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "kubo-version: suite.versions.json kubo.shipped is not a release tag (got '${version}')" >&2
  echo "kubo-version: expected something like \"v0.39.0\"" >&2
  exit 1
fi

printf '%s\n' "$version"
