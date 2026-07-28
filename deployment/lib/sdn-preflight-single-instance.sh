#!/usr/bin/env bash
#
# sdn-preflight-single-instance.sh — ONE BOX, ONE NODE.
#
#   sdn-preflight-single-instance.sh --target root@host          # remote box
#   sdn-preflight-single-instance.sh --target local              # this machine
#   sdn-preflight-single-instance.sh --target local --expect sdn-local-test
#
# OWNER LAW, 2026-07-28, verbatim:
#   "never ever have more than one instance running on a box from here on out
#    with our current deployment."
#
# This is the structural enforcement. Every deploy/install path refuses to start
# a second SDN daemon on a box that already runs one, and names what it found so
# the operator can act instead of guess.
#
# WHY A SHARED SCRIPT AND NOT A CHECK IN EACH TOOL
# ------------------------------------------------
# The law is only as good as its least careful caller. One implementation, one
# detection list, one override — so a new deploy path cannot quietly ship with a
# weaker version of the check.
#
# WHAT COUNTS AS AN INSTANCE
# --------------------------
# A DAEMON that joins the network under its own peer identity:
#   * an sdn-server daemon      (`spacedatanetwork daemon`)
#   * an SDN-patched kubo node  (`ipfs daemon` out of an SDN install root)
#   * a container running either of the above
# What does NOT count, and must not trip this check:
#   * a PLAIN kubo/ipfs blockstore that an SDN node uses as storage (host-01's
#     ipfs.service on :5002 is the sidecar's blockstore, not a second node);
#   * CLI invocations (`spacedatanetwork version`, `key export`, …) — the
#     vm-orbit-det-01 install is a CLI only and must stay allowed;
#   * a unit that exists but is stopped/disabled.
#
# --expect NAME lets a tool declare "this one instance is mine": an existing
# instance whose unit/container matches NAME is an UPGRADE, not a violation.
#
# EXIT CODES
#   0  no conflicting instance (or only the expected one) — safe to proceed
#   3  conflicting instance found — caller MUST refuse
#   1  usage / unreachable target

set -uo pipefail

TARGET=""
EXPECT=""
QUIET="no"

RED=$'\033[31m'; GRN=$'\033[32m'; YLW=$'\033[33m'; NC=$'\033[0m'

while [[ $# -gt 0 ]]; do
  case "$1" in
    --target) TARGET="$2"; shift 2 ;;
    --expect) EXPECT="$2"; shift 2 ;;
    --quiet)  QUIET="yes"; shift ;;
    -h|--help) sed -n '3,12p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 1 ;;
  esac
done
[[ -n "$TARGET" ]] || { echo "need --target <ssh-target|local>" >&2; exit 1; }

say() { [[ "$QUIET" == "yes" ]] || printf '%s\n' "$*"; }

# The probe runs identically locally and remotely. Output is one record per
# line: KIND<TAB>NAME<TAB>DETAIL
read -r -d '' PROBE <<'PROBE_EOF' || true
# --- systemd units actively RUNNING an SDN daemon -----------------------------
if command -v systemctl >/dev/null 2>&1; then
  for unit in $(systemctl list-units --type=service --state=running --no-legend --plain 2>/dev/null \
                | awk '{print $1}' | grep -Ei 'sdn|spacedata' || true); do
    exec_line=$(systemctl show "$unit" -p ExecStart --no-pager 2>/dev/null)
    # A plain kubo blockstore is NOT an instance. An SDN-patched kubo IS.
    case "$exec_line" in
      *spacedatanetwork*daemon*|*sdn-kubo*|*/opt/sdn-*)
        cfg=$(printf '%s' "$exec_line" | grep -oE '\-\-config [^ "]+' | head -1 | cut -d' ' -f2)
        printf 'UNIT\t%s\t%s\n' "$unit" "${cfg:-no --config}" ;;
      *ipfs*daemon*)
        # only if it runs out of an SDN install root
        case "$exec_line" in
          */opt/sdn*|*/opt/spacedatanetwork*) printf 'UNIT\t%s\t%s\n' "$unit" "SDN-rooted kubo" ;;
        esac ;;
    esac
  done
