package storage

// flatsql_boot_state.go — the WARM BOOT handshake between the disk-backed
// FlatSQL control tables and the record-catalog journal.
//
// WHAT THIS REPLACES. Until this landed, `engine.CreateDatabase` opened a
// brand-new IN-MEMORY database on every process start, so the control tables
// that feed /api/v1/stats, /api/v1/data/index and every query-selected export
// were empty at boot and had to be rebuilt by replaying the entire
// record-catalog journal — 5 minutes on host-01, hours on the old host-02
// shape. record_catalog_replay.go ruled a persisted replay cursor UNSOUND for
// exactly that reason: "the control tables a saved cursor would resume into are
// always EMPTY", and seeking past frame N without applying frames [0,N) would
// silently drop every record those frames describe.
//
// That ruling is now OBSOLETE, and this file is why: the control-table STATE
// survives the restart, so an offset means something.
//
// ── THE HANDSHAKE ─────────────────────────────────────────────────────────
//
// The mark is a byte offset into record-catalog.flatsqlmeta, stored in
// sdn_metadata INSIDE the engine's own disk-backed database, together with a
// digest that identifies WHICH journal it belongs to.
//
// Three properties make it sound, and each one is load-bearing:
//
//  1. THE MARK IS ONLY EVER WRITTEN UNDER THE STORE WRITE LOCK. Every live
//     writer holds s.mu across the pair (commit control rows, append journal
//     frames) — see storeRecordBatch's `tx.Commit()` then
//     `recordCatalog.AppendAll`. Acquiring that same lock before persisting the
//     mark means every writer that had already appended has necessarily
//     RELEASED the lock, and therefore finished committing its rows, WITHOUT
//     this file having to audit the internal ordering of all nine append sites.
//     Under-estimating the mark is always safe (it costs replay);
//     over-estimating it is the unsound thing, and the lock is what makes that
//     unreachable. Note that the journal END is SAMPLED OUTSIDE the lock on
//     purpose — see CheckpointRecordCatalog for why that stays sound and why
//     it matters for reader latency.
//
//  2. THE MARK IS COMMITTED TO THE SAME DURABLE ARTIFACT AS THE ROWS. It is a
//     row in a table in the same SQLite file, written through FlatSQL's VFS,
//     in TRUNCATE journal mode. A crash cannot leave the mark committed and the
//     rows not.
//
//  3. THE DIGEST BINDS THE MARK TO ONE JOURNAL. Compaction REWRITES the journal
//     (writeCompactedJournalSnapshot + rename), which makes every prior offset
//     meaningless. The digest covers the frame headers of the prefix [0, mark),
//     each of which carries that frame's payload CRC, so a rewritten journal
//     cannot match a mark taken against the old one. A mismatch degrades to the
//     cold path, which is exactly today's behaviour.
//
// ── THE FALLBACK IS ALWAYS TODAY'S BEHAVIOUR ──────────────────────────────
//
// Missing file, unreadable file, refused filesystem, mismatched digest, engine
// state error, mark past the journal's valid length: every one of them takes
// the COLD path — wipe the database files, open fresh, replay the whole journal
// from zero. The fail-closed export gate (ErrRecordCatalogHydrating, 2a2ffea5)
// is unchanged and still guards every path until hydration completes. Warm boot
// is strictly an optimisation over a correct baseline; it can never be the only
// thing standing between the node and correct data.

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
	// flatSQLControlDBName is the file the engine's control tables live in.
	//
	// IT IS DELIBERATELY NOT "sdn.db", AND THAT IS NOT COSMETIC.
	// `<basePath>/sdn.db` is the LEGACY v1 database — the modernc/go-sqlite file
	// `MigrateLegacyControl` reads from, in place, at the same basePath
	// (migrate_legacy.go, migrate_legacy_test.go's v1 fixture). While the engine
	// was `:memory:` the string `<basePath>/sdn.db` named a file only the
	// migration ever touched. Pointing the engine's disk-backed database at that
	// same path would put TWO WRITERS on one SQLite file (the Go driver and the
	// wasm engine) and — far worse — would let this file's own corrupt-database
	// recovery DELETE A USER'S LEGACY DATABASE before it had been migrated.
	//
	// s.dbPath keeps its historical value and all its other jobs (it salts the
	// local-EPM store key and is what Path() reports); the control database is a
	// separate, new file.
	flatSQLControlDBName = "control.flatsqldb"

	// bootMarkOffsetKey / bootMarkDigestKey / bootMarkFormatKey are the
	// sdn_metadata keys carrying the handshake. They are SQL column values, not
	// SDS record fields, so the IDL capitalization law does not apply.
	bootMarkOffsetKey = "flatsql_boot.record_catalog_offset"
	bootMarkDigestKey = "flatsql_boot.record_catalog_digest"
	bootMarkFormatKey = "flatsql_boot.format"

	// bootMarkAuxOffsetKey / bootMarkAuxDigestKey are the SECOND, independent
	// half of the handshake, for the OTHER journal.
	//
	// THERE ARE TWO JOURNALS AND THEY ARE UNRELATED FILES:
	// record-catalog.flatsqlmeta and auxiliary.flatsqlmeta. Until this landed
	// only the first had a mark, so `auxiliaryMetadataStore.Replay` re-applied
	// its whole file on EVERY boot — free while the control tables were
	// `:memory:`, and 211 s of fsync-per-event once they became a
	// TRUNCATE-journalled disk database (task flatsql-aux-replay-resume-mark,
	// measured A/B/A on vm-orbit-det-01: 4.6 s -> 211 s, cold and warm
	// identical, which is the signature of work with no resume mark).
	//
	// The two marks are read and written together but decided SEPARATELY: a
	// warm catalog with a cold auxiliary journal (or the reverse) is legal and
	// costs only the replay it names.
	bootMarkAuxOffsetKey = "flatsql_boot.auxiliary_offset"
	bootMarkAuxDigestKey = "flatsql_boot.auxiliary_digest"

	// bootMarkFormat is bumped whenever the meaning of the mark changes. A
	// different value takes the cold path rather than misreading an old mark.
	//
	// The auxiliary keys did NOT bump it: they are additive and OPTIONAL under
	// format 1. A database written before they existed simply has no auxiliary
	// mark, which reads as "replay the auxiliary journal from the beginning" —
	// the exact behaviour it had. Bumping instead would have forced every host
	// to discard a perfectly good control database and re-derive the whole
	// record catalog once, for nothing.
	bootMarkFormat = "1"

	// checkpointDirtyBytes is how much journal may accumulate past the mark
	// before a checkpoint is worth taking. A checkpoint costs one small SQL
	// transaction; 4 MiB of frames is tens of thousands of records, so this is
	// cheap insurance against a CRASH (a clean shutdown always checkpoints).
	checkpointDirtyBytes = 4 << 20

	// auxiliaryCheckpointDirtyBytes is the same insurance for the auxiliary
	// journal, at a much lower threshold: that journal is a slow trickle
	// (directory upserts, publications, pin-ledger rows — 20–29 MB accumulated
	// over the LIFETIME of a production box), so a 4 MiB threshold would in
	// practice mean "only ever at shutdown". 256 KiB keeps a SIGKILLed daemon's
	// auxiliary replay to a rounding error while still costing at most a few
	// checkpoints a day.
	auxiliaryCheckpointDirtyBytes = 256 << 10

	// checkpointInterval bounds how much wall-clock a crash can cost even on a
	// slow trickle of writes.
	checkpointInterval = 30 * time.Second
)

