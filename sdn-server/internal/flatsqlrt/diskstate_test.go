package flatsqlrt

// diskstate_test.go — the engine reaching REAL FILES through the seven "env"
// imports, and the durable-state ABI on top of them.
//
// This is the test that would have caught the original defect: for a year the
// engine's `path` argument did not exist, then existed but routed to RAM. An
// assertion that a file appears ON DISK and that a SECOND ENGINE reads it back
// is the only kind that cannot be satisfied by something that merely looks
// durable.

import (
	"os"
	"path/filepath"
	"testing"
)

func newDiskRuntime(t *testing.T, root string) *Runtime {
	t.Helper()
	opts := []Option{WithFileIORoot(root)}
	if os.Getenv("FLATSQLRT_INTERPRET") == "" && os.Getenv("FLATSQLRT_WASM_FILE") == "" {
		opts = append(opts, WithAOTCache(sharedAOTDir(t)))
	}
	rt, err := New(opts...)
	if err != nil {
		t.Fatalf("New(WithFileIORoot): %v", err)
	}
	t.Cleanup(rt.Close)
	return rt
}

// TestDiskBackedDatabaseSurvivesEngineTeardown is the whole point of the lane:
// rows written by one engine instance are read back by a DIFFERENT one, with a
// process-scale teardown in between.
func TestDiskBackedDatabaseSurvivesEngineTeardown(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "sdn.db")

	rt1 := newDiskRuntime(t, root)
	db1, err := rt1.OpenDatabase(ommTestSchema, "control", dbPath, JournalTruncate)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	disk, err := db1.IsDiskBacked()
	if err != nil {
		t.Fatalf("IsDiskBacked: %v", err)
	}
	if !disk {
		t.Fatal("OpenDatabase with a real path reported NOT disk-backed — the engine fell back to RAM")
	}
	if _, err := db1.Query(`CREATE TABLE control(k TEXT PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create control table: %v", err)
	}
	if _, err := db1.Query(`INSERT INTO control(k,v) VALUES('mark','4242'),('who','hermes')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// The file must EXIST and be non-empty. This is the assertion the whole
	// class of failure was hiding from.
	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("database file missing after writes — I/O went to RAM: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("database file is empty after writes")
	}
	stats := rt1.FileIO().Stats()
	if stats.Writes == 0 || stats.Opens == 0 {
		t.Fatalf("host file layer saw no traffic: %+v", stats)
	}

	db1.Destroy()
	rt1.Close()

	// A brand new engine, a brand new VM, the same file.
	rt2 := newDiskRuntime(t, root)
	db2, err := rt2.OpenDatabase(ommTestSchema, "control", dbPath, JournalTruncate)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(db2.Destroy)
	res, err := db2.Query(`SELECT v FROM control WHERE k='mark'`)
	if err != nil {
		t.Fatalf("query after reopen: %v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0][0] != "4242" {
		t.Fatalf("rows after reopen = %#v, want [[4242]] — state did not survive", res.Rows)
	}
}

// TestDiskBackedJournalModeIsTruncate pins the journal-mode ruling. WAL needs
// xShmMap, which no wasm lane provides; a silent fallback to a different mode
// would change crash recovery without changing any test.
func TestDiskBackedJournalModeIsTruncate(t *testing.T) {
	root := t.TempDir()
	rt := newDiskRuntime(t, root)
	db, err := rt.OpenDatabase(ommTestSchema, "control", filepath.Join(root, "j.db"), JournalTruncate)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	t.Cleanup(db.Destroy)
	res, err := db.Query("PRAGMA journal_mode")
	if err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0][0] != "truncate" {
		t.Fatalf("journal_mode = %#v, want truncate", res.Rows)
	}
}

// TestOpenDatabaseWithoutFileRootRefuses is the fail-closed assertion. A
// runtime with no store root must NOT quietly open against RAM, because that is
// exactly the outcome that reads as durable and is not.
func TestOpenDatabaseWithoutFileRootRefuses(t *testing.T) {
	rt := newTestRuntime(t) // no WithFileIORoot
	if rt.DiskBackedAvailable() {
		t.Fatal("runtime without WithFileIORoot claims a filesystem")
	}
	_, err := rt.OpenDatabase(ommTestSchema, "control", filepath.Join(t.TempDir(), "x.db"), JournalTruncate)
	if err == nil {
		t.Fatal("disk-backed open SUCCEEDED on a runtime with no filesystem")
	}
	if !StateRecoverableFalse(err) {
		t.Fatalf("want a no-filesystem error, got %v", err)
	}
}

// StateRecoverableFalse asserts err is the one state error a caller must NOT
// answer by re-deriving.
func StateRecoverableFalse(err error) bool {
	return err != nil && !StateRecoverable(err) && errorsIsNoFilesystem(err)
}

