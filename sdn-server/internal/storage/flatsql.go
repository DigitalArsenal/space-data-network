// Package storage provides SQLite-based storage with FlatBuffer support.
package storage

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/MPE"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/RFB"
	logging "github.com/ipfs/go-log/v2"
	"golang.org/x/crypto/scrypt"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/encfield"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/keys"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

var log = logging.Logger("storage")

// ErrStoreClosed is returned by read accessors reached AFTER Close.
//
// It exists because "closed" must be an ERROR VALUE, not a segfault. Several
// callers are deliberately fire-and-forget background goroutines (node.go's
// quota bookkeeping is spawned so it never adds latency to the materialization
// path), so they can and do outlive the store — and every one of them already
// handles an error return.
var ErrStoreClosed = errors.New("datastore is closed")

// ErrEngineRebuilding is returned by the engine query paths while
// RecoverPoisonedEngine holds the store write lock to rebuild a poisoned
// engine (26 minutes observed on host-02, 2026-09-02). A reader that arrives
// during the rebuild is answered at once with this error — mapped to 503 +
// Retry-After on the wire — instead of queueing behind the writer for the
// whole rebuild: reads never wait on the data layer (owner 2026-09-02).
var ErrEngineRebuilding = errors.New("engine is rebuilding; retry shortly")

const (
	localEPMStoreSalt     = "space-data-network-local-epm-store-v1"
	engineAOTCacheDirName = "flatsql-aot"
)

// engineAOTCacheDir returns the machine-wide AOT artifact cache. It is keyed
// by engine-bytes hash inside, so it is shared safely across datastores and
// processes; compiling per-datastore would redo a ~35 s LLVM compile for
// every store open (and every test).
// enginePageCacheEnv names the operator override for the control database's
// SQLite page cache, in MiB.
const enginePageCacheEnv = "SDN_FLATSQL_PAGE_CACHE_MIB"

// defaultEnginePageCacheMiB is the page cache the control database opens with.
//
// 256 MiB against a 10.7 GB database is still a small fraction of it, but it is
// two orders of magnitude more than SQLite's ~2 MB default and it is what makes
// an index-driven statement over millions of rows stop thrashing the host-IO
// shim. It lives inside the engine's linear memory, which is bounded at 4 GiB.
const defaultEnginePageCacheMiB = 256

// resolveEnginePageCacheMiB returns the configured page cache in MiB. 0
// disables the override and leaves SQLite's default in place.
func resolveEnginePageCacheMiB() int {
	raw := strings.TrimSpace(os.Getenv(enginePageCacheEnv))
	if raw == "" {
		return defaultEnginePageCacheMiB
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		log.Warnf("FlatSQL control database: ignoring %s=%q (%v); using %d MiB",
			enginePageCacheEnv, raw, err, defaultEnginePageCacheMiB)
		return defaultEnginePageCacheMiB
	}
	return n
}

func engineAOTCacheDir() string {
	if base, err := os.UserCacheDir(); err == nil {
		return filepath.Join(base, engineAOTCacheDirName)
	}
	return filepath.Join(os.TempDir(), engineAOTCacheDirName)
}

// EngineAOTCacheDir exposes the daemon's AOT cache directory so the
// `prewarm-aot` maintenance command compiles into the IDENTICAL directory the
// daemon reads at startup (resolution must not drift — same process, same
// HOME/XDG_CACHE_HOME yields the same path).
func EngineAOTCacheDir() string { return engineAOTCacheDir() }

// FlatSQLStore is the node's record store over the in-process FlatSQL-WASM
// engine. Every record's bytes, its index row, its provenance tags and the
// node's own auxiliary tables live in ONE disk-backed database
// (control.flatsqldb) written through the engine's VFS; a boot opens that
// file and serves (flatsql_boot_state.go). The engine's record vtabs are a
// bounded hot-window CACHE of the routed standards over the same records,
// tracked by a durable residency ledger (engine_residency.go).
type FlatSQLStore struct {
	fullTextMu             sync.Mutex
	fullTextStates         map[string]*fullTextIndexState
	fullTextSlots          chan struct{}
	fullTextWorkers        sync.WaitGroup
	fullTextClosing        bool
	db                     *sql.DB
	engine                 *flatsqlrt.Runtime
	engineDB               *flatsqlrt.Database
	auxiliaryMetadata      *auxiliaryMetadataStore
	assetPinTransactions   assetPinTransactionBeginner
	assetPinLedgerRecovery atomic.Bool
	// engineRebuilding is set while RecoverPoisonedEngine holds the write
	// lock; readGate answers ErrEngineRebuilding meanwhile.
	engineRebuilding   atomic.Bool
	engineHotHydrating atomic.Bool
	engineHotHydrated  atomic.Bool
	validator          *sds.Validator
	dbPath             string
	basePath           string
	// localEPMKeyCache memoizes the derived candidate keys for the local-EPM
	// row envelope (localEPMKeys) — scrypt is ~100ms per candidate and the
	// env/password-file inputs cannot change within a process lifetime.
	localEPMKeyOnce  sync.Once
	localEPMKeyCache [][]byte
	localEPMKeyErr   error
	// lock is the exclusive single-writer liveness lock on
	// <basePath>/store.lock (storelock.go) — held for the store's whole
	// lifetime, released by Close.
	lock *storeLock
	mu   sync.RWMutex
	// lockStats accounts s.mu itself — the lock the engine's slow-statement
	// instrument cannot see (store_lock_accounting.go).
	lockStats storeLockStats

	// unsummarizedCounts caches the COUNT(*)/SUM(record_length) scan that
	// DataSummary falls back to for a schema that has rows but no
	// source-summary lane yet. The dashboard stats lane calls DataSummary
	// every 5 s and each such scan held the single-threaded engine 0.4–0.9 s
	// per schema. A provisional count may be a minute old; the summary lane
	// replaces it.
	unsummarizedMu     sync.Mutex
	unsummarizedCounts map[string]unsummarizedCount
	// engineSources tracks per-source shadow tables already registered on the
	// engine (flatsql_register_source errors on duplicates). Guarded by mu.
	engineSources map[string]bool
	// engineHotWindow bounds the records resident in the engine vtabs per
	// schema (engine_records.go); engineResident tracks the live (non
	// tombstoned) resident count per schema. Both guarded by mu.
	engineHotWindow int
	engineResident  map[string]int64
	// engineGenericHotWindow is the smaller per-schema budget applied to
	// standards routed GENERICALLY (every embedded standard that is not one of
	// the two this host decorates). engineHotWindow x 226 standards is not a
	// bound; this is (engine_records.go engineWindowFor).
	engineGenericHotWindow int
	// engineViewRebuilds counts CreateUnifiedViews invocations on this store.
	// It is a BOUND, not a statistic: the rebuild is all-or-nothing across
	// every routed standard. Guarded by mu.
	engineViewRebuilds int64
	// engineExcluded names the routed standards THIS store does not route,
	// keyed by schema name: a plain control table already holds the
	// standard's canonical name, so routing it would DROP that table
	// (probeControlDatabase). Fixed at open; never mutated afterwards.
	engineExcluded map[string]bool
	// engineEpoch counts engine replacements (starts at 1); retiredEngines
	// holds poisoned runtimes whose live instances dependent flow VMs may
	// still reference — released at Close (engine_link.go). Both guarded by mu.
	engineEpoch    uint64
	retiredEngines []*flatsqlrt.Runtime
	// fieldEncMu guards the lazily-provisioned field-encryption identity
	// (field_encryption.go). Deliberately separate from mu: the seal and open
	// paths are reached while mu is already held (Lock or RLock, or not at
	// all, depending on the caller), and sync.RWMutex is not reentrant.
	fieldEncMu   sync.Mutex
	fieldEncPriv []byte
	fieldEncPub  []byte

	// bootBudget times each boot phase against the engine's per-call budget
	// (boot_phase_budget.go); nil outside boot.
	bootBudget *bootPhaseBudget

	// controlDBDurable reports that the engine's control database is backed by
	// a REAL FILE under basePath. Always true for an opened store; a test
	// clears it to simulate a crash that skips the final checkpoint.
	controlDBDurable bool
	// controlDBPath is the engine's own database file — deliberately NOT
	// dbPath, which names the legacy v1 database path that still salts the
	// local-EPM store key.
	controlDBPath string
	// checkpointStop signals the background checkpoint loop to leave;
	// checkpointDone is closed by the loop once it HAS left, and Close joins on
	// it. checkpointRunning says whether there is anything to join.
	// checkpointOnce makes the whole handshake idempotent.
	checkpointStop    chan struct{}
	checkpointDone    chan struct{}
	checkpointRunning atomic.Bool
	checkpointOnce    sync.Once
	// engineStateWarm / engineStateRecords record that the engine opened its
	// persisted record state at boot (openEngineRecordState).
	engineStateWarm    bool
	engineStateRecords int
	// engineMarkRowID is the sdn_record_index rowid the engine hot window has
	// been mirrored through as of the last flush (bootMarkEngineRowIDKey);
	// engineUnflushed counts engine ingests since that flush, so the
	// checkpoint loop knows whether there is anything to persist.
	engineMarkRowID atomic.Int64
	engineUnflushed atomic.Int64
	// engineHydrateBatchHook runs before each schema of the background
	// hot-window hydration, OUTSIDE the store lock. Tests use it to hold the
	// pass open while proving readers interleave.
	engineHydrateBatchHook func()
	// engineSchemaLoaded names the routed schemas whose hot window the current
	// hydration has already loaded (or reconciled from disk), so a reader of
	// THAT standard answers while other standards are still being rebuilt.
	// Guarded by s.mu.
	engineSchemaLoaded map[string]bool
	// auxAppliedOffset / auxCheckpointedOffset are the auxiliary journal's
	// applied high-water mark and its persisted resume mark. auxReplayed says
	// the auxiliary replay has run in this process — no auxiliary mark may be
	// written before it has.
	auxAppliedOffset      atomic.Int64
	auxCheckpointedOffset atomic.Int64
	auxReplayed           atomic.Bool
	// auxReplayWriter routes the auxiliary appliers into a batched replay's
	// CHUNK TRANSACTION. Nil except inside such a replay; see auxWrite.
	auxReplayWriter auxWriter
	// bootAuxFrom / bootAuxWarm / bootAuxFrames record what the auxiliary boot
	// replay did, for the startup log line and for tests that must prove a WARM
	// boot skipped work rather than merely being fast on a small fixture.
	bootAuxFrom   int64
	bootAuxWarm   bool
	bootAuxFrames int
}

// BootStats reports what the last open did.
type BootStats struct {
	// Durable reports whether the control database is backed by a real file.
	Durable bool
	// EngineWarm is true when the engine's persisted record state was opened
	// at boot, so no record was re-ingested to make the tables answer.
	EngineWarm bool
	// EngineRecords is the count the engine reported visible from its
	// persisted state at open.
	EngineRecords int
	// AuxWarm is true when the auxiliary journal resumed from a persisted mark;
	// AuxFrames is how many auxiliary frames the boot replay applied.
	AuxWarm   bool
	AuxFrames int
}

// BootState reports what this store's open did. The distinction that matters
// operationally is EngineWarm: a cold engine rebuilds its hot window from the
// tables, and "it was fast" is not evidence that the warm path was taken.
func (s *FlatSQLStore) BootState() BootStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return BootStats{
		Durable:       s.controlDBDurable,
		EngineWarm:    s.engineStateWarm,
		EngineRecords: s.engineStateRecords,
		AuxWarm:       s.bootAuxWarm,
		AuxFrames:     s.bootAuxFrames,
	}
}

// StoreOption configures a FlatSQLStore at open time.
type StoreOption func(*storeConfig)

type storeConfig struct {
	engineHotWindow        int
	engineGenericHotWindow int
	deferBootRebuilds      bool
	auxReplayChunkBytes    int64
}

// WithEngineHotWindow overrides the engine hot-window bound: the maximum
// records resident in the engine vtabs per schema, enforced at boot rebuild
// and by tombstone eviction at ingest. Values <= 0 keep the default
// (engineDefaultHotWindow). Eviction only affects the engine cache — the
// control tables are never touched.
func WithEngineHotWindow(records int) StoreOption {
	return func(c *storeConfig) {
		if records > 0 {
			c.engineHotWindow = records
		}
	}
}

// WithEngineGenericHotWindow overrides the hot-window bound for GENERICALLY
// routed standards — every embedded standard except the two this host
// decorates ($OMM, $TBS), which keep WithEngineHotWindow. Values <= 0 keep the
// default (engineDefaultGenericHotWindow). Config key
// storage.engine_generic_hot_window.
//
// A value LARGER than WithEngineHotWindow is honoured, not clamped; the store
// warns at open, because the two windows share one 4 GiB engine.
func WithEngineGenericHotWindow(records int) StoreOption {
	return func(c *storeConfig) {
		if records > 0 {
			c.engineGenericHotWindow = records
		}
	}
}

// WithDeferredBootRebuilds skips the synchronous engine hot-window hydration
// during open. Every record read from the control tables is available
// immediately; the engine's sandboxed query surface answers per standard as
// HydrateEngineHotWindowContext brings each window current in the background.
func WithDeferredBootRebuilds() StoreOption {
	return func(c *storeConfig) {
		c.deferBootRebuilds = true
	}
}

// WithAuxiliaryReplayChunkBytes bounds ONE auxiliary-journal replay
// transaction in BYTES as well as in frames (auxiliaryReplayChunkFrames).
// Values <= 0 keep the default (auxiliaryReplayChunkBytes, 8 MiB). A single
// frame larger than the budget is still applied whole: frames are never split.
func WithAuxiliaryReplayChunkBytes(bytes int64) StoreOption {
	return func(c *storeConfig) {
		if bytes > 0 {
			c.auxReplayChunkBytes = bytes
		}
	}
}

// NewFlatSQLStore opens (or creates) the record store at basePath. The store
// is single-writer: a second opener on the same path fails with
// ErrStoreLocked. There is no read-only open — a second process cannot share
// the engine's database file safely, and it has nothing to re-derive a
// private copy from; read through the daemon's API instead.
func NewFlatSQLStore(basePath string, validator *sds.Validator, opts ...StoreOption) (*FlatSQLStore, error) {
	cfg := storeConfig{
		engineHotWindow:        engineDefaultHotWindow,
		engineGenericHotWindow: engineDefaultGenericHotWindow,
	}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.engineGenericHotWindow > cfg.engineHotWindow {
		// HONOURED, NOT CLAMPED — and therefore said out loud.
		log.Warnf("FlatSQL engine: storage.engine_generic_hot_window (%d) is LARGER than storage.engine_hot_window (%d) — every generically routed standard now holds more records resident than the two decorated ones; all of them share the engine's 4 GiB ceiling",
			cfg.engineGenericHotWindow, cfg.engineHotWindow)
	}

	if err := os.MkdirAll(basePath, 0700); err != nil {
		return nil, fmt.Errorf("failed to create storage directory: %w", err)
	}
	// Single-writer liveness lock (storelock.go): taken BEFORE any store file
	// is touched, so a second writer process fails here with a clean
	// ErrStoreLocked instead of corrupting the database.
	lock, err := acquireStoreLock(basePath)
	if err != nil {
		return nil, err
	}
	// Release the lock on every failure path below; store.Close() releases
	// it on the paths that already have a store (release is idempotent).
	opened := false
	defer func() {
		if !opened {
			_ = lock.release()
		}
	}()

	// dbPath keeps its historical value: it salts the local-EPM store key and
	// it is what Path() reports. The engine's database is a different file.
	dbPath := filepath.Join(basePath, "sdn.db")
	controlDBPath := filepath.Join(basePath, flatSQLControlDBName)

	auxOpenStart := time.Now()
	auxiliaryMetadata, err := openAuxiliaryMetadataStore(filepath.Join(basePath, auxiliaryMetadataFileName), false)
	log.Infof("FlatSQL boot phase \"boot: open auxiliary metadata journal\" took %s",
		time.Since(auxOpenStart).Round(time.Millisecond))
	if err != nil {
		return nil, fmt.Errorf("failed to open auxiliary metadata: %w", err)
	}
	auxiliaryMetadata.chunkBytes = cfg.auxReplayChunkBytes
	auxiliaryOpened := false
	defer func() {
		if !auxiliaryOpened {
			auxiliaryMetadata.Close()
		}
	}()

	controlOpenStart := time.Now()
	engine, engineDB, mark, bootPlan, err := openControlEngine(basePath, controlDBPath)
	log.Infof("FlatSQL boot phase \"boot: probe + open control database\" took %s",
		time.Since(controlOpenStart).Round(time.Millisecond))
	if err != nil {
		return nil, err
	}
	// Always log the engine mode at open — AOT vs interpreted, and WHICH artifact
	// is executing. A stale AOT cache key (engine bytes or libwasmedge version
	// bumped without re-running `prewarm-aot`) degrades silently to the
	// interpreter, whose only symptom is a ~100x slowdown; production has been
	// burned by that twice, and an unconditional boot line is the cheapest way to
	// tell "slow because interpreted" from "slow because of a real regression".
	if mode := engine.Mode(); mode.AOT {
		log.Infof("FlatSQL engine mode: AOT (precompiled artifact %s)", mode.ArtifactPath)
	} else {
		log.Warnf("FlatSQL engine mode: INTERPRETED — no precompiled AOT artifact loaded from %s (%s); "+
			"daemon startup never compiles wasm artifacts. Run `spacedatanetwork prewarm-aot` as the daemon's user. "+
			"Queries will be ~100x slower.",
			mode.CacheDir, mode.MissReason)
	}
	// The mode is ASKED FOR, not asserted. This line said "journal_mode=TRUNCATE"
	// for as long as the store has been on WAL, which is the worst place to be
	// wrong: it is what an operator reads while diagnosing a power-loss gap, and
	// it told them the store fsyncs a rollback journal on every commit when it
	// appends to a WAL and syncs at NORMAL.
	log.Infof("FlatSQL control database: DISK-BACKED at %s (%s)", controlDBPath, controlDurabilityDescription(engineDB))

	// EVERY BOOT PHASE IS TIMED AGAINST THE ENGINE'S OWN PER-CALL BUDGET
	// (boot_phase_budget.go). A phase that crosses it in one call abandons the
	// execution thread and poisons the node until a human intervenes.
	bootBudget := newBootPhaseBudget(engine)
	endPhase := bootBudget.phase("boot: register engine file identifiers")
	if err := registerEngineFileIDs(engineDB, bootPlan.Excluded); err != nil {
		endPhase()
		engine.Close()
		return nil, fmt.Errorf("failed to register engine file identifiers: %w", err)
	}
	endPhase()
	// An exclusion is only real once the view a previous boot wrote for that
	// standard is gone; until then every plain-table path resolves the view
	// and answers `no such module`.
	endPhase = bootBudget.phase("boot: drop leftover views for unrouted standards")
	if err := dropExcludedStandardViews(engineDB, bootPlan.Excluded); err != nil {
		endPhase()
		engine.Close()
		return nil, fmt.Errorf("failed to clear leftover unified views for unrouted standards: %w", err)
	}
	endPhase()

	db := flatsqldrv.Open(engineDB)

	// THE PAGE CACHE IS A CONNECTOR SETTING. SQLite's default page cache is
	// ~2 MB; on a multi-GB database every cache miss is a page read back
	// through the engine's wasm host-IO shim, and a statement whose working
	// set does not fit re-reads the same index pages thousands of times.
	// Negative cache_size means "kibibytes", so the bound holds whatever the
	// page size is.
	if mib := resolveEnginePageCacheMiB(); mib > 0 {
		if _, err := db.Exec(fmt.Sprintf("PRAGMA cache_size = -%d", mib*1024)); err != nil {
			log.Warnf("FlatSQL control database: could not raise the page cache to %d MiB (%v) — large-store statements will re-read pages through the host shim", mib, err)
		} else {
			log.Infof("FlatSQL control database: page cache %d MiB (%s)", mib, enginePageCacheEnv)
		}
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		engine.Close()
		return nil, fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	store := &FlatSQLStore{
		db:                   db,
		engine:               engine,
		engineDB:             engineDB,
		auxiliaryMetadata:    auxiliaryMetadata,
		assetPinTransactions: sqlAssetPinTransactionBeginner{db: db},
		validator:            validator,
		dbPath:               dbPath,
		basePath:             basePath,
		lock:                 lock,
		engineSources:        bootPlan.registeredSources(),

		engineHotWindow:        cfg.engineHotWindow,
		engineGenericHotWindow: cfg.engineGenericHotWindow,
		engineResident:         map[string]int64{},
		engineSchemaLoaded:     map[string]bool{},
		engineExcluded:         bootPlan.Excluded,
		engineEpoch:            1,

		controlDBDurable: true,
		controlDBPath:    controlDBPath,
		checkpointStop:   make(chan struct{}),
		checkpointDone:   make(chan struct{}),
	}
	auxiliaryOpened = true

	auxResume := auxiliaryResumeOffset(mark, auxiliaryMetadata)
	store.bootAuxFrom = auxResume
	store.bootAuxWarm = auxResume > 0
	store.auxCheckpointedOffset.Store(auxResume)
	// A warm auxiliary boot inherits its mark's coverage: those frames ARE
	// applied, which is what made the resume legal.
	store.auxAppliedOffset.Store(auxResume)

	// THE ENGINE'S RECORDS SURVIVED. The mark was written only after the
	// engine flushed its record state, so every record the mark covers is
	// already visible in the vtabs: the hot-window hydration reconciles the
	// residency ledger and ingests only the records written past the mark.
	if bootPlan.EngineState.Warm {
		store.engineStateWarm = true
		store.engineStateRecords = bootPlan.EngineState.Records
		store.engineMarkRowID.Store(mark.EngineRowID)
		log.Infof("FlatSQL engine records: WARM — %d persisted record(s) visible from disk without re-ingest; records past index rowid %d will be ingested",
			bootPlan.EngineState.Records, mark.EngineRowID)
	}

	store.bootBudget = bootBudget
	endPhase = bootBudget.phase("boot: initTables (control schema, indexes)")
	if err := store.initTables(); err != nil {
		endPhase()
		store.Close()
		return nil, fmt.Errorf("failed to initialize tables: %w", err)
	}
	endPhase()
	store.settleEngineResidencyAtOpen()
	// Complete engine source bring-up: the runtime half of every persisted
	// source was restored before the database's first query (enginePrepare);
	// this guarantees a default partition on an empty store and rebuilds the
	// unified views only when they are not already current.
	endPhase = bootBudget.phase("boot: engine source setup + unified-view rebuild")
	if err := store.finishEngineSourceSetup(bootPlan); err != nil {
		endPhase()
		store.Close()
		return nil, fmt.Errorf("failed to complete engine source setup: %w", err)
	}
	endPhase()
	// THE AUXILIARY REPLAY STAYS ON THE CRITICAL PATH, DELIBERATELY. Its tables
	// are the node's own state — its encrypted local EPM (read during identity
	// bring-up, the instant this constructor returns), the directory, the pin
	// ledger, dataset shard publications and their replay cursors, source
	// licences, asset pin references. A daemon that resumed publication with
	// those tables half-populated would be serving from state it does not
	// have yet. It is cheap: a resume mark plus chunk transactions.
	auxReplayStart := time.Now()
	endPhase = bootBudget.phase("boot: auxiliary metadata replay")
	auxApplied, auxThrough, err := auxiliaryMetadata.ReplayFrom(store, auxResume)
	endPhase()
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("failed to replay auxiliary metadata: %w", err)
	}
	store.noteAuxiliaryAppliedThrough(auxThrough)
	store.bootAuxFrames = auxApplied
	store.auxReplayed.Store(true)
	if store.bootAuxWarm {
		log.Infof("FlatSQL store: WARM auxiliary metadata — resumed at journal offset %d, applied %d frames in %s",
			auxResume, auxApplied, time.Since(auxReplayStart).Round(time.Millisecond))
	} else {
		log.Infof("FlatSQL store: cold auxiliary metadata — replayed %d frames from the beginning in %s",
			auxApplied, time.Since(auxReplayStart).Round(time.Millisecond))
	}
	if cfg.deferBootRebuilds {
		log.Infof("FlatSQL store: deferred engine hot-window hydration at open")
	} else {
		endPhase = bootBudget.phase("boot: engine hot-window hydration")
		if err := store.RebuildDerivedState(); err != nil {
			endPhase()
			store.Close()
			return nil, fmt.Errorf("failed to hydrate the engine hot window: %w", err)
		}
		endPhase()
	}

	// Advance the auxiliary mark to cover everything the boot just applied,
	// then start the periodic checkpointer. Without it a CRASH would cost a
	// replay of everything since the last boot; with it, at most one interval.
	if err := store.Checkpoint(); err != nil {
		log.Warnf("FlatSQL store: initial checkpoint failed (nothing is lost): %v", err)
	}
	if interval := resolveCheckpointInterval(); interval > 0 {
		store.checkpointRunning.Store(true)
		go store.runCheckpointLoop(interval)
	} else {
		log.Warnf("FlatSQL store: background checkpointing DISABLED by %s=0", checkpointIntervalEnv)
	}

	bootBudget.summary()
	opened = true
	return store, nil
}

// RebuildDerivedState brings the engine hot window current synchronously,
// holding the store write lock: the non-deferred open (CLI verbs, tests) and
// the store maintenance API. The daemon defers this and runs
// HydrateEngineHotWindowContext in the background instead.
func (s *FlatSQLStore) RebuildDerivedState() error {
	defer s.lockWrite("RebuildDerivedState")()
	budget := newBootPhaseBudget(s.engine)
	end := budget.phase("maintenance: engine hot-window hydration")
	err := s.rebuildEngineRecordsLocked()
	end()
	if err != nil {
		return fmt.Errorf("engine records: %w", err)
	}
	budget.summary()
	return nil
}

// RebuildSourceSummaries recomputes the derived sdn_record_source_summary
// aggregate (which feeds /api/v1/stats sources[] and drives batch-clear
// bookkeeping) from the durable source-tag + record tables. Every writer
// maintains the summary incrementally, so this is a maintenance verb for an
// operator who suspects drift, not a boot step. It holds the store write lock.
func (s *FlatSQLStore) RebuildSourceSummaries() error {
	defer s.lockWrite("RebuildSourceSummaries")()
	return s.rebuildSourceSummariesFromDurableState()
}

