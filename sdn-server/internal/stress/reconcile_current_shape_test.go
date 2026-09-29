//go:build stress
// +build stress

package stress

// Host-02-shaped measurement of the current-batch reconcile
// (storage.ReconcileSourceBatch, reconcile mode "current" of
// storage.ingest_with_source), per statement and per store-lock hold, with
// API readers running against the store the whole time. It uses the
// supersede harness's store (supersede_shape_test.go: the same templates and
// environment), and from sdn-server/:
//
//	SDN_FLATSQL_SLOW_QUERY_MS=1 SDN_FLATSQL_SLOW_LOCK_MS=1 \
//	STRESS_SHAPE_TEMPLATE=<dir> SUPERSEDE_SHAPE_HYDRATED=<dir> RECONCILE_CURRENT_SHAPE_OUT=<report.json> \
//	  nice -n 10 ../scripts/go-with-wasmedge.sh test -tags=stress -timeout=6h \
//	  -run '^TestReconcileCurrentShapeHost02$' -v ./internal/stress/
//
// host-02 logged this reconcile holding the store write lock 5.57 s under
// beta.78 (2026-09-29). Two rounds run:
//   - "ingest": what a current-mode source ingest does. A lane of the
//     celestrak producer table gets a batch of RECONCILE_CURRENT_BATCH
//     records (default 32,000, one celestrak batch), then the next batch of
//     the same objects with other bytes: the dry-run count before the second
//     batch is stored, the reconcile after it, which evicts the first batch;
//   - "dataset": the replicated dataset lane (six 32,324-record batches in
//     the second producer table) reconciled to its newest batch, which
//     evicts the other five, 161,620 records: the largest eviction one call
//     meets on this shape.
//
// The harness reads the store-lock holds (write and read) from the store's
// slow-lock log lines, so the same file measures the single-hold code before
// sdn's bounded-hold rework and the code after it.

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	logging "github.com/ipfs/go-log/v2"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// readHoldLogs collects the store's READ-lock holds by site: the reconcile's
// dry run and count slices hold the lock for reading, and a read hold blocks
// every writer for its length. supersedeLogs keeps a read line's wait only.
type readHoldLogs struct {
	pr    *logging.PipeReader
	done  chan struct{}
	mu    sync.Mutex
	holds map[string]*LatencySet
}

func startReadHoldLogs() *readHoldLogs {
	c := &readHoldLogs{
		pr:    logging.NewPipeReader(logging.PipeFormat(logging.PlaintextOutput), logging.PipeLevel(logging.LevelWarn)),
		done:  make(chan struct{}),
		holds: map[string]*LatencySet{},
	}
	go func() {
		defer close(c.done)
		buf := make([]byte, 64<<10)
		var line []byte
		for {
			n, err := c.pr.Read(buf)
			for _, b := range buf[:n] {
				if b != '\n' {
					line = append(line, b)
					continue
				}
				if m := reSlowLock.FindStringSubmatch(string(line)); m != nil && m[1] == "read" {
					if held, err := time.ParseDuration(m[3]); err == nil {
						c.mu.Lock()
						if c.holds[m[2]] == nil {
							c.holds[m[2]] = &LatencySet{}
						}
						c.holds[m[2]].Add(held, nil)
						c.mu.Unlock()
					}
				}
				line = line[:0]
			}
			if err != nil {
				return
			}
		}
	}()
	return c
}

func (c *readHoldLogs) stop() []supersedeShapeLine {
	_ = c.pr.Close()
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return summarize(c.holds)
}