func errorsIsNoFilesystem(err error) bool {
	type unwrapper interface{ Unwrap() error }
	for e := err; e != nil; {
		if e == ErrStateNoFilesystem {
			return true
		}
		u, ok := e.(unwrapper)
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

// TestOpenDatabaseRefusesPathOutsideRoot — the host, not the guest, decides
// what a path may reach, and it decides BEFORE entering the guest.
func TestOpenDatabaseRefusesPathOutsideRoot(t *testing.T) {
	root := t.TempDir()
	rt := newDiskRuntime(t, root)
	outside := filepath.Join(t.TempDir(), "elsewhere.db")
	if _, err := rt.OpenDatabase(ommTestSchema, "control", outside, JournalTruncate); err == nil {
		t.Fatal("opened a database outside the engine file root")
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("refused open still created the file")
	}
}

// TestEmptyPathIsExactlyCreateDatabase pins the equivalence the engine
// documents: every existing ephemeral consumer must be unaffected by this lane.
func TestEmptyPathIsExactlyCreateDatabase(t *testing.T) {
	root := t.TempDir()
	rt := newDiskRuntime(t, root)
	for _, p := range []string{"", ":memory:"} {
		db, err := rt.OpenDatabase(ommTestSchema, "ephemeral", p, JournalTruncate)
		if err != nil {
			t.Fatalf("OpenDatabase(%q): %v", p, err)
		}
		disk, err := db.IsDiskBacked()
		if err != nil {
			t.Fatalf("IsDiskBacked: %v", err)
		}
		if disk {
			t.Fatalf("OpenDatabase(%q) reported disk-backed", p)
		}
		// The durable-state layer must refuse an ephemeral handle rather than
		// pretending: -5, asserted, never skipped.
		if _, err := db.OpenState(); !errorsIsNoFilesystem(err) {
			t.Fatalf("OpenState on ephemeral db = %v, want ErrStateNoFilesystem", err)
		}
		db.Destroy()
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("ephemeral opens wrote %d files into the root", len(entries))
	}
}

// TestOpenStateOnFreshDiskDatabase pins the boot codes a first start sees, and
// that ReindexAll is always available afterwards.
func TestOpenStateOnFreshDiskDatabase(t *testing.T) {
	root := t.TempDir()
	rt := newDiskRuntime(t, root)
	db, err := rt.OpenDatabase(ommTestSchema, "control", filepath.Join(root, "fresh.db"), JournalTruncate)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	t.Cleanup(db.Destroy)

	// No persisted engine state yet: ErrStateAbsent, and it is RECOVERABLE.
	_, err = db.OpenState()
	if err == nil {
		t.Fatal("OpenState on a fresh database returned success, want ErrStateAbsent")
	}
	if !StateRecoverable(err) {
		t.Fatalf("OpenState error %v is not recoverable — a boot would have nothing to do", err)
	}
	if _, err := db.ReindexAll(); err != nil {
		t.Fatalf("ReindexAll after absent state: %v", err)
	}
	off, err := db.FlushedOffset()
	if err != nil {
		t.Fatalf("FlushedOffset: %v", err)
	}
	if off != 0 {
		t.Fatalf("FlushedOffset on an empty stream = %d, want 0", off)
	}
}

// TestReindexAllResetsIndexToMatchTheStream is the invariant the storage boot
// relies on: the engine's FlatBuffer record index is derived state, and a
// re-derivation against an empty stream leaves an EMPTY, consistent index —
// never index rows pointing into an arena that no longer exists.
func TestReindexAllResetsIndexToMatchTheStream(t *testing.T) {
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
	if _, err := db1.IngestOne(fixtureBuffer(t)); err != nil {
		t.Fatalf("IngestOne: %v", err)
	}
	res, err := db1.Query("SELECT COUNT(*) FROM OMM")
	if err != nil || len(res.Rows) != 1 || res.Rows[0][0] != int64(1) {
		t.Fatalf("pre-teardown count = %#v err=%v, want 1", res, err)
	}
	db1.Destroy()
	rt1.Close()

	// The record ARENA was never flushed to the stream, so a reopened engine
	// must see zero records — and must say so, not report rows it cannot back.
	rt2 := newDiskRuntime(t, root)
	db2, err := rt2.OpenDatabase(ommTestSchema, "control", dbPath, JournalTruncate)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(db2.Destroy)
	if err := db2.RegisterFileID("$OMM", "OMM"); err != nil {
		t.Fatalf("RegisterFileID: %v", err)
	}
	if _, err := db2.ReindexAll(); err != nil {
		t.Fatalf("ReindexAll: %v", err)
	}
	res, err = db2.Query("SELECT COUNT(*) FROM OMM")
	if err != nil {
		t.Fatalf("post-reindex query: %v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0][0] != int64(0) {
		t.Fatalf("post-reindex count = %#v, want 0 (index must match the empty stream)", res.Rows)
	}
}
