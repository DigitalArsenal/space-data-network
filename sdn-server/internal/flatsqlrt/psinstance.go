package flatsqlrt

// psinstance.go: one threaded partition-store instance on WasmEdge (design
// §5.1, §5.4, §15, A21, A22, A24, A29, A30). It is the substrate T6 builds the
// writer, reader and bulk instances on; it knows nothing about records.
//
// What an instance is:
//   - the ps artifact, AOT-compiled with THREADS and Interruptible (the only
//     way it loads: A30, no interpreter fallback), checked to really run
//     native;
//   - its own VM, executor and shared memory: its own poison domain;
//   - the C host I/O module in its "env" (no Go HostIO fallback: A30), with
//     its own fd budget;
//   - service threads the guest spawns from flatsql_ps_start, exempt from
//     every per-call budget and stopped only through the stop word, then an
//     executor stop (A21);
//   - one doorbell thread and one completion poller (A24, A29), and a
//     heartbeat watchdog (A21).
//
// SUBSTRATE LAYOUT v1. flatsql_ps_layout(out) writes 128 bytes, all u32 LE:
//
//	 0 magic 'PSL1' (0x314C5350)   4 version (1)          8 size (128)       12 flags
//	16 stop_word addr             20 heartbeat_base      24 heartbeat_count
//	28 doorbell_base ({seq u32, sleeping u32} per writer)  32 doorbell_count
//	36 ack_base (u64 per ack word, monotonic)              40 ack_count
//	44 mem_gen addr (u32, bumped after memory.grow)        48 canary_base (u32 per thread)
//	52 live_threads addr (u32)    56..127 reserved (0)
//
// Guest obligations: every service-thread loop increments its heartbeat and
// checks the stop word (and, in long vtab/merge loops, every 4K entries); a
// thread stores 0xFFFFFFFF in its heartbeat when it leaves; an idle writer
// sets `sleeping`, re-reads `seq`, and waits on `seq` with the value it read;
// flatsql_ps_wake(addr, n) is memory.atomic.notify(addr, n). Every address is
// naturally aligned and below the memory's initial size.
//
// THE ENGINE ABI (PSABIEngine, T6). The FlatSQL engine (flatsql-ps-threads.wasm,
// psartifact.go) publishes its own layouts instead of layout v1:
// flatsql_ps_layout (a writer: ring descriptor offsets, the slab pool, one
// {seq u32, sleeping u32} doorbell pair per writer, each in its own object) and
// flatsql_ps_reader_layout (a reader: the mailbox, one {doorbell u32, state
// u32} pair per lane, the stop word). Its buffers come from flatsql_ps_alloc /
// flatsql_ps_free, and it has no heartbeat words: a writer is stopped by
// flatsql_ps_stop alone, a reader by its stop word and flatsql_ps_stop. The
// raw layout bytes are handed to the router (storage/format2), which programs
// rings and mailboxes against them.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

const (
	psLayoutMagic   = 0x314C5350
	psLayoutVersion = 1
	psLayoutBytes   = 128

	// DefaultPSAOTPrefix names the engine's threaded AOT artifacts. It must not
	// start with "flatsql-": the legacy engine prunes that prefix.
	DefaultPSAOTPrefix = "fsqlps"
)

// PSRole is the instance's role (flatsql_ps_init's first argument).
type PSRole int32

const (
	PSRoleWriter PSRole = 1
	PSRoleReader PSRole = 2 // the engine's interactive reader
	PSRoleBulk   PSRole = 3
	// PSRoleSandbox is the engine's capped lane for untrusted SQL (A28).
	PSRoleSandbox PSRole = 4
)

// PSABI is the guest contract an instance programs against.
type PSABI int

const (
	// PSABIProbe is substrate layout v1 (the T5 probe, testdata/ps-probe.wasm).
	PSABIProbe PSABI = iota
	// PSABIEngine is the FlatSQL engine's own ABI (see the note above).
	PSABIEngine
	// PSABIP4 is the format-4 engine's ABI (p4instance.go): one instance,
	// flatsql_p4_init(config), a 640-byte layout, a doorbell per thread.
	PSABIP4
)

// psExports names the guest exports an ABI's lifecycle calls outside init.
type psExports struct {
	malloc, free string // "" = wasmrt's malloc/free
	stop         string // the guest's bounded stop, called with the deadline in ms
}

func (a PSABI) exports() psExports {
	switch a {
	case PSABIEngine:
		return psExports{malloc: "flatsql_ps_alloc", free: "flatsql_ps_free", stop: "flatsql_ps_stop"}
	case PSABIP4:
		return psExports{malloc: "flatsql_p4_alloc", free: "flatsql_p4_free", stop: "flatsql_p4_stop"}
	}
	return psExports{stop: "flatsql_ps_stop"}
}

