package storage

// flatsql_boot_state.go — opening the disk-backed control database.
//
// THE CONTROL DATABASE IS THE RECORD STORE. Every record's bytes, its index row,
// its provenance tags and the node's own auxiliary tables live in ONE SQLite
// file written through FlatSQL's VFS (owner law, sdn-operating-model-streams-
// flatsql: "flatbuffers are streamed to flatsql which persists it directly to
// disk ... any node restart just uses whatever is on disk"). A boot OPENS that
// file. Nothing is re-derived, nothing is replayed, and a database that cannot
// be opened is a hard failure — there is no second copy to rebuild it from,
// so silently discarding it would be data loss dressed up as recovery.
//
// Two things still carry a mark inside the database:
//
//   - the AUXILIARY journal (auxiliary.flatsqlmeta): the node's own state —
//     encrypted local EPM, pin ledger, dataset shard publications, licences,
//     asset-pin audit. It is replayed from its persisted resume mark at every
//     boot, exactly as before, and it is the only journal this store keeps.
//   - the ENGINE hot window: the FlatSQL record vtabs are a bounded CACHE of the
//     routed standards, persisted by the engine into <db>.fsdata and its own
//     index tables. The mark names the sdn_record_index rowid the engine had
//     mirrored through when its record state was last flushed, so a warm boot
//     ingests only the records written after it (engine_residency.go).

import (
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

const (
	// flatSQLControlDBName is the file the record store lives in.
	//
	// IT IS DELIBERATELY NOT "sdn.db": `<basePath>/sdn.db` is the legacy v1
	// database whose path still salts the local-EPM store key and is what
	// Path() reports. The engine's database is a separate file.
	flatSQLControlDBName = "control.flatsqldb"

	// bootMarkFormatKey / bootMarkAuxOffsetKey / bootMarkAuxDigestKey carry the
	// auxiliary journal's resume mark; bootMarkEngineRowIDKey carries the
	// engine hot window's coverage. They are SQL column values, not SDS record
	// fields, so the IDL capitalization law does not apply.
	bootMarkFormatKey      = "flatsql_boot.format"
	bootMarkAuxOffsetKey   = "flatsql_boot.auxiliary_offset"
	bootMarkAuxDigestKey   = "flatsql_boot.auxiliary_digest"
	bootMarkEngineRowIDKey = "flatsql_boot.engine_rowid"

	// bootMarkFormat is bumped whenever the meaning of a mark changes. Format 1
	// carried a record-catalog journal offset; that journal no longer exists, so
	// a format-1 mark reads as "no mark" — the auxiliary journal replays from
	// the beginning once and the engine window is rebuilt from the tables.
	bootMarkFormat = "2"

	// auxiliaryCheckpointDirtyBytes is how much auxiliary journal may accumulate
	// past its mark before a checkpoint is worth taking. That journal is a slow
	// trickle (20–29 MB over the lifetime of a production box), so the threshold
	// is small enough that a SIGKILLed daemon replays a rounding error.
	auxiliaryCheckpointDirtyBytes = 256 << 10

	// checkpointInterval bounds how much engine hot-window work a crash can
	// cost: every interval the engine's record arena is flushed to disk and the
	// coverage mark advanced, so a warm boot ingests at most one interval's
	// worth of records into the window.
	checkpointInterval = 30 * time.Second
)

// checkpointIntervalEnv lets an operator retune the cadence on a live host
// without a rebuild. "0" disables the background checkpointer entirely (the
// marks are then only advanced at Close).
const checkpointIntervalEnv = "SDN_FLATSQL_CHECKPOINT_INTERVAL"

func resolveCheckpointInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv(checkpointIntervalEnv))
	if raw == "" {
		return checkpointInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		log.Warnf("[storage] ignoring %s=%q (%v); using default %s",
			checkpointIntervalEnv, raw, err, checkpointInterval)
		return checkpointInterval
	}
	return d
}

// engineRecordState is what the engine's OWN persisted record state said at
// open: how many records its on-disk stream + index made visible without a
// single re-ingest, and whether that state was usable at all.
type engineRecordState struct {
	// Records is the count the engine reports visible from its persisted
	// state (OpenState / ReindexAll).
	Records int
	// Warm is true when the engine opened persisted record state, so the
	// records are already resident and only the tail past the coverage mark
	// needs ingesting.
	Warm bool
}

// openEngineRecordState opens the engine's persisted record index for a
// disk-backed database at dbPath. A usable index is opened as-is (warm). A
// recoverable state error is answered by bounded engine steps that re-derive
// the index from the engine's own record stream, retaining the control tables.
func openEngineRecordState(db *flatsqlrt.Database, dbPath string, discarded bool) (engineRecordState, error) {
	n, err := db.OpenState()
	if err == nil {
		return engineRecordState{Records: n, Warm: true}, nil
	}
	if !flatsqlrt.StateRecoverable(err) {
		return engineRecordState{}, fmt.Errorf("%w: %v", errEngineStateUnrecoverable, err)
	}
	streamBytes := int64(engineStreamSize(dbPath))
	switch {
	case discarded:
		// discardEngineRecordState left an empty stream below the old mark on
		// purpose: the torn path below is the one that clears the index rows.
		log.Infof("FlatSQL engine record state discarded — starting an empty record arena; the hot window is rebuilt from the control tables")
	case !errors.Is(err, flatsqlrt.ErrStateAbsent):
		log.Warnf("FlatSQL engine record state unusable (%v) — re-deriving the index from the engine's on-disk record stream (%d bytes)", err, streamBytes)
	}
	if streamBytes > 0 {
		started := time.Now()
		for step := 1; ; step++ {
			done, stepErr := db.ReindexStep(256)
			if stepErr != nil {
				return engineRecordState{}, fmt.Errorf("%w: incremental engine record recovery: %v", errEngineStateUnrecoverable, stepErr)
			}
			if done {
				break
			}
			if step%64 == 0 {
				log.Infof("FlatSQL engine record recovery: %d bounded steps in %s", step, time.Since(started).Round(time.Millisecond))
			}
		}
		// OpenState restores records into an empty runtime. Reopening on this
		// populated handle would add them twice, so the caller must use a new
		// runtime to validate the committed index and obtain its exact count.
		return engineRecordState{}, errEngineStateReindexed
	}
	n, err = db.ReindexAll()
	if err != nil {
		return engineRecordState{}, fmt.Errorf("%w: reindex engine record state: %v", errEngineStateUnrecoverable, err)
	}
	return engineRecordState{Records: n, Warm: n > 0}, nil
}

// errEngineStateUnrecoverable marks an engine record state the boot cannot
// repair in place. The engine's record state is a CACHE of the control tables,
// so the answer is to discard <db>.fsdata and rebuild the hot window from the
// tables — never to touch the control database itself.
var errEngineStateUnrecoverable = errors.New("engine record state unrecoverable in place")

// errEngineStateReindexed: incremental recovery committed the new index
// without touching control rows. Reopen the runtime on the same files.
var errEngineStateReindexed = errors.New("engine record state reindexed; reopen runtime")

// errControlDatabaseUnusable marks a control database this boot refuses to
// open. It is the record store: the operator restores it or wipes the node.
var errControlDatabaseUnusable = errors.New("control database unusable")