// EngineSchemaReady reports whether a standard's engine table can answer a
// query now: always once the hot window is hydrated, and during a hydration
// as soon as that standard's window has been loaded (or reconciled from
// disk). The table lane gates PER STANDARD on this instead of refusing every
// read while any rebuild runs — a $TRE reader must not wait for $TBS.
func (s *FlatSQLStore) EngineSchemaReady(schemaName string) bool {
	if s == nil {
		return false
	}
	if s.engineHotHydrated.Load() {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.engineSchemaLoaded[normalizeSchemaNameForEpoch(schemaName)]
}

func (s *FlatSQLStore) engineSchemaLoadedSet(schemaName string) {
	if s.engineSchemaLoaded == nil {
		s.engineSchemaLoaded = map[string]bool{}
	}
	s.engineSchemaLoaded[normalizeSchemaNameForEpoch(schemaName)] = true
}

// EngineHotWindowHydrating reports whether the engine hot window is currently
// being brought current.
func (s *FlatSQLStore) EngineHotWindowHydrating() bool {
	return s != nil && s.engineHotHydrating.Load()
}

// EngineHotWindowHydrated reports whether the engine hot window is current
// for this process.
func (s *FlatSQLStore) EngineHotWindowHydrated() bool {
	return s == nil || s.engineHotHydrated.Load()
}

// requireWritable fails a public write verb with a typed, actionable error
// while the store is frozen for asset-pin ledger recovery.
func (s *FlatSQLStore) requireWritable(op string) error {
	if s.assetPinLedgerRecovery.Load() {
		return fmt.Errorf("%s requires closing and reopening the store to replay durable asset-pin state: %w", op, ErrAssetPinLedgerRecoveryRequired)
	}
	return nil
}

func (s *FlatSQLStore) initTables() error {
	// Create main metadata table
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sdn_metadata (
			key TEXT PRIMARY KEY,
			value TEXT,
			updated_at INTEGER
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create metadata table: %w", err)
	}
	// The engine hot window's residency ledger (engine_residency.go).
	if _, err := s.db.Exec(engineRowsTableSQL); err != nil {
		return fmt.Errorf("failed to create engine residency table: %w", err)
	}
	if _, err := s.db.Exec(engineRowsSeqIndexSQL); err != nil {
		return fmt.Errorf("failed to create engine residency index: %w", err)
	}

	// Fast lookup index for API queries (schema/day/object filters).
	recordIndexExisted, err := s.tableExists("sdn_record_index")
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sdn_record_index (
			schema_name TEXT NOT NULL,
			cid TEXT NOT NULL,
			norad_cat_id INTEGER,
			entity_id TEXT,
			object_type TEXT,
			ops_status_code TEXT,
			epoch_unix INTEGER,
			epoch_day TEXT,
			source_timestamp INTEGER NOT NULL,
			created_at INTEGER DEFAULT (strftime('%s', 'now')),
			PRIMARY KEY (schema_name, cid)
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create index table: %w", err)
	}
	if err := s.ensureColumn("sdn_record_index", "object_type", "TEXT"); err != nil {
		return err
	}
	if err := s.ensureColumn("sdn_record_index", "ops_status_code", "TEXT"); err != nil {
		return err
	}
	if err := s.createStartupIndex("sdn_record_index", "idx_sdn_record_index_lookup", recordIndexExisted, `
		CREATE INDEX IF NOT EXISTS idx_sdn_record_index_lookup
		ON sdn_record_index (schema_name, epoch_day, norad_cat_id, entity_id, source_timestamp DESC)
	`); err != nil {
		return fmt.Errorf("failed to create composite index: %w", err)
	}

	if err := s.createStartupIndex("sdn_record_index", "idx_sdn_record_index_norad", recordIndexExisted, `
		CREATE INDEX IF NOT EXISTS idx_sdn_record_index_norad
		ON sdn_record_index (schema_name, norad_cat_id, source_timestamp DESC)
	`); err != nil {
		return fmt.Errorf("failed to create norad index: %w", err)
	}

	if err := s.createStartupIndex("sdn_record_index", "idx_sdn_record_index_entity", recordIndexExisted, `
		CREATE INDEX IF NOT EXISTS idx_sdn_record_index_entity
		ON sdn_record_index (schema_name, entity_id, source_timestamp DESC)
	`); err != nil {
		return fmt.Errorf("failed to create entity index: %w", err)
	}

	if err := s.createStartupIndex("sdn_record_index", "idx_sdn_record_index_catalog_filters", recordIndexExisted, `
		CREATE INDEX IF NOT EXISTS idx_sdn_record_index_catalog_filters
		ON sdn_record_index (schema_name, object_type, ops_status_code, norad_cat_id)
	`); err != nil {
		return fmt.Errorf("failed to create catalog filter index: %w", err)
	}

	if err := s.createStartupIndex("sdn_record_index", "idx_sdn_record_index_time_window", recordIndexExisted, `
		CREATE INDEX IF NOT EXISTS idx_sdn_record_index_time_window
		ON sdn_record_index (schema_name, epoch_unix, source_timestamp DESC)
	`); err != nil {
		return fmt.Errorf("failed to create time window index: %w", err)
	}

	sourceTagsExisted, err := s.initSourceTagsTable()
	if err != nil {
		return fmt.Errorf("failed to create source tags table: %w", err)
	}

	if !sourceTagsExisted {
		log.Infof("Building FlatSQL source tag producer uniqueness index")
	}
	if err := s.createStartupIndex("sdn_record_source_tags", "idx_sdn_record_source_tags_unique", sourceTagsExisted, `
		CREATE UNIQUE INDEX IF NOT EXISTS idx_sdn_record_source_tags_unique
		ON sdn_record_source_tags (
			schema_name, cid, provider_id, source_name, batch_id,
			content_key_id, producer_peer_id, producer_public_key
		)
	`); err != nil {
		return fmt.Errorf("failed to create source tags producer uniqueness index: %w", err)
	}
	if err := s.createStartupIndex("sdn_record_source_tags", "idx_sdn_record_source_tags_lookup", sourceTagsExisted, `
		CREATE INDEX IF NOT EXISTS idx_sdn_record_source_tags_lookup
		ON sdn_record_source_tags (schema_name, provider_id, source_name, batch_id)
	`); err != nil {
		return fmt.Errorf("failed to create source tags lookup index: %w", err)
	}
	if err := s.createRequiredStartupIndex("sdn_record_source_tags", "idx_sdn_record_source_tags_source_cid", `
		CREATE INDEX IF NOT EXISTS idx_sdn_record_source_tags_source_cid
		ON sdn_record_source_tags (schema_name, provider_id, source_name, cid)
	`); err != nil {
		return fmt.Errorf("failed to create source tags source/cid index: %w", err)
	}
	if err := s.createRequiredStartupIndex("sdn_record_source_tags", "idx_sdn_record_source_tags_source_name_cid", `
		CREATE INDEX IF NOT EXISTS idx_sdn_record_source_tags_source_name_cid
		ON sdn_record_source_tags (schema_name, source_name, cid)
	`); err != nil {
		return fmt.Errorf("failed to create source tags source-name/cid index: %w", err)
	}
	if err := s.createRequiredStartupIndex("sdn_record_source_tags", "idx_sdn_record_source_tags_batch_cid", `
		CREATE INDEX IF NOT EXISTS idx_sdn_record_source_tags_batch_cid
		ON sdn_record_source_tags (schema_name, provider_id, source_name, batch_id, cid)
	`); err != nil {
		return fmt.Errorf("failed to create source tags batch/cid index: %w", err)
	}
	if err := s.createStartupIndex("sdn_record_source_tags", "idx_sdn_record_source_tags_recent", sourceTagsExisted, `
		CREATE INDEX IF NOT EXISTS idx_sdn_record_source_tags_recent
		ON sdn_record_source_tags (schema_name, created_at DESC, cid)
	`); err != nil {
		return fmt.Errorf("failed to create source tags recent index: %w", err)
	}
	if err := s.initSourceSummaryTable(); err != nil {
		return fmt.Errorf("failed to create source summary table: %w", err)
	}
	if err := s.initSourceBatchLicenseTable(); err != nil {
		return err
	}
	if err := s.initDatasetShardPublicationTable(); err != nil {
		return fmt.Errorf("failed to create dataset shard publication table: %w", err)
	}
	if err := s.initPinLedgerTable(); err != nil {
		return fmt.Errorf("failed to create pin ledger table: %w", err)
	}
	if err := s.initAssetPinLedgerTables(); err != nil {
		return fmt.Errorf("failed to create asset pin ledger tables: %w", err)
	}
	if err := s.initDatasetPublicationReplayStateTable(); err != nil {
		return fmt.Errorf("failed to create dataset publication replay state table: %w", err)
	}

	// Directory index for node/user EPM records.
	directoryExisted, err := s.tableExists("sdn_directory")
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sdn_directory (
			kind TEXT NOT NULL,
			peer_id TEXT NOT NULL,
			dn TEXT,
			legal_name TEXT,
			bitcoin_address TEXT,
			epm_cid TEXT,
			source TEXT NOT NULL,
			updated_at INTEGER NOT NULL,
			epm_json TEXT NOT NULL,
			PRIMARY KEY (kind, peer_id)
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create directory table: %w", err)
	}

	if err := s.createStartupIndex("sdn_directory", "idx_sdn_directory_search", directoryExisted, `
		CREATE INDEX IF NOT EXISTS idx_sdn_directory_search
		ON sdn_directory (kind, dn, legal_name, bitcoin_address)
	`); err != nil {
		return fmt.Errorf("failed to create directory search index: %w", err)
	}

	if err := s.createStartupIndex("sdn_directory", "idx_sdn_directory_updated", directoryExisted, `
		CREATE INDEX IF NOT EXISTS idx_sdn_directory_updated
		ON sdn_directory (kind, updated_at DESC)
	`); err != nil {
		return fmt.Errorf("failed to create directory updated index: %w", err)
	}

	// Local EPM source-of-truth records. Only the size-prefixed EPM FlatBuffer
	// bytes are persisted; editable forms and JSON views are derived from the
	// FlatBuffer at the API edge.
	if err := s.initLocalEPMTable(); err != nil {
		return fmt.Errorf("failed to create local EPM table: %w", err)
	}

	// Publication log index for PLG hash-chained logs.
	logIndexExisted, err := s.tableExists("sdn_log_index")
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		CREATE TABLE IF NOT EXISTS sdn_log_index (
			publisher_peer_id TEXT NOT NULL,
			schema_type       TEXT NOT NULL,
			sequence          INTEGER NOT NULL,
			entry_hash        TEXT NOT NULL,
			record_cid        TEXT NOT NULL,
			plg_cid           TEXT NOT NULL,
			epoch_day         TEXT,
			timestamp         INTEGER NOT NULL,
			PRIMARY KEY (publisher_peer_id, schema_type, sequence)
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create log index table: %w", err)
	}

	if err := s.createStartupIndex("sdn_log_index", "idx_sdn_log_index_head", logIndexExisted, `
		CREATE INDEX IF NOT EXISTS idx_sdn_log_index_head
		ON sdn_log_index (publisher_peer_id, schema_type, sequence DESC)
	`); err != nil {
		return fmt.Errorf("failed to create log head index: %w", err)
	}

	if err := s.createStartupIndex("sdn_log_index", "idx_sdn_log_index_epoch", logIndexExisted, `
		CREATE INDEX IF NOT EXISTS idx_sdn_log_index_epoch
		ON sdn_log_index (schema_type, epoch_day, timestamp DESC)
	`); err != nil {
		return fmt.Errorf("failed to create log epoch index: %w", err)
	}

	return nil
}

func (s *FlatSQLStore) createStartupIndex(tableName, indexName string, tableExisted bool, createSQL string) error {
	return s.createStartupIndexWithSQL(tableName, indexName, tableExisted, createSQL)
}

func (s *FlatSQLStore) createStartupIndexNoJournal(tableName, indexName string, tableExisted bool, createSQL string) error {
	return s.createStartupIndexWithSQL(tableName, indexName, tableExisted, flatsqldrv.WithoutJournal(createSQL))
}

func (s *FlatSQLStore) createStartupIndexWithSQL(tableName, indexName string, tableExisted bool, createSQL string) error {
	indexExists, err := s.indexExists(indexName)
	if err != nil {
		return err
	}
	if indexExists {
		return nil
	}
	if tableExisted {
		log.Warnf("Skipping synchronous startup creation of missing index %s on existing table %s; rebuild indexes during maintenance", indexName, tableName)
		return nil
	}
	if _, err := s.db.Exec(createSQL); err != nil {
		return err
	}
	return nil
}

func (s *FlatSQLStore) createRequiredStartupIndex(tableName, indexName string, createSQL string) error {
	indexExists, err := s.indexExists(indexName)
	if err != nil {
		return err
	}
	if indexExists {
		return nil
	}
	log.Infof("Building required FlatSQL index %s on %s", indexName, tableName)
	if _, err := s.db.Exec(createSQL); err != nil {
		return err
	}
	return nil
}

func (s *FlatSQLStore) initSourceTagsTable() (bool, error) {
	existed, err := s.tableExists("sdn_record_source_tags")
	if err != nil {
		return false, err
	}
	if !existed {
		_, err := s.db.Exec(sourceTagsTableSQL("sdn_record_source_tags"))
		return false, err
	}
	needsMigration, err := s.sourceTagsTableNeedsMigration()
	if err != nil {
		return true, err
	}
	if !needsMigration {
		return true, nil
	}
	if _, err := s.db.Exec(`DROP TABLE IF EXISTS sdn_record_source_tags_next`); err != nil {
		return true, err
	}
	if _, err := s.db.Exec(sourceTagsMigrationTableSQL("sdn_record_source_tags_next")); err != nil {
		return true, err
	}
	columns, err := s.tableColumnSet("sdn_record_source_tags")
	if err != nil {
		return true, err
	}
	producerPeerExpr := "TRIM(provider_id)"
	if columns["producer_peer_id"] {
		producerPeerExpr = "COALESCE(NULLIF(producer_peer_id, ''), TRIM(provider_id))"
	}
	producerPublicKeyExpr := "COALESCE(content_key_id, '')"
	if columns["producer_public_key"] {
		producerPublicKeyExpr = "COALESCE(NULLIF(producer_public_key, ''), COALESCE(content_key_id, ''))"
	}
	log.Infof("Migrating FlatSQL source tags to producer-aware provenance keys")
	if _, err := s.db.Exec(fmt.Sprintf(`
		INSERT OR IGNORE INTO sdn_record_source_tags_next (
			schema_name, cid, provider_id, source_name, source_url, batch_id,
			content_key_id, producer_peer_id, producer_public_key, created_at
		)
		SELECT
			schema_name,
			cid,
			TRIM(provider_id),
			TRIM(source_name),
			source_url,
			COALESCE(batch_id, ''),
			COALESCE(content_key_id, ''),
			%s,
			%s,
			created_at
		FROM sdn_record_source_tags
	`, producerPeerExpr, producerPublicKeyExpr)); err != nil {
		return true, err
	}
	if _, err := s.db.Exec(`DROP TABLE sdn_record_source_tags`); err != nil {
		return true, err
	}
	if _, err := s.db.Exec(`ALTER TABLE sdn_record_source_tags_next RENAME TO sdn_record_source_tags`); err != nil {
		return true, err
	}
	log.Infof("Migrated FlatSQL source tags to producer-aware provenance keys")
	return false, nil
}

func sourceTagsTableSQL(tableName string) string {
	return fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			schema_name TEXT NOT NULL,
			cid TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			source_name TEXT NOT NULL,
			source_url TEXT,
			batch_id TEXT NOT NULL DEFAULT '',
			content_key_id TEXT NOT NULL DEFAULT '',
			producer_peer_id TEXT NOT NULL DEFAULT '',
			producer_public_key TEXT NOT NULL DEFAULT '',
			created_at INTEGER DEFAULT (strftime('%%s', 'now')),
			PRIMARY KEY (
				schema_name, cid, provider_id, source_name, batch_id,
				content_key_id, producer_peer_id, producer_public_key
			)
		)
	`, tableName)
}

func sourceTagsMigrationTableSQL(tableName string) string {
	return fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			schema_name TEXT NOT NULL,
			cid TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			source_name TEXT NOT NULL,
			source_url TEXT,
			batch_id TEXT NOT NULL DEFAULT '',
			content_key_id TEXT NOT NULL DEFAULT '',
			producer_peer_id TEXT NOT NULL DEFAULT '',
			producer_public_key TEXT NOT NULL DEFAULT '',
			created_at INTEGER DEFAULT (strftime('%%s', 'now'))
		)
	`, tableName)
}

func (s *FlatSQLStore) sourceTagsTableNeedsMigration() (bool, error) {
	rows, err := s.db.Query(`PRAGMA table_info(sdn_record_source_tags)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	columns := map[string]int{}
	present := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		columns[name] = pk
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	required := []string{
		"schema_name", "cid", "provider_id", "source_name", "batch_id",
		"content_key_id", "producer_peer_id", "producer_public_key",
	}
	for _, name := range required {
		if !present[name] {
			return true, nil
		}
	}
	hasPrimaryKey := true
	for _, name := range required {
		if columns[name] == 0 {
			hasPrimaryKey = false
			break
		}
	}
	if hasPrimaryKey {
		return false, nil
	}
	hasUniqueIndex, err := s.indexHasColumns("idx_sdn_record_source_tags_unique", required)
	if err != nil {
		return false, err
	}
	return !hasUniqueIndex, nil
}

func (s *FlatSQLStore) indexHasColumns(indexName string, expected []string) (bool, error) {
	exists, err := s.indexExists(indexName)
	if err != nil || !exists {
		return exists, err
	}
	rows, err := s.db.Query(fmt.Sprintf(`PRAGMA index_info(%s)`, indexName))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	actual := make([]string, 0, len(expected))
	for rows.Next() {
		var seqno, cid int
		var name string
		if err := rows.Scan(&seqno, &cid, &name); err != nil {
			return false, err
		}
		actual = append(actual, name)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if len(actual) != len(expected) {
		return false, nil
	}
	for i := range expected {
		if actual[i] != expected[i] {
			return false, nil
		}
	}
	return true, nil
}

func (s *FlatSQLStore) initSourceSummaryTable() error {
	sourceSummaryExisted, err := s.tableExists("sdn_record_source_summary")
	if err != nil {
		return err
	}
	if sourceSummaryExisted {
		columns, err := s.tableColumnSet("sdn_record_source_summary")
		if err != nil {
			return err
		}
		if !columns["producer_peer_id"] || !columns["producer_public_key"] {
			if _, err := s.db.Exec(flatsqldrv.WithoutJournal(`DROP TABLE sdn_record_source_summary`)); err != nil {
				return err
			}
			sourceSummaryExisted = false
		}
	}
	_, err = s.db.Exec(flatsqldrv.WithoutJournal(sourceSummaryTableSQL("sdn_record_source_summary")))
	if err != nil {
		return fmt.Errorf("failed to create source summary table: %w", err)
	}
	if err := s.ensureColumn("sdn_record_source_summary", "max_rowid", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// first_seen lives on the summary because the read that needs it — the
	// per-producer progress feed — must not have to group the whole
	// sdn_record_source_tags table to find it. On a node holding a real
	// catalog that join costs more than the feed's read budget, and a producer
	// lane that cannot be read inside the budget is a producer that never
	// appears at all. Legacy rows carry 0 and are reported as unknown rather
	// than as 1970.
	if err := s.ensureColumn("sdn_record_source_summary", "first_seen", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.createStartupIndexNoJournal("sdn_record_source_summary", "idx_sdn_record_source_summary_schema", sourceSummaryExisted, `
		CREATE INDEX IF NOT EXISTS idx_sdn_record_source_summary_schema
		ON sdn_record_source_summary (schema_name)
	`); err != nil {
		return fmt.Errorf("failed to create source summary schema index: %w", err)
	}
	return nil
}

func sourceSummaryTableSQL(tableName string) string {
	return fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			schema_name TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			source_name TEXT NOT NULL,
			batch_id TEXT NOT NULL,
			producer_peer_id TEXT NOT NULL DEFAULT '',
			producer_public_key TEXT NOT NULL DEFAULT '',
			record_count INTEGER NOT NULL,
			total_bytes INTEGER NOT NULL,
			max_rowid INTEGER NOT NULL DEFAULT 0,
			first_seen INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT (strftime('%%s', 'now')),
			PRIMARY KEY (
				schema_name, provider_id, source_name, batch_id,
				producer_peer_id, producer_public_key
			)
		)
	`, tableName)
}

func (s *FlatSQLStore) createSchemaMetadataTable(tableName string) error {
	createSQL := schemaMetadataTableSQL(tableName)
	if strings.HasPrefix(tableName, "sds_p_") {
		createSQL = flatsqldrv.WithoutJournal(createSQL)
	}
	_, err := s.db.Exec(createSQL)
	return err
}

// schemaMetadataTableSQL is the DDL of a (producer, standard) record table.
// `data` IS the record: the FlatBuffer bytes exactly as stored (sealed when
// the standard declares an `(encrypted)` field). `supersede_key` is the
// supersede LANE a record belongs to within this producer's table —
// "src:<source>\0<object identity>", or the bare identity for an unattributed
// write (record_supersede.go); NULL for standards with no supersede rule.
func schemaMetadataTableSQL(tableName string) string {
	return fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			cid TEXT PRIMARY KEY,
			peer_id TEXT NOT NULL,
			timestamp INTEGER NOT NULL,
			data BLOB NOT NULL,
			record_length INTEGER NOT NULL,
			signature_hex TEXT,
			supersede_key TEXT,
			created_at INTEGER DEFAULT (strftime('%%s', 'now')),
			UNIQUE(cid)
		)
	`, tableName)
}

// schemaSupersedeIndexSQL is the per-table index the supersede lookup seeks on.
func schemaSupersedeIndexSQL(tableName string) string {
	return fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s_supersede ON %s (supersede_key)`, tableName, tableName)
}

func (s *FlatSQLStore) tableExists(tableName string) (bool, error) {
	// Exclude virtual tables: the engine's SDS record vtabs live in the same
	// SQLite context (e.g. "OMM"), and every tableExists caller is asking
	// about plain control/metadata tables — the OMM vtab must not be mistaken
	// for the legacy per-standard metadata table of the same name.
	var name string
	err := s.db.QueryRow(`
		SELECT name FROM sqlite_master
		WHERE type = 'table' AND name = ?
		  AND COALESCE(sql, '') NOT LIKE 'CREATE VIRTUAL%'
	`, tableName).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to inspect table %s: %w", tableName, err)
	}
	return true, nil
}

func (s *FlatSQLStore) indexExists(indexName string) (bool, error) {
	var name string
	err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, indexName).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to inspect index %s: %w", indexName, err)
	}
	return true, nil
}

func (s *FlatSQLStore) ensureColumn(tableName, columnName, columnType string) error {
	rows, err := s.db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, tableName))
	if err != nil {
		return fmt.Errorf("failed to inspect %s columns: %w", tableName, err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return fmt.Errorf("failed to scan %s column metadata: %w", tableName, err)
		}
		if strings.EqualFold(name, columnName) {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed iterating %s columns: %w", tableName, err)
	}

	alterSQL := fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, tableName, columnName, columnType)
	if tableName == "sdn_record_source_summary" {
		alterSQL = flatsqldrv.WithoutJournal(alterSQL)
	}
	if _, err := s.db.Exec(alterSQL); err != nil {
		return fmt.Errorf("failed to add %s.%s: %w", tableName, columnName, err)
	}
	return nil
}

func (s *FlatSQLStore) initLocalEPMTable() error {
	const createSQL = `
		CREATE TABLE IF NOT EXISTS sdn_local_epms (
			peer_id TEXT PRIMARY KEY,
			schema_name TEXT NOT NULL DEFAULT 'EPM.fbs',
			encrypted_epm_bytes TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		)
	`

	exists, err := s.tableExists("sdn_local_epms")
	if err != nil {
		return err
	}
	if !exists {
		_, err := s.db.Exec(createSQL)
		return err
	}

	columns, err := s.tableColumnSet("sdn_local_epms")
	if err != nil {
		return err
	}
	if !columns["encrypted_profile_json"] && !columns["encrypted_epm_json"] {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		CREATE TABLE sdn_local_epms_next (
			peer_id TEXT PRIMARY KEY,
			schema_name TEXT NOT NULL DEFAULT 'EPM.fbs',
			encrypted_epm_bytes TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		)
	`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT OR REPLACE INTO sdn_local_epms_next (
			peer_id, schema_name, encrypted_epm_bytes, updated_at
		)
		SELECT
			peer_id,
			COALESCE(NULLIF(schema_name, ''), 'EPM.fbs'),
			encrypted_epm_bytes,
			updated_at
		FROM sdn_local_epms
		WHERE encrypted_epm_bytes IS NOT NULL AND encrypted_epm_bytes <> ''
	`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE sdn_local_epms`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE sdn_local_epms_next RENAME TO sdn_local_epms`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *FlatSQLStore) tableColumnSet(tableName string) (map[string]bool, error) {
	rows, err := s.db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, tableName))
	if err != nil {
		return nil, fmt.Errorf("failed to inspect %s columns: %w", tableName, err)
	}
	defer rows.Close()

	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, fmt.Errorf("failed to scan %s column metadata: %w", tableName, err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed iterating %s columns: %w", tableName, err)
	}
	return columns, nil
}

type sqlExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// storedRecord is one record as it lands in a (producer, standard) table.
type storedRecord struct {
	cid          string
	peerID       string
	timestamp    int64
	data         []byte // sealed when the standard has encrypted fields
	signature    []byte
	createdAt    int64
	supersedeKey string
}

func insertSchemaMetadata(exec sqlExecer, tableName string, rec storedRecord) error {
	_, err := insertSchemaMetadataWithMode(exec, tableName, rec, true)
	return err
}

func insertSchemaMetadataReturningRowID(exec sqlExecer, tableName string, rec storedRecord) (int64, error) {
	return insertSchemaMetadataWithMode(exec, tableName, rec, false)
}

// schemaMetadataArgs shapes one record's bind parameters for a
// (producer, standard) table insert: NULL rather than "" for an absent
// signature or supersede key, record_length derived from the stored bytes, and
// created_at defaulted to the record timestamp. Shared with the multi-row
// batch insert (flatsql_batch_writes.go) so one row can never be shaped one
// way here and another way there.
func schemaMetadataArgs(rec storedRecord) []any {
	var signatureHex any
	if len(rec.signature) > 0 {
		signatureHex = hex.EncodeToString(rec.signature)
	}
	if rec.createdAt <= 0 {
		rec.createdAt = rec.timestamp
	}
	var supersedeKey any
	if rec.supersedeKey != "" {
		supersedeKey = rec.supersedeKey
	}
	return []any{rec.cid, rec.peerID, rec.timestamp, rec.data, int64(len(rec.data)), signatureHex, supersedeKey, rec.createdAt}
}

