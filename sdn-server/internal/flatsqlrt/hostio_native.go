package flatsqlrt

// hostio_native.go: Go handles for the C host I/O module (hostio_native.c).
//
// A partition-store instance gets its seven flatsql_io_* imports from C
// functions added straight to its "env" module (design §5.4), so no Go code,
// no cgo callback and no WasmEdge-go host-function lock runs per I/O call.
// Go only creates, installs, revokes and reads counters.
//
// There is no Go HostIO fallback for these instances (A30): if the module
// cannot be installed, the instance refuses to open.

/*
#cgo CFLAGS: -std=gnu11 -O2
#include <stdlib.h>
#include "hostio_native.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
	"unsafe"

	"github.com/second-state/WasmEdge-go/wasmedge"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// Native host I/O open flags beyond flatsql_io.h's first eight (design §5.4,
// A38) and the BUSY status (A12). Values match hostio_native.h.
const (
	ioFlagCreateParents  int32 = 0x0100
	ioFlagUnlinkIfUnused int32 = 0x0200
	ioFlagOpenDeferred   int32 = 0x0400
	ioErrBusy            int32 = -7
)

// HostIOClass is the scheduling class of an instance's I/O threads.
type HostIOClass int

const (
	HostIOWriter  HostIOClass = C.SDN_HIO_CLASS_WRITER
	HostIOReader  HostIOClass = C.SDN_HIO_CLASS_READER
	HostIOBulk    HostIOClass = C.SDN_HIO_CLASS_BULK
	HostIOControl HostIOClass = C.SDN_HIO_CLASS_CONTROL
)

// ErrNativeHostIOUnavailable: the C module does not exist on this platform.
var ErrNativeHostIOUnavailable = errors.New("flatsqlrt: the native host I/O module is not available on this platform")

// NativeHostIOSupported reports whether the C module has a real implementation.
func NativeHostIOSupported() bool { return C.sdn_hio_supported() != 0 }

// NativeStore is one confined root shared by the instances of a store. Its
// path registry is what makes UNLINK_IF_UNUSED see every instance's handles.
type NativeStore struct {
	st   *C.sdn_hio_store
	root string
	once sync.Once
}

// OpenNativeStore confines a store to root, which must already exist.
func OpenNativeStore(root string) (*NativeStore, error) {
	croot := C.CString(root)
	defer C.free(unsafe.Pointer(croot))
	var buf [512]C.char
	st := C.sdn_hio_store_open(croot, &buf[0], C.int(len(buf)))
	if st == nil {
		return nil, fmt.Errorf("flatsqlrt: %s", C.GoString(&buf[0]))
	}
	return &NativeStore{st: st, root: root}, nil
}

// Root returns the directory the store was opened on.
func (s *NativeStore) Root() string { return s.root }

// Release drops the caller's reference; instances hold their own.
func (s *NativeStore) Release() {
	if s == nil {
		return
	}
	s.once.Do(func() { C.sdn_hio_store_release(s.st) })
}

// NativeStoreStats reports the store-wide path registry lock (A29).
type NativeStoreStats struct {
	LockAcquisitions int64
	LockHoldTotal    time.Duration
	LockHoldMax      time.Duration
	LockClasses      uint32 // bit per HostIOClass that took the lock
	RegisteredPaths  int64
}

// Stats snapshots the store's counters.
func (s *NativeStore) Stats() NativeStoreStats {
	var o C.sdn_hio_store_stats
	C.sdn_hio_store_get_stats(s.st, &o)
	return NativeStoreStats{
		LockAcquisitions: int64(o.lock_acquisitions),
		LockHoldTotal:    time.Duration(o.lock_hold_total_ns),
		LockHoldMax:      time.Duration(o.lock_hold_max_ns),
		LockClasses:      uint32(o.lock_classes),
		RegisteredPaths:  int64(o.registered_paths),
	}
}

// NativeHostIO is one instance's host I/O: virtual handles, fd LRU, fencing.
type NativeHostIO struct {
	in    *C.sdn_hio_inst
	class HostIOClass
	mu    sync.Mutex
	freed bool
}

// NewNativeHostIO creates an instance's I/O view of store with its own fd
// budget (the process RLIMIT is split statically between instances, A29) and
// virtual-handle table size.
func NewNativeHostIO(store *NativeStore, class HostIOClass, fdBudget, maxHandles int) (*NativeHostIO, error) {
	if !NativeHostIOSupported() {
		return nil, ErrNativeHostIOUnavailable
	}
	if store == nil || fdBudget <= 0 || maxHandles <= 0 || maxHandles > 1<<23 {
		return nil, errors.New("flatsqlrt: invalid native host I/O configuration")
	}
	in := C.sdn_hio_inst_new(store.st, C.int(class), C.uint32_t(fdBudget), C.uint32_t(maxHandles))
	if in == nil {
		return nil, errors.New("flatsqlrt: cannot create a native host I/O instance")
	}
	return &NativeHostIO{in: in, class: class}, nil
}

// Install adds the seven flatsql_io_* functions to env (a wasmrt
// WithEnvInstaller hook).
func (h *NativeHostIO) Install(env *wasmedge.Module) error {
	ctx, err := wasmrt.ModuleInstanceContext(env)
	if err != nil {
		return err
	}
	if C.sdn_hio_install(h.in, ctx) != 0 {
		return errors.New("flatsqlrt: WasmEdge refused the native host I/O functions")
	}
	return nil
}

// InstalledIn reports how many of the seven flatsql_io_* functions in env
// are this instance's C functions. 7 proves no I/O call of that module can
// reach a Go host function.
func (h *NativeHostIO) InstalledIn(env *wasmedge.Module) (int, error) {
	ctx, err := wasmrt.ModuleInstanceContext(env)
	if err != nil {
		return 0, err
	}
	return int(C.sdn_hio_installed_count(h.in, ctx)), nil
}

// Revoke fences the instance (A23) and returns how long the drain of
// in-flight mutating calls took. After it returns, the instance cannot write
// another byte, and none of its fd numbers can be handed to anyone else.
func (h *NativeHostIO) Revoke() time.Duration { return time.Duration(C.sdn_hio_revoke(h.in)) }

// ReleaseParked lets callers parked after the revoke return ACCESS, so the
// stopping instance's threads can see their stop word and leave.
func (h *NativeHostIO) ReleaseParked() { C.sdn_hio_release_parked(h.in) }

// Reap closes the sentinel duplicates of a revoked instance once no call is
// in flight. Returns the fds still held (0 = done), or -1 if never revoked.
func (h *NativeHostIO) Reap() int { return int(C.sdn_hio_reap(h.in)) }

// Free releases the instance's C state once nothing is in flight.
func (h *NativeHostIO) Free() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.freed {
		return nil
	}
	if C.sdn_hio_inst_free(h.in) != 0 {
		return errors.New("flatsqlrt: native host I/O still has calls or fds outstanding")
	}
	h.freed = true
	return nil
}

// SetFault injects a delay before every write (op 0) or sync (op 1) syscall;
// ReleaseParked cuts those short. Op 2 stalls syncs and nothing cuts it
// short (a thread stuck in the kernel). Tests only.
func (h *NativeHostIO) SetFault(op int, delay time.Duration) {
	C.sdn_hio_set_fault(h.in, C.int(op), C.uint32_t(delay/time.Microsecond))
}

// NativeHostIOStats is a snapshot of one instance's counters.
type NativeHostIOStats struct {
	Opens, Reopens, Closes, LRUEvictions        int64
	Reads, Writes, Truncates, Syncs, Sizes      int64
	BytesRead, BytesWritten, Errors             int64
	CallsAfterRevoke, WritesAfterRevoke, Parked int64
	OpenFDs, MaxOpenFDs, DirSyncs, DirsCreated  int64
	UnlinkBusy                                  int64
	RevokeDrain                                 time.Duration
	LockAcquisitions                            int64
	LockHoldTotal, LockHoldMax                  time.Duration
	LockClasses                                 uint32
}

// Stats snapshots the instance's counters.
func (h *NativeHostIO) Stats() NativeHostIOStats {
	var o C.sdn_hio_stats
	C.sdn_hio_get_stats(h.in, &o)
	return NativeHostIOStats{
		Opens: int64(o.opens), Reopens: int64(o.reopens), Closes: int64(o.closes), LRUEvictions: int64(o.lru_evictions),
		Reads: int64(o.reads), Writes: int64(o.writes), Truncates: int64(o.truncates), Syncs: int64(o.syncs), Sizes: int64(o.sizes),
		BytesRead: int64(o.bytes_read), BytesWritten: int64(o.bytes_written), Errors: int64(o.errors),
		CallsAfterRevoke: int64(o.calls_after_revoke), WritesAfterRevoke: int64(o.writes_after_revoke), Parked: int64(o.parked),
		OpenFDs: int64(o.open_fds), MaxOpenFDs: int64(o.max_open_fds), DirSyncs: int64(o.dir_syncs), DirsCreated: int64(o.dirs_created),
		UnlinkBusy:       int64(o.unlink_busy),
		RevokeDrain:      time.Duration(o.revoke_drain_ns),
		LockAcquisitions: int64(o.lock_acquisitions),
		LockHoldTotal:    time.Duration(o.lock_hold_total_ns),
		LockHoldMax:      time.Duration(o.lock_hold_max_ns),
		LockClasses:      uint32(o.lock_classes),
	}
}

// The same operations the guest reaches through its imports, on Go memory.
// The partition-store host itself never calls these on a hot path; they
// exist for tests and for host-side maintenance tools.

func (h *NativeHostIO) Open(path string, flags int32) int32 {
	if len(path) == 0 {
		return ioErrGeneric
	}
	p := C.CString(path)
	defer C.free(unsafe.Pointer(p))
	return int32(C.sdn_hio_open(h.in, p, C.int32_t(len(path)), C.int32_t(flags)))
}

func (h *NativeHostIO) ReadAt(handle int32, dst []byte, off int64) int32 {
	if len(dst) == 0 {
		return 0
	}
	return int32(C.sdn_hio_read(h.in, C.int32_t(handle), unsafe.Pointer(&dst[0]), C.int32_t(len(dst)), C.double(off)))
}

func (h *NativeHostIO) WriteAt(handle int32, src []byte, off int64) int32 {
	if len(src) == 0 {
		return 0
	}
	return int32(C.sdn_hio_write(h.in, C.int32_t(handle), unsafe.Pointer(&src[0]), C.int32_t(len(src)), C.double(off)))
}

func (h *NativeHostIO) Truncate(handle int32, size int64) int32 {
	return int32(C.sdn_hio_truncate(h.in, C.int32_t(handle), C.double(size)))
}

func (h *NativeHostIO) Sync(handle int32) int32 {
	return int32(C.sdn_hio_sync(h.in, C.int32_t(handle)))
}

func (h *NativeHostIO) Size(handle int32) float64 {
	return float64(C.sdn_hio_size(h.in, C.int32_t(handle)))
}

func (h *NativeHostIO) Close(handle int32) int32 {
	return int32(C.sdn_hio_close(h.in, C.int32_t(handle)))
}

// RaiseNoFile raises RLIMIT_NOFILE's soft limit to min(hard, want) (design
// §5.4: 65536) and reports the limits now in force.
func RaiseNoFile(want uint64) (soft, hard uint64, err error) {
	var s, hd C.uint64_t
	if rc := C.sdn_hio_raise_nofile(C.uint64_t(want), &s, &hd); rc != 0 {
		return 0, 0, fmt.Errorf("flatsqlrt: RLIMIT_NOFILE: errno %d", -int(rc))
	}
	return uint64(s), uint64(hd), nil
}

// setNoFileSoft sets the soft RLIMIT_NOFILE exactly (tests run it in a child).
func setNoFileSoft(soft uint64) error {
	if rc := C.sdn_hio_set_nofile(C.uint64_t(soft)); rc != 0 {
		return fmt.Errorf("flatsqlrt: setrlimit: errno %d", -int(rc))
	}
	return nil
}

// countOpenFDs counts the process's open file descriptors (tests).
func countOpenFDs() int { return int(C.sdn_hio_count_open_fds()) }

// NativeHistBuckets is the latency histogram width shared with the probe.
const NativeHistBuckets = C.SDN_HIO_HIST_BUCKETS

// benchRawIO runs the raw-syscall baseline (see hostio_native.h).
func benchRawIO(dir string, threads, ops, size, fileBytes int) ([]uint64, error) {
	hist := make([]uint64, threads*2*NativeHistBuckets)
	cdir := C.CString(dir)
	defer C.free(unsafe.Pointer(cdir))
	if rc := C.sdn_hio_bench_raw(cdir, C.int(threads), C.int(ops), C.int(size), C.int(fileBytes), (*C.uint64_t)(unsafe.Pointer(&hist[0]))); rc != 0 {
		return nil, fmt.Errorf("flatsqlrt: raw I/O baseline: errno %d", -int(rc))
	}
	return hist, nil
}

// NativeHostIOSelfTest exercises the C module end to end in a temporary
// directory: install into a WasmEdge module, create with parents, write,
// sync, read back, revoke, reap and free. The daemon's substrate self-test
// reports it (design A30: format 2 needs this module).
func NativeHostIOSelfTest() error {
	if !NativeHostIOSupported() {
		return ErrNativeHostIOUnavailable
	}
	dir, err := os.MkdirTemp("", "sdn-hostio-selftest-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	st, err := OpenNativeStore(dir)
	if err != nil {
		return err
	}
	defer st.Release()
	h, err := NewNativeHostIO(st, HostIOControl, 8, 16)
	if err != nil {
		return err
	}
	env := wasmedge.NewModule(HostIOModule)
	if env == nil {
		return errors.New("flatsqlrt: cannot create a WasmEdge module")
	}
	defer env.Release()
	if err := h.Install(env); err != nil {
		return err
	}
	if n, err := h.InstalledIn(env); err != nil || n != 7 {
		return fmt.Errorf("flatsqlrt: %d of 7 imports installed (%v)", n, err)
	}
	fd := h.Open("a/b/c.bin", ioFlagRead|ioFlagWrite|ioFlagCreate|ioFlagCreateParents)
	if fd <= 0 {
		return fmt.Errorf("flatsqlrt: open: %d", fd)
	}
	want := []byte("substrate self-test")
	if n := h.WriteAt(fd, want, 0); n != int32(len(want)) {
		return fmt.Errorf("flatsqlrt: write: %d", n)
	}
	if rc := h.Sync(fd); rc != 0 {
		return fmt.Errorf("flatsqlrt: sync: %d", rc)
	}
	got := make([]byte, len(want))
	if n := h.ReadAt(fd, got, 0); n != int32(len(want)) || string(got) != string(want) {
		return fmt.Errorf("flatsqlrt: read back %d %q", n, got)
	}
	h.Revoke()
	h.ReleaseParked()
	if n := h.WriteAt(fd, want, 0); n >= 0 {
		return fmt.Errorf("flatsqlrt: a revoked handle wrote %d bytes", n)
	}
	for i := 0; i < 1000 && h.Reap() != 0; i++ {
		time.Sleep(time.Millisecond)
	}
	return h.Free()
}
