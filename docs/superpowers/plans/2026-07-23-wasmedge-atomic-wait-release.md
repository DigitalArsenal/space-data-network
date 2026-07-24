# WasmEdge Atomic Wait/Notify Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:executing-plans` task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a reproducible WasmEdge 0.14.1 runtime whose atomic wait/notify implementation cannot lose a guest `pthread_join` wakeup.

**Architecture:** Keep the approved application-blind WASI-threads adapter and signed-WASM flow architecture unchanged. Patch the pinned WasmEdge runtime itself: publish a waiter atomically with the expected-value check and persist notification state under the waiter mutex. Build that exact runtime into the self-contained Linux release artifact, then prove the existing neutral repeated-cohort regression fails on baseline and is stable on the patched runtime.

**Tech Stack:** WasmEdge 0.14.1 C++, CMake/Ninja, Docker Buildx, Go test fixture, Linux amd64/arm64.

---

### Task 1: Prove the baseline runtime defect with the neutral fixture

**Files:**
- Test: `kubo/sdn/wasmrt/wasithreads_test.go:181-249`
- Test fixture: `kubo/sdn/wasmrt/testdata/wasithreads_fixture.c:118-161`

- [x] Run the existing repeated-cohort regression against unpatched WasmEdge 0.14.1. It must use a bounded repeated-run loop because the race is nondeterministic.

```sh
for attempt in $(seq 1 100); do
  go test -mod=readonly ./sdn/wasmrt \
    -run '^TestWASIThreadsRetiresParkedWorkersAcrossWaves$' -count=1 -v || exit 1
done
```

Expected: an unpatched runtime eventually reports the subprocess watchdog's missed `pthread_join` wakeup; preserve the output as baseline evidence.

### Task 2: Correct the pinned runtime's wait/notify handoff

**Files:**
- Modify: `kubo/sdn/build/release/patches/wasmedge-0.14.1-atomic-wait-notify.patch`

- [x] Make the expected-value check and waiter publication one `WaiterMapMutex` critical section.
- [x] Add `Waiter.Notified`, set it while holding the waiter mutex, and use a predicate wait that observes either `Notified` or cancellation.
- [x] Skip already-notified waiters when honoring `notify(count)`.

Expected: the correction changes WasmEdge synchronization only; it does not alter guest scheduling or synthesize a guest pthread lifecycle in Go.

### Task 3: Build the corrected runtime as a reproducible release input

**Files:**
- Modify: `kubo/sdn/build/release/Dockerfile.linux:18-62`
- Modify: `kubo/sdn/build/release/build-release.sh:19-54`

- [x] Verify the Dockerfile pins and verifies the WasmEdge source archive, applies the patch before CMake configuration, and builds the shared LLVM/AOT-capable library.

```Dockerfile
COPY sdn/build/release/patches/wasmedge-0.14.1-atomic-wait-notify.patch /tmp/wasmedge.patch
RUN patch -p1 < /tmp/wasmedge.patch \
 && cmake --build /src/build -j"$(nproc)" --target wasmedge_shared \
 && cmake --install /src/build
```

- [x] Verify the artifact copies `libwasmedge.so*` beside the server binary so production never depends on a hand-replaced system library.

### Task 4: Verify corrected runtime safety before performance work

**Files:**
- Test: `kubo/sdn/wasmrt/wasithreads_test.go:181-249`
- Test fixture: `kubo/sdn/wasmrt/testdata/wasithreads_fixture.c:118-161`
- Verify: `kubo/sdn/build/release/Dockerfile.linux`

- [x] Run the 100-attempt repeated-cohort test against the patched library for Linux amd64 and arm64. Require all 25,600 worker lifecycles to complete per architecture.
- [x] Record exact runtime source, patch, image, and shared-library hashes plus WasmEdge version.
- [ ] Then run the signed OD child under the production host at width 2 and width 16, requiring exact browser/WasmEdge result hashes, zero surviving workers, and CPU/RSS/FlatSQL/publication measurements.

No Go file, submodule pin, branch, commit, release signing, deployment, or Supplemental-specific Go control plane is authorized by this plan.

## 2026-07-23 verification evidence

- Baseline WasmEdge 0.14.1 Linux/arm64 library:
  `86e4608e4a6a1f52595776634aea515cd8ec90b22736bd14bc318b14d9123580`.
  The neutral regression failed at 90.05 seconds with
  `pthread_join lost a worker-exit wakeup`.
- Patched source: official 0.14.1 archive SHA-256
  `ff95d3b9d4736f36e31c0477208cc70f12a0a3f946bbf756f1e7b181877d5af3`;
  patch `kubo/sdn/build/release/patches/wasmedge-0.14.1-atomic-wait-notify.patch`.
- Release-stage images: arm64
  `sha256:6b51726602c31ff2e755b2f1c4e686bcdf7175de950d8529e3ff2abda09c90bc`,
  amd64
  `sha256:943002009a6e8907ef3cea4ece6ab17ed06a97b7afbb12014ec7bf3d3c047eb4`.
- Patched shared-library hashes: arm64
  `e8af209bc3f6b45d8068193622eee6ef8d7af4fe9a2b8d0ec791d72f5592c431`;
  amd64
  `dbd3525f2adf396744af6666b29937c726541ac7188f9645de93bd0ca832b06c`.
- Each architecture passed 100 executions of four 64-worker waves: 25,600
  completed worker lifecycles per architecture. The patched Linux/arm64
  `go test -mod=readonly ./sdn/wasmrt -count=1` suite also passed.
- A disposable Linux/amd64 release bundle started successfully with
  `ipfs --version`; its bundled `lib/libwasmedge.so.0.1.0` exactly matches
  the patched amd64 shared-library hash above.