// bootMark is what a control database says about itself at open.
type bootMark struct {
	// AuxOffset/AuxDigest name the auxiliary-metadata journal prefix already
	// applied to the tables. Zero means "replay the auxiliary journal from the
	// beginning".
	AuxOffset int64
	AuxDigest string
	// EngineRowID is the sdn_record_index rowid the engine hot window had been
	// mirrored through when its record state was last flushed. Zero means no
	// coverage is claimed.
	EngineRowID int64
}

// noteAuxiliaryApplied records that the auxiliary journal is applied through
// its current end.
//
// ── THE ORDERING THAT MAKES SAMPLING THE FILE LENGTH LEGAL ────────────────
//
// Eight of the nine auxiliary writers APPLY, then APPEND, both under s.mu
// (UpsertDirectoryRecord, SaveLocalEPM, pin_ledger.go,
// dataset_shard_publication.go x2, dataset_publication_replay_state.go,
// source_batch_license.go). For those, "the append returned" implies "my rows
// are committed", and every OTHER frame in the file belongs to a writer that
// has already released s.mu — so it finished its whole pair. Sampling the
// file's length at that moment therefore names only applied frames.
//
// THE NINTH WRITER IS INVERTED. The asset-pin lane
// (appendAndCommitAssetPinMutation, asset_pin_ledger.go) journals BEFORE it
// commits its SQL transaction — deliberately, so a crash cannot leave a
// committed mutation with no durable audit frame. Between its append and its
// commit there is a frame on disk whose rows do NOT exist. If a mark ever
// covered that frame, the next boot would skip it and the rows would be gone
// FOREVER. So that lane appends through appendAuxiliaryMetadataBeforeApply,
// which does not advance anything, and notes the offset only after its commit
// succeeds.
//
// The remaining hole is a commit that FAILS: the frame stays on disk,
// unapplied, and a later writer's sample would cover it. That failure already
// poisons the store (assetPinLedgerRecovery), and this function FAILS CLOSED on
// that flag: once the asset-pin ledger needs recovery the mark stops moving
// entirely, so the next boot replays from at or before the orphaned frame and
// re-applies it.
//
// Callers must hold s.mu and must have completed BOTH their apply and their
// append.
func (s *FlatSQLStore) noteAuxiliaryApplied() {
	if s == nil || s.auxiliaryMetadata == nil {
		return
	}
	if s.assetPinLedgerRecovery.Load() {
		return
	}
	s.noteAuxiliaryAppliedThrough(s.auxiliaryMetadata.validLength())
}

// noteAuxiliaryAppliedThrough records an explicit applied offset — used by the
// auxiliary replay, which knows exactly how far it got. Monotonic.
func (s *FlatSQLStore) noteAuxiliaryAppliedThrough(off int64) {
	if s == nil {
		return
	}
	for {
		cur := s.auxAppliedOffset.Load()
		if off <= cur || s.auxAppliedOffset.CompareAndSwap(cur, off) {
			return
		}
	}
}

// openControlEngine starts the FlatSQL engine WITH A REAL FILESYSTEM rooted at
// basePath and opens the control database. The database is ALWAYS kept: it is
// the record store, and the only outcomes are "opened" or "the boot fails".
//
// The engine's record state (<db>.fsdata plus its index tables inside the
// database) is the one thing that may be thrown away here, because it is a
// cache the hot-window rebuild re-derives from the tables (engine_residency.go).
func openControlEngine(basePath, dbPath string) (*flatsqlrt.Runtime, *flatsqlrt.Database, bootMark, engineBootPlan, error) {
	// PRE-FLIGHT: never hand the engine a file that is not a database. Opening
	// a garbage file with flatsql_open_db TRAPS the guest (-fignore-exceptions
	// lowers a C++ throw to `unreachable`) and poisons the whole runtime.
	if err := checkDatabaseFile(dbPath); err != nil {
		return nil, nil, bootMark{}, engineBootPlan{}, err
	}

	// Up to three attempts, each with a FRESH RUNTIME. A poisoned runtime cannot
	// be reused for anything — including the recovery open — so a retry has to
	// replace the engine, not just the file.
	//
	// `discard` carries one decision from an attempt to the next: the engine's
	// record state (arena + index + partition map) is a cache the next attempt
	// must throw away BEFORE it opens that state. A mostly-dead arena is only
	// recognisable once the state is open, and a poisoned runtime cannot be
	// asked anything, so both are answered by a fresh runtime that discards
	// first (tryOpenControlDatabase).
	var lastErr error
	discard := false
	for attempt := 0; attempt < 3; attempt++ {
		// PROBE FIRST, in its own runtime, then open for real. Both the
		// exclusion set (which decides the schema text) and the persisted
		// source list (which must be registered before the real database's
		// first query) are answers about the file's existing contents, and
		// asking the real database would BE that first query.
		probeStart := time.Now()
		plan := probeControlDatabase(basePath, dbPath)
		log.Infof("FlatSQL boot phase \"boot: probe control database (second engine, schema read)\" took %s",
			time.Since(probeStart).Round(time.Millisecond))

		engine, err := flatsqlrt.New(
			flatsqlrt.WithPrecompiledAOTCache(engineAOTCacheDir()),
			flatsqlrt.WithFileIORoot(basePath),
		)
		if err != nil {
			return nil, nil, bootMark{}, engineBootPlan{}, fmt.Errorf("failed to start FlatSQL engine: %w", err)
		}

		engine.SetPhase("boot: open control database for writing")
		openStart := time.Now()
		engineDB, mark, engineState, err := tryOpenControlDatabase(engine, dbPath,
			engineSchemaTextExcluding(plan.Excluded), enginePrepare(plan), discard)
		engine.SetPhase("")
		log.Infof("FlatSQL boot phase \"boot: open control database for writing\" took %s (views already current: %v)",
			time.Since(openStart).Round(time.Millisecond), plan.ViewsCurrent)
		discarded := discard
		discard = false
		if err == nil {
			// THE ARENA IS A CACHE, AND A CACHE THAT IS MOSTLY DEAD IS REBUILT,
			// NOT RECONCILED. The engine persists every row ever ingested and
			// forgets its tombstones, so a warm open resurrects every evicted
			// row and the reconcile re-tombstones them ONE GUEST CALL EACH —
			// measured on the dev node 2026-09-10: 5 minutes of boot for
			// ~2.5M forgotten tombstones against ~60k live rows. Once the dead
			// rows outnumber the live ones (the residency ledger says how many
			// are live), discarding the arena and refilling the bounded
			// window from the tables is cheaper, and the next flush writes an
			// arena that holds the window and nothing else.
			if !discarded && engineState.Warm && engineArenaMostlyDead(engineDB, engineState.Records) {
				log.Infof("FlatSQL engine record state holds %d rows but the residency ledger tracks far fewer — discarding the engine record state and rebuilding the hot window from the control tables", engineState.Records)
				engineDB.Destroy()
				engine.Close()
				discard = true
				continue
			}
			plan.EngineState = engineState
			return engine, engineDB, mark, plan, nil
		}

		lastErr = err
		engine.Close() // discards a poisoned runtime AND releases its file handles
		switch {
		case errors.Is(err, errEngineStateReindexed):
			log.Infof("FlatSQL engine record index rebuilt; reopening with the control tables retained")
			continue
		case errors.Is(err, errEngineStateUnrecoverable):
			// The engine's record state is a cache. Discard it whole and
			// reopen: the hot window is rebuilt from the tables.
			log.Warnf("FlatSQL engine record state at %s.fsdata is unusable (%v) — discarding it; the hot window will be rebuilt from the control tables", dbPath, err)
			discard = true
			continue
		case errors.Is(err, errEnginePrepareFailed):
			// The FILE is fine; the host-side registration is not.
			return nil, nil, bootMark{}, engineBootPlan{}, fmt.Errorf("failed to register engine file identifiers: %w", err)
		default:
			return nil, nil, bootMark{}, engineBootPlan{}, fmt.Errorf("%w: %s: %v", errControlDatabaseUnusable, dbPath, err)
		}
	}
	return nil, nil, bootMark{}, engineBootPlan{}, fmt.Errorf("%w: %s: %v", errControlDatabaseUnusable, dbPath, lastErr)
}

