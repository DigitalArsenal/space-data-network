package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"

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
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := NewFlatSQLStore(basePath, validator)
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
	tags := SourceTags{ProviderID: "prov-a", SourceName: "celestrak-gp", BatchID: "batch-1"}
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
	stream, err = store.QueryEpochRawStream("OMM.fbs", "celestrak-gp", "nearest", target, 0)
	if err != nil {
		t.Fatalf("QueryEpochRawStream celestrak-gp failed: %v", err)
	}
	if cg := decodeEpochFrames(t, stream); len(cg) != 3 {
		t.Fatalf("celestrak-gp filter returned %d frames, want 3", len(cg))
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
	tags := SourceTags{ProviderID: "prov-a", SourceName: "celestrak-gp", BatchID: "batch-1"}
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

	tags := SourceTags{ProviderID: "prov-a", SourceName: "celestrak-gp", BatchID: "batch-scale"}
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