// checkpointIntervalEnv lets an operator retune the cadence on a live host
// without a rebuild. "0" disables the background checkpointer entirely (the
// mark is then only advanced after boot replay and at Close).
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

// bootMark is the persisted resume point read out of a warm control database.
// Offset/Digest name the record-catalog journal; AuxOffset/AuxDigest name the
// auxiliary-metadata journal. Either pair may be absent (zero), independently.
type bootMark struct {
	Offset    int64
	Digest    string
	AuxOffset int64
	AuxDigest string
}

// bootResume is what the warm/cold decision yields: one resume offset per
// journal, and whether the control database may be KEPT at all. Zero offsets
// mean "replay this journal from the beginning".
type bootResume struct {
	Catalog   int64
	Auxiliary int64
	Keep      bool
}

// controlDatabaseMayBeKept answers the question openControlEngine actually
// needs, which is NOT "is the record catalog resuming?" but "can this
// database's existing rows survive the replay that is about to happen?".
//
// Resuming answers yes. So does an EMPTY record-catalog journal, and that
// second case is not hypothetical: a node that has published nothing yet still
// accumulates auxiliary frames (directory records off the accounts feed, pin
// ledger rows, licences). Keying the decision on the catalog offset alone made
// such a node discard its control database — and therefore its auxiliary
// mark — on EVERY boot, so it could never have a warm auxiliary boot at all.
// With no catalog bytes to replay, a from-zero catalog replay applies nothing,
// and "from zero must mean from empty" is vacuous.
func controlDatabaseMayBeKept(catalogResume int64, warm bool, journal *recordCatalogJournal) bool {
	if catalogResume > 0 {
		return true
	}
	return warm && journal.validLength() == 0
}

// appendCatalogEvent / appendCatalogEvents are the ONLY way live code may add
// record-catalog frames.
//
// THEY EXIST TO MAKE AN INVARIANT STRUCTURAL INSTEAD OF POLITE. A resume mark
// may only ever cover frames whose control rows are already committed. Every
// live writer does commit first (storeRecordBatch's `tx.Commit()` precedes its
// append), but "every caller remembers to" is not a guarantee — and a caller
// that journals WITHOUT applying would produce a mark that silently skips real
// records on the next boot, which is the single worst outcome this whole lane
// can produce.
//
// So the store advances its applied high-water mark HERE, at the one place that
// is definitionally reached only after an apply. Anything that appends to the
// journal by another route (tests fabricating a journal, a future path that
// journals ahead of applying) simply does not move it, and the next boot
// replays those frames — slower, and correct.
func (s *FlatSQLStore) appendCatalogEvent(event recordCatalogEvent) error {
	return s.appendCatalogEvents([]recordCatalogEvent{event})
}

func (s *FlatSQLStore) appendCatalogEvents(events []recordCatalogEvent) error {
	if err := s.recordCatalog.AppendAll(events); err != nil {
		return err
	}
	s.noteCatalogApplied()
	return nil
}

// noteCatalogApplied records that the journal is now applied through its current
// end. Monotonic: a shorter observation never lowers the mark.
func (s *FlatSQLStore) noteCatalogApplied() {
	if s == nil || s.recordCatalog == nil {
		return
	}
	end := s.recordCatalog.validLength()
	for {
		cur := s.appliedOffset.Load()
		if end <= cur || s.appliedOffset.CompareAndSwap(cur, end) {
			return
		}
	}
}

// noteCatalogAppliedThrough records an explicit applied offset — used by the
// replay, which knows exactly how far it got.
func (s *FlatSQLStore) noteCatalogAppliedThrough(off int64) {
	for {
		cur := s.appliedOffset.Load()
		if off <= cur || s.appliedOffset.CompareAndSwap(cur, off) {
			return
		}
	}
}