func insertSchemaMetadataWithMode(exec sqlExecer, tableName string, rec storedRecord, ignoreDuplicates bool) (int64, error) {
	insertMode := "INSERT INTO"
	if ignoreDuplicates {
		insertMode = "INSERT OR IGNORE INTO"
	}
	insertSQL := fmt.Sprintf(`
		%s %s (
			cid, peer_id, timestamp, data, record_length, signature_hex, supersede_key, created_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, insertMode, tableName)
	result, err := exec.Exec(insertSQL, schemaMetadataArgs(rec)...)
	if err != nil {
		return 0, err
	}
	rowID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	return rowID, nil
}

// storableRecordBytes returns the bytes a record is stored as: sealed for a
// standard with registered `(encrypted)` fields (field_encryption.go), the
// caller's bytes unchanged otherwise. CID and indexing always happen against
// the plaintext; only the stored bytes become ciphertext. encfield's registry
// is keyed by the table name, not the ".fbs" schema name.
func (s *FlatSQLStore) storableRecordBytes(schemaName string, data []byte) ([]byte, error) {
	tableName, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return nil, err
	}
	if !encfield.HasEncryptedFields(tableName) {
		return data, nil
	}
	sealed, err := s.sealRecordFields(tableName, data)
	if err != nil {
		return nil, fmt.Errorf("seal %s record: %w", schemaName, err)
	}
	return sealed, nil
}

// openStoredRecordBytes is the mirror of storableRecordBytes for a read:
// encfield.IsSealed is a cheap magic check, false (and a no-op) for every
// record that was never sealed.
func (s *FlatSQLStore) openStoredRecordBytes(schemaName string, data []byte) ([]byte, error) {
	if !encfield.IsSealed(data) {
		return data, nil
	}
	tableName, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return nil, err
	}
	opened, err := s.openRecordFields(tableName, data)
	if err != nil {
		return nil, fmt.Errorf("decrypt %s record: %w", schemaName, err)
	}
	return opened, nil
}

// hydrateRecordData fills a Record's bytes and signature from a scanned row.
func (s *FlatSQLStore) hydrateRecordData(record *Record, schemaName string, data []byte, signatureHex sql.NullString) error {
	opened, err := s.openStoredRecordBytes(schemaName, data)
	if err != nil {
		return err
	}
	record.Data = opened
	record.RecordLength = int64(len(data))
	if signatureHex.Valid && strings.TrimSpace(signatureHex.String) != "" {
		signature, err := hex.DecodeString(strings.TrimSpace(signatureHex.String))
		if err != nil {
			return fmt.Errorf("decode signature_hex for %s: %w", record.CID, err)
		}
		record.Signature = signature
	}
	return nil
}

func (s *FlatSQLStore) ensureSourceSummaryForSchema(schemaName, tableName string) error {
	_ = tableName
	var existing int
	if err := s.db.QueryRow(`
		SELECT COUNT(*)
		FROM sdn_record_source_summary
		WHERE schema_name = ?
	`, schemaName).Scan(&existing); err != nil {
		return fmt.Errorf("inspect source summary for %s: %w", schemaName, err)
	}
	if existing > 0 {
		return nil
	}

	return nil
}

func (s *FlatSQLStore) rebuildSourceSummariesFromDurableState() error {
	return s.rebuildSourceSummaryScope("")
}

// sourceSummaryLane is one (schema, provider, source, batch) lane of the derived
// sdn_record_source_summary cache — the unit the rebuild works in.
type sourceSummaryLane struct {
	SchemaName string
	ProviderID string
	SourceName string
	BatchID    string
}

// sourceSummaryRebuildChunk bounds how many source-tag rows ONE engine statement
// may aggregate during a rebuild.
//
// The engine is a single-threaded WASM SQLite behind one lock, so a statement's
// duration is a stop-the-world for every other reader on the box — which is why
// the bound exists at all and why it is expressed in ROWS, not in time. Measured
// on host-01 (2026-08-09): the rebuild join costs ~87 µs per tag row
// (MPE 261,419 rows -> 22.7 s, RFB 10,587 rows -> 0.63 s — the same rate, and
// RFB has ONE backing table while MPE also has one, which is how we know the
// union read source was never the driver). 8,000 rows is therefore ~0.7 s of
// engine hold: under the 1 s acceptance bar with room for a loaded box.
var sourceSummaryRebuildChunk = 8000

// sourceSummaryLaneScanLimit is a runaway guard on the loose index scan below.
// A node holds tens of lanes; tens of thousands means the seek stopped
// advancing, and looping forever inside the engine lock is the failure mode this
// whole task exists to remove.
const sourceSummaryLaneScanLimit = 100000

// rebuildSourceSummaryScope recomputes the derived source summary for one schema
// ("" = every schema, the post-hydration boot path) from the durable source-tag
// and record tables.
//
// THE ONE RULE, learned the hard way on host-01 across three measured rolls:
// **no statement here may touch sdn_record_source_tags at a scale proportional
// to its rows.** That table is 1.8 M rows, and in this engine one pass over it
// costs about 30 s of held engine lock however it is expressed. Three separate
// shapes were tried and each cost the same 25-35 s:
//
//	SELECT DISTINCT schema_name                     29.7 s, n=1,  max 29.75 s
//	a row-value "loose index seek" per lane         28.7 s, n=42, max  2.75 s
//	a per-lane COUNT(*) fingerprint                 33.3 s, n=44, max  2.85 s
//
// The middle one is the instructive failure: EXPLAIN QUERY PLAN calls it a SEARCH
// on a covering index, through the engine AND through native sqlite3 on the same
// live file, and native sqlite3 answers it in 1 ms — but the engine charged
// 500-680 ms per seek and the cost tracked DATABASE SIZE (9 ms at a 90 MB
// fixture, 680 ms at the live 2.8 GB) rather than lane count. A plan is not a
// measurement here.
//
// So the rebuild does not look for what changed. It is TOLD:
//
//   - the tag writers (insertNewSourceTagsTx, upsertSourceTagsTx, and the
//     window writer chunkWriteBuffer.writeSourceTags) all increment the
//     summary, so a lane they touch is already correct;
//   - supersede / GC / batch reconciliation call the SCOPED form with the schema
//     they just mutated, and every lane of that schema is rebuilt unconditionally
//     because those verbs delete rows and the summary cannot be trusted for them.
//
// No boot step calls this: the summaries are durable rows in the same database
// as the records they describe.
//
// Pruning needs no separate pass: rebuildSourceSummaryLane DELETEs the lane's
// summary rows before inserting what it found, so a lane whose tags are gone is
// removed by the rebuild that visits it.
func (s *FlatSQLStore) rebuildSourceSummaryScope(schemaName string) error {
	lanes, err := s.sourceSummaryLanes(schemaName)
	if err != nil {
		return err
	}
	for _, lane := range lanes {
		if err := s.rebuildSourceSummaryLane(lane); err != nil {
			return err
		}
	}
	return nil
}

// sourceSummaryLanes enumerates the (schema, provider, source, batch) lanes the
// rebuild must consider. schemaName == "" means every schema. Callers hold s.mu.
//
// WHY IT DOES NOT SCAN THE TAG TABLE (measured on host-01, 2026-08-09, live):
//
// The first version of this walked sdn_record_source_tags with a row-value loose
// index scan. `EXPLAIN QUERY PLAN` says that is a seek — both through the engine
// and through native sqlite3 on the live file — and native sqlite3 answers it in
// **1 ms**. The ENGINE charged **500-680 ms per seek, 42 of them, 28.7 s total,
// every one with waited = 0 s**, and the per-seek cost tracked DATABASE SIZE
// (9 ms against the 90 MB prod-scale fixture, 680 ms against the live 2.8 GB)
// rather than lane count. Whatever the plan says, the engine is not descending.
// The rule this leaves behind: **no rebuild statement may touch the tag table at
// a scale proportional to its rows** — not a DISTINCT, not a COUNT(*), and not a
// "seek" whose cost says otherwise.
//
// So the lanes are read from the two places that already know them:
//
//  1. sdn_record_source_summary — tens of rows, and every lane written by an
//     ingest path is there already, because insertNewSourceTagsTx,
//     upsertSourceTagsTx and the batch window's writeSourceTags all increment
//     the summary (the window folds its records into one increment;
//     incrementSourceSummaryBy).
//
// That is exact for every writer in the tree. The fallback below covers the
// one case it cannot: a summary that is EMPTY for the scope (a first boot, a
// dropped/migrated summary table) is not evidence of an empty store, so that
// path still pays the loose scan once — which is the right trade, because it
// happens when there is nothing to skip anyway.
func (s *FlatSQLStore) sourceSummaryLanes(schemaName string) ([]sourceSummaryLane, error) {
	seen := make(map[sourceSummaryLane]struct{}, 64)
	lanes := make([]sourceSummaryLane, 0, 64)
	add := func(lane sourceSummaryLane) {
		if schemaName != "" && lane.SchemaName != schemaName {
			return
		}
		if _, ok := seen[lane]; ok {
			return
		}
		seen[lane] = struct{}{}
		lanes = append(lanes, lane)
	}

	// Every lane the summary already names is rebuilt: a SCOPED call comes
	// from supersede / GC / batch reconciliation, which have just DELETED rows,
	// and the global call is an operator's explicit maintenance request. This
	// reads the summary (tens of rows), not the tag table.
	summaryLanes := 0
	laneSQL := `SELECT DISTINCT schema_name, provider_id, source_name, batch_id FROM sdn_record_source_summary`
	var laneArgs []any
	if schemaName != "" {
		laneSQL += ` WHERE schema_name = ?`
		laneArgs = append(laneArgs, schemaName)
	}
	rows, err := s.db.Query(laneSQL, laneArgs...)
	if err != nil {
		return nil, fmt.Errorf("enumerate summary lanes: %w", err)
	}
	for rows.Next() {
		var lane sourceSummaryLane
		if err := rows.Scan(&lane.SchemaName, &lane.ProviderID, &lane.SourceName, &lane.BatchID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan summary lane: %w", err)
		}
		summaryLanes++
		add(lane)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("iterate summary lanes: %w", err)
	}

	if summaryLanes == 0 {
		// Nothing derived yet for this scope: a first boot, or a summary table
		// that was dropped and recreated. An empty summary is not evidence of an
		// empty store, so this is the one path that still pays the loose scan —
		// on a store where there is nothing to skip anyway.
		scanned, err := s.sourceSummaryLanesFromTags(schemaName)
		if err != nil {
			return nil, err
		}
		for _, lane := range scanned {
			add(lane)
		}
	}
	return lanes, nil
}

// sourceSummaryLanesFromTags is the COLD discovery path: a loose index scan over
// idx_sdn_record_source_tags_lookup. It is only reached when the summary holds
// nothing for the scope, because on the live node each of its seeks costs
// 500-680 ms in the engine — see sourceSummaryLanes for the measurement and why
// the warm path must not use it. Callers hold s.mu.
func (s *FlatSQLStore) sourceSummaryLanesFromTags(schemaName string) ([]sourceSummaryLane, error) {
	// STEP is a row-value comparison against the index's own column order, and
	// that is what makes it a SEEK: verified with EXPLAIN QUERY PLAN against the
	// live 2.8 GB control database, which answers
	//   SEARCH … USING COVERING INDEX idx_sdn_record_source_tags_lookup
	//          ((schema_name,provider_id,source_name,batch_id)>(?,?,?,?))
	// batch_id/provider_id/source_name are all `TEXT NOT NULL DEFAULT ''`
	// (sourceTagsTableSQL), so no COALESCE is needed — and adding one here would
	// make the comparison non-indexable, which is the whole point.
	//
	// The per-schema case deliberately does NOT add `schema_name = ?` to STEP.
	// The same EXPLAIN says that with the equality present SQLite drops the
	// row-value bound to a filter and plans
	// `SEARCH … idx_sdn_record_source_tags_batch_cid (schema_name=?)` — a forward
	// scan from the schema's first row on every step, which is O(rows) again.
	// The schema is bounded in Go instead, by stopping at the first lane that
	// belongs to a different one; the index orders by schema_name first, so a
	// change of schema means the schema is finished.
	const first = `
		SELECT schema_name, provider_id, source_name, batch_id
		FROM sdn_record_source_tags
		ORDER BY schema_name, provider_id, source_name, batch_id
		LIMIT 1`
	const firstOfSchema = `
		SELECT schema_name, provider_id, source_name, batch_id
		FROM sdn_record_source_tags
		WHERE schema_name = ?
		ORDER BY schema_name, provider_id, source_name, batch_id
		LIMIT 1`
	const step = `
		SELECT schema_name, provider_id, source_name, batch_id
		FROM sdn_record_source_tags
		WHERE (schema_name, provider_id, source_name, batch_id) > (?, ?, ?, ?)
		ORDER BY schema_name, provider_id, source_name, batch_id
		LIMIT 1`

	lanes := make([]sourceSummaryLane, 0, 64)
	var cursor sourceSummaryLane
	for i := 0; i < sourceSummaryLaneScanLimit; i++ {
		var next sourceSummaryLane
		var err error
		switch {
		case i > 0:
			// Seeking PAST the lane just found is what skips its rows — a lane of
			// 169,865 tag rows costs one index descent, not 169,865 steps.
			err = s.db.QueryRow(step, cursor.SchemaName, cursor.ProviderID, cursor.SourceName, cursor.BatchID).
				Scan(&next.SchemaName, &next.ProviderID, &next.SourceName, &next.BatchID)
		case schemaName == "":
			err = s.db.QueryRow(first).
				Scan(&next.SchemaName, &next.ProviderID, &next.SourceName, &next.BatchID)
		default:
			// Not `> (schemaName,'','','')`: a lane whose provider/source/batch are
			// all empty strings is a legal row and that bound would skip it.
			err = s.db.QueryRow(firstOfSchema, schemaName).
				Scan(&next.SchemaName, &next.ProviderID, &next.SourceName, &next.BatchID)
		}
		if errors.Is(err, sql.ErrNoRows) {
			return lanes, nil
		}
		if err != nil {
			return nil, fmt.Errorf("enumerate source-summary lanes: %w", err)
		}
		if schemaName != "" && next.SchemaName != schemaName {
			return lanes, nil
		}
		lanes = append(lanes, next)
		cursor = next
	}
	return nil, fmt.Errorf("enumerate source-summary lanes: seek did not terminate after %d lanes", sourceSummaryLaneScanLimit)
}

func (s *FlatSQLStore) deleteSourceSummaryLane(lane sourceSummaryLane) error {
	if _, err := s.db.Exec(flatsqldrv.WithoutJournal(`
		DELETE FROM sdn_record_source_summary
		WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id = ?
	`), lane.SchemaName, lane.ProviderID, lane.SourceName, lane.BatchID); err != nil {
		return fmt.Errorf("clear source summary for %s/%s/%s/%s: %w",
			lane.SchemaName, lane.ProviderID, lane.SourceName, lane.BatchID, err)
	}
	return nil
}

// sourceSummaryProducerAgg is one (producer_peer_id, producer_public_key) row of
// a lane's summary, accumulated across chunks.
type sourceSummaryProducerAgg struct {
	RecordCount int64
	TotalBytes  int64
	MaxRowID    int64
	FirstSeen   int64
}

type sourceSummaryProducerKey struct {
	PeerID    string
	PublicKey string
}

// rebuildSourceSummaryLane recomputes ONE lane in bounded cid slices, releasing
// the engine lock between slices so queued readers interleave, then replaces the
// lane's summary rows in two small statements. Callers hold s.mu.
func (s *FlatSQLStore) rebuildSourceSummaryLane(lane sourceSummaryLane) error {
	// The cid bounds are pushed into EVERY union branch (see
	// recordReadSourceFiltered): without that a multi-producer standard plans as
	// a full scan of every backing table per slice. Numbered parameters are
	// mandatory there because the predicate is repeated once per branch.
	//
	// The join is a LEFT JOIN where the single-statement rebuild used an INNER
	// one, and that is REQUIRED, not incidental. An INNER JOIN silently drops a
	// tag whose record row is missing, so the lane's record_count would be
	// PERMANENTLY below its tag count — and the fingerprint in
	// sourceSummaryLaneNeedsRebuild, which compares exactly those two numbers,
	// would never converge and would rebuild that lane on every boot forever.
	// Reporting the row at zero bytes is both the honest answer for
	// /api/v1/stats and the one that lets the skip work.
	readSource, err := s.recordReadSourceFiltered(lane.SchemaName, "cid > ?1 AND (?2 = '' OR cid <= ?2)")
	if err != nil {
		return fmt.Errorf("read source for %s: %w", lane.SchemaName, err)
	}
	boundarySQL := fmt.Sprintf(`
		SELECT cid FROM sdn_record_source_tags
		WHERE schema_name = ?1 AND provider_id = ?2 AND source_name = ?3 AND batch_id = ?4
		  AND cid > ?5
		ORDER BY cid
		LIMIT 1 OFFSET %d`, sourceSummaryRebuildChunk-1)
	aggSQL := fmt.Sprintf(`
		SELECT t.producer_peer_id, t.producer_public_key,
		       COUNT(*),
		       COALESCE(SUM(records.record_length), 0),
		       COALESCE(MAX(records.rowid), 0),
		       COALESCE(MIN(t.created_at), 0)
		FROM (
			SELECT cid, producer_peer_id, producer_public_key, created_at
			FROM sdn_record_source_tags
			WHERE schema_name = ?3 AND provider_id = ?4 AND source_name = ?5 AND batch_id = ?6
			  AND cid > ?1 AND (?2 = '' OR cid <= ?2)
		) t
		LEFT JOIN %s records ON records.cid = t.cid
		GROUP BY t.producer_peer_id, t.producer_public_key`, readSource)

	aggs := make(map[sourceSummaryProducerKey]*sourceSummaryProducerAgg, 4)
	lastCID := ""
	for slice := 0; ; slice++ {
		if slice > sourceSummaryLaneScanLimit {
			return fmt.Errorf("rebuild source summary for %s/%s/%s/%s: slice cursor did not advance",
				lane.SchemaName, lane.ProviderID, lane.SourceName, lane.BatchID)
		}
		// The slice is a CLOSED cid range so a cid carried by several producers
		// can never straddle the boundary and be dropped: the boundary is a cid
		// value, and every row with that cid is inside the slice.
		boundary := ""
		if err := s.db.QueryRow(boundarySQL,
			lane.SchemaName, lane.ProviderID, lane.SourceName, lane.BatchID, lastCID,
		).Scan(&boundary); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("slice source summary for %s/%s/%s/%s: %w",
				lane.SchemaName, lane.ProviderID, lane.SourceName, lane.BatchID, err)
		}

		rows, err := s.db.Query(aggSQL, lastCID, boundary,
			lane.SchemaName, lane.ProviderID, lane.SourceName, lane.BatchID)
		if err != nil {
			return fmt.Errorf("rebuild source summary for %s/%s/%s/%s: %w",
				lane.SchemaName, lane.ProviderID, lane.SourceName, lane.BatchID, err)
		}
		sliceRows := int64(0)
		for rows.Next() {
			var key sourceSummaryProducerKey
			var got sourceSummaryProducerAgg
			if err := rows.Scan(&key.PeerID, &key.PublicKey,
				&got.RecordCount, &got.TotalBytes, &got.MaxRowID, &got.FirstSeen); err != nil {
				rows.Close()
				return fmt.Errorf("scan source summary slice: %w", err)
			}
			sliceRows += got.RecordCount
			acc := aggs[key]
			if acc == nil {
				acc = &sourceSummaryProducerAgg{}
				aggs[key] = acc
			}
			acc.RecordCount += got.RecordCount
			acc.TotalBytes += got.TotalBytes
			if got.MaxRowID > acc.MaxRowID {
				acc.MaxRowID = got.MaxRowID
			}
			if got.FirstSeen > 0 && (acc.FirstSeen == 0 || got.FirstSeen < acc.FirstSeen) {
				acc.FirstSeen = got.FirstSeen
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return fmt.Errorf("iterate source summary slice: %w", err)
		}
		if boundary == "" || sliceRows == 0 {
			break
		}
		lastCID = boundary
	}

	if err := s.deleteSourceSummaryLane(lane); err != nil {
		return err
	}
	if len(aggs) == 0 {
		return nil
	}
	values := make([]string, 0, len(aggs))
	args := make([]any, 0, len(aggs)*10)
	for key, acc := range aggs {
		values = append(values, "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%s', 'now'))")
		firstSeen := acc.FirstSeen
		if firstSeen == 0 {
			firstSeen = time.Now().Unix()
		}
		args = append(args,
			lane.SchemaName, lane.ProviderID, lane.SourceName, lane.BatchID,
			key.PeerID, key.PublicKey,
			acc.RecordCount, acc.TotalBytes, acc.MaxRowID, firstSeen)
	}
	if _, err := s.db.Exec(flatsqldrv.WithoutJournal(fmt.Sprintf(`
		INSERT INTO sdn_record_source_summary (
			schema_name, provider_id, source_name, batch_id, producer_peer_id,
			producer_public_key, record_count, total_bytes, max_rowid, first_seen, updated_at
		) VALUES %s
	`, strings.Join(values, ", "))), args...); err != nil {
		return fmt.Errorf("rebuild source summary for %s/%s/%s/%s: %w",
			lane.SchemaName, lane.ProviderID, lane.SourceName, lane.BatchID, err)
	}
	return nil
}

func (s *FlatSQLStore) rebuildSourceSummaryForSchema(schemaName, tableName string) error {
	_ = tableName
	return s.rebuildSourceSummaryScope(schemaName)
}

// rebuildSourceSummaryForSourceBatch recomputes ONE lane unconditionally — the
// callers that reach it (batch reconciliation, RefreshSourceBatchSummary) have
// just changed that lane, so the fingerprint check is skipped and the chunked
// rebuild runs directly.
func (s *FlatSQLStore) rebuildSourceSummaryForSourceBatch(schemaName, tableName, providerID, sourceName, batchID string) error {
	_ = tableName
	return s.rebuildSourceSummaryLane(sourceSummaryLane{
		SchemaName: schemaName,
		ProviderID: providerID,
		SourceName: sourceName,
		BatchID:    batchID,
	})
}

// RefreshSourceBatchSummary recomputes one provider/source/batch summary from
// source tags and record metadata without scanning unrelated batches.
func (s *FlatSQLStore) RefreshSourceBatchSummary(schemaName, providerID, sourceName, batchID string) error {
	schemaName = strings.TrimSpace(schemaName)
	providerID = strings.TrimSpace(providerID)
	sourceName = strings.TrimSpace(sourceName)
	batchID = strings.TrimSpace(batchID)
	if schemaName == "" {
		return errors.New("schema name is required")
	}
	if providerID == "" {
		return errors.New("provider id is required")
	}
	if sourceName == "" {
		return errors.New("source name is required")
	}
	if batchID == "" {
		return errors.New("batch id is required")
	}
	tableName, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return fmt.Errorf("invalid schema name: %w", err)
	}

	defer s.lockWrite("RefreshSourceBatchSummary")()
	return s.rebuildSourceSummaryForSourceBatch(schemaName, tableName, providerID, sourceName, batchID)
}

func incrementSourceSummary(tx sqlExecer, schemaName string, tags SourceTags, recordBytes, recordRowID int64) error {
	return incrementSourceSummaryBy(tx, schemaName, tags, 1, recordBytes, recordRowID)
}

// incrementSourceSummaryBy folds a whole window of records into ONE summary
// statement. Every term aggregates exactly the way the per-record form
// accumulated it — count adds, bytes add, max_rowid takes the maximum,
// first_seen is kept once set — so a window of n records leaves the summary on
// the values n separate increments would have reached. That equivalence is the
// whole licence for batching it: the summary is what the dashboard and the
// datasync high-water mark read.
func incrementSourceSummaryBy(tx sqlExecer, schemaName string, tags SourceTags, recordCount, recordBytes, recordRowID int64) error {
	if recordCount <= 0 {
		return nil
	}
	tags = normalizeSourceTags(tags)
	_, err := tx.Exec(flatsqldrv.WithoutJournal(`
		INSERT INTO sdn_record_source_summary (
			schema_name, provider_id, source_name, batch_id, producer_peer_id,
			producer_public_key, record_count, total_bytes, max_rowid, first_seen, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, strftime('%s', 'now'), strftime('%s', 'now'))
		ON CONFLICT(schema_name, provider_id, source_name, batch_id, producer_peer_id, producer_public_key) DO UPDATE SET
			record_count = record_count + excluded.record_count,
			total_bytes = total_bytes + excluded.total_bytes,
			max_rowid = MAX(max_rowid, excluded.max_rowid),
			first_seen = CASE WHEN first_seen > 0 THEN first_seen ELSE excluded.first_seen END,
			updated_at = excluded.updated_at
	`), schemaName, tags.ProviderID, tags.SourceName, tags.BatchID, tags.ProducerPeerID, tags.ProducerPublicKey, recordCount, recordBytes, recordRowID)
	if err != nil {
		return fmt.Errorf("increment source summary: %w", err)
	}
	return nil
}

func decrementSourceSummary(tx sqlExecer, schemaName string, tags SourceTags, recordBytes, recordRowID int64) error {
	tags = normalizeSourceTags(tags)
	_, err := tx.Exec(flatsqldrv.WithoutJournal(`
		UPDATE sdn_record_source_summary
		SET
			record_count = CASE WHEN record_count > 0 THEN record_count - 1 ELSE 0 END,
			total_bytes = CASE WHEN total_bytes > ? THEN total_bytes - ? ELSE 0 END,
			max_rowid = CASE WHEN max_rowid = ? THEN 0 ELSE max_rowid END,
			updated_at = strftime('%s', 'now')
		WHERE schema_name = ?
		  AND provider_id = ?
		  AND source_name = ?
		  AND batch_id = ?
		  AND producer_peer_id = ?
		  AND producer_public_key = ?
	`), recordBytes, recordBytes, recordRowID, schemaName, tags.ProviderID, tags.SourceName, tags.BatchID, tags.ProducerPeerID, tags.ProducerPublicKey)
	if err != nil {
		return fmt.Errorf("decrement source summary: %w", err)
	}
	if _, err := tx.Exec(flatsqldrv.WithoutJournal(`
		DELETE FROM sdn_record_source_summary
		WHERE schema_name = ?
		  AND provider_id = ?
		  AND source_name = ?
		  AND batch_id = ?
		  AND producer_peer_id = ?
		  AND producer_public_key = ?
		  AND record_count <= 0
	`), schemaName, tags.ProviderID, tags.SourceName, tags.BatchID, tags.ProducerPeerID, tags.ProducerPublicKey); err != nil {
		return fmt.Errorf("delete empty source summary: %w", err)
	}
	return nil
}

func sameSourceSummaryKey(left, right SourceTags) bool {
	left = normalizeSourceTags(left)
	right = normalizeSourceTags(right)
	return strings.TrimSpace(left.ProviderID) == strings.TrimSpace(right.ProviderID) &&
		strings.TrimSpace(left.SourceName) == strings.TrimSpace(right.SourceName) &&
		strings.TrimSpace(left.BatchID) == strings.TrimSpace(right.BatchID) &&
		strings.TrimSpace(left.ProducerPeerID) == strings.TrimSpace(right.ProducerPeerID) &&
		strings.TrimSpace(left.ProducerPublicKey) == strings.TrimSpace(right.ProducerPublicKey)
}

func normalizeSourceTags(tags SourceTags) SourceTags {
	tags.ProviderID = strings.TrimSpace(tags.ProviderID)
	tags.SourceName = strings.TrimSpace(tags.SourceName)
	tags.SourceURL = strings.TrimSpace(tags.SourceURL)
	tags.BatchID = strings.TrimSpace(tags.BatchID)
	tags.ContentKeyID = strings.TrimSpace(tags.ContentKeyID)
	tags.ProducerPeerID = strings.TrimSpace(tags.ProducerPeerID)
	tags.ProducerPublicKey = strings.TrimSpace(tags.ProducerPublicKey)
	tags.License = strings.TrimSpace(tags.License)
	tags.LicenseURL = strings.TrimSpace(tags.LicenseURL)
	tags.Citation = strings.TrimSpace(tags.Citation)
	if tags.ProducerPeerID == "" {
		tags.ProducerPeerID = tags.ProviderID
	}
	if tags.ProducerPublicKey == "" {
		tags.ProducerPublicKey = tags.ContentKeyID
	}
	return tags
}

// Store stores validated data in the appropriate table.
func (s *FlatSQLStore) Store(schemaName string, data []byte, peerID string, signature []byte) (string, error) {
	return s.storeOne(schemaName, data, peerID, signature, nil)
}

// storeOne is the single-record write path. tags (optional) only inform the
// engine-cache partition here; provenance rows are written by the callers'
// UpsertSourceTags.
func (s *FlatSQLStore) storeOne(schemaName string, data []byte, peerID string, signature []byte, tags *SourceTags) (string, error) {
	if err := s.requireWritable("store record"); err != nil {
		return "", err
	}
	defer s.lockWrite("storeOne")()

	if _, err := sds.SchemaNameToTable(schemaName); err != nil {
		return "", fmt.Errorf("invalid schema name: %w", err)
	}

	// Compute CID (content identifier). Dedupe via the global record index —
	// the complete catalog of every stored record (bare rows are inserted even
	// on field-extraction failure).
	cid := computeCID(data)
	var existing int
	if err := s.db.QueryRow(`SELECT 1 FROM sdn_record_index WHERE schema_name = ? AND cid = ?`, schemaName, cid).Scan(&existing); err == nil {
		// Repeat CID (possibly from a different producer): still record it in
		// the producer's (producer, standard) table, and let a $CAT copy
		// supersede this (producer, source) lane's previous record for the
		// object.
		superseded := s.mirrorRoutedRecordFromExisting(s.db, schemaName, cid, peerID, signature, sourceNameOf(tags))
		if len(superseded) > 0 && s.engineRoutesSchema(schemaName) {
			if _, err := s.tombstoneEngineRecordsLocked(schemaName, superseded, nil); err != nil {
				return "", err
			}
		}
		return cid, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("failed to check existing record: %w", err)
	}

	// WS7.3d routed-only writes: the metadata row lands in the producer's
	// (producer, standard) table — v1 stores never write the legacy
	// per-standard tables. Content-addressed records are immutable; the index
	// dedupe above prevents a different peer from overwriting the original
	// author's attribution.
	now := time.Now().Unix()
	stored, err := s.storableRecordBytes(schemaName, data)
	if err != nil {
		return "", err
	}
	routedTable, err := s.ensureProducerStandardTable(routedProducerID(peerID), schemaName)
	if err != nil {
		return "", fmt.Errorf("ensure (producer, standard) table: %w", err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", fmt.Errorf("begin store: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	keys := recordSupersedeKeys(schemaName, data, sourceNameOf(tags))
	superseded, err := s.supersedeInProducerTableTx(tx, schemaName, routedTable, keys, cid)
	if err != nil {
		return "", err
	}
	if err := insertSchemaMetadata(tx, routedTable, storedRecord{cid: cid, peerID: peerID, timestamp: now, data: stored, signature: signature, createdAt: now, supersedeKey: keys.stored}); err != nil {
		return "", fmt.Errorf("failed to store data: %w", err)
	}
	if err := upsertRecordIndexExec(tx, schemaName, cid, now, data, s.fullTextState(schemaName)); err != nil {
		// Do not fail writes if index extraction fails for a record.
		log.Warnf("Failed to index %s record %s: %v", schemaName, cid[:16]+"...", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit store: %w", err)
	}
	committed = true

	// Engine-cache mirror (same contract as storeBatch): live ingest paths
	// write per record, and the vtab hot window must reflect them without
	// waiting for a boot rebuild. Superseded records leave the window first.
	if s.engineRoutesSchema(schemaName) {
		if _, err := s.tombstoneEngineRecordsLocked(schemaName, superseded, nil); err != nil {
			return "", err
		}
		if _, err := s.ingestEngineRecords(schemaName, []engineIngest{{cid: cid, data: data, source: engineSourceName(tags)}}, nil); err != nil {
			return "", err
		}
	}

	log.Debugf("Stored %s record with CID: %s", schemaName, cid[:16]+"...")
	return cid, nil
}

// SourceTags records source provenance needed for provider/batch-aware queries.
//
// License/LicenseURL/Citation/ShareAlike are the batch's licence terms as
// declared by the ingesting writer (the wasm parse node's ingest metadata).
// They are carriage only: the per-record sdn_record_source_tags rows and the
// record-catalog journal frames are unchanged, and the licence is persisted
// once per source batch (see SourceBatchLicense). The json tags keep them out
// of the deterministic dataset export index when empty, so an unlicensed
// export stays byte-identical to what this node published before.
type SourceTags struct {
	ProviderID        string
	SourceName        string
	SourceURL         string
	BatchID           string
	ContentKeyID      string
	ProducerPeerID    string
	ProducerPublicKey string
	License           string `json:"License,omitempty"`
	LicenseURL        string `json:"LicenseURL,omitempty"`
	Citation          string `json:"Citation,omitempty"`
	ShareAlike        bool   `json:"ShareAlike,omitempty"`
}

// SourceTagQuery filters stored records by source tags.
type SourceTagQuery struct {
	SchemaName string
	ProviderID string
	SourceName string
	BatchID    string
	Limit      int
}

// SourceBatchReconcileResult summarizes a source-batch reconciliation.
type SourceBatchReconcileResult struct {
	SchemaName string `json:"schemaName"`
	ProviderID string `json:"providerId"`
	SourceName string `json:"sourceName"`
	KeepBatch  string `json:"keepBatch"`
	Apply      bool   `json:"apply"`
	Matched    int64  `json:"matched"`
	Deleted    int64  `json:"deleted"`
}

// SourceBatchDuplicateReconcileResult summarizes duplicate logical records
// removed from one source batch.
type SourceBatchDuplicateReconcileResult struct {
	SchemaName string `json:"schemaName"`
	ProviderID string `json:"providerId"`
	SourceName string `json:"sourceName"`
	BatchID    string `json:"batchId"`
	Apply      bool   `json:"apply"`
	Matched    int64  `json:"matched"`
	Deleted    int64  `json:"deleted"`
}

// DataSummary describes local FlatSQL record volume by schema and source.
type DataSummary struct {
	TotalRecords int64
	TotalBytes   int64
	Schemas      []DataSchemaSummary
	Sources      []DataSourceSummary
}

// DataSchemaSummary is a per-schema FlatSQL record count and byte total.
type DataSchemaSummary struct {
	SchemaName string
	Count      int64
	TotalBytes int64
}

// DataSourceSummary is a per-producer/source FlatSQL record count and byte total.
type DataSourceSummary struct {
	SchemaName        string
	ProviderID        string
	SourceName        string
	BatchID           string
	ProducerPeerID    string
	ProducerPublicKey string
	Count             int64
	TotalBytes        int64
}

// SourceBatchProgress is one live pipeline-progress row per
// (schema, provider, source, batch) that has records, aggregated across
// producer identities. It carries the current record count + byte total plus
// the first/last record arrival times (per-record ingest created_at) and the
// summary's last-update time. It powers a schema-neutral anonymous progress
// surface. Unix values are 0 when unknown.
type SourceBatchProgress struct {
	SchemaName    string
	ProviderID    string
	SourceName    string
	BatchID       string
	Count         int64
	TotalBytes    int64
	FirstSeenUnix int64
	LastSeenUnix  int64
	UpdatedAtUnix int64
}

// RawRecordQuery filters raw FlatBuffer records for UI and node-to-node reads.
type RawRecordQuery struct {
	SchemaName        string
	CID               string
	ProviderID        string
	SourceName        string
	BatchID           string
	ProducerPeerID    string
	ProducerPublicKey string
	PeerID            string
	SyncFilter        string
	Search            string
	Limit             int
	Offset            int
	UseRowIDCursor    bool
	AfterRowID        int64
	MaxRowID          int64
}

// RawRecordHead summarizes a raw-record result set without hydrating any
// FlatBuffer payload bytes.
type RawRecordHead struct {
	TotalBytes             int64
	MaxRecordTimestampUnix int64
	MaxSourceUpdatedAtUnix int64
	MaxCreatedAtUnix       int64
	MaxRowID               int64
}

// RawRecordRef identifies one scan-bound record for ordered raw-byte sync.
type RawRecordRef struct {
	CID               string
	ProviderID        string
	SourceName        string
	BatchID           string
	ProducerPeerID    string
	ProducerPublicKey string
	PeerID            string
}

// IndexedRecordQuery filters materialized FlatSQL record indexes.
type IndexedRecordQuery struct {
	SchemaName          string
	Day                 string
	NoradCatID          *uint32
	EntityID            string
	ObjectType          string
	OpsStatusCode       string
	ActivePayloads      bool
	CAReadyResidentSet  bool
	From                *time.Time
	To                  *time.Time
	ProviderID          string
	SourceName          string
	BatchID             string
	Limit               int
	Offset              int
	AllowLargeResultSet bool
	OrderByCID          bool
}

// StoreWithSourceTags stores a FlatBuffer record and attaches provider/source metadata.
func (s *FlatSQLStore) StoreWithSourceTags(schemaName string, data []byte, peerID string, signature []byte, tags SourceTags) (string, error) {
	// The batch's licence is recorded BEFORE the records land: a record whose
	// licence could not be persisted must never become publishable, because
	// republishing it without its terms is the thing this carriage exists to
	// prevent. No-op when the writer declared no licence.
	if err := s.recordSourceBatchLicense(schemaName, tags); err != nil {
		return "", err
	}
	cid, err := s.storeOne(schemaName, data, peerID, signature, &tags)
	if err != nil {
		return "", err
	}
	if err := s.UpsertSourceTags(schemaName, cid, tags); err != nil {
		return "", err
	}
	return cid, nil
}

// storeWriteChunkSize bounds how many records one store-lock window (write
// RWMutex hold + control transaction + stream-appender session) processes.
// A full-catalog batch (~32K CatalogFixture GP records) under ONE lock hold
// starved every reader for the whole drain — the 2026-07-06 production
// blackout: API queries waited >11 minutes on s.mu.RLock while a dataset
// shard import held the write lock. Chunking commits each window atomically
// and releases the lock between windows so readers interleave; batch replay
// stays idempotent (CID-checked, source-batch reconcile) so a mid-batch
// failure converges on retry exactly like the per-record ingest path.
// A cold CI runner took about 1.7 seconds per 128-record window while the
// durable record-catalog append synced under this lock. Keep windows at 64
// records so one slow sync retains margin below the two-second reader budget.
const storeWriteChunkSize = 64

// storeWriteWindowBudget is the REAL invariant the chunk size exists to hold:
// one lock window must stay comfortably inside the two-second reader budget.
// The 2026-07-06 blackout was a window that ran for minutes, not a window that
// contained too many records — the record count was only ever a proxy for time.
const storeWriteWindowBudget = 2 * time.Second

// storeWriteChunkMax bounds the adaptive window. 1024 is where the measured
// gain flattens and is still ~0.7 s on hardware that can sustain it.
const storeWriteChunkMax = 1024

// adaptiveStoreChunk returns the next window size, growing while windows land
// well inside the budget and shrinking the moment one approaches it.
//
// A FIXED 64 LEFT 6x ON THE TABLE. Measured 2026-09-16 (OMM, 4 MiB, engine
// AOT): 64-record windows sustained 233 rec/s, 1024-record windows 1417 rec/s
// — the cost is the per-window commit, so sixteen times fewer commits is
// six times the throughput. But a fixed 1024 is not safe either: the same
// comment that set 64 records a cold CI runner taking 1.7 s for a 128-record
// window, which extrapolates to ~13 s at 1024 — far outside the budget the
// constant exists to protect.
//
// So target the budget instead of guessing a count. Fast hardware earns big
// windows; slow hardware keeps small ones; neither needs a human to tune it.
func adaptiveStoreChunk(current int, lastWindow time.Duration) int {
	if current <= 0 {
		return storeWriteChunkSize
	}
	switch {
	case lastWindow <= 0:
		return current
	// Grow only while it is nearly FREE to. A quarter of the budget was the
	// right threshold when a commit fsynced the rollback journal and bigger
	// windows bought 6x; under WAL they buy 6%, measured: at a 64-record
	// window WAL sustains 1152 rec/s against 1222 at 1024. Spending four
	// hundred milliseconds of reader latency for six percent is a bad trade,
	// and the 2026-07-06 blackout is what reader latency costs when it goes
	// wrong.
	//
	// Those two rates were measured under WAL+synchronous=FULL and are STALE
	// as absolutes — the store runs WAL+NORMAL since 2026-09-17 and both are
	// now higher. The heuristic is unaffected, and in fact holds harder:
	// dropping the per-commit fsync is precisely what made a bigger window
	// stop paying, so the gap between a 64- and a 1024-record window can only
	// have narrowed further. Re-measure before quoting either number.
	//
	// So the threshold is tight enough that growth only happens where windows
	// are very cheap — which keeps the TRUNCATE case (where growth really is
	// worth 6x) while leaving WAL near the small window it no longer needs to
	// leave.
	case lastWindow < storeWriteWindowBudget/16:
		if next := current * 2; next <= storeWriteChunkMax {
			return next
		}
		return storeWriteChunkMax
	// Approaching budget: halve, down to the floor that was always safe.
	case lastWindow > storeWriteWindowBudget/2:
		if next := current / 2; next >= storeWriteChunkSize {
			return next
		}
		return storeWriteChunkSize
	}
	return current
}

// StoreBatch stores FlatBuffer records in chunked store-lock windows
// (storeWriteChunkSize records per lock hold + transaction) without
// attaching per-record source tags.
func (s *FlatSQLStore) StoreBatch(schemaName string, records [][]byte, peerID string, signature []byte) (int, error) {
	return s.storeBatch(schemaName, records, peerID, signature, nil)
}

// StoreBatchWithSourceTags stores FlatBuffer records in chunked store-lock
// windows (storeWriteChunkSize records per lock hold + transaction).
func (s *FlatSQLStore) StoreBatchWithSourceTags(schemaName string, records [][]byte, peerID string, signature []byte, tags SourceTags) (int, error) {
	// See StoreWithSourceTags: licence first, then records.
	if err := s.recordSourceBatchLicense(schemaName, tags); err != nil {
		return 0, err
	}
	return s.storeBatch(schemaName, records, peerID, signature, &tags)
}

func (s *FlatSQLStore) storeBatch(schemaName string, records [][]byte, peerID string, signature []byte, tags *SourceTags) (int, error) {
	if err := s.requireWritable("store batch"); err != nil {
		return 0, err
	}
	if len(records) == 0 {
		return 0, nil
	}
	if _, err := sds.SchemaNameToTable(schemaName); err != nil {
		return 0, fmt.Errorf("invalid schema name: %w", err)
	}

	inserted := 0
	// Window size adapts to how long the last window actually took; see
	// adaptiveStoreChunk. Starts at the always-safe floor so the first window
	// on unknown hardware behaves exactly as it always did.
	window := storeWriteChunkSize
	var lastWindow time.Duration
	for start := 0; start < len(records); {
		window = adaptiveStoreChunk(window, lastWindow)
		end := start + window
		if end > len(records) {
			end = len(records)
		}
		began := time.Now()
		n, err := s.storeBatchChunk(schemaName, records[start:end], peerID, signature, tags)
		lastWindow = time.Since(began)
		inserted += n
		if err != nil {
			return inserted, err
		}
		start = end
	}
	return inserted, nil
}

// storeBatchChunk stores one chunk under one store lock and one control
// transaction (the pre-chunking storeBatch body).
func (s *FlatSQLStore) storeBatchChunk(schemaName string, records [][]byte, peerID string, signature []byte, tags *SourceTags) (int, error) {
	defer s.lockWrite("storeBatchChunk")()

	// WS7.3d routed-only writes: pre-create the (producer, standard) table
	// outside the batch transaction (no DDL inside the tx). This is the ONLY
	// table batch rows land in — v1 stores never write the legacy tables.
	routedTable, err := s.ensureProducerStandardTable(routedProducerID(peerID), schemaName)
	if err != nil {
		return 0, fmt.Errorf("ensure (producer, standard) table: %w", err)
	}
	// The source-tags path reads record rowid/bytes back through the union
	// read source (computed after the routed table exists).
	// FILTERED read source: upsertSourceTagsTx looks the record up BY CID once
	// per record, inside the ingest write lock. See recordReadSourceFiltered.
	readSource, err := s.recordReadSourceFiltered(schemaName, "cid = ?1")
	if err != nil {
		return 0, fmt.Errorf("record read source: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin batch store: %w", err)
	}
	committed := false
	// Arena rows the engine mirror added inside this transaction. The mirror
	// runs BEFORE the commit so its ledger rows land in the same fsync, which
	// means a transaction that does not commit leaves those rows in the arena
	// with no ledger row and no control row — records the engine would keep
	// SERVING until the next warm open reconciled them away. Retiring them here
	// covers every path out of this function, including a COMMIT that fails
	// after the mirror succeeded (ingestEngineBatchLocked's own failure path
	// cannot see that one). A hard crash in between remains
	// reconcileEngineResidencyLocked's job.
	var arenaRows []engineResidencyRow
	routedEngineBinding, _ := s.engineRoutedSchemaFor(schemaName)
	defer func() {
		if committed {
			return
		}
		_ = tx.Rollback()
		if len(arenaRows) > 0 {
			if _, err := s.tombstoneResidencyRowsLocked(schemaName, routedEngineBinding.Table, arenaRows, nil); err != nil {
				log.Warnf("FlatSQL engine: retire %d arena row(s) after a rolled-back %s window: %v", len(arenaRows), schemaName, err)
			}
		}
	}()

	now := time.Now().Unix()
	inserted := 0
	// New records queued for the engine vtab (loop B.3); mirrored after the
	// control transaction commits so a rollback never leaves phantom rows in
	// the engine.
	var enginePending []engineIngest
	var superseded []string

	// ONE existence probe for the whole window. This was
	// "SELECT 1 FROM sdn_record_index WHERE schema_name=? AND cid=?" per
	// record — one engine handoff each, and on a 100k-row index measured at
	// 6.5 ms for 128 absent CIDs against 0.34 ms for the same 128 in one
	// IN-list probe. The map is then maintained in Go exactly as the
	// in-transaction probe behaved: a CID this window has already queued
	// counts as present for every later record, so the same bytes twice in one
	// window still take the repeat path.
	cids := make([]string, len(records))
	for i, data := range records {
		cids[i] = computeCID(data)
	}
	present, err := recordIndexPresence(tx, schemaName, cids)
	if err != nil {
		return 0, fmt.Errorf("check %s record batch: %w", schemaName, err)
	}

	// New rows are queued here and land as multi-row statements
	// (flatsql_batch_writes.go). The buffer is flushed before ANY statement
	// below that reads rows back, so what the engine sees is what the
	// per-record path put there.
	pending := &chunkWriteBuffer{
		schemaName:  schemaName,
		routedTable: routedTable,
		textIndex:   s.fullTextState(schemaName),
		tags:        tags,
	}
	// Supersede keys carried by rows still sitting in the buffer. The supersede
	// SELECT reads the producer TABLE, so it sees only what is on disk; a queued
	// row is invisible to it. That only matters when a queued row carries the
	// key the incoming record is about to retire, which is the one case this set
	// detects.
	queuedKeys := map[string]struct{}{}
	// One batch is one source's write, so every row in this window shares the
	// supersede lane scope.
	sourceName := sourceNameOf(tags)
	for i, data := range records {
		cid := cids[i]
		if _, repeat := present[cid]; repeat {
			// Repeat CID (possibly from another producer): record it in THIS
			// producer's table too. The mirror COPIES the stored bytes back
			// out of the read source and supersedes against the producer
			// table, so anything this window still holds must be on disk
			// first.
			landed, err := pending.flush(tx)
			inserted += landed
			if err != nil {
				return inserted, err
			}
			clear(queuedKeys)
			gone := s.mirrorRoutedRecordFromExisting(tx, schemaName, cid, peerID, signature, sourceName)
			superseded = append(superseded, gone...)
			forgetPresence(present, gone)
			enginePending = dropEnginePending(enginePending, gone)
			if tags != nil {
				if err := upsertSourceTagsTx(tx, readSource, schemaName, cid, *tags, int64(len(data))); err != nil {
					return inserted, err
				}
			}
			continue
		}

		stored, err := s.storableRecordBytes(schemaName, data)
		if err != nil {
			return inserted, err
		}
		keys := recordSupersedeKeys(schemaName, data, sourceName)
		if !keys.empty() {
			// Supersede READS the producer table and the record index and
			// DELETES what it finds (record_supersede.go). Two records with the
			// same key in one window must supersede each other exactly as they
			// did record by record, and the queue is invisible to that SELECT —
			// so the queue is emptied, but ONLY when it actually holds this key.
			//
			// Flushing unconditionally here is correct but throws the window
			// away for the one standard that reaches this branch: every $CAT
			// record has a supersede key, so every $CAT record would flush a
			// buffer holding exactly itself, and the satellite catalog would
			// write one row per statement while every other standard wrote 128.
			// A catalog batch is overwhelmingly DISTINCT objects, so the
			// collision is rare and the window survives.
			if queuedKeysCollide(queuedKeys, keys) {
				landed, err := pending.flush(tx)
				inserted += landed
				if err != nil {
					return inserted, err
				}
				clear(queuedKeys)
			}
			gone, err := s.supersedeInProducerTableTx(tx, schemaName, routedTable, keys, cid)
			if err != nil {
				return inserted, err
			}
			superseded = append(superseded, gone...)
			forgetPresence(present, gone)
			enginePending = dropEnginePending(enginePending, gone)
		}
		pending.add(storedRecord{cid: cid, peerID: peerID, timestamp: now, data: stored, signature: signature, createdAt: now, supersedeKey: keys.stored}, data)
		present[cid] = struct{}{}
		if !keys.empty() {
			queuedKeys[keys.stored] = struct{}{}
		}
		if s.engineRoutesSchema(schemaName) {
			enginePending = append(enginePending, engineIngest{cid: cid, data: data, source: engineSourceName(tags)})
		}
	}
	landed, err := pending.flush(tx)
	inserted += landed
	if err != nil {
		return inserted, err
	}
	// THE ENGINE MIRROR RUNS BEFORE THE COMMIT, AND ITS LEDGER ROWS JOIN IT.
	//
	// The ledger (sdn_engine_rows) lives in this same control database, so when
	// the mirror opened a transaction of its own a window cost TWO commits and,
	// under the WAL + synchronous=FULL this store ran at the time, two fsyncs —
	// against the one the durable floor pays for the same records. Measured
	// then: 2.0 fsyncs per window, and turning the barriers off entirely was
	// worth 3.08x, which is what said the second one was worth removing. The
	// store moved to WAL+NORMAL on 2026-09-17, so the fsync half of that
	// argument has since been paid off a different way; the remaining reason to
	// fold the ledger in is the one below, which never depended on fsyncs.
	//
	// Folding it in also makes the ledger ATOMIC with the rows it describes,
	// which two transactions could never be. What it costs is the old ordering
	// guarantee: the arena ingest now happens before the commit, so a rollback
	// leaves arena rows behind. Those are tombstoned on the spot by
	// ingestEngineBatchLocked's failure path, and a hard crash in between is the
	// exact drift reconcileEngineResidencyLocked repairs at the next warm open
	// (its `extras` arm tombstones arena rows the ledger does not know).
	//
	// TOMBSTONE BEFORE INGEST, always. A window can supersede a CID and then
	// carry that same CID again as a new record (the repeat-of-a-superseded-CID
	// case); tombstoning after the ingest would retire the row just written and
	// silently drop the record from the engine. That ordering was free when the
	// mirror ran after the commit and is load-bearing now that it runs before.
	if len(superseded) > 0 {
		if _, err := s.tombstoneEngineRecordsLocked(schemaName, superseded, tx); err != nil {
			return inserted, err
		}
	}
	if len(enginePending) > 0 {
		// Engine-cache mirror: failures never fail the write (the control
		// rows are the source of truth), but a trapped runtime does.
		mirrored, err := s.ingestEngineRecords(schemaName, enginePending, tx)
		arenaRows = append(arenaRows, mirrored...)
		if err != nil {
			return inserted, err
		}
	}
	if err := tx.Commit(); err != nil {
		return inserted, fmt.Errorf("commit batch store: %w", err)
	}
	committed = true
	return inserted, nil
}

// UpsertSourceTags attaches or updates source tags for an existing record.
func (s *FlatSQLStore) UpsertSourceTags(schemaName, cid string, tags SourceTags) error {
	if err := s.requireWritable("upsert source tags"); err != nil {
		return err
	}
	if _, err := sds.SchemaNameToTable(schemaName); err != nil {
		return fmt.Errorf("invalid schema name: %w", err)
	}

	defer s.lockWrite("UpsertSourceTags")()

	// Record rowid/bytes are read back through the union read source (routed
	// tables + legacy when present).
	// FILTERED read source: upsertSourceTagsTx looks the record up BY CID once
	// per record, inside the ingest write lock. See recordReadSourceFiltered.
	readSource, err := s.recordReadSourceFiltered(schemaName, "cid = ?1")
	if err != nil {
		return fmt.Errorf("record read source: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin source tag upsert: %w", err)
	}
	defer tx.Rollback()

	if err := upsertSourceTagsTx(tx, readSource, schemaName, cid, tags, -1); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit source tag upsert: %w", err)
	}
	return nil
}

func insertNewSourceTagsTx(tx sqlExecer, schemaName, cid string, tags SourceTags, recordBytes, recordRowID int64) error {
	tags = normalizeSourceTags(tags)
	if err := ValidateSourceTags(tags); err != nil {
		return err
	}
	if strings.TrimSpace(cid) == "" {
		return errors.New("cid is required")
	}

	if _, err := tx.Exec(flatsqldrv.WithoutJournal(`
		INSERT INTO sdn_record_source_tags (
			schema_name, cid, provider_id, source_name, source_url, batch_id,
			content_key_id, producer_peer_id, producer_public_key
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`), schemaName, cid, tags.ProviderID, tags.SourceName, tags.SourceURL, tags.BatchID, tags.ContentKeyID, tags.ProducerPeerID, tags.ProducerPublicKey); err != nil {
		return fmt.Errorf("failed to insert source tags: %w", err)
	}
	if err := incrementSourceSummary(tx, schemaName, tags, recordBytes, recordRowID); err != nil {
		return err
	}
	return nil
}

func upsertSourceTagsTx(tx sqlQueryExecer, tableName, schemaName, cid string, tags SourceTags, recordBytes int64) error {
	tags = normalizeSourceTags(tags)
	if err := ValidateSourceTags(tags); err != nil {
		return err
	}
	if strings.TrimSpace(cid) == "" {
		return errors.New("cid is required")
	}

	var existingSourceURL string
	err := tx.QueryRow(`
		SELECT COALESCE(source_url, '')
		FROM sdn_record_source_tags
		WHERE schema_name = ?
		  AND cid = ?
		  AND provider_id = ?
		  AND source_name = ?
		  AND batch_id = ?
		  AND content_key_id = ?
		  AND producer_peer_id = ?
		  AND producer_public_key = ?
	`, schemaName, cid, tags.ProviderID, tags.SourceName, tags.BatchID, tags.ContentKeyID, tags.ProducerPeerID, tags.ProducerPublicKey).Scan(&existingSourceURL)
	if err == nil {
		if existingSourceURL != tags.SourceURL {
			if _, err := tx.Exec(flatsqldrv.WithoutJournal(`
				UPDATE sdn_record_source_tags
				SET source_url = ?
				WHERE schema_name = ?
				  AND cid = ?
				  AND provider_id = ?
				  AND source_name = ?
				  AND batch_id = ?
				  AND content_key_id = ?
				  AND producer_peer_id = ?
				  AND producer_public_key = ?
			`), tags.SourceURL, schemaName, cid, tags.ProviderID, tags.SourceName, tags.BatchID, tags.ContentKeyID, tags.ProducerPeerID, tags.ProducerPublicKey); err != nil {
				return fmt.Errorf("update existing source tag URL: %w", err)
			}
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("lookup existing source tags: %w", err)
	}

	bytesTotal := recordBytes
	var storedRowID sql.NullInt64
	var storedBytes sql.NullInt64
	// tableName is a FILTERED read source (recordReadSourceFiltered with
	// `cid = ?1`) — same reason as mirrorRoutedRecordFromExisting: this runs
	// once per record inside the ingest write lock.
	if err := tx.QueryRow(fmt.Sprintf(`SELECT rowid, record_length FROM %s WHERE cid = ?1`, tableName), cid).Scan(&storedRowID, &storedBytes); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("lookup source-tagged record metadata: %w", err)
		}
	}
	if bytesTotal < 0 {
		bytesTotal = storedBytes.Int64
	}

	_, err = tx.Exec(flatsqldrv.WithoutJournal(`
		INSERT INTO sdn_record_source_tags (
			schema_name, cid, provider_id, source_name, source_url, batch_id,
			content_key_id, producer_peer_id, producer_public_key
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`), schemaName, cid, tags.ProviderID, tags.SourceName, tags.SourceURL, tags.BatchID, tags.ContentKeyID, tags.ProducerPeerID, tags.ProducerPublicKey)
	if err != nil {
		return fmt.Errorf("failed to upsert source tags: %w", err)
	}

	if err := incrementSourceSummary(tx, schemaName, tags, bytesTotal, storedRowID.Int64); err != nil {
		return err
	}
	return nil
}

// GetSourceTags returns provider/source tags for a stored record.
func (s *FlatSQLStore) GetSourceTags(schemaName, cid string) (SourceTags, error) {
	if _, err := sds.SchemaNameToTable(schemaName); err != nil {
		return SourceTags{}, fmt.Errorf("invalid schema name: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	var tags SourceTags
	err := s.db.QueryRow(`
		SELECT provider_id, source_name, source_url, batch_id, content_key_id,
		       producer_peer_id, producer_public_key
		FROM sdn_record_source_tags
		WHERE schema_name = ? AND cid = ?
		ORDER BY created_at DESC
		LIMIT 1
	`, schemaName, cid).Scan(
		&tags.ProviderID,
		&tags.SourceName,
		&tags.SourceURL,
		&tags.BatchID,
		&tags.ContentKeyID,
		&tags.ProducerPeerID,
		&tags.ProducerPublicKey,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return SourceTags{}, fmt.Errorf("source tags not found: %s/%s", schemaName, cid)
		}
		return SourceTags{}, fmt.Errorf("failed to get source tags: %w", err)
	}
	return tags, nil
}

func (s *FlatSQLStore) sourceTagsForCIDs(schemaName string, cids []string) (map[string]SourceTags, error) {
	if _, err := sds.SchemaNameToTable(schemaName); err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}
	tagsByCID := make(map[string]SourceTags, len(cids))
	if len(cids) == 0 {
		return tagsByCID, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	const chunkSize = 500
	for start := 0; start < len(cids); start += chunkSize {
		end := start + chunkSize
		if end > len(cids) {
			end = len(cids)
		}
		chunk := cids[start:end]
		placeholders := make([]string, len(chunk))
		args := make([]interface{}, 0, len(chunk)+1)
		args = append(args, schemaName)
		for i, cid := range chunk {
			placeholders[i] = "?"
			args = append(args, cid)
		}

		rows, err := s.db.Query(`
			SELECT cid, provider_id, source_name, source_url, batch_id, content_key_id,
			       producer_peer_id, producer_public_key
			FROM sdn_record_source_tags
			WHERE schema_name = ? AND cid IN (`+strings.Join(placeholders, ",")+`)
			ORDER BY cid ASC, created_at DESC
		`, args...)
		if err != nil {
			return nil, fmt.Errorf("query source tags: %w", err)
		}
		for rows.Next() {
			var cid string
			var tags SourceTags
			if err := rows.Scan(
				&cid,
				&tags.ProviderID,
				&tags.SourceName,
				&tags.SourceURL,
				&tags.BatchID,
				&tags.ContentKeyID,
				&tags.ProducerPeerID,
				&tags.ProducerPublicKey,
			); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan source tags: %w", err)
			}
			if _, exists := tagsByCID[cid]; !exists {
				tagsByCID[cid] = tags
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read source tags: %w", err)
		}
		rows.Close()
	}
	return tagsByCID, nil
}

// QuerySourceTaggedRecords returns records matching provider/source/batch tags.
func (s *FlatSQLStore) QuerySourceTaggedRecords(query SourceTagQuery) ([]*Record, error) {
	if query.Limit <= 0 {
		query.Limit = 100
	}
	if query.Limit > 1000 {
		query.Limit = 1000
	}
	conditions := []string{"tags.schema_name = ?"}
	args := []interface{}{query.SchemaName, query.SchemaName}
	if providerID := strings.TrimSpace(query.ProviderID); providerID != "" {
		conditions = append(conditions, "tags.provider_id = ?")
		args = append(args, providerID)
	}
	if sourceName := strings.TrimSpace(query.SourceName); sourceName != "" {
		conditions = append(conditions, "tags.source_name = ?")
		args = append(args, sourceName)
	}
	if batchID := strings.TrimSpace(query.BatchID); batchID != "" {
		conditions = append(conditions, "tags.batch_id = ?")
		args = append(args, batchID)
	}
	args = append(args, query.Limit)

	s.mu.RLock()
	defer s.mu.RUnlock()
	readSource, err := s.recordReadSource(query.SchemaName)
	if err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT records.cid, records.peer_id, records.timestamp,
		       records.data, records.signature_hex
		FROM %s records
		INNER JOIN sdn_record_source_tags tags
			ON tags.schema_name = ? AND tags.cid = records.cid
		WHERE %s
		ORDER BY records.timestamp DESC
		LIMIT ?
	`, readSource, strings.Join(conditions, " AND ")), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query source tagged records: %w", err)
	}
	defer rows.Close()

	records := make([]*Record, 0, query.Limit)
	for rows.Next() {
		var record Record
		var ts int64
		var data []byte
		var signatureHex sql.NullString
		if err := rows.Scan(&record.CID, &record.PeerID, &ts, &data, &signatureHex); err != nil {
			return nil, fmt.Errorf("failed to scan source tagged record: %w", err)
		}
		record.Timestamp = time.Unix(ts, 0)
		if err := s.hydrateRecordData(&record, query.SchemaName, data, signatureHex); err != nil {
			return nil, fmt.Errorf("failed to read source tagged record data: %w", err)
		}
		records = append(records, &record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("source tagged record rows: %w", err)
	}
	return records, nil
}

// ReconcileSourceBatch deletes source-tagged records outside the accepted
// source batch. It is intended for DPM-series reconciliation after an operator
// has selected the latest accepted source hash/batch ID.
func (s *FlatSQLStore) ReconcileSourceBatch(schemaName, providerID, sourceName, keepBatch string, apply bool) (SourceBatchReconcileResult, error) {
	if apply {
		if err := s.requireWritable("reconcile source batch"); err != nil {
			return SourceBatchReconcileResult{}, err
		}
	}
	result := SourceBatchReconcileResult{
		SchemaName: strings.TrimSpace(schemaName),
		ProviderID: strings.TrimSpace(providerID),
		SourceName: strings.TrimSpace(sourceName),
		KeepBatch:  strings.TrimSpace(keepBatch),
		Apply:      apply,
	}
	if result.SchemaName == "" {
		return result, errors.New("schema name is required")
	}
	if result.ProviderID == "" {
		return result, errors.New("provider id is required")
	}
	if result.SourceName == "" {
		return result, errors.New("source name is required")
	}
	if result.KeepBatch == "" {
		return result, errors.New("keep batch is required")
	}
	tableName, err := sds.SchemaNameToTable(result.SchemaName)
	if err != nil {
		return result, fmt.Errorf("invalid schema name: %w", err)
	}

	defer s.lockWrite("ReconcileSourceBatch")()

	readSource, err := s.recordReadSource(result.SchemaName)
	if err != nil {
		return result, fmt.Errorf("record read source: %w", err)
	}
	args := []interface{}{result.SchemaName, result.ProviderID, result.SourceName, result.KeepBatch}
	countSQL := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM %s records
		INNER JOIN sdn_record_source_tags tags
		  ON tags.schema_name = ? AND tags.cid = records.cid
		WHERE tags.provider_id = ?
		  AND tags.source_name = ?
		  AND tags.batch_id <> ?
	`, readSource)
	if err := s.db.QueryRow(countSQL, args...).Scan(&result.Matched); err != nil {
		return result, fmt.Errorf("count source batch reconciliation records: %w", err)
	}
	if !apply || result.Matched == 0 {
		return result, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return result, fmt.Errorf("begin source batch reconciliation: %w", err)
	}
	defer tx.Rollback()

	cidSubquery := `
		SELECT cid
		FROM sdn_record_source_tags
		WHERE schema_name = ?
		  AND provider_id = ?
		  AND source_name = ?
		  AND batch_id <> ?
	`
	if _, err := tx.Exec(flatsqldrv.WithoutJournal(`CREATE TEMP TABLE IF NOT EXISTS temp_sdn_reconcile_cids (cid TEXT PRIMARY KEY)`)); err != nil {
		return result, fmt.Errorf("create reconcile cid table: %w", err)
	}
	if _, err := tx.Exec(flatsqldrv.WithoutJournal(`DELETE FROM temp_sdn_reconcile_cids`)); err != nil {
		return result, fmt.Errorf("clear reconcile cid table: %w", err)
	}
	if _, err := tx.Exec(flatsqldrv.WithoutJournal(`INSERT OR IGNORE INTO temp_sdn_reconcile_cids (cid) `+cidSubquery), args...); err != nil {
		return result, fmt.Errorf("stage source batch cids: %w", err)
	}
	if _, err := tx.Exec(flatsqldrv.WithoutJournal(`DELETE FROM sdn_record_source_tags WHERE schema_name = ? AND provider_id = ? AND source_name = ? AND batch_id <> ?`), args...); err != nil {
		return result, fmt.Errorf("delete source batch tags: %w", err)
	}
	// Delete orphaned records (staged cids with no surviving source tag) from
	// every (producer, standard) table. Deleted counts LOGICAL records (per
	// cid), independent of how many tables hold the row.
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM temp_sdn_reconcile_cids WHERE cid NOT IN (SELECT cid FROM sdn_record_source_tags WHERE schema_name = ?)`,
		result.SchemaName,
	).Scan(&result.Deleted); err != nil {
		return result, fmt.Errorf("count orphaned source batch records: %w", err)
	}
	s.deleteRoutedMirrorsWhere(tx, tableName,
		`cid IN (SELECT cid FROM temp_sdn_reconcile_cids) AND cid NOT IN (SELECT cid FROM sdn_record_source_tags WHERE schema_name = ?)`,
		result.SchemaName)
	if _, err := tx.Exec(flatsqldrv.WithoutJournal(`
		DELETE FROM sdn_record_index
		WHERE schema_name = ?
		  AND cid IN (SELECT cid FROM temp_sdn_reconcile_cids)
		  AND NOT EXISTS (
			SELECT 1
			FROM sdn_record_source_tags tags
			WHERE tags.schema_name = sdn_record_index.schema_name
			  AND tags.cid = sdn_record_index.cid
		  )
	`), result.SchemaName); err != nil {
		return result, fmt.Errorf("delete orphaned source batch index rows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("commit source batch reconciliation: %w", err)
	}
	if _, err := s.tombstoneOrphanedEngineRowsLocked(result.SchemaName); err != nil {
		return result, err
	}
	if err := s.rebuildSourceSummaryForSchema(result.SchemaName, tableName); err != nil {
		return result, err
	}
	return result, nil
}

// ReconcileSourceBatchIndexedDuplicates removes duplicate indexed records
// inside a single provider/source/batch. It keeps the newest source-tagged row
// for each logical indexed key and only deletes a record row when no source tags
// remain for that CID.
func (s *FlatSQLStore) ReconcileSourceBatchIndexedDuplicates(schemaName, providerID, sourceName, batchID string, apply bool) (SourceBatchDuplicateReconcileResult, error) {
	if apply {
		if err := s.requireWritable("reconcile source batch duplicates"); err != nil {
			return SourceBatchDuplicateReconcileResult{}, err
		}
	}
	result := SourceBatchDuplicateReconcileResult{
		SchemaName: strings.TrimSpace(schemaName),
		ProviderID: strings.TrimSpace(providerID),
		SourceName: strings.TrimSpace(sourceName),
		BatchID:    strings.TrimSpace(batchID),
		Apply:      apply,
	}
	if result.SchemaName == "" {
		return result, errors.New("schema name is required")
	}
	if result.ProviderID == "" {
		return result, errors.New("provider id is required")
	}
	if result.SourceName == "" {
		return result, errors.New("source name is required")
	}
	if result.BatchID == "" {
		return result, errors.New("batch id is required")
	}
	tableName, err := sds.SchemaNameToTable(result.SchemaName)
	if err != nil {
		return result, fmt.Errorf("invalid schema name: %w", err)
	}

	defer s.lockWrite("ReconcileSourceBatchIndexedDuplicates")()

	readSource, err := s.recordReadSource(result.SchemaName)
	if err != nil {
		return result, fmt.Errorf("record read source: %w", err)
	}
	args := []interface{}{result.SchemaName, result.ProviderID, result.SourceName, result.BatchID}
	countSQL := fmt.Sprintf(`
		WITH ranked AS (
			SELECT
				tags.cid,
				ROW_NUMBER() OVER (
					PARTITION BY
						COALESCE(idx.norad_cat_id, -1),
						COALESCE(idx.entity_id, ''),
						COALESCE(idx.object_type, ''),
						COALESCE(idx.ops_status_code, ''),
						COALESCE(idx.epoch_unix, -1),
						COALESCE(idx.epoch_day, '')
					ORDER BY tags.created_at DESC, records.timestamp DESC, tags.cid DESC
				) AS rn
			FROM sdn_record_source_tags tags
			INNER JOIN sdn_record_index idx
			  ON idx.schema_name = tags.schema_name AND idx.cid = tags.cid
			INNER JOIN %s records
			  ON records.cid = tags.cid
			WHERE tags.schema_name = ?
			  AND tags.provider_id = ?
			  AND tags.source_name = ?
			  AND tags.batch_id = ?
			  -- AN EMPTY INDEX IS NOT AN IDENTITY.
			  --
			  -- The partition above is the SATELLITE index. A standard that
			  -- populates none of it — $TBS cell sites, $IRM marks, every
			  -- non-satellite record type — lands every row in the single
			  -- partition (-1, '', '', '', -1, '') and ROW_NUMBER() marks all
			  -- but one of them a duplicate. The duplicates mode is the DEFAULT,
			  -- so a batch of six distinct cell towers reconciled down to ONE
			  -- and reported success (graph:
			  -- sdn-cellular-ingest-lands-no-batch, measured: 6 sites in, 1
			  -- stored). Records that share an ACTUAL key still collapse; a row
			  -- with no key at all is no longer anyone's duplicate.
			  AND (
			    COALESCE(idx.norad_cat_id, -1) <> -1
			    OR COALESCE(idx.entity_id, '') <> ''
			    OR COALESCE(idx.object_type, '') <> ''
			    OR COALESCE(idx.ops_status_code, '') <> ''
			    OR COALESCE(idx.epoch_unix, -1) <> -1
			    OR COALESCE(idx.epoch_day, '') <> ''
			  )
		)
		SELECT COUNT(*)
		FROM ranked
		WHERE rn > 1
	`, readSource)
	if err := s.db.QueryRow(countSQL, args...).Scan(&result.Matched); err != nil {
		return result, fmt.Errorf("count source batch duplicate records: %w", err)
	}
	if !apply || result.Matched == 0 {
		return result, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return result, fmt.Errorf("begin source batch duplicate reconciliation: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`CREATE TEMP TABLE IF NOT EXISTS temp_sdn_reconcile_duplicate_cids (cid TEXT PRIMARY KEY)`); err != nil {
		return result, fmt.Errorf("create duplicate reconcile cid table: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM temp_sdn_reconcile_duplicate_cids`); err != nil {
		return result, fmt.Errorf("clear duplicate reconcile cid table: %w", err)
	}
	stageSQL := fmt.Sprintf(`
		INSERT OR IGNORE INTO temp_sdn_reconcile_duplicate_cids (cid)
		WITH ranked AS (
			SELECT
				tags.cid,
				ROW_NUMBER() OVER (
					PARTITION BY
						COALESCE(idx.norad_cat_id, -1),
						COALESCE(idx.entity_id, ''),
						COALESCE(idx.object_type, ''),
						COALESCE(idx.ops_status_code, ''),
						COALESCE(idx.epoch_unix, -1),
						COALESCE(idx.epoch_day, '')
					ORDER BY tags.created_at DESC, records.timestamp DESC, tags.cid DESC
				) AS rn
			FROM sdn_record_source_tags tags
			INNER JOIN sdn_record_index idx
			  ON idx.schema_name = tags.schema_name AND idx.cid = tags.cid
			INNER JOIN %s records
			  ON records.cid = tags.cid
			WHERE tags.schema_name = ?
			  AND tags.provider_id = ?
			  AND tags.source_name = ?
			  AND tags.batch_id = ?
			  -- AN EMPTY INDEX IS NOT AN IDENTITY.
			  --
			  -- The partition above is the SATELLITE index. A standard that
			  -- populates none of it — $TBS cell sites, $IRM marks, every
			  -- non-satellite record type — lands every row in the single
			  -- partition (-1, '', '', '', -1, '') and ROW_NUMBER() marks all
			  -- but one of them a duplicate. The duplicates mode is the DEFAULT,
			  -- so a batch of six distinct cell towers reconciled down to ONE
			  -- and reported success (graph:
			  -- sdn-cellular-ingest-lands-no-batch, measured: 6 sites in, 1
			  -- stored). Records that share an ACTUAL key still collapse; a row
			  -- with no key at all is no longer anyone's duplicate.
			  AND (
			    COALESCE(idx.norad_cat_id, -1) <> -1
			    OR COALESCE(idx.entity_id, '') <> ''
			    OR COALESCE(idx.object_type, '') <> ''
			    OR COALESCE(idx.ops_status_code, '') <> ''
			    OR COALESCE(idx.epoch_unix, -1) <> -1
			    OR COALESCE(idx.epoch_day, '') <> ''
			  )
		)
		SELECT cid
		FROM ranked
		WHERE rn > 1
	`, readSource)
	if _, err := tx.Exec(stageSQL, args...); err != nil {
		return result, fmt.Errorf("stage source batch duplicate cids: %w", err)
	}
	if _, err := tx.Exec(`
		DELETE FROM sdn_record_source_tags
		WHERE schema_name = ?
		  AND provider_id = ?
		  AND source_name = ?
		  AND batch_id = ?
		  AND cid IN (SELECT cid FROM temp_sdn_reconcile_duplicate_cids)
	`, args...); err != nil {
		return result, fmt.Errorf("delete source batch duplicate tags: %w", err)
	}
	// Deleted counts LOGICAL records (per cid), independent of table layout.
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM temp_sdn_reconcile_duplicate_cids WHERE cid NOT IN (SELECT cid FROM sdn_record_source_tags WHERE schema_name = ?)`,
		result.SchemaName,
	).Scan(&result.Deleted); err != nil {
		return result, fmt.Errorf("count orphaned duplicate records: %w", err)
	}
	s.deleteRoutedMirrorsWhere(tx, tableName,
		`cid IN (SELECT cid FROM temp_sdn_reconcile_duplicate_cids) AND cid NOT IN (SELECT cid FROM sdn_record_source_tags WHERE schema_name = ?)`,
		result.SchemaName)
	if _, err := tx.Exec(`
		DELETE FROM sdn_record_index
		WHERE schema_name = ?
		  AND cid IN (SELECT cid FROM temp_sdn_reconcile_duplicate_cids)
		  AND NOT EXISTS (
			SELECT 1
			FROM sdn_record_source_tags tags
			WHERE tags.schema_name = sdn_record_index.schema_name
			  AND tags.cid = sdn_record_index.cid
		  )
	`, result.SchemaName); err != nil {
		return result, fmt.Errorf("delete orphaned duplicate index rows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("commit source batch duplicate reconciliation: %w", err)
	}
	if _, err := s.tombstoneOrphanedEngineRowsLocked(result.SchemaName); err != nil {
		return result, err
	}
	if err := s.rebuildSourceSummaryForSourceBatch(result.SchemaName, tableName, result.ProviderID, result.SourceName, result.BatchID); err != nil {
		return result, err
	}
	return result, nil
}

// ValidateSourceTags checks required provider/source fields.
func ValidateSourceTags(tags SourceTags) error {
	if strings.TrimSpace(tags.ProviderID) == "" {
		return errors.New("provider_id is required")
	}
	if strings.TrimSpace(tags.SourceName) == "" {
		return errors.New("source_name is required")
	}
	return nil
}

// Get retrieves data by CID.
func (s *FlatSQLStore) Get(schemaName, cid string) ([]byte, error) {
	record, err := s.GetRecord(schemaName, cid)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), record.Data...), nil
}

// Query executes a safe parameterized query against a schema table.
// The whereClause MUST use ? placeholders for all values.
// This method is only used internally with trusted where clauses.
func (s *FlatSQLStore) Query(schemaName, whereClause string, args ...interface{}) ([][]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Reads span every (producer, standard) table of the standard.
	readSource, err := s.recordReadSource(schemaName)
	if err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}

	var querySQL string
	if whereClause != "" {
		querySQL = fmt.Sprintf(`SELECT data FROM %s WHERE %s`, readSource, whereClause)
	} else {
		querySQL = fmt.Sprintf(`SELECT data FROM %s`, readSource)
	}

	rows, err := s.db.Query(querySQL, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query: %w", err)
	}
	defer rows.Close()

	var results [][]byte
	for rows.Next() {
		var stored []byte
		if err := rows.Scan(&stored); err != nil {
			log.Warnf("Failed to scan row: %v", err)
			continue
		}
		data, err := s.openStoredRecordBytes(schemaName, stored)
		if err != nil {
			log.Warnf("Failed to open stored record: %v", err)
			continue
		}
		results = append(results, data)
	}

	return results, nil
}

// QueryAll returns all records for a schema (no filtering). Safe for protocol use.
func (s *FlatSQLStore) QueryAll(schemaName string, limit int) ([][]byte, error) {
	if limit <= 0 {
		limit = 1000
	}
	if limit > 10000 {
		limit = 10000
	}
	return s.Query(schemaName, "1=1 ORDER BY timestamp DESC LIMIT ?", limit)
}

// QueryAllBounded returns recent records while enforcing both row and total-byte limits.
func (s *FlatSQLStore) QueryAllBounded(schemaName string, limit int, maxTotalBytes int) ([][]byte, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	if maxTotalBytes <= 0 {
		maxTotalBytes = 2 * 1024 * 1024 // 2MB default response budget
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	readSource, err := s.recordReadSource(schemaName)
	if err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}
	querySQL := fmt.Sprintf(`SELECT data FROM %s ORDER BY timestamp DESC LIMIT ?`, readSource)
	rows, err := s.db.Query(querySQL, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query bounded records: %w", err)
	}
	defer rows.Close()

	results := make([][]byte, 0, limit)
	totalBytes := 0
	for rows.Next() {
		var stored []byte
		if err := rows.Scan(&stored); err != nil {
			log.Warnf("Failed to scan row: %v", err)
			continue
		}
		data, err := s.openStoredRecordBytes(schemaName, stored)
		if err != nil {
			log.Warnf("Failed to open stored record: %v", err)
			continue
		}
		if len(data) > maxTotalBytes {
			continue
		}
		if totalBytes+len(data) > maxTotalBytes {
			break
		}
		totalBytes += len(data)
		results = append(results, data)
	}

	return results, nil
}

// QueryWithPeerID queries records from a specific peer.
func (s *FlatSQLStore) QueryWithPeerID(schemaName, peerID string) ([][]byte, error) {
	return s.Query(schemaName, "peer_id = ?", peerID)
}

// QuerySince queries records since a given timestamp.
func (s *FlatSQLStore) QuerySince(schemaName string, since time.Time) ([][]byte, error) {
	return s.Query(schemaName, "timestamp > ?", since.Unix())
}

// Delete removes a record by CID from every producer table that holds it,
// then its index row, source tags and derived-summary bookkeeping, and
// finally tombstones it in the engine hot window so the sandboxed query
// surface stops answering with it at once (engine_residency.go).
func (s *FlatSQLStore) Delete(schemaName, cid string) error {
	if err := s.requireWritable("delete record"); err != nil {
		return err
	}
	defer s.lockWrite("Delete")()

	tableName, err := sds.SchemaNameToTable(schemaName)
	if err != nil {
		return fmt.Errorf("invalid schema name: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin delete: %w", err)
	}
	defer tx.Rollback()

	readSource, rsErr := s.recordReadSourceFiltered(schemaName, "cid = ?1")
	if rsErr != nil {
		return fmt.Errorf("record read source: %w", rsErr)
	}
	var recordBytes sql.NullInt64
	var recordRowID sql.NullInt64
	if err := tx.QueryRow(fmt.Sprintf(`SELECT rowid, record_length FROM %s WHERE cid = ?1`, readSource), cid).Scan(&recordRowID, &recordBytes); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("not found: %s", cid)
		}
		return fmt.Errorf("lookup deleted record bytes: %w", err)
	}
	if affected := s.deleteRoutedMirrorsWhere(tx, tableName, `cid = ?`, cid); affected == 0 {
		return fmt.Errorf("not found: %s", cid)
	}
	if err := s.removeRecordCatalogRowsTx(tx, schemaName, cid, recordBytes.Int64, recordRowID.Int64); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = s.tombstoneEngineRecordsLocked(schemaName, []string{cid}, nil)
	return err
}

// Count returns the number of records in a schema table.
func (s *FlatSQLStore) Count(schemaName string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	readSource, err := s.recordReadSource(schemaName)
	if err != nil {
		return 0, fmt.Errorf("invalid schema name: %w", err)
	}

	var count int64
	err = s.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s`, readSource)).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count: %w", err)
	}

	return count, nil
}

// GarbageCollect removes old records based on age.
func (s *FlatSQLStore) GarbageCollect(maxAge time.Duration) (int64, error) {
	if err := s.requireWritable("garbage collect"); err != nil {
		return 0, err
	}
	defer s.lockWrite("GarbageCollect")()

	cutoff := time.Now().Add(-maxAge).Unix()
	return s.garbageCollectBeforeLocked(cutoff)
}

// garbageCollectBeforeLocked deletes every record (across every schema)
// with timestamp < cutoff (Unix seconds): the exact age-based delete path
// GarbageCollect has always used, factored out (Task D3) so
// GarbageCollectToQuota reuses it instead of duplicating the delete
// machinery (legacy + routed-mirror table cleanup, index sync, source-tag
// cleanup, record-catalog GC event, source-summary rebuild). Callers must
// hold s.mu (Lock) and have already checked requireWritable.
func (s *FlatSQLStore) garbageCollectBeforeLocked(cutoff int64) (int64, error) {
	var totalDeleted int64

	for _, schemaName := range s.validator.Schemas() {
		tableName, err := sds.SchemaNameToTable(schemaName)
		if err != nil {
			log.Warnf("GC skipping invalid schema %q: %v", schemaName, err)
			continue
		}

		affected := s.deleteRoutedMirrorsWhere(s.db, tableName, `timestamp < ?`, cutoff)
		totalDeleted += affected

		// Keep index table in sync with GC deletes.
		if _, err := s.db.Exec(flatsqldrv.WithoutJournal(`
			DELETE FROM sdn_record_index
			WHERE schema_name = ? AND source_timestamp < ?
		`), schemaName, cutoff); err != nil {
			log.Warnf("GC index cleanup failed for %s: %v", schemaName, err)
		}
		if affected > 0 {
			readSource, rsErr := s.recordReadSource(schemaName)
			if rsErr != nil {
				log.Warnf("GC source tag cleanup read source for %s: %v", schemaName, rsErr)
				continue
			}
			if _, err := s.db.Exec(flatsqldrv.WithoutJournal(fmt.Sprintf(`
				DELETE FROM sdn_record_source_tags
				WHERE schema_name = ?
				  AND cid NOT IN (SELECT cid FROM %s)
			`, readSource)), schemaName); err != nil {
				log.Warnf("GC source tag cleanup failed for %s: %v", schemaName, err)
			}
			if _, err := s.tombstoneOrphanedEngineRowsLocked(schemaName); err != nil {
				return totalDeleted, err
			}
			if err := s.rebuildSourceSummaryForSchema(schemaName, tableName); err != nil {
				log.Warnf("GC source summary rebuild failed for %s: %v", schemaName, err)
			}
		}
	}

	if totalDeleted > 0 {
		log.Infof("GC removed %d old records (cutoff unix=%d)", totalDeleted, cutoff)
	}

	return totalDeleted, nil
}

// quotaLowWaterMarkFraction is the hysteresis fraction GarbageCollectToQuota
// evicts DOWN TO once a store exceeds its configured cap: eviction targets
// this fraction of maxBytes rather than maxBytes itself, so a store
// sitting right at the cap does not immediately re-trigger a GC pass on
// the very next write — which would otherwise run a full sweep on every
// single incoming record once steady-state ingestion holds the store near
// its ceiling. 0.85 gives a 15% buffer before the next eviction pass is
// needed.
const quotaLowWaterMarkFraction = 0.85

// maxQuotaEvictionRounds bounds GarbageCollectToQuota's cutoff-search
// loop. Each round re-measures live bytes and, if still over the
// low-water mark, searches for a wider eviction cutoff; this caps
// worst-case iterations for a skewed record-size distribution rather than
// looping unboundedly.
const maxQuotaEvictionRounds = 8

// LiveRecordBytes returns the sum of record_length across every live
// (non-deleted) record in every schema this store recognizes.
//
// This is the quantity GarbageCollectToQuota enforces a cap against.
// FlatSQL stream files are strictly append-only (this store's stream
// files are never rewritten in place — see recordReadSource's "the store
// never VACUUMs" rowid-stability note): deleting a record's index/mirror
// rows shrinks LiveRecordBytes immediately and deterministically, but
// does NOT shrink the bytes that record's payload already occupies in its
// stream file on disk. LiveRecordBytes is therefore the metric quota
// enforcement can actually control — it bounds how large the LOGICAL
// dataset is allowed to grow. DiskUsageBytes (below) reports the true,
// larger, monotonically-growing on-disk footprint for observability; the
// two converge only once a future stream-compaction pass exists to
// reclaim already-written bytes (tracked as a residual gap — see the D3
// task report).
func (s *FlatSQLStore) LiveRecordBytes() (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// A CLOSED store answers, it does not crash. node.go's quota bookkeeping is
	// deliberately fire-and-forget (materializeDatasetPublicationPNM spawns it so
	// quota work adds no latency to materialization), so it CAN outlive the
	// store's Close — and Close nils s.db, which made this a SIGSEGV in a
	// daemon rather than an error at a call site that already handles errors.
	// Reproduced deterministically once the disk-backed boot changed shutdown
	// timing; latent long before that.
	if s.db == nil {
		return 0, ErrStoreClosed
	}
	return s.liveRecordBytesLocked()
}

// liveRecordBytesLocked sums record_length across every schema's read source.
//
// IT IS O(SCHEMAS x RECORDS) AND IT RUNS UNDER THE WRITE LOCK, which is a
// measured reader-starvation source: 42 calls and 485.1 s of s.mu time on
// host-01 in one hour, single calls up to 26.6 s, every second of it taken from
// concurrent readers (sdn-flatsql-store-lock-starves-readers).
//
// IT IS STILL THE SCAN, DELIBERATELY. The obvious fix — sum the per-lane
// total_bytes that sdn_record_source_summary already maintains — is WRONG and
// the existing TestLiveRecordBytesSumsRecordLength proves it in one line: a
// record stored WITHOUT source tags creates no summary lane, so the maintained
// total answered 0 where the scan answers 600. Quota enforcement reads this
// number; a fast wrong answer is worse than a slow right one.
//
// The real fix is a counter maintained on the record write path itself (so it
// covers untagged writes) with a rebuild path for existing stores, which is a
// design decision rather than an edit — filed as
// sdn-live-record-bytes-needs-a-maintained-counter.
func (s *FlatSQLStore) liveRecordBytesLocked() (int64, error) {
	var total int64
	for _, schemaName := range s.validator.Schemas() {
		readSource, err := s.recordReadSource(schemaName)
		if err != nil {
			continue
		}
		var sum sql.NullInt64
		if err := s.db.QueryRow(fmt.Sprintf(`SELECT COALESCE(SUM(record_length), 0) FROM %s`, readSource)).Scan(&sum); err != nil {
			log.Warnf("LiveRecordBytes: sum record_length for %s: %v", schemaName, err)
			continue
		}
		total += sum.Int64
	}
	return total, nil
}

// DiskUsageBytes sums the bytes the store holds on disk: the control
// database (records, index, tags, auxiliary tables) and its rollback journal,
// the engine's persisted record arena, and the auxiliary metadata journal.
// SQLite reuses freed pages but never shrinks the file, so this reflects the
// historical high-water mark, not the live dataset — see LiveRecordBytes.
func (s *FlatSQLStore) DiskUsageBytes() (int64, error) {
	s.mu.RLock()
	basePath := s.basePath
	controlDBPath := s.controlDBPath
	s.mu.RUnlock()

	var total int64
	for _, path := range []string{
		controlDBPath,
		controlDBPath + "-journal",
		controlDBPath + ".fsdata",
		filepath.Join(basePath, auxiliaryMetadataFileName),
	} {
		info, statErr := os.Stat(path)
		if statErr == nil {
			total += info.Size()
			continue
		}
		if !os.IsNotExist(statErr) {
			return 0, fmt.Errorf("disk usage: stat %s: %w", path, statErr)
		}
	}
	return total, nil
}

// GarbageCollectToQuota enforces a disk-quota cap (Task D3) by evicting
// the OLDEST records first — globally, across every schema — until the
// store's live-record footprint (LiveRecordBytes) drops to the low-water
// mark (quotaLowWaterMarkFraction * maxBytes), or there is nothing left
// to evict. maxBytes <= 0 disables the check (returns 0, nil
// immediately).
//
// Global "oldest first" is approximated via a shared cutoff timestamp —
// the same sdn_record_index.source_timestamp column age-based GC already
// keys off: each round estimates how many of the globally-oldest records
// need to go (bytes-over-low-water / average-bytes-per-record), looks up
// the source_timestamp at that offset via sdn_record_index ordered
// ascending, and deletes everything at-or-before it via
// garbageCollectBeforeLocked — the exact delete path GarbageCollect(maxAge)
// uses, so eviction goes through the same, already-exercised code (index
// rows, routed mirror tables, source-tag/summary cleanup, record-catalog
// GC event) rather than a duplicated one. Multiple rounds correct for a
// non-uniform size distribution (e.g. a run of oversized records skewing
// the average); maxQuotaEvictionRounds bounds the worst case.
//
// Never deletes a record newer than the chosen cutoff, so newest records
// are always retained; never runs past the low-water mark once reached,
// so it does not over-evict beyond what the hysteresis budget calls for.
//
// Residual gap: because of the append-only stream-file limitation
// documented on LiveRecordBytes, this shrinks the LIVE dataset
// immediately (bounding further disk growth) but does not itself reclaim
// already-written stream bytes from disk — see LiveRecordBytes's doc.
func (s *FlatSQLStore) GarbageCollectToQuota(maxBytes int64) (int64, error) {
	if maxBytes <= 0 {
		return 0, nil
	}
	if err := s.requireWritable("garbage collect to quota"); err != nil {
		return 0, err
	}
	defer s.lockWrite("GarbageCollectToQuota")()
	// Queued publication quota work may acquire this lock after Close.
	if s.db == nil {
		return 0, ErrStoreClosed
	}
	liveBytes, err := s.liveRecordBytesLocked()
	if err != nil {
		return 0, fmt.Errorf("garbage collect to quota: measure live bytes: %w", err)
	}
	if liveBytes <= maxBytes {
		return 0, nil
	}
	lowWater := int64(float64(maxBytes) * quotaLowWaterMarkFraction)

	var totalDeleted int64
	for round := 0; round < maxQuotaEvictionRounds && liveBytes > lowWater; round++ {
		var totalCount int64
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sdn_record_index`).Scan(&totalCount); err != nil {
			return totalDeleted, fmt.Errorf("garbage collect to quota: count records: %w", err)
		}
		if totalCount <= 0 {
			break
		}
		avgBytes := liveBytes / totalCount
		if avgBytes <= 0 {
			avgBytes = 1
		}
		remaining := liveBytes - lowWater
		recordsToEvict := remaining / avgBytes
		if remaining%avgBytes != 0 {
			recordsToEvict++
		}
		if recordsToEvict < 1 {
			recordsToEvict = 1
		}
		if recordsToEvict > totalCount {
			recordsToEvict = totalCount
		}

		var cutoff int64
		if err := s.db.QueryRow(
			`SELECT source_timestamp FROM sdn_record_index ORDER BY source_timestamp ASC LIMIT 1 OFFSET ?`,
			recordsToEvict-1,
		).Scan(&cutoff); err != nil {
			return totalDeleted, fmt.Errorf("garbage collect to quota: locate eviction cutoff: %w", err)
		}
		// garbageCollectBeforeLocked deletes strictly-less-than cutoff;
		// evict one Unix second past the selected boundary timestamp so a
		// round always makes forward progress even when many records share
		// that exact second.
		deleted, err := s.garbageCollectBeforeLocked(cutoff + 1)
		if err != nil {
			return totalDeleted, fmt.Errorf("garbage collect to quota: evict: %w", err)
		}
		totalDeleted += deleted
		if deleted == 0 {
			// No progress possible — stop rather than spin.
			break
		}
		liveBytes, err = s.liveRecordBytesLocked()
		if err != nil {
			return totalDeleted, fmt.Errorf("garbage collect to quota: remeasure live bytes: %w", err)
		}
	}

	if totalDeleted > 0 {
		log.Infof("GarbageCollectToQuota evicted %d record(s), live bytes now %d (cap %d, low-water %d)", totalDeleted, liveBytes, maxBytes, lowWater)
	}
	return totalDeleted, nil
}

// Close closes the database handle, record catalog, and engine.
func (s *FlatSQLStore) Close() error {
	s.stopFullTextIndexes()
	// Stop AND JOIN the background checkpointer before taking the write lock.
	// Signalling alone is not enough: the loop may already be blocked on s.mu
	// inside a checkpoint, and it would then run against a store this function
	// has already nil'd. Must happen with the lock NOT held — the loop is
	// waiting for it.
	s.stopCheckpointLoop()

	defer s.lockWrite("Close")()

	// A CLEAN shutdown flushes the engine's record state and advances both
	// marks: the next boot then opens warm and replays nothing.
	if err := s.checkpointLocked(); err != nil {
		log.Warnf("FlatSQL store: final checkpoint failed (the next boot rebuilds the engine tail, nothing is lost): %v", err)
	}

	var firstErr error
	if s.db != nil {
		firstErr = s.db.Close()
		s.db = nil
	}
	if s.auxiliaryMetadata != nil {
		if err := s.auxiliaryMetadata.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.auxiliaryMetadata = nil
	}
	if s.engineDB != nil {
		s.engineDB.Destroy()
		s.engineDB = nil
	}
	if s.engine != nil {
		s.engine.Close()
		s.engine = nil
	}
	// Retired (poisoned, replaced) engines are released only now: dependent
	// linked-flow VMs must have been closed by their mounts first.
	for _, retired := range s.retiredEngines {
		retired.Close()
	}
	s.retiredEngines = nil
	// Release the single-writer liveness lock LAST, after every store file
	// handle is closed, so a waiting opener never sees a half-closed store.
	if s.lock != nil {
		if err := s.lock.release(); err != nil && firstErr == nil {
			firstErr = err
		}
		s.lock = nil
	}
	return firstErr
}

// Stats returns storage statistics.
func (s *FlatSQLStore) Stats() (map[string]int64, error) {
	stats := make(map[string]int64)

	for _, schemaName := range s.validator.Schemas() {
		count, err := s.Count(schemaName)
		if err != nil {
			log.Warnf("Failed to get count for %s: %v", schemaName, err)
			continue
		}
		stats[schemaName] = count
	}

	return stats, nil
}

// computeCID computes the content identifier for data as a real CIDv1
// string (raw codec 0x55, sha2-256 multihash, default base32 multibase
// encoding) — the same identifier a stock IPFS client computes for the same
// bytes via `ipfs add --cid-version=1 --raw-leaves` or
// `ipfs block put --format=raw --mhtype=sha2-256`. This lets any record
// stored here be pinned and fetched as an ordinary IPFS block.
//
// Prior to loop A4 this returned a bare SHA-256 hex digest, which is not a
// valid CID. Rows written before that fix still carry bare-hex values in
// their `cid` columns; those rows remain readable (every lookup here is a
// plain string comparison), but content re-ingested after the fix is
// content-addressed under its proper CIDv1 rather than the old digest — see
// the loop A4 report for the full compat analysis.
// ComputeCID returns the content identifier this store assigns to a record's
// bytes — the same value Store/StoreBatch write to the cid column — so callers
// that store through the batch path can report per-record CIDs without a
// read-back.
func ComputeCID(data []byte) string {
	return computeCID(data)
}

func computeCID(data []byte) string {
	c, err := cidV1RawSHA256(data)
	if err != nil {
		// mh.Sum only fails for an unregistered hash function or an
		// explicit length exceeding the digest size; neither is possible
		// for the fixed sha2-256/default-length call below, so this branch
		// is unreachable in practice. Fall back to the legacy bare-hex
		// digest rather than panicking.
		log.Errorf("computeCID: failed to build CIDv1, falling back to bare SHA-256 hex: %v", err)
		hash := sha256.Sum256(data)
		return hex.EncodeToString(hash[:])
	}
	return c
}

// Path returns the database file path.
func (s *FlatSQLStore) Path() string {
	return s.dbPath
}

// Record represents a stored record with metadata.
type Record struct {
	CID            string
	RowID          int64
	PeerID         string
	Timestamp      time.Time
	Data           []byte
	Signature      []byte
	SourceTags     SourceTags
	MaterializedAt time.Time
	// RecordLength is the stored byte length (the sealed length for a
	// field-encrypted standard), the value the source summaries account.
	RecordLength int64
}

// DirectoryRecord represents a normalized EPM directory entry.
type DirectoryRecord struct {
	Kind           string
	PeerID         string
	DN             string
	LegalName      string
	BitcoinAddress string
	EPMCID         string
	Source         string
	EPMJSON        string
	UpdatedAt      int64
}

// LocalEPMRecord is the decrypted local EPM source-of-truth record. The
// corresponding on-disk SQLite row stores only EPM.fbs bytes encrypted.
type LocalEPMRecord struct {
	PeerID    string
	EPMBytes  []byte
	UpdatedAt int64
}

// DirectoryQuery filters directory records.
type DirectoryQuery struct {
	Kind           string
	PeerID         string
	Source         string
	ExcludePeerID  string
	ExcludeSources []string
	Search         string
	Limit          int
	Offset         int
}

// UpsertDirectoryRecord inserts or updates a directory record.
func (s *FlatSQLStore) UpsertDirectoryRecord(record DirectoryRecord) error {
	if err := s.requireWritable("upsert directory record"); err != nil {
		return err
	}
	record, err := normalizeDirectoryRecord(record)
	if err != nil {
		return err
	}

	defer s.lockWrite("UpsertDirectoryRecord")()

	if err := s.applyDirectoryRecordUpsert(record); err != nil {
		return err
	}
	if err := s.appendAuxiliaryMetadata(auxiliaryMetadataEvent{
		Kind:      auxiliaryEventDirectoryUpsert,
		Directory: &record,
	}); err != nil {
		return fmt.Errorf("append directory metadata: %w", err)
	}
	return nil
}

func normalizeDirectoryRecord(record DirectoryRecord) (DirectoryRecord, error) {
	kind := strings.TrimSpace(strings.ToLower(record.Kind))
	peerID := strings.TrimSpace(record.PeerID)
	if kind == "" {
		return DirectoryRecord{}, errors.New("directory record kind is required")
	}
	if peerID == "" {
		return DirectoryRecord{}, errors.New("directory record peer_id is required")
	}

	source := strings.TrimSpace(record.Source)
	if source == "" {
		source = "unknown"
	}
	updatedAt := record.UpdatedAt
	if updatedAt == 0 {
		updatedAt = time.Now().Unix()
	}

	epmJSON := strings.TrimSpace(record.EPMJSON)
	if epmJSON == "" {
		epmJSON = "{}"
	} else {
		var canonical any
		if err := json.Unmarshal([]byte(epmJSON), &canonical); err == nil {
			if b, err := json.Marshal(canonical); err == nil {
				epmJSON = string(b)
			}
		}
	}

	record.Kind = kind
	record.PeerID = peerID
	record.DN = strings.TrimSpace(record.DN)
	record.LegalName = strings.TrimSpace(record.LegalName)
	record.BitcoinAddress = strings.TrimSpace(record.BitcoinAddress)
	record.EPMCID = strings.TrimSpace(record.EPMCID)
	record.Source = source
	record.UpdatedAt = updatedAt
	record.EPMJSON = epmJSON
	return record, nil
}

func (s *FlatSQLStore) applyDirectoryRecordUpsert(record DirectoryRecord) error {
	_, err := s.auxWrite().Exec(`
		INSERT INTO sdn_directory (
			kind, peer_id, dn, legal_name, bitcoin_address, epm_cid, source, updated_at, epm_json
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(kind, peer_id) DO UPDATE SET
			dn = excluded.dn,
			legal_name = excluded.legal_name,
			bitcoin_address = excluded.bitcoin_address,
			epm_cid = excluded.epm_cid,
			source = excluded.source,
			updated_at = excluded.updated_at,
			epm_json = excluded.epm_json
	`, record.Kind, record.PeerID, record.DN, record.LegalName, record.BitcoinAddress, record.EPMCID, record.Source, record.UpdatedAt, record.EPMJSON)
	if err != nil {
		return fmt.Errorf("failed to upsert directory record: %w", err)
	}
	return nil
}

// QueryDirectory queries directory records using indexed filters and free-text search.
func (s *FlatSQLStore) QueryDirectory(query DirectoryQuery) ([]DirectoryRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	kind := strings.TrimSpace(strings.ToLower(query.Kind))
	peerID := strings.TrimSpace(query.PeerID)
	source := strings.TrimSpace(query.Source)
	excludePeerID := strings.TrimSpace(query.ExcludePeerID)
	search := strings.TrimSpace(query.Search)

	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}

	sqlBuilder := strings.Builder{}
	sqlBuilder.WriteString(`
		SELECT kind, peer_id, dn, legal_name, bitcoin_address, epm_cid, source, updated_at, epm_json
		FROM sdn_directory
		WHERE 1=1
	`)
	args := make([]any, 0, 6)

	if kind != "" {
		sqlBuilder.WriteString(` AND kind = ?`)
		args = append(args, kind)
	}
	if peerID != "" {
		sqlBuilder.WriteString(` AND peer_id = ?`)
		args = append(args, peerID)
	}
	if excludePeerID != "" {
		sqlBuilder.WriteString(` AND peer_id <> ?`)
		args = append(args, excludePeerID)
	}
	if source != "" {
		sqlBuilder.WriteString(` AND source = ?`)
		args = append(args, source)
	}
	excludeSources := make([]string, 0, len(query.ExcludeSources))
	for _, excludeSource := range query.ExcludeSources {
		if trimmed := strings.TrimSpace(excludeSource); trimmed != "" {
			excludeSources = append(excludeSources, trimmed)
		}
	}
	if len(excludeSources) > 0 {
		sqlBuilder.WriteString(` AND source NOT IN (`)
		for i, excludeSource := range excludeSources {
			if i > 0 {
				sqlBuilder.WriteString(`, `)
			}
			sqlBuilder.WriteString(`?`)
			args = append(args, excludeSource)
		}
		sqlBuilder.WriteString(`)`)
	}
	if search != "" {
		needle := "%" + strings.ToLower(search) + "%"
		sqlBuilder.WriteString(` AND (
			lower(COALESCE(peer_id, '')) LIKE ? OR
			lower(COALESCE(dn, '')) LIKE ? OR
			lower(COALESCE(legal_name, '')) LIKE ? OR
			lower(COALESCE(bitcoin_address, '')) LIKE ? OR
			lower(COALESCE(epm_cid, '')) LIKE ? OR
			lower(COALESCE(source, '')) LIKE ? OR
			lower(COALESCE(epm_json, '')) LIKE ?
		)`)
		for i := 0; i < 7; i++ {
			args = append(args, needle)
		}
	}

	sqlBuilder.WriteString(` ORDER BY updated_at DESC, peer_id ASC LIMIT ? OFFSET ?`)
	args = append(args, limit, offset)

	rows, err := s.db.Query(sqlBuilder.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query directory: %w", err)
	}
	defer rows.Close()

	results := make([]DirectoryRecord, 0, limit)
	for rows.Next() {
		var record DirectoryRecord
		if err := rows.Scan(
			&record.Kind,
			&record.PeerID,
			&record.DN,
			&record.LegalName,
			&record.BitcoinAddress,
			&record.EPMCID,
			&record.Source,
			&record.UpdatedAt,
			&record.EPMJSON,
		); err != nil {
			return nil, fmt.Errorf("failed to scan directory record: %w", err)
		}
		results = append(results, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("directory query iteration failed: %w", err)
	}

	return results, nil
}

// SaveLocalEPM stores a local node EPM FlatBuffer encrypted in the FlatSQL
// database. JSON profile and JSON EPM projections are intentionally not stored.
func (s *FlatSQLStore) SaveLocalEPM(peerID string, epmBytes []byte) error {
	if err := s.requireWritable("save local EPM"); err != nil {
		return err
	}
	defer s.lockWrite("SaveLocalEPM")()

	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return errors.New("local EPM peer_id is required")
	}
	if len(epmBytes) == 0 {
		return errors.New("local EPM bytes are required")
	}

	encryptedBytes, err := s.encryptLocalEPMPayload(epmBytes)
	if err != nil {
		return fmt.Errorf("encrypt local EPM bytes: %w", err)
	}
	record := auxiliaryLocalEPMRecord{
		PeerID:            peerID,
		EncryptedEPMBytes: encryptedBytes,
		UpdatedAt:         time.Now().Unix(),
	}

	if err := s.applyLocalEPMEncryptedUpsert(record); err != nil {
		return err
	}
	if err := s.appendAuxiliaryMetadata(auxiliaryMetadataEvent{
		Kind:     auxiliaryEventLocalEPMUpsert,
		LocalEPM: &record,
	}); err != nil {
		return fmt.Errorf("append local EPM metadata: %w", err)
	}

	return nil
}

func (s *FlatSQLStore) applyLocalEPMEncryptedUpsert(record auxiliaryLocalEPMRecord) error {
	peerID := strings.TrimSpace(record.PeerID)
	if peerID == "" {
		return errors.New("local EPM peer_id is required")
	}
	if strings.TrimSpace(record.EncryptedEPMBytes) == "" {
		return errors.New("local EPM encrypted bytes are required")
	}
	updatedAt := record.UpdatedAt
	if updatedAt == 0 {
		updatedAt = time.Now().Unix()
	}

	_, err := s.auxWrite().Exec(`
		INSERT INTO sdn_local_epms (
			peer_id, schema_name, encrypted_epm_bytes, updated_at
		)
		VALUES (?, 'EPM.fbs', ?, ?)
		ON CONFLICT(peer_id) DO UPDATE SET
			schema_name = 'EPM.fbs',
			encrypted_epm_bytes = excluded.encrypted_epm_bytes,
			updated_at = excluded.updated_at
	`, peerID, record.EncryptedEPMBytes, updatedAt)
	if err != nil {
		return fmt.Errorf("save local EPM record: %w", err)
	}
	return nil
}

// LoadLocalEPM decrypts the local node EPM FlatBuffer bytes.
func (s *FlatSQLStore) LoadLocalEPM(peerID string) ([]byte, error) {
	record, err := s.GetLocalEPMRecord(peerID)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), record.EPMBytes...), nil
}

// GetLocalEPMRecord decrypts the local EPM record for a peer ID.
func (s *FlatSQLStore) GetLocalEPMRecord(peerID string) (*LocalEPMRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return nil, errors.New("local EPM peer_id is required")
	}

	var encryptedBytes string
	var updatedAt int64
	err := s.db.QueryRow(`
		SELECT encrypted_epm_bytes, updated_at
		FROM sdn_local_epms
		WHERE peer_id = ?
	`, peerID).Scan(&encryptedBytes, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("local EPM profile not found for %s", peerID)
		}
		return nil, fmt.Errorf("read local EPM profile: %w", err)
	}

	epmBytes, err := s.openLocalEPM(encryptedBytes)
	if err != nil {
		if errors.Is(err, errCorruptLocalEPM) {
			s.dropCorruptLocalEPM(peerID, encryptedBytes, err)
		}
		return nil, fmt.Errorf("decrypt local EPM bytes: %w", err)
	}

	return &LocalEPMRecord{
		PeerID:    peerID,
		EPMBytes:  epmBytes,
		UpdatedAt: updatedAt,
	}, nil
}

// errCorruptLocalEPM marks a local EPM row that can never be read: an empty or
// unparseable envelope, or plaintext that is not an $EPM FlatBuffer. Such a
// row is deleted (journaled, so replay does not restore it). A row that is
// well formed but fails authentication is NOT corrupt — the machine key
// source may have changed — so it is skipped and kept.
var errCorruptLocalEPM = errors.New("corrupt local EPM row")

// openLocalEPM decrypts a stored row and checks the plaintext is an $EPM
// FlatBuffer (plain or size-prefixed). FlatBuffers either read or they do not.
func (s *FlatSQLStore) openLocalEPM(encrypted string) ([]byte, error) {
	plaintext, err := s.decryptLocalEPMPayload(encrypted)
	if err != nil {
		return nil, err
	}
	if !EPM.EPMBufferHasIdentifier(plaintext) && !EPM.SizePrefixedEPMBufferHasIdentifier(plaintext) {
		return nil, fmt.Errorf("%w: plaintext is not an $EPM FlatBuffer", errCorruptLocalEPM)
	}
	return plaintext, nil
}

// readLocalEPMRow is the guard rail every multi-row reader uses: one bad row
// is skipped (and deleted when corrupt), never failing the lane it sits in.
// On 2026-09-25 a single corrupt row failed every /api/v1/sync request, so
// the Store could list no datasets at all.
func (s *FlatSQLStore) readLocalEPMRow(peerID, encrypted string) ([]byte, bool) {
	plaintext, err := s.openLocalEPM(encrypted)
	if err == nil {
		return plaintext, true
	}
	if errors.Is(err, errCorruptLocalEPM) {
		s.dropCorruptLocalEPM(peerID, encrypted, err)
	} else {
		log.Warnf("Local EPM for %s could not be decrypted (%v); skipping it. It is kept: a key-source change is not corruption.", peerID, err)
	}
	return nil, false
}

// dropCorruptLocalEPM deletes a corrupt row in the background (readers hold
// the store's read lock) and journals the delete. The delete is conditioned
// on the exact bytes, so a good row written meanwhile is never removed.
func (s *FlatSQLStore) dropCorruptLocalEPM(peerID, encrypted string, cause error) {
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return
	}
	log.Warnf("Deleting corrupt local EPM for %s: %v", peerID, cause)
	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		record := auxiliaryLocalEPMRecord{PeerID: peerID, EncryptedEPMBytes: encrypted}
		if err := s.applyLocalEPMDelete(record); err != nil {
			log.Warnf("Could not delete corrupt local EPM for %s: %v", peerID, err)
			return
		}
		if err := s.appendAuxiliaryMetadata(auxiliaryMetadataEvent{Kind: auxiliaryEventLocalEPMDelete, LocalEPM: &record}); err != nil {
			log.Warnf("Could not journal the corrupt local EPM delete for %s: %v", peerID, err)
		}
	}()
}

func (s *FlatSQLStore) applyLocalEPMDelete(record auxiliaryLocalEPMRecord) error {
	if s.db == nil {
		return nil
	}
	if _, err := s.auxWrite().Exec(`DELETE FROM sdn_local_epms WHERE peer_id = ? AND encrypted_epm_bytes = ?`,
		strings.TrimSpace(record.PeerID), record.EncryptedEPMBytes); err != nil {
		return fmt.Errorf("delete local EPM record: %w", err)
	}
	return nil
}

type localEPMEnvelope struct {
	Version    int    `json:"version"`
	Algorithm  string `json:"algorithm"`
	KDF        string `json:"kdf"`
	IV         string `json:"iv"`
	Ciphertext string `json:"ciphertext"`
}

func (s *FlatSQLStore) encryptLocalEPMPayload(plaintext []byte) (string, error) {
	gcm, err := s.localEPMGCM()
	if err != nil {
		return "", err
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nil, iv, plaintext, []byte("EPM.fbs"))
	envelope := localEPMEnvelope{
		Version:    1,
		Algorithm:  "aes-256-gcm",
		KDF:        "scrypt-system-derived",
		IV:         base64.StdEncoding.EncodeToString(iv),
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// decryptLocalEPMPayload tries every key the node could plausibly have sealed
// the row under (see localEPMKeyPasswords). Rows written before a password
// source was added — or under a different one, since the daemon and CLI can
// resolve different sources for the same box — stay readable; new writes
// always seal under the first available source.
func (s *FlatSQLStore) decryptLocalEPMPayload(rawEnvelope string) ([]byte, error) {
	var envelope localEPMEnvelope
	if err := json.Unmarshal([]byte(rawEnvelope), &envelope); err != nil {
		return nil, fmt.Errorf("%w: envelope: %v", errCorruptLocalEPM, err)
	}
	iv, err := base64.StdEncoding.DecodeString(envelope.IV)
	if err != nil || len(iv) == 0 {
		return nil, fmt.Errorf("%w: iv: %v", errCorruptLocalEPM, err)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil || len(ciphertext) == 0 {
		return nil, fmt.Errorf("%w: ciphertext: %v", errCorruptLocalEPM, err)
	}
	keys, err := s.localEPMKeys()
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, key := range keys {
		gcm, err := localEPMGCMForKey(key)
		if err != nil {
			return nil, err
		}
		plaintext, err := gcm.Open(nil, iv, ciphertext, []byte("EPM.fbs"))
		if err == nil {
			return plaintext, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (s *FlatSQLStore) localEPMGCM() (cipher.AEAD, error) {
	keys, err := s.localEPMKeys()
	if err != nil {
		return nil, err
	}
	return localEPMGCMForKey(keys[0])
}

func localEPMGCMForKey(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// localEPMKeyPasswords returns the canonical write password first, followed
// by read-only recovery candidates used by historical local-EPM envelopes.
// New writes MUST use config.KeyPassword: it gives SDN_KEY_PASSWORD_FILE the
// same precedence and fail-closed behavior as every mnemonic/key lane.
func (s *FlatSQLStore) localEPMKeyPasswords() ([]string, error) {
	password, err := config.KeyPassword(nil)
	if err != nil {
		return nil, fmt.Errorf("resolve local EPM key password: %w", err)
	}
	if password == "" {
		password, err = keys.DeriveDefaultPassword()
		if err != nil {
			return nil, fmt.Errorf("derive machine-default local EPM key password: %w", err)
		}
	}
	passwords := make([]string, 0, 4)

	// This explicit EPM-only override predates the shared at-rest resolver and
	// remains an intentional migration/testing escape hatch. In normal node
	// operation it is unset, so the canonical password above selects writes.
	if p := strings.TrimSpace(os.Getenv("SDN_EPM_STORE_PASSWORD")); p != "" {
		passwords = append(passwords, p)
	}
	if len(passwords) == 0 || passwords[len(passwords)-1] != password {
		passwords = append(passwords, password)
	}

	// A password file can be introduced while rows remain sealed under the
	// prior machine-derived key. Keep that key as a read candidate, never as
	// the write key when an explicit password resolved above.
	derivedPassword, deriveErr := keys.DeriveDefaultPassword()
	if deriveErr == nil && derivedPassword != password {
		passwords = append(passwords, derivedPassword)
	}
	hostname, _ := os.Hostname()
	homeDir, _ := os.UserHomeDir()
	legacyPassword := strings.Join([]string{
		"sdn-local-epm",
		hostname,
		runtime.GOOS,
		runtime.GOARCH,
		homeDir,
		filepath.Dir(s.dbPath),
	}, "|")
	if legacyPassword != password {
		passwords = append(passwords, legacyPassword)
	}
	return passwords, nil
}

// localEPMKeys derives (and memoizes — scrypt is ~100ms per candidate) the
// AES keys for every candidate password, in localEPMKeyPasswords order.
func (s *FlatSQLStore) localEPMKeys() ([][]byte, error) {
	s.localEPMKeyOnce.Do(func() {
		salt := sha256.Sum256([]byte(localEPMStoreSalt + "|" + s.dbPath))
		passwords, err := s.localEPMKeyPasswords()
		if err != nil {
			s.localEPMKeyErr = err
			return
		}
		for _, password := range passwords {
			key, err := scrypt.Key([]byte(password), salt[:], 32768, 8, 1, 32)
			if err != nil {
				s.localEPMKeyErr = err
				return
			}
			s.localEPMKeyCache = append(s.localEPMKeyCache, key)
		}
	})
	if s.localEPMKeyErr != nil {
		return nil, s.localEPMKeyErr
	}
	return s.localEPMKeyCache, nil
}

// RebuildIndex scans all schema tables and repopulates sdn_record_index.
func (s *FlatSQLStore) RebuildIndex() (map[string]int64, error) {
	if err := s.requireWritable("reindex"); err != nil {
		return nil, err
	}
	defer s.lockWrite("RebuildIndex")()

	summary := make(map[string]int64)

	for _, schemaName := range s.validator.Schemas() {
		tableName, err := s.recordReadSource(schemaName)
		if err != nil {
			return nil, fmt.Errorf("invalid schema name %q: %w", schemaName, err)
		}
		rows, err := s.db.Query(fmt.Sprintf(`SELECT cid, timestamp, data FROM %s`, tableName))
		if err != nil {
			return nil, fmt.Errorf("failed to query %s for reindex: %w", tableName, err)
		}

		var indexed int64
		for rows.Next() {
			var cid string
			var ts int64
			var stored []byte
			if err := rows.Scan(&cid, &ts, &stored); err != nil {
				rows.Close()
				return nil, fmt.Errorf("failed to scan %s row: %w", tableName, err)
			}
			data, err := s.openStoredRecordBytes(schemaName, stored)
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("failed to open %s/%s record: %w", schemaName, cid, err)
			}
			if err := s.upsertRecordIndex(schemaName, cid, ts, data); err != nil {
				log.Debugf("Skipping index row for %s/%s: %v", schemaName, cid, err)
				continue
			}
			indexed++
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("failed closing rows for %s: %w", tableName, err)
		}
		summary[schemaName] = indexed
	}

	return summary, nil
}

// QueryByIndexedFields returns records for schema/day/object filters.
// day uses YYYY-MM-DD in UTC and is optional.
func (s *FlatSQLStore) QueryByIndexedFields(schemaName, day string, noradCatID *uint32, entityID string, limit int) ([]*Record, error) {
	return s.QueryIndexedRecords(IndexedRecordQuery{
		SchemaName: schemaName,
		Day:        day,
		NoradCatID: noradCatID,
		EntityID:   entityID,
		Limit:      limit,
	})
}

// QueryRecentRecords returns recent records directly from the schema table.
// It avoids the materialized index join for unfiltered consumers that do not
// require day/object predicates or source-batch snapshot semantics.
func (s *FlatSQLStore) QueryRecentRecords(schemaName string, limit int) ([]*Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tableName, err := s.recordReadSource(schemaName)
	if err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 250000 {
		limit = 250000
	}

	taggedQuery := fmt.Sprintf(`
		SELECT d.cid, d.peer_id, d.timestamp,
		       d.data, d.signature_hex,
		       tags.provider_id, tags.source_name, tags.source_url, tags.batch_id,
		       tags.content_key_id, tags.producer_peer_id, tags.producer_public_key, tags.created_at
		FROM sdn_record_source_tags tags
		INNER JOIN %s d ON d.cid = tags.cid
		WHERE tags.schema_name = ?
		ORDER BY tags.created_at DESC, d.rowid DESC
		LIMIT ?
	`, tableName)
	rows, err := s.db.Query(taggedQuery, schemaName, limit)
	if err != nil {
		return nil, fmt.Errorf("recent records query failed: %w", err)
	}

	records, err := s.scanRecentRecords(schemaName, rows)
	if err != nil {
		return nil, err
	}
	if len(records) >= limit {
		return records, nil
	}

	untaggedLimit := limit - len(records)
	untaggedQuery := fmt.Sprintf(`
		SELECT d.cid, d.peer_id, d.timestamp,
		       d.data, d.signature_hex,
		       '', '', '', '', '', '', '', NULL
		FROM %s d
		WHERE NOT EXISTS (
			SELECT 1
			FROM sdn_record_source_tags tags
			WHERE tags.schema_name = ? AND tags.cid = d.cid
		)
		ORDER BY d.rowid DESC
		LIMIT ?
	`, tableName)
	rows, err = s.db.Query(untaggedQuery, schemaName, untaggedLimit)
	if err != nil {
		return nil, fmt.Errorf("recent untagged records query failed: %w", err)
	}
	untagged, err := s.scanRecentRecords(schemaName, rows)
	if err != nil {
		return nil, err
	}
	records = append(records, untagged...)
	return records, nil
}

// DataSummary returns aggregate FlatSQL record counts and raw byte totals.
type unsummarizedCount struct {
	count int64
	bytes int64
	at    time.Time
}

// unsummarizedCountTTL bounds how stale a provisional (scanned) schema count
// may be before DataSummary scans the table again.
const unsummarizedCountTTL = 60 * time.Second

// unsummarizedSchemaCountLocked answers a schema's record count and byte
// total by table scan, at most once per unsummarizedCountTTL per schema.
// Callers hold s.mu (read side is enough; the cache has its own lock).
func (s *FlatSQLStore) unsummarizedSchemaCountLocked(schemaName, tableName string) (int64, int64, error) {
	now := time.Now()
	s.unsummarizedMu.Lock()
	if c, ok := s.unsummarizedCounts[schemaName]; ok && now.Sub(c.at) < unsummarizedCountTTL {
		s.unsummarizedMu.Unlock()
		return c.count, c.bytes, nil
	}
	s.unsummarizedMu.Unlock()

	var count int64
	var totalBytes sql.NullInt64
	if err := s.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*), COALESCE(SUM(record_length), 0) FROM %s`, tableName)).Scan(&count, &totalBytes); err != nil {
		return 0, 0, err
	}
	s.unsummarizedMu.Lock()
	if s.unsummarizedCounts == nil {
		s.unsummarizedCounts = map[string]unsummarizedCount{}
	}
	s.unsummarizedCounts[schemaName] = unsummarizedCount{count: count, bytes: totalBytes.Int64, at: now}
	s.unsummarizedMu.Unlock()
	return count, totalBytes.Int64, nil
}

func (s *FlatSQLStore) DataSummary() (*DataSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	summary := &DataSummary{
		Schemas: make([]DataSchemaSummary, 0),
		Sources: make([]DataSourceSummary, 0),
	}
	summarizedSchemas := map[string]bool{}

	schemaRows, err := s.db.Query(`
		SELECT schema_name, COALESCE(SUM(record_count), 0), COALESCE(SUM(total_bytes), 0)
		FROM sdn_record_source_summary
		GROUP BY schema_name
		ORDER BY schema_name
	`)
	if err != nil {
		return nil, fmt.Errorf("summarize source-backed schemas: %w", err)
	}
	for schemaRows.Next() {
		var schema DataSchemaSummary
		if err := schemaRows.Scan(&schema.SchemaName, &schema.Count, &schema.TotalBytes); err != nil {
			schemaRows.Close()
			return nil, fmt.Errorf("scan source-backed schema summary: %w", err)
		}
		if schema.Count > 0 {
			summary.Schemas = append(summary.Schemas, schema)
			summary.TotalRecords += schema.Count
			summary.TotalBytes += schema.TotalBytes
			summarizedSchemas[schema.SchemaName] = true
		}
	}
	if err := schemaRows.Close(); err != nil {
		return nil, fmt.Errorf("close source-backed schema summary rows: %w", err)
	}

	sourceRows, err := s.db.Query(`
		SELECT schema_name, provider_id, source_name, batch_id, producer_peer_id,
		       producer_public_key, record_count, total_bytes
		FROM sdn_record_source_summary
		WHERE record_count > 0
		ORDER BY schema_name ASC, provider_id ASC, source_name ASC, batch_id ASC,
		         producer_peer_id ASC, producer_public_key ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("summarize source-backed producers: %w", err)
	}
	for sourceRows.Next() {
		var source DataSourceSummary
		if err := sourceRows.Scan(
			&source.SchemaName,
			&source.ProviderID,
			&source.SourceName,
			&source.BatchID,
			&source.ProducerPeerID,
			&source.ProducerPublicKey,
			&source.Count,
			&source.TotalBytes,
		); err != nil {
			sourceRows.Close()
			return nil, fmt.Errorf("scan source-backed producer summary: %w", err)
		}
		summary.Sources = append(summary.Sources, source)
	}
	if err := sourceRows.Close(); err != nil {
		return nil, fmt.Errorf("close source-backed producer summary rows: %w", err)
	}

	for _, schemaName := range s.validator.Schemas() {
		if summarizedSchemas[schemaName] {
			continue
		}
		tableName, err := s.recordReadSource(schemaName)
		if err != nil {
			return nil, fmt.Errorf("invalid schema name %q: %w", schemaName, err)
		}
		count, bytesTotal, err := s.unsummarizedSchemaCountLocked(schemaName, tableName)
		if err != nil {
			return nil, fmt.Errorf("summarize %s: %w", schemaName, err)
		}
		if count > 0 {
			summary.Schemas = append(summary.Schemas, DataSchemaSummary{
				SchemaName: schemaName,
				Count:      count,
				TotalBytes: bytesTotal,
			})
			summary.TotalRecords += count
			summary.TotalBytes += bytesTotal
		}
	}

	localCount, localBytes, err := s.localEPMSummaryLocked()
	if err != nil {
		return nil, err
	}
	if localCount > 0 {
		summary.Schemas = appendOrAddSchemaSummary(summary.Schemas, DataSchemaSummary{
			SchemaName: "EPM.fbs",
			Count:      localCount,
			TotalBytes: localBytes,
		})
		summary.Sources = append(summary.Sources, DataSourceSummary{
			SchemaName:        "EPM.fbs",
			ProviderID:        "local-node",
			SourceName:        "local-epm",
			BatchID:           "local",
			ProducerPeerID:    "local-node",
			ProducerPublicKey: "local-node",
			Count:             localCount,
			TotalBytes:        localBytes,
		})
		summary.TotalRecords += localCount
		summary.TotalBytes += localBytes
	}

	return summary, nil
}

// SourceBatchProgress returns one live progress row per
// (schema, provider, source, batch) with a positive record count, aggregating
// across producer identities. Counts/bytes/updated_at come from the maintained
// source-summary table; the first/last record arrival times come from the
// per-record source-tags created_at column. It is a read-only aggregate, safe
// for the anonymous /api/v1/stats pipeline-progress surface.
func (s *FlatSQLStore) SourceBatchProgress() ([]SourceBatchProgress, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// ONE grouped scan of the summary, which is ALREADY the per-lane aggregate:
	// tens of rows. The previous shape LEFT JOINed a `GROUP BY schema, provider,
	// source, batch` over the WHOLE sdn_record_source_tags table — one row per
	// RECORD — purely to recover first/last seen. On host-01 that measured
	// **37.6 s and 90.0 s of engine hold** (2026-08-09 slow-statement log, 1.6 M
	// tag rows), and because the engine is single-threaded behind one lock,
	// every other reader on the box paid the same wait — one `sqlite_master`
	// lookup was logged waiting the full 1 m 29.989 s behind it.
	//
	// This is the SAME conversion ProducerSourceProgress already went through
	// for the same reason (see its comment): first_seen lives on the summary,
	// and last_seen is the summary's own updated_at, which is stamped by
	// incrementSourceSummary on every landed record. Legacy rows carry
	// first_seen = 0 and are reported as unknown rather than as 1970, which is
	// what NULLIF does here.
	rows, err := s.db.Query(`
		SELECT ss.schema_name, ss.provider_id, ss.source_name, ss.batch_id,
		       SUM(ss.record_count) AS count,
		       SUM(ss.total_bytes) AS total_bytes,
		       MAX(ss.updated_at) AS updated_at,
		       MIN(NULLIF(ss.first_seen, 0)) AS first_seen,
		       MAX(ss.updated_at) AS last_seen
		FROM sdn_record_source_summary ss
		GROUP BY ss.schema_name, ss.provider_id, ss.source_name, ss.batch_id
		HAVING SUM(ss.record_count) > 0
		ORDER BY ss.schema_name ASC, ss.provider_id ASC, ss.source_name ASC, ss.batch_id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("query source batch progress: %w", err)
	}
	defer rows.Close()

	out := make([]SourceBatchProgress, 0)
	for rows.Next() {
		var p SourceBatchProgress
		var updatedAt, firstSeen, lastSeen sql.NullInt64
		if err := rows.Scan(
			&p.SchemaName, &p.ProviderID, &p.SourceName, &p.BatchID,
			&p.Count, &p.TotalBytes, &updatedAt, &firstSeen, &lastSeen,
		); err != nil {
			return nil, fmt.Errorf("scan source batch progress: %w", err)
		}
		p.UpdatedAtUnix = updatedAt.Int64
		p.FirstSeenUnix = firstSeen.Int64
		p.LastSeenUnix = lastSeen.Int64
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate source batch progress: %w", err)
	}
	return out, nil
}

// ProducerSourceProgress is what ONE PRODUCER has contributed to one
// (schema, provider, source) lane on this node.
//
// SourceBatchProgress deliberately aggregates across producer identities: it
// answers "how much data is here", which is the anonymous progress question.
// This answers the other one — "who sent it" — which is what a node needs to
// show data it did not pull itself. Records that arrive over pubsub carry their
// producer's peer id in the source tags; a node that only ever reported its own
// retrieval ledger could receive a full catalog from a peer and still show an
// empty board.
type ProducerSourceProgress struct {
	ProducerPeerID string
	SchemaName     string
	ProviderID     string
	SourceName     string
	// LastBatchID is the most recently updated batch in this lane, and
	// BatchCount is how many distinct batches this producer has contributed.
	LastBatchID   string
	BatchCount    int64
	Count         int64
	TotalBytes    int64
	FirstSeenUnix int64
	LastSeenUnix  int64
	UpdatedAtUnix int64
}

// ProducerSourceProgress reports per-producer contribution to each source lane,
// most recently updated first. Read-only; no side effects.
func (s *FlatSQLStore) ProducerSourceProgress() ([]ProducerSourceProgress, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// One grouped scan of the summary, which is ALREADY the per-lane aggregate:
	// one row per (schema, provider, source, batch, producer). The previous
	// shape joined a GROUP BY over the whole sdn_record_source_tags table —
	// one row per RECORD — purely to recover first/last seen. On a node holding
	// a real catalog that join never finished inside the feed's read budget, so
	// the endpoint silently fell back to local-only rows and every peer filling
	// this node's store was invisible. first_seen now lives on the summary, and
	// the newest batch comes from the same scan instead of a per-row follow-up
	// query.
	rows, err := s.db.Query(`
		SELECT ss.producer_peer_id, ss.schema_name, ss.provider_id, ss.source_name,
		       COUNT(DISTINCT ss.batch_id) AS batch_count,
		       SUM(ss.record_count) AS count,
		       SUM(ss.total_bytes) AS total_bytes,
		       MAX(ss.updated_at) AS updated_at,
		       MIN(NULLIF(ss.first_seen, 0)) AS first_seen,
		       MAX(ss.updated_at) AS last_seen,
		       (
		         SELECT b.batch_id
		         FROM sdn_record_source_summary b
		         WHERE b.producer_peer_id = ss.producer_peer_id
		           AND b.schema_name      = ss.schema_name
		           AND b.provider_id      = ss.provider_id
		           AND b.source_name      = ss.source_name
		           AND b.record_count     > 0
		         ORDER BY b.updated_at DESC, b.max_rowid DESC
		         LIMIT 1
		       ) AS last_batch_id
		FROM sdn_record_source_summary ss
		GROUP BY ss.producer_peer_id, ss.schema_name, ss.provider_id, ss.source_name
		HAVING SUM(ss.record_count) > 0
		ORDER BY updated_at DESC, ss.producer_peer_id ASC, ss.schema_name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("query producer source progress: %w", err)
	}
	defer rows.Close()

	out := make([]ProducerSourceProgress, 0)
	for rows.Next() {
		var p ProducerSourceProgress
		var updatedAt, firstSeen, lastSeen sql.NullInt64
		var lastBatchID sql.NullString
		if err := rows.Scan(
			&p.ProducerPeerID, &p.SchemaName, &p.ProviderID, &p.SourceName,
			&p.BatchCount, &p.Count, &p.TotalBytes, &updatedAt, &firstSeen, &lastSeen,
			&lastBatchID,
		); err != nil {
			return nil, fmt.Errorf("scan producer source progress: %w", err)
		}
		p.UpdatedAtUnix = updatedAt.Int64
		p.FirstSeenUnix = firstSeen.Int64
		p.LastSeenUnix = lastSeen.Int64
		p.LastBatchID = strings.TrimSpace(lastBatchID.String)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate producer source progress: %w", err)
	}
	return out, nil
}

// latestBatchForProducerLocked names the most recently updated batch in one
// producer's lane. Best-effort: an empty answer costs a label, never a row.
// Caller holds s.mu.
func (s *FlatSQLStore) latestBatchForProducerLocked(producerPeerID, schemaName, providerID, sourceName string) string {
	var batchID string
	err := s.db.QueryRow(`
		SELECT batch_id FROM sdn_record_source_summary
		WHERE producer_peer_id = ? AND schema_name = ? AND provider_id = ? AND source_name = ?
		ORDER BY updated_at DESC, batch_id DESC
		LIMIT 1`,
		producerPeerID, schemaName, providerID, sourceName).Scan(&batchID)
	if err != nil {
		return ""
	}
	return batchID
}

// RecordIndexPageQuery filters the anonymous per-record index-page surface
// (/api/v1/data/index): one indexed record per row, optionally restricted to a
// source lane (provider/source/batch) and/or a NORAD substring.
type RecordIndexPageQuery struct {
	SchemaName string
	ProviderID string
	SourceName string
	BatchID    string
	// NoradLike is a bare NORAD substring matched against CAST(norad_cat_id AS
	// TEXT) LIKE '%<q>%'. Callers must pass digits only (LIKE wildcards are not
	// escaped here — the HTTP handler sanitizes to digits).
	NoradLike string
	Limit     int
	Offset    int
}

// RecordIndexRow is one per-record row of the index-page surface. Pointer fields
// are nil when the underlying index column is NULL (honest absence).
type RecordIndexRow struct {
	NoradCatID *int64
	EpochUnix  *int64
	CID        string
}

// RecordIndexPage returns a page of indexed records (newest EPOCH first) plus
// the total match count, over sdn_record_index joined to sdn_record_source_tags
// for the source-lane filter. No FlatBuffer payloads are hydrated.
func (s *FlatSQLStore) RecordIndexPage(q RecordIndexPageQuery) ([]RecordIndexRow, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	where := []string{"ix.schema_name = ?"}
	args := []interface{}{q.SchemaName}

	// Source attribution filter — restrict to records carrying a matching source
	// tag. Applied via EXISTS (never a JOIN) so a record with several producer
	// tag rows is not multiplied. Skipped entirely when no lane filter is set.
	var tagConds []string
	var tagArgs []interface{}
	if strings.TrimSpace(q.ProviderID) != "" {
		tagConds = append(tagConds, "tg.provider_id = ?")
		tagArgs = append(tagArgs, q.ProviderID)
	}
	if strings.TrimSpace(q.SourceName) != "" {
		tagConds = append(tagConds, "tg.source_name = ?")
		tagArgs = append(tagArgs, q.SourceName)
	}
	if strings.TrimSpace(q.BatchID) != "" {
		tagConds = append(tagConds, "tg.batch_id = ?")
		tagArgs = append(tagArgs, q.BatchID)
	}
	if len(tagConds) > 0 {
		where = append(where, "EXISTS (SELECT 1 FROM sdn_record_source_tags tg "+
			"WHERE tg.schema_name = ix.schema_name AND tg.cid = ix.cid AND "+
			strings.Join(tagConds, " AND ")+")")
		args = append(args, tagArgs...)
	}
	if q.NoradLike != "" {
		where = append(where, "CAST(ix.norad_cat_id AS TEXT) LIKE ?")
		args = append(args, "%"+q.NoradLike+"%")
	}
	whereSQL := strings.Join(where, " AND ")

	var total int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sdn_record_index ix WHERE `+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count record index page: %w", err)
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	pageArgs := append(append([]interface{}{}, args...), limit, offset)
	rows, err := s.db.Query(`
		SELECT ix.norad_cat_id, ix.epoch_unix, ix.cid
		FROM sdn_record_index ix
		WHERE `+whereSQL+`
		ORDER BY ix.epoch_unix DESC, ix.cid ASC
		LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("query record index page: %w", err)
	}
	defer rows.Close()

	out := make([]RecordIndexRow, 0, limit)
	for rows.Next() {
		var norad, epoch sql.NullInt64
		var cid string
		if err := rows.Scan(&norad, &epoch, &cid); err != nil {
			return nil, 0, fmt.Errorf("scan record index page: %w", err)
		}
		row := RecordIndexRow{CID: cid}
		if norad.Valid {
			v := norad.Int64
			row.NoradCatID = &v
		}
		if epoch.Valid {
			v := epoch.Int64
			row.EpochUnix = &v
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate record index page: %w", err)
	}
	return out, total, nil
}

// CountRawRecords returns a filtered raw-record count without hydrating
// FlatBuffer payloads from stream files.
func (s *FlatSQLStore) CountRawRecords(filter RawRecordQuery) (int64, error) {
	if err := s.CheckFullTextSearch(filter.SchemaName, filter.Search); err != nil {
		return 0, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.countRawRecordsLocked(filter)
}

func (s *FlatSQLStore) countRawRecordsLocked(filter RawRecordQuery) (int64, error) {
	if err := s.checkFullTextReadyLocked(filter); err != nil {
		return 0, err
	}
	filter.SchemaName = strings.TrimSpace(filter.SchemaName)
	if filter.SchemaName == "" {
		return 0, errors.New("schema name is required")
	}
	tableName, err := s.rawRecordReadSource(filter.SchemaName)
	if err != nil {
		return 0, fmt.Errorf("invalid schema name: %w", err)
	}

	sourceFiltered := strings.TrimSpace(filter.ProviderID) != "" ||
		strings.TrimSpace(filter.SourceName) != "" ||
		strings.TrimSpace(filter.BatchID) != "" ||
		strings.TrimSpace(filter.ProducerPeerID) != "" ||
		strings.TrimSpace(filter.ProducerPublicKey) != ""
	indexFilter, err := compileRawRecordSyncFilter(filter)
	if err != nil {
		return 0, err
	}
	indexFiltered := indexFilter.active()

	if !sourceFiltered && !indexFiltered {
		total, err := s.countSchemaRecordsLocked(tableName, filter)
		if err != nil {
			return 0, err
		}
		if filter.SchemaName == "EPM.fbs" && localEPMFilterMatches(filter) {
			localCount, err := s.countLocalEPMRecordsLocked(filter)
			if err != nil {
				return 0, err
			}
			total += localCount
		}
		return total, nil
	}

	if !indexFiltered && strings.TrimSpace(filter.PeerID) == "" && strings.TrimSpace(filter.CID) == "" {
		total, ok, err := s.countSourceSummaryRecordsLocked(filter)
		if err != nil {
			return 0, err
		}
		if ok {
			if filter.SchemaName == "EPM.fbs" && localEPMFilterMatches(filter) {
				localCount, err := s.countLocalEPMRecordsLocked(filter)
				if err != nil {
					return 0, err
				}
				total += localCount
			}
			return total, nil
		}
	}

	taggedQuery := fmt.Sprintf(`
		SELECT COUNT(*)
		FROM %s
		INNER JOIN %s records ON records.cid = tags.cid
	`, rawRecordSourceTagsReadSource(filter), tableName)
	if indexFiltered {
		taggedQuery += `
		INNER JOIN sdn_record_index idx
		  ON idx.schema_name = tags.schema_name AND idx.cid = records.cid
		`
	}
	taggedQuery += ` WHERE tags.schema_name = ?`
	args := []interface{}{filter.SchemaName}

	if peerID := strings.TrimSpace(filter.PeerID); peerID != "" {
		taggedQuery += ` AND records.peer_id = ?`
		args = append(args, peerID)
	}
	if cid := strings.TrimSpace(filter.CID); cid != "" {
		taggedQuery += ` AND records.cid = ?`
		args = append(args, cid)
	}
	if providerID := strings.TrimSpace(filter.ProviderID); providerID != "" {
		taggedQuery += ` AND tags.provider_id = ?`
		args = append(args, providerID)
	}
	if sourceName := strings.TrimSpace(filter.SourceName); sourceName != "" {
		taggedQuery += ` AND tags.source_name = ?`
		args = append(args, sourceName)
	}
	if batchID := strings.TrimSpace(filter.BatchID); batchID != "" {
		taggedQuery += ` AND tags.batch_id = ?`
		args = append(args, batchID)
	}
	if producerPeerID := strings.TrimSpace(filter.ProducerPeerID); producerPeerID != "" {
		taggedQuery += ` AND tags.producer_peer_id = ?`
		args = append(args, producerPeerID)
	}
	if producerPublicKey := strings.TrimSpace(filter.ProducerPublicKey); producerPublicKey != "" {
		taggedQuery += ` AND tags.producer_public_key = ?`
		args = append(args, producerPublicKey)
	}
	taggedQuery, args = appendRawRecordSyncFilterWhere(taggedQuery, args, indexFilter)

	var total int64
	if err := s.db.QueryRow(taggedQuery, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("raw tagged record count failed: %w", err)
	}

	if !sourceFiltered {
		untaggedQuery := fmt.Sprintf(`
			SELECT COUNT(*)
			FROM %s records
		`, tableName)
		untaggedArgs := make([]interface{}, 0, len(indexFilter.args)+3)
		if indexFiltered {
			untaggedQuery += `
			INNER JOIN sdn_record_index idx
			  ON idx.schema_name = ? AND idx.cid = records.cid
			`
			untaggedArgs = append(untaggedArgs, filter.SchemaName)
		}
		untaggedQuery += `
			WHERE NOT EXISTS (
				SELECT 1
				FROM sdn_record_source_tags tags
				WHERE tags.schema_name = ? AND tags.cid = records.cid
			)
		`
		untaggedArgs = append(untaggedArgs, filter.SchemaName)
		if peerID := strings.TrimSpace(filter.PeerID); peerID != "" {
			untaggedQuery += ` AND records.peer_id = ?`
			untaggedArgs = append(untaggedArgs, peerID)
		}
		if cid := strings.TrimSpace(filter.CID); cid != "" {
			untaggedQuery += ` AND records.cid = ?`
			untaggedArgs = append(untaggedArgs, cid)
		}
		untaggedQuery, untaggedArgs = appendRawRecordSyncFilterWhere(untaggedQuery, untaggedArgs, indexFilter)
		var untagged int64
		if err := s.db.QueryRow(untaggedQuery, untaggedArgs...).Scan(&untagged); err != nil {
			return 0, fmt.Errorf("raw untagged record count failed: %w", err)
		}
		total += untagged
	}

	if !indexFiltered && filter.SchemaName == "EPM.fbs" && localEPMFilterMatches(filter) {
		localCount, err := s.countLocalEPMRecordsLocked(filter)
		if err != nil {
			return 0, err
		}
		total += localCount
	}

	return total, nil
}

// RawRecordHead returns cursor/snapshot metadata for a raw-record result set
// without opening the FlatSQL backing stream files.
func (s *FlatSQLStore) RawRecordHead(filter RawRecordQuery) (RawRecordHead, error) {
	if err := s.CheckFullTextSearch(filter.SchemaName, filter.Search); err != nil {
		return RawRecordHead{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rawRecordHeadLocked(filter)
}

// RawRecordSnapshot reads the count and head under one catalog lock so live
// ingestion cannot place a newer boundary beside an older result count.
func (s *FlatSQLStore) RawRecordSnapshot(filter RawRecordQuery) (int64, RawRecordHead, error) {
	if err := s.CheckFullTextSearch(filter.SchemaName, filter.Search); err != nil {
		return 0, RawRecordHead{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	count, err := s.countRawRecordsLocked(filter)
	if err != nil {
		return 0, RawRecordHead{}, err
	}
	head, err := s.rawRecordHeadLocked(filter)
	return count, head, err
}

func (s *FlatSQLStore) rawRecordHeadLocked(filter RawRecordQuery) (RawRecordHead, error) {
	if err := s.checkFullTextReadyLocked(filter); err != nil {
		return RawRecordHead{}, err
	}

	filter.SchemaName = strings.TrimSpace(filter.SchemaName)
	if filter.SchemaName == "" {
		return RawRecordHead{}, errors.New("schema name is required")
	}
	tableName, err := s.rawRecordReadSource(filter.SchemaName)
	if err != nil {
		return RawRecordHead{}, fmt.Errorf("invalid schema name: %w", err)
	}

	sourceFiltered := strings.TrimSpace(filter.ProviderID) != "" ||
		strings.TrimSpace(filter.SourceName) != "" ||
		strings.TrimSpace(filter.BatchID) != "" ||
		strings.TrimSpace(filter.ProducerPeerID) != "" ||
		strings.TrimSpace(filter.ProducerPublicKey) != ""
	indexFilter, err := compileRawRecordSyncFilter(filter)
	if err != nil {
		return RawRecordHead{}, err
	}
	indexFiltered := indexFilter.active()

	var head RawRecordHead
	if sourceFiltered && !indexFiltered && strings.TrimSpace(filter.PeerID) == "" && strings.TrimSpace(filter.CID) == "" {
		summary, ok, err := s.rawSourceSummaryHeadLocked(filter)
		if err != nil {
			return RawRecordHead{}, err
		}
		if ok {
			head = summary
			// The cursor boundary must be the GLOBAL index rowid (WS7.3d), the
			// same sequence the paginating scan orders by — NOT the summary's
			// max_rowid (still written from the legacy record-table rowid by
			// incrementSourceSummary, a different sequence). Always recompute
			// MaxRowID live from MAX(idx.rowid) when a rowid cursor is in play.
			if filter.UseRowIDCursor {
				schemaHead, err := s.rawSchemaRecordHeadLocked(tableName, RawRecordQuery{
					SchemaName: filter.SchemaName,
				})
				if err != nil {
					return RawRecordHead{}, err
				}
				head.MaxRowID = schemaHead.MaxRowID
			}
		}
		if ok && (!filter.UseRowIDCursor || head.MaxRowID > 0) {
			if filter.SchemaName == "EPM.fbs" && localEPMFilterMatches(filter) {
				localCount, localBytes, err := s.localEPMSummaryLocked()
				if err != nil {
					return RawRecordHead{}, err
				}
				head.TotalBytes += localBytes
				if localCount > 0 {
					now := time.Now().Unix()
					head.MaxRecordTimestampUnix = max(head.MaxRecordTimestampUnix, now)
					head.MaxCreatedAtUnix = max(head.MaxCreatedAtUnix, now)
				}
			}
			return head, nil
		}
	}

	if sourceFiltered {
		head, err = s.rawTaggedRecordHeadLocked(tableName, filter)
	} else {
		head, err = s.rawSchemaRecordHeadLocked(tableName, filter)
	}
	if err != nil {
		return RawRecordHead{}, err
	}

	if !indexFiltered && filter.SchemaName == "EPM.fbs" && localEPMFilterMatches(filter) {
		localCount, localBytes, err := s.localEPMSummaryLocked()
		if err != nil {
			return RawRecordHead{}, err
		}
		head.TotalBytes += localBytes
		if localCount > 0 {
			now := time.Now().Unix()
			head.MaxRecordTimestampUnix = max(head.MaxRecordTimestampUnix, now)
			head.MaxCreatedAtUnix = max(head.MaxCreatedAtUnix, now)
		}
	}

	return head, nil
}

func (s *FlatSQLStore) rawSourceSummaryHeadLocked(filter RawRecordQuery) (RawRecordHead, bool, error) {
	query := `
		SELECT COUNT(*), COALESCE(SUM(total_bytes), 0), COALESCE(MAX(updated_at), 0), COALESCE(MAX(max_rowid), 0)
		FROM sdn_record_source_summary
		WHERE schema_name = ?
	`
	args := []interface{}{filter.SchemaName}
	if providerID := strings.TrimSpace(filter.ProviderID); providerID != "" {
		query += ` AND provider_id = ?`
		args = append(args, providerID)
	}
	if sourceName := strings.TrimSpace(filter.SourceName); sourceName != "" {
		query += ` AND source_name = ?`
		args = append(args, sourceName)
	}
	if batchID := strings.TrimSpace(filter.BatchID); batchID != "" {
		query += ` AND batch_id = ?`
		args = append(args, batchID)
	}
	if producerPeerID := strings.TrimSpace(filter.ProducerPeerID); producerPeerID != "" {
		query += ` AND producer_peer_id = ?`
		args = append(args, producerPeerID)
	}
	if producerPublicKey := strings.TrimSpace(filter.ProducerPublicKey); producerPublicKey != "" {
		query += ` AND producer_public_key = ?`
		args = append(args, producerPublicKey)
	}

	var summaryRows int64
	var totalBytes int64
	var maxUpdated int64
	var maxRowID int64
	if err := s.db.QueryRow(query, args...).Scan(&summaryRows, &totalBytes, &maxUpdated, &maxRowID); err != nil {
		return RawRecordHead{}, false, fmt.Errorf("raw source summary head failed: %w", err)
	}
	return RawRecordHead{
		TotalBytes:             totalBytes,
		MaxSourceUpdatedAtUnix: maxUpdated,
		MaxCreatedAtUnix:       maxUpdated,
		MaxRowID:               maxRowID,
	}, summaryRows > 0, nil
}

func (s *FlatSQLStore) countSchemaRecordsLocked(tableName string, filter RawRecordQuery) (int64, error) {
	indexFilter, err := compileRawRecordSyncFilter(filter)
	if err != nil {
		return 0, err
	}
	query := fmt.Sprintf(`SELECT COUNT(*) FROM %s records`, tableName)
	args := make([]interface{}, 0, len(indexFilter.args)+3)
	if indexFilter.active() {
		query += `
		INNER JOIN sdn_record_index idx
		  ON idx.schema_name = ? AND idx.cid = records.cid
		`
		args = append(args, filter.SchemaName)
	}
	query += ` WHERE 1=1`
	if peerID := strings.TrimSpace(filter.PeerID); peerID != "" {
		query += ` AND records.peer_id = ?`
		args = append(args, peerID)
	}
	if cid := strings.TrimSpace(filter.CID); cid != "" {
		query += ` AND records.cid = ?`
		args = append(args, cid)
	}
	query, args = appendRawRecordSyncFilterWhere(query, args, indexFilter)

	var total int64
	if err := s.db.QueryRow(query, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("raw schema record count failed: %w", err)
	}
	return total, nil
}

func (s *FlatSQLStore) rawSchemaRecordHeadLocked(tableName string, filter RawRecordQuery) (RawRecordHead, error) {
	indexFilter, err := compileRawRecordSyncFilter(filter)
	if err != nil {
		return RawRecordHead{}, err
	}
	query := fmt.Sprintf(`
		SELECT COALESCE(SUM(records.record_length), 0), COALESCE(MAX(records.timestamp), 0),
		       COALESCE(MAX(records.created_at), 0), COALESCE(MAX(records.rowid), 0)
		FROM %s records
	`, tableName)
	args := make([]interface{}, 0, len(indexFilter.args)+3)
	if indexFilter.active() {
		query += `
		INNER JOIN sdn_record_index idx
		  ON idx.schema_name = ? AND idx.cid = records.cid
		`
		args = append(args, filter.SchemaName)
	}
	query += ` WHERE 1=1`
	if peerID := strings.TrimSpace(filter.PeerID); peerID != "" {
		query += ` AND records.peer_id = ?`
		args = append(args, peerID)
	}
	if cid := strings.TrimSpace(filter.CID); cid != "" {
		query += ` AND records.cid = ?`
		args = append(args, cid)
	}
	query, args = appendRawRecordSyncFilterWhere(query, args, indexFilter)

	var head RawRecordHead
	if err := s.db.QueryRow(query, args...).Scan(
		&head.TotalBytes,
		&head.MaxRecordTimestampUnix,
		&head.MaxCreatedAtUnix,
		&head.MaxRowID,
	); err != nil {
		return RawRecordHead{}, fmt.Errorf("raw schema record head failed: %w", err)
	}
	return head, nil
}

func (s *FlatSQLStore) rawTaggedRecordHeadLocked(tableName string, filter RawRecordQuery) (RawRecordHead, error) {
	indexFilter, err := compileRawRecordSyncFilter(filter)
	if err != nil {
		return RawRecordHead{}, err
	}
	query := fmt.Sprintf(`
		SELECT COALESCE(SUM(records.record_length), 0), COALESCE(MAX(records.timestamp), 0),
		       COALESCE(MAX(tags.created_at), 0), COALESCE(MAX(records.rowid), 0)
		FROM %s
		INNER JOIN %s records ON records.cid = tags.cid
	`, rawRecordSourceTagsReadSource(filter), tableName)
	if indexFilter.active() {
		query += `
		INNER JOIN sdn_record_index idx
		  ON idx.schema_name = tags.schema_name AND idx.cid = records.cid
		`
	}
	query += ` WHERE tags.schema_name = ?`
	args := []interface{}{filter.SchemaName}

	if peerID := strings.TrimSpace(filter.PeerID); peerID != "" {
		query += ` AND records.peer_id = ?`
		args = append(args, peerID)
	}
	if cid := strings.TrimSpace(filter.CID); cid != "" {
		query += ` AND records.cid = ?`
		args = append(args, cid)
	}
	if providerID := strings.TrimSpace(filter.ProviderID); providerID != "" {
		query += ` AND tags.provider_id = ?`
		args = append(args, providerID)
	}
	if sourceName := strings.TrimSpace(filter.SourceName); sourceName != "" {
		query += ` AND tags.source_name = ?`
		args = append(args, sourceName)
	}
	if batchID := strings.TrimSpace(filter.BatchID); batchID != "" {
		query += ` AND tags.batch_id = ?`
		args = append(args, batchID)
	}
	if producerPeerID := strings.TrimSpace(filter.ProducerPeerID); producerPeerID != "" {
		query += ` AND tags.producer_peer_id = ?`
		args = append(args, producerPeerID)
	}
	if producerPublicKey := strings.TrimSpace(filter.ProducerPublicKey); producerPublicKey != "" {
		query += ` AND tags.producer_public_key = ?`
		args = append(args, producerPublicKey)
	}
	query, args = appendRawRecordSyncFilterWhere(query, args, indexFilter)

	var head RawRecordHead
	if err := s.db.QueryRow(query, args...).Scan(
		&head.TotalBytes,
		&head.MaxRecordTimestampUnix,
		&head.MaxCreatedAtUnix,
		&head.MaxRowID,
	); err != nil {
		return RawRecordHead{}, fmt.Errorf("raw tagged record head failed: %w", err)
	}
	head.MaxSourceUpdatedAtUnix = head.MaxCreatedAtUnix
	return head, nil
}

func (s *FlatSQLStore) countSourceSummaryRecordsLocked(filter RawRecordQuery) (int64, bool, error) {
	query := `
		SELECT COUNT(*), COALESCE(SUM(record_count), 0)
		FROM sdn_record_source_summary
		WHERE schema_name = ?
	`
	args := []interface{}{filter.SchemaName}
	if providerID := strings.TrimSpace(filter.ProviderID); providerID != "" {
		query += ` AND provider_id = ?`
		args = append(args, providerID)
	}
	if sourceName := strings.TrimSpace(filter.SourceName); sourceName != "" {
		query += ` AND source_name = ?`
		args = append(args, sourceName)
	}
	if batchID := strings.TrimSpace(filter.BatchID); batchID != "" {
		query += ` AND batch_id = ?`
		args = append(args, batchID)
	}
	if producerPeerID := strings.TrimSpace(filter.ProducerPeerID); producerPeerID != "" {
		query += ` AND producer_peer_id = ?`
		args = append(args, producerPeerID)
	}
	if producerPublicKey := strings.TrimSpace(filter.ProducerPublicKey); producerPublicKey != "" {
		query += ` AND producer_public_key = ?`
		args = append(args, producerPublicKey)
	}

	var summaryRows int64
	var total int64
	if err := s.db.QueryRow(query, args...).Scan(&summaryRows, &total); err != nil {
		return 0, false, fmt.Errorf("raw source summary count failed: %w", err)
	}
	return total, summaryRows > 0, nil
}

const rawRecordMaxQueryLimit = 50000
const rawRecordRefLookupBatchSize = 500

const (
	rawRecordSourceCIDIndex     = "idx_sdn_record_source_tags_source_cid"
	rawRecordSourceNameCIDIndex = "idx_sdn_record_source_tags_source_name_cid"
)

// rawRecordSourceTagsReadSource selects an index whose leading columns match
// the source filter that was actually supplied. A source_name-only sync request
// cannot seek (schema_name, provider_id, source_name, cid): the missing
// provider_id prefix turns each correlated lookup into a scan of the schema's
// tag rows. The dedicated source-name index keeps count, head, and page reads
// bounded to the requested source while preserving the provider-prefixed path.
func rawRecordSourceTagsReadSource(filter RawRecordQuery) string {
	indexName := ""
	switch {
	case strings.TrimSpace(filter.ProviderID) == "" && strings.TrimSpace(filter.SourceName) != "":
		indexName = rawRecordSourceNameCIDIndex
	case strings.TrimSpace(filter.ProviderID) != "":
		indexName = rawRecordSourceCIDIndex
	}
	if indexName == "" {
		return "sdn_record_source_tags tags"
	}
	return "sdn_record_source_tags tags INDEXED BY " + indexName
}

// QueryRawRecords returns raw FlatBuffer records with metadata and source tags.
func (s *FlatSQLStore) QueryRawRecords(filter RawRecordQuery) ([]*Record, error) {
	if err := s.readGate(); err != nil {
		return nil, err
	}
	return s.queryRawRecords(filter, true)
}

// QueryRawRecordRefs returns metadata refs without opening FlatSQL backing
// files. Provider scan paths use this to keep counts/pages cheap.
func (s *FlatSQLStore) QueryRawRecordRefs(filter RawRecordQuery) ([]*Record, error) {
	return s.queryRawRecords(filter, false)
}

func appendRawRecordRowIDCursorWhere(query string, args []interface{}, qualifier string, filter RawRecordQuery) (string, []interface{}) {
	if !filter.UseRowIDCursor {
		return query, args
	}
	prefix := strings.TrimSpace(qualifier)
	if prefix != "" {
		prefix += "."
	}
	if filter.AfterRowID > 0 {
		query += fmt.Sprintf(` AND %srowid > ?`, prefix)
		args = append(args, filter.AfterRowID)
	}
	if filter.MaxRowID > 0 {
		query += fmt.Sprintf(` AND %srowid <= ?`, prefix)
		args = append(args, filter.MaxRowID)
	}
	return query, args
}

func appendRawRecordSourceTagFilters(query string, args []interface{}, qualifier string, filter RawRecordQuery) (string, []interface{}) {
	prefix := strings.TrimSpace(qualifier)
	if prefix != "" {
		prefix += "."
	}
	if providerID := strings.TrimSpace(filter.ProviderID); providerID != "" {
		query += fmt.Sprintf(` AND %sprovider_id = ?`, prefix)
		args = append(args, providerID)
	}
	if sourceName := strings.TrimSpace(filter.SourceName); sourceName != "" {
		query += fmt.Sprintf(` AND %ssource_name = ?`, prefix)
		args = append(args, sourceName)
	}
	if batchID := strings.TrimSpace(filter.BatchID); batchID != "" {
		query += fmt.Sprintf(` AND %sbatch_id = ?`, prefix)
		args = append(args, batchID)
	}
	if producerPeerID := strings.TrimSpace(filter.ProducerPeerID); producerPeerID != "" {
		query += fmt.Sprintf(` AND %sproducer_peer_id = ?`, prefix)
		args = append(args, producerPeerID)
	}
	if producerPublicKey := strings.TrimSpace(filter.ProducerPublicKey); producerPublicKey != "" {
		query += fmt.Sprintf(` AND %sproducer_public_key = ?`, prefix)
		args = append(args, producerPublicKey)
	}
	return query, args
}

func rawRecordRowIDSourceQuery(tableName string, filter RawRecordQuery) (string, []interface{}) {
	query := fmt.Sprintf(`
		WITH candidates AS (
			SELECT records.rowid, records.cid, records.peer_id, records.timestamp,
			       records.data, records.signature_hex
			FROM %s records
			WHERE 1 = 1
	`, tableName)
	args := make([]interface{}, 0, 16)
	if peerID := strings.TrimSpace(filter.PeerID); peerID != "" {
		query += ` AND records.peer_id = ?`
		args = append(args, peerID)
	}
	if cid := strings.TrimSpace(filter.CID); cid != "" {
		query += ` AND records.cid = ?`
		args = append(args, cid)
	}
	query, args = appendRawRecordRowIDCursorWhere(query, args, "records", filter)
	query += fmt.Sprintf(`
			  AND EXISTS (
				SELECT 1
				FROM %s
				WHERE tags.schema_name = ? AND tags.cid = records.cid
	`, rawRecordSourceTagsReadSource(filter))
	args = append(args, filter.SchemaName)
	query, args = appendRawRecordSourceTagFilters(query, args, "tags", filter)
	query += fmt.Sprintf(`
			)
			ORDER BY records.rowid ASC, records.cid ASC
			LIMIT ?
		)
		SELECT candidates.rowid, candidates.cid, candidates.peer_id, candidates.timestamp,
		       candidates.data, candidates.signature_hex,
		       tags.provider_id, tags.source_name, tags.source_url, tags.batch_id,
		       tags.content_key_id, tags.producer_peer_id, tags.producer_public_key, tags.created_at
		FROM candidates
		CROSS JOIN %s
		WHERE tags.schema_name = ? AND tags.cid = candidates.cid
	`, rawRecordSourceTagsReadSource(filter))
	args = append(args, filter.Limit, filter.SchemaName)
	query, args = appendRawRecordSourceTagFilters(query, args, "tags", filter)
	query += `
		ORDER BY candidates.rowid ASC, candidates.cid ASC
		LIMIT ?
	`
	args = append(args, filter.Limit)
	return query, args
}

func (s *FlatSQLStore) queryRawRecordsWithRowIDSourceCursorLocked(tableName string, filter RawRecordQuery, hydrate bool) ([]*Record, error) {
	query, args := rawRecordRowIDSourceQuery(tableName, filter)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("raw record rowid source query failed: %w", err)
	}
	return s.scanRawRecordRows(filter.SchemaName, rows, hydrate)
}

func (s *FlatSQLStore) queryRawRecords(filter RawRecordQuery, hydrate bool) ([]*Record, error) {
	if err := s.CheckFullTextSearch(filter.SchemaName, filter.Search); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.checkFullTextReadyLocked(filter); err != nil {
		return nil, err
	}

	filter.SchemaName = strings.TrimSpace(filter.SchemaName)
	if filter.SchemaName == "" {
		return nil, errors.New("schema name is required")
	}
	tableName, err := s.rawRecordReadSource(filter.SchemaName)
	if err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	if filter.Limit > rawRecordMaxQueryLimit {
		filter.Limit = rawRecordMaxQueryLimit
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}

	sourceFiltered := strings.TrimSpace(filter.ProviderID) != "" ||
		strings.TrimSpace(filter.SourceName) != "" ||
		strings.TrimSpace(filter.BatchID) != "" ||
		strings.TrimSpace(filter.ProducerPeerID) != "" ||
		strings.TrimSpace(filter.ProducerPublicKey) != ""
	indexFilter, err := compileRawRecordSyncFilter(filter)
	if err != nil {
		return nil, err
	}
	indexFiltered := indexFilter.active()
	rowIDSourceCursorOptimized := filter.UseRowIDCursor && sourceFiltered && !indexFiltered &&
		(strings.TrimSpace(filter.ProviderID) != "" || strings.TrimSpace(filter.SourceName) != "")
	if rowIDSourceCursorOptimized {
		return s.queryRawRecordsWithRowIDSourceCursorLocked(tableName, filter, hydrate)
	}

	taggedQuery := fmt.Sprintf(`
		SELECT records.rowid, records.cid, records.peer_id, records.timestamp,
		       records.data, records.signature_hex,
		       tags.provider_id, tags.source_name, tags.source_url, tags.batch_id,
		       tags.content_key_id, tags.producer_peer_id, tags.producer_public_key, tags.created_at
		FROM %s
		INNER JOIN %s records ON records.cid = tags.cid
	`, rawRecordSourceTagsReadSource(filter), tableName)
	if indexFiltered {
		taggedQuery += `
		INNER JOIN sdn_record_index idx
		  ON idx.schema_name = tags.schema_name AND idx.cid = records.cid
		`
	}
	taggedQuery += ` WHERE tags.schema_name = ?`
	args := []interface{}{filter.SchemaName}

	if peerID := strings.TrimSpace(filter.PeerID); peerID != "" {
		taggedQuery += ` AND records.peer_id = ?`
		args = append(args, peerID)
	}
	if cid := strings.TrimSpace(filter.CID); cid != "" {
		taggedQuery += ` AND records.cid = ?`
		args = append(args, cid)
	}
	taggedQuery, args = appendRawRecordSourceTagFilters(taggedQuery, args, "tags", filter)
	taggedQuery, args = appendRawRecordRowIDCursorWhere(taggedQuery, args, "records", filter)
	taggedQuery, args = appendRawRecordSyncFilterWhere(taggedQuery, args, indexFilter)
	if filter.UseRowIDCursor {
		taggedQuery += ` ORDER BY records.rowid ASC, records.cid ASC LIMIT ?`
		args = append(args, filter.Limit)
	} else if indexFiltered {
		taggedQuery += ` ORDER BY COALESCE(idx.epoch_unix, idx.source_timestamp) ASC, records.cid ASC LIMIT ? OFFSET ?`
		args = append(args, filter.Limit, filter.Offset)
	} else {
		taggedQuery += ` ORDER BY tags.created_at DESC, tags.cid ASC LIMIT ? OFFSET ?`
		args = append(args, filter.Limit, filter.Offset)
	}

	rows, err := s.db.Query(taggedQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("raw record query failed: %w", err)
	}
	records, err := s.scanRawRecordRows(filter.SchemaName, rows, hydrate)
	if err != nil {
		return nil, err
	}

	if !sourceFiltered && len(records) < filter.Limit {
		untaggedLimit := filter.Limit - len(records)
		untaggedQuery := fmt.Sprintf(`
			SELECT records.rowid, records.cid, records.peer_id, records.timestamp,
			       records.data, records.signature_hex,
			       '', '', '', '', '', '', '', NULL
			FROM %s records
		`, tableName)
		untaggedArgs := make([]interface{}, 0, len(indexFilter.args)+3)
		if indexFiltered {
			untaggedQuery += `
			INNER JOIN sdn_record_index idx
			  ON idx.schema_name = ? AND idx.cid = records.cid
			`
			untaggedArgs = append(untaggedArgs, filter.SchemaName)
		}
		untaggedQuery += `
			WHERE NOT EXISTS (
				SELECT 1
				FROM sdn_record_source_tags tags
				WHERE tags.schema_name = ? AND tags.cid = records.cid
			)
		`
		untaggedArgs = append(untaggedArgs, filter.SchemaName)
		if peerID := strings.TrimSpace(filter.PeerID); peerID != "" {
			untaggedQuery += ` AND records.peer_id = ?`
			untaggedArgs = append(untaggedArgs, peerID)
		}
		if cid := strings.TrimSpace(filter.CID); cid != "" {
			untaggedQuery += ` AND records.cid = ?`
			untaggedArgs = append(untaggedArgs, cid)
		}
		untaggedQuery, untaggedArgs = appendRawRecordRowIDCursorWhere(untaggedQuery, untaggedArgs, "records", filter)
		untaggedQuery, untaggedArgs = appendRawRecordSyncFilterWhere(untaggedQuery, untaggedArgs, indexFilter)
		if filter.UseRowIDCursor {
			untaggedQuery += ` ORDER BY records.rowid ASC, records.cid ASC LIMIT ?`
		} else if indexFiltered {
			untaggedQuery += ` ORDER BY COALESCE(idx.epoch_unix, idx.source_timestamp) ASC, records.cid ASC LIMIT ?`
		} else {
			untaggedQuery += ` ORDER BY records.timestamp DESC, records.cid ASC LIMIT ?`
		}
		untaggedArgs = append(untaggedArgs, untaggedLimit)
		untaggedRows, err := s.db.Query(untaggedQuery, untaggedArgs...)
		if err != nil {
			return nil, fmt.Errorf("raw untagged record query failed: %w", err)
		}
		untagged, err := s.scanRawRecordRows(filter.SchemaName, untaggedRows, hydrate)
		if err != nil {
			return nil, err
		}
		records = append(records, untagged...)
	}

	if !indexFiltered && filter.SchemaName == "EPM.fbs" && localEPMFilterMatches(filter) && len(records) < filter.Limit {
		localLimit := filter.Limit - len(records)
		localRecords, err := s.queryLocalEPMRecordsLocked(filter, localLimit)
		if err != nil {
			return nil, err
		}
		records = append(records, localRecords...)
	}

	return records, nil
}

// QueryRawRecordRefsByRefs resolves scan-bound refs in batches and preserves
// the requested order. Returned records carry their STORED bytes (the
// field-decryption pass is not run): WriteRawRecordFrames writes them verbatim.
func (s *FlatSQLStore) QueryRawRecordRefsByRefs(schemaName string, refs []RawRecordRef) ([]*Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	schemaName = strings.TrimSpace(schemaName)
	if schemaName == "" {
		return nil, errors.New("schema name is required")
	}
	tableName, err := s.rawRecordReadSource(schemaName)
	if err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}
	if len(refs) == 0 {
		return nil, nil
	}

	normalizedRefs := make([]RawRecordRef, 0, len(refs))
	for _, ref := range refs {
		ref = normalizeRawRecordRef(ref)
		if ref.CID == "" {
			return nil, errors.New("record cid is required")
		}
		normalizedRefs = append(normalizedRefs, ref)
	}

	candidates := make(map[string][]*Record, len(normalizedRefs))
	for start := 0; start < len(normalizedRefs); start += rawRecordRefLookupBatchSize {
		end := start + rawRecordRefLookupBatchSize
		if end > len(normalizedRefs) {
			end = len(normalizedRefs)
		}
		if err := s.loadRawRecordRefCandidatesLocked(schemaName, tableName, normalizedRefs[start:end], candidates); err != nil {
			return nil, err
		}
	}

	ordered := make([]*Record, 0, len(normalizedRefs))
	for _, ref := range normalizedRefs {
		var matched *Record
		for _, candidate := range candidates[ref.CID] {
			if rawRecordMatchesRef(candidate, ref) {
				matched = candidate
				break
			}
		}
		if matched == nil && schemaName == "EPM.fbs" {
			local, err := s.queryLocalEPMRecordsLocked(RawRecordQuery{
				SchemaName:        schemaName,
				CID:               ref.CID,
				ProviderID:        ref.ProviderID,
				SourceName:        ref.SourceName,
				BatchID:           ref.BatchID,
				ProducerPeerID:    ref.ProducerPeerID,
				ProducerPublicKey: ref.ProducerPublicKey,
				PeerID:            ref.PeerID,
				Limit:             1,
			}, 1)
			if err == nil && len(local) == 1 {
				matched = local[0]
			}
		}
		if matched == nil {
			return nil, fmt.Errorf("raw record ref not found: %s", ref.CID)
		}
		ordered = append(ordered, matched)
	}

	return ordered, nil
}

func (s *FlatSQLStore) loadRawRecordRefCandidatesLocked(schemaName, tableName string, refs []RawRecordRef, candidates map[string][]*Record) error {
	cids := make([]string, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if _, ok := seen[ref.CID]; ok {
			continue
		}
		seen[ref.CID] = struct{}{}
		cids = append(cids, ref.CID)
	}
	if len(cids) == 0 {
		return nil
	}

	placeholders := strings.TrimRight(strings.Repeat("?,", len(cids)), ",")
	taggedQuery := fmt.Sprintf(`
		SELECT records.rowid, records.cid, records.peer_id, records.timestamp,
		       records.data, records.signature_hex,
		       tags.provider_id, tags.source_name, tags.source_url, tags.batch_id,
		       tags.content_key_id, tags.producer_peer_id, tags.producer_public_key, tags.created_at
		FROM sdn_record_source_tags tags
		INNER JOIN %s records ON records.cid = tags.cid
		WHERE tags.schema_name = ? AND records.cid IN (%s)
	`, tableName, placeholders)
	taggedArgs := make([]interface{}, 0, len(cids)+1)
	taggedArgs = append(taggedArgs, schemaName)
	for _, cid := range cids {
		taggedArgs = append(taggedArgs, cid)
	}
	taggedRows, err := s.db.Query(taggedQuery, taggedArgs...)
	if err != nil {
		return fmt.Errorf("raw record ref query failed: %w", err)
	}
	tagged, err := s.scanRawRecordRows(schemaName, taggedRows, false)
	if err != nil {
		return err
	}
	for _, record := range tagged {
		candidates[record.CID] = append(candidates[record.CID], record)
	}

	untaggedQuery := fmt.Sprintf(`
		SELECT records.rowid, records.cid, records.peer_id, records.timestamp,
		       records.data, records.signature_hex,
		       '', '', '', '', '', '', '', NULL
		FROM %s records
		WHERE records.cid IN (%s)
		  AND NOT EXISTS (
			  SELECT 1
			  FROM sdn_record_source_tags tags
			  WHERE tags.schema_name = ? AND tags.cid = records.cid
		  )
	`, tableName, placeholders)
	untaggedArgs := make([]interface{}, 0, len(cids)+1)
	for _, cid := range cids {
		untaggedArgs = append(untaggedArgs, cid)
	}
	untaggedArgs = append(untaggedArgs, schemaName)
	untaggedRows, err := s.db.Query(untaggedQuery, untaggedArgs...)
	if err != nil {
		return fmt.Errorf("raw untagged record ref query failed: %w", err)
	}
	untagged, err := s.scanRawRecordRows(schemaName, untaggedRows, false)
	if err != nil {
		return err
	}
	for _, record := range untagged {
		candidates[record.CID] = append(candidates[record.CID], record)
	}
	return nil
}

func normalizeRawRecordRef(ref RawRecordRef) RawRecordRef {
	ref.CID = strings.TrimSpace(ref.CID)
	ref.ProviderID = strings.TrimSpace(ref.ProviderID)
	ref.SourceName = strings.TrimSpace(ref.SourceName)
	ref.BatchID = strings.TrimSpace(ref.BatchID)
	ref.ProducerPeerID = strings.TrimSpace(ref.ProducerPeerID)
	ref.ProducerPublicKey = strings.TrimSpace(ref.ProducerPublicKey)
	ref.PeerID = strings.TrimSpace(ref.PeerID)
	return ref
}

func rawRecordMatchesRef(record *Record, ref RawRecordRef) bool {
	if ref.CID != "" && record.CID != ref.CID {
		return false
	}
	if ref.PeerID != "" && record.PeerID != ref.PeerID {
		return false
	}
	if ref.ProviderID != "" && record.SourceTags.ProviderID != ref.ProviderID {
		return false
	}
	if ref.SourceName != "" && record.SourceTags.SourceName != ref.SourceName {
		return false
	}
	if ref.BatchID != "" && record.SourceTags.BatchID != ref.BatchID {
		return false
	}
	if ref.ProducerPeerID != "" && record.SourceTags.ProducerPeerID != ref.ProducerPeerID {
		return false
	}
	if ref.ProducerPublicKey != "" && record.SourceTags.ProducerPublicKey != ref.ProducerPublicKey {
		return false
	}
	return true
}

// WriteRawRecordFrames writes native FlatSQL size-prefixed FlatBuffer frames
// (little-endian uint32 length, then the record bytes) for the given records.
func (s *FlatSQLStore) WriteRawRecordFrames(writer io.Writer, records []*Record) error {
	var diskLength [4]byte
	for _, record := range records {
		if len(record.Data) == 0 {
			return fmt.Errorf("record %s carries no bytes to frame", record.CID)
		}
		if len(record.Data) > int(^uint32(0)) {
			return fmt.Errorf("record %s exceeds uint32 stream frame length", record.CID)
		}
		binary.LittleEndian.PutUint32(diskLength[:], uint32(len(record.Data)))
		if _, err := writer.Write(diskLength[:]); err != nil {
			return err
		}
		if _, err := writer.Write(record.Data); err != nil {
			return err
		}
	}
	return nil
}

// storedRecordBytesLocked reads one record's bytes exactly as its producer
// table holds them (sealed for a field-encrypted standard). Callers hold s.mu.
func (s *FlatSQLStore) storedRecordBytesLocked(schemaName, cid string) ([]byte, error) {
	readSource, err := s.recordReadSourceFiltered(schemaName, "cid = ?1")
	if err != nil {
		return nil, err
	}
	var data []byte
	if err := s.db.QueryRow(fmt.Sprintf(`SELECT data FROM %s WHERE cid = ?1`, readSource), cid).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("not found: %s", cid)
		}
		return nil, err
	}
	return data, nil
}

// GetRawRecord returns one raw FlatBuffer record by schema and CID. Local EPM
// records use the peer ID as the stable local record identifier.
func (s *FlatSQLStore) GetRawRecord(schemaName, cid string) (*Record, error) {
	cid = strings.TrimSpace(cid)
	if cid == "" {
		return nil, errors.New("record id is required")
	}
	record, err := s.GetRecord(schemaName, cid)
	if err == nil {
		return record, nil
	}
	if schemaName == "EPM.fbs" {
		local, localErr := s.GetLocalEPMRecord(cid)
		if localErr == nil {
			return &Record{
				CID:       local.PeerID,
				PeerID:    local.PeerID,
				Timestamp: time.Unix(local.UpdatedAt, 0).UTC(),
				Data:      append([]byte(nil), local.EPMBytes...),
				SourceTags: SourceTags{
					ProviderID: "local-node",
					SourceName: "local-epm",
					BatchID:    "local",
				},
			}, nil
		}
	}
	return nil, err
}

func (s *FlatSQLStore) scanRecentRecords(schemaName string, rows *sql.Rows) ([]*Record, error) {
	defer rows.Close()

	records := make([]*Record, 0)
	for rows.Next() {
		rec := &Record{}
		var ts int64
		var materializedAt sql.NullInt64
		var data []byte
		var signatureHex sql.NullString
		if err := rows.Scan(
			&rec.CID,
			&rec.PeerID,
			&ts,
			&data,
			&signatureHex,
			&rec.SourceTags.ProviderID,
			&rec.SourceTags.SourceName,
			&rec.SourceTags.SourceURL,
			&rec.SourceTags.BatchID,
			&rec.SourceTags.ContentKeyID,
			&rec.SourceTags.ProducerPeerID,
			&rec.SourceTags.ProducerPublicKey,
			&materializedAt,
		); err != nil {
			return nil, fmt.Errorf("failed scanning recent row: %w", err)
		}
		rec.Timestamp = time.Unix(ts, 0).UTC()
		if err := s.hydrateRecordData(rec, schemaName, data, signatureHex); err != nil {
			return nil, fmt.Errorf("failed reading recent record data: %w", err)
		}
		if materializedAt.Valid && materializedAt.Int64 > 0 {
			rec.MaterializedAt = time.Unix(materializedAt.Int64, 0).UTC()
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recent records rows failed: %w", err)
	}
	return records, nil
}

func (s *FlatSQLStore) scanRawRecordRows(schemaName string, rows *sql.Rows, hydrate bool) ([]*Record, error) {
	defer rows.Close()

	records := make([]*Record, 0)
	for rows.Next() {
		rec := &Record{}
		var ts int64
		var materializedAt sql.NullInt64
		var data []byte
		var signatureHex sql.NullString
		if err := rows.Scan(
			&rec.RowID,
			&rec.CID,
			&rec.PeerID,
			&ts,
			&data,
			&signatureHex,
			&rec.SourceTags.ProviderID,
			&rec.SourceTags.SourceName,
			&rec.SourceTags.SourceURL,
			&rec.SourceTags.BatchID,
			&rec.SourceTags.ContentKeyID,
			&rec.SourceTags.ProducerPeerID,
			&rec.SourceTags.ProducerPublicKey,
			&materializedAt,
		); err != nil {
			return nil, fmt.Errorf("failed scanning raw record row: %w", err)
		}
		rec.Timestamp = time.Unix(ts, 0).UTC()
		rec.RecordLength = int64(len(data))
		if hydrate {
			if err := s.hydrateRecordData(rec, schemaName, data, signatureHex); err != nil {
				return nil, fmt.Errorf("failed reading raw record data: %w", err)
			}
		} else {
			rec.Data = data
		}
		if !hydrate && signatureHex.Valid && strings.TrimSpace(signatureHex.String) != "" {
			signature, err := hex.DecodeString(strings.TrimSpace(signatureHex.String))
			if err != nil {
				return nil, fmt.Errorf("decode signature_hex for %s: %w", rec.CID, err)
			}
			rec.Signature = signature
		}
		if materializedAt.Valid && materializedAt.Int64 > 0 {
			rec.MaterializedAt = time.Unix(materializedAt.Int64, 0).UTC()
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("raw record rows failed: %w", err)
	}
	return records, nil
}

func appendOrAddSchemaSummary(schemas []DataSchemaSummary, add DataSchemaSummary) []DataSchemaSummary {
	for i := range schemas {
		if schemas[i].SchemaName == add.SchemaName {
			schemas[i].Count += add.Count
			schemas[i].TotalBytes += add.TotalBytes
			return schemas
		}
	}
	return append(schemas, add)
}

func localEPMFilterMatches(filter RawRecordQuery) bool {
	if providerID := strings.TrimSpace(filter.ProviderID); providerID != "" && providerID != "local-node" {
		return false
	}
	if sourceName := strings.TrimSpace(filter.SourceName); sourceName != "" && sourceName != "local-epm" {
		return false
	}
	if batchID := strings.TrimSpace(filter.BatchID); batchID != "" && batchID != "local" {
		return false
	}
	if producerPeerID := strings.TrimSpace(filter.ProducerPeerID); producerPeerID != "" && producerPeerID != "local-node" {
		return false
	}
	if producerPublicKey := strings.TrimSpace(filter.ProducerPublicKey); producerPublicKey != "" && producerPublicKey != "local-node" {
		return false
	}
	return true
}

func (s *FlatSQLStore) localEPMSummaryLocked() (int64, int64, error) {
	rows, err := s.db.Query(`
		SELECT peer_id, encrypted_epm_bytes
		FROM sdn_local_epms
		WHERE schema_name = 'EPM.fbs'
	`)
	if err != nil {
		return 0, 0, fmt.Errorf("summarize local EPMs: %w", err)
	}
	defer rows.Close()

	var count int64
	var totalBytes int64
	for rows.Next() {
		var peerID, encrypted string
		if err := rows.Scan(&peerID, &encrypted); err != nil {
			return 0, 0, fmt.Errorf("scan local EPM summary: %w", err)
		}
		raw, ok := s.readLocalEPMRow(peerID, encrypted)
		if !ok {
			continue
		}
		count++
		totalBytes += int64(len(raw))
	}
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("local EPM summary rows failed: %w", err)
	}
	return count, totalBytes, nil
}

func (s *FlatSQLStore) queryLocalEPMRecordsLocked(filter RawRecordQuery, limit int) ([]*Record, error) {
	if limit <= 0 {
		return nil, nil
	}
	query := `
		SELECT peer_id, encrypted_epm_bytes, updated_at
		FROM sdn_local_epms
		WHERE schema_name = 'EPM.fbs'
	`
	args := make([]interface{}, 0, 3)
	if peerID := strings.TrimSpace(filter.PeerID); peerID != "" {
		query += ` AND peer_id = ?`
		args = append(args, peerID)
	}
	if cid := strings.TrimSpace(filter.CID); cid != "" {
		query += ` AND peer_id = ?`
		args = append(args, cid)
	}
	query += ` ORDER BY updated_at DESC, peer_id ASC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query local EPM records: %w", err)
	}
	defer rows.Close()

	records := make([]*Record, 0, limit)
	for rows.Next() {
		var peerID string
		var encrypted string
		var updatedAt int64
		if err := rows.Scan(&peerID, &encrypted, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan local EPM record: %w", err)
		}
		epmBytes, ok := s.readLocalEPMRow(peerID, encrypted)
		if !ok {
			continue
		}
		records = append(records, &Record{
			CID:       peerID,
			PeerID:    peerID,
			Timestamp: time.Unix(updatedAt, 0).UTC(),
			Data:      epmBytes,
			SourceTags: SourceTags{
				ProviderID: "local-node",
				SourceName: "local-epm",
				BatchID:    "local",
			},
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("local EPM rows failed: %w", err)
	}
	return records, nil
}

func (s *FlatSQLStore) countLocalEPMRecordsLocked(filter RawRecordQuery) (int64, error) {
	query := `
		SELECT COUNT(*)
		FROM sdn_local_epms
		WHERE schema_name = 'EPM.fbs'
	`
	args := make([]interface{}, 0, 1)
	if peerID := strings.TrimSpace(filter.PeerID); peerID != "" {
		query += ` AND peer_id = ?`
		args = append(args, peerID)
	}
	if cid := strings.TrimSpace(filter.CID); cid != "" {
		query += ` AND peer_id = ?`
		args = append(args, cid)
	}
	var count int64
	if err := s.db.QueryRow(query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count local EPM records: %w", err)
	}
	return count, nil
}

// QueryIndexedRecords returns records using materialized catalog/source indexes.
// indexedRecordProjection is the full record read: the record bytes plus the
// projected source tags.
const indexedRecordProjection = `d.cid, d.peer_id, d.timestamp,
		       d.data, d.signature_hex,
		       tags.provider_id, tags.source_name, tags.batch_id`

// indexedRecordLengthProjection is the BYTE PROBE: the frame length alone, the
// value already recorded for every stored record. Reading it costs no payload
// I/O, which is the whole point — a shard boundary must be decidable without
// first materialising the shard it is trying to bound.
const indexedRecordLengthProjection = `d.record_length`

// normalizeIndexedRecordWindow applies the shared window rules (day/time
// sanity, default and maximum limits) so a probe and its read agree on the
// window before either touches the database.
func normalizeIndexedRecordWindow(filter IndexedRecordQuery) (IndexedRecordQuery, error) {
	if filter.Day != "" {
		if _, err := time.Parse("2006-01-02", filter.Day); err != nil {
			return filter, fmt.Errorf("invalid day %q (expected YYYY-MM-DD)", filter.Day)
		}
	}
	if filter.From != nil && filter.To != nil && filter.From.After(*filter.To) {
		return filter, errors.New("from time must be before to time")
	}
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	maxLimit := 1000
	if filter.AllowLargeResultSet {
		maxLimit = 250000
	}
	if filter.Limit > maxLimit {
		filter.Limit = maxLimit
	}
	return filter, nil
}

// indexedRecordWindowSQL builds ONE window definition and hands it back with a
// caller-chosen projection.
//
// It exists so the byte probe (IndexedRecordWindowLimitForBytes) and the record
// read (QueryIndexedRecords) can never drift: a shard boundary computed from
// record_length must describe EXACTLY the rows the export then materialises,
// same filters, same ORDER BY, same LIMIT/OFFSET. Two hand-copied WHERE clauses
// would be a silent mis-cut waiting to happen.
func (s *FlatSQLStore) indexedRecordWindowSQL(filter IndexedRecordQuery, tableName, projection string) (string, []interface{}) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM %s d
		INNER JOIN sdn_record_index idx
		  ON idx.schema_name = ? AND idx.cid = d.cid
		LEFT JOIN (
			SELECT schema_name, cid, provider_id, source_name, batch_id
			FROM sdn_record_source_tags
			WHERE schema_name = ?
			GROUP BY schema_name, cid
		) tags ON tags.schema_name = idx.schema_name AND tags.cid = idx.cid
	`, projection, tableName)

	args := []interface{}{filter.SchemaName, filter.SchemaName}
	query += `
		WHERE 1=1
	`

	if filter.Day != "" {
		query += ` AND idx.epoch_day = ?`
		args = append(args, filter.Day)
	}

	if filter.NoradCatID != nil {
		query += ` AND idx.norad_cat_id = ?`
		args = append(args, int64(*filter.NoradCatID))
	}

	if filter.EntityID != "" {
		query += ` AND idx.entity_id = ?`
		args = append(args, filter.EntityID)
	}

	objectType := normalizeIndexEnum(filter.ObjectType)
	opsStatus := normalizeIndexEnum(filter.OpsStatusCode)
	if filter.ActivePayloads || filter.CAReadyResidentSet {
		objectType = "PAYLOAD"
	}
	if objectType != "" {
		query += ` AND idx.object_type = ?`
		args = append(args, objectType)
	}
	if opsStatus != "" {
		query += ` AND idx.ops_status_code = ?`
		args = append(args, opsStatus)
	}
	if filter.ActivePayloads || filter.CAReadyResidentSet {
		query += ` AND idx.ops_status_code IN ('OPERATIONAL', 'PARTIALLY_OPERATIONAL', 'BACKUP_STANDBY', 'SPARE', 'EXTENDED_MISSION', 'UNKNOWN')`
	}
	if filter.CAReadyResidentSet {
		query += ` AND idx.norad_cat_id IS NOT NULL`
	}
	if filter.From != nil {
		query += ` AND COALESCE(idx.epoch_unix, idx.source_timestamp) >= ?`
		args = append(args, filter.From.Unix())
	}
	if filter.To != nil {
		query += ` AND COALESCE(idx.epoch_unix, idx.source_timestamp) <= ?`
		args = append(args, filter.To.Unix())
	}
	providerID := strings.TrimSpace(filter.ProviderID)
	sourceName := strings.TrimSpace(filter.SourceName)
	batchID := strings.TrimSpace(filter.BatchID)
	if providerID != "" || sourceName != "" || batchID != "" {
		// ANY-row semantics: all requested tag conditions must hold on a single
		// tag row, but not necessarily the row the projection picked.
		query += ` AND EXISTS (
			SELECT 1 FROM sdn_record_source_tags ft
			WHERE ft.schema_name = idx.schema_name AND ft.cid = idx.cid`
		if providerID != "" {
			query += ` AND ft.provider_id = ?`
			args = append(args, providerID)
		}
		if sourceName != "" {
			query += ` AND ft.source_name = ?`
			args = append(args, sourceName)
		}
		if batchID != "" {
			query += ` AND ft.batch_id = ?`
			args = append(args, batchID)
		}
		query += `)`
	}

	if filter.OrderByCID {
		query += ` ORDER BY d.cid ASC LIMIT ?`
	} else {
		query += ` ORDER BY COALESCE(idx.epoch_unix, idx.source_timestamp) DESC, d.cid ASC LIMIT ?`
	}
	args = append(args, filter.Limit)
	if filter.Offset > 0 {
		query += ` OFFSET ?`
		args = append(args, filter.Offset)
	}

	return query, args
}

// DatasetShardFrameOverheadBytes is the per-record cost a shard pays on top of
// the record itself: the little-endian uint32 size prefix that makes the shard
// a size-prefixed STREAM (ExportDatasetRecords, export.go). It is counted here
// because a boundary that ignored it would under-report every shard by 4 bytes
// per record — 128 KB on a 32k-record window.
const DatasetShardFrameOverheadBytes = 4

// IndexedRecordWindowLimitForBytes reports how many records of a window fit in
// maxBytes, WITHOUT reading a single payload.
//
// This is the missing half of the owner's requirement ("aware of the length of
// the flatbuffer if that is going to matter in terms of sharding"). Shard
// boundaries were pure record COUNTS: 250 records of $CAT is ~33 KB, 250
// records of 128 MiB is 32 GiB — same shard, five orders of magnitude apart.
// The size prefix that bounds every read was never allowed to bound a shard.
//
// Rules:
//   - never splits a buffer: the cut lands BETWEEN frames, always (the
//     property the read already proved and this must not break);
//   - never returns 0 while the window has rows: one oversized record is a
//     one-record shard, not a stall. A record larger than the budget is
//     reported by the caller, not silently dropped;
//   - never exceeds the window's own limit.
//
// The second return value is the ONLY thing that may narrow a caller's window:
// it is true when the BUDGET stopped the probe with rows still to come, and
// false when the window simply held fewer rows than its limit. Conflating the
// two rewrites the (offset, limit) key of an ordinary short shard, which is a
// publication's identity and its consumers' cursor.
//
// The probe reads record_length from the SAME window definition the export
// then materialises (indexedRecordWindowSQL), so the count it returns is the
// count the export produces.
func (s *FlatSQLStore) IndexedRecordWindowLimitForBytes(filter IndexedRecordQuery, maxBytes int64) (int, bool, error) {
	if maxBytes <= 0 {
		return filter.Limit, false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	tableName, err := s.recordReadSource(filter.SchemaName)
	if err != nil {
		return 0, false, fmt.Errorf("invalid schema name: %w", err)
	}
	filter, err = normalizeIndexedRecordWindow(filter)
	if err != nil {
		return 0, false, err
	}

	query, args := s.indexedRecordWindowSQL(filter, tableName, indexedRecordLengthProjection)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return 0, false, fmt.Errorf("shard byte probe failed: %w", err)
	}
	defer rows.Close()

	var (
		total     int64
		count     int
		truncated bool
	)
	for rows.Next() {
		var recordLength int64
		if err := rows.Scan(&recordLength); err != nil {
			return 0, false, fmt.Errorf("failed scanning shard byte probe row: %w", err)
		}
		frame := recordLength + DatasetShardFrameOverheadBytes
		if count > 0 && total+frame > maxBytes {
			// Rows remain and the budget stopped us: this is a real cut.
			truncated = true
			break
		}
		total += frame
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, false, fmt.Errorf("shard byte probe failed: %w", err)
	}
	return count, truncated, nil
}

func (s *FlatSQLStore) QueryIndexedRecords(filter IndexedRecordQuery) ([]*Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tableName, err := s.recordReadSource(filter.SchemaName)
	if err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}

	filter, err = normalizeIndexedRecordWindow(filter)
	if err != nil {
		return nil, err
	}

	// Source tags are projected unconditionally (LEFT JOIN) so readers like the
	// Downstream consumers can group records per provider. The grouped subquery pins
	// ONE tag row per (schema, cid) for projection — a record can carry several
	// tag rows (multi-producer mirrors, re-imports under a new batch id), so
	// projection is one-of-many by design. Tag FILTERS below deliberately do NOT
	// use this join: they must match ANY tag row (e.g. a record re-tagged under
	// a second batch id still matches a query for that batch), so they run as an
	// EXISTS over the raw table.
	query, args := s.indexedRecordWindowSQL(filter, tableName, indexedRecordProjection)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("indexed query failed: %w", err)
	}
	defer rows.Close()

	var records []*Record
	for rows.Next() {
		rec := &Record{}
		var ts int64
		var data []byte
		var signatureHex sql.NullString
		var tagProviderID, tagSourceName, tagBatchID sql.NullString
		if err := rows.Scan(&rec.CID, &rec.PeerID, &ts, &data, &signatureHex,
			&tagProviderID, &tagSourceName, &tagBatchID); err != nil {
			return nil, fmt.Errorf("failed scanning indexed row: %w", err)
		}
		rec.Timestamp = time.Unix(ts, 0).UTC()
		rec.SourceTags = SourceTags{
			ProviderID: tagProviderID.String,
			SourceName: tagSourceName.String,
			BatchID:    tagBatchID.String,
		}
		if err := s.hydrateRecordData(rec, filter.SchemaName, data, signatureHex); err != nil {
			return nil, fmt.Errorf("failed reading indexed record data: %w", err)
		}
		records = append(records, rec)
	}

	return records, nil
}

// GetRecord retrieves a full record by CID.
func (s *FlatSQLStore) GetRecord(schemaName, cid string) (*Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// The cid predicate is inlined into EVERY union branch, not just applied to
	// the union's result: SQLite cannot push it through the read source's
	// `GROUP BY cid`, so the outer-only form full-scans every (producer,
	// standard) table on every record read. That scan is what saturated
	// host-01's single-threaded engine and put SECONDS of queue in front of
	// every other store read — see recordReadSourceFiltered for the
	// measurements and the two graph tasks it closes.
	readSource, err := s.recordReadSourceFiltered(schemaName, "cid = ?1")
	if err != nil {
		return nil, fmt.Errorf("invalid schema name: %w", err)
	}

	querySQL := fmt.Sprintf(`
		SELECT cid, peer_id, timestamp, data, signature_hex
		FROM %s WHERE cid = ?1
	`, readSource)

	var record Record
	var timestamp int64
	var data []byte
	var signatureHex sql.NullString
	err = s.db.QueryRow(querySQL, cid).Scan(
		&record.CID,
		&record.PeerID,
		&timestamp,
		&data,
		&signatureHex,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("not found: %s", cid)
		}
		return nil, fmt.Errorf("failed to get record: %w", err)
	}

	record.Timestamp = time.Unix(timestamp, 0)
	if err := s.hydrateRecordData(&record, schemaName, data, signatureHex); err != nil {
		return nil, fmt.Errorf("failed to read record data: %w", err)
	}
	return &record, nil
}

