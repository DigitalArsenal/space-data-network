package storage

// store-migrate's legacy reads (format2_source.go) on a standard held by
// more producer tables than a flat SQL expression allows (SQLite refuses an
// expression deeper than 1000), with orphan index rows (no producer table
// holds the record) among them.

import (
	"fmt"
	"testing"
)

func TestMigrationSourceOrphansAcrossManyProducerTables(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFlatSQLStore(dir, bootTestValidator(t), WithDeferredBootRebuilds())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const producers = 1100
	const schema = "OMM.fbs"
	src := &MigrationSource{s: s, base: dir}

	// The index, in rowid order: an orphan first, producer i's record c-i
	// (record_length 100+i, lane batch b<i%3>), a CID held by producers 3
	// and 1050 (FIRST copy: producer 3's, 7 bytes), orphans in the middle
	// and last. Orphans keep their tag rows (lane "orphans"). One
	// transaction: the store is only read below.
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	index := func(cid string) {
		exec(`INSERT INTO sdn_record_index (schema_name, cid, source_timestamp) VALUES (?, ?, 1)`, schema, cid)
	}
	tag := func(cid, batch string) {
		exec(`INSERT INTO sdn_record_source_tags (schema_name, cid, provider_id, source_name, batch_id) VALUES (?, ?, 'p', 's', ?)`,
			schema, cid, batch)
	}
	hold := func(table, cid string, length int) {
		exec(fmt.Sprintf(`INSERT INTO %s (cid, peer_id, timestamp, data, record_length) VALUES (?, 'peer', 1, x'00', ?)`, table), cid, length)
	}
	orphan := func(cid string) {
		index(cid)
		tag(cid, "orphans")
	}
	names := make([]string, producers)
	for i := range names {
		if names[i], err = ProducerStandardTableName(fmt.Sprintf("peer%04d", i), schema); err != nil {
			t.Fatal(err)
		}
		exec(fmt.Sprintf(`CREATE TABLE %s (cid TEXT PRIMARY KEY, peer_id TEXT NOT NULL, timestamp INTEGER NOT NULL,
			data BLOB NOT NULL, record_length INTEGER NOT NULL, signature_hex TEXT, supersede_key TEXT, created_at INTEGER)`, names[i]))
	}
	orphan("orphan-first")
	wantLanes := map[string][2]int64{}
	var wantHeld []string
	for i, name := range names {
		cid := fmt.Sprintf("c-%04d", i)
		index(cid)
		hold(name, cid, 100+i)
		batch := fmt.Sprintf("b%d", i%3)
		tag(cid, batch)
		l := wantLanes[batch]
		wantLanes[batch] = [2]int64{l[0] + 1, l[1] + int64(100+i)}
		wantHeld = append(wantHeld, cid)
		if i == 700 {
			orphan("orphan-middle")
		}
	}
	index("shared")
	hold(names[1050], "shared", 999)
	hold(names[3], "shared", 7)
	tag("shared", "shared")
	wantLanes["shared"] = [2]int64{1, 7}
	wantHeld = append(wantHeld, "shared")
	orphan("orphan-last")
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	all, err := src.ProducerTables()
	if err != nil {
		t.Fatal(err)
	}
	var tables []LegacyTable
	for _, tb := range all {
		if tb.Schema == schema {
			tables = append(tables, tb)
		}
	}
	if len(tables) != producers {
		t.Fatalf("%d %s producer tables, want %d", len(tables), schema, producers)
	}

	n, err := src.OrphanIndexRows(schema, tables)
	if err != nil {
		t.Fatalf("orphans: %v", err)
	}
	if n != 3 {
		t.Fatalf("%d orphans, want 3", n)
	}
	if n, err := src.OrphanIndexRows(schema, nil); err != nil || n != int64(len(wantHeld)+3) {
		t.Fatalf("with no producer table every index row is an orphan: %d, %v", n, err)
	}

	var got []string
	var after int64
	for {
		page, err := src.HeldIndexPage(schema, tables, after, 97)
		if err != nil {
			t.Fatalf("held index page after %d: %v", after, err)
		}
		for _, r := range page {
			got = append(got, r.CID)
		}
		if len(page) < 97 {
			break
		}
		after = page[len(page)-1].RowID
	}
	if len(got) != len(wantHeld) {
		t.Fatalf("%d held index rows, want %d", len(got), len(wantHeld))
	}
	for i := range got {
		if got[i] != wantHeld[i] {
			t.Fatalf("held index row %d: %s, want %s", i, got[i], wantHeld[i])
		}
	}

	// One range, then five ranges of 250 tag rows.
	prev := laneRecountChunk
	defer func() { laneRecountChunk = prev }()
	for _, chunk := range []int{prev, 250} {
		laneRecountChunk = chunk
		lanes, err := src.LaneRecount(schema, tables)
		if err != nil {
			t.Fatalf("lane recount over %d tables (ranges of %d): %v", len(tables), chunk, err)
		}
		if len(lanes) != len(wantLanes) {
			t.Fatalf("ranges of %d: lanes %+v, want %v", chunk, lanes, wantLanes)
		}
		for _, l := range lanes {
			if want := wantLanes[l.BatchID]; l.Count != want[0] || l.Bytes != want[1] {
				t.Fatalf("ranges of %d: lane %s: (%d, %d B), want (%d, %d B)", chunk, l.BatchID, l.Count, l.Bytes, want[0], want[1])
			}
		}
	}
}
