package flowrt

// Loop C.5 — WIRESPEED GATE. Standing benchmark comparing:
//
//	(a) the module-served streaming endpoint: the REAL data-retrieval flow
//	    bundle mounted at /api/v1/data/ (config mount table, pooled
//	    instances), serving GET omm/bulk as an aligned $OMM FlatBuffer
//	    stream; every decision inside the wasm flow;
//	(b) a BASELINE handler that streams the exact same pre-materialized
//	    aligned bytes (captured once from the flow for the identical
//	    request) over the same net/http stack — same mux type, same
//	    headers, chunked, flushed like the flow's $HTR pipe;
//	(c) a raw-TCP wire_speed_probe reference: a minimal socket write of the
//	    same bytes (reported alongside, NOT part of the gate).
//
// The gate: streaming (a) must sustain >= 99% of baseline (b) throughput —
// it gates transport/copy overhead added by the wasm-served path, measured
// warm (steady state).
//
// Run:
//
//	SDN_C5_WIRESPEED=1 go test ./internal/flowrt/ -run TestWirespeedGate -v -timeout 60m
//
// or via sdn-server/scripts/wirespeed-gate.sh (seeds, runs, prints the
// table, exits nonzero when the gate fails).
//
// Env knobs:
//
//	SDN_C5_OBJECTS       catalog objects to seed (default 29000)
//	SDN_C5_EPOCHS        epochs per object (default 2)
//	SDN_C5_LIMIT         bulk query limit (default = objects)
//	SDN_C5_RUNS          measured runs per target (default 5)
//	SDN_C5_WARMUP        warmup requests per target (default 2)
//	SDN_C5_POOL          flow mount instance pool (default 1; the gate is a
//	                     single-stream throughput measure)
//	SDN_C5_PAGES         flow instance memory pages (default 4096 = 256 MB)
//	SDN_C5_AOT=0         mount the flow interpreted (default is AOT since
//	                     loop C.5b fixed the nested AOT-in-AOT WasmEdge
//	                     corruption — see TestAOTMountRepro and
//	                     docs/wasmedge-aot-nested-execution.md)
//	SDN_C5_ALLOW_BLOCKED=1  do not fail the test when the gate misses —
//	                     CLEARLY LABELED override for the documented
//	                     known-miss state: with AOT flow dispatch and the
//	                     engine's raw-stream response cache (flatsql
//	                     cc4885c) warm requests measure ~37% of the
//	                     same-bytes HTTP baseline (7 ms vs 2.6 ms for
//	                     8.6 MB); the rest is per-request byte copies in
//	                     the flow-runtime template (hostcall envelope →
//	                     guest queues → $HTR body → host egress). Closing
//	                     it needs the C.3(c3) direct-linkage/zero-copy
//	                     egress work, not benchmark fixes.

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func c5EnvInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// c5Stats reports best / median throughput over a set of run durations for a
// fixed transfer size.
type c5Stats struct {
	size   int
	runs   []time.Duration
	best   time.Duration
	median time.Duration
}

func newC5Stats(size int, runs []time.Duration) c5Stats {
	sorted := append([]time.Duration(nil), runs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return c5Stats{
		size:   size,
		runs:   runs,
		best:   sorted[0],
		median: sorted[len(sorted)/2],
	}
}

func (s c5Stats) mbps(d time.Duration) float64 {
	return float64(s.size) / (1024 * 1024) / d.Seconds()
}

func (s c5Stats) bestMBps() float64   { return s.mbps(s.best) }
func (s c5Stats) medianMBps() float64 { return s.mbps(s.median) }

// timeHTTPGet issues one GET and times request-start → last body byte,
// discarding the body (both benchmark arms use this same client path).
func timeHTTPGet(t *testing.T, url string) (int64, time.Duration) {
	t.Helper()
	start := time.Now()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	n, err := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s -> status %d", url, resp.StatusCode)
	}
	return n, time.Since(start)
}

