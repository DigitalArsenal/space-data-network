//go:build stress
// +build stress

package stress

// Host-02-shaped measurement of the ReplaceCurrent supersede
// (storage.SupersedeSourceBatches), per statement and per store-lock hold,
// with API readers running against the store the whole time. Run from
// sdn-server/ (the thresholds are read at process start, so they go in the
// environment):
//
//	SDN_FLATSQL_SLOW_QUERY_MS=1 SDN_FLATSQL_SLOW_LOCK_MS=1 \
//	STRESS_SHAPE_TEMPLATE=<dir> SUPERSEDE_SHAPE_OUT=<report.json> \
//	  nice -n 10 ../scripts/go-with-wasmedge.sh test -tags=stress -timeout=6h \
//	  -run '^TestSupersedeShapeHost02$' -v ./internal/stress/
//
// The shape is host-02's $OMM as it stood when its retention block started
// superseding (2026-09-29): the celestrak-gp lane of 1,696,780 records over 53
// batches in sds_p_source_celestrak__OMM, and a replicated dataset lane of six
// 32,324-record batches in the second producer table
// (sds_p_<host-01 peer>__OMM). Keeping the newest dataset batch evicts the
// other five: 161,620 tags and records, which is the 78-79 chunks of 2,048
// host-02 logged. The engine hot window is the default 400,000 $OMM rows.
//
// STRESS_SHAPE_TEMPLATE holds the populated store (built on first use, then
// cloned per run with APFS clonefile), so a before/after pair measures the
// same bytes. SUPERSEDE_SHAPE_HYDRATED names a second template: the populated
// store after one boot and engine hot-window hydration (45 minutes on the
// owner's machine), saved by the first run that finds it empty and cloned by
// every later one. SUPERSEDE_SHAPE_SCALE scales the celestrak lane only; the
// dataset lane is always six full batches.

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	logging "github.com/ipfs/go-log/v2"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	supersedeShapeDatasetPeer     = shapeMirrorPeer // host-01, the dataset's publisher
	supersedeShapeDatasetProvider = "space-data-network-01"
	supersedeShapeDatasetSource   = "celestrak-gp-dataset"
	supersedeShapeBatchRecords    = 32_324
	supersedeShapeBatches         = 6
	supersedeShapeChunkLimit      = time.Second // the acceptance bound per lock hold
)

// Host02SupersedeShape is the $OMM part of host-02 that the supersede walks.
func Host02SupersedeShape(celestrakScale float64) []ShapePartition {
	celestrak := ScaleShape([]ShapePartition{{
		Schema: "OMM.fbs", Producer: "source:celestrak", ProviderID: shapeProvider, SourceName: "celestrak-gp",
		Batches: 53, Records: 1_696_780, RecordBytes: 324,
	}}, celestrakScale)
	return append(celestrak, ShapePartition{
		Schema: "OMM.fbs", Producer: supersedeShapeDatasetPeer, ProviderID: supersedeShapeDatasetProvider,
		SourceName: supersedeShapeDatasetSource, Batches: supersedeShapeBatches,
		Records: supersedeShapeBatches * supersedeShapeBatchRecords, RecordBytes: 324,
	})
}

// supersedeLogs groups the engine's per-statement and the store's per-lock
// log lines by statement shape and lock site.
type supersedeLogs struct {
	pr         *logging.PipeReader
	done       chan struct{}
	mu         sync.Mutex
	statements map[string]*LatencySet
	holds      map[string]*LatencySet
	waits      map[string]*LatencySet
}

var (
	reShapeProducerTable = regexp.MustCompile(`sds_p_[A-Za-z0-9_:]+__OMM`)
	reShapeInList        = regexp.MustCompile(`\((\?, )+\?\)`)
)

func startSupersedeLogs() *supersedeLogs {
	_ = logging.SetLogLevel("storage", "info")
	_ = logging.SetLogLevel("flatsqlrt", "info")
	c := &supersedeLogs{
		pr:         logging.NewPipeReader(logging.PipeFormat(logging.PlaintextOutput), logging.PipeLevel(logging.LevelInfo)),
		done:       make(chan struct{}),
		statements: map[string]*LatencySet{},
		holds:      map[string]*LatencySet{},
		waits:      map[string]*LatencySet{},
	}
	go func() {
		defer close(c.done)
		buf := make([]byte, 0, 64<<10)
		var line []byte
		for {
			n, err := c.pr.Read(buf[:cap(buf)])
			for _, b := range buf[:n] {
				if b == '\n' {
					c.observe(string(line))
					line = line[:0]
					continue
				}
				line = append(line, b)
			}
			if err != nil {
				return
			}
		}
	}()
	return c
}

func (c *supersedeLogs) set(m map[string]*LatencySet, key string) *LatencySet {
	if m[key] == nil {
		m[key] = &LatencySet{}
	}
	return m[key]
}