type indexedFields struct {
	noradCatID    *uint32
	entityID      string
	objectType    string
	opsStatusCode string
	epochUnix     *int64
	epochDay      string
}

func (s *FlatSQLStore) upsertRecordIndex(schemaName, cid string, sourceTimestamp int64, data []byte) error {
	return upsertRecordIndexExec(s.db, schemaName, cid, sourceTimestamp, data, s.fullTextState(schemaName))
}

// recordIndexConflictClause is why sdn_record_index.rowid is a stable cursor.
// That rowid is the WIRE-VISIBLE datasync cursor deployed peers hold: rows are
// only ever inserted (SQLite allocates MAX(rowid)+1; the store never VACUUMs),
// and a repeat CID takes this ON CONFLICT branch and KEEPS its rowid, so a
// record's cursor position never moves for the life of the store. Shared with
// the multi-row batch upsert (flatsql_batch_writes.go): one record must land
// the same way whether it arrived alone or inside a window.
const recordIndexConflictClause = `
		ON CONFLICT(schema_name, cid) DO UPDATE SET
			norad_cat_id = excluded.norad_cat_id,
			entity_id = excluded.entity_id,
			object_type = excluded.object_type,
			ops_status_code = excluded.ops_status_code,
			epoch_unix = excluded.epoch_unix,
			epoch_day = excluded.epoch_day,
			source_timestamp = excluded.source_timestamp
	`

