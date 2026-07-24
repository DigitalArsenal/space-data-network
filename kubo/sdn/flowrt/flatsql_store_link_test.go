//go:build linux

package flowrt

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

type envParam struct {
	tag  byte
	data []byte
}

func buildEnvelope(sql string, params ...envParam) []byte {
	var out []byte
	appendU32 := func(value uint32) {
		var encoded [4]byte
		binary.LittleEndian.PutUint32(encoded[:], value)
		out = append(out, encoded[:]...)
	}
	appendU32(uint32(len(sql)))
	out = append(out, sql...)
	appendU32(uint32(len(params)))
	for _, parameter := range params {
		out = append(out, parameter.tag)
		appendU32(uint32(len(parameter.data)))
		out = append(out, parameter.data...)
	}
	return out
}

func TestLinkedStoreExecEnvelope(t *testing.T) {
	if os.Getenv("SDN_FLATSQL_STORE_TEST") == "" {
		t.Skip("set SDN_FLATSQL_STORE_TEST=1 (requires FlatSQL engine WASM and WasmEdge)")
	}
	dir := t.TempDir()
	snapshot := filepath.Join(dir, "store.snapshot")
	descriptor := neutralLinkedStoreDescriptor()
	store, err := OpenLinkedStore(filepath.Join(dir, "aot"), snapshot, descriptor)
	if err != nil {
		t.Fatalf("OpenLinkedStore: %v", err)
	}

	exists := func(candidate string) bool {
		envelope := buildEnvelope(
			"SELECT COUNT(*) FROM fixture_records WHERE key=?",
			envParam{tag: paramTagText, data: []byte(candidate)},
		)
		return store.execEnvelope(envelope) > 0
	}
	put := func(key, label string, data []byte) {
		if exists(key) {
			return
		}
		if seq := store.ingestRecord(neutralStoreRow(key, label, data)); seq < 0 {
			t.Fatalf("ingestRecord %q failed rc=%d", key, seq)
		}
	}

	first := []byte("opaque-first")
	put("key-a", "group-a", first)
	put("key-b", "group-b", []byte("opaque-second"))
	put("key-a", "duplicate", []byte("must-not-replace"))
	if !exists("key-a") || exists("missing") {
		t.Fatal("dedup existence query returned an unexpected result")
	}

	result, err := store.db.Query("SELECT COUNT(*) FROM fixture_records")
	if err != nil || result.Rows[0][0].(int64) != 2 {
		t.Fatalf("live row count: result=%#v err=%v", result, err)
	}
	if err := store.Snapshot(); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	info, err := os.Stat(snapshot)
	if err != nil || info.Size() == 0 {
		t.Fatalf("snapshot missing or empty: info=%v err=%v", info, err)
	}
	store.Close()

	reloaded, err := OpenLinkedStore(filepath.Join(dir, "aot"), snapshot, descriptor)
	if err != nil {
		t.Fatalf("OpenLinkedStore reload: %v", err)
	}
	defer reloaded.Close()
	result, err = reloaded.db.Query("SELECT key, label, data FROM fixture_records ORDER BY key")
	if err != nil {
		t.Fatalf("reload query: %v", err)
	}
	if len(result.Rows) != 2 || result.Rows[0][0] != "key-a" || result.Rows[1][0] != "key-b" {
		t.Fatalf("reload rows: %#v", result.Rows)
	}
	if data, ok := result.Rows[0][2].([]byte); !ok || string(data) != string(first) {
		t.Fatalf("reload opaque data: %#v", result.Rows[0][2])
	}
}
