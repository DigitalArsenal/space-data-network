package format2

// The format-2 instance topology (design §5.1, A6, A12, A22, T6 scope 2):
//
//   - one writer instance: N = clamp(cores-2, 1, 16) writer threads;
//   - an interactive reader instance: L_i = max(2, ceil(2L/3)) lanes, with
//     L = clamp(cores/2, 3, 12) — index-bounded plans only;
//   - a bulk reader instance: L_b = max(1, L - L_i) lanes, nice +10;
//   - the control instance is the legacy engine on control2.flatsqldb
//     (store-migrate writes it; it stays until T8) and is opened by the
//     storage layer, not here.
//
// Each instance is its own poison domain: a trap in one fences and restarts
// that one (§15); readers learn committed state from files only, so they
// share no lock with the writer.
//
// Open refuses a store without MIGRATED (§16.1-9: format 2 never starts on a
// store store-migrate did not activate), except a fresh store (an empty data
// directory, A5), which the engine creates already MIGRATED.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

// ErrNotMigrated: the store root holds a legacy store that store-migrate
// has not activated as format 2.
var ErrNotMigrated = errors.New("format2: the store is not MIGRATED; run `spacedatanetwork store-migrate` (format 2 refuses to start)")

// Topology is the instance sizing. SandboxLanes (default 1) serve untrusted
// SQL under the work budget (A28, §22.4-7): never the shared bulk lanes.
// PointLanes (default 2) serve the O(1) statements — a record by CID, the
// copies of a CID, records by gseq — on an instance of their own, so a point
// read never queues behind a window on the interactive lanes (T6 #3:
// GetRecord p99 ≤ 5 ms, no reader wait > 50 ms).
type Topology struct {
	Writers, InteractiveLanes, BulkLanes uint32
	SandboxLanes, PointLanes             uint32
}

// DefaultTopology is §5.1 for a machine with the given cores.
func DefaultTopology(cores int) Topology {
	clamp := func(v, lo, hi int) int {
		if v < lo {
			return lo
		}
		if v > hi {
			return hi
		}
		return v
	}
	n := clamp(cores-2, 1, 16)
	l := clamp(cores/2, 3, 12)
	li := (2*l + 2) / 3
	if li < 2 {
		li = 2
	}
	lb := l - li
	if lb < 1 {
		lb = 1
	}
	return Topology{Writers: uint32(n), InteractiveLanes: uint32(li), BulkLanes: uint32(lb)}
}

// StoreConfig configures a format-2 store.
type StoreConfig struct {
	Root          string // the store root (storage.path)
	EngineRoot    string // the engine's root inside Root ("." by default)
	AOTCacheDir   string
	CompileOnMiss bool // tests only
	Topology      Topology
	QuotaBytes    uint64 // §13: cap on on-disk bytes (0: none)
	BallastBytes  uint64 // A13 (servers: 256 MiB; 0: none)
	CommitJournal bool   // §22.4 ruling 5 (per host)
	// AllowFresh creates a new format-2 store when the root holds no record
	// store at all (A5: fresh stores are format 2).
	AllowFresh bool
	// GatePeriod is how often the reader gate is passed to the writer (A12).
	GatePeriod time.Duration
	// FileIdentifier names a standard's file identifier ("$KMF"): a
	// standard with no embedded binary schema (the (encrypted) ones, whose
	// fields are never extracted) registers with it and no BFBS.
	FileIdentifier func(schemaName string) (string, bool)
}

// Store is an open format-2 store: the writer and reader instances.
type Store struct {
	cfg    StoreConfig
	heads  *HeadReader
	native *flatsqlrt.NativeStore
	w      *Writer
	ri     *Reader
	rb     *Reader
	rs     *Reader // sandbox lanes (untrusted SQL)
	rp     *Reader // point lanes (O(1) statements)

	regMu sync.Mutex
	types map[string]TypeSpec // schema -> registered spec

	stop     chan struct{}
	gateDone chan struct{}
	closeMu  sync.Once
	closeErr error

	seeks atomic.Uint64 // per-partition CID seeks (PartitionSeeks)

	// The window reads' CID budget (CIDSet): the most they may hold at once,
	// and what they hold now.
	windowMax, windowHeld atomic.Int64

	OpenedIn time.Duration
}

