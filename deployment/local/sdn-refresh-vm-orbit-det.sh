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
ALLOW_WALLET_CHANGE="no"
IGNORE_PIN="no"

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
    --allow-wallet-wasm-change) ALLOW_WALLET_CHANGE="yes"; shift ;;
    --ignore-pin) IGNORE_PIN="yes"; shift ;;
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
  docker cp "${cid}:/usr/local/lib/hd-wallet-wasi.wasm" "${STAGE}/hd-wallet-wasi.wasm"
  docker rm -f "$cid" >/dev/null 2>&1 || true
}

extract_from_host() {
  info "copying the LIVE artifacts from ${FROM_HOST} (parity with what is actually running)"
  scp -q "${FROM_HOST}:/opt/spacedatanetwork/bin/spacedatanetwork" "${STAGE}/spacedatanetwork"
  scp -q "${FROM_HOST}:/opt/spacedatanetwork/.wasmedge/lib/libwasmedge.so.0.1.0" "${STAGE}/libwasmedge.so.0.1.0"
  scp -q "${FROM_HOST}:/opt/spacedatanetwork/wasm/hd-wallet-wasi.wasm" "${STAGE}/hd-wallet-wasi.wasm"
}

if [[ -n "$IMAGE" ]]; then extract_from_image; else extract_from_host; fi

[[ -s "${STAGE}/spacedatanetwork" ]]      || die "no binary extracted"
[[ -s "${STAGE}/libwasmedge.so.0.1.0" ]]  || die "no libwasmedge extracted"
[[ -s "${STAGE}/hd-wallet-wasi.wasm" ]]   || die "no hd-wallet-wasi.wasm extracted"

# Refuse to ship anything that is not a linux/x86-64 ELF. This is the check that
# would have caught the Mach-O arm64 artifacts sitting in the source tree.
if command -v file >/dev/null 2>&1; then
  file "${STAGE}/spacedatanetwork" | grep -q 'ELF 64-bit.*x86-64' \
    || die "extracted binary is not a linux/x86-64 ELF: $(file -b "${STAGE}/spacedatanetwork")"
fi

BIN_SHA="$(shasum -a 256 "${STAGE}/spacedatanetwork" | cut -d' ' -f1)"
LIB_SHA="$(shasum -a 256 "${STAGE}/libwasmedge.so.0.1.0" | cut -d' ' -f1)"
WALLET_SHA="$(shasum -a 256 "${STAGE}/hd-wallet-wasi.wasm" | cut -d' ' -f1)"
ok "binary          sha256 ${BIN_SHA:0:16}…"
ok "libwasmedge     sha256 ${LIB_SHA:0:16}…"
ok "hd-wallet-wasi  sha256 ${WALLET_SHA:0:16}…"

if [[ "$DRY_RUN" == "yes" ]]; then
  info "[dry-run] would refresh ${TARGET}; stopping here"
  exit 0
fi

# ---- reachability: a dev VM being off must not fail a release ----------------
if ! ssh -o ConnectTimeout=10 -o BatchMode=yes "$TARGET" true 2>/dev/null; then
  warn "${TARGET} is unreachable (private LAN, may simply be powered off) — skipping, NOT failing"
  exit 0
fi

# ---- PIN: a node whose identity only one binary can read --------------------
# Learned the hard way on 2026-07-28: binary bf498aee could NOT decrypt a
# mnemonic written by bf53e36b ("chacha20poly1305: message authentication
# failed"), because the machine-derived at-rest key is not stable across builds.
# Refreshing the binary silently stranded a freshly created node identity — the
# PeerID is derived at runtime and stored nowhere, so nothing on disk showed it.
#
# A PIN file records "this node's identity is only readable by THIS binary".
# Honour it by default; overriding is an explicit, logged act.
PIN_STATE="$(ssh -o ConnectTimeout=10 "$TARGET" \
  "[ -s ${REMOTE_LIB_DIR}/PIN ] && grep -m1 '^binary_sha256=' ${REMOTE_LIB_DIR}/PIN | cut -d= -f2 || echo none" 2>/dev/null || echo none)"
