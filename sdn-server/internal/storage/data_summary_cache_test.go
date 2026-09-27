package storage

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func dataSummarySchemaCount(t *testing.T, store *FlatSQLStore, schemaName string) int64 {
	t.Helper()
	summary, err := store.DataSummary()
	if err != nil {
		t.Fatalf("DataSummary: %v", err)
	}
	for _, sc := range summary.Schemas {
		if sc.SchemaName == schemaName {
			return sc.Count
		}
	}
	return 0
}

// A schema with rows but no source-summary lane is answered from the
// partition counters: exact on every call, and never a table scan — the 5 s
// dashboard stats lane used to hold the engine 0.4–0.9 s per such schema.
func TestDataSummaryCountsAnUnsummarizedSchemaWithoutScanning(t *testing.T) {
	store := openBootStore(t, filepath.Join(t.TempDir(), "store"), bootTestValidator(t))
	defer store.Close()

	for i, norad := range []uint32{25544, 40909} {
		data := sds.NewOMMBuilder().WithNoradCatID(norad).WithObjectName("SAT").Build()
		if _, err := store.StoreRoutedByProducer("OMM.fbs", data, "peerA", nil); err != nil {
			t.Fatalf("StoreRoutedByProducer() error = %v", err)
		}
		// No source-summary lane for this schema.
		if _, err := store.db.Exec(`DELETE FROM sdn_record_source_summary WHERE schema_name = 'OMM.fbs'`); err != nil {
			t.Fatalf("clear summary: %v", err)
		}
		if got := dataSummarySchemaCount(t, store, "OMM.fbs"); got != int64(i+1) {
			t.Fatalf("count after %d record(s) = %d", i+1, got)
		}
	}
	store.unsummarizedMu.Lock()
	_, scanned := store.unsummarizedCounts["OMM.fbs"]
	store.unsummarizedMu.Unlock()
	if scanned {
		t.Fatal("DataSummary scanned a schema the partition counters already cover")
	}
}

// While a store written before the counters existed is still being counted,
// the fallback is the table scan, at most once per TTL: the 5 s dashboard
// stats lane must not rescan the table on every call.
func TestDataSummaryScansAnUnsummarizedSchemaAtMostOncePerTTL(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	v := bootTestValidator(t)
	store := openBootStore(t, basePath, v)
	data := sds.NewOMMBuilder().WithNoradCatID(25544).WithObjectName("ISS").Build()
	if _, err := store.StoreRoutedByProducer("OMM.fbs", data, "peerA", nil); err != nil {
		t.Fatalf("StoreRoutedByProducer() error = %v", err)
	}
	stripPartitionCountersForTest(t, store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	partitionRebuildStartPaused = true
	t.Cleanup(func() { partitionRebuildStartPaused = false })
	store = openBootStore(t, basePath, v)
	defer store.Close()

	// No source-summary lane for this schema: force the fallback path.
	if _, err := store.db.Exec(`DELETE FROM sdn_record_source_summary WHERE schema_name = 'OMM.fbs'`); err != nil {
		t.Fatalf("clear summary: %v", err)
	}
	if got := dataSummarySchemaCount(t, store, "OMM.fbs"); got != 1 {
		t.Fatalf("scanned count = %d, want 1", got)
	}
	// A second record within the TTL is invisible to the cached scan …
	data2 := sds.NewOMMBuilder().WithNoradCatID(40909).WithObjectName("TWO").Build()
	if _, err := store.StoreRoutedByProducer("OMM.fbs", data2, "peerA", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM sdn_record_source_summary WHERE schema_name = 'OMM.fbs'`); err != nil {
		t.Fatal(err)
	}
	if got := dataSummarySchemaCount(t, store, "OMM.fbs"); got != 1 {
		t.Fatalf("count within TTL = %d, want the cached 1", got)
	}
	// … and visible once the entry has aged out.
	store.unsummarizedMu.Lock()
	c := store.unsummarizedCounts["OMM.fbs"]
	c.at = time.Now().Add(-2 * unsummarizedCountTTL)
	store.unsummarizedCounts["OMM.fbs"] = c
	store.unsummarizedMu.Unlock()
	if got := dataSummarySchemaCount(t, store, "OMM.fbs"); got != 2 {
		t.Fatalf("count after TTL = %d, want 2", got)
	}
}
