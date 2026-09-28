//go:build stress
// +build stress

package stress

// Production-shape stress for the FlatSQL record store (host-02's partitions,
// see production_shape.go). Run from sdn-server/:
//
//	nice -n 10 ../scripts/go-with-wasmedge.sh test -tags=stress -timeout=6h \
//	  -run '^TestProductionShapeFlatSQL$' -v ./internal/stress/
//
// Environment:
//   - STRESS_SHAPE_SCALE: partition size multiplier (default 1 = host-02)
//   - STRESS_SHAPE_IQC_MIRROR_FRACTION: share of $IQC in the second producer
//     table (default 1)
//   - STRESS_SHAPE_TEMPLATE: directory holding a populated store; built there
//     on first use, then CLONED for every run, so two builds measure the same
//     bytes (the lane's A/B)
//   - STRESS_SHAPE_RESTART_EVERY: close and reopen the store after this many
//     populated records (default 300000; population resumes after a failure)
//   - STRESS_SHAPE_DIR: work directory (default: a test temp dir)
//   - STRESS_SHAPE_BUILD / STRESS_SHAPE_OUT: build label and JSON report path
//   - STRESS_SHAPE_WRITERS / STRESS_SHAPE_READERS / STRESS_SHAPE_INGEST_FOR:
//     concurrent ingest phase (default 4 writers, 8 readers, 60s)
//   - STRESS_SHAPE_METRIC_BUDGET: wall-clock cap per repeated metric (5m)
//   - STRESS_SHAPE_CHILD_RUN: how long the unclean-stop child runs before it
//     is SIGKILLed (default 40s, past one 30s checkpoint)
//
// Asserted where the owner law gives a number: no engine call near the
// 5-minute per-call budget (fail above 30 s), and readers answer during ingest
// (fail when their p99 exceeds 1 s). Also asserted: a read after Close is an
// error, never the death of the process. Everything else is reported.

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

const (
	shapeEngineCallLimit   = 30 * time.Second // well inside the 5-minute per-call budget
	shapeReaderP99Limit    = 1 * time.Second
	shapeChildStoreEnv     = "STRESS_SHAPE_CHILD_STORE"
	shapeChildMarkerEnv    = "STRESS_SHAPE_CHILD_MARKER"
	shapeReadAfterCloseEnv = "STRESS_SHAPE_READ_AFTER_CLOSE_CHILD"
	shapeWindowLimit       = 50_000   // defaultFullCatalogPublicationChunkSize
	shapeShardBudget       = 64 << 20 // DefaultDatasetPublicationMaxShardBytes
	shapeAPIPage           = 250
)

func shapeEnvFloat(name string, def float64) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(name)), 64); err == nil && v > 0 {
		return v
	}
	return def
}

func shapeEnvInt(name string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name))); err == nil && v > 0 {
		return v
	}
	return def
}

func shapeEnvDuration(name string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(strings.TrimSpace(os.Getenv(name))); err == nil && v > 0 {
		return v
	}
	return def
}

func shapeValidator(t testing.TB) *sds.Validator {
	t.Helper()
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	return v
}

// openShapeStore opens the way the daemon does: engine hot-window hydration
// deferred to HydrateEngineHotWindowContext.
func openShapeStore(t testing.TB, base string) *storage.FlatSQLStore {
	t.Helper()
	store, err := storage.NewFlatSQLStore(base, shapeValidator(t), storage.WithDeferredBootRebuilds())
	if err != nil {
		t.Fatalf("open store %s: %v", base, err)
	}
	return store
}

type shapeRun struct {
	t       *testing.T
	report  *ShapeReport
	logs    *LogCapture
	shape   []ShapePartition
	budget  time.Duration
	longest []EngineCall
}

func (r *shapeRun) fail(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.report.Failures = append(r.report.Failures, msg)
	r.t.Error(msg)
}

func (r *shapeRun) noteCalls(calls []EngineCall, poisoned bool, where string) {
	for _, c := range calls {
		c.Phase = strings.TrimSpace(where + " " + c.Phase)
		r.longest = append(r.longest, c)
	}
	if poisoned {
		r.fail("%s: the engine was POISONED", where)
	}
}

