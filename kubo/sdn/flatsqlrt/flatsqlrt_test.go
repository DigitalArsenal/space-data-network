package flatsqlrt

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// ommTestSchema mirrors the SDS OMM table shape used by the flatsql generic
// extractor test (flatsql/test/wasm-generic-extractor.test.ts), so the same
// fixture buffer parses identically in both hosts.
const ommTestSchema = `
  table OMM {
    CCSDS_OMM_VERS:double;
    CREATION_DATE:string;
    ORIGINATOR:string;
    OBJECT_NAME:string;
    OBJECT_ID:string;
    CENTER_NAME:string;
    REFERENCE_FRAME:RFM;
    REFERENCE_FRAME_EPOCH:string;
    TIME_SYSTEM:timingStandard = UTC;
    MEAN_ELEMENT_THEORY:meanElementSource = SGP4;
    COMMENT:string;
    EPOCH:string;
    SEMI_MAJOR_AXIS:double;
    MEAN_MOTION:double;
    ECCENTRICITY:double;
    INCLINATION:double;
    RA_OF_ASC_NODE:double;
    ARG_OF_PERICENTER:double;
    MEAN_ANOMALY:double;
    GM:double;
    MASS:double;
    SOLAR_RAD_AREA:double;
    SOLAR_RAD_COEFF:double;
    DRAG_AREA:double;
    DRAG_COEFF:double;
    EPHEMERIS_TYPE:ephemerisFormat = SGP4;
    CLASSIFICATION_TYPE:string;
    NORAD_CAT_ID:uint32;
    ELEMENT_SET_NO:uint32;
    REV_AT_EPOCH:double;
    BSTAR:double;
    MEAN_MOTION_DOT:double;
    MEAN_MOTION_DDOT:double;
    COV_REFERENCE_FRAME:RFM;
    COVARIANCE:[double];
    USER_DEFINED_BIP_0044_TYPE:uint;
    USER_DEFINED_OBJECT_DESIGNATOR:string;
    USER_DEFINED_EARTH_MODEL:string;
    USER_DEFINED_EPOCH_TIMESTAMP: double;
    USER_DEFINED_MICROSECONDS: double;
  }
  root_type OMM;
  file_identifier "$OMM";
`

// sampleOMMSizePrefixed is a representative $OMM FlatBuffer (OBJECT-A-6292,
// catalog 56775) with its 4-byte size prefix — the same fixture as
// flatsql/test/wasm-generic-extractor.test.ts (which strips the prefix for
// ingestBuffers; we keep it for the stream-ingest path).
const sampleOMMBase64 = "HAEAAEgAAAAkT01NAAAAADwAVAAAAAwACABQAEwAEAAAAAAAAAAAAAAARAAAADwANAAsACQAHAAUAAAAAAAAAAAAAAAAAAAABABIADwAAABQAAAAVAAAAGAAAAB4AAAAxEKtad4BV0DByqFFtsBwQGZmZmZmnGJAXf5D+u1/UUCej3xvHS04P22KKnBw9y1AUAAAAMfdAABkAAAAcAAAAAEAAABVAAAACAAAAFNETi1URVNUAAAAABQAAAAyMDI2LTA1LTExVDEwOjI2OjQxWgAAAAAFAAAARUFSVEgAAAAUAAAAMjAyNi0wNS0xMFQxMDo0NTozMVoAAAAACQAAADIwMjMtMDc4SgAAAA0AAABPQkpFQ1QtQS02MjkyAAAA"

func fixtureStream(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(sampleOMMBase64)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return b
}

func fixtureBuffer(t *testing.T) []byte {
	return fixtureStream(t)[4:]
}

// sharedAOTDir gives every test the same AOT cache so the suite runs at
// native speed; only the first run on a machine pays the ~35 s compile.
func sharedAOTDir(t *testing.T) string {
	t.Helper()
	base, err := os.UserCacheDir()
	if err != nil {
		return t.TempDir()
	}
	return base + "/sdn-flatsqlrt-test-aot"
}

func newTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	var opts []Option
	switch {
	// FLATSQLRT_WASM_FILE points the whole suite at alternate engine bytes
	// (e.g. a candidate artifact) — used to validate byte-parity and
	// performance of candidate builds.
	case os.Getenv("FLATSQLRT_WASM_FILE") != "":
		b, err := os.ReadFile(os.Getenv("FLATSQLRT_WASM_FILE"))
		if err != nil {
			t.Fatalf("FLATSQLRT_WASM_FILE: %v", err)
		}
		opts = append(opts, WithWasmBytes(b))
	// FLATSQLRT_INTERPRET=1 forces interpreted execution (perf comparison).
	case os.Getenv("FLATSQLRT_INTERPRET") != "":
	default:
		opts = append(opts, WithAOTCache(sharedAOTDir(t)))
	}
	rt, err := New(opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(rt.Close)
	return rt
}

func newOMMDatabase(t *testing.T, rt *Runtime, name string) *Database {
	t.Helper()
	db, err := rt.CreateDatabase(ommTestSchema, name)
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	t.Cleanup(db.Destroy)
	if err := db.RegisterFileID("$OMM", "OMM"); err != nil {
		t.Fatalf("RegisterFileID: %v", err)
	}
	return db
}

func TestEmbeddedArtifact(t *testing.T) {
	sum := sha256.Sum256(EmbeddedWasm())
	// Must match the provenance block in README.md.
	const want = "4d17fc5f4936305a005bfc5c63c550a58e448412900299ac7d6adc63ba0137e9"
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("embedded flatsql-wasi-noeh.wasm sha256 = %s, want %s (update README provenance if the pin moved)", got, want)
	}
}

