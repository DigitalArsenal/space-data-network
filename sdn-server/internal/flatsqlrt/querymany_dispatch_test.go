package flatsqlrt

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// The write path issues ~8.2 exec-thread handoffs per record via one
// Query() per statement. QueryMany executes a whole batch in ONE handoff.
// This measures the prize before anyone restructures the write path for it.
func TestQueryManyCollapsesDispatch(t *testing.T) {
	root := t.TempDir()
	rt := newDiskRuntime(t, root)
	db, err := rt.OpenDatabase(ommTestSchema, "qm", filepath.Join(root, "q.db"), JournalWAL)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Destroy()
	if _, err := db.Query(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}

	const n = 320 // ~ one 64-record chunk's worth of statements

	before := rt.ModuleDispatchStats()
	start := time.Now()
	db.Query("BEGIN")
	for i := 0; i < n; i++ {
		if _, err := db.Query(`INSERT INTO t (v) VALUES (?)`, fmt.Sprintf("ind-%d", i)); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	db.Query("COMMIT")
	indEl := time.Since(start)
	indStats := rt.ModuleDispatchStats()

	reqs := make([]QueryRequest, 0, n)
	for i := 0; i < n; i++ {
		reqs = append(reqs, QueryRequest{SQL: `INSERT INTO t (v) VALUES (?)`, Params: []interface{}{fmt.Sprintf("qm-%d", i)}})
	}
	start = time.Now()
	db.Query("BEGIN")
	if _, err := db.QueryMany(reqs); err != nil {
		t.Fatalf("QueryMany: %v", err)
	}
	db.Query("COMMIT")
	qmEl := time.Since(start)
	after := rt.ModuleDispatchStats()

	fmt.Printf("QM individual : %v  calls=%d dispatches=%d\n", indEl.Round(time.Millisecond),
		indStats.Calls-before.Calls, indStats.Dispatches-before.Dispatches)
	fmt.Printf("QM batched    : %v  calls=%d dispatches=%d\n", qmEl.Round(time.Millisecond),
		after.Calls-indStats.Calls, after.Dispatches-indStats.Dispatches)
	fmt.Printf("QM speedup    : %.2fx on %d statements\n", float64(indEl)/float64(qmEl), n)
}
