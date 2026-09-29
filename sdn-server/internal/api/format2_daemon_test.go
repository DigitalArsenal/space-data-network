package api

// T6 #3 through the daemon's own API on store format 2: while one writer
// ingests into 50 partitions, the dashboard's 5 s stats lane never serves a
// stale snapshot, /api/v1/stats and /api/v1/data/index answer every request,
// and the lane's store work (DataSummary, SourceBatchProgress,
// DiskUsageBytes) stays within the counter-read budget. The store runs the
// host-02 topology (1 writer, 2 interactive lanes, 1 bulk lane).
//
//	SDN_FORMAT2_API_FOR=30m   the ingest duration (default 20s)

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

type f2Latencies struct {
	mu sync.Mutex
	d  []time.Duration
}

func (l *f2Latencies) add(d time.Duration) {
	l.mu.Lock()
	l.d = append(l.d, d)
	l.mu.Unlock()
}

func (l *f2Latencies) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.d) == 0 {
		return "no samples"
	}
	s := append([]time.Duration(nil), l.d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	q := func(p float64) time.Duration { return s[int(float64(len(s)-1)*p)] }
	return fmt.Sprintf("n=%d p50=%s p99=%s max=%s", len(s), q(0.5).Round(time.Microsecond), q(0.99).Round(time.Microsecond), s[len(s)-1].Round(time.Microsecond))
}

func (l *f2Latencies) p99() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), l.d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[int(float64(len(s)-1)*0.99)]
}

