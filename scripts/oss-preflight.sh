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

BLOCKED_PATHS_REGEX='^\.claude/|^\.gocache/|^demo/\.demo-env$|^demo/\.demo-secrets\.json$|(^|/)[^/]*-?wallet\.env$|(^|/)[^/]*\.mnemonic$'
# AKIA/ASIA are wrapped in non-alphanumeric boundaries: a real AWS key id is a
# standalone 20-char token, while base64-embedded build artifacts (e.g. the
# single-file UI bundles inlining wasm) legitimately contain AKIA/ASIA as
# substrings of longer alphanumeric runs and must not trip the scan.
# Google API keys likewise need token boundaries: embedded WASM base64 can
# contain the 39-character pattern inside a much longer encoded value.
SECRET_REGEX='-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----|-----BEGIN PRIVATE KEY-----|(^|[^A-Za-z0-9])AKIA[0-9A-Z]{16}($|[^A-Za-z0-9])|(^|[^A-Za-z0-9])ASIA[0-9A-Z]{16}($|[^A-Za-z0-9])|ghp_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{80,}|xox[baprs]-[A-Za-z0-9-]{10,}|(^|[^A-Za-z0-9_+/-])AIza[A-Za-z0-9_-]{35}($|[^A-Za-z0-9_+/-])|sk_live_[A-Za-z0-9]{16,}|ORBPRO_SERVER_PRIVATE_KEY_HEX=[A-Fa-f0-9]{32,}|DERIVATION_SECRET=[A-Fa-f0-9]{32,}|(MNEMONIC|SEED_PHRASE)="?([a-z]+ ){11,}[a-z]+'
INFRA_REGEX='api\.spaceaware\.io|relay\.spaceaware\.io|tokyo\.relay\.digitalarsenal\.io|209\.182\.234\.97|~/.ssh/sdn_deploy_key'

SECRET_ALLOWLIST_REGEX='^kubo/test/sharness/t0165-keystore-data/|^sdn-server/internal/storefront/payment_stripe_test.go:|^sdn-js/node_modules/|^docs/network-ecosystem-demo\.mjs:|^scripts/oss-preflight.sh:'
INFRA_ALLOWLIST_REGEX='^scripts/oss-preflight.sh:'

echo "[oss-preflight] 1/5 Checking blocked tracked paths..."
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

echo "[oss-preflight] 2/5 Scanning tracked files for high-risk secret patterns..."
secret_hits="$(git grep -n -I -E -e "$SECRET_REGEX" || true)"
if [[ -n "$secret_hits" ]]; then
  if [[ "$FILTER_CMD" == "rg" ]]; then
    secret_hits="$(printf '%s\n' "$secret_hits" | rg -v "$SECRET_ALLOWLIST_REGEX" || true)"
  else
    secret_hits="$(printf '%s\n' "$secret_hits" | grep -Ev "$SECRET_ALLOWLIST_REGEX" || true)"
  fi
fi
if [[ -n "$secret_hits" ]]; then
  echo "[oss-preflight] FAIL: possible secret material detected:"
  echo "$secret_hits"
  fail=1
else
  echo "[oss-preflight] PASS: no high-risk secret patterns detected"
fi

echo "[oss-preflight] 3/5 Checking tracked files for production endpoint leaks..."
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

echo "[oss-preflight] 4/5 Enforcing application-blind Go..."
node scripts/check-no-app-specific-go.mjs || fail=1

echo "[oss-preflight] 5/5 Verifying the legacy-state purge migration..."
node --test scripts/purge-legacy-supplemental-omm-state.test.mjs || fail=1

if [[ "$fail" -ne 0 ]]; then
  echo "[oss-preflight] RESULT: FAILED"
  exit 1
fi

echo "[oss-preflight] RESULT: PASSED"