// noteAuxiliaryApplied is the auxiliary journal's equivalent — and it is NOT a
// copy of the record-catalog one, because the auxiliary lane has an EXCEPTION
// that the catalog lane does not.
//
// ── THE ORDERING THAT MAKES SAMPLING THE FILE LENGTH LEGAL ────────────────
//
// Eight of the nine auxiliary writers APPLY, then APPEND, both under s.mu
// (UpsertDirectoryRecord flatsql.go, SaveLocalEPM flatsql.go, pin_ledger.go,
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
// re-applies it. A crash between the append and the commit is covered for free
// — a dead process advances nothing.
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
// basePath and opens the control database, returning the journal offset the
// caller's boot replay must start from. Zero means "replay everything", and on
// that path the returned database is guaranteed EMPTY.
//
// decideResume is handed the persisted mark and answers where the replay may
// resume; it closes over the already-open journal (resumeOffset).
//
// ── FROM-ZERO MUST MEAN FROM-EMPTY ────────────────────────────────────────
//
// This is the subtlety that makes the whole cold path correct, and getting it
// wrong is silent. A replay is NOT a rebuild: producer-table rows are applied
// with INSERT OR IGNORE (record_catalog_replay.go — "the FIRST frame for a CID
// wins"), so replaying from zero over a table that already has rows CANNOT
// correct them. Two real cases depend on it:
//
//   - COMPACTION rewrites the journal AND remaps stream offsets. If a crash
//     interrupts it, roll-forward fixes the FILES and the from-zero replay of
//     the rewritten journal is what re-derives the offsets — but only if the
//     rows it is deriving into are absent.
//   - Rows a live session wrote but never journaled (repeat-CID mirror
//     attribution, producer_standard_tables.go) must not outlive the store that
//     wrote them, or a reopen shows a CID under two producers.
//
// While the database was `:memory:` every open started empty and this was free.
// Now it has to be paid for explicitly: whenever the mark is unusable, the
// control database is DISCARDED and reopened, so "cold boot" is byte-for-byte
// the behaviour this store had before the lane existed.
//
// BOTH journals are decided in the one callback, and the DISCARD below is why
// that matters: when the control database is thrown away, every table it held
// goes with it — including the auxiliary ones. The marks live INSIDE the
// database they describe, so a discard drops both together and the next open
// reads neither. There is no way to keep an auxiliary mark that outlives the
// rows it claims are already applied.
func openControlEngine(basePath, dbPath string, readOnly bool, decideResume func(bootMark, bool) bootResume) (*flatsqlrt.Runtime, *flatsqlrt.Database, bootResume, engineBootPlan, error) {
	// A READ-ONLY open takes no store lock, so it must never become a second
	// writer against a file the live daemon owns. It therefore keeps the
	// ephemeral engine and re-derives, exactly as it always has. This is not a
	// limitation to fix later: opening the writer's database read-write from a
	// second process is precisely the corruption the one-daemon-per-box law
	// exists to prevent.
	if readOnly {
		// A read-only open builds an EPHEMERAL control database with no
		// pre-existing tables, so no standard can collide with one and there
		// is no persisted source to restore.
		engine, db, err := openEphemeralControlEngine(engineDatabaseSchema)
		return engine, db, bootResume{}, engineBootPlan{Excluded: map[string]bool{}}, err
	}

	// PRE-FLIGHT: never hand the engine a file that is not a database.
	//
	// MEASURED, and it is why this check exists rather than being paranoia:
	// opening a garbage file with flatsql_open_db TRAPS the guest —
	// `trap in "flatsql_open_db": unreachable`. The embedded engine is the
	// -fignore-exceptions build, where a C++ throw lowers to `unreachable` and
	// POISONS THE WHOLE RUNTIME (README, commit b26ed45), so a single corrupt
	// byte range would otherwise cost the daemon its storage engine at boot.
	// Filed for the engine owner (errors must be values on this entry point
	// too); until then the host refuses to present the input that triggers it,
	// and the retry loop below survives it anyway.
	if err := discardNonDatabaseFile(dbPath); err != nil {
		return nil, nil, bootResume{}, engineBootPlan{}, err
	}

	// Up to three attempts, each with a FRESH RUNTIME. A poisoned runtime cannot
	// be reused for anything — including the recovery open — so a retry has to
	// replace the engine, not just the file.
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		hadFile := fileExists(dbPath)

		// PROBE FIRST, in its own runtime, then open for real. Both the
		// exclusion set (which decides the schema text) and the persisted
		// source list (which must be registered before the real database's
		// first query) are answers about the file's existing contents, and
		// asking the real database would BE that first query.
		plan := probeControlDatabase(basePath, dbPath)

		engine, err := flatsqlrt.New(
			flatsqlrt.WithPrecompiledAOTCache(engineAOTCacheDir()),
			flatsqlrt.WithFileIORoot(basePath),
		)
		if err != nil {
			return nil, nil, bootResume{}, engineBootPlan{}, fmt.Errorf("failed to start FlatSQL engine: %w", err)
		}

		engineDB, mark, warm, err := tryOpenControlDatabase(engine, dbPath,
			engineSchemaTextExcluding(plan.Excluded), enginePrepare(plan))
		if err == nil {
			resume := bootResume{}
			if decideResume != nil {
				resume = decideResume(mark, warm)
			}
			// THE RECORD CATALOG DECIDES WHETHER THE DATABASE SURVIVES. A cold
			// catalog replay must land in empty tables (above), and emptying them
			// necessarily discards the auxiliary rows too — so an auxiliary mark
			// can never be honoured across a discard, and is dropped with it.
			if resume.Keep || !hadFile {
				// Warm, or a genuine first boot whose database is already empty.
				return engine, engineDB, resume, plan, nil
			}
			// Cold with pre-existing state: discard and reopen so the replay
			// lands in empty tables. One extra engine start, on the path that
			// was already the slow one.
			log.Warnf("FlatSQL boot: no usable resume mark — discarding the control database so the full replay rebuilds from empty tables")
			engineDB.Destroy()
			engine.Close()
			if rmErr := removeControlDatabaseFiles(dbPath); rmErr != nil {
				return nil, nil, bootResume{}, engineBootPlan{}, fmt.Errorf("discard stale control database: %w", rmErr)
			}
			continue
		}

		lastErr = err
		engine.Close() // discards a poisoned runtime AND releases its file handles
		if errors.Is(err, errEnginePrepareFailed) {
			// The FILE is fine; the host-side registration is not. Fail the
			// start and leave the control database untouched — exactly the
			// non-destructive failure this was before the registration moved
			// in front of the first query.
			return nil, nil, bootResume{}, engineBootPlan{}, fmt.Errorf("failed to register engine file identifiers: %w", err)
		}
		if attempt == 2 {
			break
		}
		log.Warnf("FlatSQL control database at %s is unusable (%v) — discarding it and re-deriving from the journal", dbPath, err)
		if rmErr := removeControlDatabaseFiles(dbPath); rmErr != nil {
			return nil, nil, bootResume{}, engineBootPlan{}, fmt.Errorf("discard unusable control database: %w", rmErr)
		}
	}

	// The store must still open. Falling all the way back to the ephemeral
	// engine keeps the node ALIVE on exactly the pre-durability terms it ran on
	// for the last year — a slow boot, never a dead one.
	log.Errorf("FlatSQL control database could not be opened even after discarding it (%v) — falling back to an EPHEMERAL control database; every boot will re-derive the whole catalog until this is resolved", lastErr)
	engine, db, err := openEphemeralControlEngine(engineDatabaseSchema)
	return engine, db, bootResume{}, engineBootPlan{Excluded: map[string]bool{}}, err
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
// materialize 226 base plus 226 x 7 shadow tables in ONE un-batched burst.
//
// PLUS THE DECORATIONS THE ENGINE DERIVES BY ITSELF, which the earlier
// arithmetic left out. The engine builds an R-Tree for any table whose column
// names read geospatial, and the schema-exact catalog trips that for ELEVEN
// standards besides the intended $TBS — CRM, ENV, GNO, ION, OBT, SEN, SEO,
// SIT, SWR, TMS, TRK, every one of them a genuinely geospatial standard
// (LAT/LON/ALT columns straight out of its IDL). MEASURED on the shipped
// engine: 12 `_rtree_*` virtual tables, each backed by three plain tables, so
// 48 extra schema objects created inside the same burst, plus per-ingest index
// maintenance for those eleven standards from then on. It is disclosed here
// and PINNED by TestEngineDerivedRTreesAreTheDisclosedSet, so the next catalog
// change that trips a twelfth is a test failure and not a surprise on a
// droplet.
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

