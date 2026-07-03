package flatsqlrt

// durability_test.go measures the A.4 durability numbers: how fast the
// engine can be rebuilt at boot from the durable substrate (append-only
// stream files) via (a) re-ingest of the raw stream and (b)
// export_data/load_and_rebuild snapshots, and how wasm linear memory grows
// with row count (the wasm32 4 GiB ceiling bounds resident history).
//
//	FLATSQLRT_SCALE_MEASURE=1 go test ./internal/flatsqlrt/ -run TestDurability -v

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func buildCatalogStream(objects, epochs int) []byte {
	const baseEpochUnix = 1778400000 // 2026-05-10T00:00:00Z
	stream := make([]byte, 0, objects*epochs*300)
	for e := 0; e < epochs; e++ {
		for i := 0; i < objects; i++ {
			norad := uint32(10000 + i)
			epoch := time.Unix(baseEpochUnix+int64(e*86400+i%86400), 0).UTC().Format("2006-01-02T15:04:05Z")
			stream = append(stream, buildOMM(norad, fmt.Sprintf("SAT-%d", norad), epoch)...)
		}
	}
	return stream
}

func TestDurabilityBootRebuild(t *testing.T) {
	if os.Getenv("FLATSQLRT_SCALE_MEASURE") == "" {
		t.Skip("set FLATSQLRT_SCALE_MEASURE=1 to run durability measurements")
	}

	// Catalog-snapshot scale (~29K) and hot-window scale (~500K ≈ 29K
	// objects x 17 epochs) both must boot fast.
	for _, tc := range []struct {
		name    string
		objects int
		epochs  int
	}{
		{"catalog_29k", 29000, 1},
		{"hot_window_493k", 29000, 17},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := buildCatalogStream(tc.objects, tc.epochs)
			records := tc.objects * tc.epochs
			mb := float64(len(stream)) / (1024 * 1024)

			// (a) Boot by re-ingesting the raw stream (the stream files ARE
			// this byte layout, so this is the real boot path).
			rt := newTestRuntime(t)
			db := newOMMDatabase(t, rt, "boot-"+tc.name)
			start := time.Now()
			if _, err := db.Ingest(stream); err != nil {
				t.Fatalf("ingest: %v", err)
			}
			ingestDur := time.Since(start)

			res, err := db.Query(`SELECT COUNT(*) FROM OMM`)
			if err != nil || res.Rows[0][0].(int64) != int64(records) {
				t.Fatalf("count after ingest: %#v err=%v", res, err)
			}
			var memMB float64
			if stats, err := rt.MemoryStats(); err == nil {
				memMB = float64(stats.Bytes) / (1024 * 1024)
			}

			// (b) Snapshot round-trip: export from the hot engine,
			// load_and_rebuild into a fresh one.
			start = time.Now()
			snapshot, err := db.ExportData()
			if err != nil {
				t.Fatalf("export: %v", err)
			}
			exportDur := time.Since(start)

			rt2 := newTestRuntime(t)
			db2 := newOMMDatabase(t, rt2, "restore-"+tc.name)
			start = time.Now()
			if err := db2.LoadAndRebuild(snapshot); err != nil {
				t.Fatalf("load_and_rebuild: %v", err)
			}
			rebuildDur := time.Since(start)
			res, err = db2.Query(`SELECT COUNT(*) FROM OMM`)
			if err != nil || res.Rows[0][0].(int64) != int64(records) {
				t.Fatalf("count after rebuild: %#v err=%v", res, err)
			}

			t.Logf("MEASURE %s: %d records / %.1f MB — boot re-ingest %s; export %s (%.1f MB); load_and_rebuild %s; wasm mem %.0f MB",
				tc.name, records, mb, ingestDur, exportDur,
				float64(len(snapshot))/(1024*1024), rebuildDur, memMB)
		})
	}
}

// TestDurabilityMemoryCeiling grows one engine toward full-history scale to
// find where the wasm32 4 GiB ceiling lands. History that does not fit
// resident stays in stream files and is loaded on demand (see the decision in
// ARCHITECTURE_FLATSQL_FIRST.md §5).
func TestDurabilityMemoryCeiling(t *testing.T) {
	if os.Getenv("FLATSQLRT_SCALE_MEASURE") == "" {
		t.Skip("set FLATSQLRT_SCALE_MEASURE=1 to run durability measurements")
	}

	rt := newTestRuntime(t)
	db := newOMMDatabase(t, rt, "ceiling")

	total := 0
	for i := 0; i < 7; i++ { // up to 3.5M records, 500K per increment
		stream := buildCatalogStream(25000, 20) // 25K objects x 20 epochs = 500K records
		start := time.Now()
		if _, err := db.Ingest(stream); err != nil {
			t.Logf("MEASURE ceiling: ingest FAILED at ~%d resident records: %v", total, err)
			return
		}
		total += 500000
		dur := time.Since(start)
		stats, err := rt.MemoryStats()
		if err != nil {
			t.Fatalf("MemoryStats: %v", err)
		}
		memMB := float64(stats.Bytes) / (1024 * 1024)
		t.Logf("MEASURE ceiling: %d records resident — wasm mem %.0f MB (+%s for last 500K)",
			total, memMB, dur)
		if memMB > 3600 {
			t.Logf("MEASURE ceiling: stopping near the 4 GiB wasm32 limit at %d records", total)
			return
		}
	}
}
