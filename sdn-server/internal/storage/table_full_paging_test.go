package storage

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func TestFullTablePageUsesDurableRowsPastTheEngineWindow(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := NewFlatSQLStore(
		filepath.Join(t.TempDir(), "store"),
		validator,
		WithEngineHotWindow(2),
	)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	tags := SourceTags{ProviderID: "test", SourceName: "durable-page", BatchID: "b1", ContentKeyID: "public"}
	cids := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		record := sds.NewOMMBuilder().
			WithNoradCatID(uint32(40000 + i)).
			WithObjectName(fmt.Sprintf("DURABLE-%02d", i)).
			WithEpoch("2026-09-02T00:00:00Z").
			Build()
		cid, err := store.StoreWithSourceTags("OMM.fbs", record, "source:durable-page", nil, tags)
		if err != nil {
			t.Fatalf("store OMM %d: %v", i, err)
		}
		cids = append(cids, cid)
	}

	resident, err := store.EngineRecordCount("OMM.fbs")
	if err != nil {
		t.Fatalf("EngineRecordCount: %v", err)
	}
	if resident != 2 {
		t.Fatalf("resident rows = %d, want 2", resident)
	}
	total, err := store.CountRawRecords(RawRecordQuery{SchemaName: "OMM.fbs"})
	if err != nil {
		t.Fatalf("CountRawRecords: %v", err)
	}
	if total != 6 {
		t.Fatalf("stored rows = %d, want 6", total)
	}

	page, err := store.FullTablePage(FullTablePageQuery{
		SchemaName: "OMM.fbs",
		Limit:      2,
		Offset:     4,
	})
	if err != nil {
		t.Fatalf("FullTablePage: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("page rows = %d, want 2", len(page))
	}
	if page[0].CID != cids[1] || page[1].CID != cids[0] {
		t.Fatalf("page CIDs = [%s %s], want oldest durable rows [%s %s] newest-first",
			page[0].CID, page[1].CID, cids[1], cids[0])
	}
	if page[0].RowID <= page[1].RowID {
		t.Fatalf("row ids = [%d %d], want stable descending order", page[0].RowID, page[1].RowID)
	}
	if page[0].SourceTags.SourceName != "durable-page" || page[1].SourceTags.SourceName != "durable-page" {
		t.Fatalf("source projection = [%q %q]", page[0].SourceTags.SourceName, page[1].SourceTags.SourceName)
	}
}

func TestFullTablePageSupportsStableBoundedScanChunks(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	tags := SourceTags{ProviderID: "test", SourceName: "scan-chunks", BatchID: "b1", ContentKeyID: "public"}
	cids := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		record := sds.NewOMMBuilder().
			WithNoradCatID(uint32(41000 + i)).
			WithObjectName(fmt.Sprintf("CHUNK-%02d", i)).
			WithEpoch("2026-09-02T00:00:00Z").
			Build()
		cid, err := store.StoreWithSourceTags("OMM.fbs", record, "source:scan-chunks", nil, tags)
		if err != nil {
			t.Fatalf("store OMM %d: %v", i, err)
		}
		cids = append(cids, cid)
	}

	first, err := store.FullTablePage(FullTablePageQuery{
		SchemaName: "OMM.fbs",
		SourceName: "scan-chunks",
		Limit:      2,
	})
	if err != nil {
		t.Fatalf("first FullTablePage: %v", err)
	}
	if len(first) != 2 || first[0].CID != cids[5] || first[1].CID != cids[4] {
		t.Fatalf("first chunk = %#v, want newest two records", first)
	}

	second, err := store.FullTablePage(FullTablePageQuery{
		SchemaName:  "OMM.fbs",
		SourceName:  "scan-chunks",
		Limit:       2,
		BeforeRowID: first[len(first)-1].RowID,
	})
	if err != nil {
		t.Fatalf("second FullTablePage: %v", err)
	}
	if len(second) != 2 || second[0].CID != cids[3] || second[1].CID != cids[2] {
		t.Fatalf("second chunk = %#v, want the next two records", second)
	}
	if second[0].RowID >= first[1].RowID || second[1].RowID >= first[1].RowID {
		t.Fatalf("cursor overlap: first ends at %d, second row ids are %d and %d",
			first[1].RowID, second[0].RowID, second[1].RowID)
	}
}
