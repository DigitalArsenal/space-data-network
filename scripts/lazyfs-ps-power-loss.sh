#!/usr/bin/env bash
# Power-loss trials for the FlatSQL partition store engine under LazyFS
# (design §18 T6 #1 and "crash consistency"). LINUX ONLY: LazyFS is a FUSE
# file system. On macOS, run it in a linux/arm64 container:
#
#   docker run --rm --platform linux/arm64 --cpus 4 --cpuset-cpus 0-3 \
#     --device /dev/fuse --cap-add SYS_ADMIN --security-opt apparmor=unconfined \
#     -v <dir holding flatsql/cpp>:/t/src -v "$PWD/scripts":/t ubuntu:24.04 \
#     bash /t/lazyfs-ps-power-loss.sh [trials] [rounds-per-store] [negative-trials]
#
# /t/src/flatsql is the flatsql source at the release SDN embeds (git archive
# of its cpp/ directory); FLATBUFFERS_COMMIT is that release's FlatBuffers pin.
#
# Each trial: host_crash_ingest (flatsql cpp/test/ps/host_crash_test.cpp)
# writes 8 partitions with 2 writers on the LazyFS mount and prints an ACK
# line per durable ack to a file off the mount. After a random 0.1-0.9 s it
# is killed with SIGKILL, then LazyFS drops every unsynced byte
# (lazyfs::clear-cache: the power loss), then host_crash_verify reopens the
# store and checks: every acked record is present with its bytes, pseqs are
# a gap-free prefix, head and lane counters equal a recount, and open read 0
# d-* bytes and parsed 0 frames. A store takes ROUNDS power losses (its acks
# accumulate) before a fresh one; the commit journal alternates by store.
# The NEGATIVE CONTROL LD_PRELOADs fsync/fdatasync to no-ops, so acks are not
# durable: the verifier must then report a loss, which proves it can see one.
#
# LIMIT (as scripts/lazyfs-dir-durability.sh): LazyFS caches only file data,
# so these trials cannot drop an un-fsynced directory entry.
set -uo pipefail
TRIALS="${1:-200}"
ROUNDS="${2:-3}"
NEG_TRIALS="${3:-20}"
W=/work
mkdir -p $W
export DEBIAN_FRONTEND=noninteractive
if ! command -v cmake >/dev/null; then
  apt-get update -qq >/dev/null
  apt-get install -y -qq build-essential cmake git pkg-config libfuse3-dev fuse3 libssl-dev python3 >/dev/null
fi
# LazyFS, pinned (as scripts/lazyfs-dir-durability.sh).
if [[ ! -x $W/lazyfs/lazyfs/build/lazyfs ]]; then
  git clone -q https://github.com/dsrhaslab/lazyfs.git $W/lazyfs
  git -C $W/lazyfs checkout -q fa7d32e
  (cd $W/lazyfs/libs/libpcache && ./build.sh >/dev/null 2>&1)
  (cd $W/lazyfs/lazyfs && ./build.sh >/dev/null 2>&1)
fi
LAZYFS=$W/lazyfs/lazyfs/build/lazyfs
[[ -x $LAZYFS ]] || { echo "LazyFS did not build"; exit 1; }
# flatsql v3.2.0 native, with its FlatBuffers pin.
if [[ ! -x $W/flatsql/cpp/build/flatsql_ps_test ]]; then
  cp -r /t/src/flatsql $W/flatsql
  git clone -q https://github.com/DigitalArsenal/flatbuffers.git $W/flatbuffers
  git -C $W/flatbuffers checkout -q "${FLATBUFFERS_COMMIT:-c72a8bed83d2d1c6cf4c9d229c79fc795ec395ae}"
  (cd $W/flatsql/cpp && cmake -B build -DCMAKE_BUILD_TYPE=Release >/dev/null && cmake --build build -j8 --target flatsql_ps_test >/dev/null 2>&1)
