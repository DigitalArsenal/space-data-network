package ingest

// In-daemon ingest tests (loop C.6b): the runner drives the fetch/parse/
// store pipeline against an ALREADY-OPEN store — the daemon's own handle —
// and records must land through the normal single-writer path: durable
// stream+journal writes, datasync cursor rowids advancing, engine hot-window
// enforcement, and engine-mirror invalidation (query-cache generation bump →
// response-artifact cache key / ETag changes) visible end-to-end.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// TestNewRunnerFailsCleanlyWhenDaemonHoldsStore is the CLI topology guard:
// `spacedatanetwork ingest --storage-path <daemon store>` must fail with the
// clean single-writer lock error (never journal corruption) when a daemon
// process holds the store.
func TestNewRunnerFailsCleanlyWhenDaemonHoldsStore(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	dir := t.TempDir()
	base := filepath.Join(dir, "store")

	daemonStore, err := storage.NewFlatSQLStore(base, validator)
	if err != nil {
		t.Fatalf("open daemon store: %v", err)
	}
	defer daemonStore.Close()

	_, err = NewRunner(Config{
		StoragePath: base,
		RawPath:     filepath.Join(dir, "raw"),
	})
	if !errors.Is(err, storage.ErrStoreLocked) {
		t.Fatalf("NewRunner against daemon-held store: err = %v, want storage.ErrStoreLocked", err)
	}
}

