#!/usr/bin/env bash
# unify-deploy-ledger.sh — end the two-ledger split on a box.
#
# THE DEFECT, measured on host-01 on 2026-08-09 (graph:
# ops-host01-unledgered-rolls-and-fleet-skew). The box kept TWO deploy ledgers
# and their coverage was not overlapping-and-incomplete — it was DISJOINT. Of
# eleven binary changes that day:
#
#     5 recorded ONLY in /opt/spacedatanetwork/deploy-ledger.log
#     4 recorded ONLY in /var/log/sdn-deploy-lock.ledger
#     2 recorded in NEITHER
#     0 recorded in BOTH
#
# An agent that read one file and not the other reached a confident, wrong
# conclusion — and did: a P1 was filed for a roll that was fully ledgered, in
# the other file. "Unledgered" was a property of the reader.
#
# WHAT THIS SCRIPT DOES, AND WHAT IT DELIBERATELY DOES NOT.
#
# It does NOT merge the two files. Interleaving two partial records by hand
# produces a third partial record, which the reconciliation lane explicitly
# refused to create. History is preserved exactly as written.
#
# It makes ONE of them the live operator mirror and turns the other into a
# symlink to it, so every future writer — whichever path it happens to know —
# appends to the same bytes. open(2) follows symlinks, so no writer needs to
# change.
#
# WHICH SURVIVES, and why it is not the nicer name:
#   /var/log/sdn-deploy-lock.ledger  <- SURVIVES
#   /opt/spacedatanetwork/deploy-ledger.log -> symlink to it
# The system path lives outside every bundle root, so it stays valid across a
# bundle swap, a bundle-root move, and a box whose install path changes. The
# per-install path cannot promise that. (The name is a historical artifact of
# the deploy lock; the file is the operator ledger.)
#
# AUTHORITY IS UNCHANGED. The authoritative record is the in-bundle
# <bundle-root>/deploy-ledger.jsonl written by update.Apply as a PRECONDITION of
# the mutation (internal/update/deployledger.go). Writability of the ledger is
# implied by writability of the thing being changed, so an apply that cannot be
# recorded cannot happen. The file this script unifies is the human-readable
# fleet-wide MIRROR — never a veto, never the thing whose absence excuses a
# missing record.
#
# Idempotent: safe to run repeatedly, and safe to run on a box that has only
# one of the two files.
set -euo pipefail

SYSTEM_LEDGER="/var/log/sdn-deploy-lock.ledger"
INSTALL_LEDGER="${SDN_INSTALL_LEDGER:-/opt/spacedatanetwork/deploy-ledger.log}"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
DRY_RUN="false"

usage() {
    cat >&2 <<EOF
Usage: $0 [--dry-run] [--install-ledger <path>]

Retires the per-install deploy ledger in favour of the one system-wide operator
mirror, preserving its history verbatim and leaving a symlink in its place.

  --install-ledger <path>   default: ${INSTALL_LEDGER}
  --dry-run                 report what would change and exit 0
EOF
    exit 1
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run) DRY_RUN="true"; shift ;;
        --install-ledger) INSTALL_LEDGER="${2:?}"; shift 2 ;;
        -h|--help) usage ;;
        *) echo "unknown argument: $1" >&2; usage ;;
    esac
done

say() { echo "[unify-deploy-ledger] $*" >&2; }

if [[ -L "$INSTALL_LEDGER" ]]; then
    target="$(readlink "$INSTALL_LEDGER")"
    if [[ "$target" == "$SYSTEM_LEDGER" ]]; then
        say "already unified: $INSTALL_LEDGER -> $SYSTEM_LEDGER"
        exit 0
    fi
    say "REFUSING: $INSTALL_LEDGER is a symlink to $target, which is neither the system ledger nor a plain file."
    exit 1
fi

if [[ ! -e "$INSTALL_LEDGER" ]]; then
    say "no per-install ledger at $INSTALL_LEDGER; nothing to retire."
    if [[ "$DRY_RUN" == "true" ]]; then exit 0; fi
    mkdir -p "$(dirname "$INSTALL_LEDGER")"
    ln -s "$SYSTEM_LEDGER" "$INSTALL_LEDGER"
    say "created the pointer anyway: $INSTALL_LEDGER -> $SYSTEM_LEDGER (so a writer that knows only this path lands on the one live file)"
    exit 0
fi

CLOSED="${INSTALL_LEDGER}.closed-${STAMP}"
say "system ledger (SURVIVES): $SYSTEM_LEDGER ($( [[ -f $SYSTEM_LEDGER ]] && wc -l < "$SYSTEM_LEDGER" || echo 0 ) lines)"
say "install ledger (RETIRED): $INSTALL_LEDGER ($(wc -l < "$INSTALL_LEDGER") lines) -> $CLOSED"

if [[ "$DRY_RUN" == "true" ]]; then
    say "dry run: nothing changed"
    exit 0
fi

mkdir -p "$(dirname "$SYSTEM_LEDGER")"
touch "$SYSTEM_LEDGER"

# The closing marker goes on BOTH files: on the retired one so a reader who
# opens the archive knows where the story continues, and on the survivor so a
# reader of the live file knows a second, disjoint history exists and where it
# is. A pointer that only exists in one direction is how the split happened.
{
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) LEDGER-CLOSED this per-install deploy ledger is RETIRED (owner ruling 2026-08-09, task ops-update-server-push-upgrade-in-place)."
    echo "    Its history above is preserved verbatim and was deliberately NOT merged: interleaving two partial records produces a third partial record."
    echo "    THE LIVE OPERATOR LEDGER IS NOW: ${SYSTEM_LEDGER}"
    echo "    THE AUTHORITATIVE RECORD IS: <bundle-root>/deploy-ledger.jsonl, written by update.Apply as a precondition of the mutation."
} >> "$INSTALL_LEDGER"

{
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) LEDGER-UNIFIED this is now the ONE operator deploy ledger on this box (owner ruling 2026-08-09, task ops-update-server-push-upgrade-in-place)."
    echo "    The previously-disjoint per-install ledger was retired and archived at ${CLOSED}; ${INSTALL_LEDGER} is now a symlink to this file."
    echo "    Its history was NOT merged into this file — read the archive for events before this line."
    echo "    Authoritative record: <bundle-root>/deploy-ledger.jsonl (update.Apply refuses to mutate the bundle until that line is on disk). This file is the human-readable mirror."
} >> "$SYSTEM_LEDGER"

mv "$INSTALL_LEDGER" "$CLOSED"
ln -s "$SYSTEM_LEDGER" "$INSTALL_LEDGER"

say "UNIFIED. $INSTALL_LEDGER -> $SYSTEM_LEDGER (archive: $CLOSED)"
say "verify: readlink $INSTALL_LEDGER && tail -3 $SYSTEM_LEDGER"
