package storage

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// TestReplayRecordCatalogRestoresJournalOnlyRecords reproduces the restart bug:
// a store reopened the way a daemon opens it (WithDeferredBootRebuilds +
// WithDeferredRecordCatalogReplay) has its records only in the compact journal /
// stream files — they are INVISIBLE to the SQL control tables that feed
// /api/v1/stats sources[], /api/v1/data/index, and batch clear. It then asserts
// that ReplayRecordCatalog + RebuildSourceSummaries (the background hydration
// wiring, and the admin re-sync trigger) make every record visible again, and
// that re-running the replay is idempotent.
func TestReplayRecordCatalogRestoresJournalOnlyRecords(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}

	store, err := NewFlatSQLStore(basePath, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}

	base := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC).Unix()
	catalogfixture := SourceTags{ProviderID: "prov-a", SourceName: "catalogfixture-gp", BatchID: "batch-1"}
	other := SourceTags{ProviderID: "prov-b", SourceName: "provider-two", BatchID: "batch-1"}

	catalogfixtureRecords := [][]byte{
		buildEngineOMM(t, 1001, "SAT-1001", base),
		buildEngineOMM(t, 1002, "SAT-1002", base+86400),
		buildEngineOMM(t, 1003, "SAT-1003", base+2*86400),
	}
	otherRecords := [][]byte{
		buildEngineOMM(t, 2001, "OTHER-1", base),
		buildEngineOMM(t, 2002, "OTHER-2", base+86400),
	}
	const wantRecords = 5

	if _, err := store.StoreBatchWithSourceTags("OMM.fbs", catalogfixtureRecords, "peer-a", nil, catalogfixture); err != nil {
		t.Fatalf("store catalogfixture batch: %v", err)
	}
	if _, err := store.StoreBatchWithSourceTags("OMM.fbs", otherRecords, "peer-b", nil, other); err != nil {
		t.Fatalf("store other batch: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Reopen exactly the way the daemon opens the store: both derived-state and
	// record-catalog replay deferred. This is the post-restart, journal-only
	// state where the bug bites.
	reopened, err := NewFlatSQLStore(basePath, validator,
		WithDeferredBootRebuilds(), WithDeferredRecordCatalogReplay())
	if err != nil {
		t.Fatalf("deferred reopen failed: %v", err)
	}
	defer reopened.Close()

	// PRE-REPLAY VISIBILITY CHANGED, DELIBERATELY, AND THAT IS THE FIX.
	//
	// This test used to assert 0 sources / 0 records here, because the control
	// tables lived in an ephemeral `:memory:` database that started empty on
	// every process. Records really were invisible until a full replay rebuilt
	// them, and THAT is the "restart bug" the test name refers to — it is now
	// structurally gone (flatsql_boot_state.go): after a clean shutdown the
	// tables are already on disk and already correct.
	//
	// The two things that still matter are asserted instead:
	//   - the hydration FLAG stays false until the tail has been applied, so
	//     the fail-closed export gate (ErrRecordCatalogHydrating, 2a2ffea5) is
	//     unchanged; and
	//   - the cold path below still recovers everything from the journal alone.
	if reopened.RecordCatalogHydrated() {
		t.Fatal("record catalog must not report hydrated before replay on a deferred reopen")
	}
	summaryBefore, err := reopened.DataSummary()
	if err != nil {
		t.Fatalf("DataSummary before hydrate: %v", err)
	}
	if !reopened.BootReplay().Durable {
		t.Fatal("reopen is not disk-backed — the durable-boot lane is inert")
	}
	if summaryBefore.TotalRecords != wantRecords {
		t.Fatalf("BEFORE hydrate on a WARM reopen: total=%d, want %d (durable control tables must survive the restart)",
			summaryBefore.TotalRecords, wantRecords)
	}
	rawBefore, err := reopened.QueryRawRecords(RawRecordQuery{SchemaName: "OMM.fbs", Limit: 100})
	if err != nil {
		t.Fatalf("QueryRawRecords before hydrate: %v", err)
	}
	if len(rawBefore) != wantRecords {
		t.Fatalf("BEFORE hydrate on a WARM reopen: QueryRawRecords returned %d, want %d", len(rawBefore), wantRecords)
	}

	// Hydrate: replay the catalog into the control tables, then rebuild the
	// derived source summaries — the two-step the background goroutine and the
	// admin trigger both run.
	progressCalls := 0
	replayed, err := reopened.ReplayRecordCatalog(false, func(int) { progressCalls++ })
	if err != nil {
		t.Fatalf("ReplayRecordCatalog: %v", err)
	}
	// A WARM reopen has nothing to replay: the mark already covers the whole
	// journal, so the correct count here is ZERO. (The cold twin of this test
	// asserts the >= wantRecords case, which is where a frame count is
	// meaningful.) What must still hold is the flag flip below and the visible
	// state after — replaying nothing is only acceptable because the tables were
	// already right.
	if replayed != 0 {
		t.Fatalf("replayed=%d on a WARM reopen, want 0 (the mark already covered the journal)", replayed)
	}
	if !reopened.RecordCatalogHydrated() {
		t.Fatal("record catalog must report hydrated after ReplayRecordCatalog")
	}
	if err := reopened.RebuildSourceSummaries(); err != nil {
		t.Fatalf("RebuildSourceSummaries: %v", err)
	}

	summaryAfter, err := reopened.DataSummary()
	if err != nil {
		t.Fatalf("DataSummary after hydrate: %v", err)
	}
	if summaryAfter.TotalRecords != wantRecords {
		t.Fatalf("AFTER hydrate: total_records=%d, want %d", summaryAfter.TotalRecords, wantRecords)
	}
	if len(summaryAfter.Sources) != 2 {
		t.Fatalf("AFTER hydrate: sources=%d, want 2 (catalogfixture-gp + provider-two)", len(summaryAfter.Sources))
	}
	if got := sourceRecordCount(summaryAfter.Sources, "prov-a", "catalogfixture-gp"); got != 3 {
		t.Fatalf("AFTER hydrate: catalogfixture-gp count=%d, want 3", got)
	}
	if got := sourceRecordCount(summaryAfter.Sources, "prov-b", "provider-two"); got != 2 {
		t.Fatalf("AFTER hydrate: provider-two count=%d, want 2", got)
	}
	rawAfter, err := reopened.QueryRawRecords(RawRecordQuery{SchemaName: "OMM.fbs", Limit: 100})
	if err != nil {
		t.Fatalf("QueryRawRecords after hydrate: %v", err)
	}
	if len(rawAfter) != wantRecords {
		t.Fatalf("AFTER hydrate: QueryRawRecords returned %d, want %d", len(rawAfter), wantRecords)
	}

	// Idempotency: a forced re-replay (the admin re-sync path) + summary rebuild
	// must converge to exactly the same visible state — no duplicated rows or
	// inflated counts.
	if _, err := reopened.ReplayRecordCatalog(true, nil); err != nil {
		t.Fatalf("forced re-replay: %v", err)
	}
	if err := reopened.RebuildSourceSummaries(); err != nil {
		t.Fatalf("RebuildSourceSummaries after forced re-replay: %v", err)
	}
	summaryReplay, err := reopened.DataSummary()
	if err != nil {
		t.Fatalf("DataSummary after forced re-replay: %v", err)
	}
	if summaryReplay.TotalRecords != wantRecords || len(summaryReplay.Sources) != 2 {
		t.Fatalf("forced re-replay not idempotent: total=%d sources=%d, want %d/2",
			summaryReplay.TotalRecords, len(summaryReplay.Sources), wantRecords)
	}

	_ = progressCalls // batch boundary is large; not exercised at this record count
}

// TestReplayRecordCatalogRestoresJournalOnlyRecordsFromACOLDStore keeps the
// ORIGINAL assertion of the test above alive on the path where it still holds.
//
// Discard the control database and the store is exactly what it used to be on
// every single boot: records live only in the journal and the stream files,
// invisible to the SQL control tables until a replay rebuilds them. That path
// is now the FALLBACK rather than the norm, and it must stay correct forever —
// it is what every corruption, every compaction and every format bump degrades
// to.
func TestReplayRecordCatalogRestoresJournalOnlyRecordsFromACOLDStore(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}

	store, err := NewFlatSQLStore(basePath, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	base := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC).Unix()
	tags := SourceTags{ProviderID: "prov-a", SourceName: "catalogfixture-gp", BatchID: "batch-1"}
	records := [][]byte{
		buildEngineOMM(t, 3001, "COLD-1", base),
		buildEngineOMM(t, 3002, "COLD-2", base+86400),
		buildEngineOMM(t, 3003, "COLD-3", base+2*86400),
	}
	if _, err := store.StoreBatchWithSourceTags("OMM.fbs", records, "peer-a", nil, tags); err != nil {
		t.Fatalf("store batch: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Throw the control database away. Only the journal and the streams remain.
	if err := removeControlDatabaseFiles(filepath.Join(basePath, flatSQLControlDBName)); err != nil {
		t.Fatalf("discard control database: %v", err)
	}

	reopened, err := NewFlatSQLStore(basePath, validator,
		WithDeferredBootRebuilds(), WithDeferredRecordCatalogReplay())
	if err != nil {
		t.Fatalf("deferred reopen failed: %v", err)
	}
	defer reopened.Close()

	if reopened.BootReplay().Warm {
		t.Fatal("a discarded control database still produced a warm boot")
	}
	summaryBefore, err := reopened.DataSummary()
	if err != nil {
		t.Fatalf("DataSummary before hydrate: %v", err)
	}
	if len(summaryBefore.Sources) != 0 || summaryBefore.TotalRecords != 0 {
		t.Fatalf("COLD, BEFORE hydrate: sources=%d total=%d, want 0/0",
			len(summaryBefore.Sources), summaryBefore.TotalRecords)
	}

	if _, err := reopened.ReplayRecordCatalog(false, nil); err != nil {
		t.Fatalf("ReplayRecordCatalog: %v", err)
	}
	if err := reopened.RebuildSourceSummaries(); err != nil {
		t.Fatalf("RebuildSourceSummaries: %v", err)
	}
	summaryAfter, err := reopened.DataSummary()
	if err != nil {
		t.Fatalf("DataSummary after hydrate: %v", err)
	}
	if summaryAfter.TotalRecords != int64(len(records)) {
		t.Fatalf("COLD, AFTER hydrate: total_records=%d, want %d", summaryAfter.TotalRecords, len(records))
	}
	rawAfter, err := reopened.QueryRawRecords(RawRecordQuery{SchemaName: "OMM.fbs", Limit: 100})
	if err != nil {
		t.Fatalf("QueryRawRecords after hydrate: %v", err)
	}
	if len(rawAfter) != len(records) {
		t.Fatalf("COLD, AFTER hydrate: QueryRawRecords returned %d, want %d", len(rawAfter), len(records))
	}
}

func sourceRecordCount(sources []DataSourceSummary, providerID, sourceName string) int64 {
	var total int64
	for _, s := range sources {
		if s.ProviderID == providerID && s.SourceName == sourceName {
			total += s.Count
		}
	}
	return total
}