if [[ "$PIN_STATE" != "none" && -n "$PIN_STATE" ]]; then
  if [[ "$PIN_STATE" == "$BIN_SHA" ]]; then
    ok "PIN present and matches the incoming binary — safe to proceed"
  elif [[ "$IGNORE_PIN" == "yes" ]]; then
    warn "--ignore-pin: overriding a binary PIN on ${TARGET}"
    warn "  pinned:   ${PIN_STATE:0:16}…"
    warn "  incoming: ${BIN_SHA:0:16}…"
    warn "if this node holds an identity, CAPTURE ITS PeerID FIRST:"
    warn "  ssh ${TARGET} 'spacedatanetwork key export --format peerid'"
  else
    warn "${TARGET} is PINNED to binary ${PIN_STATE:0:16}… — refusing to ship ${BIN_SHA:0:16}…"
    ssh -o ConnectTimeout=10 "$TARGET" "cat ${REMOTE_LIB_DIR}/PIN" 2>/dev/null | sed 's/^/     /'
    warn "Nothing was changed. Resolve the pin reason, then re-run with --ignore-pin."
    exit 0
  fi
fi

# ---- idempotence: skip the 85MB push when already in step -------------------
# Default each side to the literal "none" so a MISSING artifact cannot shift the
# fields and make the other one's sha get reported as its own.
REMOTE_STATE="$(ssh -o ConnectTimeout=10 "$TARGET" "
  b=\$(sha256sum ${REMOTE_LIB_DIR}/spacedatanetwork 2>/dev/null | cut -d' ' -f1)
  l=\$(sha256sum ${REMOTE_LIB_DIR}/wasmedge/libwasmedge.so.0.1.0 2>/dev/null | cut -d' ' -f1)
  w=\$(sha256sum ${REMOTE_LIB_DIR}/hd-wallet-wasi.wasm 2>/dev/null | cut -d' ' -f1)
  i=no; [ -s \$HOME/.spacedatanetwork/keys/mnemonic ] && i=yes
  echo \"\${b:-none} \${l:-none} \${w:-none} \$i\"" 2>/dev/null || echo "none none none no")"
REMOTE_BIN_SHA="$(awk '{print $1}' <<<"$REMOTE_STATE")"
REMOTE_LIB_SHA="$(awk '{print $2}' <<<"$REMOTE_STATE")"
REMOTE_WALLET_SHA="$(awk '{print $3}' <<<"$REMOTE_STATE")"
REMOTE_HAS_IDENTITY="$(awk '{print $4}' <<<"$REMOTE_STATE")"

if [[ "$REMOTE_BIN_SHA" == "$BIN_SHA" && "$REMOTE_LIB_SHA" == "$LIB_SHA" && "$REMOTE_WALLET_SHA" == "$WALLET_SHA" ]]; then
  ok "${TARGET} already in step with this release — nothing to do"
  exit 0
fi

