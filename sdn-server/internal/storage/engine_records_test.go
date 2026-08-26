package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/IRM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/TBS"

	"github.com/spacedatanetwork/sdn-server/internal/flatsqlrt"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// buildEngineOMM produces one UNPREFIXED $OMM FlatBuffer (the Store API's
// input shape; sds builders return size-prefixed bytes, so the prefix is
// stripped) with both the EPOCH string and the numeric
// USER_DEFINED_EPOCH_TIMESTAMP populated.
func buildEngineOMM(t *testing.T, norad uint32, name string, epochUnix int64) []byte {
	t.Helper()
	epoch := time.Unix(epochUnix, 0).UTC().Format("2006-01-02T15:04:05Z")
	data := sds.NewOMMBuilder().
		WithNoradCatID(norad).
		WithObjectName(name).
		WithObjectID(fmt.Sprintf("2024-%03dA", norad%1000)).
		WithEpoch(epoch).
		WithEpochTimestamp(float64(epochUnix)).
		WithMeanMotion(15.5).
		WithEccentricity(0.0001).
		WithInclination(53.0).
		Build()
	return data[4:] // strip the 4-byte size prefix
}

func newEngineRecordsStore(t *testing.T, basePath string) *FlatSQLStore {
	t.Helper()
	return newEngineRecordsStoreWithOptions(t, basePath)
}

func newEngineRecordsStoreWithOptions(t *testing.T, basePath string, opts ...StoreOption) *FlatSQLStore {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(basePath, validator, opts...)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	return store
}

// decodeEpochFrames decodes a raw stream into (norad -> epoch unix) and
// validates every frame is a well-formed unprefixed OMM buffer.
func decodeEpochFrames(t *testing.T, stream *flatsqlrt.RawStream) map[uint32]float64 {
	t.Helper()
	frames, err := flatsqlrt.DecodeSizePrefixedStream(stream.Bytes)
	if err != nil {
		t.Fatalf("decode raw stream: %v", err)
	}
	out := make(map[uint32]float64, len(frames))
	for i, frame := range frames {
		if !OMM.OMMBufferHasIdentifier(frame) {
			t.Fatalf("frame %d is not a valid $OMM buffer", i)
		}
		omm := OMM.GetRootAsOMM(frame, 0)
		out[omm.NORAD_CAT_ID()] = omm.USER_DEFINED_EPOCH_TIMESTAMP()
	}
	if len(out) != len(frames) {
		t.Fatalf("duplicate NORAD_CAT_ID in %d frames (%d unique)", len(frames), len(out))
	}
	return out
}

func TestEngineEpochQueries(t *testing.T) {
	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	epoch1 := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC).Unix()
	epoch2 := epoch1 + 2*86400
	norads := []uint32{1001, 1002, 1003}

	var records [][]byte
	for _, norad := range norads {
		records = append(records,
			buildEngineOMM(t, norad, fmt.Sprintf("SAT-%d", norad), epoch1),
			buildEngineOMM(t, norad, fmt.Sprintf("SAT-%d", norad), epoch2),
		)
	}
	tags := SourceTags{ProviderID: "prov-a", SourceName: "catalogfixture-gp", BatchID: "batch-1"}
	inserted, err := store.StoreBatchWithSourceTags("OMM.fbs", records, "peer-epoch-test", nil, tags)
	if err != nil {
		t.Fatalf("StoreBatchWithSourceTags failed: %v", err)
	}
	if inserted != 6 {
		t.Fatalf("inserted %d records, want 6", inserted)
	}

	count, err := store.EngineRecordCount("OMM.fbs")
	if err != nil {
		t.Fatalf("EngineRecordCount failed: %v", err)
	}
	if count != 6 {
		t.Fatalf("engine record count = %d, want 6", count)
	}

	// nearest: target 36h after epoch1 is 12h before epoch2 -> epoch2 wins
	// for every object.
	target := float64(epoch1 + 36*3600)
	stream, err := store.QueryEpochRawStream("OMM.fbs", "", "nearest", target, 0)
	if err != nil {
		t.Fatalf("QueryEpochRawStream nearest failed: %v", err)
	}
	nearest := decodeEpochFrames(t, stream)
	if len(nearest) != 3 {
		t.Fatalf("nearest returned %d frames, want 3", len(nearest))
	}
	for _, norad := range norads {
		if got, ok := nearest[norad]; !ok || got != float64(epoch2) {
			t.Fatalf("nearest norad %d matched epoch %v (present=%v), want %d", norad, got, ok, epoch2)
		}
	}

	// as_of: latest record at-or-before the target -> epoch1.
	stream, err = store.QueryEpochRawStream("OMM.fbs", "", "as_of", target, 0)
	if err != nil {
		t.Fatalf("QueryEpochRawStream as_of failed: %v", err)
	}
	asOf := decodeEpochFrames(t, stream)
	if len(asOf) != 3 {
		t.Fatalf("as_of returned %d frames, want 3", len(asOf))
	}
	for _, norad := range norads {
		if asOf[norad] != float64(epoch1) {
			t.Fatalf("as_of norad %d matched epoch %v, want %d", norad, asOf[norad], epoch1)
		}
	}
	// as_of before every stored epoch -> nothing.
	stream, err = store.QueryEpochRawStream("OMM.fbs", "", "as_of", float64(epoch1-86400), 0)
	if err != nil {
		t.Fatalf("QueryEpochRawStream as_of (early) failed: %v", err)
	}
	if empty := decodeEpochFrames(t, stream); len(empty) != 0 {
		t.Fatalf("as_of before history returned %d frames, want 0", len(empty))
	}

	// forward: earliest record at-or-after the target -> epoch2.
	stream, err = store.QueryEpochRawStream("OMM.fbs", "", "forward", target, 0)
	if err != nil {
		t.Fatalf("QueryEpochRawStream forward failed: %v", err)
	}
	forward := decodeEpochFrames(t, stream)
	if len(forward) != 3 {
		t.Fatalf("forward returned %d frames, want 3", len(forward))
	}
	for _, norad := range norads {
		if forward[norad] != float64(epoch2) {
			t.Fatalf("forward norad %d matched epoch %v, want %d", norad, forward[norad], epoch2)
		}
	}

	// A second source partitions correctly.
	otherTags := SourceTags{ProviderID: "prov-b", SourceName: "provider-two", BatchID: "batch-1"}
	if _, err := store.StoreBatchWithSourceTags("OMM.fbs",
		[][]byte{buildEngineOMM(t, 2001, "OTHER-SAT", epoch1+3600)},
		"peer-epoch-test", nil, otherTags); err != nil {
		t.Fatalf("StoreBatchWithSourceTags provider-two failed: %v", err)
	}
	count, err = store.EngineRecordCount("OMM.fbs")
	if err != nil {
		t.Fatalf("EngineRecordCount failed: %v", err)
	}
	if count != 7 {
		t.Fatalf("engine record count = %d, want 7", count)
	}
	stream, err = store.QueryEpochRawStream("OMM.fbs", "provider-two", "nearest", target, 0)
	if err != nil {
		t.Fatalf("QueryEpochRawStream provider-two failed: %v", err)
	}
	p2 := decodeEpochFrames(t, stream)
	if len(p2) != 1 || p2[2001] == 0 {
		t.Fatalf("provider-two filter returned %v, want exactly norad 2001", p2)
	}
	stream, err = store.QueryEpochRawStream("OMM.fbs", "catalogfixture-gp", "nearest", target, 0)
	if err != nil {
		t.Fatalf("QueryEpochRawStream catalogfixture-gp failed: %v", err)
	}
	if cg := decodeEpochFrames(t, stream); len(cg) != 3 {
		t.Fatalf("catalogfixture-gp filter returned %d frames, want 3", len(cg))
	}
	stream, err = store.QueryEpochRawStream("OMM.fbs", "", "nearest", target, 0)
	if err != nil {
		t.Fatalf("QueryEpochRawStream all-sources failed: %v", err)
	}
	if all := decodeEpochFrames(t, stream); len(all) != 4 {
		t.Fatalf("all-sources nearest returned %d frames, want 4", len(all))
	}

	// Untagged batch writes partition under the default "local" source.
	if _, err := store.StoreBatch("OMM.fbs",
		[][]byte{buildEngineOMM(t, 3001, "LOCAL-SAT", epoch1)},
		"peer-epoch-test", nil); err != nil {
		t.Fatalf("StoreBatch untagged failed: %v", err)
	}
	stream, err = store.QueryEpochRawStream("OMM.fbs", "local", "nearest", target, 0)
	if err != nil {
		t.Fatalf("QueryEpochRawStream local failed: %v", err)
	}
	if local := decodeEpochFrames(t, stream); len(local) != 1 || local[3001] == 0 {
		t.Fatalf("local filter returned %v, want exactly norad 3001", local)
	}

	// Unsupported shapes error cleanly.
	if _, err := store.QueryEpochRawStream("CAT.fbs", "", "nearest", target, 0); err == nil {
		t.Fatal("expected error for non-OMM schema")
	}
	if _, err := store.QueryEpochRawStream("OMM.fbs", "", "day", target, 0); err == nil {
		t.Fatal("expected error for unsupported profile")
	}
}

