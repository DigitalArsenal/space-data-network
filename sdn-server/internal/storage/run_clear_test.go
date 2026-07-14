package storage

import (
	"os"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func newRunClearTestStore(t *testing.T) *FlatSQLStore {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "flatsql-run-clear-test-*")
	if err != nil {
		t.Fatalf("create temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("create validator: %v", err)
	}
	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestClearSourceBatchKeepsRecordsSharedWithOtherBatches is the run-control
// mirror of TestSupersedeSourceBatchesKeepsRecordsSharedWithKeptBatch: clearing
// ONE batch must delete its tags + orphaned records, but a record whose CID is
// also tagged by another (kept) batch must survive with the kept batch's tag.
func TestClearSourceBatchKeepsRecordsSharedWithOtherBatches(t *testing.T) {
	store := newRunClearTestStore(t)

	shared := sds.NewOMMBuilder().WithNoradCatID(25544).WithObjectName("SHARED").Build()
	onlyOld := sds.NewOMMBuilder().WithNoradCatID(40909).WithObjectName("ONLY-OLD").Build()
	onlyNew := sds.NewOMMBuilder().WithNoradCatID(50000).WithObjectName("ONLY-NEW").Build()

	tags := func(batch string) SourceTags {
		return SourceTags{ProviderID: "spacex-starlink", SourceName: "spacex-starlink", BatchID: batch, ContentKeyID: "public"}
	}
	// old batch: shared + only-old. new batch: shared (same CID) + only-new.
	for _, rec := range [][]byte{shared, onlyOld} {
		if _, err := store.StoreWithSourceTags("OMM.fbs", rec, "peer-a", nil, tags("batch-old")); err != nil {
			t.Fatalf("store old batch record: %v", err)
		}
	}
	for _, rec := range [][]byte{shared, onlyNew} {
		if _, err := store.StoreWithSourceTags("OMM.fbs", rec, "peer-a", nil, tags("batch-new")); err != nil {
			t.Fatalf("store new batch record: %v", err)
		}
	}

	result, err := store.ClearSourceBatch("OMM.fbs", "spacex-starlink", "spacex-starlink", "batch-old")
	if err != nil {
		t.Fatalf("ClearSourceBatch failed: %v", err)
	}
	// Two tag rows go (shared's old tag + only-old's tag); ONE record is
	// orphaned (only-old) — the shared record keeps its batch-new tag.
	if result.TagsDeleted != 2 {
		t.Fatalf("TagsDeleted = %d, want 2", result.TagsDeleted)
	}
	if result.RecordsDeleted != 1 {
		t.Fatalf("RecordsDeleted = %d, want 1 (only the orphaned record)", result.RecordsDeleted)
	}

	// Progress view: batch-old is gone, batch-new intact with 2 records.
	progress, err := store.SourceBatchProgress()
	if err != nil {
		t.Fatalf("SourceBatchProgress failed: %v", err)
	}
	var sawOld bool
	var newCount int64
	for _, p := range progress {
		if p.SchemaName != "OMM.fbs" || p.SourceName != "spacex-starlink" {
			continue
		}
		if p.BatchID == "batch-old" {
			sawOld = true
		}
		if p.BatchID == "batch-new" {
			newCount = p.Count
		}
	}
	if sawOld {
		t.Fatalf("cleared batch-old still present in progress: %+v", progress)
	}
	if newCount != 2 {
		t.Fatalf("batch-new count = %d, want 2 (shared record survived)", newCount)
	}

	// Index view: the shared + only-new records remain, only-old is gone.
	rows, total, err := store.RecordIndexPage(RecordIndexPageQuery{SchemaName: "OMM.fbs", Limit: 10})
	if err != nil {
		t.Fatalf("RecordIndexPage failed: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("index total = %d rows = %d, want 2/2", total, len(rows))
	}
	for _, row := range rows {
		if row.NoradCatID != nil && *row.NoradCatID == 40909 {
			t.Fatalf("orphaned only-old record (40909) still indexed")
		}
	}

	// Idempotent: clearing again is a no-op.
	again, err := store.ClearSourceBatch("OMM.fbs", "spacex-starlink", "spacex-starlink", "batch-old")
	if err != nil {
		t.Fatalf("second ClearSourceBatch failed: %v", err)
	}
	if again.TagsDeleted != 0 || again.RecordsDeleted != 0 {
		t.Fatalf("second clear deleted rows: %+v", again)
	}
}

// TestClearSourceBatchRequiresScope proves the required-key validation.
func TestClearSourceBatchRequiresScope(t *testing.T) {
	store := newRunClearTestStore(t)
	if _, err := store.ClearSourceBatch("OMM.fbs", "", "", "batch"); err == nil {
		t.Fatalf("missing source name accepted")
	}
	if _, err := store.ClearSourceBatch("OMM.fbs", "", "src", ""); err == nil {
		t.Fatalf("missing batch id accepted")
	}
	if _, err := store.ClearSourceBatch("", "", "src", "batch"); err == nil {
		t.Fatalf("missing schema accepted")
	}
}
