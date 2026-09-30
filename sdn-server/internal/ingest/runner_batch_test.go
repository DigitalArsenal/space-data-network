package ingest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format2"
	"github.com/spacedatanetwork/sdn-server/internal/wasmrt"
)

// openFormat2Store opens a fresh store format 2 the way the daemon does
// (the engine prewarmed, host-02's topology).
func openFormat2Store(t *testing.T) *storage.FlatSQLStore {
	t.Helper()
	if !flatsqlrt.NativeHostIOSupported() {
		t.Skip("the C host I/O module is not available on this platform")
	}
	if rep := wasmrt.SubstrateStatus(); !rep.Patched() {
		if os.Getenv("SDN_WASM_REQUIRE_PATCHED") == "1" {
			t.Fatalf("runtime is not patched: %+v", rep)
		}
		t.Skipf("linked libwasmedge lacks the SDN runtime patches (%+v)", rep)
	}
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
	t.Cleanup(func() { store.Close() })
	if !store.Format2() {
		t.Fatal("SDN_STORE_FORMAT=2 opened a format-1 store")
	}
	return store
}

func machine() string {
	out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
	if err != nil {
		out, _ = os.ReadFile("/proc/loadavg")
	}
	var load float64
	fmt.Sscanf(strings.Trim(strings.TrimSpace(string(out)), "{} "), "%f", &load)
	return fmt.Sprintf("%s/%s, %d CPUs, load %.1f", runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), load)
}

// gpRows is n GP rows, one object each.
func gpRows(n int) []map[string]string {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	rows := make([]map[string]string, n)
	for i := range rows {
		rows[i] = map[string]string{
			"NORAD_CAT_ID": fmt.Sprint(40000 + i), "OBJECT_NAME": fmt.Sprintf("SAT %d", i),
			"OBJECT_ID": fmt.Sprintf("2026-%03dA", i%1000), "EPOCH": base.Add(time.Duration(i) * time.Minute).Format("2006-01-02T15:04:05"),
			"MEAN_MOTION": "15.5", "ECCENTRICITY": "0.0001", "INCLINATION": "51.6", "RA_OF_ASC_NODE": "10",
			"ARG_OF_PERICENTER": "20", "MEAN_ANOMALY": "30", "BSTAR": "0.0001",
		}
	}
	return rows
}