func TestEngineRecordsBootRebuild(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, basePath)

	epoch1 := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC).Unix()
	epoch2 := epoch1 + 2*86400
	var records [][]byte
	for _, norad := range []uint32{1001, 1002, 1003} {
		records = append(records,
			buildEngineOMM(t, norad, fmt.Sprintf("SAT-%d", norad), epoch1),
			buildEngineOMM(t, norad, fmt.Sprintf("SAT-%d", norad), epoch2),
		)
	}
	tags := SourceTags{ProviderID: "prov-a", SourceName: "catalogfixture-gp", BatchID: "batch-1"}
	if _, err := store.StoreBatchWithSourceTags("OMM.fbs", records, "peer-epoch-test", nil, tags); err != nil {
		t.Fatalf("StoreBatchWithSourceTags failed: %v", err)
	}
	if count, err := store.EngineRecordCount("OMM.fbs"); err != nil || count != 6 {
		t.Fatalf("pre-close engine count = %d err=%v, want 6", count, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Reopen: the engine hot window must rebuild from control tables +
	// stream files.
	reopened := newEngineRecordsStore(t, basePath)
	defer reopened.Close()

	count, err := reopened.EngineRecordCount("OMM.fbs")
	if err != nil {
		t.Fatalf("EngineRecordCount after reopen failed: %v", err)
	}
	if count != 6 {
		t.Fatalf("engine record count after reopen = %d, want 6", count)
	}
	stream, err := reopened.QueryEpochRawStream("OMM.fbs", "", "nearest", float64(epoch1+36*3600), 0)
	if err != nil {
		t.Fatalf("QueryEpochRawStream after reopen failed: %v", err)
	}
	nearest := decodeEpochFrames(t, stream)
	if len(nearest) != 3 {
		t.Fatalf("nearest after reopen returned %d frames, want 3", len(nearest))
	}
	for _, norad := range []uint32{1001, 1002, 1003} {
		if nearest[norad] != float64(epoch2) {
			t.Fatalf("nearest after reopen norad %d matched %v, want %d", norad, nearest[norad], epoch2)
		}
	}
}

func TestEngineHotWindowHydratesFromCompactCatalogBeforeFullReplay(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(basePath, validator, WithEngineHotWindow(10))
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}

	epoch1 := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC).Unix()
	epoch2 := epoch1 + 2*86400
	catalogfixtureTags := SourceTags{ProviderID: "prov-a", SourceName: "catalogfixture-gp", BatchID: "batch-1"}
	if _, err := store.StoreBatchWithSourceTags("OMM.fbs", [][]byte{
		buildEngineOMM(t, 1001, "SAT-1001-A", epoch1),
		buildEngineOMM(t, 1001, "SAT-1001-B", epoch2),
		buildEngineOMM(t, 1002, "SAT-1002", epoch2),
	}, "peer-engine-catalog", nil, catalogfixtureTags); err != nil {
		t.Fatalf("StoreBatchWithSourceTags catalogfixture failed: %v", err)
	}
	otherTags := SourceTags{ProviderID: "prov-b", SourceName: "provider-two", BatchID: "batch-1"}
	if _, err := store.StoreBatchWithSourceTags("OMM.fbs", [][]byte{
		buildEngineOMM(t, 2001, "OTHER-SAT", epoch2),
	}, "peer-engine-catalog", nil, otherTags); err != nil {
		t.Fatalf("StoreBatchWithSourceTags provider-two failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	reopened, err := NewFlatSQLStore(basePath, validator,
		WithDeferredBootRebuilds(),
		WithDeferredRecordCatalogReplay(),
		WithEngineHotWindow(10))
	if err != nil {
		t.Fatalf("deferred reopen failed: %v", err)
	}
	defer reopened.Close()

	if count, err := reopened.EngineRecordCount("OMM.fbs"); err != nil || count != 0 {
		t.Fatalf("deferred reopen engine count = %d err=%v, want 0 nil", count, err)
	}
	stream, err := reopened.QueryEpochRawStream("OMM.fbs", "catalogfixture-gp", "nearest", float64(epoch2), 0)
	if err != nil {
		t.Fatalf("QueryEpochRawStream before hot-window hydration failed: %v", err)
	}
	if frames := decodeEpochFrames(t, stream); len(frames) != 0 {
		t.Fatalf("deferred reopen returned %d frames before hot-window hydration, want 0", len(frames))
	}

	loaded, err := reopened.HydrateEngineHotWindowFromRecordCatalog()
	if err != nil {
		t.Fatalf("HydrateEngineHotWindowFromRecordCatalog failed: %v", err)
	}
	if loaded != 4 {
		t.Fatalf("HydrateEngineHotWindowFromRecordCatalog loaded %d records, want 4", loaded)
	}
	if reopened.RecordCatalogHydrated() {
		t.Fatal("engine hot-window hydration must not mark the full record catalog hydrated")
	}
	if count, err := reopened.EngineRecordCount("OMM.fbs"); err != nil || count != 4 {
		t.Fatalf("engine count after compact hot-window hydration = %d err=%v, want 4 nil", count, err)
	}
	stream, err = reopened.QueryEpochRawStream("OMM.fbs", "catalogfixture-gp", "nearest", float64(epoch2), 0)
	if err != nil {
		t.Fatalf("QueryEpochRawStream after hot-window hydration failed: %v", err)
	}
	nearest := decodeEpochFrames(t, stream)
	if len(nearest) != 2 || nearest[1001] != float64(epoch2) || nearest[1002] != float64(epoch2) {
		t.Fatalf("catalogfixture nearest after hot-window hydration = %v, want 1001/1002 at epoch2", nearest)
	}
}

