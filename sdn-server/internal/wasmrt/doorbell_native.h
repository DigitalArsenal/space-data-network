// doorbell_native.h: see doorbell_native.c.
#ifndef SDN_DOORBELL_NATIVE_H
#define SDN_DOORBELL_NATIVE_H

#include <stdint.h>

typedef struct sdn_doorbell sdn_doorbell;

// exec: WasmEdge_ExecutorContext*; wake: the guest's wake export
// (WasmEdge_FunctionInstanceContext*, (i32 addr, i32 n) -> i32). Doorbell i's
// sequence word is at base + 8*i.
sdn_doorbell *sdn_doorbell_start(void *exec, const void *wake, uint32_t base, uint32_t count);
// The same with an explicit address per doorbell (the FlatSQL engine keeps
// each writer's and each lane's word in its own object, not in one array).
sdn_doorbell *sdn_doorbell_start_addrs(void *exec, const void *wake, const uint32_t *addrs, uint32_t count);
void sdn_doorbell_request(sdn_doorbell *db, uint32_t i);
int sdn_doorbell_notify_now(sdn_doorbell *db, uint32_t addr, int32_t n);
void sdn_doorbell_stats(sdn_doorbell *db, uint64_t *requests, uint64_t *signals, uint64_t *notifies, uint64_t *errors);
void sdn_doorbell_stop(sdn_doorbell *db);

#endif
