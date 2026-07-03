package caps

import (
	"encoding/binary"
	"encoding/json"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// The engine-native storage ops (loop C.1) let a module pull aligned
// FlatBuffer streams and cache keys straight from the FlatSQL engine.

func newCapTestStore(t *testing.T) *storage.FlatSQLStore {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	store, err := storage.NewFlatSQLStore(t.TempDir(), validator)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	tags := storage.SourceTags{ProviderID: "prov", SourceName: "celestrak-gp", BatchID: "b1"}
	for _, spec := range []struct {
		norad uint32
		epoch string
	}{
		{30001, "2026-05-10T00:00:00Z"},
		{30001, "2026-05-12T00:00:00Z"},
		{30002, "2026-05-11T00:00:00Z"},
	} {
		omm := sds.NewOMMBuilder().
			WithNoradCatID(spec.norad).
			WithObjectName("CAPSAT").
			WithEpoch(spec.epoch).
			WithEpochTimestamp(float64(mustUnix(t, spec.epoch))).
			Build()
		if _, err := store.StoreWithSourceTags("OMM.fbs", omm, "peer-cap", nil, tags); err != nil {
			t.Fatalf("store: %v", err)
		}
	}
	return store
}

func mustUnix(t *testing.T, iso string) int64 {
	t.Helper()
	ts, err := time.Parse("2006-01-02T15:04:05Z", iso)
	if err != nil {
		t.Fatalf("parse %s: %v", iso, err)
	}
	return ts.Unix()
}

// decodeCapResponse decodes a CapHandler response: either plain JSON or the
// PRE-ENCODED hostcall envelope (loop C.5 — magic \x00SDNENV1 + [u32 metaLen]
// [meta JSON][u32 segCount]([u32 segLen][segment])...), the exact bytes
// encodeHostcallJSONResponse forwards to the guest.
func decodeCapResponse(t *testing.T, resp []byte) (map[string]interface{}, [][]byte) {
	t.Helper()
	magic := []byte{0x00, 'S', 'D', 'N', 'E', 'N', 'V', '1'}
	if len(resp) > len(magic) && string(resp[:len(magic)]) == string(magic) {
		raw := resp[len(magic):]
		if len(raw) < 8 {
			t.Fatalf("pre-encoded envelope truncated (%d bytes)", len(raw))
		}
		off := 0
		metaLen := int(binary.LittleEndian.Uint32(raw[off:]))
		off += 4
		var meta map[string]interface{}
		if err := json.Unmarshal(raw[off:off+metaLen], &meta); err != nil {
			t.Fatalf("pre-encoded envelope meta not JSON: %v", err)
		}
		off += metaLen
		segCount := int(binary.LittleEndian.Uint32(raw[off:]))
		off += 4
		segments := make([][]byte, 0, segCount)
		for i := 0; i < segCount; i++ {
			n := int(binary.LittleEndian.Uint32(raw[off:]))
			off += 4
			segments = append(segments, raw[off:off+n])
			off += n
		}
		if off != len(raw) {
			t.Fatalf("pre-encoded envelope has %d trailing bytes", len(raw)-off)
		}
		return meta, segments
	}
	var envelope map[string]interface{}
	if err := json.Unmarshal(resp, &envelope); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	return envelope, nil
}

func callOp(t *testing.T, store *storage.FlatSQLStore, op string, payload map[string]interface{}) (map[string]interface{}, [][]byte) {
	t.Helper()
	handler := NewStorageCapFactory(store)(nil)
	body, _ := json.Marshal(payload)
	resp, err := handler(op, body)
	if err != nil {
		t.Fatalf("%s handler error: %v", op, err)
	}
	envelope, segments := decodeCapResponse(t, resp)
	if ok, _ := envelope["ok"].(bool); !ok {
		t.Fatalf("%s returned error: %v", op, envelope["error"])
	}
	result, _ := envelope["result"].(map[string]interface{})
	return result, segments
}

// decodeStreamFrames splits the aligned size-prefixed stream carried by the
// result's binary segment ({"$bin":0} in the pre-encoded envelope).
func decodeStreamFrames(t *testing.T, result map[string]interface{}, segments [][]byte) [][]byte {
	t.Helper()
	streamObj, _ := result["stream"].(map[string]interface{})
	ref, ok := streamObj["$bin"].(float64)
	if !ok || int(ref) >= len(segments) {
		t.Fatalf("stream missing $bin segment ref: %v (segments=%d)", result, len(segments))
	}
	raw := segments[int(ref)]
	var frames [][]byte
	off := 0
	for off < len(raw) {
		n := int(binary.LittleEndian.Uint32(raw[off : off+4]))
		off += 4
		frames = append(frames, raw[off:off+n])
		off += n
	}
	return frames
}

func TestFlatSQLQueryStreamOp(t *testing.T) {
	store := newCapTestStore(t)
	result, segments := callOp(t, store, "storage.flatsql_query_stream", map[string]interface{}{
		"sql":    `SELECT _data FROM "OMM@celestrak-gp" WHERE NORAD_CAT_ID = ?`,
		"params": []map[string]interface{}{{"t": "i64", "v": 30001}},
	})
	frames := decodeStreamFrames(t, result, segments)
	if len(frames) != 2 || result["rows"].(float64) != 2 {
		t.Fatalf("got %d frames rows=%v, want 2", len(frames), result["rows"])
	}
}

func TestFlatSQLEpochStreamOp(t *testing.T) {
	store := newCapTestStore(t)
	result, segments := callOp(t, store, "storage.flatsql_epoch_stream", map[string]interface{}{
		"schema":  "OMM.fbs",
		"source":  "celestrak-gp",
		"profile": "nearest",
		"epoch":   float64(mustUnix(t, "2026-05-11T12:00:00Z")),
		"limit":   1000,
	})
	frames := decodeStreamFrames(t, result, segments)
	if len(frames) != 2 { // one nearest record per object
		t.Fatalf("nearest epoch frames = %d, want 2", len(frames))
	}
}

func TestFlatSQLCacheKeyOp(t *testing.T) {
	store := newCapTestStore(t)
	handler := NewStorageCapFactory(store)(nil)
	payload := map[string]interface{}{
		"schema_name":    "OMM",
		"schema_version": "1",
		"sql":            "SELECT _data FROM OMM WHERE NORAD_CAT_ID = ?",
		"format":         "raw",
		"params":         []map[string]interface{}{{"t": "i64", "v": 30001}},
	}
	body, _ := json.Marshal(payload)
	resp, err := handler("storage.flatsql_cache_key", body)
	if err != nil {
		t.Fatalf("cache key: %v", err)
	}
	var envelope struct {
		OK     bool              `json:"ok"`
		Result map[string]string `json:"result"`
	}
	if err := json.Unmarshal(resp, &envelope); err != nil || !envelope.OK {
		t.Fatalf("cache key response: %s err=%v", resp, err)
	}
	key := envelope.Result["key"]
	if key == "" || key[:len("flatsql:response:v1")] != "flatsql:response:v1" {
		t.Fatalf("unexpected cache key: %q", key)
	}
	// Deterministic: same request, same key.
	resp2, _ := handler("storage.flatsql_cache_key", body)
	if string(resp2) != string(resp) {
		t.Fatal("cache key not deterministic")
	}
}

func TestFlatSQLQueryStreamOpErrors(t *testing.T) {
	store := newCapTestStore(t)
	handler := NewStorageCapFactory(store)(nil)
	body, _ := json.Marshal(map[string]interface{}{"sql": "SELECT _data FROM Nope"})
	resp, err := handler("storage.flatsql_query_stream", body)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	envelope, _ := decodeCapResponse(t, resp)
	if ok, _ := envelope["ok"].(bool); ok {
		t.Fatal("expected error envelope for bad SQL")
	}
	// Store remains healthy (A.3c no-poison contract through the cap).
	result, segments := callOp(t, store, "storage.flatsql_query_stream", map[string]interface{}{
		"sql": `SELECT _data FROM "OMM@celestrak-gp"`,
	})
	if len(decodeStreamFrames(t, result, segments)) != 3 {
		t.Fatal("store unhealthy after bad SQL through cap")
	}
}
