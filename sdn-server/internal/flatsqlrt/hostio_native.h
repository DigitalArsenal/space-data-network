// hostio_native.h: the C host I/O module for partition-store instances
// (design §5.4, A21, A23, A29). It satisfies the seven env.flatsql_io_*
// imports as C WasmEdge host functions, so no Go code (and no WasmEdge-go
// host-function lock, and no cgo callback) runs per I/O call.
//
// Handles are virtual: (slot << 8 | gen), scoped to one instance, backed by an
// fd LRU with a per-instance fd budget, and revocable with drain (fencing).
// CONNECTORS ONLY: nothing here knows what a record, partition or segment is.
#ifndef SDN_HOSTIO_NATIVE_H
#define SDN_HOSTIO_NATIVE_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

// Open flags. 0x0001-0x0080 mirror flatsql cpp/include/flatsql/flatsql_io.h;
// 0x0100-0x0400 are the partition-store additions (design §5.4, A38). The
// flatsql header must define the same values.
#define SDN_IO_READ 0x0001
#define SDN_IO_WRITE 0x0002
#define SDN_IO_CREATE 0x0004
#define SDN_IO_EXCL 0x0008
#define SDN_IO_TRUNC 0x0010
#define SDN_IO_DELETE_ON_CLOSE 0x0020
#define SDN_IO_PROBE 0x0040
#define SDN_IO_UNLINK 0x0080
#define SDN_IO_CREATE_PARENTS 0x0100  // mkdir -p + parent dir fsyncs
#define SDN_IO_UNLINK_IF_UNUSED 0x0200 // BUSY while any instance holds the path
#define SDN_IO_OPEN_DEFERRED 0x0400   // browser hint; a synchronous open here
#define SDN_IO_KNOWN_FLAGS 0x07FF

// Status codes (all negative). -1..-6 mirror flatsql_io.h; -7 is A12's BUSY.
#define SDN_IO_OK 0
#define SDN_IO_ERR_GENERIC (-1)
#define SDN_IO_ERR_NOENT (-2)
#define SDN_IO_ERR_ACCESS (-3)
#define SDN_IO_ERR_IO (-4)
#define SDN_IO_ERR_NOSPACE (-5)
#define SDN_IO_ERR_BADHANDLE (-6)
#define SDN_IO_ERR_BUSY (-7)

// Instance classes. BULK threads get OS nice +10 on their first I/O call.
#define SDN_HIO_CLASS_WRITER 0
#define SDN_HIO_CLASS_READER 1
#define SDN_HIO_CLASS_BULK 2
#define SDN_HIO_CLASS_CONTROL 3

// Fault injection (tests): a delay before the syscall of one operation kind.
#define SDN_HIO_FAULT_WRITE 0
#define SDN_HIO_FAULT_SYNC 1
#define SDN_HIO_FAULT_SYNC_HARD 2 // not cut short by release: a stuck kernel call

// Latency histogram shape shared with the probe (8 ns steps below 1 us, then
// 32 sub-buckets per power of two up to 2^39 ns).
#define SDN_HIO_HIST_BUCKETS 1088

typedef struct sdn_hio_store sdn_hio_store;
typedef struct sdn_hio_inst sdn_hio_inst;

typedef struct {
  uint64_t opens, reopens, closes, lru_evictions;
  uint64_t reads, writes, truncates, syncs, sizes;
  uint64_t bytes_read, bytes_written, errors;
  uint64_t calls_after_revoke;    // calls that arrived after revoke began
  uint64_t writes_after_revoke;   // successful writes that returned after revoke finished (must be 0)
  uint64_t parked;                // callers parked after revoke
  uint64_t open_fds, max_open_fds, dir_syncs, dirs_created, unlink_busy;
  uint64_t revoke_drain_ns;       // time revoke spent draining mutating calls
  // The instance's own table lock (never held across a syscall).
  uint64_t lock_acquisitions, lock_hold_total_ns, lock_hold_max_ns, lock_classes;
} sdn_hio_stats;

