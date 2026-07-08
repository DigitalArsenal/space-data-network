package flatsqldrv

import (
	"testing"
)

// Multi-statement Exec (sqlite3_exec-style DDL blocks) must run every
// statement through the driver.
func TestMultiStatementExec(t *testing.T) {
	db, _ := newSQLDB(t)

	if _, err := db.Exec(`
		CREATE TABLE a (v INTEGER);
		CREATE TABLE b (w TEXT);
		CREATE INDEX idx_b ON b(w);
		INSERT INTO a (v) VALUES (7);
	`); err != nil {
		t.Fatalf("multi-statement exec: %v", err)
	}
	var v int64
	if err := db.QueryRow(`SELECT v FROM a`).Scan(&v); err != nil || v != 7 {
		t.Fatalf("a.v = %d err=%v", v, err)
	}
	if _, err := db.Exec(`INSERT INTO b (w) VALUES (?); INSERT INTO b (w) VALUES (?)`, "x", "y"); err == nil {
		t.Fatal("expected error: params in multi-statement exec")
	}
}
