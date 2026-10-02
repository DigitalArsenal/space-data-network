package storage

// format2_daemon.go — the daemon on store format 2 (stack design
// docs/architecture/flatsql-partition-store.md §5.1, §14, §16, A6; task T6).
//
// SDN_STORE_FORMAT=2 selects it; format 1 stays the default and nothing here
// runs for a node that did not select it. NewFlatSQLStore then opens:
//
//   - THE PARTITION STORE (internal/storage/format2): the writer instance and
//     the interactive and bulk reader instances over <store>/fsql2/. Every
//     record read and write of the node goes there (format2_daemon_writes.go,
//     format2_daemon_reads.go): writes through the router with durable acks,
//     reads as statements on reader lanes, counters from the heads. No record
//     path takes s.mu, the engine lock of the control instance, or waits on
//     a writer (reads-never-wait law).
//   - THE CONTROL INSTANCE: the legacy engine on control2.flatsqldb, the
//     control tables store-migrate copied out of control.flatsqldb (§14
//     interim, until T8). It opens on TRUNCATE at SQLite's default
//     synchronous=FULL: it no longer carries record bytes, so the WAL patch
//     and its NORMAL durability trade (flatsql_boot_state.go) are not taken.
//     It holds NO record table: sds_p_*, sdn_record_index, the tag, summary
//     and counter tables, the engine rows and the hot window do not exist
//     there, so a record path that was not moved to the partition store fails
//     loudly ("no such table") instead of reading or writing an empty copy.
//     The interim full-text index lives in it, keyed by gseq
//     (format2_daemon_fts.go).
//
// Open refuses a legacy store that store-migrate has not activated (§16.1-9);
// an empty data directory is created as a fresh format-2 store (A5). A
// format-1 open of an activated store is refused before it touches a file
// (newFormat1RefusesMigratedStore).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
)

// format2ControlDBName is the control instance's database (store-migrate
// writes it; A5 activation step 1).
const format2ControlDBName = "control2.flatsqldb"

// ErrFormat2Store: a format-1 open of a store store-migrate activated as
// format 2. The legacy binary path must never recreate empty record tables
// beside the partition store.
var ErrFormat2Store = errors.New("the store is format 2 (store-migrate activated it); start the daemon with SDN_STORE_FORMAT=2")

// ErrFormat2Unsupported: a record API the format-2 store does not serve (the
// reason is in the wrapped message). Never a silent empty answer.
var ErrFormat2Unsupported = errors.New("not available on store format 2")

// Test hooks: the daemon never compiles an artifact (A30); tests do.
var (
	format2CompileOnMiss = false
	format2AOTCacheDir   = func() string { return engineAOTCacheDir() }
)

// format2TopologyEnv overrides the instance sizing: "writers/interactive/bulk"
// or "writers/interactive/bulk/point" (e.g. "1/2/1", host-02's §5.1
// topology). Unset: §5.1 for the machine, 2 point lanes, 1 sandbox lane.
const format2TopologyEnv = "SDN_FORMAT2_TOPOLOGY"

func format2Topology() format2.Topology {
	t := format2.DefaultTopology(runtime.NumCPU())
	raw := strings.TrimSpace(os.Getenv(format2TopologyEnv))
	if raw == "" {
		return t
	}
	parts := strings.Split(raw, "/")
	if len(parts) != 3 && len(parts) != 4 {
		log.Warnf("format 2: ignoring %s=%q (want writers/interactive/bulk[/point])", format2TopologyEnv, raw)
		return t
	}
	var v [4]uint32
	for i, p := range parts {
		n, err := strconv.ParseUint(strings.TrimSpace(p), 10, 32)
		if err != nil || n == 0 || n > 64 {
			log.Warnf("format 2: ignoring %s=%q (%q is not 1..64)", format2TopologyEnv, raw, p)
			return t
		}
		v[i] = uint32(n)
	}
	return format2.Topology{Writers: v[0], InteractiveLanes: v[1], BulkLanes: v[2], PointLanes: v[3]}
}

// format2Daemon is the daemon-side state of a format-2 store.
type format2Daemon struct {
	ctx    context.Context
	cancel context.CancelFunc

	fts    *format2FTS
	lanes  f2LaneCache
	counts f2CountCache

	// producers caches, per partition token, the raw peer id of a copy it
	// holds: control entries (reconcile, tomb) are enqueued through it.
	producersMu sync.Mutex
	producers   map[string]string
}

// newFormat1RefusesMigratedStore is the format-1 guard: a format-4 store, or
// an activated format-2 store (fsql2/MIGRATED for its own STORE), never opens
// as format 1.
func newFormat1RefusesMigratedStore(basePath string) error {
	if err := format4RefusedByOtherFormats(basePath); err != nil {
		return err
	}
	migrated, err := format2.Migrated(basePath)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", filepath.Join(basePath, format2.Dir), err)
	}
	if migrated {
		return fmt.Errorf("%w (%s)", ErrFormat2Store, basePath)
	}
	return nil
}