// TestEpochEngineMeasure reports the B.3 numbers: full-store write throughput
// at catalog-history scale (29K objects x 5 epochs = 145K OMM records) and
// old (control-index + stream hydration) vs new (engine-native raw stream)
// nearest-epoch latency. Only runs when explicitly requested:
//
//	FLATSQLRT_SCALE_MEASURE=1 go test ./internal/storage/ -run TestEpochEngineMeasure -v -timeout 60m
func TestEpochEngineMeasure(t *testing.T) {
	if os.Getenv("FLATSQLRT_SCALE_MEASURE") == "" {
		t.Skip("set FLATSQLRT_SCALE_MEASURE=1 to run epoch engine measurements")
	}
	const (
		objects       = 29000
		epochsPerObj  = 5
		batchSize     = 5000
		baseEpochUnix = 1778400000 // 2026-05-10T00:00:00Z
	)

	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	tags := SourceTags{ProviderID: "prov-a", SourceName: "catalogfixture-gp", BatchID: "batch-scale"}
	total := objects * epochsPerObj
	var (
		batch      [][]byte
		batchBytes int64
		totalBytes int64
		inserted   int
		storeDur   time.Duration
	)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		start := time.Now()
		n, err := store.StoreBatchWithSourceTags("OMM.fbs", batch, "peer-epoch-scale", nil, tags)
		if err != nil {
			t.Fatalf("StoreBatchWithSourceTags failed after %d records: %v", inserted, err)
		}
		storeDur += time.Since(start)
		inserted += n
		totalBytes += batchBytes
		batch = batch[:0]
		batchBytes = 0
	}
	for e := 0; e < epochsPerObj; e++ {
		for i := 0; i < objects; i++ {
			norad := uint32(10000 + i)
			epoch := baseEpochUnix + int64(e*86400+i%86400)
			rec := buildEngineOMM(t, norad, fmt.Sprintf("SAT-%d", norad), epoch)
			batch = append(batch, rec)
			batchBytes += int64(len(rec))
			if len(batch) == batchSize {
				flush()
			}
		}
	}
	flush()
	if inserted != total {
		t.Fatalf("inserted %d records, want %d", inserted, total)
	}
	mb := float64(totalBytes) / (1024 * 1024)
	t.Logf("MEASURE full-store ingest: %d records (%.1f MB) in %s (%.0f rec/s, %.1f MB/s) via StoreBatchWithSourceTags batches of %d",
		total, mb, storeDur, float64(total)/storeDur.Seconds(), mb/storeDur.Seconds(), batchSize)

	if count, err := store.EngineRecordCount("OMM.fbs"); err != nil || count != int64(total) {
		t.Fatalf("engine record count = %d err=%v, want %d", count, err, total)
	}

	targetUnix := int64(baseEpochUnix + 2*86400 + 43200)

	// OLD path: control-index window query + per-record stream hydration
	// (the way internal/api's epoch handler calls it).
	start := time.Now()
	matches, err := store.QueryEpochRecords(EpochRecordQuery{
		SchemaName: "OMM.fbs",
		Profile:    EpochProfileNearest,
		At:         time.Unix(targetUnix, 0).UTC(),
		Limit:      250000,
	})
	if err != nil {
		t.Fatalf("QueryEpochRecords nearest failed: %v", err)
	}
	oldDur := time.Since(start)
	if len(matches) != objects {
		t.Fatalf("old path returned %d matches, want %d", len(matches), objects)
	}
	t.Logf("MEASURE OLD epoch.nearest (QueryEpochRecords over %d rows): %d matches in %s", total, len(matches), oldDur)

	// NEW path: engine-native window query, aligned frames out.
	start = time.Now()
	stream, err := store.QueryEpochRawStream("OMM.fbs", "", "nearest", float64(targetUnix), 0)
	if err != nil {
		t.Fatalf("QueryEpochRawStream nearest failed: %v", err)
	}
	newDur := time.Since(start)
	frames, err := flatsqlrt.DecodeSizePrefixedStream(stream.Bytes)
	if err != nil {
		t.Fatalf("decode raw stream: %v", err)
	}
	if len(frames) != objects {
		t.Fatalf("new path returned %d frames, want %d", len(frames), objects)
	}
	outMB := float64(len(stream.Bytes)) / (1024 * 1024)
	t.Logf("MEASURE NEW epoch.nearest (QueryEpochRawStream over %d rows): %d aligned frames (%.1f MB) in %s",
		total, len(frames), outMB, newDur)
	t.Logf("MEASURE old/new nearest latency ratio: %.1fx", float64(oldDur)/float64(newDur))
}

