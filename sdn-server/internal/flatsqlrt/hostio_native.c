// hostio_native.c: see hostio_native.h. Design §5.4, A21, A23, A29.
//
// LOCKS. The read/write/sync hot path takes no lock at all: a call pins its
// slot (an atomic count), loads the fd, runs the syscall and unpins. The
// per-instance table lock is taken only to decide a reopen, an eviction, an
// open or a close, and is never held across a syscall. The store-wide path
// registry lock is taken by open, close and unlink only. Both are pthread
// mutexes (futex-backed, adaptive on glibc), instrumented for hold time and
// for the set of instance classes that took them (A29).
//
// FENCING. revoke() sets `revoked`, dup2()s a closed-pipe sentinel over every
// fd the instance holds (the fd NUMBER stays allocated, so a replacement's
// open can never receive it and a late syscall from the revoked instance hits
// the pipe, not a file), then waits only for in-flight mutating calls. Syncs
// are not drained: a sync can take seconds, and it cannot add bytes.

#if !defined(_WIN32)

#if defined(__linux__)
#define _GNU_SOURCE 1
#endif

#include "hostio_native.h"

#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <sched.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/resource.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <time.h>
#include <unistd.h>

#if defined(__linux__)
#include <sys/syscall.h>
#endif
#if defined(__APPLE__)
#include <pthread/qos.h>
#endif

#include <wasmedge/wasmedge.h>

#define MAX_PATH_BYTES 4096
#define MAX_TRANSFER (64 << 20)
#define STRIPES 16
#define CACHELINE 64

enum { S_FREE = 0, S_OPEN = 1, S_COLD = 2, S_REOPENING = 3, S_CLOSING = 4, S_DEAD = 5 };

static uint64_t now_ns(void) {
  struct timespec ts;
  clock_gettime(CLOCK_MONOTONIC, &ts);
  return (uint64_t)ts.tv_sec * 1000000000ull + (uint64_t)ts.tv_nsec;
}

// ---- instrumented lock ---------------------------------------------------------
typedef struct {
  pthread_mutex_t m;
  uint64_t t_acquired; // written under the lock
  _Atomic uint64_t acquisitions, hold_total, hold_max;
  _Atomic uint32_t classes;
} ilock;

static void ilock_init(ilock *l) {
  pthread_mutexattr_t a;
  pthread_mutexattr_init(&a);
#if defined(PTHREAD_ADAPTIVE_MUTEX_INITIALIZER_NP)
  pthread_mutexattr_settype(&a, PTHREAD_MUTEX_ADAPTIVE_NP);
#endif
  pthread_mutex_init(&l->m, &a);
  pthread_mutexattr_destroy(&a);
  atomic_init(&l->acquisitions, 0);
  atomic_init(&l->hold_total, 0);
  atomic_init(&l->hold_max, 0);
  atomic_init(&l->classes, 0);
  l->t_acquired = 0;
}

static void ilock_account(ilock *l) {
  uint64_t held = now_ns() - l->t_acquired;
  atomic_fetch_add_explicit(&l->hold_total, held, memory_order_relaxed);
  uint64_t prev = atomic_load_explicit(&l->hold_max, memory_order_relaxed);
  while (held > prev && !atomic_compare_exchange_weak(&l->hold_max, &prev, held)) {
  }
}

static void ilock_lock(ilock *l, int cls) {
  pthread_mutex_lock(&l->m);
  l->t_acquired = now_ns();
  atomic_fetch_add_explicit(&l->acquisitions, 1, memory_order_relaxed);
  atomic_fetch_or_explicit(&l->classes, 1u << (cls & 7), memory_order_relaxed);
}

static void ilock_unlock(ilock *l) {
  ilock_account(l);
  pthread_mutex_unlock(&l->m);
}

// Waits on cond without counting the wait as hold time.
static int ilock_wait(ilock *l, pthread_cond_t *c, uint64_t timeout_ns) {
  ilock_account(l);
  int rc;
  if (timeout_ns == 0) {
    rc = pthread_cond_wait(c, &l->m);
  } else {
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    uint64_t ns = (uint64_t)ts.tv_nsec + timeout_ns;
    ts.tv_sec += (time_t)(ns / 1000000000ull);
    ts.tv_nsec = (long)(ns % 1000000000ull);
    rc = pthread_cond_timedwait(c, &l->m, &ts);
  }
  l->t_acquired = now_ns();
  return rc;
}

// ---- store: confined root + path registry ------------------------------------------
typedef struct reg_entry {
  char *rel;
  uint32_t refs;
  uint32_t unlinking;
  struct reg_entry *next;
} reg_entry;

#define REG_BUCKETS 4096

struct sdn_hio_store {
  int rootfd;
  char *root_raw;   // the caller's spelling
  char *root_canon; // realpath
  _Atomic int32_t refs;
  ilock reg_lock;
  pthread_cond_t reg_cond;
  reg_entry *buckets[REG_BUCKETS];
  uint64_t registered;
};

static uint32_t hash_str(const char *s) {
  uint32_t h = 2166136261u;
  while (*s) {
    h ^= (uint8_t)*s++;
    h *= 16777619u;
  }
  return h;
}

// Registers one holder of rel. Returns 0, or NOENT while rel is being unlinked.
static int reg_acquire(sdn_hio_store *st, const char *rel, int cls) {
  uint32_t b = hash_str(rel) % REG_BUCKETS;
  ilock_lock(&st->reg_lock, cls);
  reg_entry *e = st->buckets[b];
  while (e && strcmp(e->rel, rel) != 0) e = e->next;
  if (e && e->unlinking) {
    ilock_unlock(&st->reg_lock);
    return SDN_IO_ERR_NOENT;
  }
  if (!e) {
    e = calloc(1, sizeof *e);
    if (!e || !(e->rel = strdup(rel))) {
      free(e);
      ilock_unlock(&st->reg_lock);
      return SDN_IO_ERR_GENERIC;
    }
    e->next = st->buckets[b];
    st->buckets[b] = e;
    st->registered++;
  }
  e->refs++;
  ilock_unlock(&st->reg_lock);
  return 0;
}

static void reg_remove_locked(sdn_hio_store *st, uint32_t b, reg_entry *e) {
  reg_entry **pp = &st->buckets[b];
  while (*pp && *pp != e) pp = &(*pp)->next;
  if (*pp) *pp = e->next;
  free(e->rel);
  free(e);
  st->registered--;
}

static void reg_release(sdn_hio_store *st, const char *rel, int cls) {
  uint32_t b = hash_str(rel) % REG_BUCKETS;
  ilock_lock(&st->reg_lock, cls);
  reg_entry *e = st->buckets[b];
  while (e && strcmp(e->rel, rel) != 0) e = e->next;
  if (e && e->refs > 0 && --e->refs == 0 && !e->unlinking) reg_remove_locked(st, b, e);
  ilock_unlock(&st->reg_lock);
}

// Claims rel for an unlink. if_unused: BUSY when any instance holds it.
static int reg_begin_unlink(sdn_hio_store *st, const char *rel, int if_unused, int cls, reg_entry **out) {
  uint32_t b = hash_str(rel) % REG_BUCKETS;
  ilock_lock(&st->reg_lock, cls);
  reg_entry *e = st->buckets[b];
  while (e && strcmp(e->rel, rel) != 0) e = e->next;
  if (e && (e->unlinking || (if_unused && e->refs > 0))) {
    ilock_unlock(&st->reg_lock);
    return SDN_IO_ERR_BUSY;
  }
  if (!e) {
    e = calloc(1, sizeof *e);
    if (!e || !(e->rel = strdup(rel))) {
      free(e);
      ilock_unlock(&st->reg_lock);
      return SDN_IO_ERR_GENERIC;
    }
    e->next = st->buckets[b];
    st->buckets[b] = e;
    st->registered++;
  }
  e->unlinking = 1;
  *out = e;
  ilock_unlock(&st->reg_lock);
  return 0;
}

static void reg_end_unlink(sdn_hio_store *st, reg_entry *e, int cls) {
  uint32_t b = hash_str(e->rel) % REG_BUCKETS;
  ilock_lock(&st->reg_lock, cls);
  e->unlinking = 0;
  if (e->refs == 0) reg_remove_locked(st, b, e);
  ilock_unlock(&st->reg_lock);
}

