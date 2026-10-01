// Package format4proof is the evidence harness for store format 4 (one SQLite
// file per partition; stack design docs/architecture/flatsql-sqlite-partitions.md,
// the build-out contract §4, §6 and §7). It measures format 4 ("s") back to
// back with format 1 ("f1") and format 2 ("f2") on the host-02-sized fixture,
// through SDN's own storage functions (internal/storage, the layer the HTTP
// handlers and modules call) in the deployed runtime (WasmEdge, AOT), and
// reports the owner's gates:
//
//   - slimmer: bytes on disk per record (DriveBytes; the grown store's bytes
//     per added record);
//   - faster: every benchset read R01–R24 cold and warm at p50 and p99
//     (DriveReads), ingest rate and call p99 for one writer, four writers and
//     same-type producers (DriveIngest), reads during writes with the WAL
//     size (DriveM01);
//   - degrades slower: the +28% step (the ingest's A+B store re-read) and the
//     count-scaled growth step (cmd/sds-tb-gen), as per-doubling slopes;
//
// plus equivalence with format 1 (every read's answer, field by field, and
// the record and tag sets after each write W01–W10; DriveEquivalence) and
// crash coverage (kill -9 loops during ingest and supersede, CrashLoop;
// LazyFS power loss, lazyfs_linux_test.go). Every number carries the load
// average and RSS of its process. A failed gate is reported with its numbers
// (WriteReport: gates.md); nothing here relaxes a bar.
//
// Environment (env.go): SDN_F1_FIXTURE, SDN_F2_FIXTURE and P4_FIXTURE name
// the three stores of the same fixture; P4PROOF_BENCHSET the benchset;
// P4PROOF_WORK the clone directory (on the fixtures' volume: clones are
// cp -c); P4PROOF_OUT the results. Fixture paths are only ever cloned, never
// opened or written. Without them every entry point skips, so the package's
// unit tests are all that CI runs.
package format4proof
