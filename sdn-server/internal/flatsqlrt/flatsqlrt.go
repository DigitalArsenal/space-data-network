// Package flatsqlrt hosts the FlatSQL WASI reactor (flatsql-wasi.wasm)
// in-process via WasmEdge and wraps its C ABI in a Go API.
//
// FlatSQL keeps data in native FlatBuffer format and provides SQL access via
// SQLite virtual tables; query results can be returned as aligned,
// size-prefixed FlatBuffer streams (zero-copy on the guest side, one copy out
// of linear memory on the host side). See README.md for ABI conventions and
// artifact provenance.
package flatsqlrt

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	logging "github.com/ipfs/go-log/v2"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

var log = logging.Logger("flatsqlrt")

// The embedded engine is the NO-EXCEPTIONS WASI build (flatsql_wasi_noeh
// CMake target): identical sources and exports to flatsql-wasi.wasm, but
// C++ throws abort instead of unwinding. This is required server-side
// because WasmEdge's AOT compiler (0.14–0.17) cannot parse wasm-exceptions
// (exnref) modules, and interpreted execution is ~100x too slow for query
// workloads. Byte-parity with the browser artifact is enforced by
// parity_test.go against shared-test-vectors/flatsql-parity.json.
//
//go:embed flatsql-wasi-noeh.wasm
var flatsqlWasm []byte

// EmbeddedWasm returns the embedded flatsql-wasi.wasm bytes (for hashing /
// provenance checks and for hosts that want to instantiate it themselves).
func EmbeddedWasm() []byte { return flatsqlWasm }

const (
	// EngineImportModule is the wasm import-module name linked dependents
	// resolve the engine's exports against — and therefore the name the live
	// engine instance is registered under in its own VM (loop C.7).
	EngineImportModule = "flatsql"

	// DefaultMaxMemoryPages allows the engine to grow to the wasm32 maximum
	// (65536 pages x 64 KiB = 4 GiB). The catalog-scale store needs far more
	// than modulert's 64 MB plugin cap.
	DefaultMaxMemoryPages = 65536

	// cstringReadChunk is the granularity for scanning guest C strings.
	// WasmEdge's GetData validates the entire requested range, so we read in
	// small chunks until the NUL terminator instead of one large window.
	cstringReadChunk = 256

	// cstringMaxLen caps guest C-string reads (defensive bound).
	cstringMaxLen = 64 * 1024 * 1024
)

// DefaultEngineExecTimeout is the HOST's wall-clock budget for ONE engine call.
//
// It is deliberately far larger than any healthy call: the full record-catalog
// replay of 523,261 frames measured 48.3 s in TOTAL on host-01, spread over many
// individual calls, so five minutes for a single call cannot be reached by
// legitimate work at catalog scale. This budget is not a performance knob — it
// is the bound that stops a RUNAWAY guest from consuming a core forever, and it
// is set at the loosest value that still does that so it can never poison a
// healthy engine.
//
// Prior art, both from unbounded engine calls: host-02 burned one vCPU (its
// only vCPU) for 612 minutes inside a single WasmEdge_VMExecuteRegistered, and
// host-01 lost all libp2p peering for 41+ minutes the same way. Zero disables
// the bound and restores that behaviour; it should never be zero in production.
//
// Override with SDN_FLATSQL_EXEC_TIMEOUT (a Go duration, e.g. "90s"), so an
// operator can retune on a live host without a rebuild.
const DefaultEngineExecTimeout = 5 * time.Minute

// engineExecTimeoutEnv names the operator override for DefaultEngineExecTimeout.
const engineExecTimeoutEnv = "SDN_FLATSQL_EXEC_TIMEOUT"

// resolveEngineExecTimeout returns the configured per-call engine budget: the
// explicit option if set, else the SDN_FLATSQL_EXEC_TIMEOUT override, else
// DefaultEngineExecTimeout. An unparseable or negative override is IGNORED with
// a warning rather than silently disabling the bound — losing the bound is the
// failure mode this whole mechanism exists to prevent, so it must not be
// reachable by a typo in a unit file.
func resolveEngineExecTimeout(explicit time.Duration) time.Duration {
	if explicit != 0 {
		return explicit
	}
	raw := strings.TrimSpace(os.Getenv(engineExecTimeoutEnv))
	if raw == "" {
		return DefaultEngineExecTimeout
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		fmt.Fprintf(os.Stderr, "[flatsqlrt] WARN ignoring %s=%q (%v); using default %s\n",
			engineExecTimeoutEnv, raw, err, DefaultEngineExecTimeout)
		return DefaultEngineExecTimeout
	}
	return d
}

// Option configures a Runtime.
type Option func(*config)

type config struct {
	maxMemoryPages   uint32
	wasmBytes        []byte
	aotCacheDir      string
	aotCompileOnMiss bool
	execTimeout      time.Duration
	fileIORoot       string
}

// WithFileIORoot gives this engine a REAL filesystem, confined to dir.
//
// Without it the engine still imports the seven flatsql_io_* functions — it
// must, or the artifact cannot instantiate — but every one of them refuses
// (refusingHostFuncs), so a disk-backed open fails instead of silently
// succeeding against something undurable. That asymmetry is deliberate: the
// defect this whole lane exists to remove was I/O that LOOKED durable.
//
// dir must exist. Every path the engine asks for is resolved inside it and
// refused if it escapes (HostIO.Resolve).
func WithFileIORoot(dir string) Option {
	return func(c *config) { c.fileIORoot = dir }
}

// WithExecTimeout overrides the per-call wall-clock budget for engine calls
// (see DefaultEngineExecTimeout). Zero keeps the default; a negative value is
// rejected in favour of the default.
func WithExecTimeout(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.execTimeout = d
		}
	}
}

// WithMaxMemoryPages overrides the default 4 GiB linear-memory growth cap.
func WithMaxMemoryPages(pages uint32) Option {
	return func(c *config) { c.maxMemoryPages = pages }
}

// WithWasmBytes hosts alternative engine bytes (e.g. a newer artifact loaded
// from disk) instead of the embedded copy.
func WithWasmBytes(b []byte) Option {
	return func(c *config) { c.wasmBytes = b }
}

// Runtime is one instantiated FlatSQL engine (one WasmEdge VM). It can hold
// multiple databases. All calls are serialized on the underlying module lock:
// the engine is compiled single-threaded (SQLITE_THREADSAFE=0).
type Runtime struct {
	mod         *wasmrt.Module
	aot         bool
	aotPath     string
	aotMiss     string
	aotCacheDir string
	poisoned    bool

	// io is the file layer behind the engine's seven "env" imports. Nil when
	// the runtime was created without WithFileIORoot, in which case the imports
	// are registered but refuse (see refusingHostFuncs).
	io *HostIO

	// Statement account for the single engine lock — see accountQuery.
	statQueries   atomic.Int64
	statSlow      atomic.Int64
	statWaitNanos atomic.Int64
	statHeldNanos atomic.Int64

	// dispatchLogStop ends the periodic guest-call account (startDispatchLog).
	dispatchLogStop chan struct{}
}

// FileIO returns this runtime's host file layer, or nil when the engine was
// created without WithFileIORoot. Exposed for boot-time observability
// (HostIO.Stats) — an unexpectedly slow open is either many small reads or a
// few large ones, and those have different fixes.
func (r *Runtime) FileIO() *HostIO {
	if r == nil {
		return nil
	}
	return r.io
}

// DiskBackedAvailable reports whether this runtime can open a database against
// real files at all.
func (r *Runtime) DiskBackedAvailable() bool { return r != nil && r.io != nil }

