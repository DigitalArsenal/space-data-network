// ps-probe: a stand-in for the FlatSQL partition-store engine (design §6.3),
// used to prove the WasmEdge substrate before the engine exists (T5).
//
// It is built exactly like the engine will be: clang --target
// wasm32-wasip1-threads, pthreads over wasi-threads, shared imported
// env.memory, and the seven env.flatsql_io_* imports. It exports the engine's
// control ABI (flatsql_ps_init/start/layout/wake/pump/stop/stats) and fills
// the substrate layout v1 the host reads (stop word, heartbeats, doorbells,
// ack words; see sdn-server/internal/wasmrt/SUBSTRATE.md). Everything else
// here is a probe: compute loops, I/O loops and hang injection.
//
// No physics and no record semantics live here. Build: build-ps-probe.sh.

#include <pthread.h>
#include <stdatomic.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#define EXPORT(name) __attribute__((export_name(name)))
#define IO_IMPORT(name) __attribute__((import_module("env"), import_name(name)))

IO_IMPORT("flatsql_io_open") int32_t flatsql_io_open(const char *path, int32_t len, int32_t flags);
IO_IMPORT("flatsql_io_read") int32_t flatsql_io_read(int32_t h, void *dst, int32_t len, double off);
IO_IMPORT("flatsql_io_write") int32_t flatsql_io_write(int32_t h, const void *src, int32_t len, double off);
IO_IMPORT("flatsql_io_truncate") int32_t flatsql_io_truncate(int32_t h, double size);
IO_IMPORT("flatsql_io_sync") int32_t flatsql_io_sync(int32_t h);
IO_IMPORT("flatsql_io_size") double flatsql_io_size(int32_t h);
IO_IMPORT("flatsql_io_close") int32_t flatsql_io_close(int32_t h);

enum {
  IO_READ = 0x0001,
  IO_WRITE = 0x0002,
  IO_CREATE = 0x0004,
  IO_TRUNC = 0x0010,
  IO_CREATE_PARENTS = 0x0100,
};

// ---- substrate layout v1 (host-visible) -----------------------------------
#define MAX_THREADS 64
#define PS_LAYOUT_MAGIC 0x314C5350u /* "PSL1" */

struct ps_layout {
  uint32_t magic, version, size, flags;
  uint32_t stop_word;
  uint32_t heartbeat_base, heartbeat_count;
  uint32_t doorbell_base, doorbell_count; // {u32 seq; u32 sleeping;}
  uint32_t ack_base, ack_count;           // u64 monotonic completion words
  uint32_t mem_gen;                       // u32, bumped after memory.grow
  uint32_t canary_base;                   // u32 per service thread
  uint32_t live_threads;                  // u32 count of running service threads
  uint32_t reserved[18];
};

struct doorbell {
  _Atomic uint32_t seq;
  _Atomic uint32_t sleeping;
};

static _Atomic uint32_t g_stop;
static _Atomic uint32_t g_heartbeat[MAX_THREADS];
static struct doorbell g_door[MAX_THREADS];
static _Atomic uint64_t g_ack[MAX_THREADS];
static _Atomic uint32_t g_memgen;
static _Atomic uint32_t g_canary[MAX_THREADS];
static _Atomic uint32_t g_live;
static _Atomic uint32_t g_hang; // tid+1 of a MODE_CPU thread told to hang
static struct ps_layout g_layout;

// ---- configuration (flatsql_ps_init) ----------------------------------------
enum {
  MODE_DOORBELL = 1, // writer-shaped: wait on the doorbell, ack what was rung
  MODE_CPU = 2,      // compute, beat, check the stop word every chunk
  MODE_SPIN = 3,     // compute forever: no beat, no stop-word check (hang)
  MODE_IO = 4,       // pread/pwrite benchmark, per-call latency histogram
  MODE_HAMMER = 5,   // pwrite a marker pattern until stopped (fencing)
};

struct probe_cfg {
  uint32_t mode;
  uint32_t threads;
  uint32_t active_wait_us; // doorbell wait while active (design: 5 ms)
  uint32_t idle_wait_us;   // doorbell wait after 1 s idle (design: 50 ms)
  uint32_t io_ops;         // MODE_IO: operations per thread
  uint32_t io_size;        // MODE_IO: bytes per operation
  uint32_t io_file_bytes;  // MODE_IO: file size
  uint32_t stack_bytes;    // explicit service-thread stack (A30)
  uint32_t marker;         // MODE_HAMMER: byte written
  uint32_t files;          // MODE_HAMMER: files per thread
  uint32_t prefix_len;     // path prefix length (bytes follow the struct)
  uint32_t drop_notify;    // negative control: skip every Nth doorbell notify (0 = never)
  uint32_t reserved[4];
};

