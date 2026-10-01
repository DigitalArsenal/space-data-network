package flatsqlrt

// p4instance.go: the format-4 engine's one threaded instance (stack design
// docs/architecture/flatsql-sqlite-partitions.md §5.1; build-out contract
// §3.3, §3.4, §5.3), on the partition-store substrate (psinstance.go):
//
//   - the artifact AOT-compiled with THREADS and Interruptible, the only way
//     it loads, checked to run native; its own VM, executor and shared
//     memory, so it poisons only itself;
//   - the C host I/O module (hostio_native.c) in its "env";
//   - malloc and free are flatsql_p4_alloc and flatsql_p4_free;
//   - boot: _initialize -> flatsql_p4_init(config TLV) -> flatsql_p4_layout
//     (640 bytes, version 1) -> the doorbell over doorbell[0..nThreads) (each
//     thread's state word follows its doorbell) and the completion poller ->
//     flatsql_p4_start;
//   - stop: the stop word, notifies, flatsql_p4_stop(deadline ms), a wait
//     for the service threads, then an executor stop for any that remain; a
//     fenced (poisoned) instance cannot run flatsql_p4_stop, so it goes
//     straight to the executor stop and OnFailure runs at once.
//
// Format 4 has ONE instance for writers, read lanes and maintenance (the
// shared WAL index must live in one memory). The raw layout goes to
// storage/format4, which programs the mailbox against it; this file knows
// the boot and the control calls, nothing about records.