func TestQueryErrorSurfaceWithoutPoisoning(t *testing.T) {
	rt := newTestRuntime(t)
	db := newOMMDatabase(t, rt, "omm-basic")

	// Since the flatsql A.3c no-throw refactor, user-triggerable errors are
	// benign: clean message, no trap, runtime stays healthy and reusable.
	if _, err := db.Query("SELECT * FROM NoSuchTable"); err == nil {
		t.Fatal("expected SQL error, got nil")
	}
	if _, err := db.Query("SELECT * FROM OMM WHERE NORAD_CAT_ID = ?"); err == nil {
		// 1 placeholder, 0 params — param-count pre-check.
		t.Fatal("expected param-count error, got nil")
	}
	if _, err := db.QueryTemplate("no-such-template"); err == nil {
		t.Fatal("expected unknown-template error, got nil")
	}
	if err := db.RegisterSource("dup"); err != nil {
		t.Fatalf("RegisterSource: %v", err)
	}
	if err := db.RegisterSource("dup"); err == nil {
		t.Fatal("expected duplicate-source error, got nil")
	}
	if rt.Poisoned() {
		t.Fatal("runtime poisoned by benign errors — no-throw contract broken")
	}

	// The SAME database keeps working after all of the above.
	if _, err := db.IngestOne(fixtureBuffer(t)); err != nil {
		t.Fatalf("IngestOne after errors: %v", err)
	}
	res, err := db.Query("SELECT OBJECT_NAME FROM OMM WHERE NORAD_CAT_ID = 56775")
	if err != nil {
		t.Fatalf("Query after errors: %v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0][0] != "OBJECT-A-6292" {
		t.Fatalf("post-error query rows: %#v", res.Rows)
	}
	if rt.Poisoned() {
		t.Fatal("runtime unexpectedly poisoned")
	}
}

func TestAOTCache(t *testing.T) {
	// Shared cache: the first run on a machine compiles (~35 s), later runs
	// assert the cache-hit path. Point FLATSQLRT_AOT_FRESH=1 at a fresh
	// TempDir to force a compile.
	dir := sharedAOTDir(t)
	if os.Getenv("FLATSQLRT_AOT_FRESH") != "" {
		dir = t.TempDir()
	}

	rt, err := New(WithAOTCache(dir))
	if err != nil {
		t.Fatalf("New with AOT cache: %v", err)
	}
	defer rt.Close()
	if !rt.AOT() {
		t.Fatal("expected AOT-compiled runtime (compiler unavailable?)")
	}
	// Assert the artifact for the engine bytes UNDER TEST is present, rather
	// than "exactly one file": the shared dev cache legitimately holds an
	// artifact per engine build, so a count assertion breaks on every
	// engine-bytes change instead of catching a real cache miss.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read AOT cache dir: %v", err)
	}
	sum := sha256.Sum256(EmbeddedWasm())
	wantName := "flatsql-" + hex.EncodeToString(sum[:])[:16] + "-we" + RuntimeVersion() + ".aot.wasm"
	found := false
	engineArtifacts := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "flatsql-") {
			engineArtifacts++
		}
		if entry.Name() == wantName {
			found = true
		}
	}
	if !found {
		t.Fatalf("AOT artifact %s missing; cache dir holds %v", wantName, entries)
	}
	if engineArtifacts == 0 {
		t.Fatalf("no engine AOT artifact in %s: %v", dir, entries)
	}

	// Engine works end-to-end on the compiled artifact.
	db, err := rt.CreateDatabase(ommTestSchema, "aot-check")
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	defer db.Destroy()
	if err := db.RegisterFileID("$OMM", "OMM"); err != nil {
		t.Fatalf("RegisterFileID: %v", err)
	}
	if _, err := db.IngestOne(fixtureBuffer(t)); err != nil {
		t.Fatalf("IngestOne: %v", err)
	}
	res, err := db.Query("SELECT NORAD_CAT_ID FROM OMM")
	if err != nil || len(res.Rows) != 1 || res.Rows[0][0] != int64(56775) {
		t.Fatalf("AOT query: rows=%#v err=%v", res, err)
	}

	// Second runtime hits the cache: it must load the SAME artifact path rt
	// did, AND that file must not have been rewritten (mtime unchanged) —
	// i.e. no second compile happened. These are direct reuse assertions,
	// not a directory-entry count: dir is the shared, cross-run dev cache
	// (see the comment above) and can legitimately already hold artifacts
	// from other engine builds or libwasmedge versions, so "exactly 1 file
	// total" is the wrong invariant and is flaky against that legacy content.
	wantPath := rt.Mode().ArtifactPath
	before, err := os.Stat(wantPath)
	if err != nil {
		t.Fatalf("stat artifact before reuse: %v", err)
	}
	rt2, err := New(WithAOTCache(dir))
	if err != nil {
		t.Fatalf("New (cached): %v", err)
	}
	defer rt2.Close()
	if !rt2.AOT() {
		t.Fatal("cached runtime not AOT")
	}
	if got := rt2.Mode().ArtifactPath; got != wantPath {
		t.Fatalf("cache reuse loaded a different artifact: got %s, want %s (cache key moved — no reuse)", got, wantPath)
	}
	after, err := os.Stat(wantPath)
	if err != nil {
		t.Fatalf("stat artifact after reuse: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("cache reuse rewrote the artifact (recompiled instead of reusing): mtime %v -> %v", before.ModTime(), after.ModTime())
	}
}

func TestPrecompiledAOTCacheDoesNotCompileOnMiss(t *testing.T) {
	dir := t.TempDir()
	rt, err := New(WithPrecompiledAOTCache(dir))
	if err != nil {
		t.Fatalf("New with precompiled AOT cache: %v", err)
	}
	defer rt.Close()
	if rt.AOT() {
		t.Fatal("runtime unexpectedly used AOT from an empty precompiled cache")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read cache dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("precompiled cache miss created %d entries, want 0", len(entries))
	}
}

func TestIngestStreamAndQuery(t *testing.T) {
	rt := newTestRuntime(t)
	db := newOMMDatabase(t, rt, "omm-ingest")

	stream := fixtureStream(t)
	n, err := db.Ingest(stream)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if n != len(stream) {
		t.Fatalf("Ingest consumed %d bytes, want %d", n, len(stream))
	}

	res, err := db.Query("SELECT OBJECT_NAME, OBJECT_ID, NORAD_CAT_ID FROM OMM WHERE NORAD_CAT_ID = ?", 56775)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("got %d rows, want 1 (columns=%v)", len(res.Rows), res.Columns)
	}
	row := res.Rows[0]
	if row[0] != "OBJECT-A-6292" || row[1] != "2023-078J" || row[2] != int64(56775) {
		t.Fatalf("unexpected row: %#v", row)
	}
}

