package storage

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// How many exec-thread handoffs does one stored record actually cost?
//
// This is instrumentation, not a threshold: it prints rather than asserts,
// because the useful thing is the SHAPE of the cost, and a number baked into an
// assertion goes stale the moment anyone improves it. Measured when written:
// 68.3 guest calls and 8.2 handoffs per record, with the top exports being
// result materialisation (cell_type, cell_number, column_name, column_count)
// plus a malloc/free pair each. That is what says the remaining cost is
// DISPATCH and result reading rather than SQL.
func TestHandoffsPerStoredRecord(t *testing.T) {
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

	recs := make([][]byte, 0, 128)
	for i := 0; i < 128; i++ {
		recs = append(recs, buildEngineOMM(t, uint32(40000+i), fmt.Sprintf("HANDOFF-%d", i), int64(1700000000+i)))
	}

	before := store.engine.ModuleDispatchStats()
	n, err := store.StoreBatch("OMM.fbs", recs, "probe-producer", nil)
	if err != nil {
		t.Fatalf("StoreBatch: %v", err)
	}
	after := store.engine.ModuleDispatchStats()

	calls := after.Calls - before.Calls
	disp := after.Dispatches - before.Dispatches
	batches := after.Batches - before.Batches
	fmt.Printf("PROBE %d records: guest_calls=%d dispatches=%d batches=%d\n", n, calls, disp, batches)
	fmt.Printf("PROBE per record: calls=%.1f dispatches=%.1f batches=%.1f\n",
		float64(calls)/float64(n), float64(disp)/float64(n), float64(batches)/float64(n))
	for _, e := range after.PerExport[:min(6, len(after.PerExport))] {
		fmt.Printf("PROBE   export %-34s %d\n", e.Name, e.Count)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
