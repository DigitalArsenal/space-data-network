package storage

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/encfield"
	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/storage/enginecatalog"
)

// ==========================================================================
// THE DATA-DESTRUCTION GUARD IS FAIL-CLOSED
// ==========================================================================

// TestUnprobedControlDatabaseRoutesOnlyTheDecoratedStandards is the fix for a
// guard that was fail-OPEN. probeControlDatabase used to answer every failure
// with an EMPTY exclusion set, and an empty exclusion set is not "route
// nothing new" — it is the full 226-standard schema plus a file-id
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
		t.Fatal("an unreadable control database produced an EMPTY exclusion set: the boot would route all 226 standards over a file it never managed to look at")
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
// 226-name one: a store still holding `sds_cat` would never migrate those rows
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

// TestLeftoverViewForAStandardThisBinaryDroppedIsSweptAtBoot covers the input
// class a per-store exclusion set cannot see: a standard that LEAVES THE
// CATALOG between binaries.
//
// A unified view is persisted. dropExcludedStandardViews used to sweep only
// the names in THIS STORE's exclusion set, which is a subset of the routed
// catalog — so a standard the previous binary routed and this one does not
// know at all (what commit 4ddce3c8 did to $KMF when it gained an
// `(encrypted)` field, and what any SDS bump does when an IDL loses its
// file_identifier) kept a view with no vtab module behind it. Every
// plain-table path then resolves that view instead of the canonical control
// table, answers `no such module: __flatsql_module_kmf_<src>`, and CREATE
// TABLE on the same name collides with it: exactly the failure the sweep
// exists to prevent, one input class short.
//
// The sweep is identified by the view's OWN definition, so a view somebody
// else created is never touched.
func TestLeftoverViewForAStandardThisBinaryDroppedIsSweptAtBoot(t *testing.T) {
	const dropped = "KMF"
	if _, routed := engineRoutedSchemas[dropped+".fbs"]; routed {
		t.Skipf("%s is routed by this binary — pick a standard this binary does not route", dropped)
	}

	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, basePath)
	// A warm store, or the control database is discarded on the next boot and
	// the leftover goes with it for the wrong reason.
	if _, err := store.Store("OMM.fbs", buildEngineOMM(t, 25544, "ISS", 1700000000), "peer", nil); err != nil {
		t.Fatalf("store $OMM: %v", err)
	}
	// What the PREVIOUS binary left behind, in the exact shape
	// CreateUnifiedViews writes.
	if _, err := store.engineDB.Query(
		`CREATE VIEW "` + dropped + `" AS SELECT "_source", "_rowid", "_offset", "_data" FROM "` + dropped + `@local"`); err != nil {
		t.Fatalf("seed leftover unified view: %v", err)
	}
	// And a view that is nobody's business but the operator's.
	if _, err := store.engineDB.Query(`CREATE VIEW "operator_report" AS SELECT count(*) AS n FROM "OMM"`); err != nil {
		t.Fatalf("seed operator view: %v", err)
	}
	if err := store.CheckpointRecordCatalog(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened := newEngineRecordsStore(t, basePath)
	defer reopened.Close()

	res, err := reopened.engineDB.Query(`SELECT name FROM sqlite_master WHERE type = 'view'`)
	if err != nil {
		t.Fatalf("enumerate persisted views: %v", err)
	}
	views := map[string]bool{}
	for _, row := range res.Rows {
		if name, ok := row[0].(string); ok {
			views[name] = true
		}
	}
	if views[dropped] {
		t.Fatalf("the leftover unified view for %s survived a boot that does not route it — every plain-table path for that name now resolves a view with no module behind it", dropped)
	}
	if !views["operator_report"] {
		t.Fatal("the sweep took a view it did not write")
	}
	// THE CONSEQUENCE, not just the artifact: the canonical control table for
	// that name can be created again. SQLite refuses CREATE TABLE while a view
	// of the same name exists.
	if _, err := reopened.db.Exec(`CREATE TABLE "` + dropped + `" (cid TEXT PRIMARY KEY, data BLOB)`); err != nil {
		t.Fatalf("the canonical control table for %s cannot be created: %v", dropped, err)
	}
}