// recoverIfPoisoned replaces a poisoned engine the way the daemon does, so
// the sections after a poisoning still measure something.
func (r *shapeRun) recoverIfPoisoned(store *storage.FlatSQLStore, where string) {
	rt, _ := store.EngineRuntime()
	if rt == nil || !rt.Poisoned() {
		return
	}
	started := time.Now()
	epoch, err := store.RecoverPoisonedEngine()
	// The recovery's own poison/rebuild lines belong to this section, not to
	// the next one's evidence.
	r.logs.Snapshot()
	r.report.Add(ShapeMetric{
		Name: where + ": poisoned-engine recovery", Value: fmt.Sprintf("%s (epoch %d, err=%v)", rd(time.Since(started)), epoch, err),
		LoadStart: LoadAverage1(), LoadEnd: LoadAverage1(),
	})
}

// engineCallVerdict asserts the per-call law on the longest call a section saw
// (watch or slow-statement log, whichever is longer).
func (r *shapeRun) engineCallVerdict(name string, calls []EngineCall, ev LogEvidence, loadStart float64) {
	longest := EngineCall{}
	if len(calls) > 0 {
		longest = calls[0]
	}
	if ev.MaxStatementHeld > longest.Elapsed {
		longest = EngineCall{Elapsed: ev.MaxStatementHeld, SQL: ev.MaxStatementSQL, Phase: "log"}
	}
	pass := longest.Elapsed <= shapeEngineCallLimit && !ev.Poisoned
	r.report.Add(ShapeMetric{
		Name: name + ": longest engine call", Value: rd(longest.Elapsed),
		LoadStart: loadStart, LoadEnd: LoadAverage1(),
		Threshold: "<= 30s", Pass: boolPtr(pass), Note: longest.SQL,
	})
	if !pass {
		r.fail("%s: longest engine call %s (> %s or poisoned=%v): %s", name, longest.Elapsed, shapeEngineCallLimit, ev.Poisoned, longest.SQL)
	}
}

// bootAndHydrate opens base, hydrates the engine hot window, and reports the
// boot: wall times, whether the engine record state was discarded, and the
// longest single engine call of the rebuild.
func (r *shapeRun) bootAndHydrate(label, base string) *storage.FlatSQLStore {
	r.logs.Snapshot()
	load := LoadAverage1()
	started := time.Now()
	store := openShapeStore(r.t, base)
	open := time.Since(started)
	boot := store.BootState()
	bootEv := r.logs.Snapshot()

	rt, _ := store.EngineRuntime()
	watch := StartEngineWatch(rt, 2*time.Millisecond)
	hydrateStart := time.Now()
	n, err := store.HydrateEngineHotWindowContext(context.Background())
	hydrate := time.Since(hydrateStart)
	calls, poisoned := watch.Stop()
	hydEv := r.logs.Snapshot()
	r.noteCalls(calls, poisoned, label)
	if err != nil {
		r.fail("%s: hydrate: %v", label, err)
	}

	discarded := bootEv.Discarded || hydEv.Discarded
	r.report.Add(ShapeMetric{
		Name: label + ": open (NewFlatSQLStore)", Value: rd(open),
		LoadStart: load, LoadEnd: LoadAverage1(),
		Note: fmt.Sprintf("engine warm=%v engine records=%d discarded=%v %s", boot.EngineWarm, boot.EngineRecords, discarded, strings.Join(append(bootEv.DiscardLines, hydEv.DiscardLines...), " | ")),
	})
	r.report.Add(ShapeMetric{
		Name: label + ": hot-window hydration", Value: fmt.Sprintf("%s (%d records)", rd(hydrate), n),
		LoadStart: load, LoadEnd: LoadAverage1(),
		Note: fmt.Sprintf("max write-lock hold %s (%s)", rd(hydEv.MaxWriteLockHeld), hydEv.MaxWriteLockSite),
	})
	merged := bootEv
	if hydEv.MaxStatementHeld > merged.MaxStatementHeld {
		merged.MaxStatementHeld, merged.MaxStatementSQL = hydEv.MaxStatementHeld, hydEv.MaxStatementSQL
	}
	merged.Poisoned = merged.Poisoned || hydEv.Poisoned
	r.engineCallVerdict(label, calls, merged, load)
	r.t.Logf("%s: open %s, hydrate %s (%d records), warm=%v discarded=%v, longest call %v", label, open, hydrate, n, boot.EngineWarm, discarded, firstCall(calls))
	r.recoverIfPoisoned(store, label)
	return store
}

func firstCall(calls []EngineCall) string {
	if len(calls) == 0 {
		return "-"
	}
	return fmt.Sprintf("%s %s", rd(calls[0].Elapsed), calls[0].SQL)
}