// EngineMode describes how this runtime is executing the engine, for boot-time
// observability. Silent interpreter fallback (a stale AOT cache key after an
// engine-bytes or libwasmedge upgrade) has bitten production twice: it has no
// symptom other than a ~100x slowdown, so the daemon logs this at startup.
type EngineMode struct {
	AOT bool // true: executing a precompiled native artifact
	// ArtifactPath is the AOT artifact actually loaded (AOT only).
	ArtifactPath string
	// CacheDir is the AOT cache that was consulted ("" if AOT was not requested).
	CacheDir string
	// MissReason explains why the precompiled artifact was not used (interpreted
	// fallback only).
	MissReason string
}

// Mode reports how the engine is executing (AOT + artifact path, or interpreted
// plus the reason the artifact was not loaded).
func (r *Runtime) Mode() EngineMode {
	return EngineMode{
		AOT:          r.aot,
		ArtifactPath: r.aotPath,
		CacheDir:     r.aotCacheDir,
		MissReason:   r.aotMiss,
	}
}

// Poisoned reports whether this runtime has trapped.
//
// The embedded engine is built WITHOUT exception support (see README).
// User-triggerable errors (bad SQL, unknown template, param mismatch,
// duplicate source, bad schema) are non-throwing since the flatsql A.3c
// refactor and do NOT poison the runtime. A trap (wasm-level failure of an
// engine export — a C++ throw on some untouched internal path, OOM,
// unreachable) may leave guest state corrupted: a poisoned runtime must be
// discarded and recreated; continuing to use it can hang or corrupt results.
//
// Reports poisoned when EITHER this layer saw a trap or the underlying wasmrt
// module poisoned itself (a trap it classified, or a dedicated-thread call
// abandoned on its wall-clock budget). storage.RecoverPoisonedEngine polls this,
// so both sources must be visible here or a dead engine is never replaced.
func (r *Runtime) Poisoned() bool { return r.poisoned || r.mod.Poisoned() }

// MarkPoisoned records an externally observed trap (e.g. a linked flow's
// direct engine dispatch trapping mid-call — the engine state may be
// corrupted exactly like a host-invoked trap). The runtime must be replaced.
func (r *Runtime) MarkPoisoned() { r.poisoned = true }

// free releases guest memory — UNLESS the runtime is poisoned, in which case it
// does nothing at all.
//
// This is not an optimisation, it is the difference between a daemon that
// reports a failed replay and a daemon that burns a core forever. Every alloc
// site here frees through `defer`, so on a trap the deferred free runs on a
// guest whose allocator state is exactly what the trap corrupted, and
// `Module.Deallocate` calls the guest's free() on `context.Background()` with
// no budget and no cancellation. Measured live on host-02 (2026-07-29): the
// record-catalog replay trapped in flatsql_query_params, the deferred free
// entered a five-instruction loop in the AOT artifact (99.7% of perf samples in
// a 14-byte address range), and because the module lock is still held by the
// caller, the trap error never returned, RecoverPoisonedEngine was never
// reached, and the box sat at 98% CPU from boot with no flow running.
//
// Leaking the buffer is free: a poisoned runtime is discarded wholesale, so its
// entire linear memory goes with it.
// The guard runs BEFORE r.mod is even read: `free` is reachable on a nil
// Runtime (TestNilRuntimeIsNotEntered pins this), and evaluating r.mod to pass
// it along would dereference nil before the guard could refuse. Delegating
// through freeVia(r.mod, …) is exactly that mistake and it segfaults.
func (r *Runtime) free(ptr uint32) {
	if !r.mayCallGuest() {
		return
	}
	r.mod.Deallocate(ptr)
}

// freeVia is free() against an explicit guest caller, so the same guard applies
// whether the free rides inside a RunOnExecThread batch or pays its own thread
// handoff. Only reachable from inside a batch, i.e. from a live Runtime.
func (r *Runtime) freeVia(inv wasmrt.GuestCaller, ptr uint32) {
	if !r.mayCallGuest() {
		return
	}
	inv.Deallocate(ptr)
}

// mayCallGuest reports whether it is still safe to enter the guest. Split out
// from free() so the guard's DECISION can be pinned by a test without a test
// having to call into a real engine (or, worse, a nil one — reaching wasmrt
// with no module segfaults inside cgo rather than raising a recoverable Go
// panic, which is a trap for anyone tempted to prove this with a zero-value
// Runtime).
// It also consults the underlying wasmrt module, which poisons ITSELF on any
// trap and on an abandoned dedicated-thread call. That second source matters:
// wasmrt can observe a breach this layer never sees as an execErr (a wall-clock
// abandonment happens in the dispatch, not in a guest return), and unless it is
// surfaced here, storage.RecoverPoisonedEngine would never learn the engine
// needs replacing and the daemon would run on a dead engine forever.
//
// Module.Poisoned is nil-safe, so this stays valid for the zero-value Runtime
// the note above warns about.
func (r *Runtime) mayCallGuest() bool {
	return r != nil && !r.poisoned && !r.mod.Poisoned()
}

// WasmModule exposes the underlying wasmrt module so hosts can register the
// LIVE engine instance into dependent VMs (wasmrt.WithLinkedModuleFrom) —
// the loop C.7 direct-linkage wiring. The instance is named
// EngineImportModule.
func (r *Runtime) WasmModule() *wasmrt.Module { return r.mod }

// New instantiates the FlatSQL WASI reactor and runs its _initialize export.
func New(opts ...Option) (*Runtime, error) {
	cfg := &config{maxMemoryPages: DefaultMaxMemoryPages, wasmBytes: flatsqlWasm}
	for _, o := range opts {
		o(cfg)
	}

	runBytes := cfg.wasmBytes
	aot := false
	aotPath := ""
	aotMiss := ""
	if cfg.aotCacheDir != "" {
		var compiled []byte
		var err error
		if cfg.aotCompileOnMiss {
			compiled, err = ensureAOT(cfg.aotCacheDir, cfg.wasmBytes)
		} else {
			compiled, err = LoadAOTArtifact(cfg.aotCacheDir, engineAOTPrefix, cfg.wasmBytes)
		}
		if err == nil {
			runBytes = compiled
			aot = true
			// Record WHICH artifact we are actually executing so the daemon can
			// log it at boot. A cache-key mismatch (engine bytes or libwasmedge
			// version changed) silently degrades to the interpreter; without this
			// the only symptom is "everything is 100x slower" in production.
			aotPath = aotArtifactPath(cfg.aotCacheDir, engineAOTPrefix, cfg.wasmBytes)
		} else {
			// Keep the reason so callers can log WHY they fell back rather than
			// just that they did (expected-path miss vs. a real load failure).
			aotMiss = err.Error()
		}
		// On cache miss or compile failure, fall back to interpreting the
		// portable bytes; callers can check Runtime.AOT() and warn. Daemon
		// startup uses the no-compile path so it never invokes the AOT
		// compiler as part of service start.
	}

	// The engine's OWN filesystem. flatsql brings its own sqlite3_vfs over
	// seven explicit imports on module "env" (hostio.go); those imports are
	// UNCONDITIONAL in the artifact, so this registration is what makes the
	// module instantiable at all — it is not an optional feature.
	var fileIO *HostIO
	ioFuncs := refusingHostFuncs()
	if cfg.fileIORoot != "" {
		io, ioErr := NewHostIO(cfg.fileIORoot)
		if ioErr != nil {
			return nil, fmt.Errorf("flatsqlrt: %w", ioErr)
		}
		fileIO = io
		ioFuncs = fileIO.hostFuncs()
	}

	mod, err := wasmrt.NewModule(runBytes,
		wasmrt.WithWASI(),
		wasmrt.WithHostModule(HostIOModule, ioFuncs),
		wasmrt.WithMaxMemoryPages(cfg.maxMemoryPages),
		// The engine is routinely executed from INSIDE another module's host
		// function (storage.flatsql_* capability hostcalls issued by AOT flow
		// mounts). libwasmedge 0.14 corrupts per-thread executor state on
		// nested AOT-inside-AOT execution — the engine therefore always runs
		// on its own locked OS thread (docs/wasmedge-aot-nested-execution.md).
		wasmrt.WithDedicatedThread(),
		// The engine instantiates as the NAMED module "flatsql" so its LIVE
		// instance can be registered into dependent VMs (linked flow
		// artifacts import flatsql.malloc/free/flatsql_* directly — loop C.7,
		// docs/flatsql-component-linkage.md B-iv). Direct linked calls are
		// safe on the CALLER's thread (one contiguous executor invocation,
		// §7.1) but require the module lock (SQLITE_THREADSAFE=0).
		wasmrt.WithRegisteredName(EngineImportModule),
		// THE ENGINE MUST BE BOUNDED. It is the most-invoked module in the
		// daemon, it is invoked while the FlatSQLStore lock is held, and that
		// lock is reachable from the libp2p connection gater — so an unbounded
		// engine call is not a slow query, it is a total node outage. Until
		// 2026-07-30 this call site set NEITHER budget, which is why one guest
		// trap could hold the store lock indefinitely (see wasmrt's
		// ErrModulePoisoned). The dedicated thread means the guest itself
		// cannot be interrupted, but the CALLER is now bounded and the module
		// is poisoned on breach, so the host always unwinds.
		wasmrt.WithExecTimeout(resolveEngineExecTimeout(cfg.execTimeout)),
	)
	if err != nil {
		return nil, fmt.Errorf("flatsqlrt: instantiate engine: %w", err)
	}
	if _, err := mod.Execute("_initialize"); err != nil {
		mod.Release()
		fileIO.CloseAll()
		return nil, fmt.Errorf("flatsqlrt: _initialize: %w", err)
	}
	r := &Runtime{mod: mod, aot: aot, aotPath: aotPath, aotMiss: aotMiss, aotCacheDir: cfg.aotCacheDir, io: fileIO}
	r.startDispatchLog()
	return r, nil
}