func TestFormat2DashboardLaneNeverStaleDuringIngest(t *testing.T) {
	if !flatsqlrt.NativeHostIOSupported() {
		t.Skip("the C host I/O module is not available on this platform")
	}
	if rep := wasmrt.SubstrateStatus(); !rep.Patched() {
		if os.Getenv("SDN_WASM_REQUIRE_PATCHED") == "1" {
			t.Fatalf("runtime is not patched: %+v", rep)
		}
		t.Skipf("linked libwasmedge lacks the SDN runtime patches (%+v)", rep)
	}
	// What `spacedatanetwork prewarm-aot` does: the daemon never compiles.
	if _, _, err := flatsqlrt.PrewarmPSThreadsAOT(storage.EngineAOTCacheDir()); err != nil {
		t.Fatalf("prewarm the partition-store engine: %v", err)
	}
	t.Setenv(format2.FormatEnv, "2")
	t.Setenv("SDN_FORMAT2_TOPOLOGY", "1/2/1")
	t.Setenv("SDN_FLATSQL_CHECKPOINT_INTERVAL", "0")
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), v)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if !store.Format2() {
		t.Fatal("SDN_STORE_FORMAT=2 opened a format-1 store")
	}

	core := NewCoreAPIHandler("", nil, nil, nil, store, v, nil, nil, nil)
	data := NewDataQueryHandler(store)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/stats", core.handleStats)
	mux.HandleFunc("/api/v1/data/index", data.handleRecordIndex)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// A populated store before the lane starts.
	ingestBatch := func(peer int, seq int, n int) error {
		base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
		recs := make([][]byte, n)
		for i := range recs {
			k := seq + i
			recs[i] = sds.NewOMMBuilder().WithNoradCatID(uint32(1_000_000 + peer*100_000 + k%100_000)).
				WithObjectName(fmt.Sprintf("I%d-%d", peer, k)).WithEpoch(base.Add(time.Duration(k) * time.Second).Format("2006-01-02T15:04:05Z")).
				WithMeanMotion(15.5).Build()[4:]
		}
		_, err := store.StoreBatchWithSourceTags("OMM.fbs", recs, fmt.Sprintf("source:ingest%02d", peer), nil,
			storage.SourceTags{ProviderID: "prov", SourceName: fmt.Sprintf("ingest%02d", peer), BatchID: "live"})
		return err
	}
	for p := 0; p < 50; p++ {
		if err := ingestBatch(p, 0, 200); err != nil {
			t.Fatal(err)
		}
	}
	core.StartDashboardSnapshots()
	defer core.StopDashboardSnapshots()
	// Every answer below comes from the lane. Before its first build the JSON
	// surface reads inline, and that read can be newer than the lane's first
	// build, which then lands with its own (older) as_of.
	for deadline := time.Now().Add(30 * time.Second); !core.cachedStoreStats().Built; {
		if time.Now().After(deadline) {
			t.Fatal("the stats lane never built")
		}
		time.Sleep(10 * time.Millisecond)
	}

	dur := 20 * time.Second
	if d, err := time.ParseDuration(os.Getenv("SDN_FORMAT2_API_FOR")); err == nil && d > 0 {
		dur = d
	}
	ctx, cancel := context.WithTimeout(context.Background(), dur)
	defer cancel()
	var ingested atomic.Int64
	var ingestErrs atomic.Int64
	var wg sync.WaitGroup
	for p := 0; p < 50; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for seq := 200; ctx.Err() == nil; seq += 100 {
				if err := ingestBatch(p, seq, 100); err != nil {
					ingestErrs.Add(1)
					return
				}
				ingested.Add(100)
			}
		}(p)
	}

	var statsLat, indexLat, laneLat f2Latencies
	var stale, failures atomic.Int64
	var firstFailure atomic.Value
	var lastTotal atomic.Int64
	var readers sync.WaitGroup
	readers.Add(3)
	go func() { // the JSON surface, as a browser polls it
		defer readers.Done()
		for ctx.Err() == nil {
			start := time.Now()
			resp, err := http.Get(srv.URL + "/api/v1/stats")
			if err != nil {
				failures.Add(1)
				firstFailure.CompareAndSwap(nil, err.Error())
				continue
			}
			var body struct {
				TotalRecords int64  `json:"total_records"`
				Stale        bool   `json:"stale"`
				AsOf         string `json:"as_of"`
			}
			err = json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			statsLat.add(time.Since(start))
			if err != nil || resp.StatusCode != http.StatusOK {
				failures.Add(1)
				firstFailure.CompareAndSwap(nil, fmt.Sprintf("stats: %d %v", resp.StatusCode, err))
				continue
			}
			asOf, perr := time.Parse(time.RFC3339, body.AsOf)
			if body.Stale || perr != nil || time.Since(asOf) > dashboardStatsInterval+2*time.Second {
				stale.Add(1)
				firstFailure.CompareAndSwap(nil, fmt.Sprintf("stale snapshot: stale=%v as_of=%q", body.Stale, body.AsOf))
			}
			if prev := lastTotal.Load(); body.TotalRecords < prev {
				failures.Add(1)
				firstFailure.CompareAndSwap(nil, fmt.Sprintf("total records went back from %d to %d", prev, body.TotalRecords))
			}
			lastTotal.Store(body.TotalRecords)
			time.Sleep(250 * time.Millisecond)
		}
	}()
	go func() { // the anonymous record index page
		defer readers.Done()
		for i := 0; ctx.Err() == nil; i++ {
			start := time.Now()
			resp, err := http.Get(fmt.Sprintf("%s/api/v1/data/index?schema=OMM.fbs&source_name=ingest%02d&limit=50&page=%d", srv.URL, i%50, 1+i%5))
			if err != nil {
				failures.Add(1)
				continue
			}
			resp.Body.Close()
			indexLat.add(time.Since(start))
			if resp.StatusCode != http.StatusOK {
				failures.Add(1)
				firstFailure.CompareAndSwap(nil, fmt.Sprintf("data/index answered %d", resp.StatusCode))
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	go func() { // the lane's own store work, sampled
		defer readers.Done()
		for ctx.Err() == nil {
			start := time.Now()
			s := core.readStoreStats()
			laneLat.add(time.Since(start))
			if s.Stale {
				stale.Add(1)
				firstFailure.CompareAndSwap(nil, "readStoreStats reported a stale read")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	wg.Wait()
	readers.Wait()
	t.Logf("MEASURED dashboard lane through the API on format 2 (%s, 1 writer, 50 partitions, 2 interactive lanes; %s/%s, %d CPUs):",
		dur, runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
	t.Logf("  ingested %d records (%.0f/s); ingest errors %d", ingested.Load(), float64(ingested.Load())/dur.Seconds(), ingestErrs.Load())
	t.Logf("  GET /api/v1/stats %s", statsLat.String())
	t.Logf("  GET /api/v1/data/index (source-filtered page) %s", indexLat.String())
	t.Logf("  stats lane store work (DataSummary + SourceBatchProgress + DiskUsageBytes) %s", laneLat.String())
	t.Logf("  stale snapshots %d, failures %d", stale.Load(), failures.Load())
	if ingestErrs.Load() != 0 {
		t.Fatalf("%d ingest goroutines failed", ingestErrs.Load())
	}
	if stale.Load() != 0 || failures.Load() != 0 {
		t.Fatalf("%d stale snapshots, %d failures (first: %v)", stale.Load(), failures.Load(), firstFailure.Load())
	}
	if lastTotal.Load() <= 50*200 {
		t.Fatalf("the dashboard never showed the ingest (last total %d)", lastTotal.Load())
	}
	if os.Getenv("SDN_PS_ACCEPTANCE") == "1" && laneLat.p99() > 10*time.Millisecond {
		t.Fatalf("stats lane store work p99 %s > 10 ms", laneLat.p99())
	}
}

// T6 #3 and #4 at full duration, through the daemon, on a migrated
// host-02-sized store: one writer ingests into 50 new partitions (OMM from
// 50 producers) for the whole run while the readers take the counters, the
// OMM window, the IQC source byte probe and first payload byte, GetRecord,
// the lanes' queue wait, the dashboard lane through /api/v1/stats and the
// record index page, and the two incident shapes replay: the 2026-09-26
// MPE SUM (the public SQL statement, on the sandbox lanes) and the
// 2026-09-27 IQC source window (the daemon's window of 1,000 records).
//
//	SDN_FORMAT2_SOAK_STORE=<a clone of a migrated store; the run writes it>
//	SDN_FORMAT2_SOAK_FOR=30m     (default 5m)
//	SDN_FORMAT2_TOPOLOGY=1/2/1   (default: host-02's)
func TestFormat2SoakThroughTheDaemon(t *testing.T) {
	root := os.Getenv("SDN_FORMAT2_SOAK_STORE")
	if root == "" {
		t.Skip("SDN_FORMAT2_SOAK_STORE names a migrated store clone")
	}
	if rep := wasmrt.SubstrateStatus(); !rep.Patched() {
		t.Skipf("linked libwasmedge lacks the SDN runtime patches (%+v)", rep)
	}
	if _, _, err := flatsqlrt.PrewarmPSThreadsAOT(storage.EngineAOTCacheDir()); err != nil {
		t.Fatal(err)
	}
	t.Setenv(format2.FormatEnv, "2")
	if os.Getenv("SDN_FORMAT2_TOPOLOGY") == "" {
		t.Setenv("SDN_FORMAT2_TOPOLOGY", "1/2/1")
	}
	dur := 5 * time.Minute
	if d, err := time.ParseDuration(os.Getenv("SDN_FORMAT2_SOAK_FOR")); err == nil && d > 0 {
		dur = d
	}
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	opened := time.Now()
	store, err := storage.NewFlatSQLStore(root, v)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	t.Logf("opened %s in %s", root, time.Since(opened).Round(time.Millisecond))
	ps := store.PartitionStore()

	sample, err := store.QueryIndexedRecords(storage.IndexedRecordQuery{SchemaName: "OMM.fbs", Limit: 1000})
	if err != nil || len(sample) == 0 {
		t.Fatalf("sample window: %d, %v", len(sample), err)
	}

	var seq atomic.Int64
	ingest := func(ctx context.Context, n *atomic.Int64, errs *atomic.Int64, firstErr *atomic.Value) *sync.WaitGroup {
		var wg sync.WaitGroup
		base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
		for p := 0; p < 50; p++ {
			wg.Add(1)
			go func(p int) {
				defer wg.Done()
				for ctx.Err() == nil {
					k0 := int(seq.Add(100)) - 100
					recs := make([][]byte, 100)
					for i := range recs {
						k := k0 + i
						recs[i] = sds.NewOMMBuilder().WithNoradCatID(uint32(3_000_000 + k)).
							WithObjectName(fmt.Sprintf("S%d-%d", p, k)).WithEpoch(base.Add(time.Duration(k) * time.Second).Format("2006-01-02T15:04:05Z")).
							WithMeanMotion(15.5).Build()[4:]
					}
					if _, err := store.StoreBatchWithSourceTags("OMM.fbs", recs, fmt.Sprintf("source:soak%02d", p), nil,
						storage.SourceTags{ProviderID: "soak", SourceName: fmt.Sprintf("soak%02d", p), BatchID: "live"}); err != nil {
						errs.Add(1)
						firstErr.CompareAndSwap(nil, err.Error())
						return
					}
					n.Add(100)
				}
			}(p)
		}
		return &wg
	}

	// The no-read baseline.
	baseDur := max(dur/6, 60*time.Second)
	var baseN, baseErrs atomic.Int64
	var baseFirst atomic.Value
	bctx, bcancel := context.WithTimeout(context.Background(), baseDur)
	ingest(bctx, &baseN, &baseErrs, &baseFirst).Wait()
	bcancel()
	baseRate := float64(baseN.Load()) / baseDur.Seconds()
	if baseErrs.Load() != 0 {
		t.Fatalf("baseline ingest: %d errors (first: %v)", baseErrs.Load(), baseFirst.Load())
	}

	core := NewCoreAPIHandler("", nil, nil, nil, store, v, nil, nil, nil)
	data := NewDataQueryHandler(store)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/stats", core.handleStats)
	mux.HandleFunc("/api/v1/data/index", data.handleRecordIndex)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	core.StartDashboardSnapshots()
	defer core.StopDashboardSnapshots()
	for deadline := time.Now().Add(60 * time.Second); !core.cachedStoreStats().Built; {
		if time.Now().After(deadline) {
			t.Fatal("the stats lane never built")
		}
		time.Sleep(10 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), dur)
	defer cancel()
	var n, ingestErrs atomic.Int64
	var ingestFirst atomic.Value
	loadStart := soakLoad()
	wg := ingest(ctx, &n, &ingestErrs, &ingestFirst)

	type reader struct {
		name  string
		pause time.Duration
		fn    func() error
		lat   f2Latencies
		errs  atomic.Int64
		first atomic.Value
	}
	errStop := fmt.Errorf("first frame")
	var stale atomic.Int64
	var lastTotal atomic.Int64
	rng := rand.New(rand.NewSource(1))
	var rngMu sync.Mutex
	readers := []*reader{
		{name: "LiveRecordBytes", pause: 10 * time.Millisecond, fn: func() error { _, err := store.LiveRecordBytes(); return err }},
		{name: "DataSummary", pause: 10 * time.Millisecond, fn: func() error { _, err := store.DataSummary(); return err }},
		{name: "OMM window LIMIT 1000", pause: 10 * time.Millisecond, fn: func() error {
			_, err := store.QueryIndexedRecords(storage.IndexedRecordQuery{SchemaName: "OMM.fbs", Limit: 1000})
			return err
		}},
		{name: "IQC source byte probe", pause: 10 * time.Millisecond, fn: func() error {
			_, _, err := store.IndexedRecordWindowLimitForBytes(storage.IndexedRecordQuery{SchemaName: "IQC.fbs", SourceName: "IQEngine", Limit: 1000}, 1<<40)
			return err
		}},
		{name: "IQC source first payload byte", pause: 10 * time.Millisecond, fn: func() error {
			_, err := ps.StreamTrusted(context.Background(), `SELECT _data FROM "IQC" WHERE _source_name = ?1 ORDER BY _epoch DESC LIMIT 1000`,
				[]format2.Cell{format2.Text("IQEngine")}, func([]byte) error { return errStop })
			if err == errStop {
				return nil
			}
			return err
		}},
		{name: "incident 09-27: IQC source window (1,000 records)", pause: 50 * time.Millisecond, fn: func() error {
			recs, err := store.QueryIndexedRecords(storage.IndexedRecordQuery{SchemaName: "IQC.fbs", SourceName: "IQEngine", Limit: 1000})
			if err == nil && len(recs) != 1000 {
				err = fmt.Errorf("%d records", len(recs))
			}
			return err
		}},
		{name: "incident 09-26: SELECT SUM(_len) FROM MPE (public SQL)", pause: 100 * time.Millisecond, fn: func() error {
			_, _, _, err := store.QuerySandboxedJSON(`SELECT COUNT(*), SUM(_len) FROM MPE`, flatsqlrt.SandboxCaps{Timeout: 5 * time.Minute})
			return err
		}},
		{name: "GetRecord", pause: time.Millisecond, fn: func() error {
			rngMu.Lock()
			r := sample[rng.Intn(len(sample))]
			rngMu.Unlock()
			_, err := store.GetRecord("OMM.fbs", r.CID)
			return err
		}},
		{name: "reader wait: SELECT 1 on the interactive lanes", pause: 5 * time.Millisecond, fn: func() error {
			_, err := ps.Query(context.Background(), format2.Request{SQL: "SELECT 1"})
			return err
		}},
		{name: "reader wait: SELECT 1 on the point lanes", pause: 5 * time.Millisecond, fn: func() error {
			_, err := ps.QueryPoint(context.Background(), format2.Request{SQL: "SELECT 1"})
			return err
		}},
		{name: "GET /api/v1/stats", pause: 250 * time.Millisecond, fn: func() error {
			resp, err := http.Get(srv.URL + "/api/v1/stats")
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			var body struct {
				TotalRecords int64  `json:"total_records"`
				Stale        bool   `json:"stale"`
				AsOf         string `json:"as_of"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || resp.StatusCode != http.StatusOK {
				return fmt.Errorf("stats: %d %v", resp.StatusCode, err)
			}
			asOf, perr := time.Parse(time.RFC3339, body.AsOf)
			if body.Stale || perr != nil || time.Since(asOf) > dashboardStatsInterval+2*time.Second {
				stale.Add(1)
			}
			if prev := lastTotal.Load(); body.TotalRecords < prev {
				return fmt.Errorf("total records went back from %d to %d", prev, body.TotalRecords)
			}
			lastTotal.Store(body.TotalRecords)
			return nil
		}},
		{name: "GET /api/v1/data/index (source page)", pause: 50 * time.Millisecond, fn: func() error {
			resp, err := http.Get(srv.URL + "/api/v1/data/index?schema=IQC.fbs&source_name=IQEngine&limit=50&page=3")
			if err != nil {
				return err
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("data/index answered %d", resp.StatusCode)
			}
			return nil
		}},
	}
	var rwg sync.WaitGroup
	for _, r := range readers {
		rwg.Add(1)
		go func(r *reader) {
			defer rwg.Done()
			for ctx.Err() == nil {
				start := time.Now()
				if err := r.fn(); err != nil {
					if ctx.Err() != nil {
						return
					}
					r.errs.Add(1)
					r.first.CompareAndSwap(nil, err.Error())
				} else {
					r.lat.add(time.Since(start))
				}
				time.Sleep(r.pause)
			}
		}(r)
	}
	wg.Wait()
	rwg.Wait()
	rate := float64(n.Load()) / dur.Seconds()
	storeLock, insts := ps.Health()

	t.Logf("MEASURED T6 #3/#4 through the daemon (%s, %s; 1 writer, 50 ingest partitions over the fixture's %s; load %.1f at start, %.1f at end):",
		dur, soakMachine(), os.Getenv("SDN_FORMAT2_TOPOLOGY"), loadStart, soakLoad())
	t.Logf("  ingest %.0f records/s with reads, %.0f records/s alone (%.0f%%); %d records; ingest errors %d (first: %v)",
		rate, baseRate, 100*rate/baseRate, n.Load(), ingestErrs.Load(), ingestFirst.Load())
	failed := false
	for _, r := range readers {
		t.Logf("  %-56s %s; errors %d%s", r.name, r.lat.String(), r.errs.Load(), soakFirst(&r.first))
		if r.errs.Load() != 0 && !strings.HasPrefix(r.name, "incident 09-26") {
			failed = true
		}
	}
	t.Logf("  stale dashboard snapshots %d", stale.Load())
	t.Logf("  store path registry lock: %d acquisitions, max hold %s", storeLock.LockAcquisitions, storeLock.LockHoldMax)
	poisoned := 0
	for _, in := range insts {
		t.Logf("  instance %-11s %-8s poisoned=%v host I/O lock: %d acquisitions, max hold %s", in.Name, in.State, in.Poisoned,
			in.LockAcquisitions, in.LockHoldMax)
		if in.Poisoned || in.State != "live" {
			poisoned++
		}
	}
	if ingestErrs.Load() != 0 || stale.Load() != 0 || poisoned != 0 || failed {
		t.Fatalf("ingest errors %d, stale snapshots %d, poisoned instances %d, reader errors %v", ingestErrs.Load(), stale.Load(), poisoned, failed)
	}
}

func soakFirst(v *atomic.Value) string {
	if s, ok := v.Load().(string); ok {
		if len(s) > 160 {
			s = s[:160]
		}
		return " (first: " + s + ")"
	}
	return ""
}

func soakLoad() float64 {
	out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
	if err != nil {
		raw, err := os.ReadFile("/proc/loadavg")
		if err != nil {
			return -1
		}
		out = raw
	}
	f := strings.Fields(strings.Trim(string(out), "{} \n"))
	if len(f) == 0 {
		return -1
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

func soakMachine() string {
	return fmt.Sprintf("%s/%s, %d CPUs", runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
}