// Engine layout sizes (flatsql_ps.h: FlatsqlPsLayout, FlatsqlPsReaderLayout).
const (
	PSEngineLayoutBytes       = 624
	PSEngineReaderLayoutBytes = 932
)

func (r PSRole) ioClass() HostIOClass {
	switch r {
	case PSRoleWriter:
		return HostIOWriter
	case PSRoleBulk:
		return HostIOBulk
	default:
		return HostIOReader
	}
}

// PSLayout is the substrate layout v1 the guest publishes.
type PSLayout struct {
	StopWord       uint32
	HeartbeatBase  uint32
	HeartbeatCount uint32
	DoorbellBase   uint32
	DoorbellCount  uint32
	AckBase        uint32
	AckCount       uint32
	MemGen         uint32
	CanaryBase     uint32
	LiveThreads    uint32
}

// ParsePSLayout decodes and bounds-checks a layout block.
func ParsePSLayout(b []byte) (PSLayout, error) {
	if len(b) < psLayoutBytes {
		return PSLayout{}, fmt.Errorf("flatsqlrt: ps layout is %d bytes, want %d", len(b), psLayoutBytes)
	}
	u := func(off int) uint32 { return binary.LittleEndian.Uint32(b[off:]) }
	if u(0) != psLayoutMagic || u(4) != psLayoutVersion || u(8) != psLayoutBytes {
		return PSLayout{}, fmt.Errorf("flatsqlrt: ps layout header %#x v%d size %d", u(0), u(4), u(8))
	}
	l := PSLayout{StopWord: u(16), HeartbeatBase: u(20), HeartbeatCount: u(24), DoorbellBase: u(28),
		DoorbellCount: u(32), AckBase: u(36), AckCount: u(40), MemGen: u(44), CanaryBase: u(48), LiveThreads: u(52)}
	if l.HeartbeatCount > 1024 || l.DoorbellCount > 1024 || l.AckCount > 1<<20 {
		return PSLayout{}, errors.New("flatsqlrt: ps layout counts out of range")
	}
	return l, nil
}

func (l PSLayout) check(mem *wasmrt.SharedMemory) error {
	words := []struct {
		off   uint32
		width uint64
	}{{l.StopWord, 4}, {l.MemGen, 4}, {l.LiveThreads, 4}}
	for i := uint32(0); i < l.HeartbeatCount; i++ {
		words = append(words, struct {
			off   uint32
			width uint64
		}{l.HeartbeatBase + 4*i, 4})
	}
	for i := uint32(0); i < l.DoorbellCount; i++ {
		words = append(words, struct {
			off   uint32
			width uint64
		}{l.DoorbellBase + 8*i, 8})
	}
	for i := uint32(0); i < l.AckCount; i++ {
		words = append(words, struct {
			off   uint32
			width uint64
		}{l.AckBase + 8*i, 8})
	}
	for _, w := range words {
		if err := mem.Check(w.off, w.width); err != nil {
			return err
		}
	}
	return nil
}

// PSConfig configures one instance.
type PSConfig struct {
	Role PSRole
	// ABI selects the guest contract (default PSABIProbe).
	ABI PSABI
	// Wasm is the portable ps artifact; it runs only AOT-compiled.
	Wasm          []byte
	AOTCacheDir   string
	AOTPrefix     string // default DefaultPSAOTPrefix
	CompileOnMiss bool   // compile a missing artifact (tests, prewarm)
	// Store is the node's shared native store; StoreRoot opens a private one.
	Store      *NativeStore
	StoreRoot  string
	FDBudget   int // this instance's share of RLIMIT_NOFILE (default 512)
	MaxHandles int // virtual handles (default 16384)
	// MaxThreads caps guest threads (default 24: 8 spare of the 32 the
	// design reserves; §5.1).
	MaxThreads     int
	MaxMemoryPages uint32 // default 32768 (2 GiB, ruling 9)
	InitConfig     []byte // passed to flatsql_ps_init
	// ControlBudget bounds each control call (A30: an explicit budget, never
	// a request context). Default 10 s.
	ControlBudget time.Duration
	// HeartbeatStale declares a service thread hung (A21, default 10 s; a
	// negative value disables the watchdog).
	HeartbeatStale time.Duration
	PollInterval   time.Duration // completion poller (default 250 us)
	// StopDeadline bounds a stop's cooperative phase (default 2 s).
	StopDeadline time.Duration
	// OnFailure runs once, on its own goroutine, when a service thread traps
	// or hangs. The instance is already fenced (revoked) when it runs.
	OnFailure func(*PSInstance, error)
}