// recordIndexArgs shapes one record's index-row bind parameters: NULL for
// every structured column the payload does not carry.
func recordIndexArgs(schemaName, cid string, sourceTimestamp int64, data []byte) []any {
	fields, err := extractIndexedFields(schemaName, data)
	if err != nil {
		// The index is the global record catalog + sync cursor (WS7.3d): every
		// stored record MUST get a row so no record is invisible to a
		// rowid-cursor scan. A field-extraction failure (e.g. an unparseable
		// OMM/CAT payload) still gets a bare index row with only the source
		// timestamp; the structured columns stay NULL.
		fields = &indexedFields{}
	}

	var norad interface{}
	if fields.noradCatID != nil {
		norad = int64(*fields.noradCatID)
	}
	var entity interface{}
	if fields.entityID != "" {
		entity = fields.entityID
	}
	var objectType interface{}
	if fields.objectType != "" {
		objectType = fields.objectType
	}
	var opsStatusCode interface{}
	if fields.opsStatusCode != "" {
		opsStatusCode = fields.opsStatusCode
	}
	var epoch interface{}
	if fields.epochUnix != nil {
		epoch = *fields.epochUnix
	}
	var day interface{}
	if fields.epochDay != "" {
		day = fields.epochDay
	}
	return []any{schemaName, cid, norad, entity, objectType, opsStatusCode, epoch, day, sourceTimestamp}
}