// TestEngineHotWindowEviction (loop C.4): the configured hot window is
// enforced at ingest by tombstoning the OLDEST resident engine records, and
// at boot by the bounded rebuild — while the durable substrate (control
// rows, stream files, datasync cursor rowid space) keeps every record.
func TestEngineHotWindowEviction(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(basePath, validator, WithEngineHotWindow(10))
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}

	epoch1 := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC).Unix()
	epoch2 := epoch1 + 2*86400
	epoch3 := epoch1 + 4*86400

	// Batch 1: 3 objects x 2 epochs = 6 records (fits the window).
	var batch1 [][]byte
	for _, norad := range []uint32{1001, 1002, 1003} {
		batch1 = append(batch1,
			buildEngineOMM(t, norad, fmt.Sprintf("SAT-%d", norad), epoch1),
			buildEngineOMM(t, norad, fmt.Sprintf("SAT-%d", norad), epoch2),
		)
	}
	evictedPayload := batch1[0] // 1001@epoch1 — the oldest ingested record
	tags := SourceTags{ProviderID: "prov-a", SourceName: "catalogfixture-gp", BatchID: "batch-1"}
	if _, err := store.StoreBatchWithSourceTags("OMM.fbs", batch1, "peer-evict-test", nil, tags); err != nil {
		t.Fatalf("StoreBatchWithSourceTags batch1 failed: %v", err)
	}
	if count, _ := store.EngineRecordCount("OMM.fbs"); count != 6 {
		t.Fatalf("engine count after batch1 = %d, want 6", count)
	}

	// Batch 2: 9 more records -> 15 total, 5 over the window. The 5 OLDEST
	// (1001@e1, 1001@e2, 1002@e1, 1002@e2, 1003@e1) must be evicted from the
	// ENGINE only.
	var batch2 [][]byte
	for norad := uint32(2001); norad <= 2009; norad++ {
		batch2 = append(batch2, buildEngineOMM(t, norad, fmt.Sprintf("SAT-%d", norad), epoch3))
	}
	tags2 := SourceTags{ProviderID: "prov-a", SourceName: "catalogfixture-gp", BatchID: "batch-2"}
	if _, err := store.StoreBatchWithSourceTags("OMM.fbs", batch2, "peer-evict-test", nil, tags2); err != nil {
		t.Fatalf("StoreBatchWithSourceTags batch2 failed: %v", err)
	}

	count, err := store.EngineRecordCount("OMM.fbs")
	if err != nil {
		t.Fatalf("EngineRecordCount failed: %v", err)
	}
	if count != 10 {
		t.Fatalf("engine count after eviction = %d, want 10 (window)", count)
	}

	// Query results reflect the window. Ingest order was [1001e1, 1001e2,
	// 1002e1, 1002e2, 1003e1, 1003e2, 2001..2009]; the 5 oldest (1001e1,
	// 1001e2, 1002e1, 1002e2, 1003e1) are evicted, so nearest(all) sees
	// exactly 10 objects: 1003 (via its resident epoch2 record) + 2001..2009.
	stream, err := store.QueryEpochRawStream("OMM.fbs", "", "nearest", float64(epoch3), 0)
	if err != nil {
		t.Fatalf("QueryEpochRawStream failed: %v", err)
	}
	nearest := decodeEpochFrames(t, stream)
	if len(nearest) != 10 {
		t.Fatalf("nearest returned %d objects, want 10", len(nearest))
	}
	if _, present := nearest[1001]; present {
		t.Fatal("norad 1001 should be fully evicted from the engine window")
	}
	if nearest[1003] != float64(epoch2) {
		t.Fatalf("norad 1003 epoch = %v, want %d (epoch1 record evicted, epoch2 resident)", nearest[1003], epoch2)
	}

	// The durable substrate is unaffected: every record is still point-
	// readable (stream files) and the datasync cursor space still covers all
	// 15 rows.
	evictedCID := computeCID(evictedPayload)
	rec, err := store.GetRawRecord("OMM.fbs", evictedCID)
	if err != nil {
		t.Fatalf("GetRawRecord for evicted record failed: %v", err)
	}
	if len(rec.Data) == 0 {
		t.Fatal("evicted record hydrated empty payload")
	}
	var indexRows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sdn_record_index WHERE schema_name = 'OMM.fbs'`).Scan(&indexRows); err != nil {
		t.Fatalf("count sdn_record_index: %v", err)
	}
	if indexRows != 15 {
		t.Fatalf("sdn_record_index rows = %d, want 15 (eviction must not touch cursor space)", indexRows)
	}
	raw, err := store.QueryRawRecords(RawRecordQuery{SchemaName: "OMM.fbs", Limit: 100})
	if err != nil {
		t.Fatalf("QueryRawRecords failed: %v", err)
	}
	if len(raw) != 15 {
		t.Fatalf("QueryRawRecords returned %d records, want 15", len(raw))
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Boot enforcement: reopening with the same window loads only the NEWEST
	// 10 records.
	reopened, err := NewFlatSQLStore(basePath, validator, WithEngineHotWindow(10))
	if err != nil {
		t.Fatalf("reopen with window failed: %v", err)
	}
	if count, _ := reopened.EngineRecordCount("OMM.fbs"); count != 10 {
		reopened.Close()
		t.Fatalf("engine count after windowed reopen = %d, want 10", count)
	}
	stream, err = reopened.QueryEpochRawStream("OMM.fbs", "", "nearest", float64(epoch3), 0)
	if err != nil {
		reopened.Close()
		t.Fatalf("QueryEpochRawStream after reopen failed: %v", err)
	}
	if nearest := decodeEpochFrames(t, stream); len(nearest) != 10 {
		reopened.Close()
		t.Fatalf("nearest after windowed reopen returned %d objects, want 10", len(nearest))
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("Close reopened failed: %v", err)
	}

	// The window is an ENGINE bound, not data loss: a wider window at the
	// next boot sees the full history again.
	wide, err := NewFlatSQLStore(basePath, validator, WithEngineHotWindow(1000))
	if err != nil {
		t.Fatalf("reopen with wide window failed: %v", err)
	}
	defer wide.Close()
	if count, _ := wide.EngineRecordCount("OMM.fbs"); count != 15 {
		t.Fatalf("engine count with wide window = %d, want 15", count)
	}
}

// TestEngineServesTBSStoredWithSourceTags is the LOCAL half of the cellular
// read surface (sdn-tbs-feed-sync-for-cache-lane): $TBS sites written by the
// cell-tower ingest flow's storage-ingest capability — StoreWithSourceTags,
// the same call the flow makes — must be readable through the engine record
// surface that storage.flatsql_query_stream -> QueryRawStream serves to the
// aggregate cache flow, on the SAME process that wrote them (no restart, no
// boot rebuild). Before the TBS record slice this read was "no such table".
func TestEngineServesTBSStoredWithSourceTags(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, basePath)

	tags := SourceTags{
		ProviderID:   "opencellid",
		SourceName:   "cell-tower-bulk",
		BatchID:      "opencellid@1",
		ContentKeyID: "public",
	}
	const siteCount = 5
	ids := make([]string, siteCount)
	for i := 0; i < siteCount; i++ {
		ids[i] = fmt.Sprintf("310-410-7-%d", 500+i)
		record := newTBSRecord(ids[i], "opencellid", 310, 51.5+0.01*float64(i), -0.12)
		if _, err := store.StoreWithSourceTags("TBS.fbs", record, "space-data-network-02", nil, tags); err != nil {
			t.Fatalf("StoreWithSourceTags $TBS %d: %v", i, err)
		}
	}

	assertEngineTBS := func(t *testing.T, s *FlatSQLStore, want int) {
		t.Helper()
		stream, err := s.QueryRawStream("SELECT _data FROM TBS ORDER BY _rowid DESC LIMIT ?", 100)
		if err != nil {
			t.Fatalf("engine $TBS read: %v", err)
		}
		frames, err := flatsqlrt.DecodeSizePrefixedStream(stream.Bytes)
		if err != nil {
			t.Fatalf("decode $TBS frames: %v", err)
		}
		if len(frames) != want {
			t.Fatalf("engine $TBS frames = %d, want %d", len(frames), want)
		}
		for i, frame := range frames {
			if !TBS.TBSBufferHasIdentifier(frame) {
				t.Fatalf("frame %d is not a $TBS buffer", i)
			}
			if site := TBS.GetRootAsTBS(frame, 0); site.SOURCESLength() != 1 || site.CONSENSUS(nil) == nil {
				t.Fatalf("frame %d lost its required SOURCES/CONSENSUS", i)
			}
		}
	}

	assertEngineTBS(t, store, siteCount)

	// The per-source shadow partition is TBS@<source-name>, exactly like
	// OMM@<source-name>: the cache lane can scope a read to one provider feed.
	scoped, err := store.engineDB.Query(`SELECT count(*) FROM "TBS@cell-tower-bulk"`)
	if err != nil {
		t.Fatalf("per-source $TBS shadow table read: %v", err)
	}
	if got := engineCellInt(t, scoped.Rows[0][0]); got != siteCount {
		t.Fatalf("TBS@cell-tower-bulk count = %d, want %d", got, siteCount)
	}

	// $OMM and $TBS partition independently — a routed standard never lands
	// in another standard's table.
	ommCount, err := store.EngineRecordCount("OMM.fbs")
	if err != nil {
		t.Fatalf("EngineRecordCount(OMM.fbs): %v", err)
	}
	if ommCount != 0 {
		t.Fatalf("OMM engine count = %d, want 0 (only $TBS was stored)", ommCount)
	}

	// Boot rebuild: a restarted node must come back serving the same sites
	// from the durable substrate, or the cache lane answers empty after every
	// upgrade.
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened := newEngineRecordsStore(t, basePath)
	defer reopened.Close()
	assertEngineTBS(t, reopened, siteCount)
}

// TestEngineTBSHotWindowEvictsOldestSites pins the hot-window bound on the
// TBS slice: eviction tombstones the OLDEST resident sites in their per-source
// shadow table and never touches the durable substrate.
func TestEngineTBSHotWindowEvictsOldestSites(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator, WithEngineHotWindow(3))
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	defer store.Close()

	tags := SourceTags{ProviderID: "opencellid", SourceName: "cell-tower-bulk", BatchID: "opencellid@1"}
	for i := 0; i < 6; i++ {
		record := newTBSRecord(fmt.Sprintf("310-410-9-%d", i), "opencellid", 310, 10+float64(i), 20)
		if _, err := store.StoreWithSourceTags("TBS.fbs", record, "space-data-network-02", nil, tags); err != nil {
			t.Fatalf("StoreWithSourceTags $TBS %d: %v", i, err)
		}
	}

	count, err := store.EngineRecordCount("TBS.fbs")
	if err != nil {
		t.Fatalf("EngineRecordCount(TBS.fbs): %v", err)
	}
	if count != 3 {
		t.Fatalf("resident $TBS after a window of 3 = %d, want 3", count)
	}

	// The window keeps the NEWEST sites.
	stream, err := store.QueryRawStream("SELECT _data FROM TBS ORDER BY _rowid ASC")
	if err != nil {
		t.Fatalf("engine $TBS read: %v", err)
	}
	frames, err := flatsqlrt.DecodeSizePrefixedStream(stream.Bytes)
	if err != nil {
		t.Fatalf("decode $TBS frames: %v", err)
	}
	if len(frames) != 3 {
		t.Fatalf("engine $TBS frames = %d, want 3", len(frames))
	}
	for i, frame := range frames {
		want := fmt.Sprintf("310-410-9-%d", i+3)
		if got := string(TBS.GetRootAsTBS(frame, 0).ID()); got != want {
			t.Fatalf("resident site %d = %q, want %q (oldest evicted first)", i, got, want)
		}
	}

	// The durable substrate keeps every site: eviction is a cache bound.
	records, err := store.QueryIndexedRecords(IndexedRecordQuery{
		SchemaName: "TBS.fbs",
		ProviderID: tags.ProviderID,
		SourceName: tags.SourceName,
		Limit:      100,
	})
	if err != nil {
		t.Fatalf("QueryIndexedRecords: %v", err)
	}
	if len(records) != 6 {
		t.Fatalf("durable $TBS records = %d, want 6 (eviction must not delete)", len(records))
	}
}

// buildEngineIRM writes the smallest LEGAL $IRM resume mark, UNPREFIXED (the
// Store API's input shape). A FlatBuffers builder refuses to finish a table
// that is missing a required field, so this constructor is itself a check that
// the vendored binding and the embedded IRM.fbs agree.
func buildEngineIRM(t *testing.T, jobID string, sequence uint64, nextOffset uint64) []byte {
	t.Helper()
	b := flatbuffers.NewBuilder(512)
	sourceURL := b.CreateString("https://example.invalid/bulk/catalog.csv")
	IRM.IRMSourceStart(b)
	IRM.IRMSourceAddSOURCE_URL(b, sourceURL)
	source := IRM.IRMSourceEnd(b)

	job := b.CreateString(jobID)
	provider := b.CreateString("mls")
	updated := b.CreateString("2026-08-25T00:00:00.000Z")
	IRM.IRMStart(b)
	IRM.IRMAddJOB_ID(b, job)
	IRM.IRMAddPROVIDER_ID(b, provider)
	IRM.IRMAddSOURCE(b, source)
	IRM.IRMAddSEQUENCE(b, sequence)
	IRM.IRMAddNEXT_OFFSET(b, nextOffset)
	IRM.IRMAddUPDATED_AT(b, updated)
	IRM.FinishSizePrefixedIRMBuffer(b, IRM.IRMEnd(b))
	return b.FinishedBytes()[4:]
}

// TestIRMWrittenThroughStorageReadsBackFromTheEngine is the owner directive's
// acceptance test: $IRM is readable through the sandboxed query surface with
// the SAME SQL shape as every other standard, because it is routed for the
// same reason every other standard is — it is one of the standards this node
// embeds. Nothing in the store names IRM.
func TestIRMWrittenThroughStorageReadsBackFromTheEngine(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, basePath)

	tags := SourceTags{ProviderID: "mls", SourceName: "cell-tower-bulk", BatchID: "mls@1"}
	const marks = 3
	var last []byte
	for i := 1; i <= marks; i++ {
		last = buildEngineIRM(t, "cellular-worldwide", uint64(i), uint64(i)*4096)
		if _, err := store.StoreWithSourceTags("IRM.fbs", last, "space-data-network-02", nil, tags); err != nil {
			t.Fatalf("store $IRM %d: %v", i, err)
		}
	}

	// THE MODULE CONTRACT, VERBATIM: this is the SQL the cellular ingest flow
	// runs through hostcap/flatsql-query to find its durable resume mark.
	const markSQL = `SELECT _data FROM IRM ORDER BY _rowid DESC LIMIT ?`
	caps := flatsqlrt.SandboxCaps{MaxRows: 32, MaxBytes: 1 << 20, Timeout: 30 * time.Second}

	assertMarks := func(t *testing.T, s *FlatSQLStore, want int) [][]byte {
		t.Helper()
		stream, err := s.QuerySandboxedStream(markSQL, caps, 32)
		if err != nil {
			t.Fatalf("sandboxed $IRM read: %v", err)
		}
		frames, err := flatsqlrt.DecodeSizePrefixedStream(stream.Bytes)
		if err != nil {
			t.Fatalf("decode $IRM frames: %v", err)
		}
		if len(frames) != want {
			t.Fatalf("$IRM frames = %d, want %d", len(frames), want)
		}
		for i, frame := range frames {
			if !IRM.IRMBufferHasIdentifier(frame) {
				t.Fatalf("frame %d does not carry the $IRM identifier", i)
			}
		}
		return frames
	}

	frames := assertMarks(t, store, marks)
	// ORDER BY _rowid DESC: the newest mark first, byte-identical to what was
	// stored. A resume mark that comes back re-encoded is a resume mark a
	// consumer cannot verify.
	if string(frames[0]) != string(last) {
		t.Fatal("the newest $IRM frame is not byte-identical to the stored record")
	}
	if got := IRM.GetRootAsIRM(frames[0], 0).SEQUENCE(); got != marks {
		t.Fatalf("newest $IRM SEQUENCE = %d, want %d", got, marks)
	}

	// The per-source shadow partition exists for a generically routed
	// standard exactly as it does for $OMM and $TBS.
	scoped, err := store.engineDB.Query(`SELECT count(*) FROM "IRM@cell-tower-bulk"`)
	if err != nil {
		t.Fatalf("per-source $IRM shadow table read: %v", err)
	}
	if got := engineCellInt(t, scoped.Rows[0][0]); got != marks {
		t.Fatalf("IRM@cell-tower-bulk count = %d, want %d", got, marks)
	}

	// A restarted node must come back serving the same marks, or every crash
	// re-reads the whole bulk source from offset zero.
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened := newEngineRecordsStore(t, basePath)
	defer reopened.Close()
	assertMarks(t, reopened, marks)
}

// TestEveryRoutedStandardAnswersEmptyOnAFreshStore is the OTHER half of the
// contract, and the half that actually broke production: a module reading a
// standard this node has never stored must get ZERO ROWS, not "no such table".
// An error there is indistinguishable from a real failure, which is exactly
// how the cellular ingest flow turned a first run into a fault envelope.
//
// It is simultaneously the proof that every routed table materialized.
func TestEveryRoutedStandardAnswersEmptyOnAFreshStore(t *testing.T) {
	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	// EXACTNESS GUARD. This test loops over the store's own routed set, so
	// without a guard it would pass VACUOUSLY if that set ever collapsed back
	// toward {OMM, TBS}. A FLOOR ("at least 100") would still admit a silent
	// loss of a hundred standards: a fresh store excludes nothing by
	// construction, so its routed set is the WHOLE catalog or the directive
	// is broken.
	routed := store.engineRoutedSchemaNames()
	if len(routed) != len(engineRoutedSchemas) {
		t.Fatalf("fresh store routes %d of %d schemas — a fresh store excludes nothing",
			len(routed), len(engineRoutedSchemas))
	}

	caps := flatsqlrt.SandboxCaps{MaxRows: 4, MaxBytes: 1 << 16, Timeout: 30 * time.Second}
	for _, schemaName := range routed {
		binding := engineRoutedSchemas[schemaName]
		stream, err := store.QuerySandboxedStream(
			`SELECT _data FROM "`+binding.Table+`" ORDER BY _rowid DESC LIMIT ?`, caps, 1)
		if err != nil {
			t.Fatalf("%s: %v", binding.Table, err)
		}
		if len(stream.Bytes) != 0 {
			t.Fatalf("%s returned %d bytes on a fresh store, want an empty stream", binding.Table, len(stream.Bytes))
		}
	}
}

// TestRoutedStandardRehydratesRecordsStoredBeforeRouting proves the
// compatibility claim: engine routing is a CACHE mirror, so records written
// while a standard was NOT routed re-enter its table through the ordinary boot
// rebuild. There is nothing to migrate because nothing ever moved — the
// durable substrate is the same stream file and the same (producer, standard)
// control rows either way.
func TestRoutedStandardRehydratesRecordsStoredBeforeRouting(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, basePath)

	// Force this store back to the pre-flip routed set while the records are
	// written: they land in the durable substrate and NOT in the engine.
	for schemaName := range engineRoutedSchemas {
		if _, decorated := engineDecoratedSchemas[schemaName]; !decorated {
			store.engineExcluded[schemaName] = true
		}
	}
	tags := SourceTags{ProviderID: "mls", SourceName: "cell-tower-bulk", BatchID: "mls@1"}
	const marks = 4
	for i := 1; i <= marks; i++ {
		record := buildEngineIRM(t, "pre-flip", uint64(i), uint64(i)*1024)
		if _, err := store.StoreWithSourceTags("IRM.fbs", record, "space-data-network-02", nil, tags); err != nil {
			t.Fatalf("store $IRM %d: %v", i, err)
		}
	}
	if count, err := store.engineDB.Query(`SELECT count(*) FROM "IRM"`); err != nil {
		t.Fatalf("unrouted $IRM count: %v", err)
	} else if got := engineCellInt(t, count.Rows[0][0]); got != 0 {
		t.Fatalf("unrouted $IRM landed %d records in the engine, want 0", got)
	}
	// The durable control row is written either way — that is the whole point.
	if _, err := store.db.Query(`SELECT cid FROM "sds_p_space_data_network_02__IRM"`); err != nil {
		t.Fatalf("unrouted $IRM has no (producer, standard) rows: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened := newEngineRecordsStore(t, basePath)
	defer reopened.Close()
	stream, err := reopened.QueryRawStream(`SELECT _data FROM IRM ORDER BY _rowid DESC LIMIT ?`, 32)
	if err != nil {
		t.Fatalf("rehydrated $IRM read: %v", err)
	}
	frames, err := flatsqlrt.DecodeSizePrefixedStream(stream.Bytes)
	if err != nil {
		t.Fatalf("decode rehydrated $IRM frames: %v", err)
	}
	if len(frames) != marks {
		t.Fatalf("rehydrated $IRM frames = %d, want %d", len(frames), marks)
	}
}

// TestPlainControlTableCollisionExcludesTheStandardWithoutDroppingIt is the
// fail-closed data-destruction guard. createUnifiedView issues
// `DROP TABLE IF EXISTS "<name>"` before it creates the view, so a standard
// whose code is already a plain control table must never be routed in that
// store — the rows are reachable (recordReadSourceFiltered unions the bare
// canonical table) and dropping them would be silent, permanent loss.
func TestPlainControlTableCollisionExcludesTheStandardWithoutDroppingIt(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, basePath)

	// The store must come back WARM, or the control database is discarded on
	// the next boot and the collision cannot exist to be guarded against. One
	// stored record plus a checkpoint is what makes the resume mark usable.
	if _, err := store.Store("OMM.fbs", buildEngineOMM(t, 25544, "ISS", 1700000000), "peer", nil); err != nil {
		t.Fatalf("store $OMM: %v", err)
	}

	// A pre-WS7.3d store shape: the standard's canonical name is a PLAIN
	// control table holding real rows. Building it means removing the unified
	// view this build creates for the same name — which is exactly the
	// collision, seen from the other side.
	if _, err := store.db.Exec(`DROP VIEW IF EXISTS CDM`); err != nil {
		t.Fatalf("drop routed view: %v", err)
	}
	if err := store.createSchemaMetadataTable("CDM"); err != nil {
		t.Fatalf("create legacy canonical table: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO CDM (cid, peer_id, timestamp, stream_path, stream_offset, record_length) VALUES ('bafyLegacyCDM', 'peer', 1, 'flatsql-streams/CDM.bin', 0, 8)`); err != nil {
		t.Fatalf("seed legacy canonical row: %v", err)
	}
	if err := store.CheckpointRecordCatalog(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened := newEngineRecordsStore(t, basePath)
	defer reopened.Close()

	if !reopened.engineExcluded["CDM.fbs"] {
		t.Fatal("a standard whose code is already a plain control table must be excluded from routing")
	}
	if reopened.engineRoutesSchema("CDM.fbs") {
		t.Fatal("the excluded standard is still routed")
	}
	if reopened.engineOwnsTableName("CDM") {
		t.Fatal("the excluded standard still claims to own its canonical table name")
	}
	// THE ROWS SURVIVED. This is the assertion the guard exists for.
	rows, err := reopened.db.Query(`SELECT cid FROM CDM`)
	if err != nil {
		t.Fatalf("legacy canonical table was destroyed: %v", err)
	}
	defer rows.Close()
	found := ""
	for rows.Next() {
		if err := rows.Scan(&found); err != nil {
			t.Fatalf("scan legacy row: %v", err)
		}
	}
	if found != "bafyLegacyCDM" {
		t.Fatalf("legacy canonical row = %q, want bafyLegacyCDM", found)
	}
	// Everything else still routes: the exclusion is per standard, not a
	// blanket retreat to the old two-entry map.
	if !reopened.engineRoutesSchema("IRM.fbs") || !reopened.engineRoutesSchema("OMM.fbs") {
		t.Fatal("one collision must not un-route the rest of the catalog")
	}
}