// repeated runs fn up to n times (at least once) within the metric budget,
// timing each call and its engine hold.
func (r *shapeRun) repeated(name string, store *storage.FlatSQLStore, n int, fn func(i int) error) {
	rt, _ := store.EngineRuntime()
	var wall, held LatencySet
	load := LoadAverage1()
	deadline := time.Now().Add(r.budget)
	watch := StartEngineWatch(rt, 2*time.Millisecond)
	for i := 0; i < n && (i == 0 || time.Now().Before(deadline)); i++ {
		before := EngineStatsSnapshot(rt)
		started := time.Now()
		err := fn(i)
		wall.Add(time.Since(started), err)
		if err == nil {
			held.Add(EngineStatsSnapshot(rt).Sub(before).Held, nil)
		} else {
			r.t.Logf("%s call %d: %v", name, i, err)
		}
	}
	calls, poisoned := watch.Stop()
	r.noteCalls(calls, poisoned, name)
	ev := r.logs.Snapshot()
	defer r.recoverIfPoisoned(store, name)
	m := ShapeMetric{Name: name, Latency: wall.Summary(), EngineHeld: held.Summary(), LoadStart: load, LoadEnd: LoadAverage1()}
	if m.Latency.Errors > 0 {
		m.Note = fmt.Sprintf("%d call(s) failed", m.Latency.Errors)
	}
	r.report.Add(m)
	r.engineCallVerdict(name, calls, ev, load)
}

func iqcWindowFilter(offset, limit int) storage.IndexedRecordQuery {
	return storage.IndexedRecordQuery{
		SchemaName: "IQC.fbs", ProviderID: shapeProvider, SourceName: "IQEngine", BatchID: shapeIQCBatch,
		Limit: limit, Offset: offset, AllowLargeResultSet: true,
	}
}

func shapeLane(parts []ShapePartition, schema, source string) ShapePartition {
	for _, p := range parts {
		if p.Schema == schema && p.SourceName == source && p.MirrorOf == "" {
			return p
		}
	}
	return ShapePartition{}
}