sdn_hio_store *sdn_hio_store_open(const char *root, char *err, int errlen) {
  if (!root || !*root) {
    snprintf(err, (size_t)errlen, "host I/O root is empty");
    return NULL;
  }
  char canon[MAX_PATH_BYTES];
  if (!realpath(root, canon)) {
    snprintf(err, (size_t)errlen, "host I/O root %s: %s", root, strerror(errno));
    return NULL;
  }
  int fd = open(canon, O_RDONLY | O_DIRECTORY | O_CLOEXEC);
  if (fd < 0) {
    snprintf(err, (size_t)errlen, "host I/O root %s: %s", canon, strerror(errno));
    return NULL;
  }
  sdn_hio_store *st = calloc(1, sizeof *st);
  if (!st) {
    close(fd);
    snprintf(err, (size_t)errlen, "out of memory");
    return NULL;
  }
  st->rootfd = fd;
  st->root_raw = strdup(root);
  st->root_canon = strdup(canon);
  atomic_init(&st->refs, 1);
  ilock_init(&st->reg_lock);
  pthread_cond_init(&st->reg_cond, NULL);
  return st;
}

void sdn_hio_store_release(sdn_hio_store *st) {
  if (!st || atomic_fetch_sub(&st->refs, 1) != 1) return;
  for (int b = 0; b < REG_BUCKETS; b++) {
    reg_entry *e = st->buckets[b];
    while (e) {
      reg_entry *n = e->next;
      free(e->rel);
      free(e);
      e = n;
    }
  }
  close(st->rootfd);
  free(st->root_raw);
  free(st->root_canon);
  pthread_mutex_destroy(&st->reg_lock.m);
  pthread_cond_destroy(&st->reg_cond);
  free(st);
}

void sdn_hio_store_get_stats(sdn_hio_store *st, sdn_hio_store_stats *out) {
  memset(out, 0, sizeof *out);
  if (!st) return;
  out->lock_acquisitions = atomic_load(&st->reg_lock.acquisitions);
  out->lock_hold_total_ns = atomic_load(&st->reg_lock.hold_total);
  out->lock_hold_max_ns = atomic_load(&st->reg_lock.hold_max);
  out->lock_classes = atomic_load(&st->reg_lock.classes);
  ilock_lock(&st->reg_lock, SDN_HIO_CLASS_CONTROL);
  out->registered_paths = st->registered;
  ilock_unlock(&st->reg_lock);
}

// ---- path confinement ------------------------------------------------------------------
// Maps a guest path onto a normalized path relative to the root: absolute paths
// must sit under the root (either spelling); ".." and NUL are refused outright.
// Symlinks are refused at open (O_NOFOLLOW per component / RESOLVE_NO_SYMLINKS).
static int normalize(sdn_hio_store *st, const char *path, int32_t len, char *out, size_t cap) {
  if (len <= 0 || len > MAX_PATH_BYTES) return SDN_IO_ERR_GENERIC;
  if (memchr(path, 0, (size_t)len)) return SDN_IO_ERR_GENERIC;
  char buf[MAX_PATH_BYTES + 1];
  memcpy(buf, path, (size_t)len);
  buf[len] = 0;
  const char *p = buf;
  if (buf[0] == '/') {
    const char *roots[2] = {st->root_canon, st->root_raw};
    int matched = 0;
    for (int i = 0; i < 2 && !matched; i++) {
      size_t rl = strlen(roots[i]);
      while (rl > 1 && roots[i][rl - 1] == '/') rl--;
      if (strncmp(buf, roots[i], rl) == 0 && (buf[rl] == '/' || buf[rl] == 0)) {
        p = buf + rl;
        matched = 1;
      }
    }
    if (!matched) return SDN_IO_ERR_ACCESS;
  }
  size_t o = 0;
  while (*p) {
    while (*p == '/') p++;
    if (!*p) break;
    const char *start = p;
    while (*p && *p != '/') p++;
    size_t n = (size_t)(p - start);
    if (n == 1 && start[0] == '.') continue;
    if (n == 2 && start[0] == '.' && start[1] == '.') return SDN_IO_ERR_ACCESS;
    if (o + n + 2 > cap) return SDN_IO_ERR_GENERIC;
    if (o) out[o++] = '/';
    memcpy(out + o, start, n);
    o += n;
  }
  out[o] = 0;
  return o ? 0 : SDN_IO_ERR_ACCESS; // the root itself is not a file
}

static int status_for(int e) {
  switch (e) {
    case ENOENT:
    case ENOTDIR: return SDN_IO_ERR_NOENT;
    case EACCES:
    case EPERM:
    case ELOOP:
    case EXDEV: return SDN_IO_ERR_ACCESS;
    case ENOSPC:
#ifdef EDQUOT
    case EDQUOT:
#endif
      return SDN_IO_ERR_NOSPACE;
    case EEXIST: return SDN_IO_ERR_GENERIC;
    default: return SDN_IO_ERR_IO;
  }
}

static int dir_sync(int dirfd) {
#if defined(__APPLE__)
  if (fcntl(dirfd, F_FULLFSYNC) == 0) return 0;
#endif
  return fsync(dirfd);
}

// Opens the directory that holds rel's last component, one component at a
// time with O_NOFOLLOW, creating missing directories when asked (each new
// directory's parent is fsynced). Returns a dir fd; *leaf points into rel.
static int walk_parent(sdn_hio_store *st, const char *rel, int create, uint64_t *created, uint64_t *syncs, const char **leaf) {
  int dirfd = openat(st->rootfd, ".", O_RDONLY | O_DIRECTORY | O_CLOEXEC);
  if (dirfd < 0) return -errno;
  const char *p = rel;
  for (;;) {
    const char *slash = strchr(p, '/');
    if (!slash) break;
    char comp[256];
    size_t n = (size_t)(slash - p);
    if (n == 0 || n >= sizeof comp) {
      close(dirfd);
      return -ENAMETOOLONG;
    }
    memcpy(comp, p, n);
    comp[n] = 0;
    int next = openat(dirfd, comp, O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC);
    if (next < 0 && errno == ENOENT && create) {
      if (mkdirat(dirfd, comp, 0700) == 0) {
        (*created)++;
        if (dir_sync(dirfd) != 0) {
          int e = errno;
          close(dirfd);
          return -e;
        }
        (*syncs)++;
      } else if (errno != EEXIST) {
        int e = errno;
        close(dirfd);
        return -e;
      }
      next = openat(dirfd, comp, O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC);
    }
    int e = errno;
    close(dirfd);
    if (next < 0) return -e;
    dirfd = next;
    p = slash + 1;
  }
  *leaf = p;
  return dirfd;
}

#if defined(__linux__)
#ifndef SYS_openat2
#define SYS_openat2 437
#endif
struct sdn_open_how {
  uint64_t flags, mode, resolve;
};
#define SDN_RESOLVE_NO_MAGICLINKS 0x02
#define SDN_RESOLVE_NO_SYMLINKS 0x04
#define SDN_RESOLVE_BENEATH 0x08
static _Atomic int g_openat2_missing;
#endif

// Opens rel (no creation of parents) confined beneath the root.
static int confined_open(sdn_hio_store *st, const char *rel, int oflags) {
#if defined(__linux__)
  if (!atomic_load_explicit(&g_openat2_missing, memory_order_relaxed)) {
    struct sdn_open_how how = {(uint64_t)(oflags | O_CLOEXEC), (oflags & O_CREAT) ? 0600 : 0,
                               SDN_RESOLVE_BENEATH | SDN_RESOLVE_NO_SYMLINKS | SDN_RESOLVE_NO_MAGICLINKS};
    long fd = syscall(SYS_openat2, st->rootfd, rel, &how, sizeof how);
    if (fd >= 0) return (int)fd;
    if (errno != ENOSYS && errno != EPERM) return -errno; // EPERM: seccomp denies openat2
    atomic_store(&g_openat2_missing, 1);
  }
#endif
  uint64_t created = 0, syncs = 0;
  const char *leaf = NULL;
  int dirfd = walk_parent(st, rel, 0, &created, &syncs, &leaf);
  if (dirfd < 0) return dirfd;
  int fd = openat(dirfd, leaf, oflags | O_NOFOLLOW | O_CLOEXEC, 0600);
  int e = errno;
  close(dirfd);
  return fd >= 0 ? fd : -e;
}

