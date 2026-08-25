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
func openControlEngine(basePath, dbPath string, readOnly bool, decideResume func(bootMark, bool) bootResume) (*flatsqlrt.Runtime, *flatsqlrt.Database, bootResume, error) {
	// A READ-ONLY open takes no store lock, so it must never become a second
	// writer against a file the live daemon owns. It therefore keeps the
	// ephemeral engine and re-derives, exactly as it always has. This is not a
	// limitation to fix later: opening the writer's database read-write from a
	// second process is precisely the corruption the one-daemon-per-box law
	// exists to prevent.
	if readOnly {
		engine, db, err := openEphemeralControlEngine()
		return engine, db, bootResume{}, err
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
		return nil, nil, bootResume{}, err
	}

	// Up to three attempts, each with a FRESH RUNTIME. A poisoned runtime cannot
	// be reused for anything — including the recovery open — so a retry has to
	// replace the engine, not just the file.
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		hadFile := fileExists(dbPath)

		engine, err := flatsqlrt.New(
			flatsqlrt.WithPrecompiledAOTCache(engineAOTCacheDir()),
			flatsqlrt.WithFileIORoot(basePath),
		)
		if err != nil {
			return nil, nil, bootResume{}, fmt.Errorf("failed to start FlatSQL engine: %w", err)
		}

		engineDB, mark, warm, err := tryOpenControlDatabase(engine, dbPath)
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
				return engine, engineDB, resume, nil
			}
			// Cold with pre-existing state: discard and reopen so the replay
			// lands in empty tables. One extra engine start, on the path that
			// was already the slow one.
			log.Warnf("FlatSQL boot: no usable resume mark — discarding the control database so the full replay rebuilds from empty tables")
			engineDB.Destroy()
			engine.Close()
			if rmErr := removeControlDatabaseFiles(dbPath); rmErr != nil {
				return nil, nil, bootResume{}, fmt.Errorf("discard stale control database: %w", rmErr)
			}
			continue
		}

		lastErr = err
		engine.Close() // discards a poisoned runtime AND releases its file handles
		if attempt == 2 {
			break
		}
		log.Warnf("FlatSQL control database at %s is unusable (%v) — discarding it and re-deriving from the journal", dbPath, err)
		if rmErr := removeControlDatabaseFiles(dbPath); rmErr != nil {
			return nil, nil, bootResume{}, fmt.Errorf("discard unusable control database: %w", rmErr)
		}
	}

	// The store must still open. Falling all the way back to the ephemeral
	// engine keeps the node ALIVE on exactly the pre-durability terms it ran on
	// for the last year — a slow boot, never a dead one.
	log.Errorf("FlatSQL control database could not be opened even after discarding it (%v) — falling back to an EPHEMERAL control database; every boot will re-derive the whole catalog until this is resolved", lastErr)
	engine, db, err := openEphemeralControlEngine()
	return engine, db, bootResume{}, err
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
func openEphemeralControlEngine() (*flatsqlrt.Runtime, *flatsqlrt.Database, error) {
	engine, err := flatsqlrt.New(flatsqlrt.WithPrecompiledAOTCache(engineAOTCacheDir()))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to start FlatSQL engine: %w", err)
	}
	db, err := engine.CreateDatabase(engineDatabaseSchema, "sdn-control")
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
func openControlDatabase(engine *flatsqlrt.Runtime, dbPath string) (*flatsqlrt.Database, bootMark, bool, error) {
	db, mark, warm, err := tryOpenControlDatabase(engine, dbPath)
	if err == nil {
		return db, mark, warm, nil
	}
	log.Warnf("FlatSQL control database at %s is unusable (%v) — discarding it and re-deriving from the journal", dbPath, err)
	if rmErr := removeControlDatabaseFiles(dbPath); rmErr != nil {
		return nil, bootMark{}, false, fmt.Errorf("discard unusable control database: %w", rmErr)
	}
	db, mark, warm, err = tryOpenControlDatabase(engine, dbPath)
	if err != nil {
		return nil, bootMark{}, false, fmt.Errorf("open control database after discard: %w", err)
	}
	return db, mark, warm, nil
}

func tryOpenControlDatabase(engine *flatsqlrt.Runtime, dbPath string) (*flatsqlrt.Database, bootMark, bool, error) {
	db, err := engine.OpenDatabase(engineDatabaseSchema, "sdn-control", dbPath, flatsqlrt.JournalTruncate)
	if err != nil {
		return nil, bootMark{}, false, err
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

// reregisterPersistedSources restores the RUNTIME half of every FlatSQL source
// the on-disk schema remembers.
//
// THE TRAP THIS CLOSES, and it is not obvious. A FlatSQL source is two things:
// a `sqlite3_create_module_v2` registration (process state, gone on restart)
// and a `CREATE VIRTUAL TABLE "<Base>@<source>" USING "__flatsql_module_..."`
// statement (flatsql cpp/src/sqlite_engine.cpp:414). While the database was
// `:memory:` both died together and neither was ever missed. Now the DDL
// SURVIVES and the module does not, so a persisted schema entry points at a
// module nothing registered — and the very first query to touch it fails with
// `no such module: __flatsql_module_...`, at boot, before any ingest has had a
// chance to re-register the source as a side effect.
//
// The store's own `engineSources` map starts empty on every open too, so
// without this the map and the schema disagree from the first instruction.
// Registering from the schema makes both true at once.
//
// The vtab CONTENT is not restored and must not be: it is the record arena,
// which this slice deliberately does not persist (see tryOpenControlDatabase).
// Registering an empty source is exactly the state a cold boot reaches before
// the hot-window rebuild refills it.
func (s *FlatSQLStore) reregisterPersistedSources() error {
	if !s.controlDBDurable {
		return nil
	}
	rows, err := s.db.Query(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND sql LIKE '%__flatsql_module_%'`)
	if err != nil {
		// A store with no such schema yet is the common case, not an error.
		return nil
	}
	var vtabs []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("scan persisted virtual table: %w", err)
		}
		vtabs = append(vtabs, name)
	}
	closeErr := rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("enumerate persisted virtual tables: %w", err)
	}
	if closeErr != nil {
		return closeErr
	}
	if len(vtabs) == 0 {
		return nil
	}

	// A shadow table is named "<Base>@<source>"; the engine's RegisterSource
	// takes the SOURCE and recreates the shadow table for every base table, so
	// one registration per distinct source is both necessary and sufficient.
	seen := map[string]bool{}
	for _, name := range vtabs {
		at := strings.LastIndex(name, "@")
		if at < 0 || at+1 >= len(name) {
			continue
		}
		source := name[at+1:]
		if seen[source] || s.engineSources[source] {
			continue
		}
		seen[source] = true
		if err := s.engineDB.RegisterSource(source); err != nil {
			// Never fatal. A source that cannot be re-registered is a source
			// whose queries would have failed anyway; the honest response is to
			// say so and let the caller decide, not to refuse to boot.
			log.Warnf("FlatSQL boot: could not re-register persisted source %q: %v", source, err)
			continue
		}
		s.engineSources[source] = true
	}
	if len(seen) == 0 {
		return nil
	}
	if err := s.engineDB.CreateUnifiedViews(); err != nil {
		log.Warnf("FlatSQL boot: rebuild unified views after restoring %d persisted sources: %v", len(seen), err)
	}
	log.Infof("FlatSQL boot: restored %d persisted engine source registration(s)", len(seen))
	return nil
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
