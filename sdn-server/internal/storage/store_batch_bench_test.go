package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// Store-only batch cost, no HTTP: 500 unique OMMs through StoreBatchWithSourceTags.
// Logs whether the engine ran AOT or interpreted — the number is meaningless
// without that, and it is what separates a daemon from a fresh container.
//
//	go test ./internal/storage/ -run '^$' -bench BenchmarkStoreBatchUniqueOMM -benchtime 1x -v
func BenchmarkStoreBatchUniqueOMM(b *testing.B) {
	dir, err := os.MkdirTemp("", "store-batch-bench-*")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { os.RemoveAll(dir) })
	validator, err := sds.NewValidator(nil)
	if err != nil {
		b.Fatal(err)
	}
	store, err := NewFlatSQLStore(filepath.Join(dir, "db"), validator)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { store.Close() })
	b.Logf("engine AOT=%v routed(OMM)=%v", store.engine != nil && store.engine.AOT(), store.engineRoutesSchema("OMM.fbs"))

	tags := SourceTags{ProviderID: "bench", SourceName: "bench-gp", BatchID: "bench-1"}
	const n = 500
	var norad uint32 = 200000
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		records := make([][]byte, 0, n)
		for j := 0; j < n; j++ {
			norad++
			records = append(records, sds.NewOMMBuilder().
				WithNoradCatID(norad).
				WithObjectName(fmt.Sprintf("BENCH-%d", norad)).
				WithEpoch("2026-08-27T12:00:00Z").
				Build())
		}
		b.StartTimer()
		if _, err := store.StoreBatchWithSourceTags("OMM.fbs", records, "bench-peer", nil, tags); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(n)*float64(b.N)/b.Elapsed().Seconds(), "records/s")
}