// ---- instance --------------------------------------------------------------------------
typedef struct {
  _Atomic uint32_t state;
  _Atomic uint32_t gen;
  _Atomic int32_t fd;
  _Atomic uint32_t pins;
  _Atomic uint32_t referenced; // CLOCK second-chance bit, set by every use
  char *rel;
  int32_t oflags; // reopen flags (no CREAT/TRUNC/EXCL)
  uint32_t gflags;
  uint32_t next_free;
} __attribute__((aligned(CACHELINE))) slot_t;

typedef struct {
  _Atomic int64_t mut, other;
  _Atomic uint64_t reads, writes, truncates, syncs, sizes, bytes_read, bytes_written, errors;
} __attribute__((aligned(CACHELINE))) stripe_t;

struct sdn_hio_inst {
  sdn_hio_store *store;
  int cls;
  uint32_t fd_budget, max_handles;
  slot_t *slots;
  ilock lock;
  pthread_cond_t cond;
  uint32_t free_head; // slot index + 1; 0 = empty
  _Atomic uint32_t high_water;
  _Atomic uint32_t open_fds, max_open_fds;
  _Atomic uint32_t clock_hand;
  _Atomic uint32_t revoked, revoke_done, released;
  _Atomic uint64_t revoke_drain_ns;
  int sentinel_fd;
  stripe_t stripes[STRIPES];
  _Atomic uint64_t opens, reopens, closes, evictions, calls_after_revoke, writes_after_revoke, parked;
  _Atomic uint64_t dir_syncs, dirs_created, unlink_busy;
  _Atomic uint32_t fault_write_us, fault_sync_us, fault_sync_hard_us, fault_close_us;
};

static _Atomic uint32_t g_stripe_seq;
static __thread int t_stripe = -1;
static __thread int t_niced;

static stripe_t *my_stripe(sdn_hio_inst *in) {
  if (t_stripe < 0) t_stripe = (int)(atomic_fetch_add(&g_stripe_seq, 1) % STRIPES);
  return &in->stripes[t_stripe];
}

static void maybe_nice(sdn_hio_inst *in) {
  if (t_niced || in->cls != SDN_HIO_CLASS_BULK) return;
  t_niced = 1;
#if defined(__linux__)
  (void)setpriority(PRIO_PROCESS, (id_t)syscall(SYS_gettid), 10);
#elif defined(__APPLE__)
  (void)pthread_set_qos_class_self_np(QOS_CLASS_UTILITY, 0);
#endif
}

// Cache-line aligned, zeroed allocation: slots and stripes are written by
// different threads and must not share a line.
static void *zalloc_aligned(size_t n) {
  void *p = NULL;
  if (posix_memalign(&p, CACHELINE, n) != 0) return NULL;
  memset(p, 0, n);
  return p;
}

sdn_hio_inst *sdn_hio_inst_new(sdn_hio_store *st, int cls, uint32_t fd_budget, uint32_t max_handles) {
  if (!st || fd_budget == 0 || max_handles == 0 || max_handles > (1u << 23)) return NULL;
  sdn_hio_inst *in = zalloc_aligned(sizeof *in);
  if (!in) return NULL;
  in->slots = zalloc_aligned((size_t)max_handles * sizeof(slot_t));
  int pipefd[2];
  if (!in->slots || pipe(pipefd) != 0) {
    free(in->slots);
    free(in);
    return NULL;
  }
  // The sentinel: the read end of a pipe whose write end is closed. pread
  // fails ESPIPE, pwrite fails EBADF, fsync/ftruncate fail EINVAL.
  close(pipefd[1]);
  (void)fcntl(pipefd[0], F_SETFD, FD_CLOEXEC);
  in->sentinel_fd = pipefd[0];
  atomic_fetch_add(&st->refs, 1);
  in->store = st;
  in->cls = cls;
  in->fd_budget = fd_budget;
  in->max_handles = max_handles;
  ilock_init(&in->lock);
  pthread_cond_init(&in->cond, NULL);
  for (uint32_t i = 0; i < max_handles; i++) atomic_init(&in->slots[i].fd, -1);
  return in;
}

static void wait_or_release(sdn_hio_inst *in, uint32_t us) {
  if (!us) return;
  uint64_t end = now_ns() + (uint64_t)us * 1000ull;
  ilock_lock(&in->lock, in->cls);
  while (!atomic_load(&in->released)) {
    uint64_t t = now_ns();
    if (t >= end) break;
    ilock_wait(&in->lock, &in->cond, end - t);
  }
  ilock_unlock(&in->lock);
}

// A call from a revoked instance: park until the supervisor releases the
// instance (it is stopping), then fail. Parking instead of failing keeps a
// zombie from spinning on errors (A21).
static int32_t park_revoked(sdn_hio_inst *in) {
  atomic_fetch_add(&in->calls_after_revoke, 1);
  atomic_fetch_add(&in->parked, 1);
  ilock_lock(&in->lock, in->cls);
  while (!atomic_load(&in->released)) ilock_wait(&in->lock, &in->cond, 100000000ull);
  ilock_unlock(&in->lock);
  return SDN_IO_ERR_ACCESS;
}

// Enter/leave a call. Returns 0, or the status to return (revoked).
static int enter(sdn_hio_inst *in, stripe_t *s, int mutating) {
  _Atomic int64_t *c = mutating ? &s->mut : &s->other;
  atomic_fetch_add_explicit(c, 1, memory_order_seq_cst);
  if (atomic_load_explicit(&in->revoked, memory_order_seq_cst)) {
    atomic_fetch_sub_explicit(c, 1, memory_order_seq_cst);
    return park_revoked(in);
  }
  maybe_nice(in);
  return 0;
}

static void leave(stripe_t *s, int mutating) {
  atomic_fetch_sub_explicit(mutating ? &s->mut : &s->other, 1, memory_order_seq_cst);
}

// Closes the fd of one unpinned OPEN slot, chosen by CLOCK (second chance):
// the hand sweeps the table, clearing each OPEN slot's referenced bit and
// evicting the first slot found with the bit already clear. Two full sweeps
// bound the search. The sweep takes no lock; the close decision does.
static int try_close_slot(sdn_hio_inst *in, slot_t *s) {
  ilock_lock(&in->lock, in->cls);
  uint32_t expected = S_OPEN;
  if (!atomic_compare_exchange_strong(&s->state, &expected, S_CLOSING)) {
    ilock_unlock(&in->lock);
    return 0;
  }
  if (atomic_load_explicit(&s->pins, memory_order_seq_cst) != 0) {
    atomic_store(&s->state, S_OPEN);
    ilock_unlock(&in->lock);
    return 0;
  }
  int fd = atomic_exchange(&s->fd, -1);
  ilock_unlock(&in->lock);
  close(fd);
  ilock_lock(&in->lock, in->cls);
  atomic_store(&s->state, atomic_load(&in->revoked) ? S_DEAD : S_COLD);
  atomic_fetch_sub(&in->open_fds, 1);
  atomic_fetch_add(&in->evictions, 1);
  pthread_cond_broadcast(&in->cond);
  ilock_unlock(&in->lock);
  return 1;
}

static int evict_one(sdn_hio_inst *in) {
  uint32_t hw = atomic_load(&in->high_water);
  if (hw == 0) return 0;
  for (uint64_t scanned = 0; scanned < 2ull * hw + 2; scanned++) {
    slot_t *s = &in->slots[atomic_fetch_add_explicit(&in->clock_hand, 1, memory_order_relaxed) % hw];
    if (atomic_load_explicit(&s->state, memory_order_relaxed) != S_OPEN) continue;
    if (atomic_load_explicit(&s->pins, memory_order_relaxed) != 0) continue;
    if (atomic_exchange_explicit(&s->referenced, 0, memory_order_relaxed)) continue;
    if (try_close_slot(in, s)) return 1;
  }
  return 0;
}

static void make_room(sdn_hio_inst *in) {
  while (atomic_load(&in->open_fds) >= in->fd_budget) {
    if (!evict_one(in)) break;
  }
}

static void note_open_fd(sdn_hio_inst *in) {
  uint32_t n = atomic_fetch_add(&in->open_fds, 1) + 1;
  uint32_t m = atomic_load(&in->max_open_fds);
  while (n > m && !atomic_compare_exchange_weak(&in->max_open_fds, &m, n)) {
  }
}