// TestInDaemonIngestSharedStoreEndToEnd proves the in-daemon topology:
// NewRunnerWithStore ingests through the daemon's store handle and the
// results are visible on every store surface the daemon serves from.
func TestInDaemonIngestSharedStoreEndToEnd(t *testing.T) {
	fixture, err := os.ReadFile("testdata/celestrak-gp-omm.csv")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	// Second batch: same objects, newer epochs → different payload bytes,
	// different batch_id, new CIDs.
	fixture2 := bytes.ReplaceAll(fixture, []byte("2026-01-01T"), []byte("2026-01-02T"))
	if bytes.Equal(fixture, fixture2) {
		t.Fatalf("fixture rewrite produced identical bytes")
	}

	var fetches atomic.Int64
	sourceServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := fetches.Add(1)
		payload := fixture
		if n > 1 {
			payload = fixture2
		}
		w.Header().Set("ETag", fmt.Sprintf(`"fixture-gp-%d"`, n))
		w.Header().Set("Content-Type", "text/csv")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer sourceServer.Close()

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	dir := t.TempDir()
	base := filepath.Join(dir, "store")

	// The "daemon's" store handle: hot window of 1 so eviction provably
	// runs during this test (2 records per batch).
	daemonStore, err := storage.NewFlatSQLStore(base, validator, storage.WithEngineHotWindow(1))
	if err != nil {
		t.Fatalf("open daemon store: %v", err)
	}
	defer daemonStore.Close()

	runner, err := NewRunnerWithStore(Config{
		StoragePath:            base,
		RawPath:                filepath.Join(dir, "raw"),
		CelestrakCatalogURL:    sourceServer.URL + "/gp.csv",
		CelestrakInterval:      minCelestrakFetchInterval,
		SatcatInterval:         minCelestrakFetchInterval,
		SpaceWeatherInterval:   minCelestrakFetchInterval,
		SpaceTrackPollInterval: time.Hour,
	}, daemonStore)
	if err != nil {
		t.Fatalf("NewRunnerWithStore failed: %v", err)
	}
	if err := runner.Close(); err != nil {
		t.Fatalf("runner.Close must not close the daemon's store: %v", err)
	}
	if _, err := daemonStore.QueryAll("OMM.fbs", 1); err != nil {
		t.Fatalf("daemon store unusable after runner.Close: %v", err)
	}

	headFilter := storage.RawRecordQuery{SchemaName: "OMM.fbs", Limit: 1}
	head0, err := daemonStore.RawRecordHead(headFilter)
	if err != nil {
		t.Fatalf("RawRecordHead baseline: %v", err)
	}
	if head0.MaxRowID != 0 {
		t.Fatalf("baseline MaxRowID = %d, want 0", head0.MaxRowID)
	}

	// --- Batch 1 ---
	if err := runner.syncCelestrakGP(context.Background()); err != nil {
		t.Fatalf("syncCelestrakGP #1: %v", err)
	}

	stored, err := daemonStore.QueryAll("OMM.fbs", 10)
	if err != nil {
		t.Fatalf("QueryAll after batch 1: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("durable OMM records after batch 1 = %d, want 2", len(stored))
	}

	// Hot-window enforcement applies on the shared handle.
	resident, err := daemonStore.EngineRecordCount("OMM.fbs")
	if err != nil {
		t.Fatalf("EngineRecordCount after batch 1: %v", err)
	}
	if resident != 1 {
		t.Fatalf("engine-resident OMM records after batch 1 = %d, want 1 (hot window)", resident)
	}

	// Datasync cursor (sdn_record_index rowids) advanced.
	head1, err := daemonStore.RawRecordHead(headFilter)
	if err != nil {
		t.Fatalf("RawRecordHead after batch 1: %v", err)
	}
	if head1.MaxRowID <= 0 {
		t.Fatalf("MaxRowID after batch 1 = %d, want > 0", head1.MaxRowID)
	}

	// The flow-served bulk query surface reflects the new records.
	epoch := float64(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix())
	stream1, err := daemonStore.QueryEpochRawStream("OMM.fbs", "", "nearest", epoch, 10)
	if err != nil {
		t.Fatalf("QueryEpochRawStream after batch 1: %v", err)
	}
	if stream1.Rows == 0 || len(stream1.Bytes) == 0 {
		t.Fatalf("epoch stream after batch 1: rows=%d bytes=%d, want data", stream1.Rows, len(stream1.Bytes))
	}
	// The response-artifact cache key is content-addressed by (schema,
	// version, format, sql, params) and intentionally STABLE across data
	// changes; freshness comes from the engine's query-cache generation.
	// It must resolve on the daemon's shared handle.
	if key, err := daemonStore.ResponseArtifactCacheKey("OMM", "1",
		`SELECT _data FROM OMM ORDER BY NORAD_CAT_ID`,
		flatsqlrt.ResponseArtifactKeyOptions{Format: "flatbuffer"}); err != nil || key == "" {
		t.Fatalf("ResponseArtifactCacheKey: key=%q err=%v", key, err)
	}

	// --- Batch 2 (new epochs) ---
	// Backdate the fetch cache beyond the CelesTrak minimum interval so the
	// second cycle actually refetches (production waits 3h between cycles).
	cachePath := filepath.Join(dir, "raw", "cache", "celestrak-gp.csv")
	past := time.Now().Add(-2 * minCelestrakFetchInterval)
	if err := os.Chtimes(cachePath, past, past); err != nil {
		t.Fatalf("backdate fetch cache: %v", err)
	}
	if err := runner.syncCelestrakGP(context.Background()); err != nil {
		t.Fatalf("syncCelestrakGP #2: %v", err)
	}

	stored, err = daemonStore.QueryAll("OMM.fbs", 10)
	if err != nil {
		t.Fatalf("QueryAll after batch 2: %v", err)
	}
	if len(stored) != 4 {
		t.Fatalf("durable OMM records after batch 2 = %d, want 4", len(stored))
	}

	resident, err = daemonStore.EngineRecordCount("OMM.fbs")
	if err != nil {
		t.Fatalf("EngineRecordCount after batch 2: %v", err)
	}
	if resident != 1 {
		t.Fatalf("engine-resident OMM records after batch 2 = %d, want 1 (hot window)", resident)
	}

	head2, err := daemonStore.RawRecordHead(headFilter)
	if err != nil {
		t.Fatalf("RawRecordHead after batch 2: %v", err)
	}
	if head2.MaxRowID <= head1.MaxRowID {
		t.Fatalf("MaxRowID did not advance: batch1=%d batch2=%d", head1.MaxRowID, head2.MaxRowID)
	}

	stream2, err := daemonStore.QueryEpochRawStream("OMM.fbs", "", "nearest", epoch, 10)
	if err != nil {
		t.Fatalf("QueryEpochRawStream after batch 2: %v", err)
	}
	if stream2.Rows == 0 || len(stream2.Bytes) == 0 {
		t.Fatalf("epoch stream after batch 2: rows=%d bytes=%d, want data", stream2.Rows, len(stream2.Bytes))
	}

	// Generation-bump invalidation, end to end: this is the IDENTICAL
	// (sql, params) query as stream1, so if ingest had not bumped the
	// engine's query-cache generation, the host raw-stream mirror would
	// serve the batch-1 bytes verbatim. New bytes + new FNV-1a64 (the
	// etag identity the data-retrieval flow serves) prove the cached
	// response was invalidated and the flow-served endpoint reflects the
	// new records.
	if bytes.Equal(stream1.Bytes, stream2.Bytes) {
		t.Fatalf("bulk query bytes unchanged after batch 2 — the cached stream was not invalidated")
	}
	if stream1.FNV1a64 == stream2.FNV1a64 {
		t.Fatalf("stream FNV-1a64 etag unchanged after batch 2: %016x", stream1.FNV1a64)
	}
}