// Open opens (with AllowFresh, creates) the format-2 store at cfg.Root.
func Open(cfg StoreConfig) (*Store, error) {
	start := time.Now()
	if cfg.Topology.Writers == 0 {
		cfg.Topology = DefaultTopology(runtime.NumCPU())
	}
	if cfg.GatePeriod <= 0 {
		cfg.GatePeriod = time.Second
	}
	engineRoot := cfg.Root
	if cfg.EngineRoot != "" && cfg.EngineRoot != "." {
		engineRoot = filepath.Join(cfg.Root, cfg.EngineRoot)
	}
	writeFormat, err := WriteFormat()
	if err != nil {
		return nil, err
	}
	migrated, err := Migrated(engineRoot)
	if err != nil {
		return nil, err
	}
	create := false
	if !migrated {
		if !cfg.AllowFresh || legacyStorePresent(cfg.Root) {
			return nil, ErrNotMigrated
		}
		if _, err := os.Stat(filepath.Join(engineRoot, Dir, "STORE")); err == nil {
			return nil, ErrNotMigrated // a STORE without MIGRATED: an unfinished migration
		}
		create = true
	}
	s := &Store{cfg: cfg, types: map[string]TypeSpec{}, stop: make(chan struct{}), gateDone: make(chan struct{}),
		heads: NewHeadReader(engineRoot)}
	if s.native, err = flatsqlrt.OpenNativeStore(cfg.Root); err != nil {
		return nil, err
	}
	opt := InstanceOptions{Store: s.native, AOTCacheDir: cfg.AOTCacheDir, CompileOnMiss: cfg.CompileOnMiss}
	fail := func(err error) (*Store, error) {
		s.shutdown()
		return nil, err
	}
	if s.w, err = OpenWriter(opt, WriterConfig{Root: cfg.EngineRoot, Writers: cfg.Topology.Writers, Create: create, RequireMigrated: true,
		QuotaBytes: cfg.QuotaBytes, BallastBytes: cfg.BallastBytes, CommitJournal: cfg.CommitJournal,
		WriteFormat: writeFormat}); err != nil {
		return fail(fmt.Errorf("format2: writer instance: %w", err))
	}
	s.windowMax.Store(defaultWindowBudget())
	if s.ri, err = OpenReader(opt, flatsqlrt.PSRoleReader, ReaderConfig{Root: cfg.EngineRoot, Lanes: cfg.Topology.InteractiveLanes}); err != nil {
		return fail(fmt.Errorf("format2: interactive reader instance: %w", err))
	}
	if s.rb, err = OpenReader(opt, flatsqlrt.PSRoleBulk, ReaderConfig{Root: cfg.EngineRoot, Lanes: cfg.Topology.BulkLanes}); err != nil {
		return fail(fmt.Errorf("format2: bulk reader instance: %w", err))
	}
	sandbox := cfg.Topology.SandboxLanes
	if sandbox == 0 {
		sandbox = 1
	}
	if s.rs, err = OpenReader(opt, flatsqlrt.PSRoleSandbox, ReaderConfig{Root: cfg.EngineRoot, Lanes: sandbox}); err != nil {
		return fail(fmt.Errorf("format2: sandbox reader instance: %w", err))
	}
	point := cfg.Topology.PointLanes
	if point == 0 {
		point = 2
	}
	if s.rp, err = OpenReader(opt, flatsqlrt.PSRoleReader, ReaderConfig{Root: cfg.EngineRoot, Lanes: point}); err != nil {
		return fail(fmt.Errorf("format2: point reader instance: %w", err))
	}
	go s.gatePump()
	s.OpenedIn = time.Since(start)
	return s, nil
}

// legacyStorePresent reports a legacy record store at root (a control
// database FILE; activation replaces it with a directory).
func legacyStorePresent(root string) bool {
	fi, err := os.Lstat(filepath.Join(root, "control.flatsqldb"))
	return err == nil && fi.Mode().IsRegular()
}