func TestReconcileCurrentShapeHost02(t *testing.T) {
	parts := Host02SupersedeShape(shapeEnvFloat("SUPERSEDE_SHAPE_SCALE", 1))
	store, stats, ok := openSupersedeShapeStore(t, parts)
	if !ok {
		return
	}
	defer store.Close()
	report := &ShapeReport{
		Build: os.Getenv("STRESS_SHAPE_BUILD"), Scale: shapeEnvFloat("SUPERSEDE_SHAPE_SCALE", 1), Shape: parts,
		Populate: stats, CPUs: runtime.NumCPU(), StartedAt: time.Now().UTC(), Path: os.Getenv("RECONCILE_CURRENT_SHAPE_OUT"),
	}
	celestrak := parts[0]
	dataset := parts[len(parts)-1]

	// Readers: records neither round touches (the celestrak lane), read by
	// CID and by a batch page.
	seqs := make([]int, 256)
	for i := range seqs {
		seqs[i] = (i * 104729) % celestrak.Records
	}
	ops := celestrakShapeReaders(store, celestrak, seqs)
	readers := shapeEnvInt("SUPERSEDE_SHAPE_READERS", 2)
	stopBaseline, baselineSets := startShapeReaders(ops, readers)
	time.Sleep(shapeEnvDuration("SUPERSEDE_SHAPE_BASELINE", time.Minute))
	stopBaseline()

	measure := func(round, what string, apply bool, schema, provider, source, keep string, wantMatched, wantDeleted int64) {
		logs := startSupersedeLogs()
		reads := startReadHoldLogs()
		stopReaders, readerSets := startShapeReaders(ops, readers)
		load := LoadAverage1()
		started := time.Now()
		result, err := store.ReconcileSourceBatch(schema, provider, source, keep, apply)
		took := time.Since(started)
		stopReaders()
		// Let the last log lines drain.
		time.Sleep(200 * time.Millisecond)
		readHolds := reads.stop()
		logs.stop()
		var b strings.Builder
		defer func() {
			// Each round is logged and saved as it ends: a later round that
			// fails must not lose this one's numbers.
			t.Logf("\n%s", b.String())
			if err := report.Write(report.Path); err != nil {
				t.Errorf("write report: %v", err)
			}
		}()
		if err != nil {
			t.Errorf("round %s %s: ReconcileSourceBatch after %s: %v", round, what, took.Round(time.Millisecond), err)
			fmt.Fprintf(&b, "round %s %s: FAILED after %s: %v\n", round, what, took.Round(time.Millisecond), err)
		}
		report.Add(ShapeMetric{
			Name: fmt.Sprintf("reconcile round %s %s", round, what),
			Value: fmt.Sprintf("matched %d, deleted %d in %s (%s/%s keep %s)",
				result.Matched, result.Deleted, rd(took), provider, source, keep),
			LoadStart: load, LoadEnd: LoadAverage1(),
		})
		if result.Matched != wantMatched || result.Deleted != wantDeleted {
			t.Errorf("round %s %s: matched %d, deleted %d; want %d / %d", round, what, result.Matched, result.Deleted, wantMatched, wantDeleted)
		}
		fmt.Fprintf(&b, "\nround %s %s: reconcile of %s/%s keep %s: matched %d, deleted %d in %s (load %.1f→%.1f)\n",
			round, what, provider, source, keep, result.Matched, result.Deleted, took.Round(time.Millisecond), load, LoadAverage1())
		for _, l := range readHolds {
			fmt.Fprintf(&b, "%-6s n=%-6d p50=%-9s p99=%-9s max=%-9s total=%-9s %s\n", "rhold", l.Summary.N, rd(l.Summary.P50), rd(l.Summary.P99), rd(l.Summary.Max), rd(l.Summary.Total), l.Name)
			report.Add(ShapeMetric{Name: "read-lock hold " + l.Name, Latency: l.Summary})
			if strings.Contains(strings.ToLower(l.Name), "reconcile") && l.Summary.Max > supersedeShapeChunkLimit {
				t.Errorf("round %s %s: %s held the store read lock %s (> %s)", round, what, l.Name, l.Summary.Max, supersedeShapeChunkLimit)
			}
		}
		reportShapeRun(t, &b, report, logs, "reconcile", supersedeShapeChunkLimit, ops, baselineSets, readerSets)
	}

	// Round "ingest": a current-mode lane of the celestrak producer table.
	n := shapeEnvInt("RECONCILE_CURRENT_BATCH", 32_000)
	laneTags := func(batch string) storage.SourceTags {
		return storage.SourceTags{ProviderID: celestrak.ProviderID, SourceName: "celestrak-gp-current", BatchID: batch}
	}
	variant := func(name string) ([][]byte, error) {
		p := celestrak
		p.SourceName = name // the comment field carries it: other bytes, same objects
		return buildShapeBatch(p, 0, n)
	}
	first, err := variant("celestrak-gp-current-a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := variant("celestrak-gp-current-b")
	if err != nil {
		t.Fatal(err)
	}
	ingestStart := time.Now()
	if _, err := store.StoreBatchWithSourceTags(celestrak.Schema, first, celestrak.Producer, nil, laneTags("current-001")); err != nil {
		t.Fatalf("ingest the lane's first batch: %v", err)
	}
	t.Logf("round ingest: first batch of %d records stored in %s", n, time.Since(ingestStart).Round(time.Millisecond))
	measure("ingest", "dry run", false, celestrak.Schema, celestrak.ProviderID, "celestrak-gp-current", "current-002", int64(n), 0)
	ingestStart = time.Now()
	if _, err := store.StoreBatchWithSourceTags(celestrak.Schema, second, celestrak.Producer, nil, laneTags("current-002")); err != nil {
		t.Fatalf("ingest the lane's second batch: %v", err)
	}
	t.Logf("round ingest: second batch of %d records stored in %s", n, time.Since(ingestStart).Round(time.Millisecond))
	measure("ingest", "apply", true, celestrak.Schema, celestrak.ProviderID, "celestrak-gp-current", "current-002", int64(n), int64(n))

	// Round "dataset": the replicated lane to its newest batch.
	keep := dataset.BatchID(dataset.Records - 1)
	evicted := int64((supersedeShapeBatches - 1) * supersedeShapeBatchRecords)
	if os.Getenv("RECONCILE_CURRENT_SKIP_DATASET") == "" {
		measure("dataset", "apply", true, dataset.Schema, dataset.ProviderID, dataset.SourceName, keep, evicted, evicted)
	}

}
