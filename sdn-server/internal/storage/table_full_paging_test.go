package storage

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
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
		SchemaName:    "OMM.fbs",
		Limit:         2,
		Offset:        4,
		IncludeSource: true,
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

func TestFullTableCandidatesUseRowIDSeekAndNoCorrelatedSourceLookup(t *testing.T) {
	plain, _ := fullTableCandidatesSQL("sds_p_test__OMM", "OMM.fbs", "", math.MaxInt64, 2000, 0)
	for _, banned := range []string{"SELECT tags.source_name", "EXISTS (", "sdn_record_source_tags"} {
		if strings.Contains(plain, banned) {
			t.Fatalf("plain candidates SQL contains %q:\n%s", banned, plain)
		}
	}
	if !strings.Contains(plain, "WHERE records.rowid < ?") || !strings.Contains(plain, "LIMIT ?") || strings.Contains(plain, "OFFSET") {
		t.Fatalf("plain candidates SQL is not rowid-cursor paged:\n%s", plain)
	}

	filtered, _ := fullTableCandidatesSQL("sds_p_test__OMM", "OMM.fbs", "catalog", math.MaxInt64, 2000, 0)
	if !strings.Contains(filtered, "JOIN sdn_record_source_tags filter_tags") {
		t.Fatalf("source filter does not use one join:\n%s", filtered)
	}
	for _, banned := range []string{"EXISTS (", "SELECT tags.source_name"} {
		if strings.Contains(filtered, banned) {
			t.Fatalf("source-filter candidates SQL contains %q:\n%s", banned, filtered)
		}
	}
}

func TestFullTablePageOffsetFallbackIsAPlainRoutedRowIDWalk(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	for i := 0; i < 4; i++ {
		record := sds.NewOMMBuilder().
			WithNoradCatID(uint32(42000 + i)).
			WithObjectName(fmt.Sprintf("PLAN-%02d", i)).
			WithEpoch("2026-09-02T00:00:00Z").
			Build()
		if _, err := store.StoreWithSourceTags("OMM.fbs", record, "source:plan", nil,
			SourceTags{ProviderID: "plan", SourceName: "plan", BatchID: "b1"}); err != nil {
			t.Fatalf("store OMM %d: %v", i, err)
		}
	}

	store.mu.RLock()
	tables, err := store.fullTableReadTablesLocked("OMM.fbs")
	store.mu.RUnlock()
	if err != nil || len(tables) != 1 {
		t.Fatalf("routed tables = %v, %v; want one table", tables, err)
	}
	statement, args := fullTableCandidatesSQL(tables[0], "OMM.fbs", "", math.MaxInt64, 1000, 20000)
	rows, err := store.db.Query("EXPLAIN QUERY PLAN "+statement, args...)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan = append(plan, detail)
	}
	joined := strings.Join(plan, "\n")
	t.Logf("OFFSET fallback plan:\n%s", joined)
	if strings.Contains(joined, "USE TEMP B-TREE") || strings.Contains(joined, "SCAN records") {
		t.Fatalf("OFFSET fallback is not a plain routed index walk:\n%s", joined)
	}
	if !strings.Contains(joined, "SEARCH records USING INTEGER PRIMARY KEY (rowid<?)") {
		t.Fatalf("OFFSET fallback lacks the routed rowid seek:\n%s", joined)
	}
}

func TestFullTablePageBatchesNewestSourceNameAndCanSkipIt(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	record := sds.NewOMMBuilder().
		WithNoradCatID(43000).
		WithObjectName("SOURCE-ORDER").
		WithEpoch("2026-09-02T00:00:00Z").
		Build()
	cid, err := store.StoreWithSourceTags("OMM.fbs", record, "source:old", nil,
		SourceTags{ProviderID: "old", SourceName: "old", BatchID: "b1"})
	if err != nil {
		t.Fatalf("store OMM: %v", err)
	}
	if _, err := store.db.Exec(`
		UPDATE sdn_record_source_tags SET created_at = 1
		WHERE schema_name = ? AND cid = ?
	`, "OMM.fbs", cid); err != nil {
		t.Fatalf("age first source tag: %v", err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO sdn_record_source_tags (
			schema_name, cid, provider_id, source_name, source_url, batch_id,
			content_key_id, producer_peer_id, producer_public_key, created_at
		) VALUES (?, ?, 'new', 'newest', '', 'b2', '', 'new', '', 2)
	`, "OMM.fbs", cid); err != nil {
		t.Fatalf("insert newer source tag: %v", err)
	}

	page, err := store.FullTablePage(FullTablePageQuery{
		SchemaName:    "OMM.fbs",
		Limit:         1,
		IncludeSource: true,
	})
	if err != nil {
		t.Fatalf("FullTablePage with source: %v", err)
	}
	if len(page) != 1 || page[0].SourceTags.SourceName != "newest" {
		t.Fatalf("newest source projection = %#v", page)
	}

	if _, err := store.db.Exec(`DROP TABLE sdn_record_source_tags`); err != nil {
		t.Fatalf("drop source tags: %v", err)
	}
	page, err = store.FullTablePage(FullTablePageQuery{SchemaName: "OMM.fbs", Limit: 1})
	if err != nil {
		t.Fatalf("FullTablePage without requested source must skip lookup: %v", err)
	}
	if len(page) != 1 || page[0].SourceTags.SourceName != "" {
		t.Fatalf("skipped source projection = %#v", page)
	}
}

func TestFullTablePageMergesProducerTablesWithIndependentCursors(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	want := make([]string, 0, 8)
	for i := 0; i < 8; i++ {
		producer := "producer-a"
		if i%2 == 1 {
			producer = "producer-b"
		}
		record := sds.NewOMMBuilder().
			WithNoradCatID(uint32(44000 + i)).
			WithObjectName(fmt.Sprintf("MERGE-%02d", i)).
			WithEpoch("2026-09-02T00:00:00Z").
			Build()
		cid, err := store.StoreWithSourceTags("OMM.fbs", record, producer, nil,
			SourceTags{ProviderID: producer, ProducerPeerID: producer, SourceName: "merge", BatchID: "b1"})
		if err != nil {
			t.Fatalf("store OMM %d: %v", i, err)
		}
		want = append([]string{cid}, want...)
	}

	var cursor FullTablePageCursor
	var got []string
	for {
		page, err := store.FullTablePageWithCursor(FullTablePageQuery{
			SchemaName: "OMM.fbs",
			Limit:      3,
			Cursor:     cursor,
		})
		if err != nil {
			t.Fatalf("FullTablePageWithCursor: %v", err)
		}
		for _, record := range page.Records {
			got = append(got, record.CID)
		}
		if len(page.Records) < 3 {
			break
		}
		cursor = page.NextCursor
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("merged cursor order = %v, want %v", got, want)
	}

	offsetPage, err := store.FullTablePage(FullTablePageQuery{SchemaName: "OMM.fbs", Limit: 3, Offset: 3})
	if err != nil {
		t.Fatalf("multi-table OFFSET fallback: %v", err)
	}
	if len(offsetPage) != 3 || offsetPage[0].CID != want[3] || offsetPage[2].CID != want[5] {
		t.Fatalf("multi-table OFFSET fallback = %#v, want %v", offsetPage, want[3:6])
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