// Opens with eviction on EMFILE/ENFILE (the fd table or RLIMIT is full).
static int open_with_room(sdn_hio_inst *in, const char *rel, int oflags) {
  make_room(in);
  for (int tries = 0;; tries++) {
    int fd = confined_open(in->store, rel, oflags);
    if (fd >= 0 || (fd != -EMFILE && fd != -ENFILE) || tries >= 8 || !evict_one(in)) return fd;
  }
}

// Reopens a COLD slot, or waits for another thread's reopen/eviction to finish.
// Returns 0 when the slot is OPEN (the caller retries), or a status.
static int slow_reopen(sdn_hio_inst *in, slot_t *s, uint32_t gen) {
  ilock_lock(&in->lock, in->cls);
  for (;;) {
    uint32_t st = atomic_load(&s->state);
    if (atomic_load(&s->gen) != gen || st == S_FREE) {
      ilock_unlock(&in->lock);
      return SDN_IO_ERR_BADHANDLE;
    }
    if (st == S_DEAD) {
      ilock_unlock(&in->lock);
      return SDN_IO_ERR_ACCESS;
    }
    if (st == S_OPEN) {
      ilock_unlock(&in->lock);
      return 0;
    }
    if (st == S_COLD) {
      atomic_store(&s->state, S_REOPENING);
      break;
    }
    ilock_wait(&in->lock, &in->cond, 0); // REOPENING or CLOSING: futex wait, not a spin
  }
  ilock_unlock(&in->lock);
  int fd = open_with_room(in, s->rel, s->oflags);
  ilock_lock(&in->lock, in->cls);
  int rc = 0;
  if (atomic_load(&s->state) != S_REOPENING) {
    // Revoked while we were opening: this fd was never used; drop it.
    if (fd >= 0) close(fd);
    rc = SDN_IO_ERR_ACCESS;
  } else if (fd >= 0) {
    atomic_store(&s->fd, fd);
    atomic_store(&s->state, S_OPEN);
    note_open_fd(in);
    atomic_fetch_add(&in->reopens, 1);
  } else {
    atomic_store(&s->state, S_COLD);
    rc = status_for(-fd);
  }
  pthread_cond_broadcast(&in->cond);
  ilock_unlock(&in->lock);
  return rc;
}

// Pins the slot behind handle h and returns its fd (or a negative status).
static int acquire_fd(sdn_hio_inst *in, int32_t h, slot_t **out) {
  if (h <= 0) return SDN_IO_ERR_BADHANDLE;
  uint32_t idx = (uint32_t)h >> 8, gen = (uint32_t)h & 0xffu;
  if (idx >= atomic_load(&in->high_water)) return SDN_IO_ERR_BADHANDLE;
  slot_t *s = &in->slots[idx];
  for (;;) {
    atomic_fetch_add_explicit(&s->pins, 1, memory_order_seq_cst);
    uint32_t st = atomic_load_explicit(&s->state, memory_order_seq_cst);
    if (atomic_load(&s->gen) != gen || st == S_FREE || st == S_DEAD) {
      atomic_fetch_sub(&s->pins, 1);
      return st == S_DEAD ? SDN_IO_ERR_ACCESS : SDN_IO_ERR_BADHANDLE;
    }
    if (st == S_OPEN) {
      *out = s;
      return atomic_load(&s->fd);
    }
    atomic_fetch_sub(&s->pins, 1);
    int rc = slow_reopen(in, s, gen);
    if (rc < 0) return rc;
  }
}

static void release_fd(sdn_hio_inst *in, slot_t *s) {
  (void)in;
  if (!atomic_load_explicit(&s->referenced, memory_order_relaxed)) {
    atomic_store_explicit(&s->referenced, 1, memory_order_relaxed);
  }
  atomic_fetch_sub_explicit(&s->pins, 1, memory_order_release);
}

static int alloc_slot(sdn_hio_inst *in, uint32_t *idx) {
  ilock_lock(&in->lock, in->cls);
  if (in->free_head) {
    *idx = in->free_head - 1;
    in->free_head = in->slots[*idx].next_free;
    ilock_unlock(&in->lock);
    return 0;
  }
  uint32_t hw = atomic_load(&in->high_water);
  if (hw >= in->max_handles) {
    ilock_unlock(&in->lock);
    return SDN_IO_ERR_GENERIC;
  }
  *idx = hw;
  atomic_store(&in->high_water, hw + 1);
  ilock_unlock(&in->lock);
  return 0;
}

static void free_slot(sdn_hio_inst *in, uint32_t idx) {
  slot_t *s = &in->slots[idx];
  ilock_lock(&in->lock, in->cls);
  free(s->rel);
  s->rel = NULL;
  uint32_t g = (atomic_load(&s->gen) + 1) & 0xffu;
  atomic_store(&s->gen, g ? g : 1);
  atomic_store(&s->state, S_FREE);
  s->next_free = in->free_head;
  in->free_head = idx + 1;
  pthread_cond_broadcast(&in->cond);
  ilock_unlock(&in->lock);
}

static int32_t do_unlink(sdn_hio_inst *in, const char *rel, int if_unused) {
  reg_entry *claim = NULL;
  int rc = reg_begin_unlink(in->store, rel, if_unused, in->cls, &claim);
  if (rc == SDN_IO_ERR_BUSY) atomic_fetch_add(&in->unlink_busy, 1);
  if (rc < 0) return rc;
  uint64_t created = 0, syncs = 0;
  const char *leaf = NULL;
  int dirfd = walk_parent(in->store, rel, 0, &created, &syncs, &leaf);
  if (dirfd < 0) {
    reg_end_unlink(in->store, claim, in->cls);
    return status_for(-dirfd);
  }
  rc = unlinkat(dirfd, leaf, 0) == 0 ? SDN_IO_OK : status_for(errno);
  close(dirfd);
  reg_end_unlink(in->store, claim, in->cls);
  return rc;
}

int32_t sdn_hio_open(sdn_hio_inst *in, const char *path, int32_t len, int32_t flags) {
  if (flags & ~SDN_IO_KNOWN_FLAGS) return SDN_IO_ERR_GENERIC;
  int mutating = (flags & (SDN_IO_CREATE | SDN_IO_TRUNC | SDN_IO_UNLINK | SDN_IO_UNLINK_IF_UNUSED | SDN_IO_CREATE_PARENTS)) != 0;
  stripe_t *sp = my_stripe(in);
  int rc = enter(in, sp, mutating);
  if (rc) return rc;
  char rel[MAX_PATH_BYTES + 1];
  rc = normalize(in->store, path, len, rel, sizeof rel);
  if (rc < 0) goto out;

  if (flags & SDN_IO_PROBE) {
    uint64_t created = 0, syncs = 0;
    const char *leaf = NULL;
    int dirfd = walk_parent(in->store, rel, 0, &created, &syncs, &leaf);
    if (dirfd < 0) {
      rc = SDN_IO_ERR_NOENT;
      goto out;
    }
    struct stat sb;
    rc = fstatat(dirfd, leaf, &sb, AT_SYMLINK_NOFOLLOW) == 0 ? SDN_IO_OK : SDN_IO_ERR_NOENT;
    close(dirfd);
    goto out;
  }
  if (flags & (SDN_IO_UNLINK | SDN_IO_UNLINK_IF_UNUSED)) {
    rc = do_unlink(in, rel, (flags & SDN_IO_UNLINK_IF_UNUSED) != 0);
    goto out;
  }

  int oflags = O_RDONLY;
  if ((flags & SDN_IO_WRITE) && (flags & SDN_IO_READ)) oflags = O_RDWR;
  else if (flags & SDN_IO_WRITE) oflags = O_WRONLY;
  int reopen_flags = oflags;
  if (flags & SDN_IO_CREATE) oflags |= O_CREAT;
  if (flags & SDN_IO_EXCL) oflags |= O_EXCL;
  if (flags & SDN_IO_TRUNC) oflags |= O_TRUNC;

  rc = reg_acquire(in->store, rel, in->cls);
  if (rc < 0) goto out;
  uint32_t idx;
  rc = alloc_slot(in, &idx);
  if (rc < 0) {
    reg_release(in->store, rel, in->cls);
    goto out;
  }

  int fd;
  if (flags & SDN_IO_CREATE_PARENTS) {
    uint64_t created = 0, syncs = 0;
    const char *leaf = NULL;
    make_room(in);
    int dirfd = walk_parent(in->store, rel, 1, &created, &syncs, &leaf);
    if (dirfd < 0) {
      fd = dirfd;
    } else {
      int made = 0;
      fd = -1;
      if ((oflags & O_CREAT) && !(oflags & O_EXCL)) {
        fd = openat(dirfd, leaf, (oflags | O_EXCL | O_NOFOLLOW | O_CLOEXEC), 0600);
        if (fd >= 0) {
          made = 1;
        } else if (errno == EEXIST) {
          fd = openat(dirfd, leaf, ((oflags & ~O_CREAT) | O_NOFOLLOW | O_CLOEXEC), 0600);
        }
      } else {
        fd = openat(dirfd, leaf, oflags | O_NOFOLLOW | O_CLOEXEC, 0600);
        made = fd >= 0 && (oflags & O_CREAT) && (oflags & O_EXCL);
      }
      if (fd < 0) fd = -errno;
      if (fd >= 0 && made) {
        if (dir_sync(dirfd) != 0) {
          int e = errno;
          close(fd);
          fd = -e;
        } else {
          syncs++;
        }
      }
      close(dirfd);
    }
    atomic_fetch_add(&in->dirs_created, created);
    atomic_fetch_add(&in->dir_syncs, syncs);
  } else {
    fd = open_with_room(in, rel, oflags);
  }
  if (fd < 0) {
    ilock_lock(&in->lock, in->cls);
    in->slots[idx].next_free = in->free_head;
    in->free_head = idx + 1;
    ilock_unlock(&in->lock);
    reg_release(in->store, rel, in->cls);
    rc = status_for(-fd);
    goto out;
  }

  slot_t *s = &in->slots[idx];
  s->rel = strdup(rel);
  s->oflags = reopen_flags;
  s->gflags = (uint32_t)flags;
  atomic_store(&s->fd, fd);
  atomic_store(&s->referenced, 1);
  uint32_t gen = atomic_load(&s->gen);
  if (gen == 0) {
    gen = 1;
    atomic_store(&s->gen, 1);
  }
  ilock_lock(&in->lock, in->cls);
  if (atomic_load(&in->revoked)) {
    // Revoke raced this open: never hand out a handle the revoke missed.
    ilock_unlock(&in->lock);
    close(fd);
    reg_release(in->store, rel, in->cls);
    free_slot(in, idx);
    rc = SDN_IO_ERR_ACCESS;
    goto out;
  }
  atomic_store(&s->state, S_OPEN);
  ilock_unlock(&in->lock);
  note_open_fd(in);
  atomic_fetch_add(&in->opens, 1);
  rc = (int32_t)((idx << 8) | gen);
out:
  if (rc < 0 && rc != SDN_IO_ERR_NOENT && rc != SDN_IO_ERR_BUSY) atomic_fetch_add(&sp->errors, 1);
  leave(sp, mutating);
  return rc;
}

