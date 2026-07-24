package flowrt

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/ipfs/kubo/sdn/flatsqlrt"
)

func neutralLinkedStoreDescriptor() *LinkedStoreDescriptor {
	return &LinkedStoreDescriptor{
		Version:  1,
		Engine:   "flatsql",
		Database: "fixture_records",
		Schema:   "table fixture_records { key:string (key); label:string; data:[ubyte]; }",
		FileIdentifiers: []LinkedStoreFileIdentifier{{
			ID: "TREC", Table: "fixture_records",
		}},
	}
}

func neutralStoreRow(key, label string, data []byte) []byte {
	builder := flatbuffers.NewBuilder(128 + len(data))
	keyOffset := builder.CreateString(key)
	labelOffset := builder.CreateString(label)
	dataOffset := builder.CreateByteVector(data)
	builder.StartObject(3)
	builder.PrependUOffsetTSlot(0, keyOffset, 0)
	builder.PrependUOffsetTSlot(1, labelOffset, 0)
	builder.PrependUOffsetTSlot(2, dataOffset, 0)
	root := builder.EndObject()
	builder.FinishWithFileIdentifier(root, []byte("TREC"))
	return builder.FinishedBytes()
}

func openNeutralLinkedStore(t *testing.T) *LinkedStore {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenLinkedStore(filepath.Join(dir, "aot"), filepath.Join(dir, "store.snapshot"), neutralLinkedStoreDescriptor())
	if err != nil {
		t.Fatalf("OpenLinkedStore: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

func TestLinkedStoreQueryReadOnly(t *testing.T) {
	store := openNeutralLinkedStore(t)
	payload := []byte("opaque-record")
	if seq := store.ingestRecord(neutralStoreRow("key-1", "group-a", payload)); seq < 0 {
		t.Fatalf("ingestRecord failed rc=%d", seq)
	}

	result, err := store.Query("SELECT key, label, data FROM fixture_records WHERE key = ?", "key-1")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(result.Rows) != 1 || result.Rows[0][0] != "key-1" || result.Rows[0][1] != "group-a" {
		t.Fatalf("Query rows: %#v", result.Rows)
	}
	if blob, ok := result.Rows[0][2].([]byte); !ok || !bytes.Equal(blob, payload) {
		t.Fatalf("Query data BLOB mismatch: %#v", result.Rows[0][2])
	}
}

func TestLinkedStoreQueryRecordStream(t *testing.T) {
	store := openNeutralLinkedStore(t)
	first := []byte("first")
	second := []byte("second")
	if seq := store.ingestRecord(neutralStoreRow("key-1", "group-a", first)); seq < 0 {
		t.Fatalf("ingest first row rc=%d", seq)
	}
	if seq := store.ingestRecord(neutralStoreRow("key-2", "group-b", second)); seq < 0 {
		t.Fatalf("ingest second row rc=%d", seq)
	}

	const sql = "SELECT data FROM fixture_records WHERE rowid > ? AND rowid <= ? ORDER BY rowid"
	caps := flatsqlrt.SandboxCaps{MaxRows: 1, MaxBytes: 1 << 20, Timeout: time.Second}
	stream, err := store.QueryRecordStream(sql, caps, int64(1), int64(2))
	if err != nil {
		t.Fatalf("QueryRecordStream: %v", err)
	}
	frames, err := flatsqlrt.DecodeSizePrefixedStream(stream.Bytes)
	if err != nil {
		t.Fatalf("DecodeSizePrefixedStream: %v", err)
	}
	if stream.Rows != 1 || stream.Columns != 1 || len(frames) != 1 || !bytes.Equal(frames[0], second) {
		t.Fatalf("unexpected stream: rows=%d columns=%d frames=%#v", stream.Rows, stream.Columns, frames)
	}
	if stream.CacheHit || stream.MirrorHit {
		t.Fatalf("record stream must be uncached: cache=%v mirror=%v", stream.CacheHit, stream.MirrorHit)
	}
	if _, err := store.QueryRecordStream(sql, flatsqlrt.SandboxCaps{}, int64(1), int64(2)); err == nil {
		t.Fatal("QueryRecordStream accepted unlimited sandbox caps")
	}
}

func TestLinkedStoreQueryRejectsNonSelect(t *testing.T) {
	store := openNeutralLinkedStore(t)
	for _, sql := range []string{
		"DELETE FROM fixture_records",
		"INSERT INTO fixture_records (key) VALUES ('x')",
		"SELECT * FROM fixture_records; DROP TABLE fixture_records",
		"  ",
	} {
		if _, err := store.Query(sql); err == nil {
			t.Fatalf("Query(%q) should have been rejected", sql)
		}
	}
	if _, err := store.Query("  select count(*) from fixture_records ;  "); err != nil {
		t.Fatalf("padded SELECT should be accepted: %v", err)
	}
}

func TestIsReadOnlySelectHelper(t *testing.T) {
	valid := []string{"SELECT 1", "select * from fixture_records", "SELECT 1;"}
	invalid := []string{"", "INSERT INTO x VALUES (1)", "SELECT 1; DELETE FROM x"}
	for _, sql := range valid {
		if !isReadOnlySelect(sql) {
			t.Errorf("isReadOnlySelect(%q) = false", sql)
		}
	}
	for _, sql := range invalid {
		if isReadOnlySelect(sql) {
			t.Errorf("isReadOnlySelect(%q) = true", sql)
		}
	}
}
