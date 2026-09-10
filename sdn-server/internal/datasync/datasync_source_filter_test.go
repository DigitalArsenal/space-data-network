package datasync

import (
	"fmt"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestScanSourceNameOnlyFilterReturnsAbsentAndPresentRows(t *testing.T) {
	store := newDataSyncTestStore(t)
	storeSourceFilterRecords(t, store, "source-alpha", 3, 71000)
	storeSourceFilterRecords(t, store, "source-beta", 2, 72000)

	absent, records, err := Scan(store, QueryRequest{
		Schema:     "OMM.fbs",
		SourceName: "source-absent",
		Limit:      73,
	}, MaxSyncChunkLimit)
	if err != nil {
		t.Fatalf("absent source Scan: %v", err)
	}
	if absent.TotalCount != 0 || absent.Count != 0 || len(records) != 0 || len(absent.Results) != 0 {
		t.Fatalf("absent source returned total=%d count=%d records=%d results=%d",
			absent.TotalCount, absent.Count, len(records), len(absent.Results))
	}

	present, records, err := Scan(store, QueryRequest{
		Schema:     "OMM.fbs",
		SourceName: "source-alpha",
		Limit:      73,
	}, MaxSyncChunkLimit)
	if err != nil {
		t.Fatalf("present source Scan: %v", err)
	}
	if present.TotalCount != 3 || present.Count != 3 || len(records) != 3 || len(present.Results) != 3 {
		t.Fatalf("present source returned total=%d count=%d records=%d results=%d; want 3",
			present.TotalCount, present.Count, len(records), len(present.Results))
	}
	for i, record := range records {
		if record.SourceTags.SourceName != "source-alpha" {
			t.Fatalf("present record %d has source %q", i, record.SourceTags.SourceName)
		}
	}
}

func storeSourceFilterRecords(t *testing.T, store *storage.FlatSQLStore, sourceName string, count int, noradBase uint32) {
	t.Helper()
	records := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		norad := noradBase + uint32(i)
		records = append(records, sds.NewOMMBuilder().
			WithNoradCatID(norad).
			WithObjectID(fmt.Sprintf("2026-%05d", norad)).
			WithObjectName(fmt.Sprintf("SOURCE-FILTER-%d", norad)).
			WithEpoch("2026-09-10T18:30:00Z").
			Build())
	}
	if _, err := store.StoreBatchWithSourceTags("OMM.fbs", records, "source:filter-test", nil, storage.SourceTags{
		ProviderID: "provider-" + sourceName,
		SourceName: sourceName,
		BatchID:    "batch-" + sourceName,
	}); err != nil {
		t.Fatalf("StoreBatchWithSourceTags %s: %v", sourceName, err)
	}
}
