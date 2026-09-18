package flatsqlrt

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// The engine pairs WAL with synchronous=NORMAL, which can lose the last
// commits on POWER LOSS (it never corrupts); TRUNCATE pairs with FULL, which
// cannot. Swapping one for the other would have been a durability change
// smuggled in as a performance change, so this pins the number that made the
// choice: WAL still beats TRUNCATE by ~3x when held to the SAME durability.
// That is the comparison this test exists for, and it is still valid.
//
// What is NO LONGER true is the conclusion that used to be written here — that
// the control store runs WAL+FULL and takes only the free win. It runs
// WAL+NORMAL as of 2026-09-17; the owner priced the durability and took it.
// The reasoning, the measurements and the exact exposure are in
// storage/flatsql_boot_state.go above the pragma. Do not reconstruct the
// decision from the numbers below.
//
// AND DO NOT QUOTE THOSE NUMBERS AS FACT. Measured when written: TRUNCATE+FULL
// 1315 rows/s, WAL+FULL 3840, WAL+NORMAL 19550. Re-measured 2026-09-18 on a
// loaded Mac Studio, 7 runs, medians: 1363, 4353, 9460. TRUNCATE+FULL is
// stable; the two WAL modes are not, and WAL+NORMAL in particular is dominated
// by whatever else the box is doing. This test PRINTS, it does not assert, for
// exactly that reason — read it as a shape, and re-run it rather than citing
// a recorded row.
func TestWALBeatsTruncateAtEqualDurability(t *testing.T) {
	const commits, rows = 30, 50
	run := func(label string, mode JournalMode, sync string) {
		root := t.TempDir()
		rt := newDiskRuntime(t, root)
		db, err := rt.OpenDatabase(ommTestSchema, "s", filepath.Join(root, "s.db"), mode)
		if err != nil {
			t.Fatalf("%s open: %v", label, err)
		}
		defer db.Destroy()
		if sync != "" {
			if _, err := db.Query("PRAGMA synchronous=" + sync); err != nil {
				t.Fatalf("%s pragma: %v", label, err)
			}
		}
		if _, err := db.Query(`CREATE TABLE IF NOT EXISTS t (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
			t.Fatalf("%s create: %v", label, err)
		}
		cur, _ := db.Query("PRAGMA synchronous")
		jm, _ := db.Query("PRAGMA journal_mode")
		started := time.Now()
		for c := 0; c < commits; c++ {
			db.Query("BEGIN")
			for r := 0; r < rows; r++ {
				if _, err := db.Query(`INSERT INTO t (v) VALUES (?)`, fmt.Sprintf("%d-%d", c, r)); err != nil {
					t.Fatalf("%s insert: %v", label, err)
				}
			}
			if _, err := db.Query("COMMIT"); err != nil {
				t.Fatalf("%s commit: %v", label, err)
			}
		}
		el := time.Since(started)
		fmt.Printf("SYNC %-22s journal=%v sync=%v  %6.0f rows/s  %.1f ms/commit\n",
			label, jm.Rows, cur.Rows, float64(commits*rows)/el.Seconds(),
			float64(el.Milliseconds())/float64(commits))
	}
	run("TRUNCATE+FULL", JournalTruncate, "")
	run("WAL+NORMAL", JournalWAL, "")
	run("WAL+FULL", JournalWAL, "FULL")
}
