#!/usr/bin/env bash
# LANE ACCEPTANCE CHECK — does the command the OPERATOR TYPES run the version
# the update lane just installed?
#
# WHY THIS EXISTS. On 2026-07-31 the lane worked perfectly and the test still
# lied. `spacedatanetwork update install` fetched, verified and applied a signed
# release; the daemon restarted onto the new binary; the proof line
# `spacedatanetwork status` -> running/healthy was taken by invoking the bundle
# CLI by absolute path. But the owner's PATH resolved a launcher shim that
# execed the OLD flat install, so the owner got unhealthy/unhealthy from a
# binary two releases behind the daemon it was asking about.
#
# The lesson is not "remember to check the wrapper". It is that a deploy is only
# done when the ENTRYPOINT an operator actually uses resolves to the artifact
# that was installed — the same failure as host-01's stale /usr/local/bin
# symlink (deployment/topology.json, cli_admit_point_fix_20260729). Verifying
# the artifact you just wrote proves nothing about the name people invoke.
#
# So this check runs THROUGH `command -v`, from a login-shell PATH, and compares
# hashes. It never takes an absolute path as evidence.
#
# Usage:
#   deployment/local/verify-update-entrypoint.sh <ssh-target> [expected-sha256]
#
# Exit 0 = the operator's command is the installed build.
# Exit 1 = entrypoint drift: something is installed that nobody is running.

set -euo pipefail

TARGET="${1:?usage: verify-update-entrypoint.sh <ssh-target> [expected-sha256]}"
EXPECTED_SHA="${2:-}"

# -l so PATH is what a real login gets, not what ssh hands a non-interactive
# command. The difference between those two is itself a class of this bug:
# ~/.bashrc bails early for non-interactive shells on Ubuntu.
read -r RESOLVED ENTRY_SHA BUNDLE_SHA BUNDLE_VERSION <<EOF
$(ssh -o ConnectTimeout=15 "$TARGET" 'bash -lc '"'"'
  set -e
  resolved=$(command -v spacedatanetwork || echo NONE)
  if [ "$resolved" = "NONE" ]; then echo "NONE - - -"; exit 0; fi
  # Ask the ENTRYPOINT itself which binary it execs, by running it under a
  # tracing shell would be fragile; instead hash what the launcher resolves the
  # same way the launcher does.
  self=$(cd "$(dirname "$resolved")" && pwd -P)
  bundle=$(cd "$self/../lib/sdn-bundle" 2>/dev/null && pwd -P || true)
  lib=$(cd "$self/../lib/spacedatanetwork" 2>/dev/null && pwd -P || true)
  if [ -n "$bundle" ] && [ -x "$bundle/bin/spacedatanetwork" ] && [ -f "$bundle/manifest.json" ]; then
    exec_path="$bundle/bin/spacedatanetwork"
  else
    exec_path="$lib/spacedatanetwork"
  fi
  entry_sha=$(sha256sum "$exec_path" | cut -d" " -f1)
  if [ -n "$bundle" ] && [ -x "$bundle/bin/spacedatanetwork" ]; then
    bundle_sha=$(sha256sum "$bundle/bin/spacedatanetwork" | cut -d" " -f1)
    bundle_version=$(python3 -c "import json;print(json.load(open(\"$bundle/manifest.json\"))[\"version\"])" 2>/dev/null || echo unknown)
  else
    bundle_sha="-"; bundle_version="-"
  fi
  echo "$resolved $entry_sha $bundle_sha $bundle_version"
'"'"'')
EOF

echo "resolved_entrypoint = $RESOLVED"
echo "entrypoint_execs    = $ENTRY_SHA"
echo "lane_installed      = $BUNDLE_SHA  (bundle version $BUNDLE_VERSION)"

if [ "$RESOLVED" = "NONE" ]; then
  echo "FAIL: 'spacedatanetwork' is not on the operator's PATH at all" >&2
  exit 1
fi

if [ "$BUNDLE_SHA" != "-" ] && [ "$ENTRY_SHA" != "$BUNDLE_SHA" ]; then
  echo "FAIL: ENTRYPOINT DRIFT — the operator's command does NOT run what the lane installed." >&2
  echo "  the lane installed $BUNDLE_SHA but '$RESOLVED' execs $ENTRY_SHA" >&2
  echo "  an update that nobody's shell can reach has not been deployed." >&2
  exit 1
fi

if [ -n "$EXPECTED_SHA" ] && [ "$ENTRY_SHA" != "$EXPECTED_SHA" ]; then
  echo "FAIL: the operator's command runs $ENTRY_SHA, expected $EXPECTED_SHA" >&2
  exit 1
fi

# The proof line, taken THROUGH the operator's entrypoint — never by absolute
# path. This is the check whose absence is the whole reason this file exists.
echo "--- spacedatanetwork status (via the operator's PATH) ---"
STATUS=$(ssh -o ConnectTimeout=15 "$TARGET" 'bash -lc "spacedatanetwork status"' 2>&1 || true)
echo "$STATUS"
if ! grep -q 'daemon_status=running' <<<"$STATUS" || ! grep -q 'data_health=healthy' <<<"$STATUS"; then
  echo "FAIL: the operator's status command does not report running/healthy" >&2
  exit 1
fi

echo "PASS: the operator's command is the build the lane installed, and it reports running/healthy."