// TestGenericHotWindowBoundsUndecoratedStandards pins the memory bound: the
// full per-schema window belongs to the two standards this host decorates and
// reads at provider scale; every other routed standard gets the smaller
// generic budget, because engine_hot_window x 226 standards is not a bound on
// anything.
func TestGenericHotWindowBoundsUndecoratedStandards(t *testing.T) {
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	store, err := NewFlatSQLStore(filepath.Join(t.TempDir(), "store"), validator,
		WithEngineHotWindow(400_000), WithEngineGenericHotWindow(2))
	if err != nil {
		t.Fatalf("NewFlatSQLStore: %v", err)
	}
	defer store.Close()

	if got := store.engineWindowFor("OMM.fbs"); got != 400_000 {
		t.Errorf("OMM window = %d, want the decorated window 400000", got)
	}
	if got := store.engineWindowFor("TBS.fbs"); got != 400_000 {
		t.Errorf("TBS window = %d, want the decorated window 400000", got)
	}
	if got := store.engineWindowFor("IRM.fbs"); got != 2 {
		t.Errorf("IRM window = %d, want the generic window 2", got)
	}

	tags := SourceTags{ProviderID: "mls", SourceName: "cell-tower-bulk", BatchID: "mls@1"}
	for i := 1; i <= 5; i++ {
		record := buildEngineIRM(t, "window", uint64(i), uint64(i)*512)
		if _, err := store.StoreWithSourceTags("IRM.fbs", record, "space-data-network-02", nil, tags); err != nil {
			t.Fatalf("store $IRM %d: %v", i, err)
		}
	}
	count, err := store.EngineRecordCount("IRM.fbs")
	if err != nil {
		t.Fatalf("EngineRecordCount(IRM.fbs): %v", err)
	}
	if count != 2 {
		t.Fatalf("resident $IRM = %d, want the generic window 2", count)
	}
	// Eviction is a CACHE bound: the durable rows are all still there.
	var durable int
	if err := store.db.QueryRow(`SELECT count(*) FROM sdn_record_index WHERE schema_name = 'IRM.fbs'`).Scan(&durable); err != nil {
		t.Fatalf("durable $IRM count: %v", err)
	}
	if durable != 5 {
		t.Fatalf("durable $IRM records = %d, want 5 — hot-window eviction must never touch the substrate", durable)
	}
}