# ---- IDENTITY GUARD ----------------------------------------------------------
# The node's PeerID is NOT stored anywhere on disk. It is DERIVED at runtime from
# ~/.spacedatanetwork/keys/mnemonic THROUGH hd-wallet-wasi.wasm. So swapping that
# wasm under an existing identity can silently change the node's PeerID, with
# nothing on disk to compare against afterwards.
#
# This is not hypothetical: the wasm shipped on host-01 (3495017 B) and the one
# baked into the release image (3049366 B) are DIFFERENT files today.
#
# So: if this VM already holds an identity and the incoming wallet wasm differs,
# refuse and make a human decide. Everything else still refreshes normally when
# the operator re-runs with the override.
if [[ "$REMOTE_HAS_IDENTITY" == "yes" && "$REMOTE_WALLET_SHA" != "none" && "$REMOTE_WALLET_SHA" != "$WALLET_SHA" ]]; then
  if [[ "$ALLOW_WALLET_CHANGE" != "yes" ]]; then
    warn "REFUSING to swap hd-wallet-wasi.wasm under an existing node identity."
    warn "  on ${TARGET}: ${REMOTE_WALLET_SHA:0:16}…"
    warn "  incoming:    ${WALLET_SHA:0:16}…"
    warn "The PeerID is derived from the mnemonic THROUGH this wasm and is stored"
    warn "nowhere, so a derivation change would silently re-identify the node."
    warn "If this change is intended, re-run with --allow-wallet-wasm-change and"
    warn "capture the PeerID before and after. Binary/lib were NOT touched."
    exit 0
  fi
  warn "--allow-wallet-wasm-change: swapping the wallet wasm under an existing identity"
  warn "verify the node's PeerID after this completes"
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
scp -q "${STAGE}/hd-wallet-wasi.wasm"  "${TARGET}:.local/lib/spacedatanetwork/.staging/hd-wallet-wasi.wasm"