var (
	// ErrPSSubstrate: format 2 cannot start on this substrate (A30).
	ErrPSSubstrate = errors.New("flatsqlrt: partition-store substrate unavailable")
	// ErrPSNotLive: the instance is stopping or stopped.
	ErrPSNotLive = errors.New("flatsqlrt: partition-store instance is not live")
	// ErrPSRetained: the instance did not stop; its VM is retained.
	ErrPSRetained = errors.New("flatsqlrt: partition-store instance did not stop; retained")
	// ErrPSRestartRequired: more instances failed to stop than the process
	// may retain (A21); the supervisor must restart the process.
	ErrPSRestartRequired = errors.New("flatsqlrt: too many partition-store instances failed to stop; restart the process")
)

// PSRetentionLimit is how many instances that failed to stop one process
// keeps before it demands a restart (A21: one on a host-02-class box).
var PSRetentionLimit int32 = 1

var psRetained atomic.Int32

// PSRetainedInstances reports how many instances this process retains.
func PSRetainedInstances() int32 { return psRetained.Load() }

const (
	psLive int32 = iota
	psDraining
	psDead
)

// PSInstance is one running partition-store instance.
type PSInstance struct {
	cfg      PSConfig
	mod      *wasmrt.Module
	io       *NativeHostIO
	store    *NativeStore
	ownStore bool
	mem      *wasmrt.SharedMemory
	layout   PSLayout
	aotPath  string
	started  int
	// engineLayout is the raw flatsql_ps_layout / flatsql_ps_reader_layout
	// block (PSABIEngine); doorbellAddrs are the words the doorbell rings.
	engineLayout  []byte
	doorbellAddrs []uint32

	doorbell *wasmrt.Doorbell
	poller   *wasmrt.CompletionPoller
	watchdog *wasmrt.Watchdog

	state     atomic.Int32
	ready     atomic.Bool // init finished; failures now fence and stop
	accessors atomic.Int64
	ctlMu     sync.Mutex

	failOnce sync.Once
	failure  atomic.Value // error
	fencedIn atomic.Int64 // ns from failure detection to revoke done
	fencedAt atomic.Int64 // unix ns when the revoke finished
	stopOnce sync.Once
	stopErr  error
	// stopStatus is the guest stop export's result (format 4: P4_OK, or
	// P4_E_BUSY when it did not drain within the deadline).
	stopStatus atomic.Int32
	stopped    chan struct{}
	superDone  chan struct{}
}

var nofileOnce sync.Once

// processNoFile raises RLIMIT_NOFILE's soft limit toward 65536 once per
// process (design §5.4) and reports the limits in force. Go's runtime has
// already lifted the soft limit to the hard limit at start-up (darwin: to
// kern.maxfilesperproc); the raise covers a limit lowered since. Both are 0
// where the limit cannot be read.
func processNoFile() (soft, hard uint64) {
	nofileOnce.Do(func() { _, _, _ = RaiseNoFile(65536) })
	soft, hard, _ = RaiseNoFile(0) // want 0 only reads
	return soft, hard
}

// psDefaultMaxHandles is an instance's virtual handle table (PSConfig.MaxHandles).
const psDefaultMaxHandles = 16384