int32_t sdn_hio_read(sdn_hio_inst *in, int32_t h, void *dst, int32_t len, double off) {
  if (len < 0 || len > MAX_TRANSFER || !(off >= 0) || off > 9007199254740992.0) return SDN_IO_ERR_GENERIC;
  stripe_t *sp = my_stripe(in);
  int rc = enter(in, sp, 0);
  if (rc) return rc;
  slot_t *s;
  int fd = acquire_fd(in, h, &s);
  if (fd < 0) {
    leave(sp, 0);
    return fd;
  }
  ssize_t n;
  do {
    n = pread(fd, dst, (size_t)len, (off_t)off);
  } while (n < 0 && errno == EINTR);
  int e = errno;
  release_fd(in, s);
  if (n < 0) {
    atomic_fetch_add_explicit(&sp->errors, 1, memory_order_relaxed);
    leave(sp, 0);
    return status_for(e);
  }
  atomic_fetch_add_explicit(&sp->reads, 1, memory_order_relaxed);
  atomic_fetch_add_explicit(&sp->bytes_read, (uint64_t)n, memory_order_relaxed);
  leave(sp, 0);
  return (int32_t)n;
}

int32_t sdn_hio_write(sdn_hio_inst *in, int32_t h, const void *src, int32_t len, double off) {
  if (len < 0 || len > MAX_TRANSFER || !(off >= 0) || off > 9007199254740992.0) return SDN_IO_ERR_GENERIC;
  stripe_t *sp = my_stripe(in);
  int rc = enter(in, sp, 1);
  if (rc) return rc;
  slot_t *s;
  int fd = acquire_fd(in, h, &s);
  if (fd < 0) {
    leave(sp, 1);
    return fd;
  }
  uint32_t delay = atomic_load_explicit(&in->fault_write_us, memory_order_relaxed);
  if (delay) wait_or_release(in, delay);
  size_t done = 0;
  int e = 0;
  while (done < (size_t)len) {
    ssize_t n = pwrite(fd, (const char *)src + done, (size_t)len - done, (off_t)off + (off_t)done);
    if (n < 0) {
      if (errno == EINTR) continue;
      e = errno;
      break;
    }
    done += (size_t)n;
  }
  release_fd(in, s);
  if (e && done == 0) {
    atomic_fetch_add_explicit(&sp->errors, 1, memory_order_relaxed);
    leave(sp, 1);
    return status_for(e);
  }
  if (atomic_load(&in->revoke_done)) atomic_fetch_add(&in->writes_after_revoke, 1);
  atomic_fetch_add_explicit(&sp->writes, 1, memory_order_relaxed);
  atomic_fetch_add_explicit(&sp->bytes_written, done, memory_order_relaxed);
  leave(sp, 1);
  return (int32_t)done;
}

int32_t sdn_hio_truncate(sdn_hio_inst *in, int32_t h, double size) {
  if (!(size >= 0) || size > 9007199254740992.0) return SDN_IO_ERR_GENERIC;
  stripe_t *sp = my_stripe(in);
  int rc = enter(in, sp, 1);
  if (rc) return rc;
  slot_t *s;
  int fd = acquire_fd(in, h, &s);
  if (fd < 0) {
    leave(sp, 1);
    return fd;
  }
  int r;
  do {
    r = ftruncate(fd, (off_t)size);
  } while (r != 0 && errno == EINTR);
  int e = errno;
  release_fd(in, s);
  atomic_fetch_add_explicit(&sp->truncates, 1, memory_order_relaxed);
  leave(sp, 1);
  return r == 0 ? SDN_IO_OK : status_for(e);
}

int32_t sdn_hio_sync(sdn_hio_inst *in, int32_t h) {
  stripe_t *sp = my_stripe(in);
  int rc = enter(in, sp, 0); // syncs add no bytes and are not drained (A23)
  if (rc) return rc;
  slot_t *s;
  int fd = acquire_fd(in, h, &s);
  if (fd < 0) {
    leave(sp, 0);
    return fd;
  }
  uint32_t delay = atomic_load_explicit(&in->fault_sync_us, memory_order_relaxed);
  if (delay) wait_or_release(in, delay);
  uint32_t hard = atomic_load_explicit(&in->fault_sync_hard_us, memory_order_relaxed);
  if (hard) {
    // A stall nothing can cut short: a thread stuck in the kernel.
    struct timespec ts = {(time_t)(hard / 1000000u), (long)(hard % 1000000u) * 1000L};
    while (nanosleep(&ts, &ts) != 0 && errno == EINTR) {
    }
  }
  int r;
#if defined(__APPLE__)
  r = fcntl(fd, F_FULLFSYNC);
  if (r != 0 && (errno == ENOTSUP || errno == EINVAL)) r = fsync(fd);
#elif defined(__linux__)
  r = fdatasync(fd);
#else
  r = fsync(fd);
#endif
  int e = errno;
  release_fd(in, s);
  atomic_fetch_add_explicit(&sp->syncs, 1, memory_order_relaxed);
  if (r != 0) atomic_fetch_add_explicit(&sp->errors, 1, memory_order_relaxed);
  leave(sp, 0);
  return r == 0 ? SDN_IO_OK : status_for(e);
}

double sdn_hio_size(sdn_hio_inst *in, int32_t h) {
  stripe_t *sp = my_stripe(in);
  int rc = enter(in, sp, 0);
  if (rc) return (double)rc;
  slot_t *s;
  int fd = acquire_fd(in, h, &s);
  if (fd < 0) {
    leave(sp, 0);
    return (double)fd;
  }
  struct stat sb;
  int r = fstat(fd, &sb);
  int e = errno;
  release_fd(in, s);
  atomic_fetch_add_explicit(&sp->sizes, 1, memory_order_relaxed);
  leave(sp, 0);
  return r == 0 ? (double)sb.st_size : (double)status_for(e);
}