// dispatchLogInterval is how often the engine states its guest-call account.
//
// WHY THIS IS ON BY DEFAULT, and why it is once every thirty minutes. On
// 2026-08-09 `perf` on host-01 showed the daemon issuing ~87,000 rt_sigaction/s
// and ~84,000 futex/s, identical at idle and under load — tens of thousands of
// guest calls per second of BACKGROUND work — and nothing in the daemon could
// say which export they were. Naming that lane needed a laptop profiler on a
// production box. It should need a journal line.
//
// The rate is chosen against the opposite failure, which this repo also has on
// record: enabling WasmEdge's cost-measuring statistics made it dump three lines
// after EVERY guest invocation, 62 % of everything the daemon said in five
// minutes (see wasmrt's package comment). Forty-eight lines a day is not that.
//
// SDN_FLATSQL_DISPATCH_LOG_SEC=<seconds> retunes it; 0 disables.
var dispatchLogInterval = func() time.Duration {
	raw := strings.TrimSpace(os.Getenv("SDN_FLATSQL_DISPATCH_LOG_SEC"))
	if raw == "" {
		return 30 * time.Minute
	}
	var secs int64
	if _, err := fmt.Sscanf(raw, "%d", &secs); err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}()

// startDispatchLog reports the engine's guest-call account periodically: how
// many guest invocations ran, how many OS-thread handoffs they cost, and which
// exports they were. Deltas, not totals — a cumulative counter cannot show a
// lane that started an hour ago.
func (r *Runtime) startDispatchLog() {
	if dispatchLogInterval <= 0 {
		return
	}
	r.dispatchLogStop = make(chan struct{})
	go func() {
		t := time.NewTicker(dispatchLogInterval)
		defer t.Stop()
		prev := r.mod.DispatchStats()
		prevQ, prevSlow, prevWait, prevHeld := r.Stats()
		for {
			select {
			case <-r.dispatchLogStop:
				return
			case <-t.C:
				cur := r.mod.DispatchStats()
				curQ, curSlow, curWait, curHeld := r.Stats()
				calls := cur.Calls - prev.Calls
				disp := cur.Dispatches - prev.Dispatches
				secs := dispatchLogInterval.Seconds()
				perHandoff := float64(calls)
				if disp > 0 {
					perHandoff = float64(calls) / float64(disp)
				}
				// EVERY figure on this line is a DELTA over the interval. Mixing a
				// delta call count with a cumulative "held" is how a reader
				// concludes the engine held its lock for 73 seconds of a 60-second
				// window — which is exactly what the first version of this line
				// said on host-01 before it was corrected.
				held := curHeld - prevHeld
				log.Infof("FlatSQL engine dispatch account (last %s, all figures are deltas over that window): "+
					"%d guest calls (%.0f/s) on %d thread handoffs (%.1f calls/handoff, batching=%v); "+
					"%d statements, %d slow, %s waited, %s held (%.0f%% of the window); busiest exports (CUMULATIVE): %s",
					dispatchLogInterval, calls, float64(calls)/secs, disp, perHandoff, wasmrt.ExecBatchEnabled(),
					curQ-prevQ, curSlow-prevSlow, (curWait - prevWait).Round(time.Millisecond),
					held.Round(time.Millisecond), 100*held.Seconds()/secs, cur.Top(6))
				prev, prevQ, prevSlow, prevWait, prevHeld = cur, curQ, curSlow, curWait, curHeld
			}
		}
	}()
}

// Close releases the WasmEdge VM and all engine memory. Databases created on
// this runtime become invalid.
func (r *Runtime) Close() {
	if r.dispatchLogStop != nil {
		close(r.dispatchLogStop)
		r.dispatchLogStop = nil
	}
	if r.mod != nil {
		r.mod.Release()
		r.mod = nil
	}
	// Release the guest's file handles LAST and unconditionally: a discarded
	// (or poisoned) engine must not keep the store's database file open, or the
	// replacement engine opens a file the dead one is still writing through.
	r.io.CloseAll()
}

// MemoryStats reports the engine's current/max linear memory.
func (r *Runtime) MemoryStats() (wasmrt.MemoryStats, error) { return r.mod.MemoryStats() }

// lastError reads flatsql_get_error. Must be called with the module lock held.
func (r *Runtime) lastError() string { return r.lastErrorVia(r.mod) }

func (r *Runtime) lastErrorVia(inv wasmrt.GuestCaller) string {
	res, err := inv.Execute("flatsql_get_error")
	if err != nil || len(res) == 0 {
		return fmt.Sprintf("(flatsql_get_error unavailable: %v)", err)
	}
	ptr := toUint32(res[0])
	if ptr == 0 {
		return "(no engine error message)"
	}
	msg, err := r.readCStringVia(inv, ptr)
	if err != nil {
		return fmt.Sprintf("(error message unreadable: %v)", err)
	}
	return msg
}

// readCString scans a NUL-terminated guest string in bounded chunks, clamped
// to the current linear-memory size. Must be called with the module lock held.
func (r *Runtime) readCString(ptr uint32) (string, error) { return r.readCStringVia(r.mod, ptr) }