func TestWirespeedGate(t *testing.T) {
	if os.Getenv("SDN_C5_WIRESPEED") == "" {
		t.Skip("set SDN_C5_WIRESPEED=1 to run the C.5 wirespeed gate")
	}
	dist := dataRetrievalFlowDist(t)

	objects := c5EnvInt("SDN_C5_OBJECTS", 29_000)
	epochs := c5EnvInt("SDN_C5_EPOCHS", 2)
	limit := c5EnvInt("SDN_C5_LIMIT", objects)
	runs := c5EnvInt("SDN_C5_RUNS", 5)
	warmup := c5EnvInt("SDN_C5_WARMUP", 2)
	pool := c5EnvInt("SDN_C5_POOL", 1)
	pages := c5EnvInt("SDN_C5_PAGES", 4096)
	useAOT := os.Getenv("SDN_C5_AOT") != "0"
	allowBlocked := os.Getenv("SDN_C5_ALLOW_BLOCKED") != ""

	const (
		batchSize     = 5_000
		baseEpochUnix = int64(1778400000) // 2026-05-10T00:00:00Z
	)

	// ---- Seed through the normal ingest path -------------------------------
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	defer store.Close()

	tags := storage.SourceTags{ProviderID: "prov-c5", SourceName: "celestrak-gp", BatchID: "batch-c5"}
	total := objects * epochs
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
		n, err := store.StoreBatchWithSourceTags("OMM.fbs", batch, "peer-c5", nil, tags)
		if err != nil {
			t.Fatalf("StoreBatchWithSourceTags after %d records: %v", inserted, err)
		}
		seedDur += time.Since(start)
		inserted += n
		batch = batch[:0]
	}
	for e := 0; e < epochs; e++ {
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
	t.Logf("C5 seed: %d records (%d objects x %d epochs) in %s (%.0f rec/s)",
		total, objects, epochs, seedDur, float64(total)/seedDur.Seconds())

	// Aim at the LAST epoch so nearest resolves inside the hot window even if
	// eviction trimmed the oldest rows.
	target := baseEpochUnix + int64(epochs-1)*86400 + 43200

	// Context (NOT part of the gate): the engine's own query+materialization
	// time for the identical profile query.
	engineStart := time.Now()
	engineStream, err := store.QueryEpochRawStream("OMM.fbs", "", "nearest", float64(target), limit)
	if err != nil {
		t.Fatalf("QueryEpochRawStream: %v", err)
	}
	engineDur := time.Since(engineStart)
	t.Logf("C5 context: engine nearest-epoch query -> %d bytes in %s (%.1f MB/s, compute+materialize, not gated)",
		len(engineStream.Bytes), engineDur, float64(len(engineStream.Bytes))/(1024*1024)/engineDur.Seconds())

	// ---- (a) the module-served streaming endpoint --------------------------
	reg := modulert.NewCapabilityRegistry()
	reg.Register("storage_query", caps.NewStorageCapFactory(store))

	aotDir := ""
	if useAOT {
		aotDir = filepath.Join(t.TempDir(), "aot-cache")
	}
	mux := http.NewServeMux()
	mounted, err := RegisterFlowMounts(mux,
		[]config.FlowMount{{Path: "/api/v1/data/", Flow: dist, Pool: pool, MemoryPages: uint32(pages)}},
		FlowMountDeps{
			CapRegistry: reg,
			NodeCtx:     &modulert.NodeContext{},
			AOTCacheDir: aotDir,
		})
	if err != nil {
		t.Fatalf("RegisterFlowMounts: %v", err)
	}
	defer func() {
		for _, mf := range mounted {
			mf.Close()
		}
	}()
	flowSrv := httptest.NewServer(mux)
	defer flowSrv.Close()
	execMode := "interpreted"
	if mounted[0].AOT() {
		execMode = "aot"
	}

	bulkPath := fmt.Sprintf("/api/v1/data/omm/bulk?epoch=%d&profile=nearest&limit=%d", target, limit)
	flowURL := flowSrv.URL + bulkPath

	// Capture the reference response ONCE: body bytes + headers. The body
	// must be the engine's aligned stream verbatim (C.3d contract) — the
	// baseline serves these exact pre-materialized bytes.
	refResp, err := http.Get(flowURL)
	if err != nil {
		t.Fatalf("GET %s: %v", flowURL, err)
	}
	refBody, err := io.ReadAll(refResp.Body)
	refResp.Body.Close()
	if err != nil {
		t.Fatalf("read reference body: %v", err)
	}
	if refResp.StatusCode != http.StatusOK {
		t.Fatalf("reference GET -> %d: %.300s", refResp.StatusCode, refBody)
	}
	if string(refBody) != string(engineStream.Bytes) {
		t.Fatalf("flow body (%d bytes) != engine aligned stream (%d bytes) — not serving verbatim",
			len(refBody), len(engineStream.Bytes))
	}
	refHeaders := refResp.Header.Clone()
	refHeaders.Del("Date")
	refHeaders.Del("Transfer-Encoding")
	refHeaders.Del("Content-Length")
	sizeMB := float64(len(refBody)) / (1024 * 1024)
	t.Logf("C5 reference response: %d bytes (%.1f MB), content-type %q, %d records",
		len(refBody), sizeMB, refResp.Header.Get("Content-Type"), limit)

	measure := func(url string) c5Stats {
		for i := 0; i < warmup; i++ {
			timeHTTPGet(t, url)
		}
		durs := make([]time.Duration, 0, runs)
		size := int64(0)
		for i := 0; i < runs; i++ {
			n, d := timeHTTPGet(t, url)
			if size == 0 {
				size = n
			} else if n != size {
				t.Fatalf("run %d transferred %d bytes, previous runs %d", i, n, size)
			}
			durs = append(durs, d)
		}
		if size != int64(len(refBody)) {
			t.Fatalf("measured transfer %d bytes, reference %d", size, len(refBody))
		}
		return newC5Stats(int(size), durs)
	}

	flowStats := measure(flowURL)

	// ---- (b) baseline: same bytes, same HTTP stack --------------------------
	// A mux-registered handler on an identical httptest.Server streams the
	// captured bytes with the captured headers, flushed like htrPipe flushes
	// the flow's $HTR frame (chunked; no Content-Length).
	baseMux := http.NewServeMux()
	baseMux.HandleFunc("/api/v1/data/omm/bulk", func(w http.ResponseWriter, r *http.Request) {
		for name, values := range refHeaders {
			for _, v := range values {
				w.Header().Add(name, v)
			}
		}
		w.WriteHeader(http.StatusOK)
		w.Write(refBody)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})
	baseSrv := httptest.NewServer(baseMux)
	defer baseSrv.Close()
	baseStats := measure(baseSrv.URL + bulkPath)

	// ---- (c) raw-TCP wire_speed_probe reference ------------------------------
	// Minimal socket write of the same bytes (no HTTP): the client sends one
	// probe line, the server writes the payload and closes.
	tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("tcp listen: %v", err)
	}
	defer tcpLn.Close()
	go func() {
		for {
			conn, err := tcpLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				// Consume the probe request line, then write the payload.
				if _, err := bufio.NewReader(c).ReadString('\n'); err != nil {
					return
				}
				c.Write(refBody)
			}(conn)
		}
	}()
	tcpProbe := func() time.Duration {
		start := time.Now()
		conn, err := net.Dial("tcp", tcpLn.Addr().String())
		if err != nil {
			t.Fatalf("tcp dial: %v", err)
		}
		defer conn.Close()
		if _, err := conn.Write([]byte("wire_speed_probe\n")); err != nil {
			t.Fatalf("tcp write: %v", err)
		}
		n, err := io.Copy(io.Discard, conn)
		if err != nil {
			t.Fatalf("tcp read: %v", err)
		}
		if n != int64(len(refBody)) {
			t.Fatalf("tcp read %d bytes, want %d", n, len(refBody))
		}
		return time.Since(start)
	}
	for i := 0; i < warmup; i++ {
		tcpProbe()
	}
	tcpDurs := make([]time.Duration, 0, runs)
	for i := 0; i < runs; i++ {
		tcpDurs = append(tcpDurs, tcpProbe())
	}
	tcpStats := newC5Stats(len(refBody), tcpDurs)

	// ---- Report + gate -------------------------------------------------------
	pctBest := flowStats.bestMBps() / baseStats.bestMBps() * 100
	pctMedian := flowStats.medianMBps() / baseStats.medianMBps() * 100

	t.Logf("C5 WIRESPEED GATE — %d records, %d bytes (%.1f MB) per transfer, %d runs (+%d warmup), flow=%s pool=%d",
		limit, len(refBody), sizeMB, runs, warmup, execMode, pool)
	t.Logf("C5   (a) flow endpoint : best %8.1f MB/s (%v)  median %8.1f MB/s (%v)  runs=%v",
		flowStats.bestMBps(), flowStats.best, flowStats.medianMBps(), flowStats.median, flowStats.runs)
	t.Logf("C5   (b) HTTP baseline : best %8.1f MB/s (%v)  median %8.1f MB/s (%v)  runs=%v",
		baseStats.bestMBps(), baseStats.best, baseStats.medianMBps(), baseStats.median, baseStats.runs)
	t.Logf("C5   (c) raw TCP ref   : best %8.1f MB/s (%v)  median %8.1f MB/s (%v)  (reference only)",
		tcpStats.bestMBps(), tcpStats.best, tcpStats.medianMBps(), tcpStats.median)
	t.Logf("C5   gate: flow/baseline = %.2f%% (best) / %.2f%% (median); requirement >= 99%%", pctBest, pctMedian)

	if pctBest >= 99.0 {
		t.Logf("C5 GATE: PASS (%.2f%% of baseline)", pctBest)
		return
	}
	msg := fmt.Sprintf("C5 GATE: FAIL — flow-served streaming is %.2f%% (best) / %.2f%% (median) of the raw baseline, requirement >= 99%%. "+
		"Flow executed %s.", pctBest, pctMedian, execMode)
	if allowBlocked {
		t.Logf("%s", msg)
		t.Logf("C5 GATE OVERRIDE: SDN_C5_ALLOW_BLOCKED=1 set — reporting the known-miss state instead of failing " +
			"(AOT dispatch + engine raw-stream response cache landed in loop C.5b; the remaining gap is " +
			"per-request byte copies inside the flow-runtime template — tracked with the C.3(c3) " +
			"direct-linkage/zero-copy egress work)")
		return
	}
	t.Fatal(msg)
}
