#!/usr/bin/env bash
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [[ -z "$ROOT" ]]; then
  echo "[oss-preflight] ERROR: must run inside a git repository" >&2
  exit 2
fi

cd "$ROOT"

fail=0
if command -v rg >/dev/null 2>&1; then
  FILTER_CMD="rg"
else
  FILTER_CMD="grep"
fi

echo "[oss-preflight] Repo: $ROOT"

# SEC-001 (verifier S5): an encrypted seed backup or a PEM private key is never
# tracked, whatever its password; the generators also refuse a git work tree.
BLOCKED_PATHS_REGEX='^\.claude/|^\.gocache/|^demo/\.demo-env$|^demo/\.demo-secrets\.json$|(^|/)[^/]*-?wallet\.env$|(^|/)[^/]*\.mnemonic$|(^|/)[^/]*\.backup$|(^|/)[^/]*\.pem$'
# AKIA/ASIA are wrapped in non-alphanumeric boundaries: a real AWS key id is a
# standalone 20-char token, while base64-embedded build artifacts (e.g. the
# single-file UI bundles inlining wasm) legitimately contain AKIA/ASIA as
# substrings of longer alphanumeric runs and must not trip the scan.
# Google API keys likewise need token boundaries: embedded WASM base64 can
# contain the 39-character pattern inside a much longer encoded value.
SECRET_REGEX='-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----|-----BEGIN PRIVATE KEY-----|(^|[^A-Za-z0-9])AKIA[0-9A-Z]{16}($|[^A-Za-z0-9])|(^|[^A-Za-z0-9])ASIA[0-9A-Z]{16}($|[^A-Za-z0-9])|ghp_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{80,}|xox[baprs]-[A-Za-z0-9-]{10,}|(^|[^A-Za-z0-9_+/-])AIza[A-Za-z0-9_-]{35}($|[^A-Za-z0-9_+/-])|sk_live_[A-Za-z0-9]{16,}|ORBPRO_SERVER_PRIVATE_KEY_HEX=[A-Fa-f0-9]{32,}|DERIVATION_SECRET=[A-Fa-f0-9]{32,}'
# A mnemonic assigned to a key (MNEMONIC=, seed_phrase:, mnemonicPhrase := ...).
MNEMONIC_ASSIGN_REGEX='([Mm][Nn][Ee][Mm][Oo][Nn][Ii][Cc]|[Ss][Ee][Ee][Dd]_?[Pp][Hh][Rr][Aa][Ss][Ee])[A-Za-z_]*[^[:alnum:]]{0,3}[:=][^[:alnum:]]{0,3}([a-z]+ ){11,}[a-z]+'
INFRA_REGEX='api\.spaceaware\.io|relay\.spaceaware\.io|tokyo\.relay\.digitalarsenal\.io|209\.182\.234\.97|~/.ssh/sdn_deploy_key'

SECRET_ALLOWLIST_REGEX='^kubo/test/sharness/t0165-keystore-data/|^sdn-server/internal/storefront/payment_stripe_test.go:|^sdn-js/node_modules/|^docs/network-ecosystem-demo\.mjs:|^scripts/oss-preflight.sh:'
# Public BIP-39 test vectors in an assignment shape. These files are excused
# from the MNEMONIC pattern only (verifier S8): a PEM key, AWS key or GitHub
# token in them still fails. Each vector is also pinned by fingerprint in
# scripts/mnemonic-scan-allowlist.txt (the precise layer).
MNEMONIC_ALLOWLIST_REGEX="${SECRET_ALLOWLIST_REGEX}|^docker-compose\.dev\.yaml:[0-9]+:|^sdn-server/cmd/spacedatanetwork/identity_wizard_cli_test\.go:[0-9]+:|^sdn-server/internal/wasm/hdwallet_test\.go:[0-9]+:"
INFRA_ALLOWLIST_REGEX='^scripts/oss-preflight.sh:'

echo "[oss-preflight] 1/11 Checking blocked tracked paths..."
if [[ "$FILTER_CMD" == "rg" ]]; then
  blocked_hits="$(git ls-files | rg -n "$BLOCKED_PATHS_REGEX" || true)"
else
  blocked_hits="$(git ls-files | grep -nE "$BLOCKED_PATHS_REGEX" || true)"
fi
if [[ -n "$blocked_hits" ]]; then
  echo "[oss-preflight] FAIL: blocked paths are still tracked:"
  echo "$blocked_hits"
  fail=1
else
  echo "[oss-preflight] PASS: no blocked tracked paths"
fi