import (
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// The engine's layout block (flatsql_p4.h FlatsqlP4Layout).
const (
	P4LayoutBytes   = 640
	p4LayoutVersion = 1
	p4SlotHeader    = 448
)

// P4Config configures the instance.
type P4Config struct {
	Wasm          []byte // the portable artifact; it runs only AOT-compiled
	AOTCacheDir   string
	CompileOnMiss bool         // compile a missing artifact (tests, prewarm)
	Store         *NativeStore // the node's shared native store; StoreRoot opens a private one
	StoreRoot     string
	FDBudget      int    // this instance's share of RLIMIT_NOFILE (default 1024)
	InitConfig    []byte // flatsql_p4_init's config TLV
	MaxThreads    int    // guest threads (default 24)
	// ControlBudget bounds each control call (default 5 min: init replays
	// journals and recovers WALs, activation checkpoints every file).
	ControlBudget time.Duration
	// OnFailure runs once, on its own goroutine, when a service thread traps
	// or hangs, or a control call poisons the instance. It is fenced by then.
	OnFailure func(*P4Instance, error)
}

// P4CallError is a control export that returned a negative engine status.
type P4CallError struct {
	Export string
	Status int32
}

func (e *P4CallError) Error() string {
	return fmt.Sprintf("flatsqlrt: %s returned status %d", e.Export, e.Status)
}

// P4Instance is the running format-4 engine.
type P4Instance struct {
	ps *PSInstance
	mu sync.Mutex // one alloc-call-free sequence at a time (contract §3.10)
}

// OpenP4Instance creates, initializes and starts the instance. A negative
// init status comes back as a *P4CallError.
func OpenP4Instance(cfg P4Config) (*P4Instance, error) {
	if len(cfg.Wasm) == 0 {
		return nil, ErrNoP4Artifact
	}
	if cfg.FDBudget <= 0 {
		cfg.FDBudget = 1024
	}
	if cfg.ControlBudget <= 0 {
		cfg.ControlBudget = 5 * time.Minute
	}
	p4 := &P4Instance{}
	ps, err := OpenPSInstance(PSConfig{
		// One instance does everything; its I/O runs at the writer class.
		Role: PSRoleWriter, ABI: PSABIP4,
		Wasm: cfg.Wasm, AOTCacheDir: cfg.AOTCacheDir, AOTPrefix: P4ThreadsAOTPrefix, CompileOnMiss: cfg.CompileOnMiss,
		Store: cfg.Store, StoreRoot: cfg.StoreRoot, FDBudget: cfg.FDBudget, MaxThreads: cfg.MaxThreads,
		InitConfig: cfg.InitConfig, ControlBudget: cfg.ControlBudget,
		HeartbeatStale: -1, // the engine has no heartbeat words: a hang is a control-call budget or a trap
		OnFailure: func(_ *PSInstance, cause error) {
			if cfg.OnFailure != nil {
				cfg.OnFailure(p4, cause)
			}
		},
	})
	if err != nil {
		return nil, err
	}
	p4.ps = ps
	return p4, nil
}

// initP4 brings up the format-4 engine (PSABIP4).
func (p *PSInstance) initP4() error {
	if _, err := p.control("_initialize"); err != nil {
		return fmt.Errorf("flatsqlrt: p4 _initialize: %w", err)
	}
	mem, err := p.mod.SharedMemory()
	if err != nil {
		return err
	}
	p.mem = mem
	var ptr uint32
	if len(p.cfg.InitConfig) > 0 {
		if ptr, err = p.mod.Allocate(p.cfg.InitConfig); err != nil {
			return fmt.Errorf("flatsqlrt: p4 config: %w", err)
		}
	}
	v, err := p.control("flatsql_p4_init", int32(ptr), int32(len(p.cfg.InitConfig)))
	if ptr != 0 {
		p.mod.Deallocate(ptr)
	}
	if err != nil {
		return fmt.Errorf("flatsqlrt: flatsql_p4_init: %w", err)
	}
	if rc := wasmrt.ToInt32(v[0]); rc < 0 {
		return &P4CallError{Export: "flatsql_p4_init", Status: rc}
	}
	out, err := p.mod.AllocateSize(P4LayoutBytes)
	if err != nil {
		return err
	}
	v, err = p.control("flatsql_p4_layout", int32(out))
	if err != nil {
		p.mod.Deallocate(out)
		return fmt.Errorf("flatsqlrt: flatsql_p4_layout: %w", err)
	}
	raw := p.mem.ReadBytes(out, P4LayoutBytes)
	p.mod.Deallocate(out)
	if n := wasmrt.ToInt32(v[0]); n != P4LayoutBytes {
		return fmt.Errorf("flatsqlrt: flatsql_p4_layout wrote %d bytes, want %d (engine ABI changed)", n, P4LayoutBytes)
	}
	u := func(off int) uint32 { return binary.LittleEndian.Uint32(raw[off:]) }
	if u(0) != p4LayoutVersion || u(4) != P4LayoutBytes || u(8) != p4SlotHeader {
		return fmt.Errorf("flatsqlrt: p4 layout version %d size %d header %d, want %d/%d/%d (engine ABI changed)",
			u(0), u(4), u(8), p4LayoutVersion, P4LayoutBytes, p4SlotHeader)
	}
	// stopWord at 12; nThreads at 120; doorbell[64] at 384, each thread's
	// state word at doorbell+4.
	n := int(u(120))
	if n < 1 || n > 64 {
		return fmt.Errorf("flatsqlrt: p4 layout reports %d service threads", n)
	}
	for i := 0; i < n; i++ {
		p.doorbellAddrs = append(p.doorbellAddrs, u(384+4*i))
	}
	p.layout.StopWord = u(12)
	if err := p.mem.Check(p.layout.StopWord, 4); err != nil || p.layout.StopWord == 0 {
		return fmt.Errorf("flatsqlrt: p4 stop word %#x: %v", p.layout.StopWord, err)
	}
	p.engineLayout = raw
	p.layout.DoorbellCount = uint32(n)
	if p.doorbell, err = p.mod.StartDoorbellAt(p.mem, "flatsql_p4_wake", p.doorbellAddrs); err != nil {
		return err
	}
	p.poller = wasmrt.StartCompletionPoller(p.mem, p.cfg.PollInterval)
	v, err = p.control("flatsql_p4_start")
	if err == nil && wasmrt.ToInt32(v[0]) < 0 {
		err = &P4CallError{Export: "flatsql_p4_start", Status: wasmrt.ToInt32(v[0])}
	}
	if err != nil {
		p.doorbell.Close()
		p.poller.Close()
		return fmt.Errorf("flatsqlrt: flatsql_p4_start: %w", err)
	}
	p.started = int(wasmrt.ToInt32(v[0]))
	return nil
}

// EngineLayout returns the raw flatsql_p4_layout block.
func (p *P4Instance) EngineLayout() []byte { return p.ps.EngineLayout() }

// Memory returns the shared-memory accessor; bracket accesses with Enter/Exit.
func (p *P4Instance) Memory() *wasmrt.SharedMemory { return p.ps.Memory() }

// Enter registers a Go accessor of the shared memory; false once stopping.
func (p *P4Instance) Enter() bool { return p.ps.Enter() }

// Exit ends an access Enter began.
func (p *P4Instance) Exit() { p.ps.Exit() }

// Wake bumps service thread i's doorbell and notifies it.
func (p *P4Instance) Wake(i int) error { return p.ps.Wake(i) }

// Stopping is closed once a stop has begun.
func (p *P4Instance) Stopping() <-chan struct{} { return p.ps.Stopping() }

// Failure returns the error that fenced the instance, or nil.
func (p *P4Instance) Failure() error { return p.ps.Failure() }

// Fence fences the instance as a trap does (fault injection).
func (p *P4Instance) Fence(cause error) { p.ps.Fence(cause) }

// Started returns how many service threads flatsql_p4_start reported.
func (p *P4Instance) Started() int { return p.ps.Started() }

// AOTPath is the artifact the instance runs.
func (p *P4Instance) AOTPath() string { return p.ps.AOTPath() }

// Stats snapshots the substrate (threads, I/O, doorbell).
func (p *P4Instance) Stats() PSStats { return p.ps.Stats() }

// ControlCall calls a scalar control export and returns its i32 result.
func (p *P4Instance) ControlCall(export string, args ...interface{}) (int32, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, err := p.ps.Control(export, args...)
	if err != nil {
		return 0, err
	}
	if len(v) == 0 {
		return 0, nil
	}
	return wasmrt.ToInt32(v[0]), nil
}

// ControlBytes copies in into guest memory and calls export(ptr, len).
func (p *P4Instance) ControlBytes(export string, in []byte) (int32, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.ps.Enter() {
		return 0, ErrPSNotLive
	}
	defer p.ps.Exit()
	ptr, err := p.ps.mod.Allocate(in)
	if err != nil {
		return 0, err
	}
	defer p.ps.mod.Deallocate(ptr)
	v, err := p.ps.control(export, int32(ptr), int32(len(in)))
	if err != nil {
		return 0, err
	}
	return wasmrt.ToInt32(v[0]), nil
}

// ControlOut calls export(ptr, capacity) on a guest buffer and returns the
// result's first rc bytes (rc is the export's i32 result).
func (p *P4Instance) ControlOut(export string, capacity int) ([]byte, int32, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.ps.Enter() {
		return nil, 0, ErrPSNotLive
	}
	defer p.ps.Exit()
	ptr, err := p.ps.mod.AllocateSize(uint32(capacity))
	if err != nil {
		return nil, 0, err
	}
	defer p.ps.mod.Deallocate(ptr)
	v, err := p.ps.control(export, int32(ptr), int32(capacity))
	if err != nil {
		return nil, 0, err
	}
	rc := wasmrt.ToInt32(v[0])
	if rc <= 0 {
		return nil, rc, nil
	}
	if int(rc) > capacity {
		return nil, rc, fmt.Errorf("flatsqlrt: %s wrote %d bytes into %d", export, rc, capacity)
	}
	return p.ps.mem.ReadBytes(ptr, int(rc)), rc, nil
}

// StopWithin stops the instance with deadline as its cooperative bound and
// returns flatsql_p4_stop's status (P4_OK, or P4_E_BUSY when it did not drain
// in time). A second stop returns the first one's answer.
func (p *P4Instance) StopWithin(deadline time.Duration) (int32, error) {
	err := p.ps.StopWithin(deadline)
	return p.ps.stopStatus.Load(), err
}