// TestPublicQuerySurfaceCoversEveryRoutedStandard pins the read surface: the
// sandboxed public query may read every routed standard's view and per-source
// shadow tables, and asking WHAT is queryable must not cost a full scan of
// every partition.
func TestPublicQuerySurfaceCoversEveryRoutedStandard(t *testing.T) {
	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	tags := SourceTags{ProviderID: "mls", SourceName: "cell-tower-bulk", BatchID: "mls@1"}
	if _, err := store.StoreWithSourceTags("IRM.fbs", buildEngineIRM(t, "surface", 1, 512), "space-data-network-02", nil, tags); err != nil {
		t.Fatalf("store $IRM: %v", err)
	}

	surface, err := store.PublicQuerySurface()
	if err != nil {
		t.Fatalf("PublicQuerySurface: %v", err)
	}
	byName := make(map[string]QuerySurfaceTable, len(surface))
	for _, entry := range surface {
		byName[entry.Name] = entry
	}
	for _, schemaName := range store.engineRoutedSchemaNames() {
		if _, ok := byName[engineRoutedSchemas[schemaName].Table]; !ok {
			t.Fatalf("%s is routed but absent from the public query surface", schemaName)
		}
	}
	irm, ok := byName["IRM"]
	if !ok {
		t.Fatal("IRM is missing from the public query surface")
	}
	if irm.Records != 1 {
		t.Fatalf("IRM surface records = %d, want 1", irm.Records)
	}
	if len(irm.Columns) == 0 {
		t.Fatal("IRM surface reports no columns")
	}
	if _, ok := byName["IRM@cell-tower-bulk"]; !ok {
		t.Fatal("the per-source shadow partition is missing from the public query surface")
	}
	// A standard with nothing resident is still queryable and reports zero
	// WITHOUT a count(*) over its partitions.
	if empty, ok := byName["CDM"]; !ok || empty.Records != 0 {
		t.Fatalf("CDM surface = %+v, want a present relation with 0 records", empty)
	}
	// SIZE BOUND. This is a public response body: an empty standard contributes
	// its base relation and NOTHING else, so 226 standards x sources empty
	// partitions (each repeating the full column list) never ship.
	if _, ok := byName["CDM@cell-tower-bulk"]; ok {
		t.Fatal("an empty standard must not list per-source partitions in the public surface")
	}
	bases, shadows := 0, 0
	for _, entry := range surface {
		if entry.Source == "" {
			bases++
			continue
		}
		shadows++
		if !strings.HasPrefix(entry.Name, "IRM@") {
			t.Fatalf("%s is a per-source partition of a standard with nothing resident", entry.Name)
		}
	}
	if bases != len(store.engineRoutedSchemaNames()) {
		t.Fatalf("public query surface lists %d base relations, want one per routed standard (%d)", bases, len(store.engineRoutedSchemaNames()))
	}
	if shadows == 0 {
		t.Fatal("the populated standard's per-source partitions are missing from the public surface")
	}
}

