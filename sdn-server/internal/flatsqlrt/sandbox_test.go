package flatsqlrt

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Sandboxed public query (gateway loop G.5): the Go wrapper over
// flatsql_query_sandboxed. The engine-level injection matrix lives in the
// flatsql repo (test/sandbox-query.test.ts); this suite asserts the Go
// surface — typed errors, stream/JSON readout, byte-parity with the
// unsandboxed raw-stream path, and that rejections do not poison the
// runtime.

func sandboxCaps() SandboxCaps {
	return SandboxCaps{MaxRows: 1000, MaxBytes: 1 << 20, Timeout: 2 * time.Second}
}

func TestSandboxedStreamAndJSON(t *testing.T) {
	rt := newTestRuntime(t)
	db := newOMMDatabase(t, rt, "sandbox-go")
	if _, err := db.IngestOne(fixtureBuffer(t)); err != nil {
		t.Fatalf("IngestOne: %v", err)
	}

	// Stream mode: byte-parity with the raw-stream path.
	stream, err := db.QuerySandboxedStream("SELECT _data FROM OMM", sandboxCaps())
	if err != nil {
		t.Fatalf("QuerySandboxedStream: %v", err)
	}
	reference, err := db.QueryRawFlatBufferStream("SELECT _data FROM OMM")
	if err != nil {
		t.Fatalf("QueryRawFlatBufferStream: %v", err)
	}
	if !bytes.Equal(stream.Bytes, reference.Bytes) {
		t.Fatalf("sandboxed stream differs from raw stream (%d vs %d bytes)",
			len(stream.Bytes), len(reference.Bytes))
	}
	if stream.Rows != 1 || stream.FrameCount != 1 {
		t.Fatalf("rows=%d frames=%d, want 1/1", stream.Rows, stream.FrameCount)
	}

	// JSON mode: bare array, schema-exact column-name keys.
	payload, rows, cols, err := db.QuerySandboxedJSON(
		"SELECT NORAD_CAT_ID, OBJECT_NAME, MEAN_MOTION FROM OMM WHERE NORAD_CAT_ID = ?",
		sandboxCaps(), 56775)
	if err != nil {
		t.Fatalf("QuerySandboxedJSON: %v", err)
	}
	if rows != 1 || cols != 3 {
		t.Fatalf("rows=%d cols=%d, want 1/3", rows, cols)
	}
	var decoded []map[string]interface{}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("json payload: %v (%s)", err, payload)
	}
	if len(decoded) != 1 {
		t.Fatalf("decoded rows = %d, want 1", len(decoded))
	}
	if got := decoded[0]["NORAD_CAT_ID"]; got != float64(56775) {
		t.Fatalf("NORAD_CAT_ID = %v (keys must be schema-exact)", decoded[0])
	}
	if got := decoded[0]["OBJECT_NAME"]; got != "STARLINK-6292" {
		t.Fatalf("OBJECT_NAME = %v", got)
	}
}

func TestSandboxedTypedRejections(t *testing.T) {
	rt := newTestRuntime(t)
	db := newOMMDatabase(t, rt, "sandbox-go-reject")
	if _, err := db.IngestOne(fixtureBuffer(t)); err != nil {
		t.Fatalf("IngestOne: %v", err)
	}
	// A control table like the store's index rows — must be invisible.
	if _, err := db.Query("CREATE TABLE sdn_secret (k TEXT)"); err != nil {
		t.Fatalf("create control table: %v", err)
	}

	cases := []struct {
		name, sql, code string
	}{
		{"write", "DROP TABLE OMM", SandboxCodeNotAuthorized},
		{"pragma", "PRAGMA table_info(OMM)", SandboxCodeNotAuthorized},
		{"attach", "ATTACH DATABASE ':memory:' AS x", SandboxCodeNotAuthorized},
		{"control-table", "SELECT * FROM sdn_secret", SandboxCodeNotAuthorized},
		{"sqlite-master", "SELECT * FROM sqlite_master", SandboxCodeNotAuthorized},
		{"multi", "SELECT 1; SELECT 2", SandboxCodeMultiStatement},
		{"empty", "  -- nothing", SandboxCodeEmptyStatement},
		{"non-blob-stream", "SELECT NORAD_CAT_ID FROM OMM", SandboxCodeNotRecordStream},
		{"params", "SELECT _data FROM OMM WHERE NORAD_CAT_ID = ?", SandboxCodeParams},
	}
	for _, tc := range cases {
		_, err := db.QuerySandboxedStream(tc.sql, sandboxCaps())
		se, ok := AsSandboxError(err)
		if !ok {
			t.Fatalf("%s: want SandboxError, got %v", tc.name, err)
		}
		if se.Code != tc.code {
			t.Fatalf("%s: code = %q (%s), want %q", tc.name, se.Code, se.Message, tc.code)
		}
	}

	// Plain SQL errors are NOT sandbox errors.
	_, err := db.QuerySandboxedStream("SELECT nope FROM OMM", sandboxCaps())
	if err == nil {
		t.Fatal("bad column: want error")
	}
	if _, ok := AsSandboxError(err); ok {
		t.Fatalf("bad column should be a plain SQL error, got sandbox error: %v", err)
	}

	// Runtime unpoisoned and usable after every rejection.
	if rt.Poisoned() {
		t.Fatal("runtime poisoned by sandbox rejections")
	}
	if _, _, _, err := db.QuerySandboxedJSON("SELECT count(*) AS N FROM OMM", sandboxCaps()); err != nil {
		t.Fatalf("query after rejections: %v", err)
	}
}

func TestSandboxedCapsAndTimeout(t *testing.T) {
	rt := newTestRuntime(t)
	db := newOMMDatabase(t, rt, "sandbox-go-caps")
	if _, err := db.IngestOne(fixtureBuffer(t)); err != nil {
		t.Fatalf("IngestOne: %v", err)
	}

	// Row cap.
	_, _, _, err := db.QuerySandboxedJSON(
		"WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 100) SELECT x FROM c",
		SandboxCaps{MaxRows: 10, Timeout: 2 * time.Second})
	if se, ok := AsSandboxError(err); !ok || se.Code != SandboxCodeRowCap {
		t.Fatalf("row cap: got %v", err)
	}

	// Byte cap.
	_, err = db.QuerySandboxedStream("SELECT _data FROM OMM",
		SandboxCaps{MaxRows: 10, MaxBytes: 16, Timeout: 2 * time.Second})
	if se, ok := AsSandboxError(err); !ok || se.Code != SandboxCodeByteCap {
		t.Fatalf("byte cap: got %v", err)
	}

	// Runaway statement: bounded by the in-engine deadline.
	started := time.Now()
	_, _, _, err = db.QuerySandboxedJSON(
		"WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c) SELECT count(*) FROM c",
		SandboxCaps{Timeout: 300 * time.Millisecond})
	elapsed := time.Since(started)
	se, ok := AsSandboxError(err)
	if !ok || se.Code != SandboxCodeTimeout {
		t.Fatalf("timeout: got %v", err)
	}
	if !strings.Contains(se.Message, "300 ms") {
		t.Fatalf("timeout message should carry the deadline: %s", se.Message)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("runaway query took %s — deadline not enforced", elapsed)
	}
	if rt.Poisoned() {
		t.Fatal("timeout poisoned the runtime")
	}
	// Engine still healthy.
	if _, err := db.QuerySandboxedStream("SELECT _data FROM OMM", sandboxCaps()); err != nil {
		t.Fatalf("query after timeout: %v", err)
	}
}