// OpenPSInstance creates, initializes and starts an instance.
func OpenPSInstance(cfg PSConfig) (*PSInstance, error) {
	if !NativeHostIOSupported() {
		return nil, fmt.Errorf("%w: %v", ErrPSSubstrate, ErrNativeHostIOUnavailable)
	}
	applyPSDefaults(&cfg)
	processNoFile()
	// A31: the start-up self-test is a metric, logged once per process.
	_ = wasmrt.SubstrateStatus()

	aot, aotPath, err := EnsureThreadedAOTArtifact(cfg.AOTCacheDir, cfg.AOTPrefix, cfg.Wasm, cfg.CompileOnMiss)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPSSubstrate, err)
	}
	if err := verifyNativeExecution(aot, cfg.MaxMemoryPages); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPSSubstrate, err)
	}

	p := &PSInstance{cfg: cfg, aotPath: aotPath, stopped: make(chan struct{}), superDone: make(chan struct{})}
	p.store = cfg.Store
	if p.store == nil {
		if p.store, err = OpenNativeStore(cfg.StoreRoot); err != nil {
			return nil, err
		}
		p.ownStore = true
	}
	cleanup := func() {
		if p.mod != nil {
			p.mod.Release()
		}
		if p.io != nil {
			p.io.Revoke()
			p.io.ReleaseParked()
			p.io.Reap()
			_ = p.io.Free()
		}
		if p.ownStore {
			p.store.Release()
		}
	}
	if p.io, err = NewNativeHostIO(p.store, cfg.Role.ioClass(), cfg.FDBudget, cfg.MaxHandles); err != nil {
		cleanup()
		return nil, fmt.Errorf("%w: %v", ErrPSSubstrate, err)
	}
	opts := []wasmrt.Option{
		wasmrt.WithWASI(),
		wasmrt.WithMaxMemoryPages(cfg.MaxMemoryPages),
		wasmrt.WithMaxThreads(cfg.MaxThreads),
		wasmrt.WithServiceThreads(),
		wasmrt.WithEnvInstaller(p.io.Install),
		wasmrt.WithExecTimeout(cfg.ControlBudget),
	}
	if ex := cfg.ABI.exports(); ex.malloc != "" {
		opts = append(opts, wasmrt.WithMallocName(ex.malloc), wasmrt.WithFreeName(ex.free))
	}
	p.mod, err = wasmrt.NewModule(aot, opts...)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("%w: %v", ErrPSSubstrate, err)
	}
	if err := p.init(); err != nil {
		if p.doorbell != nil {
			p.doorbell.Close()
		}
		if p.poller != nil {
			p.poller.Close()
		}
		if p.watchdog != nil {
			p.watchdog.Stop()
		}
		if p.mod.InterruptThreads(time.Now().Add(2*time.Second)) > 0 {
			return nil, fmt.Errorf("%w (and its threads did not stop; retained)", err)
		}
		cleanup()
		return nil, err
	}
	p.ready.Store(true)
	go p.supervise()
	return p, nil
}

func applyPSDefaults(cfg *PSConfig) {
	if cfg.AOTPrefix == "" {
		cfg.AOTPrefix = DefaultPSAOTPrefix
	}
	if cfg.FDBudget <= 0 {
		cfg.FDBudget = 512
	}
	if cfg.MaxHandles <= 0 {
		cfg.MaxHandles = psDefaultMaxHandles
	}
	if cfg.MaxThreads <= 0 {
		cfg.MaxThreads = 24
	}
	if cfg.MaxMemoryPages == 0 {
		cfg.MaxMemoryPages = 32768
	}
	if cfg.ControlBudget <= 0 {
		cfg.ControlBudget = 10 * time.Second
	}
	if cfg.HeartbeatStale == 0 {
		cfg.HeartbeatStale = 10 * time.Second
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 250 * time.Microsecond
	}
	if cfg.StopDeadline <= 0 {
		cfg.StopDeadline = 2 * time.Second
	}
}

// verifyNativeExecution proves the loader used the AOT section: a VM with the
// interpreter's instruction counter on runs the reactor's _initialize, and
// native code (compiled without counting) never advances the counter.
func verifyNativeExecution(aot []byte, maxPages uint32) error {
	m, err := wasmrt.NewModule(aot,
		wasmrt.WithWASI(),
		wasmrt.WithHostModule(HostIOModule, refusingHostFuncs()),
		wasmrt.WithMaxMemoryPages(maxPages),
		wasmrt.WithMaxThreads(1),
		wasmrt.WithInstructionCounting(),
		wasmrt.WithExecTimeout(10*time.Second),
	)
	if err != nil {
		return err
	}
	defer m.Release()
	if !m.HasFunction("_initialize") {
		return errors.New("ps artifact exports no _initialize")
	}
	// Instantiation evaluates constant expressions (global and segment
	// offsets) in the interpreter even for AOT code, so count the call alone.
	before := m.InstructionCount()
	if _, err := m.Execute("_initialize"); err != nil {
		return fmt.Errorf("ps artifact _initialize: %w", err)
	}
	if n := m.InstructionCount() - before; n != 0 {
		return fmt.Errorf("ps artifact ran %d interpreted instructions; its AOT section was not loaded", n)
	}
	return nil
}

func (p *PSInstance) control(name string, params ...interface{}) ([]interface{}, error) {
	p.ctlMu.Lock()
	defer p.ctlMu.Unlock()
	ctx := wasmrt.WithExecBudget(context.Background(), wasmrt.ExecBudget{Timeout: p.cfg.ControlBudget})
	v, err := p.mod.ExecuteContext(ctx, name, params...)
	if err != nil && p.mod.Poisoned() && p.ready.Load() && p.state.Load() == psLive {
		// A control call that trapped or outran its budget has already
		// stopped every invocation on the executor: the instance is gone.
		p.fail(fmt.Errorf("control call %s: %w", name, err))
	}
	return v, err
}

