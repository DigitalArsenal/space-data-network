// doorbell_native.c: the doorbell thread in C (design §5.4, A24, A29).
//
// One pthread per instance, parked on a condition variable. A ring that finds
// its writer asleep sets the writer's pending bit and, if the doorbell thread
// is parked, signals it; the thread then invokes the guest's wake export
// (memory.atomic.notify) on the instance's own executor, where the waiters
// are registered. It is a plain C thread: waking it is one futex wake, with no
// Go scheduler hand-off in between (a goroutine locked to its OS thread costs
// two thread wake-ups per ring: an idle M to steal it, then its own M).

#include <pthread.h>
#include <stdatomic.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#include <wasmedge/wasmedge.h>

#include "doorbell_native.h"
#include "sigstack_native.h"

#define WORDS(n) (((n) + 63) / 64)

struct sdn_doorbell {
  WasmEdge_ExecutorContext *exec;
  const WasmEdge_FunctionInstanceContext *wake;
  uint32_t count;
  uint32_t *addrs; // doorbell i's sequence word (what the guest waits on)
  _Atomic uint64_t *pending;
  pthread_mutex_t mu;
  pthread_cond_t cv;
  _Atomic int parked, stop;
  pthread_t thread;
  int started;
  _Atomic uint64_t requests, signals, notifies, errors;
};

static int invoke_wake(sdn_doorbell *db, uint32_t addr, int32_t n) {
  WasmEdge_Value params[2] = {WasmEdge_ValueGenI32((int32_t)addr), WasmEdge_ValueGenI32(n)};
  WasmEdge_Value ret[1];
  WasmEdge_Result r = WasmEdge_ExecutorInvoke(db->exec, db->wake, params, 2, ret, 1);
  return WasmEdge_ResultOK(r) ? 0 : -1;
}

static int any_pending(sdn_doorbell *db) {
  for (uint32_t w = 0; w < WORDS(db->count); w++) {
    if (atomic_load(&db->pending[w])) return 1;
  }
  return 0;
}

static void *run(void *arg) {
  sdn_doorbell *db = arg;
  for (;;) {
    pthread_mutex_lock(&db->mu);
    for (;;) {
      if (atomic_load(&db->stop)) {
        pthread_mutex_unlock(&db->mu);
        return NULL;
      }
      // parked BEFORE the pending check: a ring sets its bit BEFORE reading
      // parked, so one of the two always sees the other (no lost wake-up).
      atomic_store(&db->parked, 1);
      if (any_pending(db)) break;
      pthread_cond_wait(&db->cv, &db->mu);
    }
    atomic_store(&db->parked, 0);
    pthread_mutex_unlock(&db->mu);
    for (uint32_t w = 0; w < WORDS(db->count); w++) {
      uint64_t bits = atomic_exchange(&db->pending[w], 0);
      while (bits) {
        uint32_t b = (uint32_t)__builtin_ctzll(bits);
        bits &= bits - 1;
        uint32_t i = w * 64 + b;
        if (invoke_wake(db, db->addrs[i], 1) == 0) {
          atomic_fetch_add_explicit(&db->notifies, 1, memory_order_relaxed);
        } else {
          atomic_fetch_add_explicit(&db->errors, 1, memory_order_relaxed);
        }
      }
    }
  }
}

sdn_doorbell *sdn_doorbell_start_addrs(void *exec, const void *wake, const uint32_t *addrs, uint32_t count) {
  if (!exec || !wake || (count && !addrs) || count > 4096) return NULL;
  sdn_doorbell *db = calloc(1, sizeof *db);
  if (!db) return NULL;
  db->pending = calloc(WORDS(count) ? WORDS(count) : 1, sizeof(uint64_t));
  db->addrs = calloc(count ? count : 1, sizeof(uint32_t));
  if (!db->pending || !db->addrs) {
    free(db->pending);
    free(db->addrs);
    free(db);
    return NULL;
  }
  if (count) memcpy(db->addrs, addrs, count * sizeof(uint32_t));
  db->exec = (WasmEdge_ExecutorContext *)exec;
  db->wake = (const WasmEdge_FunctionInstanceContext *)wake;
  db->count = count;
  pthread_mutex_init(&db->mu, NULL);
  pthread_cond_init(&db->cv, NULL);
  if (pthread_create(&db->thread, NULL, run, db) != 0) {
    free(db->pending);
    free(db->addrs);
    free(db);
    return NULL;
  }
  db->started = 1;
  return db;
}

sdn_doorbell *sdn_doorbell_start(void *exec, const void *wake, uint32_t base, uint32_t count) {
  if (count > 4096) return NULL;
  uint32_t *addrs = calloc(count ? count : 1, sizeof(uint32_t));
  if (!addrs) return NULL;
  for (uint32_t i = 0; i < count; i++) addrs[i] = base + 8 * i;
  sdn_doorbell *db = sdn_doorbell_start_addrs(exec, wake, addrs, count);
  free(addrs);
  return db;
}

void sdn_doorbell_request(sdn_doorbell *db, uint32_t i) {
  if (i >= db->count) return;
  atomic_fetch_add_explicit(&db->requests, 1, memory_order_relaxed);
  atomic_fetch_or(&db->pending[i / 64], 1ull << (i % 64));
  if (atomic_load(&db->parked)) {
    pthread_mutex_lock(&db->mu);
    pthread_cond_signal(&db->cv);
    pthread_mutex_unlock(&db->mu);
    atomic_fetch_add_explicit(&db->signals, 1, memory_order_relaxed);
  }
}

int sdn_doorbell_notify_now(sdn_doorbell *db, uint32_t addr, int32_t n) {
  // On the calling Go thread: a wake that traps (a stop is set) can leave the
  // thread marked as on its signal stack (sigstack.go); clear it before Go
  // code runs there again, as a synchronous call does (syncNative).
  int rc = invoke_wake(db, addr, n);
  sdn_signal_stack_repair();
  return rc;
}

void sdn_doorbell_stats(sdn_doorbell *db, uint64_t *requests, uint64_t *signals, uint64_t *notifies, uint64_t *errors) {
  *requests = atomic_load(&db->requests);
  *signals = atomic_load(&db->signals);
  *notifies = atomic_load(&db->notifies);
  *errors = atomic_load(&db->errors);
}

void sdn_doorbell_stop(sdn_doorbell *db) {
  if (!db) return;
  pthread_mutex_lock(&db->mu);
  atomic_store(&db->stop, 1);
  pthread_cond_signal(&db->cv);
  pthread_mutex_unlock(&db->mu);
  if (db->started) pthread_join(db->thread, NULL);
  pthread_mutex_destroy(&db->mu);
  pthread_cond_destroy(&db->cv);
  free(db->pending);
  free(db->addrs);
  free(db);
}
