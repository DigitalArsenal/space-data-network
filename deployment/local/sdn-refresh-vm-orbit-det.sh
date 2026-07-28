#!/usr/bin/env bash
#
# sdn-refresh-vm-orbit-det.sh — refresh the spacedatanetwork CLI on the owner's
# LAN dev VM (ssh alias vm-orbit-det-01) as part of a release.
#
#   ./deployment/local/sdn-refresh-vm-orbit-det.sh --image sdn-node:release-amd64
#   ./deployment/local/sdn-refresh-vm-orbit-det.sh --from-host root@sdn.spaceaware.io
#   ./deployment/local/sdn-refresh-vm-orbit-det.sh --image ... --dry-run
#
# WHY THIS SCRIPT EXISTS
# ----------------------
# The VM was rebuilt ~2026-07-24 and its whole install vanished. It came back
# only because a human noticed: it was in no truth file and no deploy path, so
# nothing ever refreshed it. This is that path. It is registered in
# deployment/topology.json under dev_hosts.vm-orbit-det-01.
#
# THE THING THIS SCRIPT EXISTS TO GET RIGHT
# -----------------------------------------
# The binary is DYNAMICALLY linked against libwasmedge.so.0, and the VM has no
# wasmedge install of its own. Binary and .so must move TOGETHER or the bare
# command dies with "libwasmedge.so.0: cannot open shared object file".
#
# So both artifacts are extracted from ONE source — the same release image, or
# the same host — and swapped together. They cannot drift apart by accident,
# because there is no code path here that updates one without the other. A
# manifest of both sha256s is written on the VM so skew is detectable later.
#
# WHAT THIS SCRIPT WILL NEVER DO
# ------------------------------
#   * create a systemd unit, timer, or schedule — this host produces nothing,
#     and the OD pause law binds producers;
#   * run `init`, `key import`, or generate/copy ANY key material — a node
#     identity on this VM is an OWNER decision (surfaced 2026-07-28, still open);
#   * build on the VM, or pull a toolchain onto it.
#
# It is deliberately NON-FATAL to a release: a dev VM being unreachable (it is on
# a private LAN and may simply be off) must never fail a good production deploy.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

TARGET="${SDN_VM_ORBIT_DET_HOST:-vm-orbit-det-01}"
REMOTE_LIB_DIR='$HOME/.local/lib/spacedatanetwork'
REMOTE_BIN='$HOME/.local/bin/spacedatanetwork'
IMAGE=""
FROM_HOST=""
DRY_RUN="no"

RED=$'\033[31m'; GRN=$'\033[32m'; YLW=$'\033[33m'; BLU=$'\033[34m'; NC=$'\033[0m'
info() { printf '%s==>%s %s\n' "$BLU" "$NC" "$*"; }
ok()   { printf '%s ok %s %s\n' "$GRN" "$NC" "$*"; }
warn() { printf '%swarn%s %s\n' "$YLW" "$NC" "$*"; }
die()  { printf '%sfail%s %s\n' "$RED" "$NC" "$*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --image)     IMAGE="$2"; shift 2 ;;
    --from-host) FROM_HOST="$2"; shift 2 ;;
    --target)    TARGET="$2"; shift 2 ;;
    --dry-run)   DRY_RUN="yes"; shift ;;
    -h|--help)   sed -n '3,12p' "$0"; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

[[ -n "$IMAGE" || -n "$FROM_HOST" ]] || die "need --image <tag> or --from-host <ssh-target>"
[[ -n "$IMAGE" && -n "$FROM_HOST" ]] && die "--image and --from-host are mutually exclusive"

STAGE="$(mktemp -d)"
cleanup() { rm -rf "$STAGE"; }
trap cleanup EXIT

# ---- source the two artifacts, together, from one place ----------------------
extract_from_image() {
  command -v docker >/dev/null 2>&1 || die "docker not found locally"
  docker image inspect "$IMAGE" >/dev/null 2>&1 || die "image not found locally: $IMAGE (build it first; never build on a host)"

  # The image must be linux/amd64: the VM is x86_64. An arm64 dev image (e.g.
  # the local test node's) would install a binary that cannot exec at all.
  local arch
  arch="$(docker image inspect "$IMAGE" --format '{{.Architecture}}')"
  [[ "$arch" == "amd64" ]] || die "image $IMAGE is $arch, but vm-orbit-det-01 is x86_64 — rebuild with --platform linux/amd64"

  info "extracting binary + libwasmedge from ${IMAGE} (single source, so they stay in step)"
  local cid
  cid="$(docker create "$IMAGE")"
  # shellcheck disable=SC2064
  trap "docker rm -f '$cid' >/dev/null 2>&1 || true; rm -rf '$STAGE'" EXIT
  docker cp "${cid}:/app/spacedatanetwork" "${STAGE}/spacedatanetwork"
  docker cp "${cid}:/opt/wasmedge/lib/libwasmedge.so.0.1.0" "${STAGE}/libwasmedge.so.0.1.0"
  docker rm -f "$cid" >/dev/null 2>&1 || true
}