// prepareShapeStore returns a store directory holding the shape: cloned from
// the template (populating the template first when it is empty), or populated
// in place.
func prepareShapeStore(t *testing.T, workDir string, parts []ShapePartition) (string, PopulateStats) {
	t.Helper()
	base := filepath.Join(workDir, "store")
	template := strings.TrimSpace(os.Getenv("STRESS_SHAPE_TEMPLATE"))
	target := base
	if template != "" {
		target = filepath.Join(template, "store")
	}
	var stats PopulateStats
	marker := filepath.Join(filepath.Dir(target), "populated.txt")
	if raw, err := os.ReadFile(marker); err == nil {
		t.Logf("reusing populated store %s (%s)", target, strings.TrimSpace(string(raw)))
		var took string
		fmt.Sscanf(string(raw), "records=%d bytes=%d duration=%s", &stats.Records, &stats.Bytes, &took)
		stats.Duration, _ = time.ParseDuration(took)
	} else {
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		load := LoadAverage1()
		lastLog := time.Now()
		var err error
		stats, err = PopulateShape(context.Background(), func() (*storage.FlatSQLStore, error) {
			return storage.NewFlatSQLStore(target, shapeValidator(t), storage.WithDeferredBootRebuilds())
		}, parts, PopulateOptions{
			BatchSize:    2000,
			RestartEvery: shapeEnvInt("STRESS_SHAPE_RESTART_EVERY", 300_000),
			ResumeFile:   filepath.Join(filepath.Dir(target), "populate-progress.json"),
			Progress: func(store *storage.FlatSQLStore, lane string, done, total int) {
				if time.Since(lastLog) > 30*time.Second {
					lastLog = time.Now()
					used, _ := EngineMemory(store)
					arena := int64(0)
					if fi, err := os.Stat(filepath.Join(target, "control.flatsqldb.fsdata")); err == nil {
						arena = fi.Size()
					}
					t.Logf("populate %s: %d/%d (engine memory %d MiB, arena %d MiB, load %.1f)", lane, done, total, used>>20, arena>>20, LoadAverage1())
				}
			},
		})
		if err != nil {
			t.Fatalf("populate (resumable: rerun to continue): %v", err)
		}
		t.Logf("populated %d records / %d B in %s with %d restarts (load %.1f→%.1f)", stats.Records, stats.Bytes, stats.Duration, stats.Restarts, load, LoadAverage1())
		note := fmt.Sprintf("records=%d bytes=%d duration=%s load=%.1f", stats.Records, stats.Bytes, stats.Duration.Round(time.Second), load)
		if err := os.WriteFile(marker, []byte(note+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if target == base {
		return base, stats
	}
	// APFS clonefile (cp -c) makes the copy free and leaves the template
	// untouched for the other build's run.
	_ = os.RemoveAll(base)
	cp := exec.Command("cp", "-cR", target, base)
	if runtime.GOOS != "darwin" {
		cp = exec.Command("cp", "-R", "--reflink=auto", target, base)
	}
	if out, err := cp.CombinedOutput(); err != nil {
		t.Fatalf("clone template store: %v: %s", err, out)
	}
	return base, stats
}

func TestProductionShapeFlatSQL(t *testing.T) {
	if os.Getenv(shapeChildStoreEnv) != "" || os.Getenv(shapeReadAfterCloseEnv) != "" {
		t.Skip("child process")
	}
	scale := shapeEnvFloat("STRESS_SHAPE_SCALE", 1)
	parts := ScaleShape(Host02Shape(shapeEnvFloat("STRESS_SHAPE_IQC_MIRROR_FRACTION", 1)), scale)
	workDir := strings.TrimSpace(os.Getenv("STRESS_SHAPE_DIR"))
	if workDir == "" {
		workDir = t.TempDir()
	} else if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := &shapeRun{
		t:      t,
		shape:  parts,
		budget: shapeEnvDuration("STRESS_SHAPE_METRIC_BUDGET", 5*time.Minute),
		report: &ShapeReport{Build: os.Getenv("STRESS_SHAPE_BUILD"), Scale: scale, Shape: parts, CPUs: runtime.NumCPU(), StartedAt: time.Now().UTC(), Path: os.Getenv("STRESS_SHAPE_OUT")},
		logs:   StartLogCapture(),
	}
	defer run.logs.Stop()
	defer func() {
		sortEngineCalls(run.longest)
		if len(run.longest) > 8 {
			run.longest = run.longest[:8]
		}
		run.report.Longest = run.longest
		if err := run.report.Write(run.report.Path); err != nil {
			t.Errorf("write report: %v", err)
		}
		t.Logf("\n%s", run.report.Table())
	}()

	base, stats := prepareShapeStore(t, workDir, parts)
	run.report.Populate = stats

	// ── boot after the population's clean stop ───────────────────────────
	store := run.bootAndHydrate("c0 boot after clean stop (populated)", base)

	iqc := shapeLane(parts, "IQC.fbs", "IQEngine")
	windows := (iqc.Records + shapeWindowLimit - 1) / shapeWindowLimit

	// ── (a) the IQC source-filtered window and its byte probe ─────────────
	bounded := make([]int, windows)
	run.repeated("a1 IQC byte probe (50k window, 64MiB)", store, windows, func(i int) error {
		n, _, err := store.IndexedRecordWindowLimitForBytes(iqcWindowFilter(i*shapeWindowLimit, shapeWindowLimit), shapeShardBudget)
		bounded[i] = n
		return err
	})
	run.repeated("a2 IQC window read (probe-bounded)", store, windows, func(i int) error {
		limit := bounded[i]
		if limit <= 0 {
			limit = shapeWindowLimit
		}
		recs, err := store.QueryIndexedRecords(iqcWindowFilter(i*shapeWindowLimit, limit))
		if err == nil && len(recs) == 0 && i*shapeWindowLimit < iqc.Records {
			return errors.New("empty IQC window")
		}
		return err
	})
	run.repeated("a3 IQC API page (250)", store, 20, func(i int) error {
		_, err := store.QueryIndexedRecords(iqcWindowFilter((i*7919)%maxInt(iqc.Records-shapeAPIPage, 1), shapeAPIPage))
		return err
	})
	// An OMM page of one batch (1 of host-02's 53): the source-filtered window
	// the store used to answer by walking every $OMM index row.
	omm := shapeLane(parts, "OMM.fbs", "celestrak-gp")
	ommBatch := omm.Records / maxInt(omm.Batches, 1)
	run.repeated("a4 OMM batch page (250)", store, 20, func(i int) error {
		_, err := store.QueryIndexedRecords(storage.IndexedRecordQuery{
			SchemaName: "OMM.fbs", ProviderID: omm.ProviderID, SourceName: omm.SourceName, BatchID: omm.BatchID(0),
			Limit: shapeAPIPage, Offset: (i * 7919) % maxInt(ommBatch-shapeAPIPage, 1),
		})
		return err
	})

	// ── (b) totals ─────────────────────────────────────────────────────────
	run.repeated("b1 LiveRecordBytes", store, 3, func(int) error {
		_, err := store.LiveRecordBytes()
		return err
	})
	run.repeated("b2 DataSummary", store, 5, func(int) error {
		_, err := store.DataSummary()
		return err
	})

	// ── (d) readers during concurrent ingest ─────────────────────────────
	run.ingestUnderRead(store, iqc)

	// ── (e) stop with readers active (a clean stop) ───────────────────────
	run.drainOnClose(store, iqc)

	// ── (c) boots: after that clean stop, after an unclean one, and after
	// the engine record arena is lost (the discard path host-02 took) ──────
	store = run.bootAndHydrate("c1 boot after clean stop (drained)", base)
	if err := store.Close(); err != nil {
		t.Errorf("close before unclean child: %v", err)
	}
	run.uncleanStop(workDir, base)
	store = run.bootAndHydrate("c2 boot after unclean stop (SIGKILL, read in flight)", base)
	if err := store.Close(); err != nil {
		t.Errorf("close before arena loss: %v", err)
	}
	if err := os.Truncate(filepath.Join(base, "control.flatsqldb.fsdata"), 0); err != nil && !os.IsNotExist(err) {
		t.Fatalf("drop engine record arena: %v", err)
	}
	store = run.bootAndHydrate("c3 boot after engine arena loss (cold rebuild)", base)
	if err := store.Close(); err != nil {
		t.Errorf("final close: %v", err)
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func sortEngineCalls(calls []EngineCall) {
	for i := 1; i < len(calls); i++ {
		for j := i; j > 0 && calls[j].Elapsed > calls[j-1].Elapsed; j-- {
			calls[j], calls[j-1] = calls[j-1], calls[j]
		}
	}
}

// shapeReaderOps are the API reads a node serves while it ingests.
func shapeReaderOps(store *storage.FlatSQLStore, iqc ShapePartition, parts []ShapePartition) map[string]func(*rand.Rand) error {
	cids := make([]string, 0, 256)
	for i := 0; i < 256; i++ {
		seq := (i * 104729) % maxInt(iqc.Records, 1)
		if data, err := BuildShapeRecord(iqc, seq); err == nil {
			cids = append(cids, storage.ComputeCID(data))
		}
	}
	omm := shapeLane(parts, "OMM.fbs", "celestrak-gp")
	return map[string]func(*rand.Rand) error{
		"IQC page": func(rng *rand.Rand) error {
			_, err := store.QueryIndexedRecords(iqcWindowFilter(rng.Intn(maxInt(iqc.Records-shapeAPIPage, 1)), shapeAPIPage))
			return err
		},
		"IQC by CID": func(rng *rand.Rand) error {
			_, err := store.GetRecord("IQC.fbs", cids[rng.Intn(len(cids))])
			return err
		},
		"OMM page": func(rng *rand.Rand) error {
			_, err := store.QueryIndexedRecords(storage.IndexedRecordQuery{
				SchemaName: "OMM.fbs", ProviderID: omm.ProviderID, SourceName: omm.SourceName, BatchID: omm.BatchID(0),
				Limit: shapeAPIPage, Offset: rng.Intn(maxInt(omm.Records/maxInt(omm.Batches, 1)-shapeAPIPage, 1)),
			})
			return err
		},
		"DataSummary": func(*rand.Rand) error {
			_, err := store.DataSummary()
			return err
		},
	}
}

// ingestUnderRead runs N writers and M readers together (d).
func (r *shapeRun) ingestUnderRead(store *storage.FlatSQLStore, iqc ShapePartition) {
	writers := shapeEnvInt("STRESS_SHAPE_WRITERS", 4)
	readers := shapeEnvInt("STRESS_SHAPE_READERS", 8)
	span := shapeEnvDuration("STRESS_SHAPE_INGEST_FOR", 60*time.Second)
	ops := shapeReaderOps(store, iqc, r.shape)
	names := []string{"IQC page", "IQC by CID", "OMM page", "DataSummary"}

	rt, _ := store.EngineRuntime()
	watch := StartEngineWatch(rt, 2*time.Millisecond)
	load := LoadAverage1()
	lockBefore := store.StoreLockStats()
	ctx, cancel := context.WithTimeout(context.Background(), span)
	defer cancel()

	var all, writes LatencySet
	perOp := map[string]*LatencySet{}
	for _, n := range names {
		perOp[n] = &LatencySet{}
	}
	var written atomic.Int64
	var wg sync.WaitGroup
	lanes := []ShapePartition{iqc, shapeLane(r.shape, "OMM.fbs", "celestrak-gp"), shapeLane(r.shape, "MPE.fbs", "celestrak-gp")}
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			p := lanes[w%len(lanes)]
			seq := p.Records + 10_000_000*(w+1)
			for ctx.Err() == nil {
				records, err := buildShapeBatch(p, seq, seq+500)
				if err != nil {
					writes.Add(0, err)
					return
				}
				started := time.Now()
				_, err = store.StoreBatchWithSourceTags(p.Schema, records, p.Producer, nil,
					storage.SourceTags{ProviderID: p.ProviderID, SourceName: p.SourceName, BatchID: p.BatchID(seq)})
				writes.Add(time.Since(started), err)
				if err == nil {
					written.Add(int64(len(records)))
				}
				seq += 500
			}
		}(w)
	}
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(i) + 1))
			for k := i; ctx.Err() == nil; k++ {
				name := names[k%len(names)]
				started := time.Now()
				err := ops[name](rng)
				d := time.Since(started)
				perOp[name].Add(d, err)
				all.Add(d, err)
			}
		}(i)
	}
	wg.Wait()
	calls, poisoned := watch.Stop()
	r.noteCalls(calls, poisoned, "d ingest")
	defer r.recoverIfPoisoned(store, "d ingest")
	lock := store.StoreLockStats()
	ev := r.logs.Snapshot()

	sum := all.Summary()
	pass := sum.N > 0 && sum.P99 <= shapeReaderP99Limit
	r.report.Add(ShapeMetric{
		Name: fmt.Sprintf("d readers during ingest (%dW/%dR)", writers, readers), Latency: sum,
		LoadStart: load, LoadEnd: LoadAverage1(), Threshold: "p99 <= 1s", Pass: boolPtr(pass),
		Note: fmt.Sprintf("%d records ingested in %s; store-lock read wait %s over %d reads, write held %s",
			written.Load(), span, rd(lock.ReadWait-lockBefore.ReadWait), lock.ReadAcquires-lockBefore.ReadAcquires, rd(lock.WriteHeld-lockBefore.WriteHeld)),
	})
	if !pass {
		r.fail("readers during ingest: p99 %s > %s (n=%d, errors=%d)", sum.P99, shapeReaderP99Limit, sum.N, sum.Errors)
	}
	for _, n := range names {
		r.report.Add(ShapeMetric{Name: "d   reader " + n, Latency: perOp[n].Summary(), LoadStart: load, LoadEnd: LoadAverage1()})
	}
	r.report.Add(ShapeMetric{Name: "d   writer batch (500)", Latency: writes.Summary(), LoadStart: load, LoadEnd: LoadAverage1()})
	r.engineCallVerdict("d ingest", calls, ev, load)
}

