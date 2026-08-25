package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// seedTwoSchemaStore writes records for TWO routed standards ($OMM and $IRM),
// checkpoints them into the compact record catalog, and returns the base path
// of the closed store.
func seedTwoSchemaStore(t *testing.T, ommRecords, irmRecords int) string {
	t.Helper()
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, basePath)

	ommTags := SourceTags{ProviderID: "prov-a", SourceName: "hot-window-gp", BatchID: "batch-1"}
	for i := 0; i < ommRecords; i++ {
		record := buildEngineOMM(t, uint32(4000+i), "HOTWIN-SAT", int64(1700000000+i))
		if _, err := store.StoreWithSourceTags("OMM.fbs", record, "peer-hot-window", nil, ommTags); err != nil {
			t.Fatalf("store $OMM %d: %v", i, err)
		}
	}
	irmTags := SourceTags{ProviderID: "mls", SourceName: "cell-tower-bulk", BatchID: "mls@1"}
	for i := 1; i <= irmRecords; i++ {
		record := buildEngineIRM(t, "hot-window", uint64(i), uint64(i)*1024)
		if _, err := store.StoreWithSourceTags("IRM.fbs", record, "peer-hot-window", nil, irmTags); err != nil {
			t.Fatalf("store $IRM %d: %v", i, err)
		}
	}
	if err := store.CheckpointRecordCatalog(); err != nil {
		t.Fatalf("checkpoint record catalog: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	return basePath
}

func reopenDeferred(t *testing.T, basePath string) *FlatSQLStore {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(basePath, validator,
		WithDeferredBootRebuilds(),
		WithDeferredRecordCatalogReplay())
	if err != nil {
		t.Fatalf("deferred reopen failed: %v", err)
	}
	return store
}

// TestCompactHotWindowHydrationCostsOneJournalPassForEveryRoutedStandard is the
// BOOT COST BOUND, and it is the reason routing every embedded standard is
// affordable at all.
//
// A hot-window pass reads and decodes the ENTIRE record-catalog journal —
// 5.5 GB and ~78 s on host-02 — because the per-schema filter is applied after
// each frame is decoded. Scanning once per routed schema would therefore
// multiply boot by the routed standard count (2 -> 227 here) while holding the
// store write lock, which is hours of stop-the-world on the production boxes.
// The invariant is: the number of full journal reads does not depend on how
// many standards are routed.
func TestCompactHotWindowHydrationCostsOneJournalPassForEveryRoutedStandard(t *testing.T) {
	basePath := seedTwoSchemaStore(t, 3, 4)
	reopened := reopenDeferred(t, basePath)
	defer reopened.Close()

	if routed := len(reopened.engineRoutedSchemaNames()); routed < 100 {
		t.Fatalf("only %d routed schemas — this test is meaningless unless the whole catalog is routed", routed)
	}
	loaded, err := reopened.HydrateEngineHotWindowFromRecordCatalog()
	if err != nil {
		t.Fatalf("HydrateEngineHotWindowFromRecordCatalog failed: %v", err)
	}
	if loaded != 7 {
		t.Fatalf("hydration loaded %d records, want 7", loaded)
	}
	if passes := reopened.recordCatalog.EngineHotWindowPasses(); passes != 1 {
		t.Fatalf("hydration made %d full journal passes, want exactly 1", passes)
	}
	if count, err := reopened.EngineRecordCount("OMM.fbs"); err != nil || count != 3 {
		t.Fatalf("engine $OMM count = %d err=%v, want 3 nil", count, err)
	}
	if count, err := reopened.EngineRecordCount("IRM.fbs"); err != nil || count != 4 {
		t.Fatalf("engine $IRM count = %d err=%v, want 4 nil", count, err)
	}
}

// TestCompactHotWindowHydrationStopsOnContextCancel pins the shutdown half of
// the same bound. The pass runs under the store write lock in a goroutine
// Stop() waits on; an uninterruptible multi-GB scan there is what ran 15 of 22
// host-01 stops into SIGKILL. A cancelled hydration is not an error and does
// not claim the window is hydrated — the journal is durable, so the next boot
// redoes it.
func TestCompactHotWindowHydrationStopsOnContextCancel(t *testing.T) {
	basePath := seedTwoSchemaStore(t, 3, 4)
	reopened := reopenDeferred(t, basePath)
	defer reopened.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	loaded, err := reopened.HydrateEngineHotWindowFromRecordCatalogContext(ctx)
	if err != nil {
		t.Fatalf("cancelled hydration returned an error: %v", err)
	}
	if loaded != 0 {
		t.Fatalf("cancelled hydration loaded %d records, want 0", loaded)
	}
	if reopened.EngineHotWindowHydrated() {
		t.Fatal("a cancelled hydration must not mark the hot window hydrated")
	}
	if count, err := reopened.EngineRecordCount("OMM.fbs"); err != nil || count != 0 {
		t.Fatalf("engine $OMM count after cancellation = %d err=%v, want 0 nil", count, err)
	}

	// And it resumes: the cancellation cost nothing but the pass.
	loaded, err = reopened.HydrateEngineHotWindowFromRecordCatalog()
	if err != nil {
		t.Fatalf("hydration after cancellation failed: %v", err)
	}
	if loaded != 7 {
		t.Fatalf("hydration after cancellation loaded %d records, want 7", loaded)
	}
}

// TestCompactHotWindowDeferralLoadsEverySchema proves the memory bound loses
// nothing. One pass fans out into one candidate heap per schema, so its peak
// memory is the SUM of the resident windows; when that exceeds
// engineHotWindowCandidateBudget a schema met for the first time is deferred to
// a follow-up pass rather than allocated. A deferred schema has no partial
// state, so the follow-up reads it from offset 0 and loads it in full.
func TestCompactHotWindowDeferralLoadsEverySchema(t *testing.T) {
	restore := engineHotWindowCandidateBudget
	engineHotWindowCandidateBudget = 1
	t.Cleanup(func() { engineHotWindowCandidateBudget = restore })

	basePath := seedTwoSchemaStore(t, 3, 4)
	reopened := reopenDeferred(t, basePath)
	defer reopened.Close()

	loaded, err := reopened.HydrateEngineHotWindowFromRecordCatalog()
	if err != nil {
		t.Fatalf("HydrateEngineHotWindowFromRecordCatalog failed: %v", err)
	}
	if loaded != 7 {
		t.Fatalf("budget-constrained hydration loaded %d records, want 7", loaded)
	}
	if passes := reopened.recordCatalog.EngineHotWindowPasses(); passes != 2 {
		t.Fatalf("budget-constrained hydration made %d passes, want 2 (one per deferred schema)", passes)
	}
	if count, err := reopened.EngineRecordCount("OMM.fbs"); err != nil || count != 3 {
		t.Fatalf("engine $OMM count = %d err=%v, want 3 nil", count, err)
	}
	if count, err := reopened.EngineRecordCount("IRM.fbs"); err != nil || count != 4 {
		t.Fatalf("engine $IRM count = %d err=%v, want 4 nil", count, err)
	}
}

// TestSchemasWithRecordTablesMatchesTheReadSource pins the boot-rebuild
// short-circuit to the exact property it claims: a standard is skipped only
// when it has NO backing record table, which is the same condition under which
// recordReadSource returns the always-empty read source. Anything looser would
// silently leave a populated standard out of the engine — the defect this whole
// change exists to fix.
func TestSchemasWithRecordTablesMatchesTheReadSource(t *testing.T) {
	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	present, err := store.schemasWithRecordTables()
	if err != nil {
		t.Fatalf("schemasWithRecordTables on a fresh store: %v", err)
	}
	if len(present) != 0 {
		t.Fatalf("a fresh store reports %d schemas with record tables, want 0", len(present))
	}

	tags := SourceTags{ProviderID: "mls", SourceName: "cell-tower-bulk", BatchID: "mls@1"}
	if _, err := store.StoreWithSourceTags("IRM.fbs", buildEngineIRM(t, "presence", 1, 1024), "peer-presence", nil, tags); err != nil {
		t.Fatalf("store $IRM: %v", err)
	}

	present, err = store.schemasWithRecordTables()
	if err != nil {
		t.Fatalf("schemasWithRecordTables after one write: %v", err)
	}
	if !present["IRM.fbs"] {
		t.Fatal("the standard that was just written is not reported as present")
	}
	if present["OMM.fbs"] {
		t.Fatal("a standard with no record table is reported as present")
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	for _, schemaName := range store.engineRoutedSchemaNames() {
		readSource, err := store.recordReadSource(schemaName)
		if err != nil {
			t.Fatalf("recordReadSource(%s): %v", schemaName, err)
		}
		readable := readSource != emptyRecordReadSource
		if readable != present[schemaName] {
			t.Fatalf("%s: presence=%v but readable=%v — the skip and the read source disagree",
				schemaName, present[schemaName], readable)
		}
	}
}

// TestControlDatabaseWithoutAStandardsEngineObjectsUpgradesInPlace is the
// CUTOVER, and it is the one path host-01 and host-02 actually take: a warm
// control database written by a binary that did NOT route a standard is opened
// by a binary that does. The pre-flip file has no base vtab, no per-source
// shadow and no unified view for that standard — only its durable
// (producer, standard) rows — and the upgrade must materialize all three and
// bring the rows back, without disturbing the standards that were already
// routed.
func TestControlDatabaseWithoutAStandardsEngineObjectsUpgradesInPlace(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, basePath)

	ommTags := SourceTags{ProviderID: "prov-a", SourceName: "cutover-gp", BatchID: "batch-1"}
	if _, err := store.StoreWithSourceTags("OMM.fbs", buildEngineOMM(t, 25544, "ISS", 1700000000), "peer-cutover", nil, ommTags); err != nil {
		t.Fatalf("store $OMM: %v", err)
	}
	irmTags := SourceTags{ProviderID: "mls", SourceName: "cell-tower-bulk", BatchID: "mls@1"}
	const marks = 3
	for i := 1; i <= marks; i++ {
		if _, err := store.StoreWithSourceTags("IRM.fbs", buildEngineIRM(t, "cutover", uint64(i), uint64(i)*1024), "peer-cutover", nil, irmTags); err != nil {
			t.Fatalf("store $IRM %d: %v", i, err)
		}
	}

	// Make the file look like one a PRE-FLIP binary wrote: the standard's
	// engine objects are simply not there. Its durable rows stay exactly where
	// they are — that is the substrate the upgrade rehydrates from.
	for _, ddl := range []string{
		`DROP VIEW IF EXISTS IRM`,
		`DROP TABLE IF EXISTS "IRM@cell-tower-bulk"`,
		`DROP TABLE IF EXISTS "IRM"`,
	} {
		if _, err := store.db.Exec(ddl); err != nil {
			t.Fatalf("shape the pre-flip control database (%s): %v", ddl, err)
		}
	}
	if _, err := store.db.Query(`SELECT count(*) FROM IRM`); err == nil {
		t.Fatal("the pre-flip fixture still resolves IRM — the test is not testing an upgrade")
	}
	var durable int
	if err := store.db.QueryRow(`SELECT count(*) FROM "sds_p_peer_cutover__IRM"`).Scan(&durable); err != nil {
		t.Fatalf("durable $IRM rows: %v", err)
	}
	if durable != marks {
		t.Fatalf("durable $IRM rows = %d, want %d", durable, marks)
	}
	if err := store.CheckpointRecordCatalog(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// THE UPGRADE BOOT.
	reopened := newEngineRecordsStore(t, basePath)
	defer reopened.Close()

	// WARM, or this is not the cutover: a discarded control database would be
	// re-derived from the journal and would never exercise the upgrade of an
	// existing file.
	if boot := reopened.BootReplay(); !boot.Warm || !boot.Durable {
		t.Fatalf("upgrade boot was not a warm resume of the persisted control database: %+v", boot)
	}
	if reopened.engineExcluded["IRM.fbs"] {
		t.Fatal("a standard whose engine objects were merely absent must be routed after the upgrade, not excluded")
	}
	stream, err := reopened.QueryRawStream(`SELECT _data FROM IRM ORDER BY _rowid DESC LIMIT ?`, 32)
	if err != nil {
		t.Fatalf("read $IRM after the upgrade boot: %v", err)
	}
	frames, err := flatsqlrt.DecodeSizePrefixedStream(stream.Bytes)
	if err != nil {
		t.Fatalf("decode $IRM frames: %v", err)
	}
	if len(frames) != marks {
		t.Fatalf("$IRM frames after the upgrade = %d, want %d", len(frames), marks)
	}
	// The standard that was ALREADY routed is untouched by the upgrade.
	if count, err := reopened.EngineRecordCount("OMM.fbs"); err != nil || count != 1 {
		t.Fatalf("engine $OMM count after the upgrade = %d err=%v, want 1 nil", count, err)
	}
	// And the durable rows were never the thing being rebuilt.
	if err := reopened.db.QueryRow(`SELECT count(*) FROM "sds_p_peer_cutover__IRM"`).Scan(&durable); err != nil {
		t.Fatalf("durable $IRM rows after the upgrade: %v", err)
	}
	if durable != marks {
		t.Fatalf("durable $IRM rows after the upgrade = %d, want %d", durable, marks)
	}
}