func gpRowsCSV(rows []map[string]string) []byte {
	cols := []string{"NORAD_CAT_ID", "OBJECT_NAME", "OBJECT_ID", "EPOCH", "MEAN_MOTION", "ECCENTRICITY", "INCLINATION",
		"RA_OF_ASC_NODE", "ARG_OF_PERICENTER", "MEAN_ANOMALY", "BSTAR"}
	var b strings.Builder
	b.WriteString(strings.Join(cols, ",") + "\n")
	for _, r := range rows {
		for i, c := range cols {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(r[c])
		}
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// M9 (terabyte audit): the runner's ingest hands the store its records in
// batches: a few writes per thousands of records, every record stored and
// readable at type level when the ingest returns. On 0de504531 each record
// was its own write: on format 1 two store-lock windows per record (the
// record, then its source tags), on format 2 a CID probe, a durable commit
// and a label wait per record (11 rec/s, 6,431 engine commits for 6,000
// records on the owner's Mac at load 63).
func TestRunnerIngestBatchesItsCommits(t *testing.T) {
	const n = 3000
	rows := gpRows(n)
	tags := sourceTags("space-track", "gp-history", "https://fixture.test/gp.csv", gpRowsCSV(rows))
	ingest := func(t *testing.T, runner *Runner) (int, time.Duration) {
		t.Helper()
		start := time.Now()
		countOMM, countMPE, hash, err := runner.ingestGPRows(rows, "source:spacetrack", tags)
		took := time.Since(start)
		if err != nil {
			t.Fatal(err)
		}
		if countOMM != n || countMPE != n || hash == "" {
			t.Fatalf("OMM/MPE %d/%d hash %q, want %d/%d", countOMM, countMPE, hash, n, n)
		}
		for _, schema := range []string{"OMM.fbs", "MPE.fbs"} {
			got, err := runner.store.Count(schema)
			if err != nil || got != n {
				t.Fatalf("%s: %d records when the ingest returned (%v), want %d", schema, got, err, n)
			}
		}
		return countOMM + countMPE, took
	}

	t.Run("format1", func(t *testing.T) {
		runner := newTestRunner(t)
		before := runner.store.StoreLockStats()
		records, took := ingest(t, runner)
		windows := runner.store.StoreLockStats().WriteAcquires - before.WriteAcquires
		t.Logf("MEASURED runner ingest, format 1: %d records in %v = %.0f rec/s, %d store-lock write windows (%s)", records,
			took.Round(time.Millisecond), float64(records)/took.Seconds(), windows, machine())
		if windows > int64(records/16) {
			t.Fatalf("%d records took %d store-lock write windows, want batches (at most one per 16 records)", records, windows)
		}
	})

	t.Run("format2", func(t *testing.T) {
		// The runner's builders finish size-prefixed buffers, which the
		// embedded engine stores since flatsql 3.5.0 (§38).
		store := openFormat2Store(t)
		dir := t.TempDir()
		runner, err := NewRunnerWithStore(Config{StoragePath: filepath.Join(dir, "store"), RawPath: filepath.Join(dir, "raw")}, store)
		if err != nil {
			t.Fatal(err)
		}
		before, err := store.PartitionStore().Writer().Stats()
		if err != nil {
			t.Fatal(err)
		}
		records, took := ingest(t, runner)
		after, err := store.PartitionStore().Writer().Stats()
		if err != nil {
			t.Fatal(err)
		}
		commits := after.Commits - before.Commits
		t.Logf("MEASURED runner ingest, format 2: %d records in %v = %.0f rec/s, %d engine commits (%s)", records,
			took.Round(time.Millisecond), float64(records)/took.Seconds(), commits, machine())
		if commits > uint64(records/16) {
			t.Fatalf("%d records took %d engine commits, want batches (at most one commit per 16 records)", records, commits)
		}
	})
}

// M9 review: a batch the store takes partly still fails the ingest. The
// engine refuses a record (here a CAT buffer handed in as OMM: its file
// identifier) and stores the rest; the batch's write error stops the ingest,
// so its checkpoint does not move past the refused record, as when each
// record was its own write (a one-record batch fails when its record is
// refused). The first batched cut only logged it: the ingest counted the
// record, provenance claimed it, and the feed never fetched it again.
func TestRunnerIngestBatchFailsOnARefusedRecord(t *testing.T) {
	store := openFormat2Store(t)
	// The builders finish size-prefixed buffers; an engine before flatsql's
	// size-prefixed records (§38) takes only the bare FlatBuffer.
	bare := func(b []byte) []byte { return b }
	probe := sds.NewOMMBuilder().WithNoradCatID(1).Build()
	if _, err := store.StoreBatch("OMM.fbs", [][]byte{probe}, "source:probe", nil); err != nil {
		if !strings.Contains(err.Error(), "code -102") {
			t.Fatal(err)
		}
		bare = func(b []byte) []byte { return b[4:] }
		if _, err := store.StoreBatch("OMM.fbs", [][]byte{bare(probe)}, "source:probe", nil); err != nil {
			t.Fatal(err)
		}
	}
	good1 := bare(sds.NewOMMBuilder().WithNoradCatID(10).Build())
	good2 := bare(sds.NewOMMBuilder().WithNoradCatID(11).Build())
	refused := bare(sds.NewCATBuilder().WithNoradCatID(12).Build())
	dir := t.TempDir()
	runner, err := NewRunnerWithStore(Config{StoragePath: filepath.Join(dir, "store"), RawPath: filepath.Join(dir, "raw")}, store)
	if err != nil {
		t.Fatal(err)
	}
	tags := sourceTags("space-track", "gp-history", "https://fixture.test/gp.csv", []byte("refusal"))
	batch := runner.newIngestBatcher("source:spacetrack", tags)
	for _, rec := range [][]byte{good1, refused, good2} {
		if err := batch.add("OMM.fbs", rec); err != nil {
			t.Fatal(err)
		}
	}
	err = batch.close()
	if err == nil || !strings.Contains(err.Error(), "refused 1 of 3") {
		t.Fatalf("an ingest batch with a refused record: %v, want the refusal as the ingest's error", err)
	}
	if n, err := store.Count("OMM.fbs"); err != nil || n != 3 {
		t.Fatalf("OMM holds %d records (%v), want the probe and the two accepted records", n, err)
	}
	// One accepted record alone is no error.
	ok := runner.newIngestBatcher("source:spacetrack", tags)
	if err := ok.add("OMM.fbs", bare(sds.NewOMMBuilder().WithNoradCatID(13).Build())); err != nil {
		t.Fatal(err)
	}
	if err := ok.close(); err != nil {
		t.Fatal(err)
	}
}