// sqliteFileHeader is the 16-byte magic every SQLite database starts with.
const sqliteFileHeader = "SQLite format 3\x00"

// discardNonDatabaseFile removes dbPath (and its derived files) when it is
// present but is plainly not a SQLite database. Cheap, host-side, and it keeps
// the common corruption case from costing an engine restart.
func discardNonDatabaseFile(dbPath string) error {
	f, err := os.Open(dbPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // a missing database is a first boot, not a problem
		}
		return fmt.Errorf("inspect control database: %w", err)
	}
	hdr := make([]byte, len(sqliteFileHeader))
	n, readErr := io.ReadFull(f, hdr)
	f.Close()
	// A zero-length file is what SQLite itself creates before the first write,
	// and it opens cleanly. Anything else that does not start with the magic is
	// not a database and must never reach the engine.
	if n == 0 && (readErr == io.EOF || readErr == nil) {
		return nil
	}
	if readErr == nil && string(hdr) == sqliteFileHeader {
		return nil
	}
	log.Warnf("FlatSQL control database at %s is not a SQLite database — discarding it and re-deriving from the journal", dbPath)
	return removeControlDatabaseFiles(dbPath)
}

// openEphemeralControlEngine is the pre-durability shape: an engine with no
// filesystem and an in-memory control database. Read-only opens and any host
// that cannot reach a filesystem land here, and everything downstream behaves
// exactly as it did before this lane existed.
func openEphemeralControlEngine(schemaText string) (*flatsqlrt.Runtime, *flatsqlrt.Database, error) {
	engine, err := flatsqlrt.New(flatsqlrt.WithPrecompiledAOTCache(engineAOTCacheDir()))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to start FlatSQL engine: %w", err)
	}
	db, err := engine.CreateDatabase(schemaText, "sdn-control")
	if err != nil {
		engine.Close()
		return nil, nil, fmt.Errorf("failed to create FlatSQL database: %w", err)
	}
	return engine, db, nil
}