// upsertRecordIndexExec writes one record's index row and its full-text row.
func upsertRecordIndexExec(exec sqlExecer, schemaName, cid string, sourceTimestamp int64, data []byte, textIndex *fullTextIndexState) error {
	sqlText := `
		INSERT INTO sdn_record_index (
			schema_name, cid, norad_cat_id, entity_id, object_type, ops_status_code, epoch_unix, epoch_day, source_timestamp
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)` + recordIndexConflictClause
	if _, err := exec.Exec(flatsqldrv.WithoutJournal(sqlText), recordIndexArgs(schemaName, cid, sourceTimestamp, data)...); err != nil {
		return fmt.Errorf("failed to upsert index row: %w", err)
	}

	return upsertFullTextExec(exec, textIndex, schemaName, cid, data)
}

func extractIndexedFields(schemaName string, data []byte) (*indexedFields, error) {
	out := &indexedFields{}

	switch schemaName {
	case "OMM.fbs":
		omm, err := parseOMM(data)
		if err != nil {
			return nil, err
		}
		if id := omm.NORAD_CAT_ID(); id > 0 {
			idCopy := id
			out.noradCatID = &idCopy
		}
		out.entityID = strings.TrimSpace(string(omm.OBJECT_ID()))

		epochStr := strings.TrimSpace(string(omm.EPOCH()))
		if epochStr == "" {
			epochStr = strings.TrimSpace(string(omm.CREATION_DATE()))
		}
		if epochStr != "" {
			epochUnix, err := parseEpochString(epochStr)
			if err == nil {
				out.epochUnix = &epochUnix
				out.epochDay = time.Unix(epochUnix, 0).UTC().Format("2006-01-02")
			}
		}
	case "MPE.fbs":
		mpe, err := parseMPE(data)
		if err != nil {
			return nil, err
		}
		out.entityID = strings.TrimSpace(string(mpe.ENTITY_ID()))
		if epoch := int64(mpe.EPOCH()); epoch > 0 {
			out.epochUnix = &epoch
			out.epochDay = time.Unix(epoch, 0).UTC().Format("2006-01-02")
		}

	case "CAT.fbs":
		cat, err := parseCAT(data)
		if err != nil {
			return nil, err
		}
		if id := cat.NORAD_CAT_ID(); id > 0 {
			idCopy := id
			out.noradCatID = &idCopy
		}
		// The international designator is the object's identity when it has
		// no NORAD number (un-numbered launches, analyst objects); without it
		// such a record was invisible to every ?entity= lookup.
		out.entityID = strings.TrimSpace(string(cat.OBJECT_ID()))
		if objectType := strings.TrimSpace(cat.OBJECT_TYPE().String()); objectType != "" && objectType != "UNKNOWN" {
			out.objectType = objectType
		}
		if opsStatusCode := strings.TrimSpace(cat.OPS_STATUS_CODE().String()); opsStatusCode != "" && opsStatusCode != "UNKNOWN" {
			out.opsStatusCode = opsStatusCode
		}

	case "PNM.fbs":
		pnm, err := parsePNM(data)
		if err != nil {
			return nil, err
		}
		out.entityID = strings.TrimSpace(string(pnm.FILE_ID()))

	case "RFB.fbs":
		// RF emitters are NORAD-keyed data (sdn-data-index-rfb-norad). Without
		// this case every $RFB row indexed with norad_cat_id NULL even though
		// the bytes carried NORAD_CAT_ID, so /api/v1/data/index answered
		// norad:null for all 5,289 SatNOGS records and ?norad= matched nothing —
		// the whole "which satellites transmit on S-band" query path.
		//
		// entity_id is the TRANSMITTER, not the spacecraft: one RFB record
		// describes exactly one emission of one device, and an UPLINK/DOWNLINK
		// pair shares ID_TRANSMITTER (RFB.fbs). The spacecraft is already the
		// norad_cat_id column, so indexing ID_ENTITY here would duplicate it and
		// lose the ability to find the two halves of one transponder. ID is the
		// fallback for a record with no device identifier.
		//
		// No epoch: $RFB is a specification of a band, not an observation at a
		// time. Leaving epoch_unix NULL is the honest projection — a synthesized
		// ingest time would make every emitter look like it changed today.
		rfb, err := parseRFB(data)
		if err != nil {
			return nil, err
		}
		if id := rfb.NORAD_CAT_ID(); id > 0 {
			idCopy := id
			out.noradCatID = &idCopy
		}
		out.entityID = strings.TrimSpace(string(rfb.ID_TRANSMITTER()))
		if out.entityID == "" {
			out.entityID = strings.TrimSpace(string(rfb.ID()))
		}

	default:
		// No structured extraction for this schema yet.
	}

	return out, nil
}

