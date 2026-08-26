package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

// ==========================================================================
// THE DATA-DESTRUCTION GUARD IS FAIL-CLOSED
// ==========================================================================

// TestUnprobedControlDatabaseRoutesOnlyTheDecoratedStandards is the fix for a
// guard that was fail-OPEN. probeControlDatabase used to answer every failure
// with an EMPTY exclusion set, and an empty exclusion set is not "route
// nothing new" — it is the full 227-standard schema plus a file-id
// registration for every standard, which is exactly the input that makes
// createUnifiedView issue `DROP TABLE IF EXISTS "<CODE>"` against a plain
// control table holding that standard's rows. The probe is the ONLY guard
// (finishEngineSourceSetup, preregisterEngineSources and ensureEngineSource
// all rebuild the views with no collision test), so a probe that cannot answer
// must refuse the routed catalog.
func TestUnprobedControlDatabaseRoutesOnlyTheDecoratedStandards(t *testing.T) {
	plan := engineUnprobedPlan("test", errors.New("control database could not be inspected"))

	if len(plan.Excluded) == 0 {
		t.Fatal("a probe that could not read the control database excluded NOTHING — that is the fail-open defect")
	}
	if len(plan.Sources) != 0 || plan.ViewsCurrent {
		t.Fatal("an unprobed plan must pre-register no source and must not claim the persisted views are current")
	}
	for name := range engineRoutedSchemas {
		_, decorated := engineDecoratedSchemas[name]
		switch {
		case decorated && plan.Excluded[name]:
			t.Errorf("%s is decorated and has been engine-owned since before the flip: no store can hold a plain table of that name, so un-routing it is a different regression", name)
		case !decorated && !plan.Excluded[name]:
			t.Errorf("%s is routed generically and was never inspected — an unprobed boot must not route it", name)
		}
	}

	// THE SCHEMA IS WHAT DECIDES. A table absent from the schema the database
	// is created from can never be given a unified view, and therefore can
	// never be dropped; skipping it in Go alone would not be a guard.
	schema := engineSchemaTextExcluding(plan.Excluded)
	for name, binding := range engineRoutedSchemas {
		marker := "table " + binding.Table + " {"
		_, decorated := engineDecoratedSchemas[name]
		if decorated {
			if !strings.Contains(schema, marker) {
				t.Errorf("%s must stay in the fail-closed schema", binding.Table)
			}
			continue
		}
		if strings.Contains(schema, marker) {
			t.Errorf("%s survives in the fail-closed schema — its canonical name could still be DROPped", binding.Table)
		}
	}
}

// TestAnUnreadableControlDatabaseFailsClosedAtTheProbe drives the real entry
// point rather than the helper: a control database the probe engine cannot
// read must produce the decorated-only plan, not the empty one.
func TestAnUnreadableControlDatabaseFailsClosedAtTheProbe(t *testing.T) {
	basePath := t.TempDir()
	dbPath := filepath.Join(basePath, flatSQLControlDBName)
	// A file that passes the cheap host-side header check and is then garbage
	// — the shape behind the "database disk image is malformed" answer this
	// branch's own risk list recorded on a live control file.
	garbage := append([]byte(sqliteFileHeader), make([]byte, 4096)...)
	for i := len(sqliteFileHeader); i < len(garbage); i++ {
		garbage[i] = byte(i%251 + 1)
	}
	if err := os.WriteFile(dbPath, garbage, 0o600); err != nil {
		t.Fatalf("write unreadable control database: %v", err)
	}

	plan := probeControlDatabase(basePath, dbPath)
	if len(plan.Excluded) == 0 {
		t.Fatal("an unreadable control database produced an EMPTY exclusion set: the boot would route all 227 standards over a file it never managed to look at")
	}
	for name := range engineRoutedSchemas {
		if _, decorated := engineDecoratedSchemas[name]; decorated {
			continue
		}
		if !plan.Excluded[name] {
			t.Fatalf("%s is routed after a failed probe", name)
		}
	}
}