// TestBootRebuildsUnifiedViewsAtMostOnce is the BOOT COST gate, expressed as
// the property that actually bounds the cost rather than as a stopwatch.
//
// CreateUnifiedViews is all-or-nothing across the schema: DROP TABLE + DROP
// VIEW + CREATE VIEW for EVERY routed standard. Rebuilding once per source —
// which is what ensureEngineSource does — was free while two standards were
// routed and is O(routed tables x sources) write DDL now that every embedded
// standard is. A warm boot must therefore rebuild ZERO times (the persisted
// views already union exactly the registered sources) and a cold one exactly
// once, independent of how many providers the node has ingested from.
func TestBootRebuildsUnifiedViewsAtMostOnce(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "store")
	store := newEngineRecordsStore(t, basePath)

	if store.engineViewRebuilds > 1 {
		t.Fatalf("first open rebuilt the unified views %d times, want at most 1", store.engineViewRebuilds)
	}
	for i, source := range []string{"catalogfixture-gp", "celestrak-satcat", "satnogs-db"} {
		tags := SourceTags{ProviderID: "p", SourceName: source, BatchID: source + "@1"}
		if _, err := store.StoreWithSourceTags("OMM.fbs",
			buildEngineOMM(t, uint32(40000+i), fmt.Sprintf("SAT-%d", i), 1700000000+int64(i)), "peer", nil, tags); err != nil {
			t.Fatalf("store $OMM for %s: %v", source, err)
		}
	}
	if err := store.CheckpointRecordCatalog(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened := newEngineRecordsStore(t, basePath)
	defer reopened.Close()
	if reopened.engineViewRebuilds != 0 {
		t.Fatalf("warm boot rebuilt the unified views %d times, want 0 — the persisted views already cover the registered sources",
			reopened.engineViewRebuilds)
	}
	// SKIPPING THE REBUILD MUST NOT COST THE SHADOW MODULES. A persisted vtab
	// whose module nothing registered fails with `no such module` on the first
	// query to touch it, which is the trap the whole ordering exists to close.
	for _, relation := range []string{`"OMM"`, `"OMM@catalogfixture-gp"`, `"OMM@satnogs-db"`, `"IRM"`} {
		if _, err := reopened.engineDB.Query(`SELECT count(*) FROM ` + relation); err != nil {
			t.Fatalf("warm-boot query of %s: %v", relation, err)
		}
	}
	if count, err := reopened.EngineRecordCount("OMM.fbs"); err != nil || count != 3 {
		t.Fatalf("warm-boot $OMM count = %d (err %v), want 3", count, err)
	}
}

