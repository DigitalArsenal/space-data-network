#!/usr/bin/env bash
# Power-loss trials for the partition store's C host I/O module under LazyFS
# (design §18 T5 #5). LINUX ONLY: LazyFS is a FUSE file system.
#
#   scripts/lazyfs-dir-durability.sh [trials]      (default 1000)
#
# Needs FUSE 3 (libfuse3-dev, fuse3), cmake, g++, pkg-config and git, and
# either SDN_FLATSQLRT_TEST_BIN (a prebuilt `go test -c` binary of
# sdn-server/internal/flatsqlrt) or WASMEDGE_DIR (a patched prefix from
# scripts/build-static-wasmedge.sh) plus Go, to build one. In Docker run it
# with --privileged (or --device /dev/fuse --cap-add SYS_ADMIN).
#
# It runs the trials, then a NEGATIVE CONTROL that skips the segment's data
# sync and must report losses: a harness that cannot see a missing sync
# proves nothing.
#
# LIMIT: LazyFS forwards mkdir and create to the backing file system at once
# and caches only file data (lazyfs/src/lazyfs.cpp lfs_mkdir, lfs_create), so
# these trials cannot drop an un-fsynced directory entry. The directory-entry
# half of #5 needs a block-level replay on a Linux host: put ext4 on
# dm-log-writes, run the same trials, and replay the log to every FLUSH mark
# (xfstests' replay-log), checking the invariant at each.
set -euo pipefail

TRIALS="${1:-1000}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="${LAZYFS_WORK:-/tmp/sdn-lazyfs}"
LAZYFS_REPO="${LAZYFS_REPO:-https://github.com/dsrhaslab/lazyfs.git}"
LAZYFS_COMMIT="${LAZYFS_COMMIT:-fa7d32e}"

[[ "$(uname -s)" == "Linux" ]] || { echo "LazyFS needs Linux and FUSE" >&2; exit 2; }
mkdir -p "$WORK"

# 1. LazyFS, pinned.
if [[ ! -x "$WORK/lazyfs/lazyfs/build/lazyfs" ]]; then
  rm -rf "$WORK/lazyfs"
  git clone -q "$LAZYFS_REPO" "$WORK/lazyfs"
  git -C "$WORK/lazyfs" checkout -q "$LAZYFS_COMMIT"
  (cd "$WORK/lazyfs/libs/libpcache" && ./build.sh >/dev/null)
  (cd "$WORK/lazyfs/lazyfs" && ./build.sh >/dev/null)
fi
LAZYFS="$WORK/lazyfs/lazyfs/build/lazyfs"

# 2. The test binary.
BIN="${SDN_FLATSQLRT_TEST_BIN:-}"
if [[ -z "$BIN" ]]; then
  BIN="$WORK/flatsqlrt.test"
  (cd "$ROOT/sdn-server" && ../scripts/go-with-wasmedge.sh test -c -o "$BIN" ./internal/flatsqlrt)
fi

# 3. Mount.
MNT="$WORK/mnt" BACK="$WORK/back" FIFO="$WORK/faults.fifo" DONE="$WORK/faults.done.fifo"
fusermount3 -u "$MNT" 2>/dev/null || true
rm -rf "$MNT" "$BACK" "$FIFO" "$DONE"
mkdir -p "$MNT" "$BACK"
mkfifo "$FIFO" "$DONE"
cat > "$WORK/lazyfs.toml" <<TOML
[faults]
fifo_path="$FIFO"
fifo_path_completed="$DONE"
[cache]
apply_eviction=false
[cache.simple]
custom_size="1gb"
blocks_per_page=1
[filesystem]
log_all_operations=false
logfile=""
TOML
"$LAZYFS" "$MNT" --config-path "$WORK/lazyfs.toml" -o allow_other -o modules=subdir -o subdir="$BACK" -f >"$WORK/lazyfs.log" 2>&1 &
LAZYFS_PID=$!
cleanup() {
  fusermount3 -u "$MNT" 2>/dev/null || true
  kill "$LAZYFS_PID" 2>/dev/null || true
  wait "$LAZYFS_PID" 2>/dev/null || true
}
trap cleanup EXIT
for _ in $(seq 1 100); do
  mountpoint -q "$MNT" && break
  sleep 0.1
done
mountpoint -q "$MNT" || { cat "$WORK/lazyfs.log" >&2; echo "LazyFS did not mount" >&2; exit 1; }

# 4. Trials, then the negative control in a fresh subtree.
run() {
  SDN_LAZYFS_MOUNT="$MNT/$1" SDN_LAZYFS_FIFO="$FIFO" SDN_LAZYFS_FIFO_DONE="$DONE" \
    SDN_LAZYFS_TRIALS="$TRIALS" SDN_LAZYFS_NEGATIVE="$2" \
    "$BIN" -test.run '^TestLazyFSDirectoryDurability$' -test.v -test.count=1
}
mkdir -p "$MNT/trials" "$MNT/negative"
run trials 0
run negative 1
