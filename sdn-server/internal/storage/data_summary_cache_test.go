package storage

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// A schema with rows but no source-summary lane is counted by table scan at
// most once per TTL: the 5 s dashboard stats lane must not rescan the table
// on every call while a catalog replay is still filling it.
func TestDataSummaryScansAnUnsummarizedSchemaAtMostOncePerTTL(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	data := sds.NewOMMBuilder().WithNoradCatID(25544).WithObjectName("ISS").Build()
	if _, err := store.StoreRoutedByProducer("OMM.fbs", data, "peerA", nil); err != nil {
		t.Fatalf("StoreRoutedByProducer() error = %v", err)
	}
	// No source-summary lane for this schema: force the fallback path.
	if _, err := store.db.Exec(`DELETE FROM sdn_record_source_summary WHERE schema_name = 'OMM.fbs'`); err != nil {
		t.Fatalf("clear summary: %v", err)
	}

	ommCount := func() int64 {
		summary, err := store.DataSummary()
		if err != nil {
			t.Fatalf("DataSummary: %v", err)
		}
		for _, sc := range summary.Schemas {
			if sc.SchemaName == "OMM.fbs" {
				return sc.Count
			}
		}
		return 0
	}
	if got := ommCount(); got != 1 {
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
	if got := ommCount(); got != 1 {
		t.Fatalf("count within TTL = %d, want the cached 1", got)
	}
	// … and visible once the entry has aged out.
	store.unsummarizedMu.Lock()
	c := store.unsummarizedCounts["OMM.fbs"]
	c.at = time.Now().Add(-2 * unsummarizedCountTTL)
	store.unsummarizedCounts["OMM.fbs"] = c
	store.unsummarizedMu.Unlock()
	if got := ommCount(); got != 2 {
		t.Fatalf("count after TTL = %d, want 2", got)
	}
}