ssh -o ConnectTimeout=20 "$TARGET" "set -e
L=${REMOTE_LIB_DIR}
# STOPPED-STATE SWAP. The comment that used to sit here said 'this host runs no
# daemon, which is exactly why a plain mv is safe' — that became FALSE on
# 2026-07-30 when the owner directive put a systemd USER unit on this box, and a
# stale assumption in a deploy script is a defect, not a comment. The unit is
# stopped before the swap and started after, so the binary and libwasmedge can
# never be replaced underneath a live process. Idempotent: if no unit is
# present or it is already stopped, WAS_ACTIVE stays 'inactive' and nothing is
# started that was not running before.
WAS_ACTIVE=\$(systemctl --user is-active spacedatanetwork.service 2>/dev/null || true)
if [ \"\$WAS_ACTIVE\" = \"active\" ]; then
  echo 'stopping spacedatanetwork.service for a stopped-state swap'
  systemctl --user stop spacedatanetwork.service
fi
chmod 755 \$L/.staging/spacedatanetwork
mv -f \$L/.staging/spacedatanetwork      \$L/spacedatanetwork
mv -f \$L/.staging/libwasmedge.so.0.1.0  \$L/wasmedge/libwasmedge.so.0.1.0
mv -f \$L/.staging/hd-wallet-wasi.wasm   \$L/hd-wallet-wasi.wasm
ln -sf libwasmedge.so.0.1.0 \$L/wasmedge/libwasmedge.so.0
ln -sf libwasmedge.so.0.1.0 \$L/wasmedge/libwasmedge.so
rmdir \$L/.staging 2>/dev/null || true

# NOTE: ~/.spacedatanetwork (config.yaml + keys/mnemonic) is NEVER touched by
# this script. Identity lifecycle on this VM is an OWNER decision; this path
# only moves executable artifacts.

# Launcher shim — rewritten every refresh so it can never drift from the layout.
cat > ${REMOTE_BIN} <<'SHIM'
#!/bin/sh
# spacedatanetwork launcher — vm-orbit-det-01 (dev/test). MANAGED FILE:
# rewritten by deployment/local/sdn-refresh-vm-orbit-det.sh on every refresh.
#
# User-local layout because this VM has no passwordless sudo. The binary is
# dynamically linked against libwasmedge.so.0 and the VM has no wasmedge
# install, so the loader is pointed at the copy shipped beside the binary.
# OWNER RULING 2026-07-30 (graph: sdn-wasmedge-static-link) is that wasmedge
# belongs INSIDE the executable; when that lands, the LD_LIBRARY_PATH line and
# this whole per-user lib layout are DELETED, not maintained.

# Resolve the install from THIS SCRIPT'S OWN location, never \$HOME. Under sudo
# \$HOME becomes /root, so a \$HOME-relative shim execs a path that does not
# exist (\"/root/.local/lib/spacedatanetwork/spacedatanetwork: not found\") and,
# worse, the binary would read /root/.spacedatanetwork, find no keystore, and
# the identity path can MINT A FRESH IDENTITY. Fail closed instead.
SELF=\$(cd \"\$(dirname \"\$0\")\" && pwd -P)

# RUNTIME dir: wasmedge, the HD wallet wasm, and the at-rest/ownership identity.
# These live OUTSIDE any update bundle on purpose - update.Apply swaps a bundle
# root's CONTENTS, so a runtime kept inside one would be swapped away with the
# release that happened to ship it.
LIBDIR=\$(cd \"\$SELF/../lib/spacedatanetwork\" 2>/dev/null && pwd -P)

# EXECUTABLE: the UPDATE LANE owns it. When a self-contained bundle is present
# it WINS over the flat install, because that is what \`spacedatanetwork update
# install\` just wrote, and the command an operator TYPES has to be the version
# the lane installed.
#
# This is the exact defect of 2026-07-31: the lane swapped the bundle and the
# daemon ran the new binary, but this wrapper kept execing the flat install, so
# \`spacedatanetwork status\` answered from an OLD binary with the old health
# decode and printed unhealthy against a healthy node. Same trap class as the
# stale /usr/local/bin symlink on host-01 (topology cli_admit_point_fix_20260729):
# the artifact moved, the entrypoint did not.
#
# Preferring the bundle STRUCTURALLY is the fix. A per-apply fixup step would
# have to be remembered by every future release path; this cannot be forgotten,
# and it also survives this file being regenerated, because the template that
# regenerates it is this same text.
#
# A bundle is RECOGNISED only on the test bundle.ResolveCurrent itself applies -
# bin/<exe> beside manifest.json - so a half-extracted, staged or abandoned
# directory can never hijack the entrypoint.
BUNDLE=\$(cd \"\$SELF/../lib/sdn-bundle\" 2>/dev/null && pwd -P)
if [ -n \"\$BUNDLE\" ] && [ -x \"\$BUNDLE/bin/spacedatanetwork\" ] && [ -f \"\$BUNDLE/manifest.json\" ]; then
  EXEC=\"\$BUNDLE/bin/spacedatanetwork\"
  OWNERDIR=\"\$BUNDLE\"
elif [ -n \"\$LIBDIR\" ] && [ -x \"\$LIBDIR/spacedatanetwork\" ]; then
  EXEC=\"\$LIBDIR/spacedatanetwork\"
  OWNERDIR=\"\$LIBDIR\"
else
  echo \"spacedatanetwork: no install found beside \$SELF\" >&2
  echo \"  looked for a lane bundle at ../lib/sdn-bundle/bin/spacedatanetwork (beside manifest.json)\" >&2
  echo \"  and a flat install at ../lib/spacedatanetwork/spacedatanetwork\" >&2
  exit 78
fi

# The runtime dir is required even when the executable came from a bundle: the
# wasmedge shared library and the HD wallet wasm are resolved from it below.
if [ -z \"\$LIBDIR\" ]; then
  echo \"spacedatanetwork: runtime dir ../lib/spacedatanetwork is missing (wasmedge + hd-wallet-wasi.wasm live there)\" >&2
  exit 78
fi

# CROSS-USER REFUSAL. The install, the keystore and the at-rest password file
# all belong to ONE user. Running as anyone else reads a different \$HOME and a
# different (usually absent) keystore. Refusing is not a convenience: a silent
# run against an absent keystore is how a stray second identity gets minted.
INSTALL_OWNER=\$(stat -c %U \"\$OWNERDIR\" 2>/dev/null || echo '')
INVOKER=\$(id -un)
if [ -n \"\$INSTALL_OWNER\" ] && [ \"\$INSTALL_OWNER\" != \"\$INVOKER\" ]; then
  echo \"spacedatanetwork: USER-LOCAL install owned by \$INSTALL_OWNER; refusing to run as \$INVOKER.\" >&2
  echo \"  The node identity, its keystore and its at-rest password all belong to \$INSTALL_OWNER.\" >&2
  echo \"  Run instead:  sudo -u \$INSTALL_OWNER -H \$SELF/spacedatanetwork <args>\" >&2
  exit 77
fi

LD_LIBRARY_PATH=\"\$LIBDIR/wasmedge\${LD_LIBRARY_PATH:+:\$LD_LIBRARY_PATH}\"
export LD_LIBRARY_PATH

# The node derives its identity (mnemonic -> PeerID) through this wasm. The
# binary's built-in search paths do NOT include ~/.local, so for a user-local
# install the env var is the ONLY mechanism that finds it — without this,
# 'init' and every identity-dependent command fail. Tracked as a code
# follow-up on the graph: sdn-cli-user-local-wasm-search-path.
HD_WALLET_WASM_PATH=\"\$LIBDIR/hd-wallet-wasi.wasm\"
export HD_WALLET_WASM_PATH

# AT-REST SEAL. A box re-sealed off the machine-derived key and onto an explicit
# password FILE gives the DAEMON that file through a systemd drop-in, but the
# CLI had no equivalent and fell through to the machine-derived default —
# answering \"chacha20poly1305: message authentication failed\" on a perfectly
# intact seal. That is the exact break the owner hit on 2026-07-30.
# PATH ONLY: the value is never read, echoed or logged here.
# Exported only when the file EXISTS, so a not-yet-resealed install keeps the
# machine-derived default. An existing-but-unreadable file is deliberately left
# to the binary, which treats configured-but-unreadable as a loud ERROR rather
# than a silent fallback (sdn-server/internal/config/resolve.go, KeyPassword).
# NEVER the inline SDN_KEY_PASSWORD form (HERMES, seal council 2026-07-30):
# inline also overrides the credstore root in credstore/secrets.go and would
# orphan credentials.enc, and it is visible in ps.
if [ -z \"\${SDN_KEY_PASSWORD}\" ] && [ -z \"\${SDN_KEY_PASSWORD_FILE}\" ] && [ -e \"\$HOME/.sdn-key-password\" ]; then
  SDN_KEY_PASSWORD_FILE=\"\$HOME/.sdn-key-password\"
  export SDN_KEY_PASSWORD_FILE
fi

exec \"\$EXEC\" \"\$@\"
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
hd_wallet_wasi_sha256=${WALLET_SHA}
note=all three artifacts are extracted from ONE source and swapped together
note=hd-wallet-wasi.wasm derives the node PeerID from the mnemonic; changing it
note=under an existing identity is gated behind --allow-wallet-wasm-change
MAN

# Restart ONLY if this script stopped it. A box that was deliberately left
# stopped stays stopped.
if [ \"\$WAS_ACTIVE\" = \"active\" ]; then
  systemctl --user start spacedatanetwork.service
  echo \"daemon restarted: \$(systemctl --user is-active spacedatanetwork.service)\"
fi
"

# ---- verify BOTH shell modes, because only one of them was ever broken -------
info "verifying…"
VER_NONINT="$(ssh -o ConnectTimeout=15 "$TARGET" 'spacedatanetwork version 2>&1 | head -1' || true)"
VER_LOGIN="$(ssh -o ConnectTimeout=15 "$TARGET" 'bash -lc "spacedatanetwork version" 2>&1 | head -1' || true)"

[[ "$VER_NONINT" == version=* ]] || die "non-interactive ssh cannot run the command: ${VER_NONINT}"
[[ "$VER_LOGIN"  == version=* ]] || die "login shell cannot run the command: ${VER_LOGIN}"

ok "non-interactive: ${VER_NONINT}"
ok "login shell:     ${VER_LOGIN}"
ok "${TARGET} refreshed (identity never touched; the unit is stopped and restarted around the swap, not installed, by this script)"