// errEnginePrepareFailed marks a failure of the pre-first-query preparation
// step — today, file-identifier registration.
//
// IT IS NOT EVIDENCE THAT THE CONTROL DATABASE IS BAD, and that distinction is
// the whole reason the sentinel exists. Before this work, registerEngineFileIDs
// ran AFTER the open, in NewFlatSQLStore, and a failure was a hard,
// NON-DESTRUCTIVE start failure that left the file exactly where it was.
// Running it in front of the first query put it inside tryOpenControlDatabase,
// whose error means "unusable database" and whose caller answers that by
// DELETING the control database and re-deriving from the journal — multi-GB on
// host-01/host-02. RegisterFileID is documented as THROWING on an unknown
// table and now runs 226 times per boot instead of 2, so that blast radius is
// not theoretical. Callers unwrap this sentinel and fail the start instead.
var errEnginePrepareFailed = errors.New("engine file-identifier registration")

// enginePrepare is the work that must happen between OpenDatabase and the
// database's FIRST QUERY, or not at all.
//
// The engine registers its SQLite virtual tables lazily, once, at the first
// query (FlatSQLDatabase::initializeSQLiteEngine) — and it registers exactly
// the tables that have a file identifier by then, base AND per-source shadow.
// Doing this here therefore costs one no-op `CREATE VIRTUAL TABLE IF NOT
// EXISTS` per already-persisted table; doing it after the first query costs a
// full unified-view rebuild instead (~20 ms per schema-changing statement on
// the disk-backed engine, 227 standards as measured, three statements each).
//
// On a store with NO persisted source there is nothing to gain and something
// to lose: the base vtabs would be created here only for CreateUnifiedViews to
// DROP them again. Cold stores therefore register their file identifiers after
// the open instead (NewFlatSQLStore).
//
// WHAT THIS DOES NOT BOUND, STATED PLAINLY, WITH THE MEASUREMENT. These calls
// only tell the engine what exists; the `CREATE VIRTUAL TABLE IF NOT EXISTS`
// statements are issued INSIDE the engine, by its lazy initializeSQLiteEngine,
// at the first query — so the host cannot wrap them in one transaction the way
// rebuildUnifiedViews wraps CreateUnifiedViews. On the FIRST boot after every
// standard became routed, host-01's seven persisted sources therefore
// materialize 228 base plus 228 x 7 shadow tables in ONE un-batched burst.
//
// PLUS THE DECORATIONS THE ENGINE DERIVES BY ITSELF, which the earlier
// arithmetic left out. The engine builds an R-Tree for any table whose column
// names read geospatial, and the schema-exact catalog trips that for TWELVE
// standards besides the intended $TBS — CRM, ENV, GNO, ION, OBT, SEN, SEO,
// SIT, SWR, TMS, TRK and, from the v1.198.0 pin, TXS; every one of them a
// genuinely geospatial standard (LAT/LON/ALT columns straight out of its IDL).
// MEASURED on the shipped engine: 13 `_rtree_*` virtual tables, each backed by
// three plain tables, so 52 extra schema objects created inside the same
// burst, plus per-ingest index maintenance for those twelve standards from
// then on. It is disclosed here and PINNED by
// TestEngineDerivedRTreesAreTheDisclosedSet, so the next catalog change that
// trips a thirteenth is a test failure and not a surprise on a droplet.
//
// $TXS EARNED ITS R-TREE; it is not catalog noise. A Terrestrial Transmitter
// Site carries LATITUDE/LONGITUDE straight out of its IDL and the RF catalogue
// is read BY AREA ("which transmitters cover this ground"), so the derived
// index is the one the query pattern actually wants. Its companion $STX takes
// none and should not: a Scheduled Transmission holds no position of its own,
// only a SITE_ID join back into $TXS. One new index for one new geospatial
// standard is the arithmetic working, not drifting.
//
// MEASURED on the shipped engine (laptop NVMe, journal_mode=TRUNCATE): the
// cold first-query burst is 63.0 s against an EMPTY control database and
// 59.9 s against a 784 MB one. The cost is SCHEMA-OBJECT bound, not
// database-size bound, so host-01's 6.8 GB and host-02's 3.4 GB do not inflate
// it — but the droplets' block-volume fsync will, expect roughly 2-5x, i.e.
// 2-5 minutes, on top of the catalog replay. It is genuinely ONCE: warm
// re-boots run the same statements as no-ops in 0.08-0.8 s. Engine memory is a
// non-issue (1816 TableStores ~ 20 MB RSS).
//
// THE OPERATIONAL EDGE THAT FOLLOWS FROM THAT NUMBER: the fleet's post-restart
// health budget (config UpdateConfig.HealthTimeout, default 600 s) covers the
// burst with room, but a box whose update.health_timeout_seconds has been set
// below ~300 s will fail its first post-flip health check and SELF-ROLL-BACK.
// Check it before rolling this to a box that overrides the default.
//
// Batching the burst is a change to the engine's own initialization (one
// transaction around it), filed for the engine owner alongside the
// errors-as-values note above.
func enginePrepare(plan engineBootPlan) func(*flatsqlrt.Database) error {
	if len(plan.Sources) == 0 {
		return nil
	}
	return func(db *flatsqlrt.Database) error {
		if err := registerEngineFileIDs(db, plan.Excluded); err != nil {
			return fmt.Errorf("%w: %w", errEnginePrepareFailed, err)
		}
		for _, source := range plan.Sources {
			if err := db.RegisterSource(source); err != nil {
				// Never fatal: a source that cannot be re-registered is a
				// source whose queries would have failed anyway.
				log.Warnf("FlatSQL boot: could not re-register persisted source %q: %v", source, err)
			}
		}
		return nil
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}

// engineArenaMostlyDead reports whether the engine's persisted rows outnumber
// the residency ledger's live rows by more than the live rows themselves,
// i.e. more than half the arena is evicted, deleted or superseded records
// whose tombstones the engine forgot. A ledger the database does not have yet
// (a first boot on this binary) counts as zero live rows: an untracked arena
// is rebuilt.
func engineArenaMostlyDead(db *flatsqlrt.Database, engineRecords int) bool {
	if engineRecords <= 0 {
		return false
	}
	var live int64
	res, err := db.Query(`SELECT COUNT(*) FROM sdn_engine_rows`)
	if err == nil && res != nil && len(res.Rows) == 1 && len(res.Rows[0]) == 1 {
		live, _ = res.Rows[0][0].(int64)
	}
	dead := int64(engineRecords) - live
	return dead > live
}

// sqliteFileHeader is the 16-byte magic every SQLite database starts with.
const sqliteFileHeader = "SQLite format 3\x00"

// checkDatabaseFile refuses to present a file that is plainly not a SQLite
// database to the engine. A missing file is a first boot; a zero-length file
// is what SQLite itself creates before the first write. Anything else that
// does not start with the magic is a hard failure — the file is the record
// store, and deleting it would be data loss.
func checkDatabaseFile(dbPath string) error {
	f, err := os.Open(dbPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect control database: %w", err)
	}
	hdr := make([]byte, len(sqliteFileHeader))
	n, readErr := io.ReadFull(f, hdr)
	f.Close()
	if n == 0 && (readErr == io.EOF || readErr == nil) {
		return nil
	}
	if readErr == nil && string(hdr) == sqliteFileHeader {
		return nil
	}
	return fmt.Errorf("%w: %s is not a SQLite database", errControlDatabaseUnusable, dbPath)
}

func tryOpenControlDatabase(engine *flatsqlrt.Runtime, dbPath, schemaText string, prepare func(*flatsqlrt.Database) error, discard bool) (*flatsqlrt.Database, bootMark, engineRecordState, error) {
	phase := time.Now()
	db, err := engine.OpenDatabase(schemaText, "sdn-control", dbPath, flatsqlrt.JournalWAL)
	if err != nil {
		return nil, bootMark{}, engineRecordState{}, err
	}
	// WAL, BUT NOT AT THE COST OF DURABILITY. The engine pairs WAL with
	// synchronous=NORMAL, which can lose the last commits on POWER LOSS (it
	// never corrupts). This database is the control store — the record store
	// IS control.flatsqldb — so its crash semantics are not something to trade
	// for throughput without being asked.
	//
	// Measured, same inserts, same commit cadence:
	//   TRUNCATE + FULL    1315 rows/s   38.0 ms/commit   (what this was)
	//   WAL      + FULL    3840 rows/s   13.0 ms/commit   (what this is)
	//   WAL      + NORMAL 19550 rows/s    2.5 ms/commit   (available, not taken)
	//
	// WAL+FULL is 2.9x faster than the mode it replaces with IDENTICAL
	// durability: every commit still fsyncs, it is just an append to the WAL
	// instead of write-journal, fsync, write-db, fsync, truncate. The 14.9x is
	// there for the taking if the owner decides losing a few seconds of
	// commits on power loss is acceptable; that is a product decision.
	if _, err := db.Query("PRAGMA synchronous=FULL"); err != nil {
		return nil, bootMark{}, engineRecordState{}, fmt.Errorf("set synchronous=FULL on the control database: %w", err)
	}
	log.Infof("FlatSQL boot phase \"boot: engine OpenDatabase\" took %s", time.Since(phase).Round(time.Millisecond))
	phase = time.Now()
	// BEFORE THE FIRST QUERY. IsDiskBacked/ReindexAll/verifyControlDatabase all
	// query, and the engine's virtual-table registration is a one-shot at the
	// first query — see enginePrepare.
	if prepare != nil {
		if err := prepare(db); err != nil {
			db.Destroy()
			return nil, bootMark{}, engineRecordState{}, err
		}
	}
	disk, err := db.IsDiskBacked()
	if err != nil {
		db.Destroy()
		return nil, bootMark{}, engineRecordState{}, err
	}
	if !disk {
		// The engine reported RAM for a real path. Never treat that as durable
		// — silently succeeding against memory is the exact defect the
		// disk-backed law exists to remove.
		db.Destroy()
		return nil, bootMark{}, engineRecordState{}, errors.New("engine opened a real path but reports NOT disk-backed")
	}
	log.Infof("FlatSQL boot phase \"boot: prepare + IsDiskBacked\" took %s", time.Since(phase).Round(time.Millisecond))

	// THE RECORD ARENA IS PERSISTED, AND THE BOOT USES IT. Every checkpoint
	// flushes the engine's record stream + index to <db>.fsdata before it
	// writes the coverage mark (flushEngineStateLocked). OpenState verifies
	// that index and makes the records visible without re-ingest. What it does
	// NOT carry is the hot-window tombstones; engine_residency.go re-applies
	// those from the residency table right after open.
	//
	// BUT ONLY A STATE THAT DESCRIBES ITS STREAM. The mark and the partition
	// map are checked against the file first (engine_record_state_discard.go);
	// a state that cannot be trusted is discarded whole, here, before the
	// engine reads any of it.
	phase = time.Now()
	if !discard {
		if reason, bad := engineRecordStateInconsistent(db, dbPath); bad {
			log.Warnf("FlatSQL engine record state at %s.fsdata does not describe its record stream (%s) — discarding it; the hot window will be rebuilt from the control tables", dbPath, reason)
			discard = true
		}
	}
	if discard {
		if err := discardEngineRecordState(db, dbPath); err != nil {
			db.Destroy()
			return nil, bootMark{}, engineRecordState{}, err
		}
	}
	log.Infof("FlatSQL boot phase \"boot: check engine record state against its stream\" took %s (discarded: %v)", time.Since(phase).Round(time.Millisecond), discard)
	endState := newBootPhaseBudget(engine).phase("boot: open engine record state (OpenState)")
	state, err := openEngineRecordState(db, dbPath, discard)
	endState()
	if err != nil {
		db.Destroy()
		return nil, bootMark{}, engineRecordState{}, err
	}
	phase = time.Now()

	if err := verifyControlDatabase(db); err != nil {
		db.Destroy()
		return nil, bootMark{}, engineRecordState{}, err
	}

	mark := readBootMark(db)
	log.Infof("FlatSQL boot phase \"boot: verify + read boot mark\" took %s", time.Since(phase).Round(time.Millisecond))
	return db, mark, state, nil
}

// verifyControlDatabase is the integrity gate.
//
// It is deliberately CHEAP and O(1): the file just opened without error, which
// means SQLite parsed its header and root page, and every table this store
// depends on is either created idempotently by initTables or re-derived. The
// expensive `PRAGMA integrity_check` walks every b-tree page and costs O(db) —
// it is available behind SDN_FLATSQL_BOOT_INTEGRITY_CHECK for an operator
// investigating a suspect store, but it is NOT on the boot path, because paying
// minutes of page walking to avoid minutes of replay is not a fix.
//
// The real safety property is not this function. It is that EVERY failure
// anywhere downstream — here, at the digest check, or mid-replay — falls back to
// full re-derivation from the journal and the stream files.
func verifyControlDatabase(db *flatsqlrt.Database) error {
	if _, err := db.Query("SELECT 1"); err != nil {
		return fmt.Errorf("control database does not answer queries: %w", err)
	}
	if strings.TrimSpace(os.Getenv("SDN_FLATSQL_BOOT_INTEGRITY_CHECK")) == "" {
		return nil
	}
	res, err := db.Query("PRAGMA integrity_check(1)")
	if err != nil {
		return fmt.Errorf("integrity_check: %w", err)
	}
	if len(res.Rows) != 1 || fmt.Sprint(res.Rows[0][0]) != "ok" {
		return fmt.Errorf("integrity_check reported %v", res.Rows)
	}
	return nil
}

// readBootMark reads the persisted marks. A missing table, a missing row, a
// bad number or a format bump all mean "no mark" — never an error.
func readBootMark(db *flatsqlrt.Database) bootMark {
	res, err := db.Query(
		`SELECT key, value FROM sdn_metadata WHERE key IN (?, ?, ?, ?)`,
		bootMarkFormatKey, bootMarkAuxOffsetKey, bootMarkAuxDigestKey, bootMarkEngineRowIDKey)
	if err != nil || res == nil {
		return bootMark{}
	}
	var format, auxOffset, auxDigest, engineRowID string
	for _, row := range res.Rows {
		if len(row) < 2 {
			continue
		}
		switch fmt.Sprint(row[0]) {
		case bootMarkFormatKey:
			format = fmt.Sprint(row[1])
		case bootMarkAuxOffsetKey:
			auxOffset = fmt.Sprint(row[1])
		case bootMarkAuxDigestKey:
			auxDigest = fmt.Sprint(row[1])
		case bootMarkEngineRowIDKey:
			engineRowID = fmt.Sprint(row[1])
		}
	}
	if format != bootMarkFormat {
		return bootMark{}
	}
	mark := bootMark{}
	if auxOffset != "" && auxDigest != "" {
		if aux, err := strconv.ParseInt(auxOffset, 10, 64); err == nil && aux >= 0 {
			mark.AuxOffset = aux
			mark.AuxDigest = auxDigest
		}
	}
	if engineRowID != "" {
		if e, err := strconv.ParseInt(engineRowID, 10, 64); err == nil && e > 0 {
			mark.EngineRowID = e
		}
	}
	return mark
}

// finishEngineSourceSetup completes engine source bring-up after the store is
// constructed: it guarantees the default partition exists on a store that has
// none, and rebuilds the unified views EXACTLY WHEN THEY NEED IT.
//
// THE RUNTIME HALF, AND WHY IT IS ALREADY DONE BY NOW. A FlatSQL source is two
// things: a `sqlite3_create_module_v2` registration (process state, gone on
// restart) and a `CREATE VIRTUAL TABLE "<Base>@<source>"` statement (flatsql
// cpp/src/sqlite_engine.cpp) that SURVIVES in the persisted schema. A
// persisted vtab whose module nothing registered fails with `no such module`
// on the first query to touch it. openControlEngine closes that by registering
// the probed sources BEFORE the database's first query, so the engine's lazy
// initializeSQLiteEngine registers every base AND shadow table itself — which
// costs one no-op `CREATE VIRTUAL TABLE IF NOT EXISTS` per table instead of a
// full view rebuild.
//
// WHY THE REBUILD IS CONDITIONAL. CreateUnifiedViews is all-or-nothing across
// the schema: DROP TABLE + DROP VIEW + CREATE VIEW for EVERY routed table.
// Measured on the disk-backed engine, a schema-changing statement costs ~20 ms
// (SQLite re-reads the whole schema after each one), so rebuilding 226 views
// is ~10 s — per boot, for views that are already correct. Skipping it when
// the persisted views already union exactly the registered sources takes a
// warm open from ~10.8 s back to ~0.1 s.
func (s *FlatSQLStore) finishEngineSourceSetup(plan engineBootPlan) error {
	rebuild := !plan.ViewsCurrent
	if len(s.engineSources) == 0 {
		// EVERY ROUTED BASE NAME MUST RESOLVE, INCLUDING ON AN EMPTY STORE.
		// Base tables materialize either through the engine's lazy
		// initializeSQLiteEngine or as unified views; a store with no source
		// has neither, so `SELECT _data FROM IRM` would answer "no such table"
		// instead of zero rows — an answer no caller can tell from a real
		// failure. Registering the default partition makes the answer EMPTY.
		if err := s.engineDB.RegisterSource(engineDefaultSource); err != nil {
			log.Warnf("FlatSQL boot: could not register the default engine source: %v", err)
			return nil
		}
		s.engineSources[engineDefaultSource] = true
		rebuild = true
	}
	if !rebuild {
		log.Infof("FlatSQL boot: %d engine source registration(s) restored; unified views already current", len(s.engineSources))
		return nil
	}
	// MEASURED, NOT ASSUMED (2026-08-27, host-02's real store: 226 routed
	// standards, 9 registered sources, 2,034 shadow partitions, AOT). This was
	// the leading suspect for the boot poison — one guest call whose work is
	// quadratic in (standards x sources) — and it is NOT: the all-at-once call
	// rebuilds every view in 391 ms, and a host-side per-standard rebuild of
	// the same 226 views took 732 ms. It stays as it is. The statement that
	// actually poisoned the engine was the hot-window read
	// (engine_records.go), and it is fixed there.
	if err := s.rebuildUnifiedViews(); err != nil {
		log.Warnf("FlatSQL boot: rebuild unified views over %d engine source(s): %v", len(s.engineSources), err)
		return nil
	}
	log.Infof("FlatSQL boot: rebuilt the unified views over %d engine source(s)", len(s.engineSources))
	return nil
}

// engineBootPlan is everything the store must know about an existing control
// database BEFORE it opens that database for real.
type engineBootPlan struct {
	// Excluded names the routed standards this store must not route.
	Excluded map[string]bool
	// Sources are the per-source partitions the persisted schema remembers.
	Sources []string
	// ViewsCurrent reports that the persisted unified views already union
	// exactly Sources for every routed standard.
	ViewsCurrent bool
	// EngineState is what the engine's persisted record state reported at
	// open.
	EngineState engineRecordState
}

// registeredSources is the store-side mirror of the sources enginePrepare
// registered on the engine before its first query. The two must agree from the
// first instruction: the engine would refuse a duplicate RegisterSource, and a
// store that thinks it has no sources would register the default partition and
// rebuild every view for nothing.
func (p engineBootPlan) registeredSources() map[string]bool {
	sources := make(map[string]bool, len(p.Sources))
	for _, source := range p.Sources {
		sources[source] = true
	}
	return sources
}

// engineProbeSchema is the schema the PROBE database is opened with: one table
// that is never given a file identifier, so the probe cannot create, drop or
// rename anything. SchemaParser refuses an empty schema, which is the only
// reason it declares a table at all.
const engineProbeSchema = `
  table SDN_ENGINE_BOOT_PROBE {
    PROBE:string;
  }
`

// engineUnprobedPlan is the FAIL-CLOSED answer to "what does this control
// database already contain?" — the plan a boot uses when the probe could not
// read the file at all.
//
// It routes ONLY the two decorated standards, i.e. the schema text this store
// shipped before every embedded standard became routed
// (engineRecordSchema + engineTBSTableGraph). Those two names cannot collide
// with a plain control table in any store: they have been engine-owned since
// loop B.3 and the cellular slice, so no migration can have left a plain table
// of either name behind. Every generically routed standard is dropped from the
// schema, which is what makes the boot incapable of issuing
// `DROP TABLE IF EXISTS "<CODE>"` against rows it never got to look at.
//
// The cost of being wrong in this direction is a boot whose sandboxed query
// surface answers "no such table" for the generic standards instead of zero
// rows. The cost of being wrong in the other direction is silent, permanent,
// unledgered row loss with no backup. They are not comparable.
func engineUnprobedPlan(what string, cause error) engineBootPlan {
	excluded := make(map[string]bool, len(engineRoutedSchemas))
	for name := range engineRoutedSchemas {
		if _, decorated := engineDecoratedSchemas[name]; decorated {
			continue
		}
		excluded[name] = true
	}
	log.Errorf("FlatSQL boot probe: %s: %v — the control database could not be inspected, so this boot routes ONLY the decorated standards. %d generically routed standard(s) answer \"no such table\" until a boot whose probe succeeds; nothing is dropped.",
		what, cause, len(excluded))
	return engineBootPlan{Excluded: excluded}
}

// probeControlDatabase reads what the control database already contains, in a
// SEPARATE runtime that is fully closed before the real one opens.
//
// IT HAS TO BE A SEPARATE DATABASE, and that is the whole point. Two decisions
// depend on the file's existing contents and BOTH must be made before the real
// database is opened or first queried:
//
//   - WHICH STANDARDS MAY BE ROUTED. This is a DATA-DESTRUCTION GUARD, not
//     tidiness: createUnifiedView issues `DROP TABLE IF EXISTS "<name>"` before
//     it creates the view (flatsql cpp/src/sqlite_engine.cpp), so routing a
//     standard whose code is already a PLAIN control table would DELETE that
//     table and every row in it — rows that are reachable today, because
//     recordReadSourceFiltered unions the bare canonical table when it exists.
//     Such a standard must be absent from the schema the database is created
//     from, not merely skipped in Go. It can only fire on a database migrated
//     from a pre-WS7.3d store; it fires FAIL-CLOSED (the standard is dropped
//     from the schema, its rows stay exactly where they are, the exclusion is
//     logged). The two DECORATED standards are deliberately not excludable:
//     they have been engine-owned since loop B.3 / the cellular slice, so
//     their canonical names were reserved before this change and excluding
//     them now would be a different regression, not a fix.
//
//     THIS IS THE ONLY COPY OF THAT RULE. It used to be duplicated in
//     storage.engineExcludedStandards, which nothing called; a fail-closed
//     guard with two implementations has one that silently drifts.
//
//   - WHICH SOURCES TO REGISTER. Registering them before the real database's
//     first query is what lets the engine's lazy initialization register the
//     shadow vtabs, which is what makes the view rebuild skippable.
//
// Asking the real database either question would itself be the first query, so
// the probe is a different database entirely. It is sequential — destroyed and
// closed before the real open — so there is never a second writer on the file,
// and it costs one engine start (~5 ms) plus one open.
//
// EVERY FAILURE FAILS CLOSED, and that is a correction: this probe used to
// return an EMPTY plan whenever it could not read the file. An empty plan is
// NOT "the behaviour this store had before the probe existed" — it is the full
// 226-standard schema plus registerEngineFileIDs over every standard, which is
// precisely the input that makes createUnifiedView issue
// `DROP TABLE IF EXISTS "<CODE>"` against a colliding plain control table.
// Nothing downstream re-checks (finishEngineSourceSetup,
// preregisterEngineSources and ensureEngineSource all rebuild the views with
// no collision test), so a fail-OPEN probe left a data-destruction guard with
// no guard at all on exactly the stores that need it — dev stores and old
// backups migrated from a pre-WS7.3d shape.
//
// A probe that cannot answer therefore answers NO (engineUnprobedPlan): the
// two DECORATED standards stay routed, because they have owned their canonical
// names since loop B.3 / the cellular slice and no store can hold a plain table
// of those names, and every GENERICALLY routed standard is un-routed for that
// boot. The store comes up on the pre-flip read surface — degraded, logged, and
// self-correcting on the next boot whose probe succeeds — never with a dropped
// table.
func probeControlDatabase(basePath, dbPath string) engineBootPlan {
	plan := engineBootPlan{Excluded: map[string]bool{}}
	if !fileExists(dbPath) {
		return plan
	}
	engine, err := flatsqlrt.New(
		flatsqlrt.WithPrecompiledAOTCache(engineAOTCacheDir()),
		flatsqlrt.WithFileIORoot(basePath),
	)
	if err != nil {
		return engineUnprobedPlan("start probe engine", err)
	}
	defer engine.Close()
	db, err := engine.OpenDatabase(engineProbeSchema, "sdn-engine-boot-probe", dbPath, flatsqlrt.JournalTruncate)
	if err != nil {
		return engineUnprobedPlan("open control database for probing", err)
	}
	defer db.Destroy()
	res, err := db.Query(`SELECT type, name, COALESCE(sql, '') FROM sqlite_master WHERE type IN ('table', 'view')`)
	if err != nil {
		return engineUnprobedPlan("read control schema", err)
	}

	plain := map[string]bool{}
	views := map[string]string{}
	sourceSet := map[string]bool{}
	for _, row := range res.Rows {
		if len(row) != 3 {
			continue
		}
		kind, _ := row[0].(string)
		name, _ := row[1].(string)
		text, _ := row[2].(string)
		switch {
		case kind == "view":
			views[name] = text
		case strings.Contains(text, "__flatsql_module_"):
			if at := strings.LastIndex(name, "@"); at >= 0 && at+1 < len(name) {
				sourceSet[name[at+1:]] = true
			}
		default:
			plain[name] = true
		}
	}

	collided := []string{}
	for name, binding := range engineRoutedSchemas {
		if _, decorated := engineDecoratedSchemas[name]; decorated {
			// $OMM and $TBS have owned their canonical names since loop B.3
			// and the cellular slice; un-routing them now would be a
			// different regression, not a fix.
			continue
		}
		switch {
		case plain[binding.Table]:
			plan.Excluded[name] = true
			collided = append(collided, name)
		}
	}
	sort.Strings(collided)
	if len(collided) > 0 {
		log.Errorf("FlatSQL boot: %v are NOT engine-routed in this store — a plain control table already holds each standard's canonical name and routing it would DROP that table. Their rows stay readable through the ordinary record read source.", collided)
	}

	plan.Sources = make([]string, 0, len(sourceSet))
	for source := range sourceSet {
		plan.Sources = append(plan.Sources, source)
	}
	sort.Strings(plan.Sources)
	plan.ViewsCurrent = len(plan.Sources) > 0 && engineViewsCoverSources(plan, views)
	return plan
}

// engineViewsCoverSources reports whether the PERSISTED views already union
// exactly plan.Sources for every standard this store will route, AND project
// exactly the columns this binary's schema text declares. A view that is
// missing, that names a partition nobody registered, or that still projects a
// previous catalog's column list means rebuild.
//
// THE COLUMN CHECK IS WHAT KEEPS THE PUBLIC LISTING HONEST. A unified view
// enumerates its projection EXPLICITLY (`CREATE VIEW "X" AS SELECT "A", "B",
// ..., "_data" FROM "X@src" UNION ALL ...`), so it is a persisted copy of the
// column list — and this rebuild used to be conditional on SOURCE coverage
// alone. An upgraded binary whose catalog gained or lost a projected field
// therefore kept serving the OLD view: `SELECT *` on the view answered the
// previous column list while the shadow vtabs (re-declared from the current
// text at connect) answered the new one, and PublicQuerySurface derives from
// the text. Rebuilding on a projection mismatch makes the text the single
// answer, at the cost of ONE all-or-nothing rebuild on the boot after a
// catalog change (warm boots with an unchanged catalog still rebuild zero
// times — TestBootRebuildsUnifiedViewsAtMostOnce).
func engineViewsCoverSources(plan engineBootPlan, views map[string]string) bool {
	for schemaName, binding := range engineRoutedSchemas {
		if plan.Excluded[schemaName] {
			continue
		}
		text, ok := views[binding.Table]
		if !ok {
			return false
		}
		if strings.Count(text, binding.Table+"@") != len(plan.Sources) {
			return false
		}
		for _, source := range plan.Sources {
			if !strings.Contains(text, `"`+binding.Table+"@"+source+`"`) {
				return false
			}
		}
		columns, ok := engineRelationColumns(binding.Table)
		if !ok || !engineViewProjectsColumns(text, columns) {
			return false
		}
	}
	return true
}

// engineViewProjectsColumns reports whether a persisted unified view's FIRST
// branch projects exactly `columns`, in order. Only the quoted identifiers are
// compared, so the check does not depend on how the engine spaces its SQL —
// and the first branch is enough because CreateUnifiedViews writes the same
// projection into every branch.
func engineViewProjectsColumns(text string, columns []string) bool {
	start := strings.Index(text, "SELECT ")
	if start < 0 {
		return false
	}
	rest := text[start+len("SELECT "):]
	end := strings.Index(rest, " FROM ")
	if end < 0 {
		return false
	}
	projected := quotedSQLIdentifiers(rest[:end])
	if len(projected) != len(columns) {
		return false
	}
	for i, name := range columns {
		if projected[i] != name {
			return false
		}
	}
	return true
}

// quotedSQLIdentifiers extracts the double-quoted identifiers of a SQL
// fragment in order, honouring the "" escape.
func quotedSQLIdentifiers(fragment string) []string {
	var out []string
	for i := 0; i < len(fragment); i++ {
		if fragment[i] != '"' {
			continue
		}
		var name strings.Builder
		i++
		for i < len(fragment) {
			if fragment[i] == '"' {
				if i+1 < len(fragment) && fragment[i+1] == '"' {
					name.WriteByte('"')
					i += 2
					continue
				}
				break
			}
			name.WriteByte(fragment[i])
			i++
		}
		out = append(out, name.String())
	}
	return out
}

// auxiliaryResumeOffset decides where the auxiliary replay starts. It returns
// 0 — a full replay — for every doubt: no mark, a mark past the journal's
// valid length, an unreadable prefix, or a digest that names a different file.
func auxiliaryResumeOffset(mark bootMark, aux *auxiliaryMetadataStore) int64 {
	if aux == nil || aux.f == nil || mark.AuxOffset <= 0 {
		return 0
	}
	valid := aux.validLength()
	if mark.AuxOffset > valid {
		log.Warnf("FlatSQL boot: persisted auxiliary mark %d is past the auxiliary journal's valid length %d — replaying it from the beginning",
			mark.AuxOffset, valid)
		return 0
	}
	digest, err := aux.digestPrefix(mark.AuxOffset)
	if err != nil {
		log.Warnf("FlatSQL boot: could not verify the auxiliary journal prefix (%v) — replaying it from the beginning", err)
		return 0
	}
	if digest != mark.AuxDigest {
		log.Warnf("FlatSQL boot: auxiliary journal prefix digest changed — replaying it from the beginning")
		return 0
	}
	return mark.AuxOffset
}

// Checkpoint flushes the engine's record state to disk, advances the engine
// coverage mark, and advances the auxiliary resume mark. It is what the
// background loop runs every interval and what Close runs once at the end.
//
// The expensive half of the auxiliary mark (fingerprinting the journal
// prefix) is computed OUTSIDE the store lock; only small upserts happen inside
// it. Observing the journal end outside the lock is sound: any writer that had
// already appended below `end` still held s.mu at that moment, so it commits
// its rows before we can take the lock. Under-covering is always safe.
func (s *FlatSQLStore) Checkpoint() error {
	return errors.Join(s.checkpointEngine(), s.checkpointAuxiliaryMark())
}

func (s *FlatSQLStore) checkpointEngine() error {
	if !s.controlDBDurable {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkpointEngineLocked()
}

// checkpointEngineLocked flushes the engine record arena and, if the window is
// hydrated, records the index rowid it covers. FLUSH FIRST, MARK SECOND: a
// mark may only ever claim records the engine can already serve from disk. A
// crash between the two costs one interval of re-ingest into the window on
// the next boot (engine_residency.go reconciles the duplicates away), never a
// missing record. Requires the store write lock.
func (s *FlatSQLStore) checkpointEngineLocked() error {
	if !s.controlDBDurable || s.engineDB == nil || s.db == nil {
		return nil
	}
	if err := s.flushEngineStateLocked(); err != nil {
		return err
	}
	s.engineUnflushed.Store(0)
	if !s.engineHotHydrated.Load() {
		return nil
	}
	var maxRowID int64
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(rowid), 0) FROM sdn_record_index`).Scan(&maxRowID); err != nil {
		return fmt.Errorf("checkpoint engine: read index high-water mark: %w", err)
	}
	return s.persistEngineMarkLocked(maxRowID)
}

// checkpointAuxiliaryMark advances the auxiliary resume mark to the offset this
// store has demonstrably applied.
func (s *FlatSQLStore) checkpointAuxiliaryMark() error {
	end, digest, ok, err := s.auxiliaryMarkCandidate()
	if err != nil || !ok {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistAuxiliaryMarkLocked(end, digest)
}

// auxiliaryMarkCandidate answers "what auxiliary mark may be written right now",
// or ok=false when none may be.
func (s *FlatSQLStore) auxiliaryMarkCandidate() (int64, string, bool, error) {
	if !s.controlDBDurable || s.auxiliaryMetadata == nil || s.auxiliaryMetadata.f == nil {
		return 0, "", false, nil
	}
	// Nothing may be marked before the replay has actually run: until then the
	// control tables do not describe the journal prefix at all.
	if !s.auxReplayed.Load() {
		return 0, "", false, nil
	}
	// FAIL CLOSED with noteAuxiliaryApplied: a poisoned asset-pin ledger means
	// there may be a journaled mutation whose rows never committed.
	if s.assetPinLedgerRecovery.Load() {
		return 0, "", false, nil
	}
	end := s.auxAppliedOffset.Load()
	if valid := s.auxiliaryMetadata.validLength(); end > valid {
		end = valid
	}
	if end <= 0 {
		return 0, "", false, nil
	}
	digest, err := s.auxiliaryMetadata.digestPrefix(end)
	if err != nil {
		return 0, "", false, fmt.Errorf("checkpoint auxiliary metadata: digest: %w", err)
	}
	return end, digest, true, nil
}

// flushEngineStateLocked persists the engine's record stream + index
// (flatsql_flush_index: append the new stream bytes, fsync them, then commit
// the index pages and the high-water mark in the same transaction). Requires
// the store write lock.
func (s *FlatSQLStore) flushEngineStateLocked() error {
	if !s.controlDBDurable || s.engineDB == nil {
		return nil
	}
	if s.engine != nil && s.engine.Poisoned() {
		return errors.New("checkpoint: engine poisoned — record state not flushed, mark withheld")
	}
	if err := s.engineDB.FlushIndex(); err != nil {
		return fmt.Errorf("checkpoint: flush engine record state: %w", err)
	}
	return nil
}

// checkpointLocked is Checkpoint for a caller that ALREADY holds the store
// write lock (Close). It pays for the auxiliary digest under the lock, once
// per process.
func (s *FlatSQLStore) checkpointLocked() error {
	auxErr := s.checkpointAuxiliaryMarkLocked()
	return errors.Join(s.checkpointEngineLocked(), auxErr)
}

func (s *FlatSQLStore) checkpointAuxiliaryMarkLocked() error {
	end, digest, ok, err := s.auxiliaryMarkCandidate()
	if err != nil || !ok {
		return err
	}
	return s.persistAuxiliaryMarkLocked(end, digest)
}

// persistEngineMarkLocked records the index rowid the engine hot window has
// been mirrored through. Requires the store write lock.
func (s *FlatSQLStore) persistEngineMarkLocked(rowID int64) error {
	if !s.controlDBDurable || rowID <= 0 {
		return nil
	}
	if err := s.upsertBootMarkRowsLocked("engine coverage", [][2]string{
		{bootMarkFormatKey, bootMarkFormat},
		{bootMarkEngineRowIDKey, strconv.FormatInt(rowID, 10)},
	}); err != nil {
		return err
	}
	s.engineMarkRowID.Store(rowID)
	return nil
}

// persistAuxiliaryMarkLocked writes the auxiliary handshake rows.
func (s *FlatSQLStore) persistAuxiliaryMarkLocked(end int64, digest string) error {
	if err := s.upsertBootMarkRowsLocked("auxiliary metadata", [][2]string{
		{bootMarkFormatKey, bootMarkFormat},
		{bootMarkAuxOffsetKey, strconv.FormatInt(end, 10)},
		{bootMarkAuxDigestKey, digest},
	}); err != nil {
		return err
	}
	s.auxCheckpointedOffset.Store(end)
	return nil
}

func (s *FlatSQLStore) upsertBootMarkRowsLocked(what string, rows [][2]string) error {
	// The store may have been closed while we waited for the lock. A mark is
	// worth nothing next to a nil-pointer dereference in a daemon.
	if s.db == nil {
		return nil
	}
	now := time.Now().Unix()
	for _, kv := range rows {
		if _, err := s.db.Exec(
			`INSERT INTO sdn_metadata(key, value, updated_at) VALUES(?, ?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			kv[0], kv[1], now); err != nil {
			return fmt.Errorf("checkpoint %s: persist %s: %w", what, kv[0], err)
		}
	}
	return nil
}

