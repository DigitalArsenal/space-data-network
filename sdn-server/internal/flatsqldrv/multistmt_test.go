package flatsqldrv

import (
	"strings"
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

func TestSplitStatementsKeepsTriggerBodiesWhole(t *testing.T) {
	query := `
		CREATE TABLE a (x);
		CREATE TRIGGER IF NOT EXISTS t INSTEAD OF INSERT ON v
		BEGIN
		  INSERT OR IGNORE INTO a (x) VALUES (NEW.x);
		  DELETE FROM a WHERE x = CASE WHEN NEW.x IS NULL THEN 0 ELSE NEW.x END;
		END;
		CREATE TABLE b (y);
	`
	stmts := splitStatements(query)
	if len(stmts) != 3 {
		t.Fatalf("split into %d statements, want 3: %#v", len(stmts), stmts)
	}
	if !strings.HasPrefix(stmts[1], "CREATE TRIGGER") || !strings.HasSuffix(stmts[1], "END") {
		t.Fatalf("trigger statement was cut: %q", stmts[1])
	}
	if !strings.Contains(stmts[1], "DELETE FROM a") {
		t.Fatalf("trigger body lost its second statement: %q", stmts[1])
	}
	if !strings.HasPrefix(stmts[2], "CREATE TABLE b") {
		t.Fatalf("statement after the trigger is %q, want CREATE TABLE b", stmts[2])
	}
}

func TestSplitStatementsStillSplitsOrdinaryBeginEnd(t *testing.T) {
	stmts := splitStatements(`BEGIN; INSERT INTO a (x) VALUES (1); COMMIT;`)
	if len(stmts) != 3 {
		t.Fatalf("split into %d statements, want 3: %#v", len(stmts), stmts)
	}
}
