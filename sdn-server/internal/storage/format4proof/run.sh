#!/usr/bin/env bash
# The whole format-4 proof, in order, on the integration tree (format 4
# landed: the engine release embedded, the backend, store-migrate --to 4).
#
#   sdn-server/internal/storage/format4proof/run.sh <dir> [phase...]
#
# <dir> holds the binaries, the work clones (same APFS volume as the
# fixtures: clones are cp -c) and the results (<dir>/out). Phases, in this
# order when none is named:
#
#   build      the harness test binary, sds-tb-gen and spacedatanetwork
#   prepare    inputs from a clone of the format-1 fixture
#   migrate    store-migrate --to 4: the reference (kept as the format-4
#              fixture, settled: full text built for every type SDN enables)
#              checked against format 1, then a run under kill -9
#   layout     the format-4 fixture's feed files against format 1 (C-37: one
#              file per source feed x standard, no provider or source column)
#   bytes      bytes on disk per record, every arm, every file at rest
#   reads      every benchset read, cold and warm, s / f1 / f2 back to back
#              (a `<TYPE>@<source>` shape is held to the baselines answering
#              the same question: R17 format 1 by SQL, R18 formats 1 and 2
#              through their EPOCH API with the source filter)
#   ingest     phases A+B (the +28% store) and C (same-type producers)
#   grown      the reads again on the +28% stores
#   grown-c31  format 1's R17/R18 on its +28% store only: the same-question
#              bars of the `<TYPE>@<source>` slopes (gate 3), for a grown
#              pass run without f1 (P4PROOF_ARMS=s,f2: f1's full grown
#              pass takes about an hour); about 10 minutes
#   m01        reads during W01 + W06, writes repeated for 10 minutes
#              (P4PROOF_M01_MINUTES)
#   writes     W01-W10 with record-set digests
#   coverage   the coverage classes (every recordBackend method and axis the
#              benchset leaves out, COVERAGE.md), untimed, every arm
#   crash      kill -9 loops (ingest, supersede) on format 4, 100 rounds each
#   lazyfs     the power-loss rounds in a Linux container (lazyfs.sh), 100 each
#   growth     sds-tb-gen count-scaled steps to G1, about 17.5M records
#              (P4PROOF_GROWTH_STEPS_F2, P4PROOF_GROWTH_STEPS_S), one source
#              feed per producer peer, so format 4 passes 1,000 feed files
#              (P4PROOF_GROWTH_FEED_PER_PEER, P4PROOF_GROWTH_ZIPF); the 120 GiB
#              floor stops either early (it reports where)
#
# Sample sizes are the owner's (2026-10-01 evening, "fewer runs please"):
# one cold and three warm passes per read shape, ingest and supersede once
# per arm. A shape within 10% of a bar (gates.md lists it) is re-run alone:
#   P4PROOF_CLASSES=<class> P4PROOF_SHAPE='<regexp>' run.sh <dir> reads
#   equivalence, report
#
# Environment: SDN_F1_FIXTURE, SDN_F2_FIXTURE (P4_FIXTURE optional: the
# migrate phase makes one), P4PROOF_BENCHSET; WASMEDGE_DIR (the patched
# prefix). Builds use -p 6. Every phase reports its load; keep the box quiet.
set -euo pipefail
DIR="${1:?usage: run.sh <dir> [phase...]}"
shift
HERE="$(cd "$(dirname "$0")" && pwd)"
SDN="$(cd "$HERE/../../../.." && pwd)"
mkdir -p "$DIR/work" "$DIR/out"
DIR="$(cd "$DIR" && pwd)"
export P4PROOF_WORK="$DIR/work" P4PROOF_OUT="$DIR/out" P4PROOF_SDN_BIN="$DIR/spacedatanetwork"
export GOMAXPROCS="${GOMAXPROCS:-8}"
: "${SDN_F1_FIXTURE:?}" "${SDN_F2_FIXTURE:?}" "${P4PROOF_BENCHSET:?}" "${WASMEDGE_DIR:?}"
PHASES=("$@")
[[ ${#PHASES[@]} -gt 0 ]] || PHASES=(build prepare migrate bytes reads ingest grown m01 writes coverage crash lazyfs growth equivalence report)
T="$DIR/p4proof.test"
say() { echo "=== $(date -u +%FT%TZ) $* (load $(sysctl -n vm.loadavg 2>/dev/null || cut -d' ' -f1-3 /proc/loadavg))"; }
tst() { "$T" -test.run "^$1\$" -test.v -test.count=1 -test.timeout 24h 2>&1 | grep -v '^\[' | tee -a "$DIR/out/run.log"; }
for phase in "${PHASES[@]}"; do
  say "$phase"
  case "$phase" in
    build)
      (cd "$SDN/sdn-server" && GOFLAGS=-p=6 ../scripts/go-with-wasmedge.sh test -c -o "$T" ./internal/storage/format4proof)
      (cd "$SDN/sdn-server" && GOFLAGS=-p=6 ../scripts/go-with-wasmedge.sh build -o "$DIR/sds-tb-gen" ./cmd/sds-tb-gen)
      (cd "$SDN/sdn-server" && GOFLAGS=-p=6 ../scripts/go-with-wasmedge.sh build -o "$DIR/spacedatanetwork" ./cmd/spacedatanetwork)
      "$DIR/spacedatanetwork" prewarm-aot >/dev/null ;;
    prepare) tst TestProofPrepare ;;
    migrate) tst TestProofMigrate ;;
    layout) tst TestProofLayout ;;
    bytes) tst TestProofBytes ;;
    reads) P4PROOF_LABEL=fixture tst TestProofReads ;;
    ingest) tst TestProofIngest ;;
    grown) P4PROOF_LABEL=grown tst TestProofReads ;;
    grown-c31) P4PROOF_ARMS=f1 P4PROOF_CLASSES=R17,R18 P4PROOF_LABEL=grown tst TestProofReads ;;
    m01) tst TestProofM01 ;;
    writes) tst TestProofWrites ;;
    coverage) tst TestProofCoverage ;;
    crash) P4PROOF_CRASH=1 tst TestProofCrash ;;
    lazyfs) bash "$HERE/lazyfs.sh" "$DIR/lazyfs" "${P4PROOF_LAZYFS_ROUNDS:-100}" s | tee -a "$DIR/out/run.log" ;;
    growth)
      # Count-scaled (MPE-sized records), Zipf producers, from a fixture
      # clone; the volume keeps 120 GiB free.
      for arm in s f2; do
        fmt=4; steps="${P4PROOF_GROWTH_STEPS_S:-G0=1,G1=17459492}"
        [[ $arm == f2 ]] && { fmt=2; steps="${P4PROOF_GROWTH_STEPS_F2:-G0=1,G1=17459492}"; }
        src="${P4_FIXTURE:-$DIR/work/p4-fixture}"; [[ $arm == f2 ]] && src="$SDN_F2_FIXTURE"
        st="$DIR/work/growth-$arm"; rm -rf "$st"; cp -c -R "$src" "$st" 2>/dev/null || cp -a --reflink=auto "$src" "$st"
        # Every peer its own source feed (P4PROOF_GROWTH_FEED_PER_PEER=0:
        # the shared 64 x 16): format 4 grows a feed file per peer (C-37).
        feeds=(); [[ "${P4PROOF_GROWTH_FEED_PER_PEER:-1}" == 1 ]] && feeds=(-feed-per-peer)
        "$DIR/sds-tb-gen" -store "$st" -seeds "$DIR/work" -format "$fmt" -schemas MPE.fbs -mix MPE=1 -producers 8 ${feeds[@]+"${feeds[@]}"} \
          -peers-per-type "${P4PROOF_GROWTH_PEERS:-1024}" -zipf "${P4PROOF_GROWTH_ZIPF:-1.1}" -batch 4096 -duration 6h -min-free-gib 120 \
          -steps "$steps" -results "$DIR/out" -csv "$DIR/out/growth-$arm" \
          | grep -v '^\[' | grep -E '^#|ERROR|STALL|STUCK' | tee -a "$DIR/out/run.log"
        rm -rf "$st"
      done ;;
    equivalence) tst TestProofEquivalence || true ;;
    report) tst TestProofReport || true; echo "gate report: $DIR/out/gates.md" ;;
    *) echo "unknown phase $phase"; exit 2 ;;
  esac
done
