package datasync

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestSearchSnapshotExcludesNewRecordsAndRejectsWithdrawals(t *testing.T) {
	store := newDataSyncTestStore(t)
	var firstCID string
	for i := uint32(1); i <= 3; i++ {
		data := sds.NewCATBuilder().WithNoradCatID(i).WithObjectName("Optical payload").Build()
		cid, err := store.StoreWithSourceTags("CAT.fbs", data, "source", nil, storage.SourceTags{ProviderID: "provider-a", SourceName: "satcat"})
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			firstCID = cid
		}
	}
	req := QueryRequest{Schema: "CAT.fbs", Search: "optical", ProviderID: "provider-a", SourceName: "satcat", Limit: 1}
	var first *ScanResponse
	deadline := time.Now().Add(60 * time.Second)
	for {
		var err error
		first, _, err = Scan(store, req, 1000)
		if err == nil {
			break
		}
		if !errors.Is(err, storage.ErrSearchIndexBuilding) || time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if first.TotalCount != 3 || first.NextCursor == "" || first.Search != "optical" {
		t.Fatalf("first search page: %+v", first)
	}
	newData := sds.NewCATBuilder().WithNoradCatID(4).WithObjectName("Optical newcomer").Build()
	if _, err := store.StoreWithSourceTags("CAT.fbs", newData, "source", nil, storage.SourceTags{ProviderID: "provider-a", SourceName: "satcat"}); err != nil {
		t.Fatal(err)
	}
	req.Cursor, req.SnapshotID, req.Head, req.TotalCount = first.NextCursor, first.SnapshotID, first.Head, first.TotalCount
	second, _, err := Scan(store, req, 1000)
	if err != nil || second.TotalCount != 3 || second.SnapshotID != first.SnapshotID {
		t.Fatalf("new arrival changed bounded search: %+v %v", second, err)
	}
	if err := store.Delete("CAT.fbs", firstCID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Scan(store, req, 1000); err == nil || !strings.Contains(err.Error(), "search results changed") {
		t.Fatalf("withdrawal reused stale search metadata: %v", err)
	}
}

func TestSearchCursorRejectsChangedQueryBeforeReadingStore(t *testing.T) {
	req := QueryRequest{Schema: "CAT.fbs", SourceName: "satcat", ProviderID: "provider-a", Search: "optical", Limit: 100}
	filter := FilterFromRequest(req, 100, 0)
	if filter.Search != "optical" {
		t.Fatal("search was not forwarded")
	}
	req.Cursor = EncodeRawRecordCursor(100, 205, "snapshot", rawRecordQueryHash(filter, DefaultQueryProfile))
	for _, change := range []func(*QueryRequest){
		func(r *QueryRequest) { r.Search = "radio" },
		func(r *QueryRequest) { r.Search = "" },
		func(r *QueryRequest) { r.SourceName = "gcat" },
		func(r *QueryRequest) { r.ProviderID = "provider-b" },
		func(r *QueryRequest) { r.SyncFilter = "NORAD_CAT_ID = 25544" },
	} {
		changed := req
		change(&changed)
		_, _, err := Scan(&storage.FlatSQLStore{}, changed, 1000)
		if err == nil || !strings.Contains(err.Error(), "different search or data source") {
			t.Fatalf("changed query was accepted: %v", err)
		}
	}
	req.Cursor = EncodeRawRecordCursor(100, 205, "snapshot")
	if _, _, err := Scan(&storage.FlatSQLStore{}, req, 1000); err == nil {
		t.Fatal("an unbound legacy cursor was accepted for a full-text search")
	}
}
