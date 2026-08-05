#!/usr/bin/env bash
# verify-remote-checksum.sh — stage a locally-built artifact on a remote host
# and prove the bytes that landed match the bytes that were built, BEFORE
# any caller treats the staged file as live.
#
# sdn-deploy-checksum-manifest: the real host-01/host-02 binary cutovers
# recorded in deployment/topology.json (the binary_*_2026* entries) are a
# manual recipe — local Docker build, scp, ABI gate, guarded restart — run
# by hand under /run/sdn-deploy.lock, NOT through deploy.sh's deploy_binary()
# (that path is a separate, older mechanism; see
# ops-deploy-sh-full-node-builds-on-host). Nothing in that manual recipe
# verified the scp'd bytes beyond the ssh transport itself. This script is
# the integrity step for that recipe: it computes the SHA-256 independently
# on both ends and fails closed on any mismatch or missing hash tool,
# leaving no staged file behind on failure.
#
# This is INTEGRITY ONLY (checksum), not authenticity (signature). Signing
# the manifest is a Seal Council decision gated on the owner naming a key
# (deployment/signing.json); this script does not wait on that and does not
# claim to provide it.
#
# Usage:
#   verify-remote-checksum.sh <local-file> <ssh-target> <remote-staged-path>
#
# <ssh-target> is anything `ssh`/`scp` accepts (uses your ambient SSH config,
# e.g. an alias from ~/.ssh/config — same convention as deploy.sh's SSH_USER/
# SSH_KEY, but this script takes the target as one ssh-style arg so it can be
# used ad hoc against any host alias without deploy.sh's servers.yaml).
#
# On success: the verified file is left at <remote-staged-path> on the
# target and this prints its sha256; the caller performs the actual atomic
# swap into its final location (this script never moves anything into a
# live path — staging and cutover are deliberately separate steps).
# On failure: exits non-zero, removes the staged remote file, prints which
# side computed what.
set -euo pipefail

usage() {
    echo "Usage: $0 <local-file> <ssh-target> <remote-staged-path>" >&2
    exit 1
}

[[ $# -eq 3 ]] || usage
LOCAL_FILE="$1"
SSH_TARGET="$2"
REMOTE_PATH="$3"

RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m'
log_info() { echo -e "${BLUE}[verify-checksum]${NC} $1"; }
log_success() { echo -e "${GREEN}[verify-checksum]${NC} $1"; }
log_error() { echo -e "${RED}[verify-checksum]${NC} $1" >&2; }

[[ -f "$LOCAL_FILE" ]] || { log_error "local file not found: $LOCAL_FILE"; exit 1; }

calculate_sha256() {
    local file="$1"
    if command -v sha256sum &> /dev/null; then
        sha256sum "$file" | awk '{print $1}'
    elif command -v shasum &> /dev/null; then
        shasum -a 256 "$file" | awk '{print $1}'
    else
        log_error "No SHA-256 tool found locally (need sha256sum or shasum)"
        exit 1
    fi
}

EXPECTED="$(calculate_sha256 "$LOCAL_FILE")"
log_info "local  $(basename "$LOCAL_FILE"): ${EXPECTED}"

log_info "staging to ${SSH_TARGET}:${REMOTE_PATH}..."
scp -o BatchMode=yes -o StrictHostKeyChecking=no "$LOCAL_FILE" "${SSH_TARGET}:${REMOTE_PATH}"

ACTUAL="$(ssh -o BatchMode=yes -o StrictHostKeyChecking=no "$SSH_TARGET" \
    "sha256sum '${REMOTE_PATH}' 2>/dev/null | awk '{print \$1}' || shasum -a 256 '${REMOTE_PATH}' 2>/dev/null | awk '{print \$1}'")"

if [[ -z "$ACTUAL" ]]; then
    log_error "could not compute a remote hash on ${SSH_TARGET} (no sha256sum/shasum on host) — refusing"
    ssh -o BatchMode=yes -o StrictHostKeyChecking=no "$SSH_TARGET" "rm -f '${REMOTE_PATH}'" || true
    exit 1
fi

log_info "remote $(basename "$REMOTE_PATH"): ${ACTUAL}"

if [[ "$EXPECTED" != "$ACTUAL" ]]; then
    log_error "CHECKSUM MISMATCH staging $(basename "$LOCAL_FILE") on ${SSH_TARGET}:${REMOTE_PATH}"
    log_error "  local  (built):  $EXPECTED"
    log_error "  remote (staged): $ACTUAL"
    log_error "Refusing — the staged bytes do not match what was built. Removing the staged file."
    ssh -o BatchMode=yes -o StrictHostKeyChecking=no "$SSH_TARGET" "rm -f '${REMOTE_PATH}'" || true
    exit 1
fi

log_success "checksum verified (${EXPECTED}) — ${REMOTE_PATH} on ${SSH_TARGET} matches ${LOCAL_FILE}"