// TestLegacyBlobTableKeepsItsStandardUnroutedUntilItsRowsMigrate is the
// ruling's SECOND per-store exclusion.
//
// migrateLegacySchemaTable merges a pre-stream `sds_<lower>` BLOB table into
// the canonical table, and DEFERS whenever the engine owns that canonical
// name. Routing every standard therefore turned a 2-name carve-out into a
// 227-name one: a store still holding `sds_cat` would never migrate those rows
// into `CAT`, and Get / recordReadSource — which union the canonical table and
// the (producer, standard) tables and nothing else — could never reach them
// again. Nothing is destroyed; the rows simply stop existing as far as the
// node is concerned, which is why the compatibility rule is "unrouted until
// the blob migration runs".
func TestLegacyBlobTableKeepsItsStandardUnroutedUntilItsRowsMigrate(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, basePath)

	// The store must come back WARM or the control database is discarded on
	// the next boot and the legacy table goes with it.
	if _, err := store.Store("OMM.fbs", buildEngineOMM(t, 25544, "ISS", 1700000000), "peer", nil); err != nil {
		t.Fatalf("store $OMM: %v", err)
	}
	if _, err := store.db.Exec(`
		CREATE TABLE sds_cat (
			cid TEXT PRIMARY KEY,
			peer_id TEXT NOT NULL,
			timestamp INTEGER NOT NULL,
			data BLOB NOT NULL,
			signature BLOB,
			UNIQUE(cid)
		)`); err != nil {
		t.Fatalf("create pre-stream blob table: %v", err)
	}
	if _, err := store.db.Exec(
		`INSERT INTO sds_cat (cid, peer_id, timestamp, data) VALUES ('legacy-cid', 'peer', 1, ?)`,
		[]byte("legacy-cat-bytes")); err != nil {
		t.Fatalf("seed pre-stream blob row: %v", err)
	}
	if err := store.CheckpointRecordCatalog(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened := newEngineRecordsStore(t, basePath)
	defer reopened.Close()

	if !reopened.engineExcluded["CAT.fbs"] {
		t.Fatal("a standard whose store still holds its pre-stream blob table must not be routed")
	}
	if reopened.engineRoutesSchema("CAT.fbs") {
		t.Fatal("the standard is still routed, so its blob rows can never migrate")
	}
	if exists, err := reopened.tableExists("sds_cat"); err != nil {
		t.Fatalf("inspect legacy blob table: %v", err)
	} else if exists {
		t.Fatal("the legacy blob table was NOT migrated — its rows are unreachable through every production read path")
	}
	if exists, err := reopened.tableExists("CAT"); err != nil {
		t.Fatalf("inspect canonical table: %v", err)
	} else if !exists {
		t.Fatal("the canonical table does not exist after migration")
	}

	// THE ASSERTION THE RULE EXISTS FOR: the rows are readable the ordinary
	// way, through the production read path.
	data, err := reopened.Get("CAT.fbs", "legacy-cid")
	if err != nil {
		t.Fatalf("migrated legacy row is unreachable through Get: %v", err)
	}
	if string(data) != "legacy-cat-bytes" {
		t.Fatalf("migrated legacy row = %q, want %q", string(data), "legacy-cat-bytes")
	}

	// Per standard, not a blanket retreat.
	if !reopened.engineRoutesSchema("IRM.fbs") || !reopened.engineRoutesSchema("OMM.fbs") {
		t.Fatal("one legacy blob table must not un-route the rest of the catalog")
	}
}

// TestEnginePrepareFailureNeverDiscardsTheControlDatabase pins the blast
// radius of moving registerEngineFileIDs in front of the database's first
// query. It used to run AFTER the open, in NewFlatSQLStore, where a failure
// was a hard NON-DESTRUCTIVE start failure. Inside tryOpenControlDatabase its
// error means "unusable database", and the caller answers that by DELETING the
// control database — multi-GB of re-derive on host-01/host-02 — for an error
// that says nothing about the file. RegisterFileID THROWS on a table absent
// from the schema, and now runs 227 times per boot instead of 2.
func TestEnginePrepareFailureNeverDiscardsTheControlDatabase(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, basePath)
	if _, err := store.Store("OMM.fbs", buildEngineOMM(t, 25544, "ISS", 1700000000), "peer", nil); err != nil {
		t.Fatalf("store $OMM: %v", err)
	}
	if err := store.CheckpointRecordCatalog(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	dbPath := filepath.Join(basePath, flatSQLControlDBName)
	before, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat control database: %v", err)
	}

	engine, err := flatsqlrt.New(
		flatsqlrt.WithPrecompiledAOTCache(engineAOTCacheDir()),
		flatsqlrt.WithFileIORoot(basePath),
	)
	if err != nil {
		t.Fatalf("start engine: %v", err)
	}
	defer engine.Close()

	// Open with a schema that OMITS one standard while the prepare registers a
	// file identifier for it: the documented "unknown table" registration
	// failure, reached before the first query.
	schema := engineSchemaTextExcluding(map[string]bool{"CDM.fbs": true})
	prepare := enginePrepare(engineBootPlan{Excluded: map[string]bool{}, Sources: []string{engineDefaultSource}})
	_, _, _, err = openControlDatabase(engine, dbPath, schema, prepare)
	if err == nil {
		t.Fatal("registering a file identifier for a table absent from the schema must fail the open")
	}
	if !errors.Is(err, errEnginePrepareFailed) {
		t.Fatalf("prepare failure was not reported as a registration failure: %v", err)
	}
	after, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("the control database was DISCARDED by a registration failure: %v", err)
	}
	if after.Size() != before.Size() {
		t.Fatalf("control database size changed after a registration failure: %d -> %d", before.Size(), after.Size())
	}
}

