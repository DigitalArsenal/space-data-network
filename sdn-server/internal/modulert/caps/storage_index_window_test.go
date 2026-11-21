package caps

import (
	"fmt"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	"github.com/spacedatanetwork/sdn-server/internal/datasync"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

// The catalog builder's per-object history read (stack #1020), through the
// storage.index_page hostcall the flow calls: one object's sets in a 14-day
// window of a history source, oldest first (SORT_FIELD EPOCH) or newest first
// (SORT_DESC), limited, over a real format-4 store whose source holds one batch
// per day and other objects interleaved.
func TestStorageIndexWindowPageReadsOneObjectsHistoryInEpochOrder(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewFlatSQLStore(t.TempDir(), validator)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	t0 := time.Date(2026, 11, 21, 6, 0, 0, 0, time.UTC)
	put := func(norad uint32, epoch time.Time, tags storage.SourceTags) {
		t.Helper()
		stamp := epoch.Format("2006-01-02T15:04:05.000000Z")
		omm := sds.NewOMMBuilder().WithNoradCatID(norad).WithObjectName(fmt.Sprintf("SAT-%d", norad)).
			WithEpoch(stamp).WithEpochTimestamp(float64(epoch.Unix())).Build()
		if _, err := store.StoreWithSourceTags("OMM.fbs", omm, "peer-window", nil, tags); err != nil {
			t.Fatal(err)
		}
	}
	// 20 daily sets of each object, one batch per day; the other object first in each batch.
	for day := 20; day >= 0; day-- {
		tags := storage.SourceTags{ProviderID: "catalog-inputs", SourceName: "history", BatchID: fmt.Sprintf("day-%02d", day)}
		at := t0.Add(-time.Duration(day) * 24 * time.Hour)
		put(43013, at.Add(time.Hour), tags)
		put(25544, at, tags)
	}
	// The same object in another source must not enter the window.
	put(25544, t0.Add(-36*time.Hour), storage.SourceTags{ProviderID: "catalog-inputs", SourceName: "public-gp", BatchID: "newest"})

	bridge := indexBridge(store, "storage_query")
	from := t0.Add(-14 * 24 * time.Hour)
	req := syncwire.QRPFields{Kind: syncwire.QRPKindRequest, SchemaName: "OMM.fbs", ProviderID: "catalog-inputs", SourceName: "history",
		Filters:     []syncwire.QRPFilterField{{Field: "NORAD_CAT_ID", Op: 0, Value: "25544"}},
		FromEpochMs: from.UnixMilli(), ToEpochMs: t0.UnixMilli(), Limit: 64, MaxBytes: 8 << 20}
	epochs := func(rows [][]byte) []time.Time {
		t.Helper()
		var out []time.Time
		for _, raw := range rows {
			o := OMM.GetRootAsOMM(raw, 0)
			if o.NORAD_CAT_ID() != 25544 {
				t.Fatalf("another object entered the window: %d", o.NORAD_CAT_ID())
			}
			at, err := time.Parse("2006-01-02T15:04:05.000000Z", string(o.EPOCH()))
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, at)
		}
		return out
	}

	// Oldest first: the 14 sets of [t0-14d, t0), the window echoed, nothing more to read.
	asc := req
	asc.SortField = "EPOCH"
	_, h, rows := indexRead(t, bridge, asc)
	got := epochs(rows)
	if len(got) != 14 || h.TOTAL_COUNT() != 14 || h.TRUNCATED() || len(h.NEXT_CURSOR()) != 0 {
		t.Fatalf("ascending window: %d rows, TOTAL_COUNT %d, TRUNCATED %v, NEXT_CURSOR %q", len(got), h.TOTAL_COUNT(), h.TRUNCATED(), h.NEXT_CURSOR())
	}
	if h.FROM_EPOCH_MS() != from.UnixMilli() || h.TO_EPOCH_MS() != t0.UnixMilli() {
		t.Fatalf("window not echoed: %d..%d", h.FROM_EPOCH_MS(), h.TO_EPOCH_MS())
	}
	for i, at := range got {
		if want := from.Add(time.Duration(i) * 24 * time.Hour); !at.Equal(want) {
			t.Fatalf("ascending row %d is %s, want %s", i, at, want)
		}
	}
	// No SORT_FIELD reads the same order (the index's own).
	_, _, plain := indexRead(t, bridge, req)
	if fmt.Sprint(epochs(plain)) != fmt.Sprint(got) {
		t.Fatal("a window without SORT_FIELD is not oldest first")
	}

	// Newest first, limited: the 5 newest, TOTAL_COUNT still the window's, TRUNCATED.
	desc := asc
	desc.SortDesc, desc.Limit = true, 5
	_, h, rows = indexRead(t, bridge, desc)
	newest := epochs(rows)
	if len(newest) != 5 || h.TOTAL_COUNT() != 14 || !h.TRUNCATED() {
		t.Fatalf("descending window: %d rows, TOTAL_COUNT %d, TRUNCATED %v", len(newest), h.TOTAL_COUNT(), h.TRUNCATED())
	}
	for i, at := range newest {
		if want := t0.Add(-time.Duration(i+1) * 24 * time.Hour); !at.Equal(want) {
			t.Fatalf("descending row %d is %s, want %s", i, at, want)
		}
	}

	// Malformed or broadening requests are refused, never ignored.
	reject := func(name string, q syncwire.QRPFields) {
		t.Helper()
		if r := indexTypedCall(bridge, syncwire.BuildQRP(q)); r.StatusCode() == 0 {
			t.Fatalf("%s accepted", name)
		}
	}
	bad := asc
	bad.SortField = "CREATION_DATE"
	reject("SORT_FIELD CREATION_DATE", bad)
	bad = asc
	bad.Filters = []syncwire.QRPFilterField{{Field: "NORAD_CAT_ID", Op: 3, Value: "25544"}}
	reject("a Lte filter", bad)
	bad = asc
	bad.Filters = []syncwire.QRPFilterField{{Field: "OBJECT_NAME", Op: 0, Value: "SAT-25544"}}
	reject("an OBJECT_NAME filter", bad)
	bad = asc
	bad.Cursor = "x"
	reject("a CURSOR", bad)
	bad = asc
	bad.Page = 2
	reject("PAGE 2", bad)
	bad = asc
	bad.FromEpochMs, bad.ToEpochMs = t0.UnixMilli(), from.UnixMilli()
	reject("FROM after TO", bad)
	bad = asc
	bad.Search = "SAT"
	reject("SEARCH", bad)
	bad = asc
	bad.SchemaName = "CAT.fbs"
	reject("a schema without an epoch index", bad)
	bad = asc
	bad.Limit = 1001
	reject("LIMIT 1001", bad)
}

