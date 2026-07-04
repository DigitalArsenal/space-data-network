package flowrt

// Loop C.4 measurement harness: latency + throughput of the flow-served
// /api/v1/data/omm/bulk over a store seeded at scale through the NORMAL
// ingest path. Closes the original defect (`omm/bulk?format=json` timing out
// on the mattn/go-sqlite3 handler). Only runs when explicitly requested:
//
//	SDN_C4_MEASURE=1 go test ./internal/flowrt/ -run TestHTTPMountMeasure -v -timeout 120m
//
// Reports, per the loop protocol:
//   - GET omm/bulk epoch-nearest latency at limit=29000 and limit=250000,
//     flatbuffers (default) and json, interpreted AND AOT flow dispatch
//     (AOT unblocked in loop C.5b — see TestAOTMountRepro);
//   - concurrent throughput (8 parallel clients) with pool=1 vs pool=4;
//   - hot-window enforcement at scale (500K ingested through a 400K window).

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestHTTPMountMeasure(t *testing.T) {
	if os.Getenv("SDN_C4_MEASURE") == "" {
		t.Skip("set SDN_C4_MEASURE=1 to run the C.4 flow mount measurements")
	}
	dist := dataRetrievalFlowDist(t)

	const (
		objects       = 250_000
		epochsPerObj  = 2
		batchSize     = 5_000
		hotWindow     = 400_000
		baseEpochUnix = int64(1778400000) // 2026-05-10T00:00:00Z
		mountPages    = uint32(16384)     // 1 GiB per instance: json encode at 250K records
	)

	// ---- Seed through the normal ingest path -------------------------------
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator,
		storage.WithEngineHotWindow(hotWindow))
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	defer store.Close()

	tags := storage.SourceTags{ProviderID: "prov-measure", SourceName: "celestrak-gp", BatchID: "batch-measure"}
	total := objects * epochsPerObj
	var (
		batch    [][]byte
		inserted int
		seedDur  time.Duration
	)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		start := time.Now()
		n, err := store.StoreBatchWithSourceTags("OMM.fbs", batch, "peer-measure", nil, tags)
		if err != nil {
			t.Fatalf("StoreBatchWithSourceTags after %d records: %v", inserted, err)
		}
		seedDur += time.Since(start)
		inserted += n
		batch = batch[:0]
	}
	for e := 0; e < epochsPerObj; e++ {
		for i := 0; i < objects; i++ {
			norad := uint32(100000 + i)
			epoch := baseEpochUnix + int64(e)*86400 + int64(i%86400)
			batch = append(batch, buildMountTestOMM(t, norad, fmt.Sprintf("SAT-%d", norad), epoch))
			if len(batch) == batchSize {
				flush()
			}
		}
	}
	flush()
	if inserted != total {
		t.Fatalf("inserted %d records, want %d", inserted, total)
	}
	t.Logf("MEASURE seed: %d records via StoreBatchWithSourceTags in %s (%.0f rec/s)",
		total, seedDur, float64(total)/seedDur.Seconds())

	// Hot-window enforcement at scale: 500K ingested, 400K resident.
	resident, err := store.EngineRecordCount("OMM.fbs")
	if err != nil {
		t.Fatalf("EngineRecordCount: %v", err)
	}
	if resident != hotWindow {
		t.Fatalf("engine resident = %d, want %d (hot window)", resident, hotWindow)
	}
	t.Logf("MEASURE hot window: %d ingested -> %d resident in the engine (window %d enforced at ingest)",
		total, resident, hotWindow)

	// Nearest the SECOND epoch so every object resolves to a resident record
	// (the evicted 100K oldest are all first-epoch rows).
	target := baseEpochUnix + 86400 + 43200

	reg := modulert.NewCapabilityRegistry()
	reg.RegisterBridgeAware("storage_query", caps.NewStorageCapFactory(store))

	mount := func(pool int, aotDir string) (*httptest.Server, []*MountedFlow) {
		t.Helper()
		mux := http.NewServeMux()
		mounted, err := RegisterFlowMounts(mux,
			[]config.FlowMount{{Path: "/api/v1/data/", Flow: dist, Pool: pool, MemoryPages: mountPages}},
			FlowMountDeps{
				CapRegistry: reg,
				NodeCtx:     &modulert.NodeContext{},
				AOTCacheDir: aotDir,
				EngineLink:  store,
			})
		if err != nil {
			t.Fatalf("RegisterFlowMounts(pool=%d, aot=%q): %v", pool, aotDir, err)
		}
		if len(mounted) != 1 {
			t.Fatalf("mounted %d flows, want 1", len(mounted))
		}
		return httptest.NewServer(mux), mounted
	}

	get := func(srv *httptest.Server, limit int, format string) (int, int, time.Duration) {
		t.Helper()
		url := fmt.Sprintf("%s/api/v1/data/omm/bulk?epoch=%d&profile=nearest&limit=%d", srv.URL, target, limit)
		if format != "" {
			url += "&format=" + format
		}
		start := time.Now()
		resp, err := http.Get(url)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		dur := time.Since(start)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s -> %d: %.200s", url, resp.StatusCode, body)
		}
		return resp.StatusCode, len(body), dur
	}

	// Both execution modes: interpreted (the C.4 configuration) and AOT
	// (production default since loop C.5b fixed the nested AOT-in-AOT
	// WasmEdge corruption — see TestAOTMountRepro).
	modes := []struct {
		name   string
		aotDir string
	}{
		{"interpreted", ""},
		{"aot", t.TempDir()},
	}
	for _, mode := range modes {
		srv, mounted := mount(4, mode.aotDir)
		execMode := "interpreted"
		if mounted[0].AOT() {
			execMode = "aot"
		}
		for _, limit := range []int{29_000, 250_000} {
			for _, format := range []string{"", "json"} {
				fname := format
				if fname == "" {
					fname = "flatbuffers(default)"
				}
				// Warm once, then measure 3 and report the median-ish middle run.
				get(srv, limit, format)
				var durs []time.Duration
				var size int
				for i := 0; i < 3; i++ {
					_, n, d := get(srv, limit, format)
					durs = append(durs, d)
					size = n
				}
				best := durs[0]
				for _, d := range durs[1:] {
					if d < best {
						best = d
					}
				}
				mb := float64(size) / (1024 * 1024)
				t.Logf("MEASURE omm/bulk [%s flow=%s] limit=%d format=%s: %d bytes (%.1f MB) best=%s runs=%v (%.1f MB/s)",
					mode.name, execMode, limit, fname, size, mb, best, durs, mb/best.Seconds())
			}
		}
		srv.Close()
		for _, mf := range mounted {
			mf.Close()
		}
	}

	// ---- Concurrent throughput: 8 clients, pool=1 vs pool=4 (AOT) ---------
	for _, pool := range []int{1, 4} {
		srv, mounted := mount(pool, t.TempDir())
		const clients = 8
		const perClient = 4
		var totalBytes atomic.Int64
		var wg sync.WaitGroup
		start := time.Now()
		for c := 0; c < clients; c++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < perClient; i++ {
					_, n, _ := get(srv, 29_000, "")
					totalBytes.Add(int64(n))
				}
			}()
		}
		wg.Wait()
		wall := time.Since(start)
		reqs := clients * perClient
		mb := float64(totalBytes.Load()) / (1024 * 1024)
		t.Logf("MEASURE concurrent [pool=%d]: %d clients x %d reqs (limit=29000 fb) in %s -> %.2f req/s, %.1f MB/s aggregate",
			pool, clients, perClient, wall, float64(reqs)/wall.Seconds(), mb/wall.Seconds())
		srv.Close()
		for _, mf := range mounted {
			mf.Close()
		}
	}
}
