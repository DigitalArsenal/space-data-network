//go:build stress
// +build stress

package stress

// Host-02-shaped measurement of the duplicate reconcile every source ingest
// runs (storage.ReconcileSourceBatchIndexedDuplicates), per statement and per
// store-lock hold, with API readers running against the store the whole time.
// It uses the supersede harness's store (supersede_shape_test.go: the same
// templates and environment), and from sdn-server/:
//
//	SDN_FLATSQL_SLOW_QUERY_MS=1 SDN_FLATSQL_SLOW_LOCK_MS=1 \
//	STRESS_SHAPE_TEMPLATE=<dir> SUPERSEDE_SHAPE_HYDRATED=<dir> RECONCILE_SHAPE_OUT=<report.json> \
//	  nice -n 10 ../scripts/go-with-wasmedge.sh test -tags=stress -timeout=6h \
//	  -run '^TestReconcileDuplicatesShapeHost02$' -v ./internal/stress/
//
// host-02 logged this reconcile holding the store write lock 9.3 s
// (2026-09-28/29) while its ordinary holds were 8 ms. Two rounds run against
// the newest celestrak-gp batch (32,000 records):
//   - "few": RECONCILE_SHAPE_FEW (default 256) of its objects are published
//     again in the same batch with other bytes, as a re-fetch that lands in
//     the batch it already wrote does; the reconcile keeps the newer copy;
//   - "batch": the whole batch is published again, so every row of it has a
//     newer duplicate: the largest staged set one call can meet.
//
// Each round's losers carry no other tag of the schema, so each is evicted
// from its producer table, the record index and the engine hot window.

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestReconcileDuplicatesShapeHost02(t *testing.T) {
	parts := Host02SupersedeShape(shapeEnvFloat("SUPERSEDE_SHAPE_SCALE", 1))
	store, stats, ok := openSupersedeShapeStore(t, parts)
	if !ok {
		return
	}
	defer store.Close()
	report := &ShapeReport{
		Build: os.Getenv("STRESS_SHAPE_BUILD"), Scale: shapeEnvFloat("SUPERSEDE_SHAPE_SCALE", 1), Shape: parts,
		Populate: stats, CPUs: runtime.NumCPU(), StartedAt: time.Now().UTC(), Path: os.Getenv("RECONCILE_SHAPE_OUT"),
	}

	celestrak := parts[0]
	last := celestrak.Records - 1
	batch := celestrak.BatchID(last)
	first := last
	for first > 0 && celestrak.BatchID(first-1) == batch {
		first--
	}
	batchRecords := celestrak.Records - first
	tags := storage.SourceTags{ProviderID: celestrak.ProviderID, SourceName: celestrak.SourceName, BatchID: batch}

	// Readers: celestrak records outside the reconciled batch, by CID and by a
	// page of batch 0.
	seqs := make([]int, 256)
	for i := range seqs {
		seqs[i] = (i * 104729) % first
	}
	ops := celestrakShapeReaders(store, celestrak, seqs)
	readers := shapeEnvInt("SUPERSEDE_SHAPE_READERS", 2)
	stopBaseline, baselineSets := startShapeReaders(ops, readers)
	time.Sleep(shapeEnvDuration("SUPERSEDE_SHAPE_BASELINE", time.Minute))
	stopBaseline()

	rounds := []struct {
		name string
		n    int
	}{
		{"few", shapeEnvInt("RECONCILE_SHAPE_FEW", 256)},
		{"batch", batchRecords},
	}
	var b strings.Builder
	for round, r := range rounds {
		if r.n < 1 || r.n > batchRecords {
			t.Fatalf("round %s: %d duplicates, want 1..%d", r.name, r.n, batchRecords)
		}
		// Same object and epoch (so the same index key), other bytes: the
		// comment field carries the lane name.
		variant := celestrak
		variant.SourceName = fmt.Sprintf("%s-replay-%d", celestrak.SourceName, round)
		records, err := buildShapeBatch(variant, first, first+r.n)
		if err != nil {
			t.Fatal(err)
		}
		// A tag's created_at is in seconds, and the reconcile keeps the newest
		// tag of each key: this round's copies must be a second newer than the
		// last round's.
		time.Sleep(1100 * time.Millisecond)
		ingestStart := time.Now()
		if _, err := store.StoreBatchWithSourceTags(celestrak.Schema, records, celestrak.Producer, nil, tags); err != nil {
			t.Fatalf("round %s: ingest %d duplicates: %v", r.name, len(records), err)
		}
		ingestTook := time.Since(ingestStart)

		logs := startSupersedeLogs()
		stopReaders, readerSets := startShapeReaders(ops, readers)
		load := LoadAverage1()
		started := time.Now()
		result, reconcileErr := store.ReconcileSourceBatchIndexedDuplicates(celestrak.Schema, tags.ProviderID, tags.SourceName, tags.BatchID, true)
		took := time.Since(started)
		stopReaders()
		// Let the last log lines drain.
		time.Sleep(200 * time.Millisecond)
		logs.stop()
		if reconcileErr != nil {
			t.Fatalf("round %s: ReconcileSourceBatchIndexedDuplicates: %v", r.name, reconcileErr)
		}

		report.Add(ShapeMetric{
			Name:      "reconcile round " + r.name,
			Value:     fmt.Sprintf("matched %d, deleted %d in %s (batch %s; %d duplicates ingested in %s)", result.Matched, result.Deleted, rd(took), batch, len(records), rd(ingestTook)),
			LoadStart: load, LoadEnd: LoadAverage1(),
		})
		if result.Matched != int64(r.n) || result.Deleted != int64(r.n) {
			t.Errorf("round %s: matched %d, deleted %d; want %d each", r.name, result.Matched, result.Deleted, r.n)
		}
		fmt.Fprintf(&b, "\nround %s: reconcile of %s/%s %s: matched %d, deleted %d in %s (load %.1f→%.1f); ingest of %d duplicates took %s\n",
			r.name, tags.ProviderID, tags.SourceName, batch, result.Matched, result.Deleted, took.Round(time.Millisecond), load, LoadAverage1(), len(records), ingestTook.Round(time.Millisecond))
		reportShapeRun(t, &b, report, logs, "reconcile", supersedeShapeChunkLimit, ops, baselineSets, readerSets)
	}
	t.Logf("\n%s", b.String())
	if err := report.Write(report.Path); err != nil {
		t.Errorf("write report: %v", err)
	}
}
