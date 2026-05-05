#!/usr/bin/env bash
# update-upstream-ipfs.sh - Refresh or verify upstream IPFS WebUI/Desktop mirrors.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MODE="update"

usage() {
  cat <<'EOF'
Usage: scripts/update-upstream-ipfs.sh [--check]

Refreshes the upstream IPFS WebUI and IPFS Desktop mirror trees, refreshes the
SDN vendor snapshot consumed by overlays, and runs focused mirror boundary
tests.

Options:
  --check   Do not mutate mirrors; verify generated vendor files and focused
            boundary tests are current.

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

run_focused_checks() {
  node "$ROOT/scripts/sync-upstream-webui-into-sdn-js.mjs" --check
  npm --prefix "$ROOT/sdn-js" exec vitest run \
    src/ui/upstream-webui/branding.test.ts \
    src/ui/upstream-webui/upstream-mirror-boundary.test.ts
}

if [[ "$MODE" == "check" ]]; then
  run_focused_checks
  exit 0
fi

"$ROOT/scripts/subtree-update.sh" webui
"$ROOT/scripts/subtree-update.sh" desktop
node "$ROOT/scripts/sync-upstream-webui-into-sdn-js.mjs"
run_focused_checks