static struct probe_cfg g_cfg;
static char g_prefix[256];

// ---- stats (flatsql_ps_stats) ------------------------------------------------
#define HIST_BUCKETS 1088
struct thread_stats {
  _Atomic uint64_t wakeups;       // returns from a doorbell wait
  _Atomic uint64_t lost_wakeup;   // timed out although the notify for pending work had landed
  _Atomic uint64_t work;          // doorbell rings acknowledged / chunks computed
  _Atomic uint64_t io_ok;         // successful I/O calls
  _Atomic uint64_t io_err;        // failed I/O calls
  _Atomic uint64_t bytes_written; // bytes the guest believes it wrote
  _Atomic uint64_t checksum;      // compute result (keeps the loop honest)
  _Atomic uint64_t late_ring;     // timed out with a ring whose notify had not landed yet
};
static struct thread_stats g_tstats[MAX_THREADS];
static uint64_t g_hist[MAX_THREADS][2][HIST_BUCKETS]; // [thread][0=read,1=write]

// The last notify flatsql_ps_wake issued for each doorbell: the doorbell's seq
// when the call began (low 32 bits) and the monotonic clock in microseconds,
// low 32 bits, when memory.atomic.notify had returned (high 32 bits). One
// 64-bit word, so a waiter reads both halves of one notify.
static _Atomic uint64_t g_notified[MAX_THREADS];
static _Atomic uint32_t g_wake_calls; // doorbell notifies, for cfg.drop_notify

static uint64_t now_ns(void) {
  struct timespec ts;
  clock_gettime(CLOCK_MONOTONIC, &ts);
  return (uint64_t)ts.tv_sec * 1000000000ull + (uint64_t)ts.tv_nsec;
}

// Log-linear bucket: 8 ns steps below 1024 ns, then 32 sub-buckets per power
// of two. The host decodes with the same rule (psinstance_test.go).
static uint32_t hist_bucket(uint64_t ns) {
  if (ns < 1024) return (uint32_t)(ns >> 3);
  uint32_t e = 63u - (uint32_t)__builtin_clzll(ns);
  if (e > 39) return HIST_BUCKETS - 1;
  uint32_t sub = (uint32_t)(ns >> (e - 5)) & 31u;
  return 128u + (e - 10u) * 32u + sub;
}

static uint64_t mix(uint64_t x) {
  x ^= x >> 33;
  x *= 0xff51afd7ed558ccdull;
  x ^= x >> 33;
  x *= 0xc4ceb9fe1a85ec53ull;
  x ^= x >> 33;
  return x;
}

static uint64_t compute(uint64_t seed, uint64_t iters) {
  uint64_t acc = seed;
  for (uint64_t i = 0; i < iters; i++) acc = mix(acc + i);
  return acc;
}

static int wait_u32(_Atomic uint32_t *addr, uint32_t expected, uint64_t timeout_ns) {
  return __builtin_wasm_memory_atomic_wait32((int32_t *)addr, (int32_t)expected, (int64_t)timeout_ns);
}

static void notify_u32(_Atomic uint32_t *addr, uint32_t count) {
  (void)__builtin_wasm_memory_atomic_notify((int32_t *)addr, count);
}

static int stopping(void) { return atomic_load_explicit(&g_stop, memory_order_acquire) != 0; }

static int32_t path_open(uint32_t tid, uint32_t index, int32_t flags) {
  char path[320];
  uint32_t n = g_cfg.prefix_len;
  if (n > sizeof(g_prefix)) n = sizeof(g_prefix);
  memcpy(path, g_prefix, n);
  // "<prefix>t<tid>/f<index>"
  const char hex[] = "0123456789abcdef";
  path[n++] = 't';
  path[n++] = hex[(tid >> 4) & 15];
  path[n++] = hex[tid & 15];
  path[n++] = '/';
  path[n++] = 'f';
  for (int s = 12; s >= 0; s -= 4) path[n++] = hex[(index >> s) & 15];
  return flatsql_io_open(path, (int32_t)n, flags);
}

