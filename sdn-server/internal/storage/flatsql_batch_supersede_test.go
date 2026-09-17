package storage

import (
	"fmt"
	"path/filepath"
	"sort"
	"testing"
)

// flatsql_batch_supersede_test.go — the intersection the batch window's own
// test did not construct: a CID that a LATER record in the SAME window
// supersedes away, and which then reappears inside that window.
//
// The batched path probes sdn_record_index ONCE per window and then maintains
// the presence map in Go. A supersede DELETES index rows, so the map has to be
// told (forgetPresence); without that it keeps claiming a CID exists, the
// repeat branch runs, its mirror cannot find the row it was told exists, and
// the record is dropped with no error. $CAT is the only standard with a
// supersede rule, which makes this a silent last-writer-wins violation in the
// satellite catalog.
//
// Both cases assert the batched store lands EXACTLY what the per-record store
// lands — rows, index CIDs, source-tag CIDs and the folded summary.

// batchTestIndexCIDs lists the record-index CIDs of a schema, sorted.
func batchTestIndexCIDs(t *testing.T, s *FlatSQLStore, schemaName string) []string {
	t.Helper()
	return batchTestScanCIDs(t, s, `SELECT cid FROM sdn_record_index WHERE schema_name = ?`, schemaName)
}

// batchTestTagCIDs lists the source-tag CIDs of a schema, sorted.
func batchTestTagCIDs(t *testing.T, s *FlatSQLStore, schemaName string) []string {
	t.Helper()
	return batchTestScanCIDs(t, s, `SELECT cid FROM sdn_record_source_tags WHERE schema_name = ?`, schemaName)
}

