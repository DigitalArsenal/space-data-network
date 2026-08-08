package caps

// ADMIT-POINT PARITY: a size-prefixed SDS shard is not a record, in EITHER
// runtime.
//
// Found while ruling on the browser side of the same bug
// (sdn-js-flatsql-storage-stream-surface). sdn-js accepted a whole
// 32,141-frame $OMM shard as one record because its file-identifier check had
// no length constraint; measured, that produced 0 queryable rows and doubled
// IndexedDB. The Go store's engineRecordPayload
// (internal/storage/engine_records.go:137) never had that hole — it strips a
// size prefix only when `uint32(data[:4])+4 == len(data)` — but it is a
// STRIPPER, not a gate: handed a multi-frame shard it returns the buffer
// UNCHANGED and IngestOneWithSource receives the whole stream unsplit.
//
// Every other Go entry point either validates (api/publish.go RouteBuffer +
// VerifyEnvelope, protocol/sds_exchange.go) or demuxes (handlePublishBatch,
// channels.SplitNativeStreamFramesForChannel, storage.ingest_with_source's
// splitSizePrefixedStream) before reaching Store. The `storage.write` module
// capability did neither: it base64-decoded and called Store directly, so a
// module holding storage_write could hand the host a whole shard. These tests
// pin the refusal, and pin that single records still go through untouched.

import (
	"encoding/base64"
	"strings"
	"testing"
)

// TestStorageWriteRefusesMultiFrameShard is the regression: the shard shape is
// refused at the admit point and the refusal NAMES the capability that takes
// it, rather than silently storing one malformed row.
func TestStorageWriteRefusesMultiFrameShard(t *testing.T) {
	handler := newStorageHandlerWithGrants(t, "storage_write")

	records := [][]byte{
		buildIngestTestOMM(t, 40001, 1700000000),
		buildIngestTestOMM(t, 40002, 1700000060),
		buildIngestTestOMM(t, 40003, 1700000120),
	}
	shard := sizePrefixedStream(records)

	// The shard LOOKS like a record to a naive identifier check: the first
	// frame's u32 prefix puts the file identifier at bytes 8..11. That is
	// exactly the false positive that cost the browser lane 2x storage.
	if len(shard) < 12 {
		t.Fatalf("shard fixture too small: %d bytes", len(shard))
	}

	ok, envelope := storageOpOK(t, handler, "storage.write", map[string]interface{}{
		"schema": "OMM.fbs",
		"data":   base64.StdEncoding.EncodeToString(shard),
	})
	if ok {
		t.Fatal("storage.write ACCEPTED a multi-frame shard — the single-record admit point stored an unsplit stream")
	}
	errMsg, _ := envelope["error"].(map[string]interface{})["message"].(string)
	if !strings.Contains(errMsg, "ONE record") {
		t.Fatalf("refusal did not say storage.write admits one record: %q", errMsg)
	}
	if !strings.Contains(errMsg, "storage.ingest_with_source") {
		t.Fatalf("refusal did not name the capability that takes a stream: %q", errMsg)
	}
	if !strings.Contains(errMsg, "3 frames") {
		t.Fatalf("refusal did not report the frame count it detected: %q", errMsg)
	}
}

// TestStorageWriteStillAcceptsOneRecord pins the other side: the guard must
// not turn a legitimate single-record write into a refusal. Both admissible
// shapes are exercised — bare, and size-prefixed with the prefix accounting
// for the whole buffer (what engineRecordPayload strips).
func TestStorageWriteStillAcceptsOneRecord(t *testing.T) {
	bare := buildIngestTestOMM(t, 40010, 1700000000)

	if ok, envelope := storageOpOK(t, newStorageHandlerWithGrants(t, "storage_write"), "storage.write", map[string]interface{}{
		"schema": "OMM.fbs",
		"data":   base64.StdEncoding.EncodeToString(bare),
	}); !ok {
		t.Fatalf("storage.write refused a bare single record: %v", envelope)
	}

	single := sizePrefixedStream([][]byte{buildIngestTestOMM(t, 40011, 1700000060)})
	if frames, isStream := multiFrameSizePrefixedStream(single); isStream {
		t.Fatalf("a ONE-frame stream must not be classified as a shard (got %d frames)", frames)
	}
	if ok, envelope := storageOpOK(t, newStorageHandlerWithGrants(t, "storage_write"), "storage.write", map[string]interface{}{
		"schema": "OMM.fbs",
		"data":   base64.StdEncoding.EncodeToString(single),
	}); !ok {
		t.Fatalf("storage.write refused a single size-prefixed record: %v", envelope)
	}
}

// TestMultiFrameSizePrefixedStreamIsStrict pins the classifier itself. It
// gates a refusal, so a false positive would break a legitimate write: the
// frame lengths must tile the buffer EXACTLY and every frame must carry a
// printable file identifier.
func TestMultiFrameSizePrefixedStreamIsStrict(t *testing.T) {
	records := [][]byte{
		buildIngestTestOMM(t, 40020, 1700000000),
		buildIngestTestOMM(t, 40021, 1700000060),
	}
	shard := sizePrefixedStream(records)

	if frames, isStream := multiFrameSizePrefixedStream(shard); !isStream || frames != 2 {
		t.Fatalf("a real 2-frame shard must classify as a stream: frames=%d isStream=%v", frames, isStream)
	}

	cases := map[string][]byte{
		"empty":            {},
		"short":            []byte("short"),
		"opaque text":      []byte("a perfectly ordinary opaque record payload with no framing"),
		"bare record":      buildIngestTestOMM(t, 40022, 1700000120),
		"truncated shard":  shard[:len(shard)-3],
		"trailing garbage": append(append([]byte{}, shard...), 0x01, 0x02, 0x03),
	}
	for name, payload := range cases {
		if frames, isStream := multiFrameSizePrefixedStream(payload); isStream {
			t.Fatalf("%s must NOT classify as a shard (frames=%d)", name, frames)
		}
	}

	// One frame with a non-printable identifier disqualifies the whole
	// payload: partial tiling is not evidence of a shard.
	poisoned := append([]byte{}, shard...)
	poisoned[8] = 0x00
	if _, isStream := multiFrameSizePrefixedStream(poisoned); isStream {
		t.Fatal("a payload whose first frame lacks a printable file identifier must not classify as a shard")
	}
}
