package storage

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// How many fsyncs does one StoreBatch window cost?
//
// The 1 GiB all-schemas bench sits at ~25% of its own durable disk floor — the
// floor being the same records appended and fsynced at the same 64-record
// cadence. Dispatch is no longer the cost (0.3 handoffs per record) and the
// process is ~83% idle in a CPU profile, so the gap is waiting, and the thing
// a durable write waits on is fsync. One window that fsyncs N times can only
// ever reach 1/N of a floor that fsyncs once.
//
// Instrumentation, not a threshold: it prints, because the useful output is the
// RATIO of fsyncs to windows and a number in an assertion goes stale the moment
// anyone improves it.
func TestFsyncsPerStoreBatchWindow(t *testing.T) {
	dir := t.TempDir()
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(dir, "db"), v)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	const window = 64
	const rounds = 4

	// Warm: the first window of a schema creates its routed table and shadow
	// tables, which is one-time DDL and not what the steady state pays.
	warm := make([][]byte, 0, window)
	for i := 0; i < window; i++ {
		warm = append(warm, buildEngineOMM(t, uint32(20000+i), "FSYNC-WARM", int64(1700000000+i)))
	}
	if _, err := store.StoreBatch("OMM.fbs", warm, "fsync-probe", nil); err != nil {
		t.Fatalf("warm StoreBatch: %v", err)
	}

	io := store.engine.FileIO()
	before := io.Stats()
	beforeDispatch := store.engine.ModuleDispatchStats()

	total := 0
	next := uint32(30000)
	for r := 0; r < rounds; r++ {
		recs := make([][]byte, 0, window)
		for i := 0; i < window; i++ {
			recs = append(recs, buildEngineOMM(t, next, "FSYNC-PROBE", int64(1700100000+int64(next))))
			next++
		}
		n, err := store.StoreBatch("OMM.fbs", recs, "fsync-probe", nil)
		if err != nil {
			t.Fatalf("StoreBatch round %d: %v", r, err)
		}
		total += n
	}

	after := io.Stats()
	afterDispatch := store.engine.ModuleDispatchStats()

	syncs := after.Syncs - before.Syncs
	writes := after.Writes - before.Writes
	bytes := after.BytesWrote - before.BytesWrote
	disp := afterDispatch.Dispatches - beforeDispatch.Dispatches

	fmt.Printf("FSYNC %d windows of %d (%d records): fsyncs=%d (%.1f per window, %.3f per record)\n",
		rounds, window, total, syncs, float64(syncs)/float64(rounds), float64(syncs)/float64(total))
	fmt.Printf("FSYNC   host writes=%d (%.1f per window) bytes=%d (%.0f per record) dispatches=%d\n",
		writes, float64(writes)/float64(rounds), bytes, float64(bytes)/float64(total), disp)
}
