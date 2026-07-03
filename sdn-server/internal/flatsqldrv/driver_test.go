package flatsqldrv

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

// The driver must present standard SQLite-dialect database/sql behavior over
// the WASM engine: DDL/DML, params, transactions, LastInsertId/RowsAffected,
// and (with a journal) crash-safe deterministic replay incl. rowids — the
// properties internal/storage depends on.

const testSchema = `table Dummy { id: int (id); } root_type Dummy;`

func newSQLDB(t *testing.T, journal *StatementJournal) (*sql.DB, *flatsqlrt.Database) {
	t.Helper()
	rt, err := flatsqlrt.New()
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	t.Cleanup(rt.Close)
	edb, err := rt.CreateDatabase(testSchema, "drv-test")
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	t.Cleanup(edb.Destroy)
	db := Open(edb, journal)
	t.Cleanup(func() { db.Close() })
	return db, edb
}

func TestBasicCRUDAndMeta(t *testing.T) {
	db, _ := newSQLDB(t, nil)

	if _, err := db.Exec(`CREATE TABLE kv (k TEXT PRIMARY KEY, v TEXT, n INTEGER)`); err != nil {
		t.Fatalf("DDL: %v", err)
	}
	res, err := db.Exec(`INSERT INTO kv (k, v, n) VALUES (?, ?, ?)`, "a", "one", int64(1))
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if id, _ := res.LastInsertId(); id != 1 {
		t.Fatalf("LastInsertId = %d, want 1", id)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("RowsAffected = %d, want 1", n)
	}
	if _, err := db.Exec(`INSERT INTO kv (k, v, n) VALUES ('b', 'two', 2)`); err != nil {
		t.Fatalf("INSERT literal: %v", err)
	}

	res, err = db.Exec(`UPDATE kv SET v = ? WHERE n <= ?`, "upd", int64(2))
	if err != nil {
		t.Fatalf("UPDATE: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 2 {
		t.Fatalf("update RowsAffected = %d, want 2", n)
	}

	var k, v string
	var n int64
	if err := db.QueryRow(`SELECT k, v, n FROM kv WHERE k = ?`, "a").Scan(&k, &v, &n); err != nil {
		t.Fatalf("QueryRow: %v", err)
	}
	if k != "a" || v != "upd" || n != 1 {
		t.Fatalf("row = %q %q %d", k, v, n)
	}

	// NULL + blob + time round-trips.
	if _, err := db.Exec(`CREATE TABLE misc (b BLOB, s TEXT, t TEXT)`); err != nil {
		t.Fatalf("DDL misc: %v", err)
	}
	when := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO misc (b, s, t) VALUES (?, ?, ?)`, []byte{1, 2, 3}, nil, when); err != nil {
		t.Fatalf("INSERT misc: %v", err)
	}
	var b []byte
	var s sql.NullString
	var ts string
	if err := db.QueryRow(`SELECT b, s, t FROM misc`).Scan(&b, &s, &ts); err != nil {
		t.Fatalf("scan misc: %v", err)
	}
	if len(b) != 3 || b[2] != 3 || s.Valid || ts != "2026-07-03 12:00:00+00:00" {
		t.Fatalf("misc row: b=%v s=%v t=%q", b, s, ts)
	}

	// Errors surface as errors, not hangs (A.3c contract through the driver).
	if _, err := db.Query(`SELECT * FROM nope`); err == nil {
		t.Fatal("expected error for missing table")
	}
	var cnt int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM kv`).Scan(&cnt); err != nil || cnt != 2 {
		t.Fatalf("post-error count: %d err=%v", cnt, err)
	}
}

