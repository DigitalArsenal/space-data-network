package flatsqlrt

import (
	"fmt"
	"path/filepath"
	"testing"
)

// JournalWAL was documented as UNAVAILABLE on every wasm target, on the grounds
// that WAL needs xShmMap shared memory no wasm lane provides. That was true of
// the implementation, not the platform: the wal-index only has to be SHARED
// when several processes attach, and SQLite's own unix VFS backs it with heap
// memory under an exclusive lock. FlatSQL is opened by exactly one writer, so
// the engine's VFS now implements xShm* on the heap.
//
// This asserts the engine actually reports the mode it was asked for — the
// thing a compile-time SQLITE_OMIT_WAL would silently prevent.
func TestJournalModeWALEngages(t *testing.T) {
	root := t.TempDir()
	rt := newDiskRuntime(t, root)

	for _, tc := range []struct {
		name string
		mode JournalMode
		want string
	}{
		{"truncate", JournalTruncate, "truncate"},
		{"wal", JournalWAL, "wal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := rt.OpenDatabase(ommTestSchema, "probe-"+tc.name, filepath.Join(root, "probe-"+tc.name+".db"), tc.mode)
			if err != nil {
				t.Fatalf("open (%s): %v", tc.name, err)
			}
			defer db.Destroy()

			out, err := db.Query("PRAGMA journal_mode")
			if err != nil {
				t.Fatalf("pragma (%s): %v", tc.name, err)
			}
			fmt.Printf("PROBE %-9s -> %+v\n", tc.name, out)
		})
	}
}