func TestIngestOneAndParamTypes(t *testing.T) {
	rt := newTestRuntime(t)
	db := newOMMDatabase(t, rt, "omm-one")

	if _, err := db.IngestOne(fixtureBuffer(t)); err != nil {
		t.Fatalf("IngestOne: %v", err)
	}

	// String + float params through the TLV path.
	res, err := db.Query(
		"SELECT NORAD_CAT_ID FROM OMM WHERE OBJECT_NAME = ? AND MEAN_MOTION > ?",
		"OBJECT-A-6292", 1.0)
	if err != nil {
		t.Fatalf("Query with params: %v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0][0] != int64(56775) {
		t.Fatalf("unexpected result: %#v", res.Rows)
	}
}

func TestQueryRawFlatBufferStream(t *testing.T) {
	rt := newTestRuntime(t)
	db := newOMMDatabase(t, rt, "omm-raw")

	original := fixtureBuffer(t)
	if _, err := db.IngestOne(original); err != nil {
		t.Fatalf("IngestOne: %v", err)
	}

	stream, err := db.QueryRawFlatBufferStream("SELECT _data FROM OMM WHERE NORAD_CAT_ID = ?", 56775)
	if err != nil {
		t.Fatalf("QueryRawFlatBufferStream: %v", err)
	}
	if stream.Rows != 1 || stream.Columns != 1 {
		t.Fatalf("artifact counts rows=%d cols=%d, want 1/1", stream.Rows, stream.Columns)
	}

	frames, err := DecodeSizePrefixedStream(stream.Bytes)
	if err != nil {
		t.Fatalf("DecodeSizePrefixedStream: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1", len(frames))
	}
	if !bytes.Equal(frames[0], original) {
		t.Fatalf("round-tripped FlatBuffer differs: got %d bytes, want %d", len(frames[0]), len(original))
	}
	// Frame header should carry the exact payload length.
	if got := binary.LittleEndian.Uint32(stream.Bytes[:4]); int(got) != len(original) {
		t.Fatalf("frame length prefix %d, want %d", got, len(original))
	}
}

// TestRawStreamResponseCache covers the engine's cached raw-stream response
// artifacts (flatsql cc4885c, loop C.5b): repeated (sql, params) requests are
// served from the cache byte-identically without SQL re-execution; ingest
// invalidates.
func TestRawStreamResponseCache(t *testing.T) {
	rt := newTestRuntime(t)
	db := newOMMDatabase(t, rt, "omm-raw-cache")

	original := fixtureBuffer(t)
	if _, err := db.IngestOne(original); err != nil {
		t.Fatalf("IngestOne: %v", err)
	}

	const sql = "SELECT _data FROM OMM WHERE NORAD_CAT_ID = ?"
	first, err := db.QueryRawFlatBufferStream(sql, 56775)
	if err != nil {
		t.Fatalf("QueryRawFlatBufferStream (cold): %v", err)
	}
	if first.CacheHit {
		t.Fatal("cold raw-stream query reported CacheHit=true")
	}

	second, err := db.QueryRawFlatBufferStream(sql, 56775)
	if err != nil {
		t.Fatalf("QueryRawFlatBufferStream (warm): %v", err)
	}
	if !second.CacheHit {
		t.Fatal("warm raw-stream query was not served from the response cache")
	}
	if !bytes.Equal(first.Bytes, second.Bytes) {
		t.Fatal("cache hit returned different bytes than the cold query")
	}
	if second.Rows != first.Rows || second.Columns != first.Columns {
		t.Fatalf("cache hit counts rows=%d cols=%d, want %d/%d",
			second.Rows, second.Columns, first.Rows, first.Columns)
	}

	// Ingest invalidates: next request re-executes (miss) and sees the data.
	if _, err := db.IngestOne(original); err != nil {
		t.Fatalf("IngestOne (second): %v", err)
	}
	third, err := db.QueryRawFlatBufferStream(sql, 56775)
	if err != nil {
		t.Fatalf("QueryRawFlatBufferStream (post-ingest): %v", err)
	}
	if third.CacheHit {
		t.Fatal("raw-stream cache survived an ingest (stale artifact served)")
	}
	if third.Rows != 2 {
		t.Fatalf("post-ingest rows = %d, want 2", third.Rows)
	}
}

func TestQueryTemplates(t *testing.T) {
	rt := newTestRuntime(t)
	db := newOMMDatabase(t, rt, "omm-tmpl")
	if _, err := db.Ingest(fixtureStream(t)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	if err := db.RegisterQueryTemplate("byNorad",
		"SELECT OBJECT_NAME FROM OMM WHERE NORAD_CAT_ID = ?", true); err != nil {
		t.Fatalf("RegisterQueryTemplate: %v", err)
	}
	res, err := db.QueryTemplate("byNorad", 56775)
	if err != nil {
		t.Fatalf("QueryTemplate: %v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0][0] != "OBJECT-A-6292" {
		t.Fatalf("unexpected template result: %#v", res.Rows)
	}
	if _, err := db.QueryTemplate("nope"); err == nil {
		t.Fatal("expected unknown-template error, got nil")
	}
}

func TestQueryMany(t *testing.T) {
	rt := newTestRuntime(t)
	db := newOMMDatabase(t, rt, "omm-many")
	if _, err := db.Ingest(fixtureStream(t)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	results, err := db.QueryMany([]QueryRequest{
		{SQL: "SELECT COUNT(*) FROM OMM"},
		{SQL: "SELECT OBJECT_ID FROM OMM WHERE NORAD_CAT_ID = ?", Params: []interface{}{56775}},
	})
	if err != nil {
		t.Fatalf("QueryMany: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Rows[0][0] != int64(1) {
		t.Fatalf("count = %#v, want 1", results[0].Rows[0][0])
	}
	if results[1].Rows[0][0] != "2023-078J" {
		t.Fatalf("object id = %#v", results[1].Rows[0][0])
	}
}

func TestExportAndRebuild(t *testing.T) {
	rt := newTestRuntime(t)
	src := newOMMDatabase(t, rt, "omm-export")
	if _, err := src.Ingest(fixtureStream(t)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	snapshot, err := src.ExportData()
	if err != nil {
		t.Fatalf("ExportData: %v", err)
	}
	if len(snapshot) == 0 {
		t.Fatal("empty snapshot")
	}

	dst := newOMMDatabase(t, rt, "omm-rebuild")
	if err := dst.LoadAndRebuild(snapshot); err != nil {
		t.Fatalf("LoadAndRebuild: %v", err)
	}
	res, err := dst.Query("SELECT OBJECT_NAME FROM OMM WHERE NORAD_CAT_ID = 56775")
	if err != nil {
		t.Fatalf("Query after rebuild: %v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0][0] != "OBJECT-A-6292" {
		t.Fatalf("rebuilt store query: %#v", res.Rows)
	}
}

func TestEncodeParamsLayout(t *testing.T) {
	blob, err := EncodeParams([]interface{}{nil, true, int64(-2), 3.5, "ab", []byte{9}})
	if err != nil {
		t.Fatalf("EncodeParams: %v", err)
	}
	want := []byte{
		0, 0, 0, 0, 0, // null, len 0
		1, 1, 0, 0, 0, 1, // bool, len 1, true
		2, 8, 0, 0, 0, 0xfe, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, // int64 -2 LE
		3, 8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x0c, 0x40, // float64 3.5 LE
		4, 2, 0, 0, 0, 'a', 'b', // string "ab"
		5, 1, 0, 0, 0, 9, // bytes {9}
	}
	if !bytes.Equal(blob, want) {
		t.Fatalf("TLV layout mismatch:\n got %x\nwant %x", blob, want)
	}
}