func batchTestScanCIDs(t *testing.T, s *FlatSQLStore, query, schemaName string) []string {
	t.Helper()
	rows, err := s.db.Query(query, schemaName)
	if err != nil {
		t.Fatalf("scan cids: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("scan cid: %v", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("scan cids: %v", err)
	}
	sort.Strings(out)
	return out
}

// batchTestSummary returns the folded (record_count, total_bytes, max_rowid).
func batchTestSummary(t *testing.T, s *FlatSQLStore, schemaName string) (int64, int64, int64) {
	t.Helper()
	var c, b, m int64
	err := s.db.QueryRow(`SELECT COALESCE(SUM(record_count),0), COALESCE(SUM(total_bytes),0), COALESCE(MAX(max_rowid),0)
		FROM sdn_record_source_summary WHERE schema_name = ?`, schemaName).Scan(&c, &b, &m)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	return c, b, m
}

func batchTestLabels(cids []string, named map[string]string) []string {
	out := make([]string, len(cids))
	for i, c := range cids {
		if n, ok := named[c]; ok {
			out[i] = n
		} else {
			out[i] = c[:12]
		}
	}
	return out
}

func TestStoreBatchRepeatOfSupersededCIDMatchesPerRecord(t *testing.T) {
	validator := bootTestValidator(t)
	tags := SourceTags{ProviderID: "prov", SourceName: "probe", BatchID: "b1", ContentKeyID: "public"}

	// Same NORAD id, so B supersedes A within the producer table.
	a := buildCATForTest("ISS v1", "1998-067A", 25544, "", "")
	b := buildCATForTest("ISS v2", "1998-067A", 25544, "", "")
	named := map[string]string{computeCID(a): "A(ISS v1)", computeCID(b): "B(ISS v2)"}

	t.Run("seeded store, window [B,A]", func(t *testing.T) {
		batched := openBootStore(t, filepath.Join(t.TempDir(), "batched"), validator)
		defer batched.Close()
		perRec := openBootStore(t, filepath.Join(t.TempDir(), "perrec"), validator)
		defer perRec.Close()
		for _, st := range []*FlatSQLStore{batched, perRec} {
			if _, err := st.StoreWithSourceTags("CAT.fbs", a, "peer", nil, tags); err != nil {
				t.Fatalf("seed A: %v", err)
			}
		}
		requireBatchMatchesPerRecord(t, batched, perRec, [][]byte{b, a}, tags, named)
	})

	t.Run("empty store, window [A,B,A]", func(t *testing.T) {
		batched := openBootStore(t, filepath.Join(t.TempDir(), "batched"), validator)
		defer batched.Close()
		perRec := openBootStore(t, filepath.Join(t.TempDir(), "perrec"), validator)
		defer perRec.Close()
		requireBatchMatchesPerRecord(t, batched, perRec, [][]byte{a, b, a}, tags, named)
	})
}

// requireBatchMatchesPerRecord stores `window` into `batched` as ONE batch and
// into `perRec` one record at a time, then requires the two stores to hold
// identical state.
func requireBatchMatchesPerRecord(t *testing.T, batched, perRec *FlatSQLStore, window [][]byte, tags SourceTags, named map[string]string) {
	t.Helper()
	inserted, err := batched.StoreBatchWithSourceTags("CAT.fbs", window, "peer", nil, tags)
	if err != nil {
		t.Fatalf("StoreBatchWithSourceTags: %v", err)
	}
	for i, d := range window {
		if _, err := perRec.StoreWithSourceTags("CAT.fbs", d, "peer", nil, tags); err != nil {
			t.Fatalf("StoreWithSourceTags %d: %v", i, err)
		}
	}

	gotRows, _ := batched.Count("CAT.fbs")
	wantRows, _ := perRec.Count("CAT.fbs")
	gotIndex, wantIndex := batchTestIndexCIDs(t, batched, "CAT.fbs"), batchTestIndexCIDs(t, perRec, "CAT.fbs")
	gotTags, wantTags := batchTestTagCIDs(t, batched, "CAT.fbs"), batchTestTagCIDs(t, perRec, "CAT.fbs")
	gotN, gotBytes, gotMax := batchTestSummary(t, batched, "CAT.fbs")
	wantN, wantBytes, wantMax := batchTestSummary(t, perRec, "CAT.fbs")
	gotEngine, wantEngine := engineCount(t, batched, "CAT"), engineCount(t, perRec, "CAT")

	t.Logf("batch inserted=%d", inserted)
	t.Logf("  rows       batch=%d  perRecord=%d", gotRows, wantRows)
	t.Logf("  index CIDs batch=%v  perRecord=%v", batchTestLabels(gotIndex, named), batchTestLabels(wantIndex, named))
	t.Logf("  tag CIDs   batch=%v  perRecord=%v", batchTestLabels(gotTags, named), batchTestLabels(wantTags, named))
	t.Logf("  summary    batch=(n=%d bytes=%d maxrow=%d)  perRecord=(n=%d bytes=%d maxrow=%d)", gotN, gotBytes, gotMax, wantN, wantBytes, wantMax)
	t.Logf("  engine CAT batch=%d  perRecord=%d", gotEngine, wantEngine)

	if gotRows != wantRows {
		t.Errorf("row count: batch=%d per-record=%d", gotRows, wantRows)
	}
	if fmt.Sprint(gotIndex) != fmt.Sprint(wantIndex) {
		t.Errorf("record-index CIDs differ: batch=%v per-record=%v", batchTestLabels(gotIndex, named), batchTestLabels(wantIndex, named))
	}
	if fmt.Sprint(gotTags) != fmt.Sprint(wantTags) {
		t.Errorf("source-tag CIDs differ: batch=%v per-record=%v", batchTestLabels(gotTags, named), batchTestLabels(wantTags, named))
	}
	if gotN != wantN || gotBytes != wantBytes {
		t.Errorf("source summary differs: batch=(n=%d bytes=%d) per-record=(n=%d bytes=%d)", gotN, gotBytes, wantN, wantBytes)
	}
	if gotEngine != wantEngine {
		t.Errorf("engine row count: batch=%d per-record=%d", gotEngine, wantEngine)
	}
	if int(inserted) != int(wantRows) && len(window) > 0 {
		// `inserted` counts rows that LANDED; after a within-window supersede
		// the window's surviving row count is what the per-record path holds.
		t.Logf("note: inserted=%d, surviving rows=%d", inserted, wantRows)
	}
}