// ---- service threads ----------------------------------------------------------
static void doorbell_loop(uint32_t tid) {
  struct doorbell *d = &g_door[tid];
  uint32_t seen = atomic_load(&d->seq);
  uint64_t last_work = now_ns();
  while (!stopping()) {
    atomic_fetch_add_explicit(&g_heartbeat[tid], 1, memory_order_relaxed);
    if (atomic_load_explicit(&g_canary[tid], memory_order_relaxed) != 0xC0FFEE00u + tid) {
      __builtin_trap();
    }
    uint32_t s = atomic_load_explicit(&d->seq, memory_order_acquire);
    if (s != seen) {
      seen = s;
      atomic_store_explicit(&g_ack[tid], (uint64_t)s, memory_order_release);
      atomic_fetch_add_explicit(&g_tstats[tid].work, 1, memory_order_relaxed);
      last_work = now_ns();
      continue;
    }
    uint64_t idle = now_ns() - last_work;
    uint64_t timeout_us = idle > 1000000000ull ? g_cfg.idle_wait_us : g_cfg.active_wait_us;
    atomic_store_explicit(&d->sleeping, 1, memory_order_seq_cst);
    if (atomic_load_explicit(&d->seq, memory_order_seq_cst) != s || stopping()) {
      atomic_store_explicit(&d->sleeping, 0, memory_order_relaxed);
      continue;
    }
    uint64_t deadline = now_ns() + timeout_us * 1000ull;
    int r = wait_u32(&d->seq, s, timeout_us * 1000ull);
    atomic_store_explicit(&d->sleeping, 0, memory_order_relaxed);
    atomic_fetch_add_explicit(&g_tstats[tid].wakeups, 1, memory_order_relaxed);
    if (r == 2 && atomic_load(&d->seq) != s && !stopping()) {
      // The wait timed out and work is here. Finding it is not a lost wakeup:
      // a ring is a store, then a notify the doorbell thread issues later, so
      // one that lands within a notify's latency of the deadline (or after
      // it, while `sleeping` is still set) meets a wait that has already
      // timed out. The wakeup was LOST only if a notify for work past `s`
      // had returned before the deadline. It was issued after that work's
      // store, so after this wait compared `s` and registered. The runtime's
      // deadline is no earlier than `deadline` (the runtime reads its clock
      // after this one), and memory.atomic.notify marks a registered waiter
      // under the lock the waiter re-takes when it times out, so that waiter
      // must have returned 0. The slack covers the runtime timing the wait
      // on another clock (1000 ppm of the wait, twice NTP's largest slew)
      // and the microsecond flooring.
      uint64_t rec = atomic_load(&g_notified[tid]);
      uint32_t served = (uint32_t)rec, done_us = (uint32_t)(rec >> 32);
      uint32_t slack_us = (uint32_t)(timeout_us / 1000) + 2;
      uint32_t cutoff_us = (uint32_t)(deadline / 1000) - slack_us;
      if ((int32_t)(served - s) > 0 && (int32_t)(cutoff_us - done_us) > 0) {
        atomic_fetch_add_explicit(&g_tstats[tid].lost_wakeup, 1, memory_order_relaxed);
      } else {
        atomic_fetch_add_explicit(&g_tstats[tid].late_ring, 1, memory_order_relaxed);
      }
    }
  }
}

static void spin_loop(uint32_t tid);

static void cpu_loop(uint32_t tid) {
  uint64_t acc = tid;
  while (!stopping()) {
    if (atomic_load_explicit(&g_hang, memory_order_relaxed) == tid + 1) spin_loop(tid);
    acc = compute(acc, 100000);
    atomic_fetch_add_explicit(&g_heartbeat[tid], 1, memory_order_relaxed);
    atomic_fetch_add_explicit(&g_tstats[tid].work, 1, memory_order_relaxed);
  }
  atomic_store(&g_tstats[tid].checksum, acc);
}

static void spin_loop(uint32_t tid) {
  // Neither beats nor reads the stop word: only an interrupting stop ends it.
  volatile uint64_t acc = tid;
  for (;;) acc = mix(acc + 1);
}

