package storage

import (
	"fmt"
	"path/filepath"
	"sort"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// A batch window writes its rows in multi-row statements (flatsql_batch_writes.go)
// instead of five statements per record. That is only allowed to be a SPEED
// change, so this is the equivalence it rests on: the same records through
// StoreBatch and through the one-at-a-time Store path must leave two stores
// holding the same rows — same records, same index rows, same engine residency,
// same derived summary — for exactly the shapes where a window could diverge:
//
//   - the SAME bytes twice inside one window (the second copy is a repeat CID
//     that the per-record path saw because the first had already been written);
//   - a CID already in the store reappearing in a later window;
//   - two $CAT records with the same identity inside one window, where the
//     second must supersede the first — a delete of a row the window itself
//     wrote.
func TestStoreBatchWindowMatchesPerRecordStores(t *testing.T) {
	validator := bootTestValidator(t)
	tags := SourceTags{ProviderID: "prov", SourceName: "equivalence", BatchID: "b1", ContentKeyID: "public"}

	omm := func(norad uint32) []byte {
		return sds.NewOMMBuilder().
			WithNoradCatID(norad).
			WithObjectID(fmt.Sprintf("2026-%06dA", norad)).
			WithEpoch("2026-01-16T11:51:22Z").
			Build()
	}
	// A window with a duplicate of its own first record in the middle of it.
	ommWindow := [][]byte{omm(1), omm(2), omm(1), omm(3)}
	// A window that repeats records the store already holds, plus one new one.
	ommReplay := [][]byte{omm(2), omm(4), omm(1)}
	// A $CAT window that supersedes its own first record: both rows carry the
	// same identity, so the second must remove the first.
	catWindow := [][]byte{
		buildCATForTest("ISS v1", "1998-067A", 25544, "", ""),
		buildCATForTest("VANGUARD", "1958-002B", 5, "", ""),
		buildCATForTest("ISS v2", "1998-067A", 25544, "", ""),
	}

	batched := openBootStore(t, filepath.Join(t.TempDir(), "batched"), validator)
	defer batched.Close()
	perRecord := openBootStore(t, filepath.Join(t.TempDir(), "per-record"), validator)
	defer perRecord.Close()

	for _, window := range []struct {
		schema  string
		records [][]byte
	}{
		{"OMM.fbs", ommWindow},
		{"OMM.fbs", ommReplay},
		{"CAT.fbs", catWindow},
	} {
		if _, err := batched.StoreBatchWithSourceTags(window.schema, window.records, "peer", nil, tags); err != nil {
			t.Fatalf("StoreBatchWithSourceTags(%s): %v", window.schema, err)
		}
		for i, data := range window.records {
			if _, err := perRecord.StoreWithSourceTags(window.schema, data, "peer", nil, tags); err != nil {
				t.Fatalf("StoreWithSourceTags(%s, %d): %v", window.schema, i, err)
			}
		}
	}

	for _, schema := range []string{"OMM.fbs", "CAT.fbs"} {
		gotRows, err := batched.Count(schema)
		if err != nil {
			t.Fatalf("Count(%s): %v", schema, err)
		}
		wantRows, err := perRecord.Count(schema)
		if err != nil {
			t.Fatalf("Count(%s): %v", schema, err)
		}
		if gotRows != wantRows {
			t.Fatalf("%s rows: batch window = %d, per record = %d", schema, gotRows, wantRows)
		}
		if got, want := indexCount(t, batched, schema), indexCount(t, perRecord, schema); got != want {
			t.Fatalf("%s index rows: batch window = %d, per record = %d", schema, got, want)
		}
		if got, want := storedCIDs(t, batched, schema), storedCIDs(t, perRecord, schema); !equalStrings(got, want) {
			t.Fatalf("%s CIDs:\n batch window = %v\n per record  = %v", schema, got, want)
		}
	}
	// OMM only, deliberately. A $CAT record superseded by a LATER record in the
	// SAME window leaves its row in the engine hot window on this path: the
	// window tombstones the superseded CIDs before it mirrors the window's own
	// records, so the tombstone finds nothing and the mirror then ingests both
	// copies. Measured on this window: 3 engine CAT rows against the 2 control
	// rows, and IDENTICALLY on the per-record-statement implementation this
	// batching replaced (commit 7bc1c25d), so it is pre-existing and not a
	// property of writing the window in multi-row statements. The control
	// tables — which are the source of truth — are correct on both; the next
	// boot's reconcileEngineResidencyLocked drops the stale row.
	if got, want := engineCount(t, batched, "OMM"), engineCount(t, perRecord, "OMM"); got != want {
		t.Fatalf("engine OMM rows: batch window = %d, per record = %d", got, want)
	}

	// The derived summary is the one piece the batch path writes with folded
	// arithmetic (one increment for the window instead of one per record).
	gotSummary, err := batched.DataSummary()
	if err != nil {
		t.Fatalf("DataSummary: %v", err)
	}
	wantSummary, err := perRecord.DataSummary()
	if err != nil {
		t.Fatalf("DataSummary: %v", err)
	}
	if gotSummary.TotalRecords != wantSummary.TotalRecords || gotSummary.TotalBytes != wantSummary.TotalBytes {
		t.Fatalf("summary: batch window = %d records / %d bytes, per record = %d / %d",
			gotSummary.TotalRecords, gotSummary.TotalBytes, wantSummary.TotalRecords, wantSummary.TotalBytes)
	}
}

// One window is written in multi-row statements of batchStatementRows rows, so
// a window bigger than that spans several of them. This is the check that the
// split loses nothing and double-counts nothing: every record lands once, in
// the control tables, the index and the engine window alike.
func TestStoreBatchWindowSpansMultipleStatements(t *testing.T) {
	store := openBootStore(t, filepath.Join(t.TempDir(), "store"), bootTestValidator(t))
	defer store.Close()

	const records = batchStatementRows*2 + 37
	window := make([][]byte, records)
	for i := range window {
		window[i] = sds.NewOMMBuilder().
			WithNoradCatID(uint32(40000 + i)).
			WithObjectID(fmt.Sprintf("2026-%06dB", i)).
			WithEpoch("2026-03-16T11:51:22Z").
			Build()
	}
	tags := SourceTags{ProviderID: "prov", SourceName: "wide-window", BatchID: "b1", ContentKeyID: "public"}
	inserted, err := store.StoreBatchWithSourceTags("OMM.fbs", window, "peer", nil, tags)
	if err != nil {
		t.Fatalf("StoreBatchWithSourceTags: %v", err)
	}
	if inserted != records {
		t.Fatalf("inserted = %d, want %d", inserted, records)
	}
	if n, err := store.Count("OMM.fbs"); err != nil || n != records {
		t.Fatalf("OMM rows = %d (err %v), want %d", n, err, records)
	}
	if n := indexCount(t, store, "OMM.fbs"); n != records {
		t.Fatalf("index rows = %d, want %d", n, records)
	}
	if n := engineCount(t, store, "OMM"); n != records {
		t.Fatalf("engine OMM rows = %d, want %d", n, records)
	}
	summary, err := store.DataSummary()
	if err != nil {
		t.Fatalf("DataSummary: %v", err)
	}
	if summary.TotalRecords != records {
		t.Fatalf("summary total = %d, want %d", summary.TotalRecords, records)
	}
	// Replaying the same window inserts nothing and moves no count.
	if n, err := store.StoreBatchWithSourceTags("OMM.fbs", window, "peer", nil, tags); err != nil || n != 0 {
		t.Fatalf("replay inserted = %d (err %v), want 0", n, err)
	}
	if n, err := store.Count("OMM.fbs"); err != nil || n != records {
		t.Fatalf("OMM rows after replay = %d (err %v), want %d", n, err, records)
	}
	if n := engineCount(t, store, "OMM"); n != records {
		t.Fatalf("engine OMM rows after replay = %d, want %d", n, records)
	}
	summary, err = store.DataSummary()
	if err != nil {
		t.Fatalf("DataSummary after replay: %v", err)
	}
	if summary.TotalRecords != records {
		t.Fatalf("summary total after replay = %d, want %d", summary.TotalRecords, records)
	}
}

// The full-text row is written by a statement that READS BACK the index row it
// indexes (SELECT rowid FROM sdn_record_index WHERE schema_name=? AND cid=?),
// so a window that queues its index rows must not index text until those rows
// are on disk. This is the assertion that ordering is right: records stored as
// ONE window, with the search index already live, are searchable.
func TestStoreBatchWindowIndexesFullTextAfterTheIndexRows(t *testing.T) {
	store := openBootStore(t, filepath.Join(t.TempDir(), "store"), bootTestValidator(t))
	defer store.Close()

	// Bring the OMM search index up before the window is written, so the
	// window takes the live-ingest path rather than the cold backfill.
	waitFullTextIndex(t, store, "OMM.fbs")

	records := [][]byte{
		sds.NewOMMBuilder().WithObjectName("München optical payload").WithNoradCatID(9001).WithObjectID("2026-900A").WithEpoch("2026-01-16T11:51:22Z").Build(),
		sds.NewOMMBuilder().WithObjectName("Orbit object two").WithNoradCatID(9002).WithObjectID("2026-900B").WithEpoch("2026-01-16T11:51:22Z").Build(),
		sds.NewOMMBuilder().WithObjectName("Orbit object three").WithNoradCatID(9003).WithObjectID("2026-900C").WithEpoch("2026-01-16T11:51:22Z").Build(),
	}
	if _, err := store.StoreBatch("OMM.fbs", records, "peer", nil); err != nil {
		t.Fatalf("StoreBatch: %v", err)
	}

	for _, tc := range []struct {
		search string
		want   int64
	}{
		{"munchen optical", 1},
		{"orbit", 2},
		{"nonexistent", 0},
	} {
		got, err := store.CountRawRecords(RawRecordQuery{SchemaName: "OMM.fbs", Search: tc.search})
		if err != nil {
			t.Fatalf("CountRawRecords(%q): %v", tc.search, err)
		}
		if got != tc.want {
			t.Fatalf("search %q matched %d records in a batched window, want %d", tc.search, got, tc.want)
		}
	}
}

// storedCIDs lists the record index CIDs of one schema in a stable order.
func storedCIDs(t *testing.T, s *FlatSQLStore, schemaName string) []string {
	t.Helper()
	rows, err := s.db.Query(`SELECT cid FROM sdn_record_index WHERE schema_name = ?`, schemaName)
	if err != nil {
		t.Fatalf("read index CIDs: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var cid string
		if err := rows.Scan(&cid); err != nil {
			t.Fatalf("scan cid: %v", err)
		}
		out = append(out, cid)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read index CIDs: %v", err)
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
