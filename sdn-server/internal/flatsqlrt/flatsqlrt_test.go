package flatsqlrt

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
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

// starlink6292SizePrefixed is a real $OMM FlatBuffer (STARLINK-6292, NORAD
// 56775) WITH its 4-byte size prefix — the same fixture as
// flatsql/test/wasm-generic-extractor.test.ts (which strips the prefix for
// ingestBuffers; we keep it for the stream-ingest path).
const starlink6292Base64 = "HAEAAEgAAAAkT01NAAAAADwAVAAAAAwACABQAEwAEAAAAAAAAAAAAAAARAAAADwANAAsACQAHAAUAAAAAAAAAAAAAAAAAAAABABIADwAAABQAAAAVAAAAGAAAAB4AAAAxEKtad4BV0DByqFFtsBwQGZmZmZmnGJAXf5D+u1/UUCej3xvHS04P22KKnBw9y1AUAAAAMfdAABkAAAAcAAAAAEAAABVAAAACAAAAFNETi1URVNUAAAAABQAAAAyMDI2LTA1LTExVDEwOjI2OjQxWgAAAAAFAAAARUFSVEgAAAAUAAAAMjAyNi0wNS0xMFQxMDo0NTozMVoAAAAACQAAADIwMjMtMDc4SgAAAA0AAABTVEFSTElOSy02MjkyAAAA"

func fixtureStream(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(starlink6292Base64)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return b
}

func fixtureBuffer(t *testing.T) []byte {
	return fixtureStream(t)[4:]
}

func newTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	rt, err := New()
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
	// Must match the provenance block in README.md (flatsql pin 0c76d87).
	const want = "3b28fd9cefe376c0fe10e9fb41f280ece36d50b93ab4f482208db2d27cc18cf6"
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("embedded flatsql-wasi.wasm sha256 = %s, want %s (update README provenance if the pin moved)", got, want)
	}
}

func TestQueryErrorSurface(t *testing.T) {
	rt := newTestRuntime(t)
	db := newOMMDatabase(t, rt, "omm-basic")
	if _, err := db.Query("SELECT * FROM NoSuchTable"); err == nil {
		t.Fatal("expected SQL error, got nil")
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
	if row[0] != "STARLINK-6292" || row[1] != "2023-078J" || row[2] != int64(56775) {
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
		"STARLINK-6292", 1.0)
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
	if len(res.Rows) != 1 || res.Rows[0][0] != "STARLINK-6292" {
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
	if len(res.Rows) != 1 || res.Rows[0][0] != "STARLINK-6292" {
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