static void io_loop(uint32_t tid) {
  uint32_t size = g_cfg.io_size, fbytes = g_cfg.io_file_bytes;
  if (size == 0 || fbytes < size) return;
  int32_t h = path_open(tid, 0, IO_READ | IO_WRITE | IO_CREATE | IO_TRUNC | IO_CREATE_PARENTS);
  if (h < 0) {
    atomic_fetch_add(&g_tstats[tid].io_err, 1);
    return;
  }
  uint8_t *buf = aligned_alloc(64, size);
  memset(buf, (int)(0x40 + tid), size);
  flatsql_io_truncate(h, (double)fbytes);
  for (uint32_t off = 0; off + size <= fbytes; off += size) flatsql_io_write(h, buf, (int32_t)size, off);
  if (flatsql_io_sync(h) != 0 || flatsql_io_size(h) != (double)fbytes) {
    atomic_fetch_add(&g_tstats[tid].io_err, 1);
  }
  uint64_t slots = fbytes / size, x = 0x9e3779b97f4a7c15ull ^ tid;
  for (uint32_t i = 0; i < g_cfg.io_ops && !stopping(); i++) {
    x = mix(x + i);
    double off = (double)((x % slots) * size);
    int write = (int)(i & 1);
    uint64_t t0 = now_ns();
    int32_t n = write ? flatsql_io_write(h, buf, (int32_t)size, off) : flatsql_io_read(h, buf, (int32_t)size, off);
    uint64_t dt = now_ns() - t0;
    g_hist[tid][write][hist_bucket(dt)]++;
    if (n == (int32_t)size) {
      atomic_fetch_add_explicit(&g_tstats[tid].io_ok, 1, memory_order_relaxed);
    } else {
      atomic_fetch_add_explicit(&g_tstats[tid].io_err, 1, memory_order_relaxed);
    }
    if ((i & 1023) == 0) atomic_fetch_add_explicit(&g_heartbeat[tid], 1, memory_order_relaxed);
  }
  flatsql_io_close(h);
  free(buf);
}

static void hammer_loop(uint32_t tid) {
  uint32_t files = g_cfg.files ? g_cfg.files : 1;
  if (files > 64) files = 64;
  int32_t hs[64];
  for (uint32_t f = 0; f < files; f++) {
    hs[f] = path_open(tid, f, IO_READ | IO_WRITE | IO_CREATE | IO_TRUNC | IO_CREATE_PARENTS);
  }
  uint8_t buf[512];
  memset(buf, (int)g_cfg.marker, sizeof buf);
  uint64_t i = 0;
  while (!stopping()) {
    uint32_t f = (uint32_t)(i % files);
    int32_t n = hs[f] >= 0 ? flatsql_io_write(hs[f], buf, sizeof buf, (double)((i / files) % 64) * sizeof buf) : -1;
    if (n > 0) {
      atomic_fetch_add_explicit(&g_tstats[tid].bytes_written, (uint64_t)n, memory_order_relaxed);
      atomic_fetch_add_explicit(&g_tstats[tid].io_ok, 1, memory_order_relaxed);
    } else {
      atomic_fetch_add_explicit(&g_tstats[tid].io_err, 1, memory_order_relaxed);
    }
    // Syncs are not drained by revoke (design A23); keep some in flight.
    if ((i & 63) == 63 && hs[f] >= 0) flatsql_io_sync(hs[f]);
    atomic_fetch_add_explicit(&g_heartbeat[tid], 1, memory_order_relaxed);
    i++;
  }
}

static void *service_main(void *arg) {
  uint32_t tid = (uint32_t)(uintptr_t)arg;
  atomic_store(&g_canary[tid], 0xC0FFEE00u + tid);
  switch (g_cfg.mode) {
    case MODE_DOORBELL: doorbell_loop(tid); break;
    case MODE_CPU: cpu_loop(tid); break;
    case MODE_SPIN: spin_loop(tid); break;
    case MODE_IO: io_loop(tid); break;
    case MODE_HAMMER: hammer_loop(tid); break;
    default: break;
  }
  // Tell the host watchdog this thread left (layout contract: all ones).
  atomic_store(&g_heartbeat[tid], 0xFFFFFFFFu);
  atomic_fetch_sub(&g_live, 1);
  notify_u32(&g_live, 0x7fffffff);
  return 0;
}

