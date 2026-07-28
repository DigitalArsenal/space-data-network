package sourcemetrics

import (
	"testing"
	"time"
)

// landIngest books one successful pull for a source, the way the storage
// ingest cap does.
func landIngest(t *testing.T, store *Store, appID, provider, source string, records, inserted int) {
	t.Helper()
	store.RecordIngest(Ingest{
		AppID:      appID,
		ProviderID: provider,
		SourceName: source,
		SourceURL:  "https://celestrak.org/pub/" + source,
		Schema:     "CAT.fbs",
		BatchID:    "batch-" + source,
		PullBytes:  1024,
		Records:    records,
		Inserted:   inserted,
		At:         time.Now().Add(-30 * time.Minute),
	})
}

// A ledger that outlived its record store is the defect this exists for: a node
// migration copied source-metrics.db and not the records, so the ledger claims
// 68k rows the node cannot serve and the debounce gate holds the lane shut.
func TestReconcileWithdrawsClaimsTheStoreCannotCorroborate(t *testing.T) {
	store := openLedger(t)
	landIngest(t, store, "flow.satcat", "space-data-network-02", "celestrak-satcat-csv", 70122, 68127)

	before := sourceByName(t, store, "celestrak-satcat-csv")
	if before.LastRetrievedAt == nil {
		t.Fatal("precondition: the row should carry a successful pull")
	}

	// The store holds nothing at all for this source.
	invalidated, err := store.ReconcileAgainstStore(map[string]int64{})
	if err != nil {
		t.Fatalf("ReconcileAgainstStore: %v", err)
	}
	if len(invalidated) != 1 {
		t.Fatalf("invalidated %d row(s), want 1: %+v", len(invalidated), invalidated)
	}
	got := invalidated[0]
	if got.SourceID != "space-data-network-02/celestrak-satcat-csv" {
		t.Fatalf("invalidated source_id = %q", got.SourceID)
	}
	if got.AppID != "flow.satcat" {
		t.Fatalf("invalidated app_id = %q", got.AppID)
	}
	// The report names the claim so an operator sees which pull was withdrawn.
	if got.ClaimedRecords != 70122 || got.ClaimedInserted != 68127 {
		t.Fatalf("claim reported as %d/%d, want 70122/68127", got.ClaimedRecords, got.ClaimedInserted)
	}
	if got.LastRetrievedAt == nil {
		t.Fatal("the withdrawn claim should still report the timestamp it carried")
	}

	after := sourceByName(t, store, "celestrak-satcat-csv")
	if after.LastRetrievedAt != nil {
		t.Fatalf("last_retrieved_at survived invalidation: %v", after.LastRetrievedAt)
	}
	if after.LastRecords != 0 || after.LastInserted != 0 || after.IngestCount != 0 {
		t.Fatalf("success totals survived invalidation: records=%d inserted=%d ingests=%d",
			after.LastRecords, after.LastInserted, after.IngestCount)
	}
	if after.LastBatchID != "" {
		t.Fatalf("batch identity survived invalidation: %q", after.LastBatchID)
	}
	if !after.Invalidated || after.InvalidatedAt == nil || after.InvalidatedReason == "" {
		t.Fatalf("the row is not marked invalid: %+v", after)
	}
	// Facts that are still TRUE are kept: this node really did fetch that URL.
	if after.SourceURL == "" {
		t.Fatal("source_url should survive invalidation")
	}
	if after.AppID != "flow.satcat" {
		t.Fatalf("app_id should survive invalidation, got %q", after.AppID)
	}
	if after.FetchCount == 0 {
		t.Fatal("fetch_count should survive invalidation")
	}
}

// The dangerous direction. A healthy ledger must be left alone — invalidating it
// would send this node back to a publisher it does not owe a pull.
func TestReconcileLeavesACorroboratedLedgerUntouched(t *testing.T) {
	store := openLedger(t)
	landIngest(t, store, "flow.satcat", "space-data-network-02", "celestrak-satcat-csv", 70122, 68127)
	before := sourceByName(t, store, "celestrak-satcat-csv")

	invalidated, err := store.ReconcileAgainstStore(map[string]int64{
		"space-data-network-02/celestrak-satcat-csv": 68127,
	})
	if err != nil {
		t.Fatalf("ReconcileAgainstStore: %v", err)
	}
	if len(invalidated) != 0 {
		t.Fatalf("invalidated a corroborated row: %+v", invalidated)
	}

	after := sourceByName(t, store, "celestrak-satcat-csv")
	if after.Invalidated {
		t.Fatal("a corroborated row was marked invalid")
	}
	if after.LastRetrievedAt == nil || !after.LastRetrievedAt.Equal(*before.LastRetrievedAt) {
		t.Fatalf("last_retrieved_at changed: %v -> %v", before.LastRetrievedAt, after.LastRetrievedAt)
	}
	if after.LastRecords != before.LastRecords || after.LastInserted != before.LastInserted {
		t.Fatalf("totals changed: %d/%d -> %d/%d",
			before.LastRecords, before.LastInserted, after.LastRecords, after.LastInserted)
	}
	if after.IngestCount != before.IngestCount || after.LastBatchID != before.LastBatchID {
		t.Fatalf("batch identity changed: %+v -> %+v", before, after)
	}
}