// runCheckpointLoop advances the marks periodically so a CRASH costs at most one
// interval. A clean shutdown checkpoints unconditionally in Close.
//
// It closes checkpointDone on the way out, and Close WAITS on that before it
// tears anything down: without the handshake the loop can already be inside
// Checkpoint, blocked on s.mu, when Close takes the lock and nils s.db.
func (s *FlatSQLStore) runCheckpointLoop(interval time.Duration) {
	defer close(s.checkpointDone)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.checkpointStop:
			return
		case <-ticker.C:
			if s.checkpointDirty() {
				if err := s.Checkpoint(); err != nil {
					log.Warnf("FlatSQL checkpoint failed: %v", err)
				}
			}
		}
	}
}

// stopCheckpointLoop signals the loop and waits for it to leave. Idempotent, and
// safe when the loop was never started. MUST be called WITHOUT the store lock:
// the loop may be waiting for exactly that lock.
func (s *FlatSQLStore) stopCheckpointLoop() {
	s.checkpointOnce.Do(func() {
		if s.checkpointStop != nil {
			close(s.checkpointStop)
		}
		if s.checkpointRunning.Load() {
			<-s.checkpointDone
		}
	})
}

// checkpointDirty reports whether there is anything worth a checkpoint: engine
// ingests since the last flush, or enough auxiliary journal past its mark.
func (s *FlatSQLStore) checkpointDirty() bool {
	if s.engineUnflushed.Load() > 0 {
		return true
	}
	if s.auxiliaryMetadata != nil && s.auxiliaryMetadata.f != nil &&
		s.auxAppliedOffset.Load()-s.auxCheckpointedOffset.Load() >= auxiliaryCheckpointDirtyBytes {
		return true
	}
	return false
}