// ---- control ABI (design §6.3) -------------------------------------------------
EXPORT("flatsql_ps_init")
int32_t flatsql_ps_init(int32_t role, int32_t cfg_ptr, int32_t cfg_len) {
  (void)role;
  if (cfg_len < (int32_t)sizeof(struct probe_cfg)) return -1;
  memcpy(&g_cfg, (const void *)(uintptr_t)cfg_ptr, sizeof g_cfg);
  if (g_cfg.threads > MAX_THREADS) return -1;
  uint32_t n = g_cfg.prefix_len;
  if (n > sizeof(g_prefix) || (int32_t)(sizeof g_cfg + n) > cfg_len) return -1;
  memcpy(g_prefix, (const char *)(uintptr_t)cfg_ptr + sizeof g_cfg, n);
  atomic_store(&g_stop, 0);
  memset(&g_layout, 0, sizeof g_layout);
  g_layout.magic = PS_LAYOUT_MAGIC;
  g_layout.version = 1;
  g_layout.size = sizeof g_layout;
  g_layout.stop_word = (uint32_t)(uintptr_t)&g_stop;
  g_layout.heartbeat_base = (uint32_t)(uintptr_t)g_heartbeat;
  g_layout.heartbeat_count = g_cfg.threads;
  g_layout.doorbell_base = (uint32_t)(uintptr_t)g_door;
  g_layout.doorbell_count = g_cfg.threads;
  g_layout.ack_base = (uint32_t)(uintptr_t)g_ack;
  g_layout.ack_count = g_cfg.threads;
  g_layout.mem_gen = (uint32_t)(uintptr_t)&g_memgen;
  g_layout.canary_base = (uint32_t)(uintptr_t)g_canary;
  g_layout.live_threads = (uint32_t)(uintptr_t)&g_live;
  return 0;
}

EXPORT("flatsql_ps_layout")
int32_t flatsql_ps_layout(int32_t out_ptr) {
  memcpy((void *)(uintptr_t)out_ptr, &g_layout, sizeof g_layout);
  return (int32_t)sizeof g_layout;
}

// Spawns the service threads and returns at once. Returns how many started.
EXPORT("flatsql_ps_start")
int32_t flatsql_ps_start(void) {
  pthread_attr_t attr;
  pthread_attr_init(&attr);
  pthread_attr_setdetachstate(&attr, PTHREAD_CREATE_DETACHED);
  if (g_cfg.stack_bytes) pthread_attr_setstacksize(&attr, g_cfg.stack_bytes);
  int32_t started = 0;
  for (uint32_t i = 0; i < g_cfg.threads; i++) {
    atomic_fetch_add(&g_live, 1);
    pthread_t t;
    if (pthread_create(&t, &attr, service_main, (void *)(uintptr_t)i) != 0) {
      atomic_fetch_sub(&g_live, 1);
      break;
    }
    started++;
  }
  pthread_attr_destroy(&attr);
  return started;
}

// memory.atomic.notify(addr, n). For a doorbell it also records the notify
// (g_notified) so a waiter can tell a lost wakeup from a late ring. With
// cfg.drop_notify = N it skips every Nth doorbell notify and records it as
// issued: a runtime that loses wakeups, which doorbell_loop must count.
EXPORT("flatsql_ps_wake")
int32_t flatsql_ps_wake(int32_t addr, int32_t n) {
  uint32_t a = (uint32_t)addr, base = (uint32_t)(uintptr_t)g_door;
  uint32_t tid = (a - base) / (uint32_t)sizeof(struct doorbell);
  int door = a >= base && tid < MAX_THREADS && (a - base) % sizeof(struct doorbell) == 0;
  uint32_t served = door ? atomic_load(&g_door[tid].seq) : 0;
  int32_t woke = 0;
  if (!(door && n == 1 && g_cfg.drop_notify &&
        atomic_fetch_add(&g_wake_calls, 1) % g_cfg.drop_notify == g_cfg.drop_notify - 1)) {
    woke = __builtin_wasm_memory_atomic_notify((int32_t *)(uintptr_t)addr, (uint32_t)n);
  }
  if (door) {
    uint64_t done_us = (uint32_t)(now_ns() / 1000);
    atomic_store(&g_notified[tid], (done_us << 32) | served);
  }
  return woke;
}

