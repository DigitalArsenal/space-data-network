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

int get_marker(int i) { return (i >= 0 && i < MAX_THREADS) ? g_marker[i] : -1; }
int get_peak(void)    { return atomic_load(&g_peak); }
int get_joined(void)  { return atomic_load(&g_joined); }
