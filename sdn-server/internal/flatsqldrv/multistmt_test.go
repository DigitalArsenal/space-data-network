package flatsqldrv

import (
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
)

// Multi-statement Exec (sqlite3_exec-style DDL blocks) must run every
// statement — through the driver AND through journal replay.
func TestMultiStatementExecAndReplay(t *testing.T) {
	jpath := filepath.Join(t.TempDir(), "multi.sdnj")
	journal, err := OpenStatementJournal(jpath)
	if err != nil {
		t.Fatal(err)
	}
	db, _ := newSQLDB(t, journal)

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
	db.Close()
	journal.Close()

	journal2, err := OpenStatementJournal(jpath)
	if err != nil {
		t.Fatal(err)
	}
	defer journal2.Close()
	rt, err := flatsqlrt.New()
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	edb, err := rt.CreateDatabase(testSchema, "multi-replay")
	if err != nil {
		t.Fatal(err)
	}
	defer edb.Destroy()
	if _, err := journal2.Replay(edb); err != nil {
		t.Fatalf("replay: %v", err)
	}
	res, err := edb.Query(`SELECT v FROM a`)
	if err != nil || len(res.Rows) != 1 || res.Rows[0][0].(int64) != 7 {
		t.Fatalf("replayed a: %#v err=%v", res, err)
	}
	if _, err := edb.Query(`SELECT COUNT(*) FROM b`); err != nil {
		t.Fatalf("replayed b missing: %v", err)
	}
}
