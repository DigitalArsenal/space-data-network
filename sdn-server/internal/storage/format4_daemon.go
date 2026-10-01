package storage

// format4_daemon.go — the daemon on store format 4, "p4": one SQLite file per
// partition (producer x record type), in the FlatSQL engine (stack design
// docs/architecture/flatsql-sqlite-partitions.md §10, G7; build-out contract
// §5.4, §5.5, C-32).
//
// SDN_STORE_FORMAT=4 (or "sqlite", any case) selects it; unset is format 1
// and "2" is format 2, both unchanged. NewFlatSQLStore then opens:
//
//   - THE ENGINE (internal/storage/format4): one threaded instance holding
//     every record, its tags, the lane counters, the type indexes and the
//     full-text index under <data>/fsql4/. Every record read and write of the
//     node goes there through format4Backend (format4_daemon_writes.go,
//     format4_daemon_reads.go). No record path takes s.mu or the control
//     instance's lock, and no read waits on a writer.
//   - THE CONTROL INSTANCE (control_instance.go) on <data>/fsql4/control.db:
//     the control tables only, as format 2's.
//
// Format 4 opens an activated format-4 store, or creates a fresh one in an
// empty data directory. It refuses formats 2 and 3 by name and a format-1
// store store-migrate has not migrated (§2.4); formats 1 and 2 refuse a
// format-4 store before they touch a file (format4RefusedByOtherFormats).
// The duplicate rank (W-h) and the engine hot window (W-m) do not exist here.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
)

// format4ControlDBName is the control instance's database under fsql4/.
const format4ControlDBName = "control.db"

// format4CloseDeadline bounds the engine's stop at Close: the node's
// shutdown drain (cmd/spacedatanetwork nodeDrainTimeout).
const format4CloseDeadline = 30 * time.Second

// ErrFormat4Store: a format-1 or format-2 open of a format-4 store. Neither
// may recreate record tables beside it.
var ErrFormat4Store = errors.New("the store is format 4; start the daemon with SDN_STORE_FORMAT=4")

// Test hooks: the daemon never compiles an artifact; tests do, and the
// backend tests run on format4test's Fake.
var (
	format4CompileOnMiss = false
	format4AOTCacheDir   = func() string { return engineAOTCacheDir() }
	format4OpenEngine    = func(ctx context.Context, opt format4.Options) (format4.API, error) {
		e, err := format4.Open(ctx, opt)
		if err != nil {
			return nil, err
		}
		return e, nil
	}
)

// format4Daemon is the daemon-side state of a format-4 store.
type format4Daemon struct {
	// ctx is what record reads and writes run under: cancelled at Close,
	// never a deadline.
	ctx    context.Context
	cancel context.CancelFunc
	opt    format4.Options

	// engine is the open engine. A trap fences it (every call ErrStopped)
	// and reopen swaps in a fresh one; init replays the journal.
	engine    atomic.Pointer[format4Engine]
	reopening atomic.Bool

	// types are the schemas registered with the engine (C-5 keeps them on
	// disk; a write registers a schema the open did not).
	typesMu sync.Mutex
	types   map[string]bool
	ident   func(schema string) (string, bool)
}

// format4Engine boxes the API for the atomic pointer.
type format4Engine struct{ format4.API }

func (d *format4Daemon) api() format4.API { return d.engine.Load().API }

// format4RefusedByOtherFormats is the format-1 and format-2 guard: a store
// with format-4 markers never opens as either, and nothing is touched first.
func format4RefusedByOtherFormats(basePath string) error {
	m, err := marker.Read(basePath)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", filepath.Join(basePath, marker.Dir), err)
	}
	if m.Format4() {
		return fmt.Errorf("%w (%s)", ErrFormat4Store, basePath)
	}
	return nil
}