func (p *PSInstance) init() error {
	switch p.cfg.ABI {
	case PSABIEngine:
		return p.initEngine()
	case PSABIP4:
		return p.initP4()
	}
	if _, err := p.control("_initialize"); err != nil {
		return fmt.Errorf("flatsqlrt: ps _initialize: %w", err)
	}
	mem, err := p.mod.SharedMemory()
	if err != nil {
		return err
	}
	p.mem = mem
	cfgBytes := p.cfg.InitConfig
	if len(cfgBytes) == 0 {
		cfgBytes = []byte{0}
	}
	ptr, err := p.mod.Allocate(cfgBytes)
	if err != nil {
		return fmt.Errorf("flatsqlrt: ps config: %w", err)
	}
	v, err := p.control("flatsql_ps_init", int32(p.cfg.Role), int32(ptr), int32(len(p.cfg.InitConfig)))
	p.mod.Deallocate(ptr)
	if err != nil {
		return fmt.Errorf("flatsqlrt: flatsql_ps_init: %w", err)
	}
	if rc := wasmrt.ToInt32(v[0]); rc != 0 {
		return fmt.Errorf("flatsqlrt: flatsql_ps_init returned %d", rc)
	}
	out, err := p.mod.AllocateSize(psLayoutBytes)
	if err != nil {
		return err
	}
	v, err = p.control("flatsql_ps_layout", int32(out))
	if err != nil {
		p.mod.Deallocate(out)
		return fmt.Errorf("flatsqlrt: flatsql_ps_layout: %w", err)
	}
	raw := p.mem.ReadBytes(out, psLayoutBytes)
	p.mod.Deallocate(out)
	if n := wasmrt.ToInt32(v[0]); n < psLayoutBytes {
		return fmt.Errorf("flatsqlrt: flatsql_ps_layout wrote %d bytes", n)
	}
	if p.layout, err = ParsePSLayout(raw); err != nil {
		return err
	}
	if err := p.layout.check(p.mem); err != nil {
		return err
	}
	if p.doorbell, err = p.mod.StartDoorbell(p.mem, "flatsql_ps_wake", p.layout.DoorbellBase, int(p.layout.DoorbellCount)); err != nil {
		return err
	}
	p.poller = wasmrt.StartCompletionPoller(p.mem, p.cfg.PollInterval)
	v, err = p.control("flatsql_ps_start")
	if err != nil {
		p.doorbell.Close()
		p.poller.Close()
		return fmt.Errorf("flatsqlrt: flatsql_ps_start: %w", err)
	}
	p.started = int(wasmrt.ToInt32(v[0]))
	if p.cfg.HeartbeatStale > 0 {
		p.watchdog, err = wasmrt.StartWatchdog(p.mem, p.layout.HeartbeatBase, int(p.layout.HeartbeatCount), p.cfg.HeartbeatStale,
			func(thread int, idle time.Duration) {
				p.fail(fmt.Errorf("hung service thread %d: heartbeat still for %s", thread, idle.Round(time.Millisecond)))
			})
		if err != nil {
			return err
		}
	}
	return nil
}

