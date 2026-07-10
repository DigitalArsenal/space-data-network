#!/usr/bin/env bash
# scripts/check-no-mnemonics.sh
#
# Pre-commit guard: blocks commits that would introduce a BIP-39 mnemonic
# phrase or other obvious wallet/private-key secret markers into staged
# file content. Written after config/dev-wallet.env (gitignored, but still
# present in older git history) was found to hold a real dev mnemonic; see
# coordination/ for the remediation task this script implements.
#
# Detects, in STAGED file content only (git index, not the working tree):
#   (a) a run of 12 or more consecutive words that are ALL members of the
#       BIP-39 English wordlist (scripts/wordlists/bip39-english-wordlist.txt)
#       -- real mnemonics are always exactly 12/15/18/21/24 words, and 12
#       is the minimum, so any such run is mnemonic-shaped.
#   (b) inside .env-style files: the literal markers "mnemonic"/
#       "seed_phrase" used as a key, an "xprv..." extended private key, or
#       a PEM "-----BEGIN ... PRIVATE KEY-----" block.
#
# On a match, this script prints the offending file path and a remediation
# hint. It never echoes the matched secret text itself.
#
# Wired into .husky/pre-commit (this repo's existing hook mechanism; see
# scripts/oss-preflight.sh for the sibling tracked-file scan run at
# pre-push/CI time via scripts/ci-local.sh).

set -euo pipefail

ROOT="$(git rev-parse --show-toplevel)"
cd "$ROOT"

WORDLIST="$ROOT/scripts/wordlists/bip39-english-wordlist.txt"
WORDLIST_REL="scripts/wordlists/bip39-english-wordlist.txt"

if [[ ! -f "$WORDLIST" ]]; then
  echo "[check-no-mnemonics] ERROR: wordlist data file missing at $WORDLIST_REL" >&2
  exit 2
fi

is_env_like() {
  local base
  base="$(basename "$1")"
  case "$base" in
    .env|.env.*|*.env|*.env.local|*.env.production) return 0 ;;
    *) return 1 ;;
  esac
}

is_binary_staged() {
  # `git diff --cached --numstat` prints "-" for added/removed line counts
  # on binary paths.
  local marker
  marker="$(git diff --cached --numstat -- "$1" | head -1 | awk '{print $1}')"
  [[ "$marker" == "-" ]]
}

# A run of 12+ consecutive BIP-39 wordlist words anywhere in the staged
# content of $1. Reads from the git index (":$1"), not the working tree, so
# only what is actually about to be committed is scanned.
#
# Valid BIP-39 mnemonics are exactly 12/15/18/21/24 words, but a real
# mnemonic embedded in a file is often adjacent to another wordlist word
# with no separating punctuation (e.g. `SEED="abandon abandon ... art"` --
# "seed" is itself a BIP-39 word), which shifts the *exact* run length off
# the valid set and would cause a false negative. Since 12 is the minimum
# valid mnemonic length, any run >= 12 already contains a 12-word (or
# longer valid-length) mnemonic-shaped window, so that is the trigger.
# Empirically, ordinary prose in this repo (READMEs, docs) never produced a
# run above 10 -- see the guard's test suite / task verification notes.
#
# DISTINCTNESS FILTER (added when the embedded SpaceAware UI bundle started
# inlining hd-wallet code, U1.2): minified wallet JS tokenizes into long
# runs of REPEATED BIP-39 words ("return base key curve this base key curve
# base key depth ..." -- base/key/curve/this/depth are all wordlist words),
# as did a Go duration literal ("time minute time minute time hour ...").
# Real mnemonics are 12/15/18/21/24 words sampled independently from 2048,
# so 12+ words with fewer than 9 DISTINCT among them is astronomically
# improbable (repeats happen, near-total repetition does not). Requiring
# >= 9 distinct words in the run keeps every real-mnemonic detection
# (including the all-zeros test vector "abandon x11 about" -- SEE BELOW: a
# run whose distinct count is < 9 but which contains the word "abandon" 11+
# times IS still flagged, covering degenerate low-entropy test vectors)
# while eliminating code-token false positives.
scan_bip39_sequences() {
  git show ":$1" 2>/dev/null | awk -v wordlist="$WORDLIST" '
    BEGIN {
      while ((getline w < wordlist) > 0) {
        if (w == "" || w ~ /^#/) continue
        inlist[w] = 1
      }
      close(wordlist)
      run = 0
      hit = 0
    }
    function run_reset() {
      run = 0
      delete seen
      distinct = 0
      abandons = 0
    }
    {
      line = tolower($0)
      gsub(/[^a-z]+/, " ", line)
      n = split(line, toks, " ")
      for (i = 1; i <= n; i++) {
        t = toks[i]
        if (t == "") continue
        if (t in inlist) {
          run++
          if (!(t in seen)) { seen[t] = 1; distinct++ }
          if (t == "abandon") abandons++
          # Trigger: mnemonic-shaped run with real-mnemonic word diversity,
          # OR the canonical degenerate all-zeros vector (abandon x11+).
          if (run >= 12 && (distinct >= 9 || abandons >= 11)) hit = 1
        } else {
          run_reset()
        }
      }
    }
    END {
      exit (hit ? 0 : 1)
    }
  '
}

# Obvious secret markers inside .env-style staged content: mnemonic/seed
# keys, xprv extended private keys, PEM private key blocks.
scan_env_keywords() {
  git show ":$1" 2>/dev/null | grep -Eiq -- \
    '-----BEGIN [A-Z ]*PRIVATE KEY-----|xprv[0-9A-Za-z]{10,}|(^|[^a-z_])mnemonic([^a-z_]|$)|(^|[^a-z_])seed_phrase([^a-z_]|$)'
}

staged_files="$(git diff --cached --name-only --diff-filter=ACMR)"

fail=0

while IFS= read -r f; do
  [[ -z "$f" ]] && continue
  [[ "$f" == "$WORDLIST_REL" ]] && continue
  is_binary_staged "$f" && continue

  if scan_bip39_sequences "$f"; then
    echo "[check-no-mnemonics] BLOCKED: $f contains a run of 12+ consecutive words drawn entirely from the BIP-39 wordlist (real mnemonics are always 12/15/18/21/24 words -- looks like a mnemonic phrase)." >&2
    echo "[check-no-mnemonics] Remediation: remove the phrase from this file. Keep wallet secrets only in gitignored config/*wallet*.env files (see config/dev-wallet.env.example for the template) and never stage them. If a real secret was exposed, rotate it." >&2
    fail=1
  fi

  if is_env_like "$f" && scan_env_keywords "$f"; then
    echo "[check-no-mnemonics] BLOCKED: $f (env-style file) contains a mnemonic/seed_phrase/xprv/PRIVATE KEY marker." >&2
    echo "[check-no-mnemonics] Remediation: move the secret value out of tracked files into a gitignored config/*wallet*.env (see config/dev-wallet.env.example) and reference it locally only." >&2
    fail=1
  fi
done <<< "$staged_files"

if [[ "$fail" -ne 0 ]]; then
  echo "[check-no-mnemonics] RESULT: FAILED -- commit blocked. Matched secret text is intentionally not shown above." >&2
  exit 1
fi

exit 0