// openControlDatabase opens dbPath disk-backed, verifies it, and reports the
// persisted mark. One wipe-and-retry is attempted: a database that cannot be
// opened or verified is worth exactly nothing, because everything it holds is
// derivable from the journal and the stream files.
func openControlDatabase(engine *flatsqlrt.Runtime, dbPath, schemaText string, prepare func(*flatsqlrt.Database) error) (*flatsqlrt.Database, bootMark, bool, error) {
	db, mark, warm, err := tryOpenControlDatabase(engine, dbPath, schemaText, prepare)
	if err == nil {
		return db, mark, warm, nil
	}
	if errors.Is(err, errEnginePrepareFailed) {
		// A registration failure says nothing about the file. Never trade a
		// multi-GB control database for it.
		return nil, bootMark{}, false, err
	}
	log.Warnf("FlatSQL control database at %s is unusable (%v) — discarding it and re-deriving from the journal", dbPath, err)
	if rmErr := removeControlDatabaseFiles(dbPath); rmErr != nil {
		return nil, bootMark{}, false, fmt.Errorf("discard unusable control database: %w", rmErr)
	}
	db, mark, warm, err = tryOpenControlDatabase(engine, dbPath, schemaText, prepare)
	if err != nil {
		return nil, bootMark{}, false, fmt.Errorf("open control database after discard: %w", err)
	}
	return db, mark, warm, nil
}