int32_t sdn_hio_close(sdn_hio_inst *in, int32_t h) {
  stripe_t *sp = my_stripe(in);
  int rc = enter(in, sp, 1);
  if (rc) return rc;
  if (h <= 0 || ((uint32_t)h >> 8) >= atomic_load(&in->high_water)) {
    leave(sp, 1);
    return SDN_IO_ERR_BADHANDLE;
  }
  uint32_t idx = (uint32_t)h >> 8, gen = (uint32_t)h & 0xffu;
  slot_t *s = &in->slots[idx];
  ilock_lock(&in->lock, in->cls);
  for (;;) {
    uint32_t st = atomic_load(&s->state);
    if (atomic_load(&s->gen) != gen || st == S_FREE || st == S_DEAD) {
      ilock_unlock(&in->lock);
      leave(sp, 1);
      return st == S_DEAD ? SDN_IO_ERR_ACCESS : SDN_IO_ERR_BADHANDLE;
    }
    if (st == S_REOPENING || st == S_CLOSING) {
      ilock_wait(&in->lock, &in->cond, 0);
      continue;
    }
    atomic_store(&s->state, S_CLOSING);
    break;
  }
  ilock_unlock(&in->lock);
  uint32_t delay = atomic_load_explicit(&in->fault_close_us, memory_order_relaxed);
  if (delay) wait_or_release(in, delay);
  while (atomic_load_explicit(&s->pins, memory_order_seq_cst) != 0) sched_yield();
  int fd = atomic_exchange(&s->fd, -1);
  int r = 0;
  if (fd >= 0) {
    r = close(fd);
    atomic_fetch_sub(&in->open_fds, 1);
  }
  // Exactly one of close and revoke releases the path registration: revoke
  // releases every slot it finds not DEAD and marks it DEAD, so close releases
  // only a slot revoke has not reached, and marks it DEAD in the same hold of
  // the table lock so a later revoke skips it. Releasing twice would drop
  // ANOTHER instance's hold on the path and let UNLINK_IF_UNUSED remove a
  // file it still reads.
  ilock_lock(&in->lock, in->cls);
  int release = atomic_load(&s->state) != S_DEAD;
  if (release) {
    reg_release(in->store, s->rel, in->cls);
    atomic_store(&s->state, S_DEAD);
  }
  ilock_unlock(&in->lock);
  if (release && (s->gflags & SDN_IO_DELETE_ON_CLOSE)) (void)do_unlink(in, s->rel, 1);
  free_slot(in, idx);
  atomic_fetch_add(&in->closes, 1);
  leave(sp, 1);
  return r == 0 ? SDN_IO_OK : SDN_IO_ERR_IO;
}

// ---- revocation ----------------------------------------------------------------------
static int64_t in_flight(sdn_hio_inst *in, int mutating) {
  int64_t n = 0;
  for (int i = 0; i < STRIPES; i++) {
    n += atomic_load_explicit(mutating ? &in->stripes[i].mut : &in->stripes[i].other, memory_order_seq_cst);
  }
  return n;
}

uint64_t sdn_hio_revoke(sdn_hio_inst *in) {
  uint64_t t0 = now_ns();
  if (atomic_exchange(&in->revoked, 1)) return atomic_load(&in->revoke_drain_ns);
  ilock_lock(&in->lock, in->cls);
  uint32_t hw = atomic_load(&in->high_water);
  for (uint32_t i = 0; i < hw; i++) {
    slot_t *s = &in->slots[i];
    uint32_t st = atomic_load(&s->state);
    if (st == S_FREE) continue;
    int fd = atomic_load(&s->fd);
    if (fd >= 0) (void)dup2(in->sentinel_fd, fd);
    if (s->rel && st != S_DEAD) reg_release(in->store, s->rel, in->cls);
    atomic_store(&s->state, S_DEAD);
  }
  pthread_cond_broadcast(&in->cond);
  ilock_unlock(&in->lock);
  // Only mutating calls are drained; a stuck sync must not hold the fence.
  while (in_flight(in, 1) > 0) {
    struct timespec ts = {0, 20000};
    nanosleep(&ts, NULL);
  }
  uint64_t drain = now_ns() - t0;
  atomic_store(&in->revoke_drain_ns, drain);
  atomic_store(&in->revoke_done, 1);
  return drain;
}

void sdn_hio_release_parked(sdn_hio_inst *in) {
  ilock_lock(&in->lock, in->cls);
  atomic_store(&in->released, 1);
  pthread_cond_broadcast(&in->cond);
  ilock_unlock(&in->lock);
}

int sdn_hio_reap(sdn_hio_inst *in) {
  if (!atomic_load(&in->revoke_done)) return -1;
  if (in_flight(in, 0) > 0 || in_flight(in, 1) > 0) return (int)(in_flight(in, 0) + in_flight(in, 1));
  int held = 0;
  ilock_lock(&in->lock, in->cls);
  uint32_t hw = atomic_load(&in->high_water);
  for (uint32_t i = 0; i < hw; i++) {
    slot_t *s = &in->slots[i];
    if (atomic_load(&s->pins) != 0) {
      held++;
      continue;
    }
    int fd = atomic_exchange(&s->fd, -1);
    if (fd >= 0) {
      close(fd);
      atomic_fetch_sub(&in->open_fds, 1);
    }
  }
  ilock_unlock(&in->lock);
  return held;
}

int sdn_hio_inst_free(sdn_hio_inst *in) {
  if (!in) return 0;
  if (in_flight(in, 0) > 0 || in_flight(in, 1) > 0) return -1;
  uint32_t hw = atomic_load(&in->high_water);
  for (uint32_t i = 0; i < hw; i++) {
    if (atomic_load(&in->slots[i].pins) != 0) return -1;
  }
  for (uint32_t i = 0; i < hw; i++) {
    slot_t *s = &in->slots[i];
    int fd = atomic_load(&s->fd);
    if (fd >= 0) close(fd);
    if (s->rel && atomic_load(&s->state) != S_DEAD && atomic_load(&s->state) != S_FREE) {
      reg_release(in->store, s->rel, in->cls);
    }
    free(s->rel);
  }
  close(in->sentinel_fd);
  pthread_mutex_destroy(&in->lock.m);
  pthread_cond_destroy(&in->cond);
  sdn_hio_store_release(in->store);
  free(in->slots);
  free(in);
  return 0;
}

void sdn_hio_set_fault(sdn_hio_inst *in, int op, uint32_t delay_us) {
  if (op == SDN_HIO_FAULT_WRITE) atomic_store(&in->fault_write_us, delay_us);
  if (op == SDN_HIO_FAULT_SYNC) atomic_store(&in->fault_sync_us, delay_us);
  if (op == SDN_HIO_FAULT_SYNC_HARD) atomic_store(&in->fault_sync_hard_us, delay_us);
  if (op == SDN_HIO_FAULT_CLOSE) atomic_store(&in->fault_close_us, delay_us);
}

void sdn_hio_get_stats(sdn_hio_inst *in, sdn_hio_stats *o) {
  memset(o, 0, sizeof *o);
  for (int i = 0; i < STRIPES; i++) {
    stripe_t *s = &in->stripes[i];
    o->reads += atomic_load(&s->reads);
    o->writes += atomic_load(&s->writes);
    o->truncates += atomic_load(&s->truncates);
    o->syncs += atomic_load(&s->syncs);
    o->sizes += atomic_load(&s->sizes);
    o->bytes_read += atomic_load(&s->bytes_read);
    o->bytes_written += atomic_load(&s->bytes_written);
    o->errors += atomic_load(&s->errors);
  }
  o->opens = atomic_load(&in->opens);
  o->reopens = atomic_load(&in->reopens);
  o->closes = atomic_load(&in->closes);
  o->lru_evictions = atomic_load(&in->evictions);
  o->calls_after_revoke = atomic_load(&in->calls_after_revoke);
  o->writes_after_revoke = atomic_load(&in->writes_after_revoke);
  o->parked = atomic_load(&in->parked);
  o->open_fds = atomic_load(&in->open_fds);
  o->max_open_fds = atomic_load(&in->max_open_fds);
  o->dir_syncs = atomic_load(&in->dir_syncs);
  o->dirs_created = atomic_load(&in->dirs_created);
  o->unlink_busy = atomic_load(&in->unlink_busy);
  o->revoke_drain_ns = atomic_load(&in->revoke_drain_ns);
  o->lock_acquisitions = atomic_load(&in->lock.acquisitions);
  o->lock_hold_total_ns = atomic_load(&in->lock.hold_total);
  o->lock_hold_max_ns = atomic_load(&in->lock.hold_max);
  o->lock_classes = atomic_load(&in->lock.classes);
}

