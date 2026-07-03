package flatsqlrt

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// partition_test.go proves the per-(provider, standard) storage layout the
// rewrite is built on (loop A.3): flatsql_register_source shadow tables
// (`OMM@<source>`), unified UNION ALL views with a `_source` column, the
// per-object nearest-epoch window query executed inside the engine over a
// bounded per-provider table, and plain control-table DDL/DML through
// flatsql_query (needed for Phase B index/provenance tables).

// buildOMM produces one size-prefixed $OMM FlatBuffer via the SDS builder
// (the same generated bindings production ingest uses). The numeric
// USER_DEFINED_EPOCH_TIMESTAMP is populated alongside the EPOCH string.
func buildOMM(norad uint32, name, epoch string) []byte {
	ts, err := time.Parse("2006-01-02T15:04:05Z", epoch)
	if err != nil {
		panic(err)
	}
	return sds.NewOMMBuilder().
		WithNoradCatID(norad).
		WithObjectName(name).
		WithObjectID(fmt.Sprintf("2024-%03dA", norad%1000)).
		WithEpoch(epoch).
		WithEpochTimestamp(float64(ts.Unix())).
		WithMeanMotion(15.5).
		WithEccentricity(0.0001).
		WithInclination(53.0).
		Build()
}

func concatStreams(bufs ...[]byte) []byte {
	var out []byte
	for _, b := range bufs {
		out = append(out, b...)
	}
	return out
}

// nearestEpochRawSQL is the per-object nearest-epoch selection, executed
// entirely inside FlatSQL over a bounded per-provider table. It matches the
// profile sdn-js builds in epoch-query-sql.ts (strftime over the EPOCH
// string), keeping the SQL isomorphic between hosts.
const nearestEpochRawSQL = `
SELECT _data FROM (
  SELECT _data, ROW_NUMBER() OVER (
    PARTITION BY NORAD_CAT_ID
    ORDER BY ABS(strftime('%s', EPOCH) - strftime('%s', ?))
  ) AS rn
  FROM "OMM@celestrak-gp"
) WHERE rn = 1`

func TestProviderPartitioningAndNearestEpoch(t *testing.T) {
	rt := newTestRuntime(t)
	db := newOMMDatabase(t, rt, "omm-partition")

	if err := db.RegisterSource("celestrak-gp"); err != nil {
		t.Fatalf("RegisterSource celestrak-gp: %v", err)
	}
	if err := db.RegisterSource("provider-two"); err != nil {
		t.Fatalf("RegisterSource provider-two: %v", err)
	}

	// celestrak-gp: 3 objects x 2 epochs.
	celestrak := concatStreams(
		buildOMM(1001, "SAT-A", "2026-05-10T00:00:00Z"),
		buildOMM(1001, "SAT-A", "2026-05-12T00:00:00Z"),
		buildOMM(1002, "SAT-B", "2026-05-10T00:00:00Z"),
		buildOMM(1002, "SAT-B", "2026-05-12T00:00:00Z"),
		buildOMM(1003, "SAT-C", "2026-05-10T00:00:00Z"),
		buildOMM(1003, "SAT-C", "2026-05-12T00:00:00Z"),
	)
	if _, err := db.IngestWithSource(celestrak, "celestrak-gp"); err != nil {
		t.Fatalf("IngestWithSource celestrak-gp: %v", err)
	}
	// provider-two: 1 object, distinct catalog number.
	if _, err := db.IngestOneWithSource(buildOMM(2001, "OTHER-SAT", "2026-05-11T00:00:00Z")[4:], "provider-two"); err != nil {
		t.Fatalf("IngestOneWithSource provider-two: %v", err)
	}

	// Records landed in per-source shadow tables.
	for _, tc := range []struct {
		table string
		want  int64
	}{
		{`OMM@celestrak-gp`, 6},
		{`OMM@provider-two`, 1},
	} {
		res, err := db.Query(fmt.Sprintf(`SELECT COUNT(*) FROM "%s"`, tc.table))
		if err != nil {
			t.Fatalf("count %s: %v", tc.table, err)
		}
		if got := res.Rows[0][0].(int64); got != tc.want {
			t.Fatalf("%s has %d rows, want %d", tc.table, got, tc.want)
		}
	}

	// Unified view spans providers and exposes _source (the engine reports
	// the full shadow-table name, `Table@source`).
	if err := db.CreateUnifiedViews(); err != nil {
		t.Fatalf("CreateUnifiedViews: %v", err)
	}
	res, err := db.Query(`SELECT _source, COUNT(*) FROM OMM GROUP BY _source ORDER BY _source`)
	if err != nil {
		t.Fatalf("unified view query: %v", err)
	}
	if len(res.Rows) != 2 ||
		res.Rows[0][0] != "OMM@celestrak-gp" || res.Rows[0][1].(int64) != 6 ||
		res.Rows[1][0] != "OMM@provider-two" || res.Rows[1][1].(int64) != 1 {
		t.Fatalf("unified view rows: %#v", res.Rows)
	}

	// Nearest-epoch, per object, inside the engine, streamed out aligned.
	// Target 2026-05-11T12:00:00Z is closer to the 05-12 epoch for all
	// three objects.
	stream, err := db.QueryRawFlatBufferStream(nearestEpochRawSQL, "2026-05-11T12:00:00Z")
	if err != nil {
		t.Fatalf("nearest-epoch raw stream: %v", err)
	}
	frames, err := DecodeSizePrefixedStream(stream.Bytes)
	if err != nil {
		t.Fatalf("decode stream: %v", err)
	}
	if len(frames) != 3 {
		t.Fatalf("nearest-epoch returned %d frames, want 3 (one per object)", len(frames))
	}
	// Row form of the same profile, as a registered template.
	if err := db.RegisterQueryTemplate("omm.epoch_nearest_rows", `
SELECT NORAD_CAT_ID, EPOCH FROM (
  SELECT NORAD_CAT_ID, EPOCH, ROW_NUMBER() OVER (
    PARTITION BY NORAD_CAT_ID
    ORDER BY ABS(strftime('%s', EPOCH) - strftime('%s', ?))
  ) AS rn
  FROM "OMM@celestrak-gp"
) WHERE rn = 1 ORDER BY NORAD_CAT_ID`, true); err != nil {
		t.Fatalf("register nearest-epoch template: %v", err)
	}
	rows, err := db.QueryTemplate("omm.epoch_nearest_rows", "2026-05-11T12:00:00Z")
	if err != nil {
		t.Fatalf("QueryTemplate: %v", err)
	}
	if len(rows.Rows) != 3 {
		t.Fatalf("template returned %d rows, want 3", len(rows.Rows))
	}
	for i, wantNorad := range []int64{1001, 1002, 1003} {
		if rows.Rows[i][0].(int64) != wantNorad || rows.Rows[i][1] != "2026-05-12T00:00:00Z" {
			t.Fatalf("row %d = %#v, want norad %d epoch 2026-05-12T00:00:00Z", i, rows.Rows[i], wantNorad)
		}
	}
}