// newAuxiliaryMetadataDigest starts a running frame-header digest for the
// auxiliary journal. The frame version and a domain prefix are mixed in first
// so a format change can never produce a colliding fingerprint.
func newAuxiliaryMetadataDigest() hash.Hash {
	h := sha256.New()
	fmt.Fprintf(h, "auxiliary-metadata-v%d\n", auxiliaryMetadataFrameVersion)
	return h
}

// extendJournalDigest folds the {length, payload CRC32} frame headers in
// [*off, limit) into h and advances *off. Headers only: each carries the CRC of
// its payload, so the digest transitively covers every payload byte in the
// prefix in cheap 8-byte reads. Caller holds the journal lock.
func extendJournalDigest(h hash.Hash, f *os.File, off *int64, limit int64) error {
	var hdr [8]byte
	for *off < limit {
		if limit-*off < 8 {
			return fmt.Errorf("journal prefix %d ends mid-header at %d", limit, *off)
		}
		if _, err := f.ReadAt(hdr[:], *off); err != nil {
			return err
		}
		n := int64(uint32(hdr[0]) | uint32(hdr[1])<<8 | uint32(hdr[2])<<16 | uint32(hdr[3])<<24)
		if n <= 0 || *off+8+n > limit {
			return fmt.Errorf("journal prefix %d ends mid-frame at %d", limit, *off)
		}
		h.Write(hdr[:])
		*off += 8 + n
	}
	return nil
}

// sealJournalDigest snapshots the running state and binds it to a specific
// length. It CLONES rather than finalising, so the running state stays usable
// for the next checkpoint.
func sealJournalDigest(h hash.Hash, limit int64) (string, error) {
	m, ok := h.(encoding.BinaryMarshaler)
	if !ok {
		return "", errors.New("journal digest is not cloneable")
	}
	state, err := m.MarshalBinary()
	if err != nil {
		return "", err
	}
	clone := sha256.New()
	u, ok := clone.(encoding.BinaryUnmarshaler)
	if !ok {
		return "", errors.New("journal digest clone is not restorable")
	}
	if err := u.UnmarshalBinary(state); err != nil {
		return "", err
	}
	fmt.Fprintf(clone, ":%d", limit)
	return hex.EncodeToString(clone.Sum(nil)), nil
}