func normalizeIndexEnum(value string) string {
	normalized := strings.TrimSpace(strings.ToUpper(value))
	normalized = strings.ReplaceAll(normalized, " ", "_")
	normalized = strings.ReplaceAll(normalized, "-", "_")
	if normalized == "UNKNOWN" {
		return ""
	}
	return normalized
}

func parseOMM(data []byte) (omm *OMM.OMM, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("malformed OMM buffer: %v", r)
		}
	}()
	switch {
	case OMM.SizePrefixedOMMBufferHasIdentifier(data):
		return OMM.GetSizePrefixedRootAsOMM(data, 0), nil
	case OMM.OMMBufferHasIdentifier(data):
		return OMM.GetRootAsOMM(data, 0), nil
	default:
		return nil, errors.New("invalid OMM buffer")
	}
}

func parsePNM(data []byte) (pnm *PNM.PNM, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("malformed PNM buffer: %v", r)
		}
	}()
	switch {
	case PNM.SizePrefixedPNMBufferHasIdentifier(data):
		return PNM.GetSizePrefixedRootAsPNM(data, 0), nil
	case PNM.PNMBufferHasIdentifier(data):
		return PNM.GetRootAsPNM(data, 0), nil
	default:
		return nil, errors.New("invalid PNM buffer")
	}
}

