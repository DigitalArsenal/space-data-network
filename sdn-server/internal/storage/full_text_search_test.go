package storage

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func waitFullTextIndex(t *testing.T, store *FlatSQLStore, schema string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		err := store.CheckFullTextSearch(schema, "munchen")
		if err == nil {
			return
		}
		if !errors.Is(err, ErrSearchIndexBuilding) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("search index did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func fullTextMatches(t *testing.T, store *FlatSQLStore, query string) int {
	t.Helper()
	count, err := store.CountRawRecords(RawRecordQuery{SchemaName: "CAT.fbs", Search: query})
	if err != nil {
		t.Fatal(err)
	}
	return int(count)
}

func TestFullTextIndexCoversColdRecordsAndResumes(t *testing.T) {
	path := t.TempDir()
	store := newEngineRecordsStoreWithOptions(t, path, WithEngineGenericHotWindow(2))
	t.Cleanup(func() {
		if store != nil {
			_ = store.Close()
		}
	})
	var first string
	for i := 1; i <= 205; i++ {
		name := fmt.Sprintf("Orbit object %d", i)
		if i == 1 {
			name = "München optical payload"
		}
		cid, err := store.StoreWithSourceTags("CAT.fbs", licenceTestCATRecord(uint32(i), name), "source-peer", nil, SourceTags{ProviderID: "provider-a", SourceName: "satcat", BatchID: "edition"})
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			first = cid
		}
	}
	waitFullTextIndex(t, store, "CAT.fbs")
	if n := fullTextMatches(t, store, "munchen optical"); n != 1 {
		t.Fatalf("cold record search = %d", n)
	}
	query := RawRecordQuery{SchemaName: "CAT.fbs", Search: "orbit", ProviderID: "provider-a", SourceName: "satcat", Limit: 100, Offset: 100}
	rows, err := store.QueryRawRecords(query)
	if err != nil || len(rows) != 100 {
		t.Fatalf("second full-text page = %d: %v", len(rows), err)
	}
	query.ProviderID = "other-provider"
	if n, err := store.CountRawRecords(query); err != nil || n != 0 {
		t.Fatalf("search escaped source scope: %d %v", n, err)
	}
	for _, text := range []string{`" OR 1=1 --`, "!!!", "nonexistent"} {
		if n := fullTextMatches(t, store, text); n != 0 {
			t.Fatalf("literal query %q matched %d records", text, n)
		}
	}
	if _, err := store.StoreWithSourceTags("CAT.fbs", licenceTestCATRecord(206, "München optical instrument"), "source-peer", nil, SourceTags{ProviderID: "provider-a", SourceName: "satcat", BatchID: "edition"}); err != nil {
		t.Fatal(err)
	}
	if n := fullTextMatches(t, store, "optical"); n != 2 {
		t.Fatalf("live ingest was not indexed: %d", n)
	}
	if err := store.Delete("CAT.fbs", first); err != nil {
		t.Fatal(err)
	}
	if n := fullTextMatches(t, store, "optical"); n != 1 {
		t.Fatalf("delete left stale search terms: %d", n)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	store = newEngineRecordsStoreWithOptions(t, path, WithEngineGenericHotWindow(2))
	waitFullTextIndex(t, store, "CAT.fbs")
	if n := fullTextMatches(t, store, "optical"); n != 1 {
		t.Fatalf("restart changed search results: %d", n)
	}
	state := store.fullTextState("CAT.fbs")
	state.mu.Lock()
	indexed := state.indexed
	state.mu.Unlock()
	if indexed > 1 {
		t.Fatalf("warm restart reindexed %d historical records", indexed)
	}
	// A changed schema/extractor identity must rebuild even when the previous
	// checkpoint already covered every row.
	if _, err := store.db.Exec(`UPDATE sdn_record_fts_progress SET fingerprint='previous-extractor'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE sdn_record_fts SET text='legacy-content'`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	store = newEngineRecordsStoreWithOptions(t, path, WithEngineGenericHotWindow(2))
	waitFullTextIndex(t, store, "CAT.fbs")
	if fullTextMatches(t, store, "optical") != 1 || fullTextMatches(t, store, "legacy") != 0 {
		t.Fatal("extractor change reused previously indexed text")
	}
}