// gatePump passes the oldest running reader statement to the writer (A12):
// retired files are unlinked only once every statement that might read them
// has ended.
func (s *Store) gatePump() {
	defer close(s.gateDone)
	t := time.NewTicker(s.cfg.GatePeriod)
	defer t.Stop()
	for {
		oldest := uint64(0)
		for _, r := range []*Reader{s.ri, s.rb, s.rs, s.rp} {
			if r == nil {
				continue
			}
			if v := r.OldestActiveStart(); v != 0 && (oldest == 0 || v < oldest) {
				oldest = v
			}
		}
		gate := float64(-1)
		if oldest != 0 {
			gate = float64(oldest)
		}
		_ = s.w.SetReaderGate(gate)
		select {
		case <-s.stop:
			return
		case <-t.C:
		}
	}
}

// Writer returns the writer instance.
func (s *Store) Writer() *Writer { return s.w }

// Interactive returns the interactive reader instance.
func (s *Store) Interactive() *Reader { return s.ri }

// Bulk returns the bulk reader instance.
func (s *Store) Bulk() *Reader { return s.rb }

// SetQuota changes the on-disk cap (§13).
func (s *Store) SetQuota(bytes uint64) error { return s.w.SetQuota(bytes) }

func (s *Store) shutdown() error {
	var first error
	for _, r := range []*Reader{s.ri, s.rb, s.rs, s.rp} {
		if r != nil {
			if err := r.Stop(); err != nil && first == nil {
				first = err
			}
		}
	}
	if s.w != nil {
		if err := s.w.Stop(); err != nil && first == nil {
			first = err
		}
	}
	if s.native != nil {
		s.native.Release()
	}
	if s.heads != nil {
		s.heads.Close()
	}
	return first
}

// Close stops the instances (bounded).
func (s *Store) Close() error {
	s.closeMu.Do(func() {
		close(s.stop)
		<-s.gateDone
		s.closeErr = s.shutdown()
	})
	return s.closeErr
}

// spec returns (registering on first use) the engine type of a schema.
func (s *Store) spec(schema string) (TypeSpec, error) {
	s.regMu.Lock()
	defer s.regMu.Unlock()
	if t, ok := s.types[schema]; ok {
		return t, nil
	}
	t, err := TypeSpecFor(schema)
	if err != nil && s.cfg.FileIdentifier != nil && !errors.Is(err, ErrSealedRuleField) {
		// No embedded binary schema (an (encrypted) standard): the engine
		// stores, dedupes and serves its frames by CID, arrival and tags;
		// it extracts nothing (A19: the sealed bytes carry only a magic).
		code := strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(schema), ".fbs"))
		if ident, ok := s.cfg.FileIdentifier(code + ".fbs"); ok && len(ident) == 4 {
			var fid [4]byte
			copy(fid[:], ident)
			t, err = CIDOnlyTypeSpec(code, fid), nil
		}
	}
	if err != nil {
		return TypeSpec{}, err
	}
	if err := s.w.RegisterType(t); err != nil {
		return TypeSpec{}, err
	}
	s.types[schema] = t
	return t, nil
}

// point runs an O(1) trusted read (a CID or gseq lookup) on the point lanes;
// a plan the engine finds unbounded moves on as query does.
func (s *Store) point(ctx context.Context, req Request) (*Result, error) {
	if s.rp == nil {
		return s.query(ctx, req)
	}
	res, err := s.rp.Query(ctx, req)
	if IsStatus(err, StatusNeedsBulk) {
		return s.rb.Query(ctx, req)
	}
	return res, err
}

// Point returns the point reader instance (acceptance measurements).
func (s *Store) Point() *Reader { return s.rp }

// query runs a trusted read on the interactive lanes, resubmitting an
// unbounded plan to the bulk lanes (§9 admission: FLATSQL_NEEDS_BULK).
func (s *Store) query(ctx context.Context, req Request) (*Result, error) {
	res, err := s.ri.Query(ctx, req)
	if IsStatus(err, StatusNeedsBulk) {
		return s.rb.Query(ctx, req)
	}
	return res, err
}