extract_from_host() {
  info "copying the LIVE artifacts from ${FROM_HOST} (parity with what is actually running)"
  scp -q "${FROM_HOST}:/opt/spacedatanetwork/bin/spacedatanetwork" "${STAGE}/spacedatanetwork"
  scp -q "${FROM_HOST}:/opt/spacedatanetwork/.wasmedge/lib/libwasmedge.so.0.1.0" "${STAGE}/libwasmedge.so.0.1.0"
}

if [[ -n "$IMAGE" ]]; then extract_from_image; else extract_from_host; fi

[[ -s "${STAGE}/spacedatanetwork" ]]      || die "no binary extracted"
[[ -s "${STAGE}/libwasmedge.so.0.1.0" ]]  || die "no libwasmedge extracted"

# Refuse to ship anything that is not a linux/x86-64 ELF. This is the check that
# would have caught the Mach-O arm64 artifacts sitting in the source tree.
if command -v file >/dev/null 2>&1; then
  file "${STAGE}/spacedatanetwork" | grep -q 'ELF 64-bit.*x86-64' \
    || die "extracted binary is not a linux/x86-64 ELF: $(file -b "${STAGE}/spacedatanetwork")"
fi

BIN_SHA="$(shasum -a 256 "${STAGE}/spacedatanetwork" | cut -d' ' -f1)"
LIB_SHA="$(shasum -a 256 "${STAGE}/libwasmedge.so.0.1.0" | cut -d' ' -f1)"
ok "binary     sha256 ${BIN_SHA:0:16}…"
ok "libwasmedge sha256 ${LIB_SHA:0:16}…"

if [[ "$DRY_RUN" == "yes" ]]; then
  info "[dry-run] would refresh ${TARGET}; stopping here"
  exit 0
fi

# ---- reachability: a dev VM being off must not fail a release ----------------
if ! ssh -o ConnectTimeout=10 -o BatchMode=yes "$TARGET" true 2>/dev/null; then
  warn "${TARGET} is unreachable (private LAN, may simply be powered off) — skipping, NOT failing"
  exit 0
fi

# ---- idempotence: skip the 85MB push when already in step -------------------
# Default each side to the literal "none" so a MISSING artifact cannot shift the
# fields and make the other one's sha get reported as its own.
REMOTE_STATE="$(ssh -o ConnectTimeout=10 "$TARGET" "
  b=\$(sha256sum ${REMOTE_LIB_DIR}/spacedatanetwork 2>/dev/null | cut -d' ' -f1)
  l=\$(sha256sum ${REMOTE_LIB_DIR}/wasmedge/libwasmedge.so.0.1.0 2>/dev/null | cut -d' ' -f1)
  echo \"\${b:-none} \${l:-none}\"" 2>/dev/null || echo "none none")"
REMOTE_BIN_SHA="$(awk '{print $1}' <<<"$REMOTE_STATE")"
REMOTE_LIB_SHA="$(awk '{print $2}' <<<"$REMOTE_STATE")"

if [[ "$REMOTE_BIN_SHA" == "$BIN_SHA" && "$REMOTE_LIB_SHA" == "$LIB_SHA" ]]; then
  ok "${TARGET} already in step with this release — nothing to do"
  exit 0
fi
if [[ "$REMOTE_BIN_SHA" == "none" ]]; then
  info "remote binary ABSENT -> installing ${BIN_SHA:0:16}…"
else
  info "remote binary ${REMOTE_BIN_SHA:0:16}… -> ${BIN_SHA:0:16}…"
fi
if [[ "$REMOTE_LIB_SHA" != "none" && "$REMOTE_LIB_SHA" != "$LIB_SHA" ]]; then
  info "libwasmedge  ${REMOTE_LIB_SHA:0:16}… -> ${LIB_SHA:0:16}… (moves WITH the binary)"
fi