// initEngine brings up a FlatSQL engine instance (PSABIEngine): init with the
// config TLVs, read the role's layout, start the doorbell over its words and
// the completion poller, then start the service threads.
func (p *PSInstance) initEngine() error {
	if _, err := p.control("_initialize"); err != nil {
		return fmt.Errorf("flatsqlrt: ps _initialize: %w", err)
	}
	mem, err := p.mod.SharedMemory()
	if err != nil {
		return err
	}
	p.mem = mem
	var ptr uint32
	if len(p.cfg.InitConfig) > 0 {
		if ptr, err = p.mod.Allocate(p.cfg.InitConfig); err != nil {
			return fmt.Errorf("flatsqlrt: ps config: %w", err)
		}
	}
	v, err := p.control("flatsql_ps_init", int32(p.cfg.Role), int32(ptr), int32(len(p.cfg.InitConfig)))
	if ptr != 0 {
		p.mod.Deallocate(ptr)
	}
	if err != nil {
		return fmt.Errorf("flatsqlrt: flatsql_ps_init: %w", err)
	}
	if rc := wasmrt.ToInt32(v[0]); rc < 0 {
		return fmt.Errorf("flatsqlrt: flatsql_ps_init (role %d) returned %d", p.cfg.Role, rc)
	}
	export, want := "flatsql_ps_layout", PSEngineLayoutBytes
	if p.cfg.Role != PSRoleWriter {
		export, want = "flatsql_ps_reader_layout", PSEngineReaderLayoutBytes
	}
	out, err := p.mod.AllocateSize(uint32(want))
	if err != nil {
		return err
	}
	v, err = p.control(export, int32(out))
	if err != nil {
		p.mod.Deallocate(out)
		return fmt.Errorf("flatsqlrt: %s: %w", export, err)
	}
	if n := int(wasmrt.ToInt32(v[0])); n != want {
		p.mod.Deallocate(out)
		return fmt.Errorf("flatsqlrt: %s wrote %d bytes, want %d (engine ABI changed)", export, n, want)
	}
	raw := p.mem.ReadBytes(out, want)
	p.mod.Deallocate(out)
	if binary.LittleEndian.Uint32(raw) != 1 {
		return fmt.Errorf("flatsqlrt: %s version %d, want 1", export, binary.LittleEndian.Uint32(raw))
	}
	p.engineLayout = raw
	u := func(off int) uint32 { return binary.LittleEndian.Uint32(raw[off:]) }
	if p.cfg.Role == PSRoleWriter {
		// FlatsqlPsLayout: nWriters at 4; writerSeq[64] at 112, writerSleeping[64] at 368.
		n := int(u(4))
		if n < 1 || n > 64 {
			return fmt.Errorf("flatsqlrt: engine reports %d writers", n)
		}
		for i := 0; i < n; i++ {
			seq, sleeping := u(112+4*i), u(368+4*i)
			if sleeping != seq+4 {
				return fmt.Errorf("flatsqlrt: writer %d doorbell words %#x/%#x are not a pair", i, seq, sleeping)
			}
			p.doorbellAddrs = append(p.doorbellAddrs, seq)
		}
	} else {
		// FlatsqlPsReaderLayout: nLanes at 4; stopWord at 160; laneDoorbell[64]
		// at 164, laneState[64] at 420 (each lane's state follows its doorbell).
		n := int(u(4))
		if n < 1 || n > 64 {
			return fmt.Errorf("flatsqlrt: engine reports %d lanes", n)
		}
		for i := 0; i < n; i++ {
			db, st := u(164+4*i), u(420+4*i)
			if st != db+4 {
				return fmt.Errorf("flatsqlrt: lane %d doorbell words %#x/%#x are not a pair", i, db, st)
			}
			p.doorbellAddrs = append(p.doorbellAddrs, db)
		}
		p.layout.StopWord = u(160)
		if err := p.mem.Check(p.layout.StopWord, 4); err != nil {
			return err
		}
	}
	p.layout.DoorbellCount = uint32(len(p.doorbellAddrs))
	if p.doorbell, err = p.mod.StartDoorbellAt(p.mem, "flatsql_ps_wake", p.doorbellAddrs); err != nil {
		return err
	}
	p.poller = wasmrt.StartCompletionPoller(p.mem, p.cfg.PollInterval)
	v, err = p.control("flatsql_ps_start")
	if err != nil {
		p.doorbell.Close()
		p.poller.Close()
		return fmt.Errorf("flatsqlrt: flatsql_ps_start: %w", err)
	}
	if rc := wasmrt.ToInt32(v[0]); rc < 0 {
		p.doorbell.Close()
		p.poller.Close()
		return fmt.Errorf("flatsqlrt: flatsql_ps_start returned %d", rc)
	} else {
		p.started = int(rc)
	}
	return nil
}

// EngineLayout returns the engine's raw layout block (PSABIEngine): the
// writer's FlatsqlPsLayout or a reader's FlatsqlPsReaderLayout.
func (p *PSInstance) EngineLayout() []byte { return p.engineLayout }

// Memory returns the instance's shared-memory accessor. Callers bracket every
// access with Enter/Exit (A22) so the memory is never released under them.
func (p *PSInstance) Memory() *wasmrt.SharedMemory { return p.mem }

// Enter registers a Go accessor of the instance's shared memory (A22); it
// returns false once the instance is stopping. Every true is paired with Exit.
func (p *PSInstance) Enter() bool { return p.enter() }

// Exit ends an access Enter began.
func (p *PSInstance) Exit() { p.exit() }

// Wake advances doorbell i and always asks the doorbell thread for a notify
// (the engine's lanes: the caller picks an idle lane from its state word).
func (p *PSInstance) Wake(i int) error {
	if !p.enter() {
		return ErrPSNotLive
	}
	defer p.exit()
	p.doorbell.Wake(i)
	return nil
}

// WaitWord waits until the u64 at addr is at least target (a ring's
// ackedRseq, any monotonic engine word), through the completion poller.
func (p *PSInstance) WaitWord(ctx context.Context, addr uint32, target uint64) error {
	if !p.enter() {
		return ErrPSNotLive
	}
	p.exit()
	err := p.poller.Wait(ctx, addr, target)
	if errors.Is(err, wasmrt.ErrClosed) {
		return ErrPSNotLive
	}
	return err
}