echo "[oss-preflight] 2/11 Scanning tracked files for high-risk secret patterns..."
secret_scan() { # <pattern> <allowlist>
  local hits
  hits="$(git grep -n -I -E -e "$1" || true)"
  if [[ -n "$hits" ]]; then
    if [[ "$FILTER_CMD" == "rg" ]]; then
      hits="$(printf '%s\n' "$hits" | rg -v "$2" || true)"
    else
      hits="$(printf '%s\n' "$hits" | grep -Ev "$2" || true)"
    fi
  fi
  [[ -n "$hits" ]] && printf '%s\n' "$hits"
  return 0
}
secret_hits="$(secret_scan "$SECRET_REGEX" "$SECRET_ALLOWLIST_REGEX"; secret_scan "$MNEMONIC_ASSIGN_REGEX" "$MNEMONIC_ALLOWLIST_REGEX")"
if [[ -n "$secret_hits" ]]; then
  # path:line only — echoing the matched line would publish the secret in CI logs.
  echo "[oss-preflight] FAIL: possible secret material detected (matched text withheld):"
  printf '%s\n' "$secret_hits" | cut -d: -f1,2
  fail=1
else
  echo "[oss-preflight] PASS: no high-risk secret patterns detected"
fi

echo "[oss-preflight] 3/11 Checking tracked files for production endpoint leaks..."
infra_hits="$(git grep -n -I -E -e "$INFRA_REGEX" || true)"
if [[ -n "$infra_hits" ]]; then
  if [[ "$FILTER_CMD" == "rg" ]]; then
    infra_hits="$(printf '%s\n' "$infra_hits" | rg -v "$INFRA_ALLOWLIST_REGEX" || true)"
  else
    infra_hits="$(printf '%s\n' "$infra_hits" | grep -Ev "$INFRA_ALLOWLIST_REGEX" || true)"
  fi
fi
if [[ -n "$infra_hits" ]]; then
  echo "[oss-preflight] FAIL: production endpoint or host references detected:"
  echo "$infra_hits"
  fail=1
else
  echo "[oss-preflight] PASS: no production host leaks detected"
fi

echo "[oss-preflight] 4/11 Enforcing application-blind Go..."
node scripts/check-no-app-specific-go.mjs || fail=1

echo "[oss-preflight] 5/11 Verifying the legacy-state purge migration..."
node --test scripts/purge-legacy-supplemental-omm-state.test.mjs || fail=1

# sdn-js ran two incompatible libp2p majors once, held together by ~370 lines of
# hand-written shims in sdn-js/src/helia.ts, and GHSA-vrf4-mx87-p53w had no
# in-range fix for as long as that stood. The split is closed; this check now
# asserts the opposite invariant - one copy of each package on the critical path
# - so a dependency bump cannot re-split it quietly and bring the shims back.
echo "[oss-preflight] 6/11 Checking the sdn-js helia/libp2p layering..."
node scripts/check-sdn-js-dependency-layering.mjs || fail=1

# sdn-server and the vendored kubo tree are two independent Go modules with no
# go.work and no import relationship, kept on the same libp2p by hand. Both keep
# compiling when they drift; only the peers stop interoperating.
echo "[oss-preflight] 7/11 Checking sdn-server/kubo libp2p lockstep..."

# SEC-001 (2026-10-15): a real 24-word node seed sat in a tracked file for
# months because the mnemonic guard only read STAGED content. These steps scan
# every tracked file and the lines the checked-out history added, in any file
# type, and hold every trust anchor to deployment/identity/anchors.json.
echo "[oss-preflight] 8/11 Scanning every tracked file for BIP-39 mnemonics..."
node scripts/check-no-mnemonics.mjs --tracked || fail=1

# History: the commits this checkout carries beyond origin/main (a shallow CI
# checkout: the tip commit, i.e. its whole tree). SDN_MNEMONIC_HISTORY_RANGE
# overrides it — `--all` audits every ref (deployment/identity/ROTATION.md).
history_range="${SDN_MNEMONIC_HISTORY_RANGE:-}"
if [[ -z "$history_range" ]]; then
  if [[ "$(git rev-parse --is-shallow-repository 2>/dev/null)" == "true" ]]; then
    history_range="HEAD"
  elif git rev-parse -q --verify origin/main >/dev/null 2>&1; then
    history_range="origin/main..HEAD"
  else
    history_range="HEAD"
  fi
fi
echo "[oss-preflight] 9/11 Scanning lines added in ${history_range} for BIP-39 mnemonics..."
node scripts/check-no-mnemonics.mjs --history ${history_range} || fail=1

echo "[oss-preflight] 10/11 Checking no tracked file trusts a denylisted identity..."
node scripts/check-no-denylisted-identity.mjs || fail=1

echo "[oss-preflight] 11/11 Checking every trust anchor reads deployment/identity/anchors.json..."
node deployment/identity/sync-anchors.mjs --check || fail=1
node --test deployment/identity/anchors.test.mjs scripts/check-no-mnemonics.test.mjs || fail=1

if [[ "$fail" -ne 0 ]]; then
  echo "[oss-preflight] RESULT: FAILED"
  exit 1
fi

echo "[oss-preflight] RESULT: PASSED"