# ---- ship both, then swap both ----------------------------------------------
info "shipping to ${TARGET} (no sudo required; user-local layout under ~/.local)"
ssh -o ConnectTimeout=15 "$TARGET" "mkdir -p ${REMOTE_LIB_DIR}/wasmedge ${REMOTE_LIB_DIR}/.staging \$HOME/.local/bin"
scp -q "${STAGE}/spacedatanetwork"     "${TARGET}:.local/lib/spacedatanetwork/.staging/spacedatanetwork"
scp -q "${STAGE}/libwasmedge.so.0.1.0" "${TARGET}:.local/lib/spacedatanetwork/.staging/libwasmedge.so.0.1.0"

ssh -o ConnectTimeout=20 "$TARGET" "set -e
L=${REMOTE_LIB_DIR}
# Swap BOTH artifacts before anything runs again. Neither is in use: this host
# runs no daemon, which is exactly why a plain mv is safe here.
chmod 755 \$L/.staging/spacedatanetwork
mv -f \$L/.staging/spacedatanetwork      \$L/spacedatanetwork
mv -f \$L/.staging/libwasmedge.so.0.1.0  \$L/wasmedge/libwasmedge.so.0.1.0
ln -sf libwasmedge.so.0.1.0 \$L/wasmedge/libwasmedge.so.0
ln -sf libwasmedge.so.0.1.0 \$L/wasmedge/libwasmedge.so
rmdir \$L/.staging 2>/dev/null || true

# Launcher shim — rewritten every refresh so it can never drift from the layout.
cat > ${REMOTE_BIN} <<'SHIM'
#!/bin/sh
# spacedatanetwork launcher — vm-orbit-det-01 (dev/test). MANAGED FILE:
# rewritten by deployment/local/sdn-refresh-vm-orbit-det.sh on every refresh.
#
# User-local layout because this VM has no passwordless sudo. The binary is
# dynamically linked against libwasmedge.so.0 and the VM has no wasmedge
# install, so the loader is pointed at the copy shipped beside the binary.
LIBDIR=\"\$HOME/.local/lib/spacedatanetwork\"
LD_LIBRARY_PATH=\"\$LIBDIR/wasmedge\${LD_LIBRARY_PATH:+:\$LD_LIBRARY_PATH}\"
export LD_LIBRARY_PATH
exec \"\$LIBDIR/spacedatanetwork\" \"\$@\"
SHIM
chmod 755 ${REMOTE_BIN}

# PATH must be fixed ABOVE Ubuntu's non-interactive bail in ~/.bashrc, or
# 'ssh vm-orbit-det-01 spacedatanetwork ...' cannot find the command even though
# an interactive login can. Guarded so repeated refreshes never duplicate it.
if ! grep -q SDN_LOCAL_BIN_PATH \$HOME/.bashrc 2>/dev/null; then
  cp -a \$HOME/.bashrc \$HOME/.bashrc.bak-\$(date -u +%Y%m%dT%H%M%SZ) 2>/dev/null || true
  T=\$(mktemp)
  {
    echo '# SDN_LOCAL_BIN_PATH — must stay ABOVE the non-interactive bail below.'
    echo 'case \":\$PATH:\" in'
    echo '  *\":\$HOME/.local/bin:\"*) ;;'
    echo '  *) PATH=\"\$HOME/.local/bin:\$PATH\" ;;'
    echo 'esac'
    echo 'export PATH'
    echo
    cat \$HOME/.bashrc
  } > \"\$T\"
  mv \"\$T\" \$HOME/.bashrc
  chmod 644 \$HOME/.bashrc
fi

# Manifest: makes binary/lib skew detectable without re-downloading either.
cat > \$L/RELEASE-MANIFEST <<MAN
refreshed_at=\$(date -u +%Y-%m-%dT%H:%M:%SZ)
source=${IMAGE:-${FROM_HOST}}
binary_sha256=${BIN_SHA}
libwasmedge_sha256=${LIB_SHA}
note=binary and libwasmedge are extracted from ONE source and swapped together
MAN
"

# ---- verify BOTH shell modes, because only one of them was ever broken -------
info "verifying…"
VER_NONINT="$(ssh -o ConnectTimeout=15 "$TARGET" 'spacedatanetwork version 2>&1 | head -1' || true)"
VER_LOGIN="$(ssh -o ConnectTimeout=15 "$TARGET" 'bash -lc "spacedatanetwork version" 2>&1 | head -1' || true)"

[[ "$VER_NONINT" == version=* ]] || die "non-interactive ssh cannot run the command: ${VER_NONINT}"
[[ "$VER_LOGIN"  == version=* ]] || die "login shell cannot run the command: ${VER_LOGIN}"

ok "non-interactive: ${VER_NONINT}"
ok "login shell:     ${VER_LOGIN}"
ok "${TARGET} refreshed (no unit, no schedule, no identity — by design)"