// Stopping is closed once Stop has begun (callers waiting on instance words
// select on it).
func (p *PSInstance) Stopping() <-chan struct{} { return p.stopped }

// supervise turns a service-thread trap into a fenced failure (§15).
func (p *PSInstance) supervise() {
	defer close(p.superDone)
	select {
	case <-p.mod.Failure():
		p.fail(fmt.Errorf("service thread trapped: %v", p.mod.PoisonCause()))
	case <-p.stopped:
	}
}

// fail fences the instance once: revoke its host I/O first (so it cannot
// write another byte), then stop it on another goroutine and tell the owner.
func (p *PSInstance) fail(cause error) {
	if !p.ready.Load() {
		return // OpenPSInstance is still initializing and unwinds its own failure
	}
	p.failOnce.Do(func() {
		start := time.Now()
		p.failure.Store(cause)
		p.mod.MarkPoisoned(cause)
		p.io.Revoke()
		p.fencedIn.Store(int64(time.Since(start)))
		p.fencedAt.Store(time.Now().UnixNano())
		fmt.Fprintf(os.Stderr, "[flatsqlrt] ERROR partition-store instance (role %d) fenced: %v\n", p.cfg.Role, cause)
		go func() {
			_ = p.Stop()
			if p.cfg.OnFailure != nil {
				p.cfg.OnFailure(p, cause)
			}
		}()
	})
}

// Fence fences the instance as a service-thread trap does (§15): its host
// I/O is revoked, it stops, and OnFailure runs. For fault injection.
func (p *PSInstance) Fence(cause error) { p.fail(cause) }

// Failure returns the error that fenced the instance, or nil.
func (p *PSInstance) Failure() error {
	if v, ok := p.failure.Load().(error); ok {
		return v
	}
	return nil
}

// FenceLatency is the time from failure detection to revoke completion.
func (p *PSInstance) FenceLatency() time.Duration { return time.Duration(p.fencedIn.Load()) }

// FencedAt is when a failure's revoke completed (zero if never fenced).
func (p *PSInstance) FencedAt() time.Time {
	if ns := p.fencedAt.Load(); ns != 0 {
		return time.Unix(0, ns)
	}
	return time.Time{}
}

func (p *PSInstance) enter() bool {
	p.accessors.Add(1)
	if p.state.Load() != psLive {
		p.accessors.Add(-1)
		return false
	}
	return true
}

func (p *PSInstance) exit() { p.accessors.Add(-1) }

// Ring advances writer i's doorbell (A24). Never blocks.
func (p *PSInstance) Ring(i int) error {
	if !p.enter() {
		return ErrPSNotLive
	}
	defer p.exit()
	p.doorbell.Ring(i)
	return nil
}

// WaitAck waits until ack word i reaches target.
func (p *PSInstance) WaitAck(ctx context.Context, i int, target uint64) error {
	if i < 0 || uint32(i) >= p.layout.AckCount {
		return fmt.Errorf("flatsqlrt: ack word %d out of range", i)
	}
	if !p.enter() {
		return ErrPSNotLive
	}
	off := p.layout.AckBase + uint32(8*i)
	p.exit()
	err := p.poller.Wait(ctx, off, target)
	if errors.Is(err, wasmrt.ErrClosed) {
		return ErrPSNotLive
	}
	return err
}

// Load32 reads a guest word (diagnostics and tests); ok=false once stopping.
func (p *PSInstance) Load32(addr uint32) (uint32, bool) {
	if !p.enter() {
		return 0, false
	}
	defer p.exit()
	return p.mem.Load32(addr), true
}

// Store32 writes a guest word (tests: fault injection); false once stopping.
func (p *PSInstance) Store32(addr, v uint32) bool {
	if !p.enter() {
		return false
	}
	defer p.exit()
	p.mem.Store32(addr, v)
	return true
}

// Layout returns the guest's substrate layout.
func (p *PSInstance) Layout() PSLayout { return p.layout }

// Started returns how many service threads flatsql_ps_start reported.
func (p *PSInstance) Started() int { return p.started }

// Module exposes the wasm module (tests and T6 wiring).
func (p *PSInstance) Module() *wasmrt.Module { return p.mod }

// HostIO exposes the instance's native host I/O.
func (p *PSInstance) HostIO() *NativeHostIO { return p.io }

// AOTPath is the artifact the instance runs.
func (p *PSInstance) AOTPath() string { return p.aotPath }