// drainOnClose stops the store while readers (API pages and one publisher
// sweeping the IQC window) are active, and times the stop (e).
//
// The readers are GATED the way a daemon drains its API before it closes the
// store: the stop waits for every in-flight read, admits no new one, then
// calls Close. An ungated read that reaches the store after Close crashes the
// whole process (see readAfterCloseChild), so it cannot run in this one.
func (r *shapeRun) drainOnClose(store *storage.FlatSQLStore, iqc ShapePartition) {
	readers := shapeEnvInt("STRESS_SHAPE_READERS", 8)
	ops := shapeReaderOps(store, iqc, r.shape)
	names := []string{"IQC page", "IQC by CID", "OMM page", "DataSummary"}
	var gate sync.RWMutex
	var closed atomic.Bool
	var wg sync.WaitGroup
	var inWindow atomic.Bool
	gated := func(fn func() error) (bool, error) {
		gate.RLock()
		defer gate.RUnlock()
		if closed.Load() {
			return false, nil
		}
		return true, fn()
	}
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(i) + 100))
			for k := i; ; k++ {
				op := ops[names[k%len(names)]]
				if ran, _ := gated(func() error { return op(rng) }); !ran {
					return
				}
			}
		}(i)
	}
	wg.Add(1)
	go func() { // the auto-publisher's sweep: probe, then read, window by window
		defer wg.Done()
		for off := 0; ; off = (off + shapeWindowLimit) % maxInt(iqc.Records, 1) {
			ran, err := gated(func() error {
				inWindow.Store(true)
				defer inWindow.Store(false)
				n, _, err := store.IndexedRecordWindowLimitForBytes(iqcWindowFilter(off, shapeWindowLimit), shapeShardBudget)
				if err == nil {
					_, err = store.QueryIndexedRecords(iqcWindowFilter(off, maxInt(n, 1)))
				}
				return err
			})
			if !ran || err != nil {
				return
			}
		}
	}()
	time.Sleep(3 * time.Second)
	load := LoadAverage1()
	windowBusy := inWindow.Load()
	started := time.Now()
	gate.Lock() // in-flight reads finish; no new read starts
	closed.Store(true)
	drain := time.Since(started)
	closeStart := time.Now()
	err := store.Close()
	closeTook := time.Since(closeStart)
	gate.Unlock()
	wg.Wait()
	if err != nil {
		r.fail("close with readers active: %v", err)
	}
	r.report.Add(ShapeMetric{
		Name:      fmt.Sprintf("e stop with %d readers + publisher active", readers),
		Value:     fmt.Sprintf("total %s = reads drained %s + Close %s", rd(drain+closeTook), rd(drain), rd(closeTook)),
		LoadStart: load, LoadEnd: LoadAverage1(),
		Note: fmt.Sprintf("publisher window in flight at stop: %v", windowBusy),
	})
	r.t.Logf("e: stop drained reads in %s, Close %s (publisher in window: %v)", drain, closeTook, windowBusy)
	r.readAfterClose()
}