// format2StorePresent reports format-2 or format-3 markers (fsql2/STORE or
// fsql2/MIGRATED, a finished or unfinished format-2 migration).
func format2StorePresent(basePath string) (bool, error) {
	for _, name := range []string{"STORE", "MIGRATED"} {
		if _, err := os.Lstat(filepath.Join(basePath, format2.Dir, name)); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

// format4CreateMode decides how the engine opens <data>/fsql4 (§5.5): an
// activated store opens; an activation store-migrate left half done is
// finished first; a format-1 store it has not migrated is refused; an
// empty data directory is created fresh. Caller holds the store lock.
func format4CreateMode(basePath string) (format4.CreateMode, error) {
	if f2, err := format2StorePresent(basePath); err != nil {
		return 0, err
	} else if f2 {
		return 0, fmt.Errorf("%w (%s)", format4.ErrWrongFormat, basePath)
	}
	m, err := marker.Read(basePath)
	if err != nil {
		return 0, err
	}
	if m.NeedsFinish() {
		if err := marker.FinishActivation(basePath); err != nil {
			return 0, fmt.Errorf("format 4: finish the activation: %w", err)
		}
		log.Infof("format 4: finished the activation store-migrate began (%s moved to %s)", marker.LegacyControl, marker.PreFormat4Dir)
		if m, err = marker.Read(basePath); err != nil {
			return 0, err
		}
	}
	switch {
	case m.Activated() || m.LegacyControlDir:
		// The engine checks the markers (P4_E_FORMAT when they are not an
		// activated store's).
		return format4.OpenExisting, nil
	case m.LegacyControlFile:
		return 0, fmt.Errorf("%w (%s)", format4.ErrNotMigrated, basePath)
	default:
		return format4.CreateFresh, nil
	}
}

// newFormat4Store is NewFlatSQLStore for SDN_STORE_FORMAT=4.
func newFormat4Store(basePath string, validator *sds.Validator, cfg storeConfig) (*FlatSQLStore, error) {
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
	mode, err := format4CreateMode(basePath)
	if err != nil {
		return nil, err
	}

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

	ctx, cancel := context.WithCancel(context.Background())
	d := &format4Daemon{ctx: ctx, cancel: cancel, types: map[string]bool{}}
	if validator != nil {
		d.ident = validator.FileIdentifier
	}
	dataRoot, err := filepath.Abs(basePath)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("format 4: %w", err)
	}
	d.opt = format4.Options{
		DataRoot:      dataRoot,
		Create:        mode,
		GseqFloor:     1,
		Cores:         runtime.NumCPU(),
		AOTCacheDir:   format4AOTCacheDir(),
		CompileOnMiss: format4CompileOnMiss,
	}
	d.opt.OnFailure = d.onFailure
	engineStart := time.Now()
	api, err := format4OpenEngine(ctx, d.opt)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("format 4: open the engine: %w", err)
	}
	d.engine.Store(&format4Engine{api})
	// A reopen after a trap opens what is now an activated store.
	d.opt.Create = format4.OpenExisting
	log.Infof("format 4: engine open in %s (%s)", time.Since(engineStart).Round(time.Millisecond), describeFormat4Mode(mode))
	closeEngine := func() {
		cancel()
		cctx, ccancel := context.WithTimeout(context.Background(), format4CloseDeadline)
		_ = d.api().Close(cctx)
		ccancel()
	}

	// The control instance lives beside the partitions.
	controlDir := filepath.Join(basePath, marker.Dir)
	if err := os.MkdirAll(controlDir, 0o700); err != nil {
		closeEngine()
		return nil, fmt.Errorf("format 4: %w", err)
	}
	dbPath := filepath.Join(basePath, "sdn.db") // salts the local-EPM key, as in format 1
	controlDBPath := filepath.Join(controlDir, format4ControlDBName)
	start := time.Now()
	engine, engineDB, mark, err := openControlInstance(basePath, controlDBPath)
	if err != nil {
		closeEngine()
		return nil, err
	}
	log.Infof("format 4: control instance on %s (%s) in %s", controlDBPath, controlDurabilityDescription(engineDB),
		time.Since(start).Round(time.Millisecond))
	db := flatsqldrv.Open(engineDB)
	if mib := resolveEnginePageCacheMiB(); mib > 0 {
		if _, err := db.Exec(fmt.Sprintf("PRAGMA cache_size = -%d", mib*1024)); err != nil {
			log.Warnf("format 4: control page cache: %v", err)
		}
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		engine.Close()
		closeEngine()
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
		f4:                   d,
	}
	store.rb = format4Backend{s: store, d: d}
	// Record reads never consult a hot window: none exists here.
	store.engineHotHydrated.Store(true)
	auxiliaryOpened = true
	fail := func(err error) (*FlatSQLStore, error) {
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

	if mode == format4.CreateFresh {
		// Last: pre-format-4 binaries now fail loudly on this store (§2.1).
		if err := createLegacyControlDir(basePath); err != nil {
			return fail(err)
		}
	}
	if err := d.registerTypes(validator); err != nil {
		return fail(err)
	}
	if cfg.quotaBytes > 0 {
		if err := d.api().SetQuota(cfg.quotaBytes); err != nil {
			return fail(fmt.Errorf("format 4: set the quota: %w", err))
		}
	}

	if err := store.Checkpoint(); err != nil {
		log.Warnf("format 4: initial checkpoint failed (nothing is lost): %v", err)
	}
	if interval := resolveCheckpointInterval(); interval > 0 {
		store.checkpointRunning.Store(true)
		go store.runCheckpointLoop(interval)
	}
	store.startEnginePoisonWatch()
	opened = true
	return store, nil
}

func describeFormat4Mode(mode format4.CreateMode) string {
	if mode == format4.CreateFresh {
		return "a fresh store, or the one this directory holds"
	}
	return "an activated store"
}

// createLegacyControlDir makes <data>/control.flatsqldb a directory, so a
// binary from before format 4 fails on it instead of creating an empty
// format-1 store beside this one.
func createLegacyControlDir(basePath string) error {
	p := filepath.Join(basePath, marker.LegacyControl)
	if err := os.Mkdir(p, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("format 4: create %s: %w", p, err)
	}
	dir, err := os.Open(basePath)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// format4TypeSpec is a schema's engine registration. A routed standard with
// no embedded binary schema (an encrypted one) still stores and serves its
// frames by CID, arrival and tags; the engine extracts nothing from it.
func (d *format4Daemon) format4TypeSpec(schema string) (format4.TypeSpec, error) {
	spec, err := format4.TypeSpecFor(schema)
	if err == nil || d.ident == nil {
		return spec, err
	}
	typ, terr := sds.SchemaNameToTable(schema)
	if terr != nil {
		return spec, err
	}
	ident, ok := d.ident(typ + ".fbs")
	if !ok || len(ident) != 4 {
		return spec, err
	}
	fallback := format4.TypeSpec{TypeSpec: format2.TypeSpec{SchemaName: typ + ".fbs", Flags: format2.TypeVerifyCID}, PageSize: 4096,
		A18Bound: 10000}
	copy(fallback.FID[:], ident)
	return fallback, nil
}

// registerTypes registers every standard the validator embeds (§5.5 step 7).
// Re-registering identical bytes is a no-op in the engine.
func (d *format4Daemon) registerTypes(validator *sds.Validator) error {
	if validator == nil {
		return nil
	}
	for _, schema := range validator.Schemas() {
		if _, err := sds.SchemaNameToTable(schema); err != nil {
			continue
		}
		if err := d.ensureType(schema); err != nil {
			log.Warnf("format 4: %s is not registered: %v", schema, err)
		}
	}
	return nil
}

// ensureType registers schema with the engine once per daemon.
func (d *format4Daemon) ensureType(schema string) error {
	d.typesMu.Lock()
	defer d.typesMu.Unlock()
	if d.types[schema] {
		return nil
	}
	spec, err := d.format4TypeSpec(schema)
	if err != nil {
		return err
	}
	if err := d.api().RegisterType(spec); err != nil {
		return fmt.Errorf("register %s: %w", schema, err)
	}
	d.types[schema] = true
	return nil
}

// onFailure is the engine's OnFailure: a trap or hang fenced it. The daemon
// reopens it (init replays the journal); calls meanwhile answer ErrStopped.
func (d *format4Daemon) onFailure(err error) {
	log.Errorf("format 4: the engine failed and is fenced (%v); reopening", err)
	if !d.reopening.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer d.reopening.Store(false)
		for attempt := 1; d.ctx.Err() == nil; attempt++ {
			api, err := format4OpenEngine(d.ctx, d.opt)
			if err == nil {
				old := d.engine.Swap(&format4Engine{api})
				cctx, cancel := context.WithTimeout(context.Background(), time.Second)
				_ = old.Close(cctx)
				cancel()
				d.typesMu.Lock()
				d.types = map[string]bool{} // re-registered on use (a no-op for persisted specs)
				d.typesMu.Unlock()
				log.Infof("format 4: engine reopened after %d attempt(s)", attempt)
				return
			}
			log.Errorf("format 4: reopen attempt %d failed: %v", attempt, err)
			select {
			case <-d.ctx.Done():
				return
			case <-time.After(format4ReopenBackoff(attempt)):
			}
		}
	}()
}

// format4ReopenBackoff is the wait before reopen attempt+1: a second per
// attempt, at most 30 s.
func format4ReopenBackoff(attempt int) time.Duration {
	if d := time.Duration(attempt) * time.Second; d < 30*time.Second {
		return d
	}
	return 30 * time.Second
}

// close stops the engine within the shutdown drain deadline.
func (d *format4Daemon) close() error {
	d.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), format4CloseDeadline)
	defer cancel()
	return d.api().Close(ctx)
}

// Format4 reports whether this store runs store format 4.
func (s *FlatSQLStore) Format4() bool { return s != nil && s.f4 != nil }