// Control invokes a guest export on the instance under its control budget.
// T6 uses it for the engine's control ABI; tests for probes.
func (p *PSInstance) Control(name string, params ...interface{}) ([]interface{}, error) {
	if !p.enter() {
		return nil, ErrPSNotLive
	}
	defer p.exit()
	return p.control(name, params...)
}

// PSStats is a snapshot of an instance.
type PSStats struct {
	State        string
	Poisoned     bool
	Threads      wasmrt.ThreadStats
	IO           NativeHostIOStats
	Doorbell     wasmrt.DoorbellStats
	PollerSweeps int64
}

// Stats snapshots the instance.
func (p *PSInstance) Stats() PSStats {
	states := []string{"live", "draining", "dead"}
	st := PSStats{State: states[p.state.Load()], Poisoned: p.mod.Poisoned(), Threads: p.mod.ThreadStats(), IO: p.io.Stats()}
	if p.doorbell != nil {
		st.Doorbell = p.doorbell.Stats()
	}
	if p.poller != nil {
		st.PollerSweeps = p.poller.Sweeps()
	}
	return st
}

// Stop stops the instance: the stop word and wakes, the guest's own bounded
// flatsql_ps_stop, a wait for the service threads, then an executor stop
// for any that remain (A21). The host I/O is revoked (a failed instance was
// revoked first), the doorbell and poller close, Go accessors drain (A22),
// and the VM is released. An instance whose threads outlive all of that is
// retained, never freed beneath them: Stop then returns ErrPSRetained, or
// ErrPSRestartRequired past PSRetentionLimit.
func (p *PSInstance) Stop() error { return p.StopWithin(p.cfg.StopDeadline) }

// StopWithin is Stop with its cooperative phase bounded by deadline. The
// first stop decides; a later one returns its answer.
func (p *PSInstance) StopWithin(deadline time.Duration) error {
	p.stopOnce.Do(func() { p.stopErr = p.shutdown(deadline) })
	return p.stopErr
}

func (p *PSInstance) shutdown(deadline time.Duration) error {
	p.state.Store(psDraining)
	close(p.stopped)
	if p.watchdog != nil {
		p.watchdog.Stop()
	}
	// The probe always has a stop word; an engine has one when its layout
	// names it (a writer is stopped by its stop export alone).
	hasStopWord := p.cfg.ABI == PSABIProbe || p.layout.StopWord != 0
	if hasStopWord {
		p.mem.Store32(p.layout.StopWord, 1)
	}
	// Threads parked in a revoked host call (A21) must come back to see the
	// stop word; from here on a revoked call fails at once instead of parking.
	p.io.ReleaseParked()
	if hasStopWord {
		_ = p.doorbell.NotifyAll(p.layout.StopWord)
	}
	for i := 0; i < int(p.layout.DoorbellCount); i++ {
		if p.doorbellAddrs != nil {
			_ = p.doorbell.NotifyAll(p.doorbellAddrs[i])
		} else {
			_ = p.doorbell.NotifyAll(p.layout.DoorbellBase + uint32(8*i))
		}
	}
	if !p.mod.Poisoned() {
		if v, err := p.control(p.cfg.ABI.exports().stop, float64(deadline.Milliseconds())); err == nil && len(v) > 0 {
			p.stopStatus.Store(wasmrt.ToInt32(v[0]))
		}
	}
	if p.mod.Poisoned() && p.cfg.ABI == PSABIP4 {
		// The format-4 engine's threads leave only through flatsql_p4_stop,
		// which a poisoned instance never runs: nothing can drain, so a
		// fenced instance goes straight to the executor stop and its owner
		// hears of the failure at once.
		deadline = 0
	}
	left := p.mod.WaitThreads(time.Now().Add(deadline))
	if left > 0 {
		left = p.mod.InterruptThreads(time.Now().Add(time.Second))
	}
	p.io.Revoke()
	p.io.ReleaseParked()
	p.doorbell.Close()
	p.poller.Close()
	for p.accessors.Load() != 0 {
		time.Sleep(50 * time.Microsecond)
	}
	<-p.superDone
	if left > 0 {
		p.state.Store(psDead)
		p.mod.MarkPoisoned(fmt.Errorf("%d service threads did not stop", left))
		if psRetained.Add(1) > PSRetentionLimit {
			return fmt.Errorf("%w (%d retained)", ErrPSRestartRequired, psRetained.Load())
		}
		return fmt.Errorf("%w: %d threads", ErrPSRetained, left)
	}
	p.mod.Release()
	for i := 0; i < 2000 && p.io.Reap() != 0; i++ {
		time.Sleep(time.Millisecond)
	}
	err := p.io.Free()
	if p.ownStore {
		p.store.Release()
	}
	p.state.Store(psDead)
	return err
}
