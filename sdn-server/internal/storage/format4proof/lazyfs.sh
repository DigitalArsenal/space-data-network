#!/usr/bin/env bash
# LazyFS power-loss rounds for store format 4 through SDN's storage layer
# (format4proof, TestProofLazyFSPowerLoss; contract §4). LazyFS is a FUSE
# file system, so this runs in a Linux container and cleans up after itself.
#
#   sdn-server/internal/storage/format4proof/lazyfs.sh <work dir> [rounds] [arm]
#
# <work dir> receives the logs and results (it is not the mount). arm is s
# (format 4, default), f2 or f1. The container is the static-WasmEdge build
# image (SDN_LAZYFS_IMAGE, default sdn-wasmedge-static:74db37e14, linux/amd64;
# its prefix carries the runtime patches the threaded engines need). Inside,
# it builds LazyFS (pinned), builds this package's test binary, mounts LazyFS,
# prewarms every engine's AOT artifact (a store open never compiles; round
# 1 of the first build found none in the container and every round failed to
# open), runs the rounds, then the NEGATIVE CONTROL (fsync and fdatasync are no-ops
# in the writer through LD_PRELOAD; the engine's host I/O is C and calls
# them through libc), which must report a loss.
#
# LIMIT (as scripts/lazyfs-dir-durability.sh): LazyFS caches only file data,
# so a lost un-fsynced directory entry is not exercised.
set -euo pipefail

if [[ "${1:-}" != "--inner" ]]; then
  WORK="${1:?usage: lazyfs.sh <work dir> [rounds] [arm]}"
  ROUNDS="${2:-100}"
  ARM="${3:-s}"
  HERE="$(cd "$(dirname "$0")" && pwd)"
  REPO="$(cd "$HERE/../../../.." && pwd)"
  mkdir -p "$WORK/aot-cache"
  WORK="$(cd "$WORK" && pwd)"
  MODCACHE="$(cd "$REPO/sdn-server" && go env GOMODCACHE)"
  # The engines' AOT artifacts persist in <work dir>/aot-cache (the daemon's
  # cache directory inside): the harness prewarms them before any round, and
  # a rerun reuses them.
  exec docker run --rm --platform linux/amd64 \
    --device /dev/fuse --cap-add SYS_ADMIN --security-opt apparmor=unconfined \
    -v "$REPO":/src:ro -v "$WORK":/work -v "$MODCACHE":/gomod -v "$WORK/aot-cache":/root/.cache/flatsql-aot \
    -e GOMODCACHE=/gomod -e GOFLAGS=-mod=mod -e ROUNDS="$ROUNDS" -e ARM="$ARM" \
    "${SDN_LAZYFS_IMAGE:-sdn-wasmedge-static:74db37e14}" \
    bash /src/sdn-server/internal/storage/format4proof/lazyfs.sh --inner
fi

# ---- inside the container ----------------------------------------------------
W=/work
L=/tmp/lazyfs-run
mkdir -p "$L"
export DEBIAN_FRONTEND=noninteractive
if ! command -v fusermount3 >/dev/null || ! command -v cmake >/dev/null; then
  apt-get update -qq >/dev/null
  apt-get install -y -qq build-essential cmake git pkg-config libfuse3-dev fuse3 >/dev/null
fi
# LazyFS, pinned (as scripts/lazyfs-dir-durability.sh).
if [[ ! -x "$L/lazyfs/lazyfs/build/lazyfs" ]]; then
  git clone -q https://github.com/dsrhaslab/lazyfs.git "$L/lazyfs"
  git -C "$L/lazyfs" checkout -q fa7d32e
  (cd "$L/lazyfs/libs/libpcache" && ./build.sh >/dev/null 2>&1)
  (cd "$L/lazyfs/lazyfs" && ./build.sh >/dev/null 2>&1)
fi
LAZYFS="$L/lazyfs/lazyfs/build/lazyfs"
[[ -x "$LAZYFS" ]] || { echo "LazyFS did not build"; exit 1; }

# The test binary (the source is read-only: build from a copy of what the
# build needs).
rm -rf "$L/src" && mkdir -p "$L/src" && cp -a /src/sdn-server /src/scripts "$L/src/"
(cd "$L/src/sdn-server" && WASMEDGE_DIR=/opt/wasmedge-static/prefix GOFLAGS="-mod=mod -p=6" \
  ../scripts/go-with-wasmedge.sh test -c -o "$L/p4proof.test" ./internal/storage/format4proof)

cat > "$L/nosync.c" <<'C'
int fsync(int fd) { (void)fd; return 0; }
int fdatasync(int fd) { (void)fd; return 0; }
int syncfs(int fd) { (void)fd; return 0; }
C
gcc -shared -fPIC -O2 -o "$L/nosync.so" "$L/nosync.c"

MNT="$L/mnt" BACK="$L/back" FIFO="$L/faults.fifo" DONE="$L/faults.done.fifo"
fusermount3 -u "$MNT" 2>/dev/null || true
rm -rf "$MNT" "$BACK" "$FIFO" "$DONE"
mkdir -p "$MNT" "$BACK"
mkfifo "$FIFO" "$DONE"
cat > "$L/lazyfs.toml" <<TOML
[faults]
fifo_path="$FIFO"
fifo_path_completed="$DONE"
[cache]
apply_eviction=false
[cache.simple]
custom_size="4gb"
blocks_per_page=1
[filesystem]
log_all_operations=false
logfile=""
TOML
"$LAZYFS" "$MNT" --config-path "$L/lazyfs.toml" -o allow_other -o modules=subdir -o subdir="$BACK" -f >"$W/lazyfs.log" 2>&1 &
LPID=$!
cleanup() {
  fusermount3 -u "$MNT" 2>/dev/null || true
  kill "$LPID" 2>/dev/null || true
  wait "$LPID" 2>/dev/null || true
}
trap cleanup EXIT
for _ in $(seq 1 100); do mountpoint -q "$MNT" && break; sleep 0.1; done
mountpoint -q "$MNT" || { cat "$W/lazyfs.log"; echo "LazyFS did not mount"; exit 1; }

echo "machine: $(uname -m), $(nproc) CPUs; LazyFS fa7d32e; arm $ARM; $ROUNDS rounds per scenario"
run() { # run <label> <negative so or empty>
  P4PROOF_WORK="$W/$1-work" P4PROOF_OUT="$W/$1-out" P4PROOF_LAZYFS_MOUNT="$MNT" P4PROOF_LAZYFS_FIFO="$FIFO" \
    P4PROOF_LAZYFS_FIFO_DONE="$DONE" P4PROOF_LAZYFS_ROUNDS="$ROUNDS" P4PROOF_LAZYFS_ARM="$ARM" P4PROOF_LAZYFS_NEGATIVE="$2" \
    "$L/p4proof.test" -test.run '^TestProofLazyFSPowerLoss$' -test.v -test.count=1 -test.timeout 6h 2>&1 | grep -E 'RESULT|FAIL|PASS|ok|panic' || true
}
mkdir -p "$W/trials-work" "$W/trials-out" "$W/negative-work" "$W/negative-out"
run trials ""
run negative "$L/nosync.so"