// ==========================================================================
// THE ROUTED SURFACE SURVIVES THE CALLER'S SPELLING
// ==========================================================================

// TestRecordsStoredWithTheBareStandardCodeSurviveARestart is the delivered
// outcome, measured through the spelling production actually uses.
//
// sdn_record_index.schema_name and the record-catalog journal frame are
// written VERBATIM, and the bare code is what caps/storage.go forwards from a
// module's {"schema":"OMM"} and what the wasm provider sources set as
// record_schema. Both rebuild paths asked only for the canonical ".fbs" name,
// so a record written the bare way was live-readable and came back as ZERO
// rows after a restart — the exact failure the routing flip exists to
// eliminate.
func TestRecordsStoredWithTheBareStandardCodeSurviveARestart(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, basePath)

	if _, err := store.Store("IRM", buildEngineIRM(t, "bare-spelling", 1, 4096), "peer", nil); err != nil {
		t.Fatalf("store bare-spelled $IRM: %v", err)
	}
	if _, err := store.Store("OMM", buildEngineOMM(t, 25544, "ISS", 1700000000), "peer", nil); err != nil {
		t.Fatalf("store bare-spelled $OMM: %v", err)
	}

	// The PUBLIC surface must agree with the rows that are provably readable.
	surface, err := store.PublicQuerySurface()
	if err != nil {
		t.Fatalf("PublicQuerySurface: %v", err)
	}
	for _, code := range []string{"IRM", "OMM"} {
		var records int64 = -1
		partitions := 0
		for _, rel := range surface {
			switch {
			case rel.Name == code:
				records = rel.Records
			case strings.HasPrefix(rel.Name, code+"@") && rel.Source != "":
				partitions++
			}
		}
		if records != 1 {
			t.Errorf("PublicQuerySurface reports %s Records = %d, want 1", code, records)
		}
		if partitions == 0 {
			t.Errorf("PublicQuerySurface lists no %s@<source> partition although %s has a record resident", code, code)
		}
	}

	if err := store.CheckpointRecordCatalog(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	assertOneFrame := func(t *testing.T, s *FlatSQLStore, table string) {
		t.Helper()
		stream, err := s.QueryRawStream(`SELECT _data FROM "`+table+`" ORDER BY _rowid DESC LIMIT ?`, 8)
		if err != nil {
			t.Fatalf("read %s after restart: %v", table, err)
		}
		frames, err := flatsqlrt.DecodeSizePrefixedStream(stream.Bytes)
		if err != nil {
			t.Fatalf("decode %s frames: %v", table, err)
		}
		if len(frames) != 1 {
			t.Fatalf("%s came back with %d frames after a restart, want 1", table, len(frames))
		}
	}

	// Path 1: the boot rebuild that reads sdn_record_index.
	reopened := newEngineRecordsStore(t, basePath)
	assertOneFrame(t, reopened, "IRM")
	assertOneFrame(t, reopened, "OMM")
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened store: %v", err)
	}

	// Path 2: the ONE-PASS record-catalog journal hydration, whose frames carry
	// the same bare spelling.
	deferred := reopenDeferred(t, basePath)
	defer deferred.Close()
	loaded, err := deferred.HydrateEngineHotWindowFromRecordCatalog()
	if err != nil {
		t.Fatalf("HydrateEngineHotWindowFromRecordCatalog: %v", err)
	}
	if loaded != 2 {
		t.Fatalf("journal hydration loaded %d bare-spelled records, want 2", loaded)
	}
	assertOneFrame(t, deferred, "IRM")
	assertOneFrame(t, deferred, "OMM")
}