fi
BIN=$W/flatsql/cpp/build/flatsql_ps_test
[[ -x $BIN ]] || { echo "flatsql_ps_test did not build"; exit 1; }
# The negative control's shim.
cat > $W/nosync.c <<'C'
int fsync(int fd) { (void)fd; return 0; }
int fdatasync(int fd) { (void)fd; return 0; }
int syncfs(int fd) { (void)fd; return 0; }
C
gcc -shared -fPIC -O2 -o $W/nosync.so $W/nosync.c

MNT=$W/mnt BACK=$W/back FIFO=$W/faults.fifo DONE=$W/faults.done.fifo
fusermount3 -u $MNT 2>/dev/null || true
rm -rf $MNT $BACK $FIFO $DONE
mkdir -p $MNT $BACK
mkfifo $FIFO $DONE
cat > $W/lazyfs.toml <<TOML
[faults]
fifo_path="$FIFO"
fifo_path_completed="$DONE"
[cache]
apply_eviction=false
[cache.simple]
custom_size="2gb"
blocks_per_page=1
[filesystem]
log_all_operations=false
logfile=""
TOML
$LAZYFS $MNT --config-path $W/lazyfs.toml -o allow_other -o modules=subdir -o subdir=$BACK -f >$W/lazyfs.log 2>&1 &
LPID=$!
for _ in $(seq 1 100); do mountpoint -q $MNT && break; sleep 0.1; done
mountpoint -q $MNT || { cat $W/lazyfs.log; echo "LazyFS did not mount"; exit 1; }
exec 7<>$DONE

powerloss() {
  echo "lazyfs::clear-cache" > $FIFO
  local line
  read -t 60 -r line <&7 || { echo "no clear-cache completion"; return 1; }
  [[ "$line" == finished::clear-cache* ]] || { echo "clear-cache said: $line"; return 1; }
}

# trial <label> <trials> <preload>
trials() {
  local label=$1 n=$2 preload=$3
  local fails=0 acked_total=0 store=0 round=0 journal=0 acks="" dir=""
  for t in $(seq 1 $n); do
    if (( round == 0 )); then
      store=$((store + 1)); journal=$((store % 2))
      dir=$MNT/$label-$store; acks=$W/$label-$store.acks
      rm -rf $dir; mkdir -p $dir; : > $acks
    fi
    round=$((round + 1))
    local ms=$((100 + RANDOM % 800))
    if [[ -n $preload ]]; then
      LD_PRELOAD=$preload $BIN --test=host_crash_ingest --dir=$dir --round=$((store * 100 + round)) --journal=$journal --max-seconds=60 >>$acks 2>/dev/null &
    else
      $BIN --test=host_crash_ingest --dir=$dir --round=$((store * 100 + round)) --journal=$journal --max-seconds=60 >>$acks 2>/dev/null &
    fi
    local pid=$!
    sleep "$(printf '%d.%03d' $((ms / 1000)) $((ms % 1000)))"
    kill -9 $pid 2>/dev/null; wait $pid 2>/dev/null
    powerloss || { echo "$label trial $t: the power loss failed"; exit 1; }
    local out
    out=$($BIN --test=host_crash_verify --dir=$dir --acks=$acks --journal=$journal 2>&1)
    local rc=$?
    local a
    a=$(grep -c '^ACK' $acks)
    if (( rc != 0 )) || grep -q 'FAIL\|missing\|differ\|not a gap-free' <<<"$out"; then
      fails=$((fails + 1))
      if (( fails <= 3 )); then echo "--- $label trial $t (store $store round $round, journal $journal, killed at ${ms} ms, $a acks):"; grep -E 'FAIL|missing|differ|gap-free|partition' <<<"$out" | head -5; fi
    fi
    acked_total=$a
    if (( round == ROUNDS )); then round=0; fi
  done
  echo "RESULT $label: $n trials, $fails with violations, $store stores x up to $ROUNDS power losses each"
}

echo "machine: $(uname -m), $(nproc) CPUs, $(grep MemTotal /proc/meminfo | awk '{print $2/1048576 " GiB"}'); LazyFS fa7d32e"
trials trials "$TRIALS" ""
trials negative "$NEG_TRIALS" $W/nosync.so
fusermount3 -u $MNT; kill $LPID 2>/dev/null; wait $LPID 2>/dev/null