func (r *Runtime) readCStringVia(inv wasmrt.GuestCaller, ptr uint32) (string, error) {
	if ptr == 0 {
		return "", nil
	}
	stats, err := inv.MemoryStats()
	if err != nil {
		return "", err
	}
	memEnd := uint32(stats.Bytes)
	var buf []byte
	for off := ptr; off < memEnd && len(buf) < cstringMaxLen; {
		chunk := uint32(cstringReadChunk)
		if off+chunk > memEnd {
			chunk = memEnd - off
		}
		data, err := inv.ReadMemory(off, chunk)
		if err != nil {
			return "", err
		}
		if i := bytes.IndexByte(data, 0); i >= 0 {
			return string(append(buf, data[:i]...)), nil
		}
		buf = append(buf, data...)
		off += chunk
	}
	return string(buf), nil
}

// engineErr wraps a benign engine error (error-sentinel return + populated
// error latch). Since the flatsql no-throw refactor (loop A.3c),
// user-triggerable failures — bad SQL, param-count mismatch, unknown
// template, duplicate source, bad schema — take this path without throwing,
// so the runtime stays healthy and is NOT poisoned.
func (r *Runtime) engineErr(op string) error { return r.engineErrVia(r.mod, op) }

func (r *Runtime) engineErrVia(inv wasmrt.GuestCaller, op string) error {
	return fmt.Errorf("flatsqlrt: %s: %s", op, r.lastErrorVia(inv))
}

// execErr wraps a trap/host-level failure of an engine export call and marks
// the runtime poisoned.
func (r *Runtime) execErr(op string, err error) error {
	r.poisoned = true
	return fmt.Errorf("flatsqlrt: %s (runtime poisoned — recreate it): %w", op, err)
}

// checkUsable refuses an operation on a poisoned engine BEFORE the module lock
// is taken.
//
// wasmrt already refuses a poisoned module at dispatch, so this is not the
// safety net — it is the one that keeps callers off the LOCK. That distinction
// is the whole host-01 outage: the trapping call held the module lock, so every
// later caller blocked in mod.Lock() and never reached a poison check at all.
// Checking before the lock means an unwinding caller (sql.Tx.Rollback is the
// measured one) fails instantly instead of queueing behind a dead engine.
func (r *Runtime) checkUsable(op string) error {
	if r.mayCallGuest() {
		return nil
	}
	return fmt.Errorf("flatsqlrt: %s refused: engine is poisoned and awaiting replacement: %w",
		op, wasmrt.ErrModulePoisoned)
}

// allocCString copies s into guest memory as a NUL-terminated C string.
// Caller must Deallocate. Must be called with the module lock held.
func (r *Runtime) allocCString(s string) (uint32, error) { return r.allocCStringVia(r.mod, s) }

func (r *Runtime) allocCStringVia(inv wasmrt.GuestCaller, s string) (uint32, error) {
	return inv.Allocate(append([]byte(s), 0))
}

// allocBytes copies b into guest memory; empty slices pass ptr 0 with len 0
// (matching the JS shim's withBytes convention). Caller must Deallocate
// non-zero pointers.
func (r *Runtime) allocBytes(b []byte) (uint32, error) { return r.allocBytesVia(r.mod, b) }

func (r *Runtime) allocBytesVia(inv wasmrt.GuestCaller, b []byte) (uint32, error) {
	if len(b) == 0 {
		return 0, nil
	}
	return inv.Allocate(b)
}

// BuildQueryCacheKey returns the engine's deterministic cache key for a
// template query invocation (dataset + artifact version + query id + params).
func (r *Runtime) BuildQueryCacheKey(dataset, artifactVersion, queryID string, params ...interface{}) (string, error) {
	r.mod.Lock()
	defer r.mod.Unlock()

	blob, err := EncodeParams(params)
	if err != nil {
		return "", err
	}
	ptrs := make([]uint32, 0, 4)
	free := func() {
		for _, p := range ptrs {
			if p != 0 {
				r.free(p)
			}
		}
	}
	defer free()
	alloc := func(s string) (uint32, error) {
		p, err := r.allocCString(s)
		if err == nil {
			ptrs = append(ptrs, p)
		}
		return p, err
	}

	dsPtr, err := alloc(dataset)
	if err != nil {
		return "", err
	}
	verPtr, err := alloc(artifactVersion)
	if err != nil {
		return "", err
	}
	idPtr, err := alloc(queryID)
	if err != nil {
		return "", err
	}
	blobPtr, err := r.allocBytes(blob)
	if err != nil {
		return "", err
	}
	if blobPtr != 0 {
		ptrs = append(ptrs, blobPtr)
	}

	res, err := r.mod.Execute("flatsql_build_query_cache_key",
		int32(dsPtr), int32(verPtr), int32(idPtr),
		int32(blobPtr), int32(len(blob)), int32(len(params)))
	if err != nil {
		return "", r.execErr("flatsql_build_query_cache_key", err)
	}
	keyPtr := toUint32(res[0])
	if keyPtr == 0 {
		return "", r.engineErr("build_query_cache_key")
	}
	return r.readCString(keyPtr)
}

// ResponseArtifactKeyOptions mirrors the JS buildResponseArtifactCacheKey
// options (format defaults to "json"; Projection is newline-joined).
type ResponseArtifactKeyOptions struct {
	Format          string
	PublishEventKey string
	Projection      []string
	Params          []interface{}
}

// BuildResponseArtifactCacheKey returns the engine's deterministic cache key
// for a response artifact (the ETag/conditional-GET identity of a query
// result).
func (r *Runtime) BuildResponseArtifactCacheKey(schemaName, schemaVersion, sql string, opts ResponseArtifactKeyOptions) (string, error) {
	r.mod.Lock()
	defer r.mod.Unlock()

	format := opts.Format
	if format == "" {
		format = "json"
	}
	projection := ""
	for i, p := range opts.Projection {
		if i > 0 {
			projection += "\n"
		}
		projection += p
	}
	blob, err := EncodeParams(opts.Params)
	if err != nil {
		return "", err
	}

	ptrs := make([]uint32, 0, 7)
	defer func() {
		for _, p := range ptrs {
			if p != 0 {
				r.free(p)
			}
		}
	}()
	alloc := func(s string) (uint32, error) {
		p, err := r.allocCString(s)
		if err == nil {
			ptrs = append(ptrs, p)
		}
		return p, err
	}

	schemaPtr, err := alloc(schemaName)
	if err != nil {
		return "", err
	}
	verPtr, err := alloc(schemaVersion)
	if err != nil {
		return "", err
	}
	sqlPtr, err := alloc(sql)
	if err != nil {
		return "", err
	}
	fmtPtr, err := alloc(format)
	if err != nil {
		return "", err
	}
	evtPtr, err := alloc(opts.PublishEventKey)
	if err != nil {
		return "", err
	}
	projPtr, err := alloc(projection)
	if err != nil {
		return "", err
	}
	blobPtr, err := r.allocBytes(blob)
	if err != nil {
		return "", err
	}
	if blobPtr != 0 {
		ptrs = append(ptrs, blobPtr)
	}

	res, err := r.mod.Execute("flatsql_build_response_artifact_cache_key",
		int32(schemaPtr), int32(verPtr), int32(sqlPtr), int32(fmtPtr),
		int32(evtPtr), int32(projPtr),
		int32(blobPtr), int32(len(blob)), int32(len(opts.Params)))
	if err != nil {
		return "", r.execErr("flatsql_build_response_artifact_cache_key", err)
	}
	keyPtr := toUint32(res[0])
	if keyPtr == 0 {
		return "", r.engineErr("build_response_artifact_cache_key")
	}
	return r.readCString(keyPtr)
}

