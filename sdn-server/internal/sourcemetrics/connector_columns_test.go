package sourcemetrics

import (
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqldrv"
)

// The $ICN connector plane reads three origin columns off source_metrics and
// two validator columns off fetch_events. Both tables predate them, so they
// must be ADDED to an existing ledger — CREATE TABLE IF NOT EXISTS never
// touches a table that already exists.

func TestConnectorColumnsAppearOnAnExistingLedger(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DBFileName)

	// An older ledger: both tables in their original shape.
	db, closer, err := flatsqldrv.OpenStandalone(path)
	if err != nil {
		t.Fatalf("open legacy ledger: %v", err)
	}
	for _, ddl := range []string{
		`CREATE TABLE source_metrics (
			source_id TEXT PRIMARY KEY, app_id TEXT NOT NULL DEFAULT '', provider_id TEXT NOT NULL DEFAULT '',
			source_name TEXT NOT NULL DEFAULT '', source_url TEXT NOT NULL DEFAULT '',
			last_retrieved_at INTEGER NOT NULL DEFAULT 0, debounce_hours REAL NOT NULL DEFAULT 0,
			last_pull_size_bytes INTEGER NOT NULL DEFAULT 0, last_status INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '', last_duration_ms INTEGER NOT NULL DEFAULT 0,
			last_batch_id TEXT NOT NULL DEFAULT '', last_batch_repeated INTEGER NOT NULL DEFAULT 0,
			last_fetch_seq INTEGER NOT NULL DEFAULT 0, last_schemas TEXT NOT NULL DEFAULT '',
			last_records INTEGER NOT NULL DEFAULT 0, last_inserted INTEGER NOT NULL DEFAULT 0,
			fetch_count INTEGER NOT NULL DEFAULT 0, ingest_count INTEGER NOT NULL DEFAULT 0,
			last_pnm_id TEXT NOT NULL DEFAULT '', last_pnm_cid TEXT NOT NULL DEFAULT '',
			last_pnm_schema TEXT NOT NULL DEFAULT '', last_pnm_feed_head TEXT NOT NULL DEFAULT '',
			last_pnm_records INTEGER NOT NULL DEFAULT 0, last_pnm_at INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE fetch_events (
			url TEXT PRIMARY KEY, last_status INTEGER NOT NULL DEFAULT 0, last_bytes INTEGER NOT NULL DEFAULT 0,
			last_duration_ms INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '',
			last_at INTEGER NOT NULL DEFAULT 0, fetch_count INTEGER NOT NULL DEFAULT 0)`,
		`INSERT INTO source_metrics (source_id, provider_id, source_name, source_url, last_retrieved_at)
		 VALUES ('space-data-network-02/celestrak-gp', 'space-data-network-02', 'celestrak-gp', 'https://celestrak.org/NORAD/elements/gp.php', 1700000000)`,
		`INSERT INTO fetch_events (url, last_status, fetch_count) VALUES ('https://celestrak.org/NORAD/elements/gp.php', 200, 3)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			closer()
			t.Fatalf("seed legacy ledger: %v", err)
		}
	}
	closer()

	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open upgraded ledger: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	for table, cols := range map[string][]string{
		"source_metrics": {"origin_id", "origin_name", "dataset_id"},
		"fetch_events":   {"last_etag", "last_modified"},
	} {
		have, err := store.columnSet(table)
		if err != nil {
			t.Fatalf("columnSet(%s): %v", table, err)
		}
		for _, col := range cols {
			if !have[col] {
				t.Fatalf("%s.%s missing after upgrade", table, col)
			}
		}
	}

	// The legacy rows survive and read back with empty origin / validators.
	row := sourceByName(t, store, "celestrak-gp")
	if row.OriginID != "" || row.OriginName != "" || row.DatasetID != "" {
		t.Fatalf("legacy row grew an origin from nowhere: %+v", row)
	}
	if etag, lm := store.Validators("https://celestrak.org/NORAD/elements/gp.php"); etag != "" || lm != "" {
		t.Fatalf("legacy fetch row grew validators: %q %q", etag, lm)
	}
	status, _, count, _, _, _ := store.FetchEvent("https://celestrak.org/NORAD/elements/gp.php")
	if status != 200 || count != 3 {
		t.Fatalf("FetchEvent legacy = status %d count %d", status, count)
	}
}

func TestRecordIngestPersistsOriginWithoutBlankingIt(t *testing.T) {
	store := openLedger(t)
	const url = "https://celestrak.org/SpaceData/SW-All.csv"

	store.RecordFetch(Fetch{URL: url, Status: 200, Bytes: 4096})
	store.RecordIngest(Ingest{
		AppID: "celestrak-spw-ingest", ProviderID: "space-data-network-02", SourceName: "celestrak-space-weather",
		SourceURL: url, Schema: "SPW.fbs", BatchID: "b1", Records: 10, Inserted: 10,
		OriginID: "celestrak.org", OriginName: "CelesTrak", DatasetID: "sw-all",
	})
	row := sourceByName(t, store, "celestrak-space-weather")
	if row.OriginID != "celestrak.org" || row.OriginName != "CelesTrak" || row.DatasetID != "sw-all" {
		t.Fatalf("origin not persisted: %+v", row)
	}

	// A later batch without origin meta keeps what the lane already declared.
	store.RecordFetch(Fetch{URL: url, Status: 200, Bytes: 4096})
	store.RecordIngest(Ingest{
		AppID: "celestrak-spw-ingest", ProviderID: "space-data-network-02", SourceName: "celestrak-space-weather",
		SourceURL: url, Schema: "SPW.fbs", BatchID: "b2", Records: 10, Inserted: 1,
	})
	row = sourceByName(t, store, "celestrak-space-weather")
	if row.OriginID != "celestrak.org" || row.OriginName != "CelesTrak" || row.DatasetID != "sw-all" {
		t.Fatalf("origin blanked by an ingest without meta: %+v", row)
	}
	if row.LastBatchID != "b2" || row.IngestCount != 2 {
		t.Fatalf("ingest bookkeeping regressed: %+v", row)
	}
}

func TestValidatorsReturnWhatRecordFetchStored(t *testing.T) {
	store := openLedger(t)
	const url = "https://celestrak.org/NORAD/elements/gp.php?GROUP=active&FORMAT=csv"

	// Never fetched: nothing to present.
	if etag, lm := store.Validators(url); etag != "" || lm != "" {
		t.Fatalf("validators before any fetch: %q %q", etag, lm)
	}

	store.RecordFetch(Fetch{URL: url, Status: 200, Bytes: 100, ETag: `"v1"`, LastModified: "Wed, 03 Sep 2026 12:00:00 GMT"})
	etag, lm := store.Validators(url)
	if etag != `"v1"` || lm != "Wed, 03 Sep 2026 12:00:00 GMT" {
		t.Fatalf("validators after 200 = %q %q", etag, lm)
	}

	// A 304 carries no body and must not erase the validators.
	store.RecordFetch(Fetch{URL: url, Status: 304})
	if etag, lm := store.Validators(url); etag != `"v1"` || lm == "" {
		t.Fatalf("304 blanked validators: %q %q", etag, lm)
	}
	// A failed fetch does not either.
	store.RecordFetch(Fetch{URL: url, Status: 503, Err: "upstream busy", ETag: `"ignored"`})
	if etag, _ := store.Validators(url); etag != `"v1"` {
		t.Fatalf("5xx rewrote the ETag: %q", etag)
	}
	// A new 200 replaces them.
	store.RecordFetch(Fetch{URL: url, Status: 200, ETag: `"v2"`})
	if etag, _ := store.Validators(url); etag != `"v2"` {
		t.Fatalf("second 200 did not replace the ETag: %q", etag)
	}
	// The observer path books validators for a URL the ledger already has.
	store.RecordValidators(url, `"v3"`, "")
	etag, lm = store.Validators(url)
	if etag != `"v3"` || lm != "Wed, 03 Sep 2026 12:00:00 GMT" {
		t.Fatalf("RecordValidators = %q %q", etag, lm)
	}

	status, _, count, at, gotEtag, _ := store.FetchEvent(url)
	if status != 200 || count != 4 || at.IsZero() || gotEtag != `"v3"` {
		t.Fatalf("FetchEvent = status %d count %d at %v etag %q", status, count, at, gotEtag)
	}
}
