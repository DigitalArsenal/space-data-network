package storage

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// THE PAGED HOT-WINDOW READ MUST BE THE SAME ANSWER, not a similar one.
//
// The single statement it replaces held host-02's engine for 5m0.001s and
// poisoned it (rehearsal, 2026-08-27). Splitting it is only admissible if the
// rows it yields — and their ORDER, which decides the arena sequence every
// engine _rowid is derived from — are identical. This runs both against the
// same store and compares them element by element.
func TestPagedHotWindowReadReproducesTheSingleStatement(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, basePath)
	defer store.Close()

	const records = 25
	tags := SourceTags{ProviderID: "prov-page", SourceName: "paging-src", BatchID: "batch-page"}
	for i := 0; i < records; i++ {
		record := buildEngineOMM(t, uint32(9000+i), "PAGE-SAT", int64(1700000000+i))
		if _, err := store.StoreWithSourceTags("OMM.fbs", record, "peer-paging", nil, tags); err != nil {
			t.Fatalf("store record %d: %v", i, err)
		}
	}

	readSource, err := store.recordReadSource("OMM.fbs")
	if err != nil {
		t.Fatalf("recordReadSource: %v", err)
	}
	aliases := engineSchemaNameAliases("OMM.fbs")
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(aliases)), ", ")

	// The statement as it was before paging.
	args := make([]any, 0, len(aliases)+1)
	for _, alias := range aliases {
		args = append(args, alias)
	}
	args = append(args, records)
	rows, err := store.db.Query(fmt.Sprintf(`
		SELECT idx.rowid AS rid, rr.stream_path, rr.stream_offset, rr.record_length,
		       COALESCE((
		           SELECT tags.source_name FROM sdn_record_source_tags tags
		           WHERE tags.schema_name = idx.schema_name AND tags.cid = idx.cid
		           ORDER BY tags.created_at DESC LIMIT 1
		       ), '') AS source_name
		FROM sdn_record_index idx
		JOIN %s rr ON rr.cid = idx.cid
		WHERE idx.schema_name IN (%s)
		ORDER BY idx.rowid DESC
		LIMIT ?
	`, readSource, placeholders), args...)
	if err != nil {
		t.Fatalf("baseline query: %v", err)
	}
	var want []engineHotWindowRow
	for rows.Next() {
		var r engineHotWindowRow
		if err := rows.Scan(&r.rid, &r.streamPath, &r.streamOffset, &r.recordLength, &r.sourceName); err != nil {
			rows.Close()
			t.Fatalf("scan baseline: %v", err)
		}
		want = append(want, r)
	}
	rows.Close()
	if len(want) != records {
		t.Fatalf("baseline returned %d rows, want %d", len(want), records)
	}

	// A page size that forces several pages AND a short final page.
	restore := engineHotWindowRebuildPage
	engineHotWindowRebuildPage = 7
	defer func() { engineHotWindowRebuildPage = restore }()

	pages, stats, err := store.readEngineHotWindowPages("OMM.fbs", readSource, aliases, placeholders, records)
	if err != nil {
		t.Fatalf("paged read: %v", err)
	}
	if stats.pages < 3 {
		t.Fatalf("paged read used %d page(s) — the multi-page path was not exercised", stats.pages)
	}

	var got []engineHotWindowRow
	for _, page := range pages {
		got = append(got, page...)
	}
	if len(got) != len(want) {
		t.Fatalf("paged read returned %d rows, baseline %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d differs\n paged: %+v\n baseline: %+v", i, got[i], want[i])
		}
	}

	// A WINDOW SMALLER THAN THE STORE MUST STILL TAKE THE NEWEST RECORDS.
	// Paging walks rowid DESCENDING for exactly this reason; a page loop that
	// walked the other way would silently make a restart resident on the
	// OLDEST records. Same store, because opening the engine costs ~12 s and
	// this package's test binary already runs close to its 30-minute cap.
	const window = 6
	engineHotWindowRebuildPage = 4
	windowPages, _, err := store.readEngineHotWindowPages("OMM.fbs", readSource, aliases, placeholders, window)
	if err != nil {
		t.Fatalf("paged read (small window): %v", err)
	}
	total := 0
	var lowest, highest int64
	first := true
	for _, page := range windowPages {
		for _, row := range page {
			total++
			if first {
				lowest, highest, first = row.rid, row.rid, false
			}
			if row.rid < lowest {
				lowest = row.rid
			}
			if row.rid > highest {
				highest = row.rid
			}
		}
	}
	if total != window {
		t.Fatalf("paged read returned %d rows for a window of %d", total, window)
	}
	var maxRid int64
	if err := store.db.QueryRow(`SELECT MAX(rowid) FROM sdn_record_index WHERE schema_name = ?`, "OMM.fbs").Scan(&maxRid); err != nil {
		t.Fatalf("max rowid: %v", err)
	}
	if highest != maxRid {
		t.Fatalf("window's highest rowid = %d, store max = %d — the window did not take the NEWEST records", highest, maxRid)
	}
	if highest-lowest != int64(window-1) {
		t.Fatalf("window spans rowids %d..%d for %d rows — the pages are not contiguous", lowest, highest, window)
	}
}