// TestEngineIngestRoutesEveryFileIDToItsOwnTable is the drift test across all
// 226 bindings: a buffer bearing a standard's four-byte identifier lands in
// that standard's table AND IN NO OTHER. With two routed standards a
// file-id/table mix-up was obvious; across the whole catalog it would be
// invisible until a consumer read one standard's rows out of another's table.
func TestEngineIngestRoutesEveryFileIDToItsOwnTable(t *testing.T) {
	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	routed := store.engineRoutedSchemaNames()
	for _, schemaName := range routed {
		binding := engineRoutedSchemas[schemaName]
		b := flatbuffers.NewBuilder(64)
		b.StartObject(0)
		root := b.EndObject()
		b.FinishWithFileIdentifier(root, []byte(binding.FileID))
		if _, err := store.engineDB.IngestOneWithSource(b.FinishedBytes(), engineDefaultSource); err != nil {
			t.Fatalf("ingest %s: %v", binding.FileID, err)
		}
	}

	for _, schemaName := range routed {
		binding := engineRoutedSchemas[schemaName]
		res, err := store.engineDB.Query(`SELECT count(*) FROM "` + binding.Table + `"`)
		if err != nil {
			t.Fatalf("count %s: %v", binding.Table, err)
		}
		if got := engineCellInt(t, res.Rows[0][0]); got != 1 {
			t.Fatalf("%s holds %d records, want exactly 1 (its own)", binding.Table, got)
		}
	}
}

// TestLeftoverUnifiedViewsAreInvisibleToTheLegacyTablePaths is the ROLLBACK
// safety proof. Rolling back to a binary that routes only $OMM and $TBS leaves
// 224 unified views behind in the control database. That is harmless only
// because every legacy/canonical-table path filters on type='table': a
// leftover view must never be mistaken for a per-standard metadata table, or
// the older binary would try to read record rows out of a view over vtabs it
// does not know how to register.
func TestLeftoverUnifiedViewsAreInvisibleToTheLegacyTablePaths(t *testing.T) {
	store := newEngineRecordsStore(t, filepath.Join(t.TempDir(), "store"))
	defer store.Close()

	tags := SourceTags{ProviderID: "mls", SourceName: "cell-tower-bulk", BatchID: "mls@1"}
	if _, err := store.StoreWithSourceTags("IRM.fbs", buildEngineIRM(t, "rollback", 1, 128), "space-data-network-02", nil, tags); err != nil {
		t.Fatalf("store $IRM: %v", err)
	}
	// The view exists...
	if _, err := store.engineDB.Query(`SELECT count(*) FROM "IRM"`); err != nil {
		t.Fatalf("routed IRM view is missing: %v", err)
	}
	// ...and is invisible to the plain-table paths.
	exists, err := store.tableExists("IRM")
	if err != nil {
		t.Fatalf("tableExists(IRM): %v", err)
	}
	if exists {
		t.Fatal("the IRM unified view is visible as a plain table: a rolled-back binary would read records out of it")
	}
	source, err := store.recordReadSourceFiltered("IRM.fbs", "")
	if err != nil {
		t.Fatalf("recordReadSource(IRM.fbs): %v", err)
	}
	if source == "IRM" || strings.Contains(source, " IRM ") {
		t.Fatalf("record read source names the unified view: %q", source)
	}
	if !strings.Contains(source, "__IRM") {
		t.Fatalf("record read source %q does not name the (producer, standard) table that actually holds the rows", source)
	}
}