// One cooperative iteration (design §5.2): used when no thread could spawn.
EXPORT("flatsql_ps_pump")
int32_t flatsql_ps_pump(double budget_us) {
  (void)budget_us;
  int32_t acked = 0;
  for (uint32_t i = 0; i < g_cfg.threads; i++) {
    uint32_t s = atomic_load(&g_door[i].seq);
    if ((uint64_t)s != atomic_load(&g_ack[i])) {
      atomic_store(&g_ack[i], (uint64_t)s);
      acked++;
    }
  }
  return acked;
}

// Sets the stop word, wakes every waiter, and waits for the service threads
// to leave, bounded by deadline_ms. Returns the threads still running.
EXPORT("flatsql_ps_stop")
int32_t flatsql_ps_stop(double deadline_ms) {
  atomic_store_explicit(&g_stop, 1, memory_order_seq_cst);
  notify_u32(&g_stop, 0x7fffffff);
  for (uint32_t i = 0; i < MAX_THREADS; i++) notify_u32(&g_door[i].seq, 0x7fffffff);
  uint64_t end = now_ns() + (uint64_t)(deadline_ms * 1e6);
  for (;;) {
    uint32_t live = atomic_load(&g_live);
    if (live == 0) return 0;
    uint64_t t = now_ns();
    if (t >= end) return (int32_t)live;
    uint64_t slice = end - t;
    if (slice > 10000000ull) slice = 10000000ull;
    wait_u32(&g_live, live, slice);
  }
}

EXPORT("flatsql_ps_stats")
int32_t flatsql_ps_stats(int32_t out_ptr, int32_t len) {
  int32_t n = (int32_t)sizeof g_tstats;
  if (len < n) return -n;
  memcpy((void *)(uintptr_t)out_ptr, g_tstats, sizeof g_tstats);
  return n;
}

// ---- probes -------------------------------------------------------------------
EXPORT("probe_hist_ptr") int32_t probe_hist_ptr(void) { return (int32_t)(uintptr_t)g_hist; }

// One thread of compute on the calling thread.
EXPORT("probe_cpu")
int64_t probe_cpu(int64_t iters) { return (int64_t)compute(1, (uint64_t)iters); }

struct par_arg {
  uint64_t iters, seed, out;
};
static void *par_main(void *p) {
  struct par_arg *a = p;
  a->out = compute(a->seed, a->iters);
  return 0;
}

// `threads` pthreads each compute `iters`, then join. The wall time of this
// call against probe_cpu(threads*iters) is the parallel-AOT spike.
EXPORT("probe_parallel")
int64_t probe_parallel(int32_t threads, int64_t iters) {
  if (threads < 1 || threads > MAX_THREADS) return -1;
  pthread_t t[MAX_THREADS];
  struct par_arg a[MAX_THREADS];
  int started = 0;
  for (int i = 0; i < threads; i++) {
    a[i].iters = (uint64_t)iters;
    a[i].seed = (uint64_t)i + 1;
    a[i].out = 0;
    if (pthread_create(&t[i], 0, par_main, &a[i]) != 0) break;
    started++;
  }
  uint64_t acc = 0;
  for (int i = 0; i < started; i++) {
    pthread_join(t[i], 0);
    acc ^= a[i].out;
  }
  // Non-negative on success so the caller can tell it from -1/-2.
  return started == threads ? (int64_t)(acc >> 1) : -2;
}

// Grows linear memory from inside the guest and bumps the generation word.
EXPORT("probe_grow")
int32_t probe_grow(int32_t pages) {
  int32_t old = __builtin_wasm_memory_grow(0, (unsigned long)pages);
  if (old >= 0) atomic_fetch_add(&g_memgen, 1);
  return old;
}

EXPORT("probe_mem_pages") int32_t probe_mem_pages(void) { return (int32_t)__builtin_wasm_memory_size(0); }

// Makes MODE_CPU thread `tid` stop beating and spin forever (a hung writer).
EXPORT("probe_hang")
void probe_hang(int32_t tid) { atomic_store(&g_hang, (uint32_t)tid + 1); }

// Writes `value` at `addr` from the guest (base-stability cross-check).
EXPORT("probe_store")
void probe_store(int32_t addr, int32_t value) {
  atomic_store((_Atomic int32_t *)(uintptr_t)addr, value);
}

EXPORT("probe_load")
int32_t probe_load(int32_t addr) { return atomic_load((_Atomic int32_t *)(uintptr_t)addr); }