// CreateDatabase parses a FlatBuffers .fbs schema and creates one SQL table
// per schema table. name is a diagnostic label.
func (r *Runtime) CreateDatabase(schema, name string) (*Database, error) {
	r.mod.Lock()
	defer r.mod.Unlock()

	schemaPtr, err := r.allocCString(schema)
	if err != nil {
		return nil, err
	}
	defer r.free(schemaPtr)
	namePtr, err := r.allocCString(name)
	if err != nil {
		return nil, err
	}
	defer r.free(namePtr)

	res, err := r.mod.Execute("flatsql_create_db", int32(schemaPtr), int32(namePtr))
	if err != nil {
		return nil, r.execErr("flatsql_create_db", err)
	}
	handle := toUint32(res[0])
	if handle == 0 {
		return nil, r.engineErr("create_db")
	}
	return &Database{rt: r, handle: handle, name: name}, nil
}

// Database is one FlatSQL database (a set of FlatBuffer-backed tables) on a
// Runtime.
type Database struct {
	rt     *Runtime
	handle uint32
	name   string

	// rawMirror holds recently materialized raw streams host-side, keyed by
	// (generation, sql, params) — see rawstream_mirror.go. Lazily created;
	// guarded by the Runtime's module lock like all database state.
	rawMirror *rawStreamMirror

	// engineRefMirror holds bodies harvested from LINKED flows' engine
	// body-references, keyed by (generation, fnv1a64, size) — content
	// identity under the engine's own staleness authority. Same locking as
	// rawMirror.
	engineRefMirror *rawStreamMirror
}

// Name returns the diagnostic label the database was created with.
func (d *Database) Name() string { return d.name }

// Handle exposes the engine-side database handle — the value linked flow
// artifacts receive through sdn_flatsql_link_init and pass to their direct
// flatsql_* calls (loop C.7).
func (d *Database) Handle() uint32 { return d.handle }

// Destroy releases the engine-side database. The handle must not be used
// afterwards.
func (d *Database) Destroy() {
	if d.handle == 0 {
		return
	}
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()
	d.rt.mod.Execute("flatsql_destroy_db", int32(d.handle))
	d.handle = 0
}

// RegisterFileID maps a 4-byte FlatBuffer file identifier (e.g. "$OMM") to a
// schema table so ingest can route buffers by their embedded identifier.
func (d *Database) RegisterFileID(fileID, tableName string) error {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	fidPtr, err := d.rt.allocCString(fileID)
	if err != nil {
		return err
	}
	defer d.rt.free(fidPtr)
	tblPtr, err := d.rt.allocCString(tableName)
	if err != nil {
		return err
	}
	defer d.rt.free(tblPtr)

	if _, err := d.rt.mod.Execute("flatsql_register_file_id", int32(d.handle), int32(fidPtr), int32(tblPtr)); err != nil {
		return d.rt.execErr("flatsql_register_file_id", err)
	}
	return nil
}

// RegisterSource creates per-source shadow tables (`Base@source`) so ingest
// can be partitioned by provider/source.
func (d *Database) RegisterSource(source string) error {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	srcPtr, err := d.rt.allocCString(source)
	if err != nil {
		return err
	}
	defer d.rt.free(srcPtr)

	// flatsql_register_source is a void export: failures (e.g. duplicate
	// source) only land in the error latch, so detect success by the source
	// count changing.
	before, err := d.rt.mod.Execute("flatsql_get_sources_count", int32(d.handle))
	if err != nil {
		return d.rt.execErr("flatsql_get_sources_count", err)
	}
	if _, err := d.rt.mod.Execute("flatsql_register_source", int32(d.handle), int32(srcPtr)); err != nil {
		return d.rt.execErr("flatsql_register_source", err)
	}
	after, err := d.rt.mod.Execute("flatsql_get_sources_count", int32(d.handle))
	if err != nil {
		return d.rt.execErr("flatsql_get_sources_count", err)
	}
	if wasmrt.ToInt32(after[0]) == wasmrt.ToInt32(before[0]) {
		return d.rt.engineErr("register_source")
	}
	return nil
}

// CreateUnifiedViews replaces each base table name with a UNION ALL view over
// its per-source shadow tables, adding a `_source` column.
func (d *Database) CreateUnifiedViews() error {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()
	if _, err := d.rt.mod.Execute("flatsql_create_unified_views", int32(d.handle)); err != nil {
		return d.rt.execErr("flatsql_create_unified_views", err)
	}
	return nil
}

// MarkDeleted tombstones one record (identified by its vtab `_rowid`
// sequence) in a table or per-source shadow table (full name, e.g.
// "OMM@celestrak-gp"). Queries skip tombstoned records immediately; the
// underlying arena bytes are only reclaimed by a rebuild. CAUTION: the table
// name MUST be a registered source/table — an unknown name throws inside the
// engine, which traps (poisons) the no-EH build. Callers should pass names
// obtained from the engine itself (e.g. the unified view's `_source` column).
func (d *Database) MarkDeleted(tableName string, sequence uint64) error {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	tblPtr, err := d.rt.allocCString(tableName)
	if err != nil {
		return err
	}
	defer d.rt.free(tblPtr)

	if _, err := d.rt.mod.Execute("flatsql_mark_deleted", int32(d.handle), int32(tblPtr), float64(sequence)); err != nil {
		return d.rt.execErr("flatsql_mark_deleted", err)
	}
	return nil
}

// DeletedCount reports how many records are tombstoned in a table (0 for
// unknown tables — the engine treats that case as empty, not an error).
func (d *Database) DeletedCount(tableName string) (int64, error) {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	tblPtr, err := d.rt.allocCString(tableName)
	if err != nil {
		return 0, err
	}
	defer d.rt.free(tblPtr)

	res, err := d.rt.mod.Execute("flatsql_get_deleted_count", int32(d.handle), int32(tblPtr))
	if err != nil {
		return 0, d.rt.execErr("flatsql_get_deleted_count", err)
	}
	return int64(toFloat64(res[0])), nil
}

// ClearTombstones forgets a table's tombstone set (used after a compaction
// rebuild re-ingested only the live records).
func (d *Database) ClearTombstones(tableName string) error {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	tblPtr, err := d.rt.allocCString(tableName)
	if err != nil {
		return err
	}
	defer d.rt.free(tblPtr)

	if _, err := d.rt.mod.Execute("flatsql_clear_tombstones", int32(d.handle), int32(tblPtr)); err != nil {
		return d.rt.execErr("flatsql_clear_tombstones", err)
	}
	return nil
}

// Ingest appends a size-prefixed FlatBuffer stream. Returns the number of
// bytes consumed from the stream (== len(stream) on full success; the C ABI
// does not expose the per-call record count).
func (d *Database) Ingest(stream []byte) (int, error) {
	return d.ingest("flatsql_ingest", stream, "")
}

// IngestOne appends a single FlatBuffer (no size prefix). Returns the
// engine-assigned record sequence.
func (d *Database) IngestOne(buf []byte) (int, error) {
	return d.ingest("flatsql_ingest_one", buf, "")
}

// IngestWithSource appends a size-prefixed stream into the source's shadow
// tables (bytes consumed returned). The source must have been registered
// with RegisterSource.
func (d *Database) IngestWithSource(stream []byte, source string) (int, error) {
	return d.ingest("flatsql_ingest_with_source", stream, source)
}

// IngestOneWithSource appends one FlatBuffer into the source's shadow tables.
func (d *Database) IngestOneWithSource(buf []byte, source string) (int, error) {
	return d.ingest("flatsql_ingest_one_with_source", buf, source)
}