// ---- WasmEdge host functions -------------------------------------------------------------
static uint8_t *guest_ptr(const WasmEdge_CallingFrameContext *cf, uint32_t ptr, uint32_t len) {
  WasmEdge_MemoryInstanceContext *mem = WasmEdge_CallingFrameGetMemoryInstance(cf, 0);
  if (!mem) return NULL;
  return WasmEdge_MemoryInstanceGetPointer(mem, ptr, len ? len : 1);
}

static WasmEdge_Result hf_open(void *data, const WasmEdge_CallingFrameContext *cf, const WasmEdge_Value *in, WasmEdge_Value *out) {
  int32_t len = WasmEdge_ValueGetI32(in[1]);
  const uint8_t *p = (len > 0 && len <= MAX_PATH_BYTES) ? guest_ptr(cf, (uint32_t)WasmEdge_ValueGetI32(in[0]), (uint32_t)len) : NULL;
  // Copy the path out of shared memory before parsing it: another guest thread
  // can change those bytes while we read them.
  char path[MAX_PATH_BYTES];
  int32_t rc = SDN_IO_ERR_GENERIC;
  if (p) {
    memcpy(path, p, (size_t)len);
    rc = sdn_hio_open((sdn_hio_inst *)data, path, len, WasmEdge_ValueGetI32(in[2]));
  }
  out[0] = WasmEdge_ValueGenI32(rc);
  return WasmEdge_Result_Success;
}

static WasmEdge_Result hf_read(void *data, const WasmEdge_CallingFrameContext *cf, const WasmEdge_Value *in, WasmEdge_Value *out) {
  int32_t len = WasmEdge_ValueGetI32(in[2]);
  int32_t rc = SDN_IO_ERR_GENERIC;
  if (len == 0) {
    rc = SDN_IO_OK;
  } else if (len > 0 && len <= MAX_TRANSFER) {
    uint8_t *p = guest_ptr(cf, (uint32_t)WasmEdge_ValueGetI32(in[1]), (uint32_t)len);
    if (p) rc = sdn_hio_read((sdn_hio_inst *)data, WasmEdge_ValueGetI32(in[0]), p, len, WasmEdge_ValueGetF64(in[3]));
  }
  out[0] = WasmEdge_ValueGenI32(rc);
  return WasmEdge_Result_Success;
}

static WasmEdge_Result hf_write(void *data, const WasmEdge_CallingFrameContext *cf, const WasmEdge_Value *in, WasmEdge_Value *out) {
  int32_t len = WasmEdge_ValueGetI32(in[2]);
  int32_t rc = SDN_IO_ERR_GENERIC;
  if (len == 0) {
    rc = SDN_IO_OK;
  } else if (len > 0 && len <= MAX_TRANSFER) {
    const uint8_t *p = guest_ptr(cf, (uint32_t)WasmEdge_ValueGetI32(in[1]), (uint32_t)len);
    if (p) rc = sdn_hio_write((sdn_hio_inst *)data, WasmEdge_ValueGetI32(in[0]), p, len, WasmEdge_ValueGetF64(in[3]));
  }
  out[0] = WasmEdge_ValueGenI32(rc);
  return WasmEdge_Result_Success;
}

static WasmEdge_Result hf_truncate(void *data, const WasmEdge_CallingFrameContext *cf, const WasmEdge_Value *in, WasmEdge_Value *out) {
  (void)cf;
  out[0] = WasmEdge_ValueGenI32(sdn_hio_truncate((sdn_hio_inst *)data, WasmEdge_ValueGetI32(in[0]), WasmEdge_ValueGetF64(in[1])));
  return WasmEdge_Result_Success;
}

static WasmEdge_Result hf_sync(void *data, const WasmEdge_CallingFrameContext *cf, const WasmEdge_Value *in, WasmEdge_Value *out) {
  (void)cf;
  out[0] = WasmEdge_ValueGenI32(sdn_hio_sync((sdn_hio_inst *)data, WasmEdge_ValueGetI32(in[0])));
  return WasmEdge_Result_Success;
}

static WasmEdge_Result hf_size(void *data, const WasmEdge_CallingFrameContext *cf, const WasmEdge_Value *in, WasmEdge_Value *out) {
  (void)cf;
  out[0] = WasmEdge_ValueGenF64(sdn_hio_size((sdn_hio_inst *)data, WasmEdge_ValueGetI32(in[0])));
  return WasmEdge_Result_Success;
}

static WasmEdge_Result hf_close(void *data, const WasmEdge_CallingFrameContext *cf, const WasmEdge_Value *in, WasmEdge_Value *out) {
  (void)cf;
  out[0] = WasmEdge_ValueGenI32(sdn_hio_close((sdn_hio_inst *)data, WasmEdge_ValueGetI32(in[0])));
  return WasmEdge_Result_Success;
}

// sig: one character per parameter, then ':' and the result ('i' = i32,
// 'f' = f64). Example: "iiif:i".
static int add_fn(WasmEdge_ModuleInstanceContext *env, const char *name, WasmEdge_HostFunc_t fn, void *data, const char *sig) {
  WasmEdge_ValType p[4], r[1];
  uint32_t np = 0;
  const char *c = sig;
  for (; *c && *c != ':' && np < 4; c++) p[np++] = *c == 'i' ? WasmEdge_ValTypeGenI32() : WasmEdge_ValTypeGenF64();
  if (*c != ':') return -1;
  r[0] = c[1] == 'i' ? WasmEdge_ValTypeGenI32() : WasmEdge_ValTypeGenF64();
  WasmEdge_FunctionTypeContext *ft = WasmEdge_FunctionTypeCreate(p, np, r, 1);
  if (!ft) return -1;
  WasmEdge_FunctionInstanceContext *f = WasmEdge_FunctionInstanceCreate(ft, fn, data, 0);
  WasmEdge_FunctionTypeDelete(ft);
  if (!f) return -1;
  WasmEdge_String n = WasmEdge_StringCreateByCString(name);
  WasmEdge_ModuleInstanceAddFunction(env, n, f);
  WasmEdge_StringDelete(n);
  return 0;
}

int sdn_hio_install(sdn_hio_inst *in, void *envp) {
  WasmEdge_ModuleInstanceContext *env = (WasmEdge_ModuleInstanceContext *)envp;
  if (!in || !env) return -1;
  if (add_fn(env, "flatsql_io_open", hf_open, in, "iii:i")) return -1;
  if (add_fn(env, "flatsql_io_read", hf_read, in, "iiif:i")) return -1;
  if (add_fn(env, "flatsql_io_write", hf_write, in, "iiif:i")) return -1;
  if (add_fn(env, "flatsql_io_truncate", hf_truncate, in, "if:i")) return -1;
  if (add_fn(env, "flatsql_io_sync", hf_sync, in, "i:i")) return -1;
  if (add_fn(env, "flatsql_io_size", hf_size, in, "i:f")) return -1;
  if (add_fn(env, "flatsql_io_close", hf_close, in, "i:i")) return -1;
  return 0;
}

// Counts how many of the seven flatsql_io_* functions in env are THIS
// instance's C functions (their host data is the instance). 7 means every I/O
// call of the module reaches C directly and none reaches a Go host function.
int sdn_hio_installed_count(sdn_hio_inst *in, void *envp) {
  static const char *names[] = {"flatsql_io_open", "flatsql_io_read", "flatsql_io_write", "flatsql_io_truncate",
                                "flatsql_io_sync", "flatsql_io_size", "flatsql_io_close"};
  WasmEdge_ModuleInstanceContext *env = (WasmEdge_ModuleInstanceContext *)envp;
  int n = 0;
  for (int i = 0; i < 7; i++) {
    WasmEdge_String name = WasmEdge_StringCreateByCString(names[i]);
    WasmEdge_FunctionInstanceContext *f = WasmEdge_ModuleInstanceFindFunction(env, name);
    WasmEdge_StringDelete(name);
    if (f && WasmEdge_FunctionInstanceGetData(f) == (const void *)in) n++;
  }
  return n;
}

