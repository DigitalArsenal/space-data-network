package storage

// Store on-disk cost measurement harness (sdn-cellular-ingest-store-write-is-
// the-bottleneck). NOT a CI test: it needs a real $TBS stream fixture and is
// skipped without SDN_STORE_COST_FIXTURE. It exists so the bytes/record and
// rows/s claims in that task are MEASURED on this machine, per file, with the
// exact write path the storage_ingest capability uses.

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

func loadTBSFixture(t *testing.T, limit int) [][]byte {
	t.Helper()
	path := os.Getenv("SDN_STORE_COST_FIXTURE")
	if path == "" {
		t.Skip("set SDN_STORE_COST_FIXTURE to a size-prefixed $TBS stream file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var records [][]byte
	for off := 0; off+4 <= len(raw) && len(records) < limit; {
		n := int(binary.LittleEndian.Uint32(raw[off : off+4]))
		if n <= 0 || off+4+n > len(raw) {
			break
		}
		rec := make([]byte, n)
		copy(rec, raw[off+4:off+4+n])
		records = append(records, rec)
		off += 4 + n
	}
	if len(records) == 0 {
		t.Fatalf("fixture %s yielded no records", path)
	}
	return records
}

func dirBytes(t *testing.T, base string) (map[string]int64, int64) {
	t.Helper()
	out := map[string]int64{}
	var total int64
	err := filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(base, p)
		out[rel] = info.Size()
		total += info.Size()
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", base, err)
	}
	return out, total
}

func reportCost(t *testing.T, label, base string, records int, elapsed time.Duration) {
	files, total := dirBytes(t, base)
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return files[keys[i]] > files[keys[j]] })
	t.Logf("=== %s: %d records in %s (%.0f rows/s) ===", label, records, elapsed.Round(time.Millisecond), float64(records)/elapsed.Seconds())
	t.Logf("TOTAL %d B  =  %.1f B/record", total, float64(total)/float64(records))
	for _, k := range keys {
		if files[k] == 0 {
			continue
		}
		t.Logf("  %-52s %12d B  %8.1f B/rec", k, files[k], float64(files[k])/float64(records))
	}
}

// TestMeasureStoreCostPerRecord ingests real $TBS records through
// StoreBatchWithSourceTags (what caps/storage.go storage.ingest_with_source
// calls) and reports on-disk bytes per file per record.
func TestMeasureStoreCostPerRecord(t *testing.T) {
	limit := 100000
	if v := os.Getenv("SDN_STORE_COST_N"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	records := loadTBSFixture(t, limit)

	base := os.Getenv("SDN_STORE_COST_DIR")
	if base == "" {
		base = filepath.Join(t.TempDir(), "db")
	} else {
		_ = os.RemoveAll(base)
	}
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := NewFlatSQLStore(base, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}

	tags := SourceTags{
		ProviderID:        "mls-archive",
		SourceName:        "mls-final-full-cell-export",
		SourceURL:         "https://d229kd5ey79jzj.cloudfront.net/export/MLS-full-cell-export-2024-05-01T000000.csv.gz",
		BatchID:           "mls-archive@0",
		ProducerPeerID:    "12D3KooWQYV9dGMFoRzNStwpXztXaBUjtPqi6aU76ZgUriHhKust",
		ProducerPublicKey: "08011220b9cbd0a3fdbb5a1e2f4c8d9e0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3",
	}

	started := time.Now()
	inserted, err := store.StoreBatchWithSourceTags("TBS.fbs", records, "12D3KooWQYV9dGMFoRzNStwpXztXaBUjtPqi6aU76ZgUriHhKust", nil, tags)
	if err != nil {
		t.Fatalf("StoreBatchWithSourceTags: %v", err)
	}
	elapsed := time.Since(started)
	if inserted != len(records) {
		t.Logf("WARNING: inserted %d of %d records", inserted, len(records))
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var payload int64
	for _, r := range records {
		payload += int64(len(r))
	}
	t.Logf("fixture payload %d B over %d records = %.1f B/record", payload, len(records), float64(payload)/float64(len(records)))
	reportCost(t, fmt.Sprintf("StoreBatchWithSourceTags TBS.fbs"), base, inserted, elapsed)
}

// TestOpenExistingStoreRehearsal opens an EXISTING store directory with this
// build and reports what the boot migrations cost and what they reclaimed.
//
// It is the rehearsal the deploy law requires: run the migration on a COPY of
// the real layout, locally, before the binary that contains it reaches a box.
// Skipped unless SDN_STORE_REHEARSAL_DIR names a store copy.
func TestOpenExistingStoreRehearsal(t *testing.T) {
	base := os.Getenv("SDN_STORE_REHEARSAL_DIR")
	if base == "" {
		t.Skip("set SDN_STORE_REHEARSAL_DIR to a COPY of a store directory")
	}
	// THE RUNTIME PIN, STATED WITH THE MEASUREMENT. The store opens the engine
	// with WithPrecompiledAOTCache, which LOADS an artifact but never compiles
	// one — so a cold cache silently means an INTERPRETED engine, and an
	// interpreted engine turns a 30-second boot statement into one that blows
	// the engine's uninterruptible 5-minute per-call budget and poisons it.
	// A rehearsal that does not say which of the two it measured is not
	// evidence. Prewarm, then report.
	artifact, alreadyPresent, err := flatsqlrt.PrewarmAOTArtifact(engineAOTCacheDir(), "flatsql", flatsqlrt.EmbeddedWasm())
	if err != nil {
		t.Logf("AOT prewarm failed (%v) — this rehearsal runs INTERPRETED", err)
	} else {
		t.Logf("AOT artifact %s (already present: %v)", artifact, alreadyPresent)
	}

	_, before := dirBytes(t, base)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	started := time.Now()
	store, err := NewFlatSQLStore(base, validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore(%s): %v", base, err)
	}
	openElapsed := time.Since(started)
	if engine, _ := store.EngineRuntime(); engine != nil {
		t.Logf("engine AOT in use: %v", engine.AOT())
	}
	t.Logf("open + migrations took %s", openElapsed.Round(time.Millisecond))

	for _, q := range []struct{ label, sql string }{
		{"record index rows", `SELECT COUNT(*) FROM sdn_record_index`},
		{"record index TBS.fbs", `SELECT COUNT(*) FROM sdn_record_index WHERE schema_name = 'TBS.fbs'`},
		{"record index bare TBS", `SELECT COUNT(*) FROM sdn_record_index WHERE schema_name = 'TBS'`},
		{"source tag rows", `SELECT COUNT(*) FROM sdn_record_source_tag_rows`},
		{"interned provenance rows", `SELECT COUNT(*) FROM sdn_source_provenance`},
		{"source tags via the view", `SELECT COUNT(*) FROM sdn_record_source_tags`},
	} {
		var n int64
		if err := store.db.QueryRow(q.sql).Scan(&n); err != nil {
			t.Errorf("%s: %v", q.label, err)
			continue
		}
		t.Logf("  %-28s %d", q.label, n)
	}
	var ledger string
	if err := store.db.QueryRow(`SELECT value FROM sdn_metadata WHERE key = ?`, schemaNameCanonicalizationLedgerKey).Scan(&ledger); err == nil {
		t.Logf("  canonicalization ledger: %s", ledger)
	}

	closeStarted := time.Now()
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	t.Logf("close took %s", time.Since(closeStarted).Round(time.Millisecond))

	files, after := dirBytes(t, base)
	t.Logf("store %d B -> %d B (%+d B)", before, after, after-before)
	for name, size := range files {
		if size > 1<<20 {
			t.Logf("  %-44s %13d B", name, size)
		}
	}
}