func (d *Database) ingest(export string, data []byte, source string) (int, error) {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	// Ingest is 3-5 guest calls (malloc, maybe malloc, ingest, free, free) and
	// it runs once per shard on every replay and every feed head — one batch
	// per ingest instead of five handoffs.
	var count int
	err := d.rt.mod.RunOnExecThread(context.Background(), func(inv wasmrt.GuestCaller) error {
		dataPtr, err := d.rt.allocBytesVia(inv, data)
		if err != nil {
			return err
		}
		if dataPtr != 0 {
			defer d.rt.freeVia(inv, dataPtr)
		}

		args := []interface{}{int32(d.handle), int32(dataPtr), int32(len(data))}
		if source != "" {
			srcPtr, err := d.rt.allocCStringVia(inv, source)
			if err != nil {
				return err
			}
			defer d.rt.freeVia(inv, srcPtr)
			args = append(args, int32(srcPtr))
		}

		res, err := inv.Execute(export, args...)
		if err != nil {
			return fmt.Errorf("flatsqlrt: %s: %w", export, err)
		}
		n := toFloat64(res[0])
		if n < 0 {
			return d.rt.engineErrVia(inv, export)
		}
		count = int(n)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// Result is a materialized row/column query result.
type Result struct {
	Columns []string
	Rows    [][]interface{}
}

// Query runs SQL and materializes rows. Params (optional) bind `?`
// placeholders; supported Go types: nil, bool, all ints, float32/64, string,
// []byte.
func (d *Database) Query(sql string, params ...interface{}) (*Result, error) {
	// Before the lock, deliberately — see checkUsable. This is the exact call
	// the database/sql Rollback path takes, and the one that re-entered a
	// trapped engine and hung host-01 for 41 minutes.
	if err := d.rt.checkUsable("query"); err != nil {
		return nil, err
	}
	queued := time.Now()
	d.rt.mod.Lock()
	waited := time.Since(queued)
	started := time.Now()
	defer func() {
		d.rt.mod.Unlock()
		d.rt.accountQuery(sql, waited, time.Since(started))
	}()

	// ONE unit of work on the engine thread: the statement AND its whole
	// materialization. The lock above is already held for exactly this span, so
	// the batch changes no concurrency property — only how many times control
	// crosses an OS thread boundary to get here (once, instead of once per
	// result cell). See wasmrt.Module.RunOnExecThread.
	var out *Result
	err := d.rt.mod.RunOnExecThread(context.Background(), func(inv wasmrt.GuestCaller) error {
		if err := d.execQuery(inv, sql, params); err != nil {
			return err
		}
		res, err := d.readCurrentResult(inv)
		if err != nil {
			return err
		}
		out = res
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// slowQueryThreshold is when a single engine statement becomes evidence
// instead of noise.
//
// THE INSTRUMENT THAT WAS MISSING. Three P1 latency tasks
// (sdn-pin-ledger-probe-costs-76-seconds, sdn-record-by-cid-read-12-to-29-seconds,
// sdn-flatsql-engine-read-queue-seconds-per-call) all reduced to ONE defect —
// a read source that full-scanned two 250k-row tables — and none of them could
// see it, because the engine reported nothing about what it was executing or
// how long anything waited. The daemon spent 96 % of a 2-vCPU box re-reading the
// same 10,534 pages and the only visible symptom was "the API is slow".
//
// The engine is ONE single-threaded instance behind ONE lock, so a statement
// that takes a second does not cost one request a second — it costs EVERY
// concurrent request a second. That makes the two numbers below the ones worth
// printing: how long this statement waited for the engine, and how long it then
// held it. Both are needed: a long WAIT names a victim, a long HOLD names a
// culprit.
//
// Override with SDN_FLATSQL_SLOW_QUERY_MS; 0 disables.
var slowQueryThreshold = func() time.Duration {
	if v := strings.TrimSpace(os.Getenv("SDN_FLATSQL_SLOW_QUERY_MS")); v != "" {
		var ms int64
		if _, err := fmt.Sscanf(v, "%d", &ms); err == nil {
			if ms <= 0 {
				return 0
			}
			return time.Duration(ms) * time.Millisecond
		}
	}
	return 250 * time.Millisecond
}()

// Stats returns the engine's cumulative statement account: how many statements
// ran, how many were slow, how long callers spent WAITING for the engine lock,
// and how long they HELD it. Cheap enough to be always on (four atomic adds per
// statement) and the only way to tell a saturated engine from a slow handler.
func (r *Runtime) Stats() (queries, slow int64, totalWait, totalHeld time.Duration) {
	return r.statQueries.Load(), r.statSlow.Load(),
		time.Duration(r.statWaitNanos.Load()), time.Duration(r.statHeldNanos.Load())
}

// accountQuery records one statement and logs it when it crossed the slow
// threshold on EITHER axis. The SQL is logged whole: these statements are
// composed by the storage layer, and the composition (a union read source, a
// window, a join) is precisely the thing that has to be identifiable.
func (r *Runtime) accountQuery(sql string, waited, held time.Duration) {
	r.statQueries.Add(1)
	r.statWaitNanos.Add(int64(waited))
	r.statHeldNanos.Add(int64(held))
	if slowQueryThreshold <= 0 || (waited < slowQueryThreshold && held < slowQueryThreshold) {
		return
	}
	r.statSlow.Add(1)
	log.Warnf("FlatSQL slow statement: held %s, waited %s for the engine lock — the engine is single-threaded, so this cost EVERY concurrent reader the same wait. SQL: %s",
		held.Round(time.Millisecond), waited.Round(time.Millisecond), collapseSQL(sql))
}

// collapseSQL squeezes a composed statement onto one log line without losing
// the table names, which are the whole point of logging it.
func collapseSQL(sql string) string {
	s := strings.Join(strings.Fields(sql), " ")
	const max = 1200
	if len(s) > max {
		return s[:max] + " …(truncated)"
	}
	return s
}

// execQuery runs flatsql_query or flatsql_query_params. Lock must be held.
func (d *Database) execQuery(inv wasmrt.GuestCaller, sql string, params []interface{}) error {
	sqlPtr, err := d.rt.allocCStringVia(inv, sql)
	if err != nil {
		return err
	}
	defer d.rt.freeVia(inv, sqlPtr)

	if len(params) == 0 {
		res, err := inv.Execute("flatsql_query", int32(d.handle), int32(sqlPtr))
		if err != nil {
			return d.rt.execErr("flatsql_query", err)
		}
		if wasmrt.ToInt32(res[0]) == 0 {
			return d.rt.engineErrVia(inv, "query")
		}
		return nil
	}

	blob, err := EncodeParams(params)
	if err != nil {
		return err
	}
	blobPtr, err := d.rt.allocBytesVia(inv, blob)
	if err != nil {
		return err
	}
	if blobPtr != 0 {
		defer d.rt.freeVia(inv, blobPtr)
	}
	res, err := inv.Execute("flatsql_query_params",
		int32(d.handle), int32(sqlPtr), int32(blobPtr), int32(len(blob)), int32(len(params)))
	if err != nil {
		return d.rt.execErr("flatsql_query_params", err)
	}
	if wasmrt.ToInt32(res[0]) == 0 {
		return d.rt.engineErrVia(inv, "query_params")
	}
	return nil
}

// Cell type tags returned by flatsql_result_cell_type.
const (
	cellNull   = 0
	cellBool   = 1
	cellInt32  = 2
	cellInt64  = 3
	cellFloat  = 4
	cellString = 5
	cellBlob   = 6
)

// readCurrentResult materializes the engine's current result. Lock must be held.
//
// THIS IS THE CALL-COUNT HOT SPOT. Materializing R rows x C columns costs
// 2 + C + 2*R*C guest invocations — the engine has no export that hands back a
// whole result in one buffer for a TRUSTED caller (flatsql_query_sandboxed does
// exactly that, but its SQLite authorizer makes control tables invisible, so it
// cannot serve the store's own reads). A 30-row x 15-column control table is
// therefore ~920 calls for ONE indexed read.
//
// Until the engine grows a bulk-result export, the fix is to stop paying a
// cross-OS-thread handoff for each of them: `inv` is the batch's GuestCaller,
// and every call below rides the single handoff RunOnExecThread already paid.
func (d *Database) readCurrentResult(inv wasmrt.GuestCaller) (*Result, error) {
	colsRes, err := inv.Execute("flatsql_result_column_count")
	if err != nil {
		return nil, err
	}
	rowsRes, err := inv.Execute("flatsql_result_row_count")
	if err != nil {
		return nil, err
	}
	nCols := int(wasmrt.ToInt32(colsRes[0]))
	nRows := int(wasmrt.ToInt32(rowsRes[0]))

	out := &Result{Columns: make([]string, nCols), Rows: make([][]interface{}, nRows)}
	for c := 0; c < nCols; c++ {
		nameRes, err := inv.Execute("flatsql_result_column_name", int32(c))
		if err != nil {
			return nil, err
		}
		name, err := d.rt.readCStringVia(inv, toUint32(nameRes[0]))
		if err != nil {
			return nil, err
		}
		out.Columns[c] = name
	}
	for rIdx := 0; rIdx < nRows; rIdx++ {
		row := make([]interface{}, nCols)
		for c := 0; c < nCols; c++ {
			v, err := d.readCell(inv, rIdx, c)
			if err != nil {
				return nil, err
			}
			row[c] = v
		}
		out.Rows[rIdx] = row
	}
	return out, nil
}

func (d *Database) readCell(inv wasmrt.GuestCaller, row, col int) (interface{}, error) {
	m := inv
	tRes, err := m.Execute("flatsql_result_cell_type", int32(row), int32(col))
	if err != nil {
		return nil, err
	}
	switch wasmrt.ToInt32(tRes[0]) {
	case cellNull:
		return nil, nil
	case cellBool:
		v, err := m.Execute("flatsql_result_cell_number", int32(row), int32(col))
		if err != nil {
			return nil, err
		}
		return toFloat64(v[0]) != 0, nil
	case cellInt32, cellInt64:
		v, err := m.Execute("flatsql_result_cell_number", int32(row), int32(col))
		if err != nil {
			return nil, err
		}
		return int64(toFloat64(v[0])), nil
	case cellFloat:
		v, err := m.Execute("flatsql_result_cell_number", int32(row), int32(col))
		if err != nil {
			return nil, err
		}
		return toFloat64(v[0]), nil
	case cellString:
		v, err := m.Execute("flatsql_result_cell_string", int32(row), int32(col))
		if err != nil {
			return nil, err
		}
		return d.rt.readCStringVia(inv, toUint32(v[0]))
	case cellBlob:
		ptrRes, err := m.Execute("flatsql_result_cell_blob", int32(row), int32(col))
		if err != nil {
			return nil, err
		}
		sizeRes, err := m.Execute("flatsql_result_cell_blob_size", int32(row), int32(col))
		if err != nil {
			return nil, err
		}
		size := uint32(wasmrt.ToInt32(sizeRes[0]))
		if size == 0 {
			return []byte{}, nil
		}
		return m.ReadMemory(toUint32(ptrRes[0]), size)
	default:
		return nil, fmt.Errorf("flatsqlrt: unknown cell type at (%d,%d)", row, col)
	}
}

// RegisterQueryTemplate registers a reusable (optionally cached) query.
func (d *Database) RegisterQueryTemplate(queryID, sql string, cacheable bool) error {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	idPtr, err := d.rt.allocCString(queryID)
	if err != nil {
		return err
	}
	defer d.rt.free(idPtr)
	sqlPtr, err := d.rt.allocCString(sql)
	if err != nil {
		return err
	}
	defer d.rt.free(sqlPtr)

	c := int32(0)
	if cacheable {
		c = 1
	}
	res, err := d.rt.mod.Execute("flatsql_register_query_template", int32(d.handle), int32(idPtr), int32(sqlPtr), c)
	if err != nil {
		return d.rt.execErr("flatsql_register_query_template", err)
	}
	if wasmrt.ToInt32(res[0]) == 0 {
		return d.rt.engineErr("register_query_template")
	}
	return nil
}

// QueryTemplate executes a registered template with bound params.
func (d *Database) QueryTemplate(queryID string, params ...interface{}) (*Result, error) {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	var out *Result
	err := d.rt.mod.RunOnExecThread(context.Background(), func(inv wasmrt.GuestCaller) error {
		idPtr, err := d.rt.allocCStringVia(inv, queryID)
		if err != nil {
			return err
		}
		defer d.rt.freeVia(inv, idPtr)

		blob, err := EncodeParams(params)
		if err != nil {
			return err
		}
		blobPtr, err := d.rt.allocBytesVia(inv, blob)
		if err != nil {
			return err
		}
		if blobPtr != 0 {
			defer d.rt.freeVia(inv, blobPtr)
		}

		res, err := inv.Execute("flatsql_query_template",
			int32(d.handle), int32(idPtr), int32(blobPtr), int32(len(blob)), int32(len(params)))
		if err != nil {
			return d.rt.execErr("flatsql_query_template", err)
		}
		if wasmrt.ToInt32(res[0]) == 0 {
			return d.rt.engineErrVia(inv, "query_template")
		}
		r, err := d.readCurrentResult(inv)
		if err != nil {
			return err
		}
		out = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// QueryMany executes a batch of queries in one guest call and materializes
// each result in order.
func (d *Database) QueryMany(reqs []QueryRequest) ([]*Result, error) {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	var results []*Result
	err := d.rt.mod.RunOnExecThread(context.Background(), func(inv wasmrt.GuestCaller) error {
		blob, err := EncodeQueryRequests(reqs)
		if err != nil {
			return err
		}
		blobPtr, err := d.rt.allocBytesVia(inv, blob)
		if err != nil {
			return err
		}
		if blobPtr != 0 {
			defer d.rt.freeVia(inv, blobPtr)
		}

		res, err := inv.Execute("flatsql_query_many",
			int32(d.handle), int32(blobPtr), int32(len(blob)), int32(len(reqs)))
		if err != nil {
			return d.rt.execErr("flatsql_query_many", err)
		}
		if wasmrt.ToInt32(res[0]) == 0 {
			return d.rt.engineErrVia(inv, "query_many")
		}

		countRes, err := inv.Execute("flatsql_batch_result_count")
		if err != nil {
			return err
		}
		n := int(wasmrt.ToInt32(countRes[0]))
		results = make([]*Result, 0, n)
		for i := 0; i < n; i++ {
			selRes, err := inv.Execute("flatsql_select_batch_result", int32(i))
			if err != nil {
				return err
			}
			if wasmrt.ToInt32(selRes[0]) == 0 {
				return d.rt.engineErrVia(inv, fmt.Sprintf("select_batch_result(%d)", i))
			}
			r, err := d.readCurrentResult(inv)
			if err != nil {
				return err
			}
			results = append(results, r)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// RawStream is an aligned, size-prefixed FlatBuffer stream query result:
// concatenated [u32le length][bytes] frames, plus the row/column counts the
// engine reported for the artifact.
type RawStream struct {
	Bytes   []byte
	Rows    int
	Columns int
	// CacheHit reports whether the engine served this stream from its
	// response-artifact cache (no SQL re-execution — loop C.5b).
	CacheHit bool
	// MirrorHit reports the stream was served from the HOST-side mirror
	// (zero engine execution, zero memory copies — loop C.5c). Implies the
	// bytes are identical to what the engine would return: the mirror is
	// keyed by the engine's own (generation, sql, params) identity.
	MirrorHit bool
	// FNV1a64 is the word-folded FNV-1a 64 content hash of Bytes (the
	// canonical body-reference / entity-tag identity — FNV1a64WordFolded).
	FNV1a64 uint64
	// FrameCount counts Bytes' size-prefixed frames, skipping zero-length
	// prefixes (the modules' x-sdn-record-count rule); -1 when the framing
	// is malformed.
	FrameCount int
}

// QueryRawFlatBufferStream executes SQL whose every result cell is a BLOB
// (e.g. `SELECT _data FROM OMM WHERE ...`) and returns the aligned
// size-prefixed frame stream — the wire format served to the network.
//
// Repeated queries whose engine generation is unchanged are served from the
// host-side mirror without executing anything in the engine (see
// rawstream_mirror.go). Callers MUST NOT mutate the returned Bytes.
func (d *Database) QueryRawFlatBufferStream(sql string, params ...interface{}) (*RawStream, error) {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	blob, err := EncodeParams(params)
	if err != nil {
		return nil, err
	}

	// Host mirror first: (generation, sql, params) is the engine's own
	// staleness identity — a hit is byte-equivalent to re-reading the
	// engine's cached artifact, minus the round-trip and the copy. This one
	// probe stays OUTSIDE the batch so a mirror hit costs a single guest call
	// and never enters the exec thread for a readout it will not do.
	preGen, genErr := d.queryCacheGenerationLocked(d.rt.mod)
	mirrorKey := ""
	if genErr == nil {
		mirrorKey = rawMirrorKey(sql, blob, preGen)
		if d.rawMirror != nil {
			if cached := d.rawMirror.get(mirrorKey); cached != nil {
				hit := *cached
				hit.CacheHit = true
				hit.MirrorHit = true
				return &hit, nil
			}
		}
	}

	var stream *RawStream
	err = d.rt.mod.RunOnExecThread(context.Background(), func(inv wasmrt.GuestCaller) error {
		sqlPtr, err := d.rt.allocCStringVia(inv, sql)
		if err != nil {
			return err
		}
		defer d.rt.freeVia(inv, sqlPtr)

		blobPtr, err := d.rt.allocBytesVia(inv, blob)
		if err != nil {
			return err
		}
		if blobPtr != 0 {
			defer d.rt.freeVia(inv, blobPtr)
		}

		res, err := inv.Execute("flatsql_query_raw_flatbuffer_stream",
			int32(d.handle), int32(sqlPtr), int32(blobPtr), int32(len(blob)), int32(len(params)))
		if err != nil {
			return d.rt.execErr("flatsql_query_raw_flatbuffer_stream", err)
		}
		if wasmrt.ToInt32(res[0]) == 0 {
			return d.rt.engineErrVia(inv, "query_raw_flatbuffer_stream")
		}

		ptrRes, err := inv.Execute("flatsql_response_artifact_data")
		if err != nil {
			return err
		}
		sizeRes, err := inv.Execute("flatsql_response_artifact_size")
		if err != nil {
			return err
		}
		rowsRes, err := inv.Execute("flatsql_response_artifact_row_count")
		if err != nil {
			return err
		}
		colsRes, err := inv.Execute("flatsql_response_artifact_column_count")
		if err != nil {
			return err
		}
		hitRes, err := inv.Execute("flatsql_response_artifact_cache_hit")
		if err != nil {
			return err
		}

		size := uint32(wasmrt.ToInt32(sizeRes[0]))
		var out []byte
		if size > 0 {
			out, err = inv.ReadMemory(toUint32(ptrRes[0]), size)
			if err != nil {
				return err
			}
		} else {
			out = []byte{}
		}
		stream = &RawStream{
			Bytes:      out,
			Rows:       int(toFloat64(rowsRes[0])),
			Columns:    int(toFloat64(colsRes[0])),
			CacheHit:   wasmrt.ToInt32(hitRes[0]) != 0,
			FNV1a64:    FNV1a64WordFolded(out),
			FrameCount: countStreamFrames(out),
		}

		// Mirror only when the generation is unchanged across the execution: a
		// mutating raw-stream statement (or any concurrent invalidation) bumps
		// it, and such results must never be replayed.
		if mirrorKey != "" {
			if postGen, err := d.queryCacheGenerationLocked(inv); err == nil && postGen == preGen {
				if d.rawMirror == nil {
					d.rawMirror = newRawStreamMirror()
				}
				d.rawMirror.put(mirrorKey, stream)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return stream, nil
}

// queryCacheGenerationLocked reads the engine's query-cache generation
// counter (bumped by every mutator). Caller holds the module lock.
func (d *Database) queryCacheGenerationLocked(inv wasmrt.GuestCaller) (uint64, error) {
	res, err := inv.Execute("flatsql_query_cache_generation", int32(d.handle))
	if err != nil {
		return 0, d.rt.execErr("flatsql_query_cache_generation", err)
	}
	return uint64(toFloat64(res[0])), nil
}

// ExportData snapshots the raw arena stream (the durability primitive).
func (d *Database) ExportData() ([]byte, error) {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	ptrRes, err := d.rt.mod.Execute("flatsql_export_data", int32(d.handle))
	if err != nil {
		return nil, d.rt.execErr("flatsql_export_data", err)
	}
	sizeRes, err := d.rt.mod.Execute("flatsql_export_size")
	if err != nil {
		return nil, err
	}
	size := uint32(wasmrt.ToInt32(sizeRes[0]))
	if size == 0 {
		return []byte{}, nil
	}
	return d.rt.mod.ReadMemory(toUint32(ptrRes[0]), size)
}

// LoadAndRebuild restores a snapshot produced by ExportData, rebuilding
// indexes.
func (d *Database) LoadAndRebuild(data []byte) error {
	d.rt.mod.Lock()
	defer d.rt.mod.Unlock()

	dataPtr, err := d.rt.allocBytes(data)
	if err != nil {
		return err
	}
	if dataPtr != 0 {
		defer d.rt.free(dataPtr)
	}
	if _, err := d.rt.mod.Execute("flatsql_load_and_rebuild", int32(d.handle), int32(dataPtr), int32(len(data))); err != nil {
		return d.rt.execErr("flatsql_load_and_rebuild", err)
	}
	return nil
}

// DecodeSizePrefixedStream splits a [u32le length][bytes] frame stream into
// individual FlatBuffer payloads (host-side helper for tests and adapters).
func DecodeSizePrefixedStream(stream []byte) ([][]byte, error) {
	var frames [][]byte
	off := 0
	for off < len(stream) {
		if off+4 > len(stream) {
			return nil, fmt.Errorf("flatsqlrt: truncated frame header at offset %d", off)
		}
		n := int(binary.LittleEndian.Uint32(stream[off : off+4]))
		off += 4
		if off+n > len(stream) {
			return nil, fmt.Errorf("flatsqlrt: truncated frame payload at offset %d (want %d bytes)", off, n)
		}
		frames = append(frames, stream[off:off+n])
		off += n
	}
	return frames, nil
}

func toUint32(v interface{}) uint32 {
	switch val := v.(type) {
	case int32:
		return uint32(val)
	case uint32:
		return val
	case int64:
		return uint32(val)
	case uint64:
		return uint32(val)
	default:
		return 0
	}
}

func toFloat64(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case float32:
		return float64(val)
	case int32:
		return float64(val)
	case int64:
		return float64(val)
	case uint32:
		return float64(val)
	case uint64:
		return float64(val)
	default:
		return 0
	}
}
