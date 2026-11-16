package storage

import (
	"fmt"
	"testing"
)

func TestFormat2RecordIndexSequenceSnapshot(t *testing.T) {
	requireFormat2Engine(t)
	store := openFormat2ForTest(t, t.TempDir())
	defer store.Close()
	tags := SourceTags{ProviderID: "test-provider", SourceName: "test-source", BatchID: "test-batch"}
	add := func(i int) {
		t.Helper()
		data := f2TestCAT(uint32(900000+i), fmt.Sprintf("SYNTHETIC-%d", i), "PAYLOAD", "OPERATIONAL")
		if _, err := store.StoreWithSourceTags("CAT.fbs", data, "test-peer", nil, tags); err != nil {
			t.Fatal(err)
		}
	}
	query := RecordIndexSeqQuery{RecordIndexPageQuery: RecordIndexPageQuery{SchemaName: "CAT.fbs", ProviderID: tags.ProviderID, SourceName: tags.SourceName, BatchID: tags.BatchID, Limit: 2}}
	empty, err := store.RecordIndexSeqPage(query)
	if err != nil || empty.Total != 0 || len(empty.Rows) != 0 {
		t.Fatalf("empty page: total=%d rows=%d err=%v", empty.Total, len(empty.Rows), err)
	}
	for i := 0; i < 5; i++ {
		add(i)
	}
	first, err := store.RecordIndexSeqPage(query)
	if err != nil || first.Total != 5 || len(first.Rows) != 2 || first.Through <= 0 {
		t.Fatalf("first page: total=%d rows=%d through=%d err=%v", first.Total, len(first.Rows), first.Through, err)
	}
	add(5)
	query.ThroughSeq = first.Through
	seen := map[string]bool{}
	rows := first.Rows
	var last int64
	for {
		for _, row := range rows {
			if row.Seq <= last || row.Seq > first.Through || seen[row.CID] || row.NoradCatID == nil {
				t.Fatal("invalid sequence order, duplicate, or missing projected column")
			}
			last, seen[row.CID] = row.Seq, true
		}
		query.AfterSeq = last
		page, err := store.RecordIndexSeqPage(query)
		if err != nil || page.Total != 5 || page.Through != first.Through {
			t.Fatalf("continuation: total=%d through=%d err=%v", page.Total, page.Through, err)
		}
		if len(page.Rows) == 0 {
			break
		}
		rows = page.Rows
	}
	if len(seen) != 5 {
		t.Fatalf("snapshot returned %d records, want 5", len(seen))
	}
	query.AfterSeq = 0
	query.Offset = 1
	query.NoradLike = "900002"
	filtered, err := store.RecordIndexSeqPage(query)
	if err != nil || filtered.Total != 1 || len(filtered.Rows) != 0 {
		t.Fatalf("filtered offset: total=%d rows=%d err=%v", filtered.Total, len(filtered.Rows), err)
	}
	query.Offset = 0
	query.SourceName = "absent"
	absent, err := store.RecordIndexSeqPage(query)
	if err != nil || absent.Total != 0 || len(absent.Rows) != 0 {
		t.Fatalf("absent source: total=%d rows=%d err=%v", absent.Total, len(absent.Rows), err)
	}
}

func TestFormat2RecordIndexSequenceCountPaths(t *testing.T) {
	requireFormat2Engine(t)
	store := openFormat2ForTest(t, t.TempDir())
	defer store.Close()
	for i := 0; i < 5; i++ {
		tags := SourceTags{ProviderID: fmt.Sprintf("provider-%d", i), SourceName: "synthetic"}
		if _, err := store.StoreWithSourceTags("CAT.fbs", f2TestCAT(uint32(900000+i), fmt.Sprintf("SYNTHETIC-%d", i), "PAYLOAD", "OPERATIONAL"), "peer", nil, tags); err != nil {
			t.Fatal(err)
		}
	}
	q := RecordIndexSeqQuery{RecordIndexPageQuery: RecordIndexPageQuery{SchemaName: "CAT.fbs", Limit: 1}}
	page, err := store.RecordIndexSeqPage(q)
	if err != nil || page.Total != 5 || len(page.Rows) != 1 {
		t.Fatalf("unfiltered page: total=%d rows=%d err=%v", page.Total, len(page.Rows), err)
	}
	store.f2.counts.mu.Lock()
	counts := len(store.f2.counts.entries)
	store.f2.counts.mu.Unlock()
	if counts != 0 {
		t.Fatalf("unfiltered page ran %d window counts; want the type-head count", counts)
	}
	q.NoradLike = "900002"
	page, err = store.RecordIndexSeqPage(q)
	if err != nil || page.Total != 1 || len(page.Rows) != 1 {
		t.Fatalf("filtered page: total=%d rows=%d err=%v", page.Total, len(page.Rows), err)
	}
	store.f2.counts.mu.Lock()
	counts = len(store.f2.counts.entries)
	store.f2.counts.mu.Unlock()
	if counts != 1 {
		t.Fatalf("filtered page ran %d window counts, want one", counts)
	}

}