// TestEnginePrepareFailureNeverDiscardsTheControlDatabase pins the blast
// radius of moving registerEngineFileIDs in front of the database's first
// query. It used to run AFTER the open, in NewFlatSQLStore, where a failure
// was a hard NON-DESTRUCTIVE start failure. Inside tryOpenControlDatabase its
// error means "unusable database", and the caller answers that by DELETING the
// control database — multi-GB of re-derive on host-01/host-02 — for an error
// that says nothing about the file. RegisterFileID THROWS on a table absent
// from the schema, and now runs 226 times per boot instead of 2.
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

// ==========================================================================
// A FIELD-ENCRYPTED STANDARD IS NEVER ROUTED
// ==========================================================================
//
// "Every embedded standard is routed" has exactly one stated exception, and
// this is it. $KMF (Key Material Frame — Ed25519 seeds, X25519 private keys,
// AES-256-GCM keys) declares KEY_BYTES `(encrypted)`, so the store SEALS it at
// rest: the stream file holds an encfield envelope and the plaintext exists
// only in the caller's buffer.
//
// The engine mirror is fed FROM THAT CALLER'S BUFFER, and the engine is the
// public /api/v1/query surface where `SELECT _data` returns the whole record.
// Routing $KMF therefore published, in plaintext, exactly the bytes the seal
// exists to protect — for the life of the process, to any module holding
// `storage_query`. There is no per-column fix: `_data` alone is enough.
//
// So the standard does not route, and this test measures the refusal end to
// end rather than trusting the catalog: the plaintext must be absent from the
// query surface AND from the stream file, while the record itself must still
// store, seal and read back through the ordinary APIs.
func TestFieldEncryptedStandardsAreNeverEngineRouted(t *testing.T) {
	// THE DECLARATION, re-validated against the IDL by
	// TestEveryEmbeddedStandardIsRoutedOrDeclaredUnroutable.
	if reason := engineUnroutableSchemas["KMF.fbs"]; reason != enginecatalog.SkipEncryptedField {
		t.Fatalf("KMF.fbs is declared unroutable for %q, want %q", reason, enginecatalog.SkipEncryptedField)
	}

	// THE GENERAL RULE, not a $KMF branch: nothing the store seals is routed.
	for name, binding := range engineRoutedSchemas {
		if encfield.HasEncryptedFields(binding.Table) {
			t.Errorf("%s seals fields at rest but is engine-routed: its plaintext would be served from the public query surface", name)
		}
	}
	if engineRoutesSchema("KMF.fbs") {
		t.Fatal("KMF.fbs is routed")
	}

	// THE REFUSAL THAT ACTUALLY REMOVES THE NAME FROM SQL. Withholding the
	// file id keeps records out of the table, but it does not keep the table
	// out of the database: CreateUnifiedViews builds a view for EVERY table
	// the schema text declares, whatever file ids are registered. "No such
	// table: KMF" therefore survives only while the generated schema text
	// contains no KMF table at all — assert that, rather than asserting it
	// downstream and calling three refusals independently sufficient.
	for _, decl := range []string{"table KMF ", "table KMF{", "table KMF\n"} {
		if strings.Contains(engineDatabaseSchema, decl) {
			t.Fatalf("the generated engine schema declares %q — a view for it materializes at CreateUnifiedViews", decl)
		}
	}

	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, basePath)
	if store.engineRoutesSchema("KMF.fbs") {
		t.Fatal("this store routes KMF.fbs")
	}

	secret := []byte("SUPER-SECRET-KEY-MATERIAL-0123456789")
	record := buildKMFRecordForTest(t, "probe-key-1", secret, 1)
	tags := SourceTags{ProviderID: "probe", SourceName: "probe-src", BatchID: "probe@1"}
	cid, err := store.StoreWithSourceTags("KMF.fbs", record, "peer-probe", nil, tags)
	if err != nil {
		t.Fatalf("store $KMF: %v", err)
	}

	caps := flatsqlrt.SandboxCaps{MaxRows: 8, MaxBytes: 1 << 20, Timeout: 30 * time.Second}
	assertNoKMFOnTheQuerySurface := func(t *testing.T, s *FlatSQLStore, when string) {
		t.Helper()
		for _, sqlText := range []string{
			`SELECT KEY_ID, KEY_BYTES FROM KMF`,
			`SELECT _data FROM KMF`,
			`SELECT _data FROM "KMF@probe-src"`,
		} {
			payload, _, _, err := s.QuerySandboxedJSON(sqlText, caps)
			if err == nil {
				t.Fatalf("%s: %s answered from the public query surface: %s", when, sqlText, payload)
			}
			if bytes.Contains(payload, secret) {
				t.Fatalf("%s: %s leaked the key material", when, sqlText)
			}
		}
		if _, err := s.QuerySandboxedStream(
			`SELECT _data FROM KMF ORDER BY _rowid DESC LIMIT ?`, caps, 1); err == nil {
			t.Fatalf("%s: the $KMF record stream answered from the public query surface", when)
		}
		surface, err := s.PublicQuerySurface()
		if err != nil {
			t.Fatalf("%s: public query surface: %v", when, err)
		}
		for _, rel := range surface {
			if rel.Name == "KMF" || strings.HasPrefix(rel.Name, "KMF@") {
				t.Fatalf("%s: the public query surface advertises %q", when, rel.Name)
			}
		}
		if n := s.engineResidentCount("KMF.fbs"); n != 0 {
			t.Fatalf("%s: engine residency for KMF.fbs = %d, want 0 — a count that is not the count is how a restart 'loses' rows", when, n)
		}
	}
	assertNoKMFOnTheQuerySurface(t, store, "live")

	// STILL SEALED AT REST, and the seal is what the exclusion protects.
	streamFile := filepath.Join(basePath, flatSQLStreamDirName, "KMF.flatsql")
	sealed, err := os.ReadFile(streamFile)
	if err != nil {
		t.Fatalf("read KMF stream file: %v", err)
	}
	if bytes.Contains(sealed, secret) {
		t.Fatal("the durable stream file holds the plaintext key material")
	}

	// AND THE STANDARD STILL WORKS. Un-routing is not a quarantine: the record
	// reads back through the ordinary API, decrypted.
	got, err := store.Get("KMF.fbs", cid)
	if err != nil {
		t.Fatalf("read back $KMF: %v", err)
	}
	if !bytes.Contains(got, secret) {
		t.Fatal("the ordinary read path did not return the decrypted key material")
	}

	if err := store.CheckpointRecordCatalog(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// A RESTART MUST NOT ROUTE IT EITHER. The boot rebuild reads the stream
	// files RAW (no decryption pass), so a sealed frame there is an encfield
	// envelope the engine cannot ingest — the case that used to be counted as
	// resident anyway.
	reopened := newEngineRecordsStore(t, basePath)
	defer reopened.Close()
	assertNoKMFOnTheQuerySurface(t, reopened, "after restart")
	if got, err := reopened.Get("KMF.fbs", cid); err != nil {
		t.Fatalf("read back $KMF after restart: %v", err)
	} else if !bytes.Contains(got, secret) {
		t.Fatal("the record did not survive the restart")
	}
}

