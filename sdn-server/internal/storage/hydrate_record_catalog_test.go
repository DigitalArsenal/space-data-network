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

	if reopened.RecordCatalogHydrated() {
		t.Fatal("record catalog must not report hydrated before replay on a deferred reopen")
	}
	summaryBefore, err := reopened.DataSummary()
	if err != nil {
		t.Fatalf("DataSummary before hydrate: %v", err)
	}
	if len(summaryBefore.Sources) != 0 || summaryBefore.TotalRecords != 0 {
		t.Fatalf("BEFORE hydrate: sources=%d total=%d, want 0/0 (records must be invisible pre-replay)",
			len(summaryBefore.Sources), summaryBefore.TotalRecords)
	}
	rawBefore, err := reopened.QueryRawRecords(RawRecordQuery{SchemaName: "OMM.fbs", Limit: 100})
	if err != nil {
		t.Fatalf("QueryRawRecords before hydrate: %v", err)
	}
	if len(rawBefore) != 0 {
		t.Fatalf("BEFORE hydrate: QueryRawRecords returned %d, want 0", len(rawBefore))
	}

	// Hydrate: replay the catalog into the control tables, then rebuild the
	// derived source summaries — the two-step the background goroutine and the
	// admin trigger both run.
	progressCalls := 0
	replayed, err := reopened.ReplayRecordCatalog(false, func(int) { progressCalls++ })
	if err != nil {
		t.Fatalf("ReplayRecordCatalog: %v", err)
	}
	if replayed < wantRecords {
		t.Fatalf("replayed=%d, want >= %d record frames", replayed, wantRecords)
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

func sourceRecordCount(sources []DataSourceSummary, providerID, sourceName string) int64 {
	var total int64
	for _, s := range sources {
		if s.ProviderID == providerID && s.SourceName == sourceName {
			total += s.Count
		}
	}
	return total
}
