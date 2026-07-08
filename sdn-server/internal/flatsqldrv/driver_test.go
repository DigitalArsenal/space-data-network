package flatsqldrv

import (
	"database/sql"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

// The driver must present standard SQLite-dialect database/sql behavior over
// the WASM engine: DDL/DML, params, transactions, LastInsertId/RowsAffected,
// and multi-statement DDL blocks — the properties internal/storage depends on.

const testSchema = `table Dummy { id: int (id); } root_type Dummy;`

func newSQLDB(t *testing.T) (*sql.DB, *flatsqlrt.Database) {
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
	db := Open(edb)
	t.Cleanup(func() { db.Close() })
	return db, edb
}

func TestBasicCRUDAndMeta(t *testing.T) {
	db, _ := newSQLDB(t)

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
	db, _ := newSQLDB(t)
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