func parseRFB(data []byte) (rfb *RFB.RFB, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("malformed RFB buffer: %v", r)
		}
	}()
	switch {
	case RFB.SizePrefixedRFBBufferHasIdentifier(data):
		return RFB.GetSizePrefixedRootAsRFB(data, 0), nil
	case RFB.RFBBufferHasIdentifier(data):
		return RFB.GetRootAsRFB(data, 0), nil
	default:
		return nil, errors.New("invalid RFB buffer")
	}
}

func parseMPE(data []byte) (mpe *MPE.MPE, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("malformed MPE buffer: %v", r)
		}
	}()
	switch {
	case MPE.SizePrefixedMPEBufferHasIdentifier(data):
		return MPE.GetSizePrefixedRootAsMPE(data, 0), nil
	case MPE.MPEBufferHasIdentifier(data):
		return MPE.GetRootAsMPE(data, 0), nil
	default:
		return nil, errors.New("invalid MPE buffer")
	}
}

func parseCAT(data []byte) (cat *CAT.CAT, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("malformed CAT buffer: %v", r)
		}
	}()
	switch {
	case CAT.SizePrefixedCATBufferHasIdentifier(data):
		return CAT.GetSizePrefixedRootAsCAT(data, 0), nil
	case CAT.CATBufferHasIdentifier(data):
		return CAT.GetRootAsCAT(data, 0), nil
	default:
		return nil, errors.New("invalid CAT buffer")
	}
}

// SchemaDateRange holds catalog metadata for a single schema.
type SchemaDateRange struct {
	Schema      string
	RecordCount int64
	OldestEpoch *time.Time
	NewestEpoch *time.Time
	TotalBytes  int64
}

// SchemaDateRanges returns catalog metadata for all schemas with stored data.
func (s *FlatSQLStore) SchemaDateRanges() ([]SchemaDateRange, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT schema_name, COUNT(*) as cnt,
		       MIN(epoch_unix) as min_epoch,
		       MAX(epoch_unix) as max_epoch
		FROM sdn_record_index
		GROUP BY schema_name
		ORDER BY schema_name
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to query schema date ranges: %w", err)
	}
	defer rows.Close()

	var ranges []SchemaDateRange
	for rows.Next() {
		var r SchemaDateRange
		var minEpoch, maxEpoch sql.NullInt64
		if err := rows.Scan(&r.Schema, &r.RecordCount, &minEpoch, &maxEpoch); err != nil {
			return nil, fmt.Errorf("failed to scan schema date range: %w", err)
		}
		if minEpoch.Valid && minEpoch.Int64 > 0 {
			t := time.Unix(minEpoch.Int64, 0).UTC()
			r.OldestEpoch = &t
		}
		if maxEpoch.Valid && maxEpoch.Int64 > 0 {
			t := time.Unix(maxEpoch.Int64, 0).UTC()
			r.NewestEpoch = &t
		}
		ranges = append(ranges, r)
	}

	// Compute total bytes from per-schema tables.
	for i := range ranges {
		tableName, err := s.recordReadSource(ranges[i].Schema)
		if err != nil {
			continue
		}
		var totalBytes sql.NullInt64
		err = s.db.QueryRow(fmt.Sprintf(`SELECT SUM(record_length) FROM %s`, tableName)).Scan(&totalBytes)
		if err == nil && totalBytes.Valid {
			ranges[i].TotalBytes = totalBytes.Int64
		}
	}

	return ranges, nil
}

// PeerStorageBytes returns the total stored bytes for a given peer across all schemas.
func (s *FlatSQLStore) PeerStorageBytes(peerID string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var total int64
	for _, schemaName := range s.validator.Schemas() {
		readSource, err := s.recordReadSource(schemaName)
		if err != nil {
			continue
		}
		var bytes sql.NullInt64
		err = s.db.QueryRow(fmt.Sprintf(`SELECT SUM(record_length) FROM %s WHERE peer_id = ?`, readSource), peerID).Scan(&bytes)
		if err == nil && bytes.Valid {
			total += bytes.Int64
		}
	}

	return total, nil
}

// LogHeadInfo holds the latest log state for a (publisher, schema) pair.
type LogHeadInfo struct {
	PublisherPeerID string
	SchemaType      string
	Sequence        uint64
	EntryHash       string
	RecordCID       string
	Timestamp       int64
}

// UpsertLogIndex inserts or updates a publication log index entry.
func (s *FlatSQLStore) UpsertLogIndex(publisherPeerID, schemaType string, sequence uint64, entryHash, recordCID, plgCID, epochDay string, timestamp int64) error {
	_, err := s.db.Exec(`
		INSERT INTO sdn_log_index (
			publisher_peer_id, schema_type, sequence, entry_hash, record_cid, plg_cid, epoch_day, timestamp
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(publisher_peer_id, schema_type, sequence) DO UPDATE SET
			entry_hash = excluded.entry_hash,
			record_cid = excluded.record_cid,
			plg_cid = excluded.plg_cid,
			epoch_day = excluded.epoch_day,
			timestamp = excluded.timestamp
	`, publisherPeerID, schemaType, sequence, entryHash, recordCID, plgCID, epochDay, timestamp)
	if err != nil {
		return fmt.Errorf("failed to upsert log index: %w", err)
	}
	return nil
}

// LogIndexRow is one sdn_log_index entry for UpsertLogIndexBatch.
type LogIndexRow struct {
	Sequence  uint64
	EntryHash string
	RecordCID string
	PLGCID    string
	EpochDay  string
	Timestamp int64
}

// UpsertLogIndexBatch upserts many log-index rows for one (publisher, schema)
// log inside a single transaction. Per-row UpsertLogIndex auto-commits each
// statement through the engine, which caps batch PLG appends at per-row
// transaction cost.
func (s *FlatSQLStore) UpsertLogIndexBatch(publisherPeerID, schemaType string, rows []LogIndexRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin log index batch: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	stmt, err := tx.Prepare(`
		INSERT INTO sdn_log_index (
			publisher_peer_id, schema_type, sequence, entry_hash, record_cid, plg_cid, epoch_day, timestamp
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(publisher_peer_id, schema_type, sequence) DO UPDATE SET
			entry_hash = excluded.entry_hash,
			record_cid = excluded.record_cid,
			plg_cid = excluded.plg_cid,
			epoch_day = excluded.epoch_day,
			timestamp = excluded.timestamp
	`)
	if err != nil {
		return fmt.Errorf("prepare log index upsert: %w", err)
	}
	defer stmt.Close()
	for _, row := range rows {
		if _, err := stmt.Exec(publisherPeerID, schemaType, row.Sequence, row.EntryHash, row.RecordCID, row.PLGCID, row.EpochDay, row.Timestamp); err != nil {
			return fmt.Errorf("upsert log index seq %d: %w", row.Sequence, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit log index batch: %w", err)
	}
	committed = true
	return nil
}

// GetLogHead returns the latest sequence and entry hash for a (publisher, schema) log.
func (s *FlatSQLStore) GetLogHead(publisherPeerID, schemaType string) (uint64, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var sequence uint64
	var entryHash string
	err := s.db.QueryRow(`
		SELECT sequence, entry_hash
		FROM sdn_log_index
		WHERE publisher_peer_id = ? AND schema_type = ?
		ORDER BY sequence DESC
		LIMIT 1
	`, publisherPeerID, schemaType).Scan(&sequence, &entryHash)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, "", nil
		}
		return 0, "", fmt.Errorf("failed to get log head: %w", err)
	}
	return sequence, entryHash, nil
}

// QueryLogEntries returns PLG FlatBuffer data for entries after sinceSequence.
func (s *FlatSQLStore) QueryLogEntries(publisherPeerID, schemaType string, sinceSequence uint64, limit int) ([][]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	// Publication log entries live under PLOG.fbs (PLG.fbs is the Plugin
	// Manifest standard — joining it here returned zero entries for every
	// log sync).
	plgTableName, err := s.recordReadSource("PLOG.fbs")
	if err != nil {
		return nil, fmt.Errorf("invalid PLG schema name: %w", err)
	}

	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT p.data
		FROM sdn_log_index li
		INNER JOIN %s p ON p.cid = li.plg_cid
		WHERE li.publisher_peer_id = ?
		  AND li.schema_type = ?
		  AND li.sequence > ?
		ORDER BY li.sequence ASC
		LIMIT ?
	`, plgTableName), publisherPeerID, schemaType, sinceSequence, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query log entries: %w", err)
	}
	defer rows.Close()

	var results [][]byte
	for rows.Next() {
		var stored []byte
		if err := rows.Scan(&stored); err != nil {
			log.Warnf("Failed to scan log entry: %v", err)
			continue
		}
		data, err := s.openStoredRecordBytes("PLOG.fbs", stored)
		if err != nil {
			log.Warnf("Failed to open log entry record: %v", err)
			continue
		}
		results = append(results, data)
	}
	return results, nil
}

// QueryLogHeads returns the latest log head info for all publishers of a schema type.
func (s *FlatSQLStore) QueryLogHeads(schemaType string) ([]LogHeadInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT li.publisher_peer_id, li.schema_type, li.sequence, li.entry_hash, li.record_cid, li.timestamp
		FROM sdn_log_index li
		INNER JOIN (
			SELECT publisher_peer_id, schema_type, MAX(sequence) as max_seq
			FROM sdn_log_index
			WHERE schema_type = ?
			GROUP BY publisher_peer_id, schema_type
		) latest ON li.publisher_peer_id = latest.publisher_peer_id
		       AND li.schema_type = latest.schema_type
		       AND li.sequence = latest.max_seq
		ORDER BY li.publisher_peer_id
	`, schemaType)
	if err != nil {
		return nil, fmt.Errorf("failed to query log heads: %w", err)
	}
	defer rows.Close()

	var heads []LogHeadInfo
	for rows.Next() {
		var h LogHeadInfo
		if err := rows.Scan(&h.PublisherPeerID, &h.SchemaType, &h.Sequence, &h.EntryHash, &h.RecordCID, &h.Timestamp); err != nil {
			log.Warnf("Failed to scan log head: %v", err)
			continue
		}
		heads = append(heads, h)
	}
	return heads, nil
}

// LogRecordCount returns the total number of log entries for a (publisher, schema) pair.
func (s *FlatSQLStore) LogRecordCount(publisherPeerID, schemaType string) (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var count uint64
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM sdn_log_index
		WHERE publisher_peer_id = ? AND schema_type = ?
	`, publisherPeerID, schemaType).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count log entries: %w", err)
	}
	return count, nil
}

// LogEpochRange returns the oldest and newest epoch days for a (publisher, schema) log.
func (s *FlatSQLStore) LogEpochRange(publisherPeerID, schemaType string) (oldest, newest string, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	err = s.db.QueryRow(`
		SELECT COALESCE(MIN(epoch_day), ''), COALESCE(MAX(epoch_day), '')
		FROM sdn_log_index
		WHERE publisher_peer_id = ? AND schema_type = ?
		  AND epoch_day IS NOT NULL AND epoch_day != ''
	`, publisherPeerID, schemaType).Scan(&oldest, &newest)
	if err != nil {
		return "", "", fmt.Errorf("failed to get log epoch range: %w", err)
	}
	return oldest, newest, nil
}

func parseEpochString(raw string) (int64, error) {
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		return 0, errors.New("empty epoch")
	}

	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000000",
		"2006-01-02T15:04:05.000",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}

	for _, layout := range layouts {
		if t, err := time.Parse(layout, normalized); err == nil {
			return t.UTC().Unix(), nil
		}
	}

	if floatEpoch, err := strconv.ParseFloat(normalized, 64); err == nil && floatEpoch > 0 {
		return int64(floatEpoch), nil
	}

	return 0, fmt.Errorf("unsupported epoch format: %q", raw)
}
