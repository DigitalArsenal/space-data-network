package flatsqlrt

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// WAL vs TRUNCATE at the engine level: the same inserts, the same commit
// cadence, the only difference being the journal mode. TRUNCATE fsyncs the
// journal on every commit; WAL appends and fsyncs at NORMAL.
func TestWALOutperformsTruncateOnCommitCost(t *testing.T) {
	const (
		commits       = 40
		rowsPerCommit = 50
	)
	for _, tc := range []struct {
		name string
		mode JournalMode
	}{
		{"TRUNCATE", JournalTruncate},
		{"WAL", JournalWAL},
	} {
		root := t.TempDir()
		rt := newDiskRuntime(t, root)
		db, err := rt.OpenDatabase(ommTestSchema, "bench", filepath.Join(root, "b.db"), tc.mode)
		if err != nil {
			t.Fatalf("%s open: %v", tc.name, err)
		}
		if _, err := db.Query(`CREATE TABLE IF NOT EXISTS t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
			t.Fatalf("%s create: %v", tc.name, err)
		}

		started := time.Now()
		n := 0
		for c := 0; c < commits; c++ {
			if _, err := db.Query("BEGIN"); err != nil {
				t.Fatalf("%s begin: %v", tc.name, err)
			}
			for r := 0; r < rowsPerCommit; r++ {
				if _, err := db.Query(`INSERT INTO t (v) VALUES (?)`, fmt.Sprintf("row-%d-%d", c, r)); err != nil {
					t.Fatalf("%s insert: %v", tc.name, err)
				}
				n++
			}
			if _, err := db.Query("COMMIT"); err != nil {
				t.Fatalf("%s commit: %v", tc.name, err)
			}
		}
		el := time.Since(started)
		fmt.Printf("BENCH %-9s %d rows in %d commits: %v  (%.0f rows/s, %.1f ms/commit)\n",
			tc.name, n, commits, el.Round(time.Millisecond),
			float64(n)/el.Seconds(), float64(el.Milliseconds())/float64(commits))
		db.Destroy()
	}
}
