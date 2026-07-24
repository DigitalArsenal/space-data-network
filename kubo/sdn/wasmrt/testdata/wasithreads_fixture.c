// wasi-threads proof fixture — compiled with wasi-sdk --target=wasm32-wasip1-threads.
//
// A GENERIC pthreads program (ZERO OD/SDN knowledge) that exists only to prove
// kubo's wasmrt actually SPAWNS and RUNS more than one OS thread under WasmEdge
// (compiling != running). See ../wasithreads.go and README.md.
//
//   run(nthreads, work_kilo) spawns `nthreads` real pthreads. Each worker:
//     (a) writes a DISTINCT marker into shared linear memory at its own slot
//         (aliased stacks would corrupt these markers);
//     (b) arrives at a SPIN-BARRIER and waits (bounded) until ALL workers have
//         arrived — this can ONLY complete if the workers run CONCURRENTLY, so
//         the recorded peak == nthreads is a hard witness of real parallelism
//         (it depends only on shared memory working across threads, not on
//         wait/notify);
//     (c) busy-works `work_kilo * 1000` iterations (for wall-time scaling).
//   main then pthread_join()s every worker, which additionally exercises
//   cross-thread atomic wait/notify.
//
// Read back by the Go test:  get_marker(i), get_peak(), get_joined().
#include <pthread.h>
#include <sched.h>
#include <stdatomic.h>
#include <stdint.h>

#define MAX_THREADS 64
#define BARRIER_SPIN_LIMIT 100000000L

static int          g_nthreads;
static long         g_work_iters;
static _Atomic int  g_arrived;
static _Atomic int  g_peak;
static _Atomic int  g_joined;
static int          g_marker[MAX_THREADS];

// The production-shaped retirement probe has its own state so the original
// run() parallelism witness remains unchanged. Workers finish both phases,
// park, and are then released one-at-a-time immediately before pthread_join.
// That deliberately exercises the live exit/notify versus join/wait handoff.
static _Atomic int  g_phase1_done;
static _Atomic int  g_phase2_done;
static _Atomic int  g_parked;
static _Atomic int  g_release_count;

static void refresh_peak(void) {
  int a = atomic_load(&g_arrived);
  int p = atomic_load(&g_peak);
  while (a > p && !atomic_compare_exchange_weak(&g_peak, &p, a)) { /* retry */ }
}

static void *worker(void *arg) {
  int idx = (int)(intptr_t)arg;             // lives on THIS thread's stack
  g_marker[idx] = 0x5A5A0000 | idx;         // distinct, non-zero, per-slot
  atomic_fetch_add(&g_arrived, 1);
  refresh_peak();
  // Spin-barrier: wait (bounded) until every worker has arrived. Completes
  // immediately only when all workers are truly running at the same time.
  for (long i = 0; i < BARRIER_SPIN_LIMIT && atomic_load(&g_arrived) < g_nthreads; i++) {
    if ((i & 0xFFFF) == 0) refresh_peak();
  }
  refresh_peak();
  // Fixed busy-work for wall-time scaling measurement.
  volatile double acc = 0;
  for (long i = 0; i < g_work_iters; i++) acc += (double)i * 1.0000001;
  (void)acc;
  atomic_fetch_add(&g_joined, 1);
  return (void *)(intptr_t)idx;
}

static void do_busy_work(long iterations) {
  volatile double acc = 0;
  for (long i = 0; i < iterations; i++) acc += (double)i * 1.0000001;
  (void)acc;
}

static void *parked_worker(void *arg) {
  int idx = (int)(intptr_t)arg;
  g_marker[idx] = 0x6B6B0000 | idx;

  // Phase one models the bounded probe cohort. No worker starts phase two
  // until every probe lane has completed.
  do_busy_work(g_work_iters);
  atomic_fetch_add_explicit(&g_phase1_done, 1, memory_order_acq_rel);
  while (atomic_load_explicit(&g_phase1_done, memory_order_acquire) <
         g_nthreads) {
    sched_yield();
  }

  // Phase two models the complete-file GET cohort. All workers then remain
  // alive and parked until the main lane releases their exact slot.
  do_busy_work(g_work_iters);
  atomic_fetch_add_explicit(&g_phase2_done, 1, memory_order_acq_rel);
  atomic_fetch_add_explicit(&g_parked, 1, memory_order_acq_rel);
  while (atomic_load_explicit(&g_release_count, memory_order_acquire) <= idx) {
    sched_yield();
  }
  return (void *)(intptr_t)idx;
}

// Exported entry: spawn `nthreads` workers (each doing work_kilo*1000 iters of
// busy-work after the barrier), join them all. Returns the observed peak.
int run(int nthreads, int work_kilo) {
  if (nthreads < 1) nthreads = 1;
  if (nthreads > MAX_THREADS) nthreads = MAX_THREADS;
  g_nthreads = nthreads;
  g_work_iters = (long)work_kilo * 1000L;
  atomic_store(&g_arrived, 0);
  atomic_store(&g_peak, 0);
  atomic_store(&g_joined, 0);
  for (int i = 0; i < nthreads; i++) g_marker[i] = 0;

  pthread_t th[MAX_THREADS];
  for (int i = 0; i < nthreads; i++) {
    if (pthread_create(&th[i], NULL, worker, (void *)(intptr_t)i) != 0) return -1;
  }
  for (int i = 0; i < nthreads; i++) {
    pthread_join(th[i], NULL);
  }
  return atomic_load(&g_peak);
}

// Production-shaped entry: one 64-wide cohort performs two phases, parks,
// and is retired serially. The explicit export_name keeps the hermetic fixture
// build script unchanged while making this neutral regression callable.
__attribute__((export_name("run_parked_release")))
int run_parked_release(int nthreads, int work_kilo) {
  if (nthreads < 1) nthreads = 1;
  if (nthreads > MAX_THREADS) nthreads = MAX_THREADS;
  g_nthreads = nthreads;
  g_work_iters = (long)work_kilo * 1000L;
  atomic_store(&g_joined, 0);
  atomic_store(&g_phase1_done, 0);
  atomic_store(&g_phase2_done, 0);
  atomic_store(&g_parked, 0);
  atomic_store(&g_release_count, 0);
  for (int i = 0; i < nthreads; i++) g_marker[i] = 0;

  pthread_t th[MAX_THREADS];
  for (int i = 0; i < nthreads; i++) {
    if (pthread_create(&th[i], NULL, parked_worker,
                       (void *)(intptr_t)i) != 0) {
      return -1;
    }
  }

  long spins = 0;
  while (atomic_load_explicit(&g_parked, memory_order_acquire) < nthreads) {
    if (++spins >= BARRIER_SPIN_LIMIT) return -2;
    sched_yield();
  }

  for (int i = 0; i < nthreads; i++) {
    atomic_store_explicit(&g_release_count, i + 1, memory_order_release);
    if (pthread_join(th[i], NULL) != 0) return -3;
    atomic_fetch_add_explicit(&g_joined, 1, memory_order_acq_rel);
  }
  return atomic_load(&g_joined);
}

int get_marker(int i) { return (i >= 0 && i < MAX_THREADS) ? g_marker[i] : -1; }
int get_peak(void)    { return atomic_load(&g_peak); }
int get_joined(void)  { return atomic_load(&g_joined); }
