#!/usr/bin/env bash
# What a CI lane ACTUALLY sees: a clean checkout with no node_modules anywhere.
#
# Written after three consecutive red runs on main. A suite wired into the
# preflight — which installs nothing, deliberately — passed locally and failed
# on a runner, three times, because this tree has packages a fresh clone does
# not. Hiding only the ROOT node_modules is not enough: suites resolve from
# sdn-js/ too (scripts/module-placement.test.mjs does createRequire over
# sdn-js/package.json), so a root-only hide still gave a false pass.
#
#   ./scripts/repro-clean-checkout.sh preflight
#
# WHAT IT DOES NOT CATCH: a suite that reaches OUTSIDE this repository.
# tests/isomorphic/seed-orbpro-module-catalog.test.mjs resolves
# repoRoot/../../../OrbPro/..., which exists in the stack-of-repos layout on a
# developer's machine and in no CI checkout — hiding node_modules says nothing
# about it. If a suite resolves a path above the repo root, it cannot run in
# this repo's CI at all; quarantine it in scripts/check-no-orphan-test-suites.mjs.
#
# Any ci-local.sh mode works, but `preflight` is the one this exists for: it is
# the only lane that runs Node suites with no install in front of them.
set -u
cd "$(dirname "$0")/.."

MODE="${1:-preflight}"
TREES=(node_modules sdn-js/node_modules desktop/node_modules webui/node_modules)
HIDDEN=()

restore () {
  for tree in "${HIDDEN[@]:-}"; do
    [ -n "$tree" ] && [ -d "$tree.hidden" ] && mv "$tree.hidden" "$tree"
  done
}
# Restore on EVERY exit path, including Ctrl-C: leaving a developer's tree
# without its dependencies would be a far worse bug than the one this catches.
trap restore EXIT INT TERM

for tree in "${TREES[@]}"; do
  if [ -d "$tree" ]; then mv "$tree" "$tree.hidden" && HIDDEN+=("$tree"); fi
done
echo "hidden: ${HIDDEN[*]:-none}"

CI=true ./scripts/ci-local.sh "$MODE"
