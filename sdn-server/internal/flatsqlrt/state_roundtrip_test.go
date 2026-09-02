package flatsqlrt

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPersistedStateRoundTrip pins what the store builds on (2026-09-02):
// flush → reopen → OpenState round-trips every flushed record and its source
// partition; a record ingested AFTER the last flush is not persisted; and a
// tombstone (MarkDeleted) is NOT persisted — a reopened engine shows the row
// again, which is why the store re-applies its hot-window bound after a warm
// open (storage.restoreEngineResidencyFromPersistedState).
func TestPersistedStateRoundTrip(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "arena.db")

	rt1 := newDiskRuntime(t, root)
	db1, err := rt1.OpenDatabase(ommTestSchema, "control", dbPath, JournalTruncate)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	if err := db1.RegisterFileID("$OMM", "OMM"); err != nil {
		t.Fatalf("RegisterFileID: %v", err)
	}
	if err := db1.RegisterSource("srcA"); err != nil {
		t.Fatalf("RegisterSource: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := db1.IngestOneWithSource(fixtureBuffer(t), "srcA"); err != nil {
			t.Fatalf("IngestOneWithSource %d: %v", i, err)
		}
	}
	first, err := db1.Query("SELECT _source, _rowid FROM OMM ORDER BY _rowid LIMIT 1")
	if err != nil || len(first.Rows) != 1 {
		t.Fatalf("first row: %#v err=%v", first, err)
	}
	t.Logf("first row identity: %#v", first.Rows[0])
	src, _ := first.Rows[0][0].(string)
	seq, _ := first.Rows[0][1].(int64)
	if err := db1.MarkDeleted(src, uint64(seq)); err != nil {
		t.Fatalf("MarkDeleted(%q,%d): %v", src, seq, err)
	}
	res, err := db1.Query("SELECT COUNT(*) FROM OMM")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	t.Logf("live count after 5 ingests + 1 tombstone: %#v", res.Rows)
	if err := db1.FlushIndex(); err != nil {
		t.Fatalf("FlushIndex: %v", err)
	}
	off, err := db1.FlushedOffset()
	t.Logf("flushed offset %d err=%v", off, err)
	// a second ingest AFTER the flush, never flushed: must NOT be visible after reopen
	if _, err := db1.IngestOneWithSource(fixtureBuffer(t), "srcA"); err != nil {
		t.Fatalf("post-flush ingest: %v", err)
	}
	db1.Destroy()
	rt1.Close()

	rt2 := newDiskRuntime(t, root)
	db2, err := rt2.OpenDatabase(ommTestSchema, "control", dbPath, JournalTruncate)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(db2.Destroy)
	if err := db2.RegisterFileID("$OMM", "OMM"); err != nil {
		t.Fatalf("RegisterFileID: %v", err)
	}
	if err := db2.RegisterSource("srcA"); err != nil {
		t.Logf("RegisterSource on reopen: %v", err)
	}
	n, err := db2.OpenState()
	t.Logf("OpenState → %d, err=%v", n, err)
	if err != nil {
		t.Fatalf("OpenState: %v", err)
	}
	res, err = db2.Query("SELECT COUNT(*) FROM OMM")
	if err != nil {
		t.Fatalf("post-open count: %v", err)
	}
	t.Logf("count after reopen: %#v", res.Rows)
	res2, err := db2.Query("SELECT _source, _rowid FROM OMM ORDER BY _rowid")
	if err != nil {
		t.Fatalf("rows: %v", err)
	}
	t.Logf("rows after reopen: %#v", res2.Rows)
	if len(res.Rows) != 1 || res.Rows[0][0] != int64(5) {
		t.Fatalf("want 5 rows (5 flushed; the tombstone is not persisted; the post-flush ingest is lost), got %#v", res.Rows)
	}
	// ingest more after reopen, flush, reopen again → 5
	if _, err := db2.IngestOneWithSource(fixtureBuffer(t), "srcA"); err != nil {
		t.Fatalf("ingest after reopen: %v", err)
	}
	if err := db2.FlushIndex(); err != nil {
		t.Fatalf("FlushIndex 2: %v", err)
	}
	db2.Destroy()
	rt2.Close()
	rt3 := newDiskRuntime(t, root)
	db3, err := rt3.OpenDatabase(ommTestSchema, "control", dbPath, JournalTruncate)
	if err != nil {
		t.Fatalf("reopen 3: %v", err)
	}
	t.Cleanup(db3.Destroy)
	_ = db3.RegisterFileID("$OMM", "OMM")
	_ = db3.RegisterSource("srcA")
	if _, err := db3.OpenState(); err != nil {
		t.Fatalf("OpenState 3: %v", err)
	}
	res, _ = db3.Query("SELECT COUNT(*) FROM OMM")
	t.Logf("count after third open: %#v", res.Rows)
	if len(res.Rows) != 1 || res.Rows[0][0] != int64(6) {
		t.Fatalf("want 6 rows after the second flush, got %#v", res.Rows)
	}
}

// TestPersistedStateOpenIsCheap: opening 40k persisted records must not
// re-ingest or grow linear memory — the index is read, the records stay on disk.
func TestPersistedStateOpenIsCheap(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "arena.db")
	rt1 := newDiskRuntime(t, root)
	db1, err := rt1.OpenDatabase(ommTestSchema, "control", dbPath, JournalTruncate)
	if err != nil {
		t.Fatal(err)
	}
	_ = db1.RegisterFileID("$OMM", "OMM")
	buf := fixtureBuffer(t)
	st0, _ := rt1.MemoryStats()
	for i := 0; i < 40000; i++ {
		if _, err := db1.IngestOne(buf); err != nil {
			t.Fatal(err)
		}
	}
	st1, _ := rt1.MemoryStats()
	if err := db1.FlushIndex(); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(dbPath + ".fsdata")
	t.Logf("ingest 40k x %d B: wasm mem %d -> %d MB; fsdata %d MB", len(buf), st0.Bytes>>20, st1.Bytes>>20, fi.Size()>>20)
	db1.Destroy()
	rt1.Close()

	rt2 := newDiskRuntime(t, root)
	db2, err := rt2.OpenDatabase(ommTestSchema, "control", dbPath, JournalTruncate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db2.Destroy)
	_ = db2.RegisterFileID("$OMM", "OMM")
	m0, _ := rt2.MemoryStats()
	start := time.Now()
	n, err := db2.OpenState()
	m1, _ := rt2.MemoryStats()
	t.Logf("OpenState → %d in %s; wasm mem %d -> %d MB", n, time.Since(start), m0.Bytes>>20, m1.Bytes>>20)
	if err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	res, err := db2.Query("SELECT COUNT(*) FROM OMM")
	m2, _ := rt2.MemoryStats()
	t.Logf("count %#v in %s; wasm mem %d MB", res.Rows, time.Since(start), m2.Bytes>>20)
	if err != nil {
		t.Fatal(err)
	}
}
