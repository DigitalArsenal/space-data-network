#!/usr/bin/env bash
# update-upstream-ipfs.sh - Refresh or verify upstream IPFS WebUI/Desktop mirrors.
#
# UI CLEAN SLATE (owner ruling 2026-07-24): the sdn-js UI overlay program and
# its vendored upstream-webui snapshot were deleted pending the owner's new UI
# codebase, so this script now manages only the upstream mirror subtrees.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MODE="update"

usage() {
  cat <<'EOF'
Usage: scripts/update-upstream-ipfs.sh [--check]

Refreshes the upstream IPFS WebUI and IPFS Desktop mirror trees.

Options:
  --check   Do not mutate mirrors; nothing further to verify while the UI
            program is removed (owner clean-slate ruling 2026-07-24).

Environment:
  WEBUI_BRANCH    Branch or ref to pull from webui-upstream (default: main)
  DESKTOP_BRANCH  Branch or ref to pull from desktop-upstream (default: main)
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --check)
      MODE="check"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

if [[ "$MODE" == "check" ]]; then
  echo "upstream mirror check: sdn-js UI overlays removed (clean slate); nothing to verify"
  exit 0
fi

"$ROOT/scripts/subtree-update.sh" webui
"$ROOT/scripts/subtree-update.sh" desktop