func (c *supersedeLogs) observe(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m := reSlowStatement.FindStringSubmatch(line); m != nil {
		held, err := time.ParseDuration(m[1])
		if err != nil {
			return
		}
		sql := strings.Join(strings.Fields(m[3]), " ")
		sql = reShapeProducerTable.ReplaceAllString(sql, "sds_p_<producer>__OMM")
		sql = reShapeInList.ReplaceAllString(sql, "(?…)")
		if len(sql) > 200 {
			sql = sql[:200] + "…"
		}
		c.set(c.statements, sql).Add(held, nil)
		return
	}
	if m := reSlowLock.FindStringSubmatch(line); m != nil {
		held, _ := time.ParseDuration(m[3])
		waited, _ := time.ParseDuration(m[4])
		if m[1] == "write" {
			c.set(c.holds, m[2]).Add(held, nil)
		} else {
			c.set(c.waits, m[2]).Add(waited, nil)
		}
	}
}

func (c *supersedeLogs) stop() {
	_ = c.pr.Close()
	<-c.done
}

type supersedeShapeLine struct {
	Name    string
	Summary LatencySummary
}

func summarize(m map[string]*LatencySet) []supersedeShapeLine {
	out := make([]supersedeShapeLine, 0, len(m))
	for k, v := range m {
		out = append(out, supersedeShapeLine{Name: k, Summary: v.Summary()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Summary.Total > out[j].Summary.Total })
	return out
}

func TestSupersedeShapeHost02(t *testing.T) {
	parts := Host02SupersedeShape(shapeEnvFloat("SUPERSEDE_SHAPE_SCALE", 1))
	workDir := strings.TrimSpace(os.Getenv("SUPERSEDE_SHAPE_DIR"))
	if workDir == "" {
		workDir = t.TempDir()
	} else if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hydratedTemplate := strings.TrimSpace(os.Getenv("SUPERSEDE_SHAPE_HYDRATED"))
	hydratedStore := filepath.Join(hydratedTemplate, "store")
	haveHydrated := false
	if hydratedTemplate != "" {
		if _, err := os.Stat(filepath.Join(hydratedTemplate, "hydrated.txt")); err == nil {
			haveHydrated = true
		}
	}
	var base string
	var stats PopulateStats
	if haveHydrated {
		base = filepath.Join(workDir, "store")
		_ = os.RemoveAll(base)
		if out, err := exec.Command("cp", "-cR", hydratedStore, base).CombinedOutput(); err != nil {
			t.Fatalf("clone hydrated template: %v: %s", err, out)
		}
		t.Logf("cloned hydrated store %s", hydratedStore)
	} else {
		base, stats = prepareShapeStore(t, workDir, parts)
	}
	report := &ShapeReport{
		Build: os.Getenv("STRESS_SHAPE_BUILD"), Scale: shapeEnvFloat("SUPERSEDE_SHAPE_SCALE", 1), Shape: parts,
		Populate: stats, CPUs: runtime.NumCPU(), StartedAt: time.Now().UTC(), Path: os.Getenv("SUPERSEDE_SHAPE_OUT"),
	}
	if os.Getenv("SUPERSEDE_SHAPE_POPULATE_ONLY") != "" {
		t.Logf("populated %d records in %s", stats.Records, stats.Duration)
		return
	}

	store := openShapeStore(t, base)
	hydrateStart := time.Now()
	hydrated, err := store.HydrateEngineHotWindowContext(context.Background())
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	t.Logf("hydrated %d engine rows in %s", hydrated, time.Since(hydrateStart))
	if hydratedTemplate != "" && !haveHydrated {
		if err := store.Close(); err != nil {
			t.Fatalf("close after hydration: %v", err)
		}
		_ = os.RemoveAll(hydratedStore)
		if err := os.MkdirAll(hydratedTemplate, 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("cp", "-cR", base, hydratedStore).CombinedOutput(); err != nil {
			t.Fatalf("save hydrated template: %v: %s", err, out)
		}
		if err := os.WriteFile(filepath.Join(hydratedTemplate, "hydrated.txt"), []byte(fmt.Sprintf("hydrated=%d took=%s\n", hydrated, time.Since(hydrateStart))), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("saved hydrated template %s", hydratedStore)
		if os.Getenv("SUPERSEDE_SHAPE_HYDRATE_ONLY") != "" {
			return
		}
		store = openShapeStore(t, base)
		if _, err := store.HydrateEngineHotWindowContext(context.Background()); err != nil {
			t.Fatalf("hydrate after reopen: %v", err)
		}
	}
	defer store.Close()

	dataset := parts[len(parts)-1]
	celestrak := parts[0]
	keep := dataset.BatchID(dataset.Records - 1)

	// Readers: records the supersede never touches (the celestrak lane), read
	// by CID and by a batch page, plus the summary the dashboard polls.
	cids := make([]string, 0, 256)
	for i := 0; i < 256; i++ {
		if data, err := BuildShapeRecord(celestrak, (i*104729)%celestrak.Records); err == nil {
			cids = append(cids, storage.ComputeCID(data))
		}
	}
	perBatch := celestrak.Records / maxInt(celestrak.Batches, 1)
	ops := []struct {
		name string
		fn   func(*rand.Rand) error
	}{
		{"OMM by CID", func(rng *rand.Rand) error {
			_, err := store.GetRecord("OMM.fbs", cids[rng.Intn(len(cids))])
			return err
		}},
		{"OMM batch page (250)", func(rng *rand.Rand) error {
			_, err := store.QueryIndexedRecords(storage.IndexedRecordQuery{
				SchemaName: "OMM.fbs", ProviderID: celestrak.ProviderID, SourceName: celestrak.SourceName,
				BatchID: celestrak.BatchID(0), Limit: shapeAPIPage, Offset: rng.Intn(maxInt(perBatch-shapeAPIPage, 1)),
			})
			return err
		}},
	}
	// The readers share ONE single-threaded engine, so a reader's latency is
	// also the other readers' statements. A baseline phase with the same
	// readers and no supersede separates that from what the supersede adds.
	readers := shapeEnvInt("SUPERSEDE_SHAPE_READERS", 2)
	startReaders := func() (stop func(), sets []*LatencySet) {
		sets = make([]*LatencySet, len(ops))
		for i := range sets {
			sets[i] = &LatencySet{}
		}
		var halt atomic.Bool
		var wg sync.WaitGroup
		for r := 0; r < readers; r++ {
			wg.Add(1)
			go func(seed int64) {
				defer wg.Done()
				rng := rand.New(rand.NewSource(seed))
				for !halt.Load() {
					i := rng.Intn(len(ops))
					started := time.Now()
					err := ops[i].fn(rng)
					sets[i].Add(time.Since(started), err)
					time.Sleep(time.Duration(5+rng.Intn(20)) * time.Millisecond)
				}
			}(int64(r + 1))
		}
		return func() { halt.Store(true); wg.Wait() }, sets
	}
	stopBaseline, baselineSets := startReaders()
	time.Sleep(shapeEnvDuration("SUPERSEDE_SHAPE_BASELINE", time.Minute))
	stopBaseline()

	logs := startSupersedeLogs()
	stopReaders, readerSets := startReaders()
	load := LoadAverage1()
	started := time.Now()
	result, supersedeErr := store.SupersedeSourceBatches(dataset.Schema, dataset.ProviderID, dataset.SourceName, keep)
	took := time.Since(started)
	stopReaders()
	// Let the last log lines drain.
	time.Sleep(200 * time.Millisecond)
	logs.stop()
	if supersedeErr != nil {
		t.Fatalf("SupersedeSourceBatches: %v", supersedeErr)
	}

	wantEvicted := int64((supersedeShapeBatches - 1) * supersedeShapeBatchRecords)
	report.Add(ShapeMetric{
		Name:      "supersede result",
		Value:     fmt.Sprintf("%d tags, %d records, %d files in %s (keep %s)", result.TagsDeleted, result.RecordsDeleted, result.FilesDeleted, rd(took), keep),
		LoadStart: load, LoadEnd: LoadAverage1(),
	})
	if result.TagsDeleted != wantEvicted || result.RecordsDeleted != wantEvicted {
		t.Errorf("evicted %d tags / %d records, want %d each", result.TagsDeleted, result.RecordsDeleted, wantEvicted)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "supersede of %s/%s keep %s: %d tags, %d records in %s (load %.1f→%.1f)\n",
		dataset.ProviderID, dataset.SourceName, keep, result.TagsDeleted, result.RecordsDeleted, took.Round(time.Millisecond), load, LoadAverage1())
	line := func(kind string, l supersedeShapeLine) {
		fmt.Fprintf(&b, "%-6s n=%-6d p50=%-9s p99=%-9s max=%-9s total=%-9s %s\n", kind, l.Summary.N, rd(l.Summary.P50), rd(l.Summary.P99), rd(l.Summary.Max), rd(l.Summary.Total), l.Name)
	}
	for _, l := range summarize(logs.holds) {
		line("hold", l)
		report.Add(ShapeMetric{Name: "write-lock hold " + l.Name, Latency: l.Summary})
		if strings.Contains(strings.ToLower(l.Name), "supersede") && l.Summary.Max > supersedeShapeChunkLimit {
			t.Errorf("%s held the store lock %s (> %s)", l.Name, l.Summary.Max, supersedeShapeChunkLimit)
		}
	}
	for _, l := range summarize(logs.waits) {
		line("wait", l)
		report.Add(ShapeMetric{Name: "read-lock wait " + l.Name, Latency: l.Summary})
	}
	for _, l := range summarize(logs.statements) {
		line("stmt", l)
		report.Add(ShapeMetric{Name: "statement " + l.Name, Latency: l.Summary})
	}
	for i, op := range ops {
		base := baselineSets[i].Summary()
		line("idle", supersedeShapeLine{Name: op.name + " (readers alone)", Summary: base})
		report.Add(ShapeMetric{Name: "reader without supersede " + op.name, Latency: base})
		sum := readerSets[i].Summary()
		line("reader", supersedeShapeLine{Name: op.name, Summary: sum})
		report.Add(ShapeMetric{Name: "reader during supersede " + op.name, Latency: sum})
		if sum.Errors > 0 {
			t.Errorf("reader %s: %d errors", op.name, sum.Errors)
		}
	}
	t.Logf("\n%s", b.String())
	if err := report.Write(report.Path); err != nil {
		t.Errorf("write report: %v", err)
	}
}