func TestTransactions(t *testing.T) {
	db, _ := newSQLDB(t, nil)
	if _, err := db.Exec(`CREATE TABLE txt (v INTEGER)`); err != nil {
		t.Fatal(err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO txt (v) VALUES (1)`); err != nil {
		t.Fatalf("tx insert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO txt (v) VALUES (2)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	var cnt int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM txt`).Scan(&cnt); err != nil || cnt != 1 {
		t.Fatalf("count after tx = %d err=%v (rollback leaked?)", cnt, err)
	}
}

func TestJournalReplayReproducesStateAndRowids(t *testing.T) {
	dir := t.TempDir()
	jpath := filepath.Join(dir, "control.sdnj")

	journal, err := OpenStatementJournal(jpath)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	db, _ := newSQLDB(t, journal)

	if _, err := db.Exec(`CREATE TABLE idx (schema_name TEXT, cid TEXT)`); err != nil {
		t.Fatal(err)
	}
	// Non-tx writes, a committed tx, and a rolled-back tx (must NOT replay).
	if _, err := db.Exec(`INSERT INTO idx (rowid, schema_name, cid) VALUES (?, 'OMM.fbs', ?)`, int64(10), "cid-10"); err != nil {
		t.Fatal(err)
	}
	tx, _ := db.Begin()
	if _, err := tx.Exec(`INSERT INTO idx (rowid, schema_name, cid) VALUES (?, 'OMM.fbs', ?)`, int64(11), "cid-11"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, _ = db.Begin()
	if _, err := tx.Exec(`INSERT INTO idx (rowid, schema_name, cid) VALUES (?, 'OMM.fbs', ?)`, int64(99), "cid-99"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	// SELECTs must not be journaled.
	if _, err := db.Query(`SELECT COUNT(*) FROM idx`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	journal.Close()

	// Boot: fresh engine + replay.
	journal2, err := OpenStatementJournal(jpath)
	if err != nil {
		t.Fatalf("reopen journal: %v", err)
	}
	defer journal2.Close()

	rt, err := flatsqlrt.New()
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	edb, err := rt.CreateDatabase(testSchema, "drv-replay")
	if err != nil {
		t.Fatal(err)
	}
	defer edb.Destroy()

	n, err := journal2.Replay(edb)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if n != 3 { // CREATE + 2 committed INSERTs; rollback + SELECT excluded
		t.Fatalf("replayed %d frames, want 3", n)
	}
	res, err := edb.Query(`SELECT rowid, cid FROM idx ORDER BY rowid`)
	if err != nil {
		t.Fatalf("query after replay: %v", err)
	}
	if len(res.Rows) != 2 ||
		res.Rows[0][0].(int64) != 10 || res.Rows[0][1] != "cid-10" ||
		res.Rows[1][0].(int64) != 11 || res.Rows[1][1] != "cid-11" {
		t.Fatalf("replayed rows: %#v", res.Rows)
	}
}

func TestJournalTornTailTruncated(t *testing.T) {
	dir := t.TempDir()
	jpath := filepath.Join(dir, "torn.sdnj")

	journal, err := OpenStatementJournal(jpath)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.appendAll([]journalFrame{{SQL: `CREATE TABLE t1 (v INTEGER)`}}); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash mid-append: garbage partial frame at the tail.
	if _, err := journal.f.Write([]byte{42, 0, 0, 0, 1, 2}); err != nil {
		t.Fatal(err)
	}
	journal.Close()

	journal2, err := OpenStatementJournal(jpath)
	if err != nil {
		t.Fatalf("reopen with torn tail: %v", err)
	}
	defer journal2.Close()

	rt, err := flatsqlrt.New()
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	edb, err := rt.CreateDatabase(testSchema, "drv-torn")
	if err != nil {
		t.Fatal(err)
	}
	defer edb.Destroy()
	n, err := journal2.Replay(edb)
	if err != nil || n != 1 {
		t.Fatalf("replay after torn tail: n=%d err=%v", n, err)
	}
	// And the journal must still be appendable.
	if err := journal2.appendAll([]journalFrame{{SQL: `CREATE TABLE t2 (v INTEGER)`}}); err != nil {
		t.Fatalf("append after truncate: %v", err)
	}
}