func TestControlTableDDLThroughEngine(t *testing.T) {
	rt := newTestRuntime(t)
	db := newOMMDatabase(t, rt, "omm-ddl")

	// Plain SQL tables (Phase B will hold index/provenance/cursor state in
	// these) must work through flatsql_query alongside virtual tables.
	if _, err := db.Query(`CREATE TABLE sdn_test_metadata (key TEXT PRIMARY KEY, value TEXT, seq INTEGER)`); err != nil {
		t.Fatalf("DDL: %v", err)
	}
	if _, err := db.Query(`INSERT INTO sdn_test_metadata (key, value, seq) VALUES (?, ?, ?)`, "cursor", "abc", int64(42)); err != nil {
		t.Fatalf("INSERT with params: %v", err)
	}
	if _, err := db.Query(`INSERT INTO sdn_test_metadata (key, value, seq) VALUES ('head', 'def', 43)`); err != nil {
		t.Fatalf("INSERT literal: %v", err)
	}
	res, err := db.Query(`SELECT key, value, seq FROM sdn_test_metadata ORDER BY seq`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if len(res.Rows) != 2 || res.Rows[0][0] != "cursor" || res.Rows[0][2].(int64) != 42 {
		t.Fatalf("control table rows: %#v", res.Rows)
	}
	if _, err := db.Query(`UPDATE sdn_test_metadata SET seq = seq + 1 WHERE key = ?`, "head"); err != nil {
		t.Fatalf("UPDATE: %v", err)
	}
	res, err = db.Query(`SELECT seq FROM sdn_test_metadata WHERE key = 'head'`)
	if err != nil {
		t.Fatalf("SELECT after update: %v", err)
	}
	if res.Rows[0][0].(int64) != 44 {
		t.Fatalf("seq = %#v, want 44", res.Rows[0][0])
	}
}

// TestCatalogScaleMeasurements reports the A.3 numbers: ingest throughput for
// a full-catalog-sized snapshot and nearest-epoch query latency over a
// multi-epoch history, all inside the WASM engine. It takes minutes under
// the WasmEdge interpreter (see loop doc A.3 findings), so it only runs when
// explicitly requested:
//
//	FLATSQLRT_SCALE_MEASURE=1 go test ./internal/flatsqlrt/ -run CatalogScale -v
func TestCatalogScaleMeasurements(t *testing.T) {
	if os.Getenv("FLATSQLRT_SCALE_MEASURE") == "" {
		t.Skip("set FLATSQLRT_SCALE_MEASURE=1 to run catalog-scale measurements")
	}
	const (
		objects       = 29000
		epochsPerObj  = 5
		baseEpochUnix = 1778400000 // 2026-05-10T00:00:00Z
	)

	rt := newTestRuntime(t)
	db := newOMMDatabase(t, rt, "omm-scale")
	if err := db.RegisterSource("celestrak-gp"); err != nil {
		t.Fatalf("RegisterSource: %v", err)
	}

	// Snapshot: one epoch per object (~29K records).
	snapshot := make([]byte, 0, objects*300)
	for i := 0; i < objects; i++ {
		norad := uint32(10000 + i)
		epoch := time.Unix(baseEpochUnix+int64(i%86400), 0).UTC().Format("2006-01-02T15:04:05Z")
		snapshot = append(snapshot, buildOMM(norad, fmt.Sprintf("SAT-%d", norad), epoch)...)
	}
	start := time.Now()
	if _, err := db.IngestWithSource(snapshot, "celestrak-gp"); err != nil {
		t.Fatalf("snapshot ingest: %v", err)
	}
	ingestDur := time.Since(start)
	mb := float64(len(snapshot)) / (1024 * 1024)
	t.Logf("MEASURE ingest snapshot: %d records, %.1f MB in %s (%.0f rec/s, %.1f MB/s)",
		objects, mb, ingestDur, float64(objects)/ingestDur.Seconds(), mb/ingestDur.Seconds())

	// History: 4 more epochs per object (total 145K rows in the provider table).
	for e := 1; e < epochsPerObj; e++ {
		hist := make([]byte, 0, objects*300)
		for i := 0; i < objects; i++ {
			norad := uint32(10000 + i)
			epoch := time.Unix(baseEpochUnix+int64(e*86400+i%86400), 0).UTC().Format("2006-01-02T15:04:05Z")
			hist = append(hist, buildOMM(norad, fmt.Sprintf("SAT-%d", norad), epoch)...)
		}
		if _, err := db.IngestWithSource(hist, "celestrak-gp"); err != nil {
			t.Fatalf("history ingest %d: %v", e, err)
		}
	}
	res, err := db.Query(`SELECT COUNT(*) FROM "OMM@celestrak-gp"`)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	total := res.Rows[0][0].(int64)
	if total != int64(objects*epochsPerObj) {
		t.Fatalf("table has %d rows, want %d", total, objects*epochsPerObj)
	}

	// Nearest-epoch over the full history, numeric epoch column: one frame
	// per object. This is the production query shape.
	const nearestEpochNumericSQL = `
SELECT _data FROM (
  SELECT _data, ROW_NUMBER() OVER (
    PARTITION BY NORAD_CAT_ID
    ORDER BY ABS(USER_DEFINED_EPOCH_TIMESTAMP - ?)
  ) AS rn
  FROM "OMM@celestrak-gp"
) WHERE rn = 1`
	targetUnix := float64(baseEpochUnix + 2*86400 + 43200)
	start = time.Now()
	stream, err := db.QueryRawFlatBufferStream(nearestEpochNumericSQL, targetUnix)
	if err != nil {
		t.Fatalf("nearest-epoch numeric query: %v", err)
	}
	queryDur := time.Since(start)
	frames, err := DecodeSizePrefixedStream(stream.Bytes)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(frames) != objects {
		t.Fatalf("nearest-epoch returned %d frames, want %d", len(frames), objects)
	}
	outMB := float64(len(stream.Bytes)) / (1024 * 1024)
	t.Logf("MEASURE nearest-epoch NUMERIC over %d rows -> %d aligned frames (%.1f MB) in %s",
		total, len(frames), outMB, queryDur)

	// strftime-over-EPOCH-string variant for comparison (what naive SQL
	// would do). Kept as a measurement, not an assertion.
	target := time.Unix(baseEpochUnix+2*86400+43200, 0).UTC().Format("2006-01-02T15:04:05Z")
	start = time.Now()
	streamStr, err := db.QueryRawFlatBufferStream(nearestEpochRawSQL, target)
	if err != nil {
		t.Fatalf("nearest-epoch strftime query: %v", err)
	}
	t.Logf("MEASURE nearest-epoch STRFTIME over %d rows -> %d bytes in %s",
		total, len(streamStr.Bytes), time.Since(start))

	// Bounded snapshot-shape query (latest-per-object over one epoch each,
	// the REST default when history is retained elsewhere).
	res, err = db.Query(`SELECT COUNT(*) FROM (
  SELECT NORAD_CAT_ID, ROW_NUMBER() OVER (
    PARTITION BY NORAD_CAT_ID ORDER BY USER_DEFINED_EPOCH_TIMESTAMP DESC
  ) AS rn FROM "OMM@celestrak-gp") WHERE rn = 1`)
	if err != nil {
		t.Fatalf("latest-per-object count: %v", err)
	}
	if res.Rows[0][0].(int64) != int64(objects) {
		t.Fatalf("latest-per-object = %#v, want %d", res.Rows[0][0], objects)
	}

	if stats, err := rt.MemoryStats(); err == nil {
		t.Logf("MEASURE wasm memory: %d pages (%.0f MB) of max %d pages",
			stats.Pages, float64(stats.Bytes)/(1024*1024), stats.MaxPages)
	}
}