// TestEngineIngestRefusesBytesItCannotRoute pins the residency invariant that
// a silent drop broke: the engine's ingest entry point answers a non-negative
// count for a buffer it cannot place, so a caller that counts loop iterations
// reports records the table does not hold ("live-readable, then ZERO rows
// after a restart", reported as resident). Every caller now classifies the
// bytes FIRST.
func TestEngineIngestRefusesBytesItCannotRoute(t *testing.T) {
	irm, routed := engineRoutedSchemaFor("IRM.fbs")
	if !routed {
		t.Fatal("IRM.fbs is not routed")
	}
	omm := buildEngineOMM(t, 25544, "ISS", 1700000000)

	if _, _, ok := engineIngestablePayload(irm, omm); ok {
		t.Fatal("an $OMM buffer was accepted as an $IRM record: the identifier is what the engine routes on")
	}
	if _, _, ok := engineIngestablePayload(irm, []byte("SDF1\x01\x8f\x84,not-a-flatbuffer")); ok {
		t.Fatal("a field-encryption envelope was accepted as a record")
	}
	if _, _, ok := engineIngestablePayload(irm, []byte("short")); ok {
		t.Fatal("a buffer too short to carry an identifier was accepted")
	}
	ommBinding, _ := engineRoutedSchemaFor("OMM.fbs")
	payload, reason, ok := engineIngestablePayload(ommBinding, omm)
	if !ok {
		t.Fatalf("a real $OMM record was refused for its own table: %s", reason)
	}
	if string(payload[4:8]) != ommBinding.FileID {
		t.Fatalf("payload identifier = %q, want %q", payload[4:8], ommBinding.FileID)
	}

	// END TO END: residency is what the engine actually holds, across a
	// restart, for every schema the store reports as resident.
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, basePath)
	for i := 0; i < 3; i++ {
		if _, err := store.Store("OMM.fbs", buildEngineOMM(t, uint32(40000+i), "SAT", 1700000000+int64(i)), "peer", nil); err != nil {
			t.Fatalf("store $OMM: %v", err)
		}
	}
	if err := store.CheckpointRecordCatalog(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened := newEngineRecordsStore(t, basePath)
	defer reopened.Close()
	for _, schemaName := range reopened.engineRoutedSchemaNames() {
		resident := reopened.engineResidentCount(schemaName)
		if resident == 0 {
			continue
		}
		binding, _ := reopened.engineRoutedSchemaFor(schemaName)
		res, err := reopened.engineDB.Query(`SELECT COUNT(*) FROM ` + quoteEngineRelation(binding.Table))
		if err != nil {
			t.Fatalf("count %s: %v", binding.Table, err)
		}
		if len(res.Rows) != 1 || len(res.Rows[0]) != 1 {
			t.Fatalf("count %s returned %d rows", binding.Table, len(res.Rows))
		}
		count, _ := res.Rows[0][0].(int64)
		if count != resident {
			t.Errorf("%s: engine holds %d records but residency says %d", schemaName, count, resident)
		}
	}
}

