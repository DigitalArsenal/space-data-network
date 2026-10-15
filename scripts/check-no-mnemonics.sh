#!/usr/bin/env bash
# scripts/check-no-mnemonics.sh — BIP-39 mnemonic / wallet-secret guard.
#
#   ./scripts/check-no-mnemonics.sh                      # staged content (.husky/pre-commit)
#   ./scripts/check-no-mnemonics.sh --tracked            # every tracked file (CI, oss-preflight)
#   ./scripts/check-no-mnemonics.sh --history <range>    # every line added in <range>
#
# SEC-001 (2026-10-15): this guard used to read STAGED content only, so a real
# 24-word node seed committed before it existed (sdn-js/verify-identity.mjs)
# sat in the public repository unseen. The scanner is now
# scripts/check-no-mnemonics.mjs, which covers the index, the whole tracked
# tree and git history in any file type; this wrapper keeps the hook path.
# It never prints a matched phrase.
set -euo pipefail

ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

if [[ ! -f "$ROOT/scripts/wordlists/bip39-english-wordlist.txt" ]]; then
  echo "[check-no-mnemonics] ERROR: wordlist data file missing at scripts/wordlists/bip39-english-wordlist.txt" >&2
  exit 2
fi
if ! command -v node >/dev/null 2>&1; then
  echo "[check-no-mnemonics] ERROR: node is required (scripts/check-no-mnemonics.mjs)" >&2
  exit 2
fi

if [[ $# -eq 0 ]]; then
  set -- --staged
fi
if node "$ROOT/scripts/check-no-mnemonics.mjs" "$@"; then
  exit 0
else
  status=$?
fi
if [[ "$status" -eq 1 ]]; then
  echo "[check-no-mnemonics] RESULT: FAILED -- matched secret text is intentionally not shown above." >&2
fi
exit "$status"