// readAfterClose checks what a read that arrives after Close does, in a
// child process because the failure it guards against is a crash. It FAILS
// the run unless the child returns an error and exits cleanly: a crash here
// is a daemon killed by its own shutdown race (host-02's SEGV exits).
func (r *shapeRun) readAfterClose() {
	cmd := exec.Command(os.Args[0], "-test.run=^TestProductionShapeReadAfterCloseChild$", "-test.v")
	cmd.Env = append(os.Environ(), shapeReadAfterCloseEnv+"=1")
	out, err := cmd.CombinedOutput()
	verdict := "error returned (no crash)"
	pass := true
	switch {
	case err != nil:
		verdict, pass = "child process died: "+err.Error(), false
	case !strings.Contains(string(out), "--- PASS"):
		verdict, pass = "child did not report a pass", false
	}
	r.report.Add(ShapeMetric{
		Name: "e2 read after Close (child process)", Value: verdict,
		LoadStart: LoadAverage1(), LoadEnd: LoadAverage1(),
		Threshold: "error, no crash", Pass: boolPtr(pass),
		Note: "QueryIndexedRecords on a closed store, after the engine has run",
	})
	r.t.Logf("e2: read after Close: %s", verdict)
	if !pass {
		r.fail("read after Close: %s\n%s", verdict, lastLines(string(out), 20))
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// TestProductionShapeReadAfterCloseChild opens a small store, runs the engine,
// closes the store and reads from it: the read must be an error, not a panic
// and not a crash. Inert unless run by readAfterClose.
func TestProductionShapeReadAfterCloseChild(t *testing.T) {
	if os.Getenv(shapeReadAfterCloseEnv) == "" {
		t.Skip("only runs as the read-after-Close child")
	}
	store := openShapeStore(t, filepath.Join(t.TempDir(), "store"))
	if _, err := store.QueryIndexedRecords(iqcWindowFilter(0, 10)); err != nil {
		t.Fatalf("read before Close: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Errorf("read after Close panicked: %v", p)
			}
		}()
		_, err = store.QueryIndexedRecords(iqcWindowFilter(0, 10))
	}()
	if err == nil {
		t.Error("read after Close returned no error")
	}
	t.Logf("read after Close: err=%v", err)
}

// uncleanStop runs the child that opens the store, ingests and reads, and is
// SIGKILLed while a long read is in flight.
func (r *shapeRun) uncleanStop(workDir, base string) {
	marker := filepath.Join(workDir, "unclean-child.ready")
	_ = os.Remove(marker)
	logPath := filepath.Join(workDir, "unclean-child.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		r.t.Fatal(err)
	}
	defer logFile.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestProductionShapeUncleanStopChild$", "-test.v", "-test.timeout=3h")
	cmd.Env = append(os.Environ(), shapeChildStoreEnv+"="+base, shapeChildMarkerEnv+"="+marker)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		r.t.Fatalf("start unclean-stop child: %v", err)
	}
	deadline := time.Now().Add(2 * time.Hour)
	var what []byte
	for time.Now().Before(deadline) {
		if what, err = os.ReadFile(marker); err == nil && len(what) > 0 {
			break
		}
		if cmd.ProcessState != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = cmd.Process.Kill() // SIGKILL: no Close, no final checkpoint
	_ = cmd.Wait()
	r.report.Add(ShapeMetric{
		Name: "c2 unclean stop", Value: "SIGKILL", LoadStart: LoadAverage1(), LoadEnd: LoadAverage1(),
		Note: strings.TrimSpace(string(what)) + " (child log " + logPath + ")",
	})
	r.t.Logf("unclean stop: killed child while %s", strings.TrimSpace(string(what)))
}

// TestProductionShapeUncleanStopChild is the process TestProductionShapeFlatSQL
// kills. It is inert unless STRESS_SHAPE_CHILD_STORE names a store.
func TestProductionShapeUncleanStopChild(t *testing.T) {
	base := os.Getenv(shapeChildStoreEnv)
	marker := os.Getenv(shapeChildMarkerEnv)
	if base == "" || marker == "" {
		t.Skip("only runs as the unclean-stop child")
	}
	store := openShapeStore(t, base)
	if _, err := store.HydrateEngineHotWindowContext(context.Background()); err != nil {
		t.Logf("hydrate: %v", err)
	}
	rt, _ := store.EngineRuntime()
	iqc := shapeLane(ScaleShape(Host02Shape(shapeEnvFloat("STRESS_SHAPE_IQC_MIRROR_FRACTION", 1)), shapeEnvFloat("STRESS_SHAPE_SCALE", 1)), "IQC.fbs", "IQEngine")

	var readStart atomic.Int64 // unix nanos of the read in flight, 0 when none
	var readName atomic.Value
	readName.Store("")
	var batches atomic.Int64
	go func() { // ingest: new $IQC under the live batch
		for seq := iqc.Records + 50_000_000; ; seq += 500 {
			records, err := buildShapeBatch(iqc, seq, seq+500)
			if err != nil {
				return
			}
			if _, err := store.StoreBatchWithSourceTags(iqc.Schema, records, iqc.Producer, nil,
				storage.SourceTags{ProviderID: iqc.ProviderID, SourceName: iqc.SourceName, BatchID: iqc.BatchID(seq)}); err != nil {
				t.Logf("child ingest: %v", err)
			}
			batches.Add(1)
		}
	}()
	go func() { // long reads: the publisher's window and the totals
		reads := []struct {
			name string
			fn   func() error
		}{
			{"IQC 50k window read", func() error {
				_, err := store.QueryIndexedRecords(iqcWindowFilter(0, shapeWindowLimit))
				return err
			}},
			{"IQC byte probe", func() error {
				_, _, err := store.IndexedRecordWindowLimitForBytes(iqcWindowFilter(shapeWindowLimit, shapeWindowLimit), shapeShardBudget)
				return err
			}},
			{"LiveRecordBytes", func() error { _, err := store.LiveRecordBytes(); return err }},
		}
		for k := 0; ; k++ {
			op := reads[k%len(reads)]
			readName.Store(op.name)
			readStart.Store(time.Now().UnixNano())
			_ = op.fn()
			readStart.Store(0)
		}
	}()
	runFor := shapeEnvDuration("STRESS_SHAPE_CHILD_RUN", 40*time.Second)
	time.Sleep(runFor)
	// Hand over the moment a read has been in flight for 500 ms (or give up
	// waiting after another minute and say so).
	giveUp := time.Now().Add(time.Minute)
	for {
		started := readStart.Load()
		long := started != 0 && time.Since(time.Unix(0, started)) >= 500*time.Millisecond
		if long || time.Now().After(giveUp) {
			phase, sql, elapsed, ok := rt.InFlight()
			desc := fmt.Sprintf("no read in flight for 500ms; engine in flight=%v", ok)
			if long {
				desc = fmt.Sprintf("%q in flight for %s", readName.Load(), time.Since(time.Unix(0, started)).Round(time.Millisecond))
			}
			if ok {
				desc += fmt.Sprintf("; engine statement %s [%s]: %s", elapsed.Round(time.Millisecond), phase, shortSQL(sql))
			}
			desc += fmt.Sprintf("; %d ingest batches since boot", batches.Load())
			if err := os.WriteFile(marker, []byte(desc+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			break
		}
		time.Sleep(time.Millisecond)
	}
	// Wait to be killed; never Close.
	time.Sleep(time.Hour)
}