// ==========================================================================
// RECOVERY CARRIES THE SAME GUARANTEE AS BOOT
// ==========================================================================

// TestRecoveredEngineStillResolvesEveryRoutedBaseName closes the gap between
// the branch's new promise and the recovery path. RecoverPoisonedEngine
// DISCARDS the control database and opens a replacement, whose lazy
// initializeSQLiteEngine latches with no tables and whose unified views went
// with the old file. The replays register a source per record they load, so a
// store whose journal yields no records came out of recovery with no base vtab
// and no view for any routed standard: `SELECT _data FROM IRM` answered
// "no such table" — the answer firstRunSemantics promises can never happen,
// and the one PublicQuerySurface turns into an error for the WHOLE surface.
func TestRecoveredEngineStillResolvesEveryRoutedBaseName(t *testing.T) {
	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	engine, _ := store.EngineRuntime()
	if engine == nil || engine.Poisoned() {
		t.Fatal("engine should be live and healthy before the test poisons it")
	}
	epochBefore := store.EngineEpoch()
	engine.MarkPoisoned()

	if _, err := store.RecoverPoisonedEngine(); err != nil {
		t.Fatalf("RecoverPoisonedEngine: %v", err)
	}
	if store.EngineEpoch() <= epochBefore {
		t.Fatalf("engine epoch did not advance: before=%d after=%d", epochBefore, store.EngineEpoch())
	}

	caps := flatsqlrt.SandboxCaps{MaxRows: 4, MaxBytes: 1 << 16, Timeout: 30 * time.Second}
	for _, schemaName := range store.engineRoutedSchemaNames() {
		binding := engineRoutedSchemas[schemaName]
		stream, err := store.QuerySandboxedStream(
			`SELECT _data FROM "`+binding.Table+`" ORDER BY _rowid DESC LIMIT ?`, caps, 1)
		if err != nil {
			t.Fatalf("%s does not resolve after engine recovery: %v", binding.Table, err)
		}
		if len(stream.Bytes) != 0 {
			t.Fatalf("%s returned %d bytes on a recovered empty store, want an empty stream", binding.Table, len(stream.Bytes))
		}
	}
	if _, err := store.PublicQuerySurface(); err != nil {
		t.Fatalf("PublicQuerySurface after engine recovery: %v", err)
	}
}

// ==========================================================================
// CONFIGURED WINDOWS ARE HONOURED, NOT SILENTLY REWRITTEN
// ==========================================================================

// TestGenericHotWindowAboveTheDecoratedWindowIsHonoured: raising
// storage.engine_generic_hot_window above storage.engine_hot_window used to
// hand the operator engineHotWindow instead, with no warning and no mention in
// the config comment. A key that quietly means something else over half its
// range is worse than one that costs memory; the store warns at open instead.
func TestGenericHotWindowAboveTheDecoratedWindowIsHonoured(t *testing.T) {
	store := newEngineRecordsStoreWithOptions(t, filepath.Join(t.TempDir(), "store"),
		WithEngineHotWindow(1000), WithEngineGenericHotWindow(5000))
	defer store.Close()

	if got := store.engineWindowFor("IRM.fbs"); got != 5000 {
		t.Fatalf("generic window for a generically routed standard = %d, want the configured 5000", got)
	}
	if got := store.engineWindowFor("OMM.fbs"); got != 1000 {
		t.Fatalf("window for a decorated standard = %d, want the configured 1000", got)
	}
	if got := store.engineWindowFor("TBS.fbs"); got != 1000 {
		t.Fatalf("window for a decorated standard = %d, want the configured 1000", got)
	}
	// And the ordinary direction still holds.
	smaller := newEngineRecordsStoreWithOptions(t, filepath.Join(t.TempDir(), "smaller"),
		WithEngineHotWindow(1000), WithEngineGenericHotWindow(50))
	defer smaller.Close()
	if got := smaller.engineWindowFor("IRM.fbs"); got != 50 {
		t.Fatalf("generic window = %d, want 50", got)
	}
}