typedef struct {
  // The store-wide path registry lock, shared by every instance of a store
  // (open, close, unlink only; never on read/write/sync).
  uint64_t lock_acquisitions, lock_hold_total_ns, lock_hold_max_ns, lock_classes;
  uint64_t registered_paths;
} sdn_hio_store_stats;

// Store: one confined root directory shared by the instances that use it.
sdn_hio_store *sdn_hio_store_open(const char *root, char *err, int errlen);
void sdn_hio_store_release(sdn_hio_store *st);
void sdn_hio_store_get_stats(sdn_hio_store *st, sdn_hio_store_stats *out);

// Instance: one wasm instance's view of the store.
sdn_hio_inst *sdn_hio_inst_new(sdn_hio_store *st, int cls, uint32_t fd_budget, uint32_t max_handles);
// Frees an instance. Returns 0, or -1 (and frees nothing) while any call or
// fd is still outstanding; a revoked instance must be reaped first.
int sdn_hio_inst_free(sdn_hio_inst *in);
void sdn_hio_get_stats(sdn_hio_inst *in, sdn_hio_stats *out);

// Adds the seven flatsql_io_* functions to a WasmEdge module instance
// (the "env" module). `env` is a WasmEdge_ModuleInstanceContext*.
int sdn_hio_install(sdn_hio_inst *in, void *env);
// How many of the seven imports in env are this instance's C functions.
int sdn_hio_installed_count(sdn_hio_inst *in, void *env);

// Revocation (A23): set revoked, point every fd at a closed-pipe sentinel
// (dup2, so no fd number is ever reused), wait only for in-flight MUTATING
// calls, and release the instance's path registrations. Returns the drain
// time in ns. Later calls park (A21) until sdn_hio_release_parked.
uint64_t sdn_hio_revoke(sdn_hio_inst *in);
void sdn_hio_release_parked(sdn_hio_inst *in);
// Closes the sentinel duplicates once no call is in flight. Returns the fds
// still held (0 = fully reaped), or -1 if the instance was never revoked.
int sdn_hio_reap(sdn_hio_inst *in);

// The same operations the host functions perform, on host pointers.
int32_t sdn_hio_open(sdn_hio_inst *in, const char *path, int32_t len, int32_t flags);
int32_t sdn_hio_read(sdn_hio_inst *in, int32_t h, void *dst, int32_t len, double off);
int32_t sdn_hio_write(sdn_hio_inst *in, int32_t h, const void *src, int32_t len, double off);
int32_t sdn_hio_truncate(sdn_hio_inst *in, int32_t h, double size);
int32_t sdn_hio_sync(sdn_hio_inst *in, int32_t h);
double sdn_hio_size(sdn_hio_inst *in, int32_t h);
int32_t sdn_hio_close(sdn_hio_inst *in, int32_t h);

void sdn_hio_set_fault(sdn_hio_inst *in, int op, uint32_t delay_us);

// RLIMIT_NOFILE: raise the soft limit to min(hard, want). Reports both.
int sdn_hio_raise_nofile(uint64_t want, uint64_t *soft, uint64_t *hard);
// Sets the soft limit exactly (tests run this in a child process).
int sdn_hio_set_nofile(uint64_t soft);
int sdn_hio_count_open_fds(void);

// The raw-syscall baseline for the overhead measurement: `threads` pthreads
// each pread/pwrite `ops` random `size`-byte blocks of their own file in dir,
// timing every call exactly as the probe does. hist: threads*2*BUCKETS.
int sdn_hio_bench_raw(const char *dir, int threads, int ops, int size, int file_bytes, uint64_t *hist);

// Whether this build has a real implementation (0 on Windows).
int sdn_hio_supported(void);

#ifdef __cplusplus
}
#endif

#endif