// ---- process limits and the raw baseline ------------------------------------------------------
int sdn_hio_raise_nofile(uint64_t want, uint64_t *soft, uint64_t *hard) {
  struct rlimit rl;
  if (getrlimit(RLIMIT_NOFILE, &rl) != 0) return -errno;
  rlim_t target = (rlim_t)want;
  if (rl.rlim_max != RLIM_INFINITY && target > rl.rlim_max) target = rl.rlim_max;
  if (rl.rlim_cur == RLIM_INFINITY || rl.rlim_cur < target) {
    rlim_t try_to = target;
    while (try_to > rl.rlim_cur) {
      struct rlimit next = {try_to, rl.rlim_max};
      if (setrlimit(RLIMIT_NOFILE, &next) == 0) break;
      try_to = try_to * 3 / 4; // darwin caps below kern.maxfilesperproc
    }
    getrlimit(RLIMIT_NOFILE, &rl);
  }
  *soft = (uint64_t)rl.rlim_cur;
  *hard = rl.rlim_max == RLIM_INFINITY ? UINT64_MAX : (uint64_t)rl.rlim_max;
  return 0;
}

int sdn_hio_set_nofile(uint64_t soft) {
  struct rlimit rl;
  if (getrlimit(RLIMIT_NOFILE, &rl) != 0) return -errno;
  rl.rlim_cur = (rlim_t)soft;
  return setrlimit(RLIMIT_NOFILE, &rl) == 0 ? 0 : -errno;
}

int sdn_hio_count_open_fds(void) {
  struct rlimit rl;
  getrlimit(RLIMIT_NOFILE, &rl);
  int limit = rl.rlim_cur > 65536 ? 65536 : (int)rl.rlim_cur, n = 0;
  for (int fd = 0; fd < limit; fd++) {
    if (fcntl(fd, F_GETFD) != -1) n++;
  }
  return n;
}

static uint32_t hist_bucket(uint64_t ns) {
  if (ns < 1024) return (uint32_t)(ns >> 3);
  uint32_t e = 63u - (uint32_t)__builtin_clzll(ns);
  if (e > 39) return SDN_HIO_HIST_BUCKETS - 1;
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

typedef struct {
  const char *dir;
  int tid, ops, size, file_bytes;
  uint64_t *hist; // [2][BUCKETS]
  int err;
} bench_arg;

static void *bench_main(void *p) {
  bench_arg *a = p;
  char path[MAX_PATH_BYTES];
  snprintf(path, sizeof path, "%s/raw-%d", a->dir, a->tid);
  int fd = open(path, O_RDWR | O_CREAT | O_TRUNC | O_CLOEXEC, 0600);
  if (fd < 0) {
    a->err = errno;
    return NULL;
  }
  char *buf = aligned_alloc(64, (size_t)a->size);
  memset(buf, 0x40 + a->tid, (size_t)a->size);
  if (ftruncate(fd, a->file_bytes) != 0) a->err = errno;
  for (int off = 0; off + a->size <= a->file_bytes; off += a->size) {
    if (pwrite(fd, buf, (size_t)a->size, off) != a->size) a->err = errno;
  }
  uint64_t slots = (uint64_t)(a->file_bytes / a->size), x = 0x9e3779b97f4a7c15ull ^ (uint64_t)a->tid;
  for (int i = 0; i < a->ops; i++) {
    x = mix(x + (uint64_t)i);
    off_t off = (off_t)((x % slots) * (uint64_t)a->size);
    int write = i & 1;
    uint64_t t0 = now_ns();
    ssize_t n = write ? pwrite(fd, buf, (size_t)a->size, off) : pread(fd, buf, (size_t)a->size, off);
    uint64_t dt = now_ns() - t0;
    a->hist[write * SDN_HIO_HIST_BUCKETS + hist_bucket(dt)]++;
    if (n != a->size) a->err = errno ? errno : EIO;
  }
  free(buf);
  close(fd);
  unlink(path);
  return NULL;
}

int sdn_hio_bench_raw(const char *dir, int threads, int ops, int size, int file_bytes, uint64_t *hist) {
  if (threads < 1 || threads > 64 || size <= 0 || file_bytes < size) return -EINVAL;
  pthread_t t[64];
  bench_arg a[64];
  for (int i = 0; i < threads; i++) {
    a[i] = (bench_arg){dir, i, ops, size, file_bytes, hist + (size_t)i * 2 * SDN_HIO_HIST_BUCKETS, 0};
    pthread_create(&t[i], NULL, bench_main, &a[i]);
  }
  int err = 0;
  for (int i = 0; i < threads; i++) {
    pthread_join(t[i], NULL);
    if (a[i].err) err = a[i].err;
  }
  return -err;
}

int sdn_hio_supported(void) { return 1; }

#else /* _WIN32: the partition store's host I/O module is POSIX-only. */

#include "hostio_native.h"
#include <stddef.h>

sdn_hio_store *sdn_hio_store_open(const char *root, char *err, int errlen) {
  (void)root;
  if (err && errlen > 0) {
    const char msg[] = "the native host I/O module is not available on Windows";
    size_t n = sizeof msg < (size_t)errlen ? sizeof msg : (size_t)errlen;
    for (size_t i = 0; i + 1 < n; i++) err[i] = msg[i];
    err[n - 1] = 0;
  }
  return NULL;
}
void sdn_hio_store_release(sdn_hio_store *st) { (void)st; }
void sdn_hio_store_get_stats(sdn_hio_store *st, sdn_hio_store_stats *out) { (void)st; (void)out; }
sdn_hio_inst *sdn_hio_inst_new(sdn_hio_store *st, int cls, uint32_t b, uint32_t m) { (void)st; (void)cls; (void)b; (void)m; return NULL; }
int sdn_hio_inst_free(sdn_hio_inst *in) { (void)in; return 0; }
void sdn_hio_get_stats(sdn_hio_inst *in, sdn_hio_stats *out) { (void)in; (void)out; }
int sdn_hio_install(sdn_hio_inst *in, void *env) { (void)in; (void)env; return -1; }
int sdn_hio_installed_count(sdn_hio_inst *in, void *env) { (void)in; (void)env; return 0; }
uint64_t sdn_hio_revoke(sdn_hio_inst *in) { (void)in; return 0; }
void sdn_hio_release_parked(sdn_hio_inst *in) { (void)in; }
int sdn_hio_reap(sdn_hio_inst *in) { (void)in; return -1; }
int32_t sdn_hio_open(sdn_hio_inst *in, const char *p, int32_t l, int32_t f) { (void)in; (void)p; (void)l; (void)f; return SDN_IO_ERR_ACCESS; }
int32_t sdn_hio_read(sdn_hio_inst *in, int32_t h, void *d, int32_t l, double o) { (void)in; (void)h; (void)d; (void)l; (void)o; return SDN_IO_ERR_ACCESS; }
int32_t sdn_hio_write(sdn_hio_inst *in, int32_t h, const void *s, int32_t l, double o) { (void)in; (void)h; (void)s; (void)l; (void)o; return SDN_IO_ERR_ACCESS; }
int32_t sdn_hio_truncate(sdn_hio_inst *in, int32_t h, double s) { (void)in; (void)h; (void)s; return SDN_IO_ERR_ACCESS; }
int32_t sdn_hio_sync(sdn_hio_inst *in, int32_t h) { (void)in; (void)h; return SDN_IO_ERR_ACCESS; }
double sdn_hio_size(sdn_hio_inst *in, int32_t h) { (void)in; (void)h; return SDN_IO_ERR_ACCESS; }
int32_t sdn_hio_close(sdn_hio_inst *in, int32_t h) { (void)in; (void)h; return SDN_IO_ERR_ACCESS; }
void sdn_hio_set_fault(sdn_hio_inst *in, int op, uint32_t d) { (void)in; (void)op; (void)d; }
int sdn_hio_raise_nofile(uint64_t w, uint64_t *s, uint64_t *h) { (void)w; *s = 0; *h = 0; return -1; }
int sdn_hio_set_nofile(uint64_t s) { (void)s; return -1; }
int sdn_hio_count_open_fds(void) { return -1; }
int sdn_hio_bench_raw(const char *d, int t, int o, int s, int f, uint64_t *h) { (void)d; (void)t; (void)o; (void)s; (void)f; (void)h; return -1; }
int sdn_hio_supported(void) { return 0; }

#endif