// TestEngineDerivedRTreesAreTheDisclosedSet pins the schema objects the ENGINE
// creates on its own. Routing every standard handed the engine 226 tables of
// schema-exact column names, and it answers geospatial-looking names with a
// derived R-Tree (three backing tables each) — cost that lands inside the
// un-batched first-query burst enginePrepare's header sizes the health-timeout
// warning against, and per-ingest index maintenance afterwards. The set is
// therefore a reviewed fact, not an emergent one.
func TestEngineDerivedRTreesAreTheDisclosedSet(t *testing.T) {
	disclosed := []string{
		"_rtree_CRM", "_rtree_ENV", "_rtree_GNO", "_rtree_ION", "_rtree_OBT",
		"_rtree_SEN", "_rtree_SEO", "_rtree_SIT", "_rtree_SWR", "_rtree_TBS",
		"_rtree_TMS", "_rtree_TRK",
	}

	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	res, err := store.engineDB.Query(
		"SELECT name FROM sqlite_master WHERE sql LIKE 'CREATE VIRTUAL%' AND name LIKE '\\_rtree\\_%' ESCAPE '\\' ORDER BY name")
	if err != nil {
		t.Fatalf("enumerate derived r-trees: %v", err)
	}
	got := make([]string, 0, len(res.Rows))
	for _, row := range res.Rows {
		got = append(got, fmt.Sprint(row[0]))
	}
	if strings.Join(got, ",") != strings.Join(disclosed, ",") {
		t.Fatalf("engine-derived r-trees = %v, disclosed %v — update the boot-cost disclosure in flatsql_boot_state.go (enginePrepare) before changing this list",
			got, disclosed)
	}
}