// newFormat2Store is NewFlatSQLStore for SDN_STORE_FORMAT=2.
func newFormat2Store(basePath string, validator *sds.Validator, cfg storeConfig) (*FlatSQLStore, error) {
	// A format-4 store is refused before any file is touched.
	if err := format4RefusedByOtherFormats(basePath); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(basePath, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create storage directory: %w", err)
	}
	lock, err := acquireStoreLock(basePath)
	if err != nil {
		return nil, err
	}
	opened := false
	defer func() {
		if !opened {
			_ = lock.release()
		}
	}()
	migrated, err := format2.Migrated(basePath)
	if err != nil {
		return nil, err
	}
	if !migrated {
		if fi, err := os.Lstat(filepath.Join(basePath, flatSQLControlDBName)); err == nil && fi.Mode().IsRegular() {
			return nil, format2.ErrNotMigrated
		}
		if _, err := os.Stat(filepath.Join(basePath, format2.Dir, "STORE")); err == nil {
			return nil, format2.ErrNotMigrated // an unfinished migration
		}
	}

	dbPath := filepath.Join(basePath, "sdn.db") // salts the local-EPM key, as in format 1
	controlDBPath := filepath.Join(basePath, format2ControlDBName)

	auxiliaryMetadata, err := openAuxiliaryMetadataStore(filepath.Join(basePath, auxiliaryMetadataFileName), false)
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

	start := time.Now()
	engine, engineDB, mark, err := openControlInstance(basePath, controlDBPath)
	if err != nil {
		return nil, err
	}
	log.Infof("format 2: control instance on %s (%s) in %s", controlDBPath, controlDurabilityDescription(engineDB),
		time.Since(start).Round(time.Millisecond))
	db := flatsqldrv.Open(engineDB)
	if mib := resolveEnginePageCacheMiB(); mib > 0 {
		if _, err := db.Exec(fmt.Sprintf("PRAGMA cache_size = -%d", mib*1024)); err != nil {
			log.Warnf("format 2: control page cache: %v", err)
		}
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		engine.Close()
		return nil, fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
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
		engineSources:        map[string]bool{},
		engineResident:       map[string]int64{},
		engineSchemaLoaded:   map[string]bool{},
		engineExcluded:       map[string]bool{},
		engineEpoch:          1,
		poisonWatch:          newEnginePoisonWatch(),
		controlDBDurable:     true,
		controlDBPath:        controlDBPath,
		checkpointStop:       make(chan struct{}),
		checkpointDone:       make(chan struct{}),
		f2:                   &format2Daemon{ctx: ctx, cancel: cancel, producers: map[string]string{}},
	}
	// Record reads never consult the hot window: it does not exist here.
	store.engineHotHydrated.Store(true)
	auxiliaryOpened = true
	fail := func(err error) (*FlatSQLStore, error) {
		cancel()
		store.Close()
		return nil, err
	}

	auxResume := auxiliaryResumeOffset(mark, auxiliaryMetadata)
	store.bootAuxFrom = auxResume
	store.bootAuxWarm = auxResume > 0
	store.auxCheckpointedOffset.Store(auxResume)
	store.auxAppliedOffset.Store(auxResume)

	if err := store.initControlTables(); err != nil {
		return fail(fmt.Errorf("failed to initialize control tables: %w", err))
	}
	auxApplied, auxThrough, err := auxiliaryMetadata.ReplayFrom(store, auxResume)
	if err != nil {
		return fail(fmt.Errorf("failed to replay auxiliary metadata: %w", err))
	}
	store.noteAuxiliaryAppliedThrough(auxThrough)
	store.bootAuxFrames = auxApplied
	store.auxReplayed.Store(true)

	psStart := time.Now()
	ps, err := format2.Open(format2.StoreConfig{
		Root:          basePath,
		AOTCacheDir:   format2AOTCacheDir(),
		CompileOnMiss: format2CompileOnMiss,
		Topology:      format2Topology(),
		QuotaBytes:    uint64(max(cfg.quotaBytes, 0)),
		AllowFresh:    !migrated,
		FileIdentifier: func(schema string) (string, bool) {
			if validator == nil {
				return "", false
			}
			return validator.FileIdentifier(schema)
		},
	})
	if err != nil {
		return fail(fmt.Errorf("format 2: open the partition store: %w", err))
	}
	store.ps = ps
	store.rb = format2Backend{store}
	log.Infof("format 2: partition store open in %s (writer + interactive + bulk instances, %s)",
		time.Since(psStart).Round(time.Millisecond), describeFormat2Topology(format2Topology()))

	if err := store.Checkpoint(); err != nil {
		log.Warnf("format 2: initial checkpoint failed (nothing is lost): %v", err)
	}
	if interval := resolveCheckpointInterval(); interval > 0 {
		store.checkpointRunning.Store(true)
		go store.runCheckpointLoop(interval)
	}
	store.startEnginePoisonWatch()
	store.startFormat2FTS()
	opened = true
	return store, nil
}

func describeFormat2Topology(t format2.Topology) string {
	point := t.PointLanes
	if point == 0 {
		point = 2
	}
	return fmt.Sprintf("%d writer threads, %d interactive lanes, %d bulk lanes, %d point lanes, 1 sandbox lane", t.Writers, t.InteractiveLanes, t.BulkLanes, point)
}

// closeFormat2Locked stops the format-2 daemon state and the partition
// store (the backend's close: the partition store is open).
func (s *FlatSQLStore) closeFormat2Locked() error {
	if s.f2 != nil && s.f2.cancel != nil {
		s.f2.cancel()
		s.f2.counts.wait()
	}
	return s.ps.Close()
}

// Format2 reports whether this store runs store format 2.
func (s *FlatSQLStore) Format2() bool { return s != nil && s.ps != nil }

// PartitionStore exposes the format-2 partition store (nil on format 1):
// acceptance measurements and the maintenance API.
func (s *FlatSQLStore) PartitionStore() *format2.Store {
	if s == nil {
		return nil
	}
	return s.ps
}

// f2ctx is the context format-2 reads and writes run under: cancelled at
// Close, never a deadline (a lane statement is cancelled, not abandoned).
func (s *FlatSQLStore) f2ctx() context.Context {
	if s.f2 == nil || s.f2.ctx == nil {
		return context.Background()
	}
	return s.f2.ctx
}

// f2Closed answers ErrStoreClosed once Close ran.
func (s *FlatSQLStore) f2Closed() error {
	if s == nil || s.closed.Load() {
		return ErrStoreClosed
	}
	return nil
}