func tryOpenControlDatabase(engine *flatsqlrt.Runtime, dbPath, schemaText string, prepare func(*flatsqlrt.Database) error) (*flatsqlrt.Database, bootMark, bool, error) {
	db, err := engine.OpenDatabase(schemaText, "sdn-control", dbPath, flatsqlrt.JournalTruncate)
	if err != nil {
		return nil, bootMark{}, false, err
	}
	// BEFORE THE FIRST QUERY. IsDiskBacked/ReindexAll/verifyControlDatabase all
	// query, and the engine's virtual-table registration is a one-shot at the
	// first query — see enginePrepare.
	if prepare != nil {
		if err := prepare(db); err != nil {
			db.Destroy()
			return nil, bootMark{}, false, err
		}
	}
	disk, err := db.IsDiskBacked()
	if err != nil {
		db.Destroy()
		return nil, bootMark{}, false, err
	}
	if !disk {
		// The engine reported RAM for a real path. Never treat that as durable
		// — silently succeeding against memory is the exact defect this lane
		// exists to remove.
		db.Destroy()
		return nil, bootMark{}, false, errors.New("engine opened a real path but reports NOT disk-backed")
	}

	// The FlatBuffer record arena is NOT persisted in this slice: the store's
	// hot window is a cache that RebuildDerivedState re-derives, and its Go-side
	// bookkeeping (engineSources / engineResident / engineEpoch) does not
	// survive a restart either. Index rows left on disk by the previous process
	// would therefore point into an arena that no longer exists. ReindexAll
	// re-derives the index from the (empty) stream, which makes index and arena
	// consistent by construction. This is deliberate and is why the store never
	// calls FlushIndex — nothing is ever written to <db>.fsdata, so it never
	// grows.
	if _, err := db.ReindexAll(); err != nil {
		db.Destroy()
		return nil, bootMark{}, false, fmt.Errorf("reset engine record index: %w", err)
	}

	if err := verifyControlDatabase(db); err != nil {
		db.Destroy()
		return nil, bootMark{}, false, err
	}

	mark, warm := readBootMark(db)
	return db, mark, warm, nil
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

// readBootMark reads the persisted resume point. A missing table, a missing
// row, a bad number or a format bump all mean "no mark" — never an error,
// because "no mark" is simply a cold boot.
// The AUXILIARY pair is optional: it may be absent (a database written before
// this lane existed, or one whose auxiliary replay has not checkpointed yet)
// without costing the record catalog its warm resume. Absent reads as zero,
// which means "replay the auxiliary journal from the beginning".
func readBootMark(db *flatsqlrt.Database) (bootMark, bool) {
	res, err := db.Query(
		`SELECT key, value FROM sdn_metadata WHERE key IN (?, ?, ?, ?, ?)`,
		bootMarkFormatKey, bootMarkOffsetKey, bootMarkDigestKey,
		bootMarkAuxOffsetKey, bootMarkAuxDigestKey)
	if err != nil || res == nil {
		return bootMark{}, false
	}
	var format, offset, digest, auxOffset, auxDigest string
	for _, row := range res.Rows {
		if len(row) < 2 {
			continue
		}
		key := fmt.Sprint(row[0])
		val := fmt.Sprint(row[1])
		switch key {
		case bootMarkFormatKey:
			format = val
		case bootMarkOffsetKey:
			offset = val
		case bootMarkDigestKey:
			digest = val
		case bootMarkAuxOffsetKey:
			auxOffset = val
		case bootMarkAuxDigestKey:
			auxDigest = val
		}
	}
	// `warm` means "this database carries a mark in a format we understand" —
	// NOT "the record catalog is resuming". Each journal's pair is then judged
	// on its own (resumeOffset / auxiliaryResumeOffset), because a store can
	// legitimately have one and not the other: a node with no records at all
	// still checkpoints an auxiliary mark, and gating that behind a
	// record-catalog mark it will never have would cost it a full auxiliary
	// replay on every boot forever.
	if format != bootMarkFormat {
		return bootMark{}, false
	}
	if offset == "" && auxOffset == "" {
		return bootMark{}, false
	}
	mark := bootMark{}
	if offset != "" && digest != "" {
		n, err := strconv.ParseInt(offset, 10, 64)
		if err != nil || n < 0 {
			return bootMark{}, false
		}
		mark.Offset = n
		mark.Digest = digest
	}
	if auxOffset != "" && auxDigest != "" {
		if aux, err := strconv.ParseInt(auxOffset, 10, 64); err == nil && aux >= 0 {
			mark.AuxOffset = aux
			mark.AuxDigest = auxDigest
		}
	}
	return mark, true
}

// removeControlDatabaseFiles deletes the control database and everything the
// engine derives from it. Nothing here is a source of truth: the journal and
// the stream files are.
func removeControlDatabaseFiles(dbPath string) error {
	for _, p := range []string{dbPath, dbPath + "-journal", dbPath + ".fsdata"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", p, err)
		}
	}
	return nil
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
	legacyBlob := []string{}
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
		case plain[legacySchemaTableName(name)]:
			// THE SECOND PER-STORE EXCLUSION, and it is a REACHABILITY guard
			// rather than a destruction one. migrateLegacySchemaTable merges a
			// pre-stream `sds_<lower>` BLOB table into the canonical table, and
			// it DEFERS whenever the engine owns that canonical name. Routing a
			// standard whose store still holds its blob table would therefore
			// pin those rows outside every production read path — Get and
			// recordReadSource both union the canonical table and the
			// (producer, standard) tables, neither of which the blob rows are
			// in — with nothing to un-pin them.
			//
			// So the standard stays UNROUTED until the blob migration runs.
			// That migration merges the rows into the plain canonical table,
			// and the NEXT boot keeps the standard unrouted for the collision
			// reason above, with its rows readable the ordinary way.
			plan.Excluded[name] = true
			legacyBlob = append(legacyBlob, name)
		}
	}
	sort.Strings(collided)
	sort.Strings(legacyBlob)
	if len(collided) > 0 {
		log.Errorf("FlatSQL boot: %v are NOT engine-routed in this store — a plain control table already holds each standard's canonical name and routing it would DROP that table. Their rows stay readable through the ordinary record read source.", collided)
	}
	if len(legacyBlob) > 0 {
		log.Errorf("FlatSQL boot: %v are NOT engine-routed in this store — a pre-stream sds_<lower> BLOB table still holds their rows, and routing the canonical name would stop the legacy migration from ever merging them into the record read path. They route on the boot after that migration runs.", legacyBlob)
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

// resumeOffset decides where the boot replay starts.
//
// It returns 0 — a full replay — for every doubt. The only way to get a warm
// resume is: a mark was persisted, it names THIS journal (digest match), and it
// does not point past the journal's CRC-valid length.
func resumeOffset(mark bootMark, warm bool, journal *recordCatalogJournal) int64 {
	if !warm || journal == nil || journal.f == nil {
		return 0
	}
	if mark.Offset <= 0 {
		return 0
	}
	valid := journal.validLength()
	if mark.Offset > valid {
		// The journal is SHORTER than the mark: it was compacted, replaced, or
		// truncated at a torn tail. Offsets from the old file mean nothing.
		log.Warnf("FlatSQL boot: persisted mark %d is past the journal's valid length %d — replaying from the beginning",
			mark.Offset, valid)
		return 0
	}
	digest, err := journal.digestPrefix(mark.Offset)
	if err != nil {
		log.Warnf("FlatSQL boot: could not verify the journal prefix (%v) — replaying from the beginning", err)
		return 0
	}
	if digest != mark.Digest {
		log.Warnf("FlatSQL boot: journal prefix digest changed (compaction or replacement) — replaying from the beginning")
		return 0
	}
	return mark.Offset
}

// auxiliaryResumeOffset is resumeOffset for the OTHER journal, and it answers
// the same way: 0 for every doubt.
//
// ── WHY A FROM-ZERO AUXILIARY REPLAY IS SAFE OVER POPULATED TABLES ────────
//
// The record catalog needs "from zero means from empty" (openControlEngine)
// because its appliers are INSERT OR IGNORE — a from-zero replay cannot correct
// a row that is already there. The auxiliary appliers are not like that, and
// this lane depends on the difference, so it is stated rather than assumed:
//
//   - every auxiliary upsert is ON CONFLICT DO UPDATE (sdn_directory,
//     sdn_local_epms, sdn_pin_ledger, sdn_dataset_shard_publications,
//     sdn_dataset_publication_replay_state, sdn_source_batch_license), so
//     re-applying converges to the same row rather than losing to the first
//     writer;
//   - the asset-pin frames short-circuit on their own stable event ID
//     (assetPinAuditEventAlreadyApplied) or on the receipt digest, and a
//     conflicting re-apply is an ERROR, not a silent overwrite;
//   - the one destructive frame (dataset shard publication delete) is replayed
//     IN JOURNAL ORDER against the same prefix that produced the rows, so its
//     effect is idempotent.
//
// That is why a warm catalog may sit next to a cold auxiliary journal: the
// worst outcome is the work this task exists to avoid, never a wrong row.
func auxiliaryResumeOffset(mark bootMark, warm bool, aux *auxiliaryMetadataStore) int64 {
	if !warm || aux == nil || aux.f == nil {
		return 0
	}
	if mark.AuxOffset <= 0 {
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

// digestPrefix fingerprints the journal's frame headers over [0, limit).
//
// Headers only: each 8-byte header carries the frame length and the CRC32 of
// that frame's payload, so a digest over the headers transitively covers every
// payload byte in the prefix without reading any of them. That keeps the cost
// O(frames) in cheap 8-byte reads instead of O(bytes).
//
// It is also INCREMENTAL. A periodic checkpoint must never re-fingerprint the
// whole catalog: the journal lock is the same one live appends take, so an
// O(catalog) walk here would become a new writer stall every interval — and
// reader starvation behind long lock holds is an ALREADY MEASURED production
// symptom (task sdn-flatsql-sync-discovery-latency-resets: anonymous
// list_published_shards reads at 22–42 s). The running state covers [0, off);
// a checkpoint extends it by the handful of frames written since the last one.
func (j *recordCatalogJournal) digestPrefix(limit int64) (string, error) {
	if j == nil || j.f == nil {
		return "", errors.New("record catalog journal is not open")
	}
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.digest == nil || limit < j.digestOffset {
		// First call, or a limit BEHIND what we have folded in (a compacted or
		// replaced journal). Restart the running state rather than trying to
		// rewind a hash.
		j.digest = newRecordCatalogDigest()
		j.digestOffset = 0
	}
	if err := extendRecordCatalogDigest(j.digest, j.f, &j.digestOffset, limit); err != nil {
		// Leave the running state consistent with what it actually covers so a
		// later call can still extend from there.
		return "", err
	}
	return sealRecordCatalogDigest(j.digest, limit)
}

// validLength reports the journal's CRC-valid length as established when it was
// opened (a torn tail is truncated away for writers, and bounded for readers).
func (j *recordCatalogJournal) validLength() int64 {
	if j == nil || j.f == nil {
		return 0
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.readOnly {
		return j.replayLimit
	}
	info, err := j.f.Stat()
	if err != nil {
		return 0
	}
	return info.Size()
}

// CheckpointRecordCatalog advances the persisted resume mark to the journal's
// current end, so the NEXT boot replays only what arrives after this moment.
//
// ── WHY THE EXPENSIVE HALF IS OUTSIDE THE STORE LOCK ──────────────────────
//
// Measuring the journal end and fingerprinting its prefix happen BEFORE
// s.mu.Lock(); only three tiny sdn_metadata upserts happen inside it. Reader
// starvation behind long store-lock holds is a measured production symptom, not
// a theoretical one (task sdn-flatsql-sync-discovery-latency-resets: anonymous
// list_published_shards answers at 22–42 s while writers hold the store), and a
// periodic maintenance task that took the write lock for an O(catalog) walk
// would be a new instance of exactly that class.
//
// Observing `end` outside the lock stays SOUND, and the argument is worth
// stating because it is not obvious: any writer that had already appended
// frames below `end` when we sampled it still held s.mu at that moment, so it
// necessarily RELEASES that lock before we acquire it — and it commits its
// control rows inside its own hold. By the time we write the mark, every frame
// the mark covers is backed by committed rows, regardless of the order an
// individual writer uses internally. A writer that appends AFTER our sample is
// simply not covered, and under-covering is always safe: it costs replay, never
// correctness.
//
// It advances BOTH marks. They are gated separately and on purpose: the
// record-catalog mark waits for hydration, which on the production daemon
// happens in the BACKGROUND minutes after boot, while the auxiliary mark is
// ready the moment the synchronous auxiliary replay finishes at open. Gating
// them together would have meant a deferred-hydration daemon — i.e. every real
// node — never persisting an auxiliary mark until its first clean shutdown, and
// this whole fix would have been dead on the boxes it was written for.
func (s *FlatSQLStore) CheckpointRecordCatalog() error {
	return errors.Join(s.checkpointCatalogMark(), s.checkpointAuxiliaryMark())
}

// checkpointAuxiliaryMark advances the auxiliary resume mark to the offset this
// store has demonstrably applied. Same shape as the catalog half: the digest is
// computed OUTSIDE the store lock, only the upserts are inside it.
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
// or ok=false when none may be. Shared by the locked and unlocked checkpoint
// paths so the eligibility rules exist once.
func (s *FlatSQLStore) auxiliaryMarkCandidate() (int64, string, bool, error) {
	if s.readOnly || !s.controlDBDurable || s.auxiliaryMetadata == nil || s.auxiliaryMetadata.f == nil {
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

func (s *FlatSQLStore) checkpointCatalogMark() error {
	if s.readOnly || !s.controlDBDurable || s.recordCatalog == nil || s.recordCatalog.f == nil {
		return nil
	}
	// A mark is only meaningful once the control tables actually describe the
	// whole journal prefix. While hydration is in flight they do not.
	if !s.recordCatalogHydrated.Load() {
		return nil
	}
	// THE MARK FOLLOWS THE APPLIED OFFSET, NOT THE FILE LENGTH. Journal bytes
	// past what this store has actually applied are exactly the bytes the next
	// boot must replay.
	end := s.appliedOffset.Load()
	if valid := s.recordCatalog.validLength(); end > valid {
		end = valid
	}
	if end <= 0 {
		return nil
	}
	digest, err := s.recordCatalog.digestPrefix(end)
	if err != nil {
		return fmt.Errorf("checkpoint record catalog: digest: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistBootMarkLocked(end, digest)
}

// checkpointRecordCatalogLocked is the same operation for a caller that ALREADY
// holds the store write lock (Close). It is the only path that pays for the
// digest under the lock, and it runs exactly once per process. It too advances
// both marks — a clean shutdown is precisely when the auxiliary mark is most
// worth having.
func (s *FlatSQLStore) checkpointRecordCatalogLocked() error {
	auxErr := s.checkpointAuxiliaryMarkLocked()
	return errors.Join(s.checkpointCatalogMarkLocked(), auxErr)
}

func (s *FlatSQLStore) checkpointAuxiliaryMarkLocked() error {
	end, digest, ok, err := s.auxiliaryMarkCandidate()
	if err != nil || !ok {
		return err
	}
	return s.persistAuxiliaryMarkLocked(end, digest)
}

func (s *FlatSQLStore) checkpointCatalogMarkLocked() error {
	if s.readOnly || !s.controlDBDurable || s.recordCatalog == nil || s.recordCatalog.f == nil {
		return nil
	}
	if !s.recordCatalogHydrated.Load() {
		return nil
	}
	// THE MARK FOLLOWS THE APPLIED OFFSET, NOT THE FILE LENGTH. Journal bytes
	// past what this store has actually applied are exactly the bytes the next
	// boot must replay.
	end := s.appliedOffset.Load()
	if valid := s.recordCatalog.validLength(); end > valid {
		end = valid
	}
	if end <= 0 {
		return nil
	}
	digest, err := s.recordCatalog.digestPrefix(end)
	if err != nil {
		return fmt.Errorf("checkpoint record catalog: digest: %w", err)
	}
	return s.persistBootMarkLocked(end, digest)
}

// persistBootMarkLocked writes the three record-catalog handshake rows.
// Requires the store write lock. Deliberately three single-row upserts and
// nothing else: this is the entire footprint of a checkpoint inside the lock.
func (s *FlatSQLStore) persistBootMarkLocked(end int64, digest string) error {
	if err := s.upsertBootMarkRowsLocked("record catalog", [][2]string{
		{bootMarkFormatKey, bootMarkFormat},
		{bootMarkOffsetKey, strconv.FormatInt(end, 10)},
		{bootMarkDigestKey, digest},
	}); err != nil {
		return err
	}
	s.checkpointedOffset.Store(end)
	return nil
}

// persistAuxiliaryMarkLocked writes the auxiliary handshake rows. Same shape,
// same lock, three more small upserts.
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

// runCheckpointLoop advances the mark periodically so a CRASH costs at most one
// interval of replay. A clean shutdown checkpoints unconditionally in Close.
//
// It closes checkpointDone on the way out, and Close WAITS on that before it
// tears anything down. That handshake is not decoration: without it the loop can
// already be inside CheckpointRecordCatalog, blocked on s.mu, when Close takes
// the lock and nils s.db — and the loop then dereferences a nil *sql.DB the
// instant Close releases. Measured as a SIGSEGV in the node package's quota-GC
// test, which closes a store while the daemon is still running.
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
				if err := s.CheckpointRecordCatalog(); err != nil {
					log.Warnf("FlatSQL boot-state checkpoint failed: %v", err)
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

// checkpointDirty reports whether enough journal has accumulated past the mark
// to be worth a checkpoint. Reading the file size is far cheaper than taking
// the store write lock, so the cheap question is asked first.
// Either journal being dirty is enough; the checkpoint itself re-checks each
// half's own eligibility.
func (s *FlatSQLStore) checkpointDirty() bool {
	if s.recordCatalog != nil && s.recordCatalog.f != nil &&
		s.appliedOffset.Load()-s.checkpointedOffset.Load() >= checkpointDirtyBytes {
		return true
	}
	if s.auxiliaryMetadata != nil && s.auxiliaryMetadata.f != nil &&
		s.auxAppliedOffset.Load()-s.auxCheckpointedOffset.Load() >= auxiliaryCheckpointDirtyBytes {
		return true
	}
	return false
}

// newRecordCatalogDigest starts a running frame-header digest. The frame
// version is mixed in first so a format change can never produce a colliding
// fingerprint.
func newRecordCatalogDigest() hash.Hash {
	h := sha256.New()
	fmt.Fprintf(h, "record-catalog-v%d\n", recordCatalogFrameVersion)
	return h
}

// newAuxiliaryMetadataDigest is the same running state for the auxiliary
// journal, with its OWN domain prefix. The two files share the 8-byte
// {length, payload CRC32} frame header, so the walker below is shared; the
// prefix is what makes a catalog digest and an auxiliary digest of the same
// bytes different values, so neither journal's mark can ever be honoured
// against the other's file.
func newAuxiliaryMetadataDigest() hash.Hash {
	h := sha256.New()
	fmt.Fprintf(h, "auxiliary-metadata-v%d\n", auxiliaryMetadataFrameVersion)
	return h
}

// extendRecordCatalogDigest folds the frame headers in [*off, limit) into h and
// advances *off. Caller holds the journal lock.
func extendRecordCatalogDigest(h hash.Hash, f *os.File, off *int64, limit int64) error {
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

// sealRecordCatalogDigest snapshots the running state and binds it to a
// specific length. It CLONES rather than finalising, so the running state stays
// usable for the next checkpoint — a Sum() on a live hash.Hash does not
// consume it, but mixing the limit in would, so the clone is mandatory.
func sealRecordCatalogDigest(h hash.Hash, limit int64) (string, error) {
	m, ok := h.(encoding.BinaryMarshaler)
	if !ok {
		return "", errors.New("record catalog digest is not cloneable")
	}
	state, err := m.MarshalBinary()
	if err != nil {
		return "", err
	}
	clone := sha256.New()
	u, ok := clone.(encoding.BinaryUnmarshaler)
	if !ok {
		return "", errors.New("record catalog digest clone is not restorable")
	}
	if err := u.UnmarshalBinary(state); err != nil {
		return "", err
	}
	fmt.Fprintf(clone, ":%d", limit)
	return hex.EncodeToString(clone.Sum(nil)), nil
}

// digestRecordCatalogPrefix fingerprints a journal file from scratch, for tests
// and for any caller that has no running state.
func digestRecordCatalogPrefix(f *os.File, limit int64) (string, error) {
	h := newRecordCatalogDigest()
	off := int64(0)
	if err := extendRecordCatalogDigest(h, f, &off, limit); err != nil {
		return "", err
	}
	return sealRecordCatalogDigest(h, limit)
}