// Partial evidence is still evidence: one source's records surviving does not
// license withdrawing another source's claim, and vice versa.
func TestReconcileIsPerSource(t *testing.T) {
	store := openLedger(t)
	landIngest(t, store, "flow.satcat", "space-data-network-02", "celestrak-satcat-csv", 70122, 68127)
	landIngest(t, store, "flow.spw", "space-data-network-02", "celestrak-space-weather", 25363, 12115)

	invalidated, err := store.ReconcileAgainstStore(map[string]int64{
		"space-data-network-02/celestrak-space-weather": 12115,
	})
	if err != nil {
		t.Fatalf("ReconcileAgainstStore: %v", err)
	}
	if len(invalidated) != 1 || invalidated[0].SourceID != "space-data-network-02/celestrak-satcat-csv" {
		t.Fatalf("wrong rows invalidated: %+v", invalidated)
	}
	if sourceByName(t, store, "celestrak-space-weather").Invalidated {
		t.Fatal("a source whose records ARE held was invalidated")
	}
	if !sourceByName(t, store, "celestrak-satcat-csv").Invalidated {
		t.Fatal("a source whose records are missing was not invalidated")
	}
}

// The pacing a publisher is owed is a limit on ATTEMPTS, and this node really
// did attempt. Attempt stamps must survive invalidation or every migration
// turns into a fetch storm.
func TestAttemptGatingSurvivesInvalidation(t *testing.T) {
	store := openLedger(t)
	store.RecordAttempt("flow.satcat")
	landIngest(t, store, "flow.satcat", "space-data-network-02", "celestrak-satcat-csv", 70122, 68127)
	// A second attempt that has not yet landed anything.
	store.RecordAttempt("flow.satcat")

	attemptBefore, failuresBefore := store.AttemptState("flow.satcat")
	if attemptBefore == nil {
		t.Fatal("precondition: an attempt should be stamped")
	}

	if _, err := store.ReconcileAgainstStore(map[string]int64{}); err != nil {
		t.Fatalf("ReconcileAgainstStore: %v", err)
	}

	attemptAfter, failuresAfter := store.AttemptState("flow.satcat")
	if attemptAfter == nil {
		t.Fatal("the attempt stamp was destroyed by invalidation; a restart storm can now hammer the publisher")
	}
	if !attemptAfter.Equal(*attemptBefore) {
		t.Fatalf("attempt stamp moved: %v -> %v", attemptBefore, attemptAfter)
	}
	if failuresAfter != failuresBefore {
		t.Fatalf("failure streak changed: %d -> %d", failuresBefore, failuresAfter)
	}
}

// A source that recovers must stop reading as invalid, or the $APPS feed keeps
// reporting a fault that has been fixed.
func TestASuccessfulIngestLiftsTheInvalidation(t *testing.T) {
	store := openLedger(t)
	landIngest(t, store, "flow.satcat", "space-data-network-02", "celestrak-satcat-csv", 70122, 68127)
	if _, err := store.ReconcileAgainstStore(map[string]int64{}); err != nil {
		t.Fatalf("ReconcileAgainstStore: %v", err)
	}
	if !sourceByName(t, store, "celestrak-satcat-csv").Invalidated {
		t.Fatal("precondition: the row should be invalid")
	}

	landIngest(t, store, "flow.satcat", "space-data-network-02", "celestrak-satcat-csv", 70200, 70200)

	after := sourceByName(t, store, "celestrak-satcat-csv")
	if after.Invalidated || after.InvalidatedReason != "" {
		t.Fatalf("a recovered source is still flagged invalid: %+v", after)
	}
	if after.LastRetrievedAt == nil || after.LastInserted != 70200 {
		t.Fatalf("the recovering pull was not recorded: %+v", after)
	}
}

// Reconciliation runs on every boot, so it must not re-report a row it has
// already withdrawn.
func TestReconcileDoesNotReReportAWithdrawnClaim(t *testing.T) {
	store := openLedger(t)
	landIngest(t, store, "flow.satcat", "space-data-network-02", "celestrak-satcat-csv", 70122, 68127)

	first, err := store.ReconcileAgainstStore(map[string]int64{})
	if err != nil {
		t.Fatalf("ReconcileAgainstStore: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first pass invalidated %d row(s), want 1", len(first))
	}
	second, err := store.ReconcileAgainstStore(map[string]int64{})
	if err != nil {
		t.Fatalf("ReconcileAgainstStore (second pass): %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second pass re-reported %d withdrawn row(s)", len(second))
	}
}

// A row that never claimed a successful ingest — a PNM projection, say — has
// nothing to withdraw and must not be touched.
func TestReconcileIgnoresRowsWithNoSuccessClaim(t *testing.T) {
	store := openLedger(t)
	store.RecordPNM("space-data-network-02", "celestrak-gp", PNM{
		ID:          "pnm-1",
		CID:         "bafkreiexample",
		Schema:      "OMM.fbs",
		RecordCount: 10,
		PublishedAt: time.Now(),
	})

	invalidated, err := store.ReconcileAgainstStore(map[string]int64{})
	if err != nil {
		t.Fatalf("ReconcileAgainstStore: %v", err)
	}
	if len(invalidated) != 0 {
		t.Fatalf("invalidated a row that never claimed an ingest: %+v", invalidated)
	}
	if sourceByName(t, store, "celestrak-gp").Invalidated {
		t.Fatal("a PNM-only row was marked invalid")
	}
}