fi

# --- bare processes (no unit, e.g. a hand-started daemon) ---------------------
# `ps axo pid=,args=` is portable across Linux and macOS; pgrep -a is NOT (macOS
# pgrep has no -a, which produced entries with empty detail and false hits).
# Match only an ACTUAL daemon invocation, and never this probe's own grep.
ps axo pid=,args= 2>/dev/null \
  | grep -E '(^|/)(spacedatanetwork|ipfs)[[:space:]]+daemon([[:space:]]|$)' \
  | grep -v 'grep -E' \
  | while read -r pid rest; do
      case "$rest" in
        *ipfs*daemon*)
          # a plain kubo blockstore is not an instance; only an SDN-rooted one is
          case "$rest" in
            */opt/sdn*|*/opt/spacedatanetwork*) printf 'PROC\tpid=%s\t%s\n' "$pid" "$rest" ;;
          esac ;;
        *) printf 'PROC\tpid=%s\t%s\n' "$pid" "$rest" ;;
      esac
    done

# --- containers ---------------------------------------------------------------
# Only containers actually RUNNING a node. Build tooling (buildx/buildkit
# builders are literally named after the project) is not an instance.
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
  docker ps --format '{{.Names}}\t{{.Image}}\t{{.Command}}' 2>/dev/null \
    | grep -Ei 'sdn|spacedata' \
    | grep -Eiv 'buildkit|buildx|builder|registry' \
    | while IFS="$(printf '\t')" read -r n i c; do
        case "$c" in
          *daemon*|*spacedatanetwork*) printf 'CONTAINER\t%s\t%s\n' "$n" "$i" ;;
        esac
      done
fi
PROBE_EOF

if [[ "$TARGET" == "local" ]]; then
  FOUND="$(bash -c "$PROBE" 2>/dev/null)"
else
  FOUND="$(ssh -o ConnectTimeout=10 -o BatchMode=yes "$TARGET" "bash -s" <<<"$PROBE" 2>/dev/null)"
  if [[ $? -ne 0 && -z "$FOUND" ]]; then
    echo "${RED}preflight: cannot reach ${TARGET}${NC}" >&2
    exit 1
  fi
fi

FOUND="$(grep -v '^[[:space:]]*$' <<<"${FOUND:-}" || true)"
[[ -z "$FOUND" ]] && { say "${GRN}preflight: no SDN instance running on ${TARGET} — one-box-one-node satisfied${NC}"; exit 0; }

# Filter out the instance the caller already owns (an upgrade in place).
CONFLICTS="$FOUND"
if [[ -n "$EXPECT" ]]; then
  CONFLICTS="$(grep -v -F "	${EXPECT}	" <<<"$FOUND" || true)"
  CONFLICTS="$(grep -v -F "	${EXPECT}" <<<"$CONFLICTS" || true)"
fi
CONFLICTS="$(grep -v '^[[:space:]]*$' <<<"${CONFLICTS:-}" || true)"

if [[ -z "$CONFLICTS" ]]; then
  say "${GRN}preflight: only the expected instance (${EXPECT}) is running on ${TARGET} — proceeding as an upgrade${NC}"
  exit 0
fi

printf '%spreflight: ONE BOX, ONE NODE — %s already runs an SDN instance:%s\n' "$RED" "$TARGET" "$NC" >&2
while IFS=$'\t' read -r kind name detail; do
  [[ -z "${kind:-}" ]] && continue
  printf '    %-9s %-46s %s\n' "$kind" "$name" "$detail" >&2
done <<<"$CONFLICTS"
printf '%s\n' "" >&2
printf '  Owner law 2026-07-28: never more than one instance running on a box.\n' >&2
printf '  Stop/disable the existing instance first, or give the new node its own box.\n' >&2
printf '  DEV-ONLY override: SDN_ALLOW_MULTI_INSTANCE=1 (logged, never for a cluster host).\n' >&2
exit 3