// Stack #1019: a page that continues a cursor takes the snapshot's count from
// the cursor instead of counting the whole source again.
func TestStorageIndexPageContinuationCarriesTheSnapshotCount(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	store := newCapTestStore(t)
	tags := storage.SourceTags{ProviderID: "a", SourceName: "x", BatchID: "b"}
	for i := 0; i < 7; i++ {
		if _, err := store.StoreWithSourceTags("CAT.fbs", indexSynthetic("CAT.fbs", i), "synthetic", nil, tags); err != nil {
			t.Fatal(err)
		}
	}
	bridge := indexBridge(store, "storage_query")
	req := syncwire.QRPFields{Kind: syncwire.QRPKindRequest, SchemaName: "CAT.fbs", ProviderID: "a", SourceName: "x", Limit: 3}
	_, h, rows := indexRead(t, bridge, req)
	if h.TOTAL_COUNT() != 7 || len(rows) != 3 {
		t.Fatalf("first page: TOTAL_COUNT %d, %d rows", h.TOTAL_COUNT(), len(rows))
	}
	next, ok, err := datasync.ParseRawRecordCursor(string(h.NEXT_CURSOR()))
	if err != nil || !ok || next.TotalCount != 7 || next.MaxRowID <= 0 {
		t.Fatalf("NEXT_CURSOR does not carry the snapshot count: %+v %v %v", next, ok, err)
	}

	// The store is asked for the count only when the page does not know it.
	page, err := store.RecordIndexSeqPage(storage.RecordIndexSeqQuery{
		RecordIndexPageQuery: storage.RecordIndexPageQuery{SchemaName: "CAT.fbs", ProviderID: "a", SourceName: "x", BatchID: "", Limit: 4},
		AfterSeq:             next.AfterRowID, ThroughSeq: next.MaxRowID, KnownTotal: 1234567})
	if err != nil || page.Total != 1234567 || len(page.Rows) != 4 {
		t.Fatalf("a continuation recounted: total %d, %d rows, %v", page.Total, len(page.Rows), err)
	}
	page, err = store.RecordIndexSeqPage(storage.RecordIndexSeqQuery{
		RecordIndexPageQuery: storage.RecordIndexPageQuery{SchemaName: "CAT.fbs", ProviderID: "a", SourceName: "x", Limit: 4},
		AfterSeq:             next.AfterRowID, ThroughSeq: next.MaxRowID})
	if err != nil || page.Total != 7 {
		t.Fatalf("a page without a known count: total %d, %v", page.Total, err)
	}

	// Walking the hostcall's cursors serves every record once, with the same TOTAL_COUNT.
	seen := len(rows)
	for cursor := string(h.NEXT_CURSOR()); cursor != ""; {
		q := req
		q.Cursor = cursor
		_, h, rows = indexRead(t, bridge, q)
		if h.TOTAL_COUNT() != 7 {
			t.Fatalf("continuation TOTAL_COUNT %d, want 7", h.TOTAL_COUNT())
		}
		seen += len(rows)
		cursor = string(h.NEXT_CURSOR())
	}
	if seen != 7 {
		t.Fatalf("cursor walk served %d records, want 7", seen)
	}
}
